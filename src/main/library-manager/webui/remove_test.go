package webui

import (
	"maps"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/nkreimeyer/library-manager/internal/testutil"
	"github.com/nkreimeyer/library-manager/organize"
)

func removeLibrary(t *testing.T) (lib, out string) {
	t.Helper()
	base := t.TempDir()
	lib, out = filepath.Join(base, "library"), filepath.Join(base, "out")
	writeSong(t, filepath.Join(lib, "A", "ABBA - SOS"), "ABBA", "SOS", nil)
	writeSong(t, filepath.Join(lib, "A", "ABBA - Waterloo"), "ABBA", "Waterloo", nil)
	writeSong(t, filepath.Join(lib, "Q", "Queen - Rock"), "Queen", "Rock", nil)
	return lib, out
}

func TestRemoveValidate(t *testing.T) {
	s := newTestServer(t)
	lib, out := removeLibrary(t)
	var cv configView
	if got := s.do("POST", "/api/remove/validate", removeRequest{Library: lib, Out: out}, &cv); got != http.StatusOK || cv.Library != lib {
		t.Fatalf("validate: %d %+v", got, cv)
	}
	var e apiError
	for _, q := range []removeRequest{
		{Library: filepath.Join(lib, "missing")},
		{Library: lib, Patterns: "song: no dash here"},
	} {
		if got := s.do("POST", "/api/remove/validate", q, &e); got != http.StatusBadRequest || e.Error == "" {
			t.Errorf("%+v: %d %q", q, got, e.Error)
		}
	}
	if got := s.do("POST", "/api/remove/analyse", removeRequest{Library: lib, Out: out, Patterns: "# nothing\n\n"}, &e); got != http.StatusBadRequest {
		t.Errorf("no patterns: %d", got)
	}
}

func TestRemoveFlow(t *testing.T) {
	s := newTestServer(t)
	lib, out := removeLibrary(t)
	before := testutil.Tree(t, lib)
	q := removeRequest{Library: lib, Out: out, Patterns: "ABBA\nartist: Nobody\n"}

	var j Job
	if got := s.do("POST", "/api/remove/analyse", q, &j); got != http.StatusAccepted {
		t.Fatalf("analyse: %d", got)
	}
	j = s.wait()
	if j.State != stateDone || !j.DryRun {
		t.Fatalf("analyse job: %+v", j)
	}
	if !maps.Equal(testutil.Tree(t, lib), before) {
		t.Fatal("the check changed the library")
	}
	id := j.Result.(map[string]any)["analysis"].(string)
	var v struct {
		ID     string `json:"id"`
		Report struct {
			Removed   []struct{ Song string } `json:"removed"`
			Unmatched []string                `json:"unmatched"`
		} `json:"report"`
		Applied bool `json:"applied"`
	}
	if got := s.do("GET", "/api/remove/analysis?id="+id, nil, &v); got != http.StatusOK {
		t.Fatalf("analysis: %d", got)
	}
	if len(v.Report.Removed) != 2 || len(v.Report.Unmatched) != 1 || v.Applied {
		t.Fatalf("view = %+v", v)
	}

	if got := s.do("POST", "/api/remove/apply", map[string]any{"analysis": id}, &j); got != http.StatusAccepted {
		t.Fatalf("apply: %d", got)
	}
	j = s.wait()
	if j.State != stateDone || j.DryRun || j.Stats.Moved != 2 || j.Stats.Failed != 0 {
		t.Fatalf("apply job: %+v", j)
	}
	if _, err := os.Stat(filepath.Join(out, "removed", "A", "ABBA - SOS", "song.txt")); err != nil {
		t.Errorf("not removed: %v", err)
	}
	if _, err := os.Stat(filepath.Join(lib, "Q", "Queen - Rock", "song.txt")); err != nil {
		t.Errorf("Queen was touched: %v", err)
	}
	var e apiError
	if got := s.do("POST", "/api/remove/apply", map[string]any{"analysis": id}, &e); got != http.StatusConflict {
		t.Errorf("second apply: %d", got)
	}

	// The shared undo puts the songs back.
	if got := s.do("POST", "/api/organize/undo", folderRequest{Library: lib, Out: out}, &j); got != http.StatusAccepted {
		t.Fatalf("undo: %d", got)
	}
	if j = s.wait(); j.State != stateDone || j.Stats.Moved != 2 {
		t.Fatalf("undo job: %+v", j)
	}
	if !maps.Equal(testutil.Tree(t, lib), before) {
		t.Error("the library is not as before the undo")
	}
}

func TestRemoveApplyPlanChanged(t *testing.T) {
	s := newTestServer(t)
	lib, out := removeLibrary(t)
	q := removeRequest{Library: lib, Out: out, Patterns: "ABBA"}
	var j Job
	s.do("POST", "/api/remove/analyse", q, &j)
	j = s.wait()
	id := j.Result.(map[string]any)["analysis"].(string)
	// A third ABBA song appears after the check.
	writeSong(t, filepath.Join(lib, "A", "ABBA - Mamma Mia"), "ABBA", "Mamma Mia", nil)
	s.do("POST", "/api/remove/apply", map[string]any{"analysis": id}, &j)
	j = s.wait()
	if j.State != stateFailed || j.Stats.Moved != 0 {
		t.Fatalf("apply job: %+v", j)
	}
	if res, _ := j.Result.(map[string]any); res["planChanged"] != true {
		t.Errorf("result = %v", j.Result)
	}
	if _, err := os.Stat(filepath.Join(lib, "A", "ABBA - SOS", "song.txt")); err != nil {
		t.Errorf("a song was moved: %v", err)
	}
	// The analysis now holds the new plan: applying it removes all three.
	s.do("POST", "/api/remove/apply", map[string]any{"analysis": id}, &j)
	if j = s.wait(); j.State != stateDone || j.Stats.Moved != 3 {
		t.Fatalf("second apply: %+v", j)
	}
}

// A remove stopped during the moves ends "cancelled" after the current move, and the
// shared undo puts that song back.
func TestRemoveApplyStopped(t *testing.T) {
	s := newTestServer(t)
	lib, out := removeLibrary(t)
	before := testutil.Tree(t, lib)
	var j Job
	s.do("POST", "/api/remove/analyse", removeRequest{Library: lib, Out: out, Patterns: "ABBA"}, &j)
	j = s.wait()
	id := j.Result.(map[string]any)["analysis"].(string)

	removeProgressHook = func(phase string, done int) {
		if phase == organize.PhaseMove && done == 1 {
			s.srv.jobs.cancelRunning()
		}
	}
	t.Cleanup(func() { removeProgressHook = nil })
	s.do("POST", "/api/remove/apply", map[string]any{"analysis": id}, &j)
	if j = s.wait(); j.State != stateCancelled || j.Stats.Moved != 1 || j.Stats.Failed != 0 {
		t.Fatalf("stopped apply: %+v", j)
	}
	if _, err := os.Stat(filepath.Join(lib, "A", "ABBA - Waterloo", "song.txt")); err != nil {
		t.Errorf("the song after the stop was moved: %v", err)
	}
	if !strings.Contains(testutil.ReadFile(t, filepath.Join(out, removeReportFile)), `"outcome": "skipped"`) {
		t.Error("the report does not list the skipped move")
	}
	s.do("POST", "/api/organize/undo", folderRequest{Library: lib, Out: out}, &j)
	if j = s.wait(); j.State != stateDone || j.Stats.Moved != 1 {
		t.Fatalf("undo job: %+v", j)
	}
	if !maps.Equal(testutil.Tree(t, lib), before) {
		t.Error("the undo did not restore the library")
	}
}
