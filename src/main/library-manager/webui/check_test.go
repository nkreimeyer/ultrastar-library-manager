package webui

import (
	"maps"
	"net/http"
	"os"
	"path/filepath"
	"testing"

	"github.com/nkreimeyer/library-manager/internal/testutil"
)

// checkLibrary has two working songs and one without its audio file (an error).
func checkLibrary(t *testing.T) (lib, out string) {
	t.Helper()
	base := t.TempDir()
	lib, out = filepath.Join(base, "library"), filepath.Join(base, "out")
	writeSong(t, filepath.Join(lib, "A", "ABBA - SOS"), "ABBA", "SOS", nil)
	writeSong(t, filepath.Join(lib, "Q", "Queen - Rock"), "Queen", "Rock", nil)
	writeSong(t, filepath.Join(lib, "Q", "Queen - Broken"), "Queen", "Broken", nil)
	if err := os.Remove(filepath.Join(lib, "Q", "Queen - Broken", "song.mp3")); err != nil {
		t.Fatal(err)
	}
	return lib, out
}

func TestCheckValidate(t *testing.T) {
	s := newTestServer(t)
	lib, out := checkLibrary(t)
	var cv configView
	if got := s.do("POST", "/api/check/validate", checkRequest{Library: lib}, &cv); got != http.StatusOK ||
		cv.Library != lib || cv.OutDir != filepath.Join(filepath.Dir(lib), "organize-output") {
		t.Fatalf("validate: %d %+v", got, cv)
	}
	var e apiError
	if got := s.do("POST", "/api/check/validate", checkRequest{Library: filepath.Join(lib, "missing")}, &e); got != http.StatusBadRequest || e.Error == "" {
		t.Errorf("missing library: %d %q", got, e.Error)
	}
	if got := s.do("POST", "/api/check/analyse", checkRequest{Library: lib, Out: filepath.Join(lib, "out")}, &e); got != http.StatusBadRequest {
		t.Errorf("out inside the library: %d", got)
	}
	_ = out
}

func TestCheckFlow(t *testing.T) {
	s := newTestServer(t)
	lib, out := checkLibrary(t)
	before := testutil.Tree(t, lib)

	var j Job
	if got := s.do("POST", "/api/check/analyse", checkRequest{Library: lib, Out: out}, &j); got != http.StatusAccepted {
		t.Fatalf("analyse: %d", got)
	}
	if j = s.wait(); j.State != stateDone || !j.DryRun {
		t.Fatalf("analyse job: %+v", j)
	}
	if !maps.Equal(testutil.Tree(t, lib), before) {
		t.Fatal("the check changed the library")
	}
	id := j.Result.(map[string]any)["analysis"].(string)
	type view struct {
		ID     string `json:"id"`
		Report struct {
			Checked int `json:"checked"`
			Broken  int `json:"broken"`
			Songs   []struct {
				Path, Status string
				Findings     []struct{ Code, Severity string }
			} `json:"songs"`
		} `json:"report"`
		Applied bool `json:"applied"`
	}
	var v view
	if got := s.do("GET", "/api/check/analysis?id="+id, nil, &v); got != http.StatusOK {
		t.Fatalf("analysis: %d", got)
	}
	if v.Report.Checked != 3 || v.Report.Broken != 1 || len(v.Report.Songs) == 0 || v.Report.Songs[0].Path != "Q/Queen - Broken" ||
		v.Report.Songs[0].Status != "broken" || len(v.Report.Songs[0].Findings) == 0 {
		t.Fatalf("report = %+v", v.Report)
	}

	// Setting aside the broken song re-checks and moves it.
	if got := s.do("POST", "/api/check/apply", map[string]any{"analysis": id}, &j); got != http.StatusAccepted {
		t.Fatalf("apply: %d", got)
	}
	if j = s.wait(); j.State != stateDone || j.DryRun || j.Stats.Moved != 1 || j.Stats.Failed != 0 {
		t.Fatalf("apply job: %+v", j)
	}
	if _, err := os.Stat(filepath.Join(out, "invalid", "Q", "Queen - Broken", "song.txt")); err != nil {
		t.Errorf("not moved: %v", err)
	}
	if _, err := os.Stat(filepath.Join(lib, "Q", "Queen - Rock", "song.txt")); err != nil {
		t.Errorf("a working song was touched: %v", err)
	}
	var e apiError
	if got := s.do("POST", "/api/check/apply", map[string]any{"analysis": id}, &e); got != http.StatusConflict {
		t.Errorf("second apply: %d", got)
	}
}
