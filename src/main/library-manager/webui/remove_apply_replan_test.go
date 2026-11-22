package webui

import (
	"maps"
	"net/http"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	log "github.com/sirupsen/logrus"

	"github.com/nkreimeyer/library-manager/internal/testutil"
)

// A changed remove plan lists at most maxDiffLines differences and counts the rest.
func TestRemoveApplyPlanChangedTruncated(t *testing.T) {
	setMaxDiffLines(t, 1)
	s := newTestServer(t)
	lib, out := removeLibrary(t)
	var j Job
	s.do("POST", "/api/remove/analyse", removeRequest{Library: lib, Out: out, Patterns: "ABBA"}, &j)
	id := s.wait().Result.(map[string]any)["analysis"].(string)
	writeSong(t, filepath.Join(lib, "A", "ABBA - Mamma Mia"), "ABBA", "Mamma Mia", nil)
	writeSong(t, filepath.Join(lib, "A", "ABBA - Fernando"), "ABBA", "Fernando", nil)
	before := testutil.Tree(t, lib)
	s.do("POST", "/api/remove/apply", map[string]any{"analysis": id}, &j)
	j = s.wait()
	if j.State != stateFailed || j.Stats.Moved != 0 || !strings.Contains(j.Error, "2 song(s) would be handled differently") {
		t.Fatalf("job = %+v", j)
	}
	if len(j.Details) != 2 || !strings.HasPrefix(j.Details[0], "now removed: ") || j.Details[1] != "… and 1 more" {
		t.Errorf("details = %q", j.Details)
	}
	if !maps.Equal(testutil.Tree(t, lib), before) {
		t.Error("a changed plan moved something")
	}
}

// A changed plan is not cut off when it has exactly maxDiffLines differences.
func TestRemoveApplyPlanChangedAtTheLimit(t *testing.T) {
	setMaxDiffLines(t, 2)
	s := newTestServer(t)
	lib, out := removeLibrary(t)
	var j Job
	s.do("POST", "/api/remove/analyse", removeRequest{Library: lib, Out: out, Patterns: "ABBA"}, &j)
	id := s.wait().Result.(map[string]any)["analysis"].(string)
	writeSong(t, filepath.Join(lib, "A", "ABBA - Mamma Mia"), "ABBA", "Mamma Mia", nil)
	writeSong(t, filepath.Join(lib, "A", "ABBA - Fernando"), "ABBA", "Fernando", nil)
	s.do("POST", "/api/remove/apply", map[string]any{"analysis": id}, &j)
	j = s.wait()
	want := []string{"now removed: A/ABBA - Fernando", "now removed: A/ABBA - Mamma Mia"}
	if strings.Join(j.Details, "|") != strings.Join(want, "|") {
		t.Errorf("details = %q, want %q", j.Details, want)
	}
}

// A song that was removed from the plan by hand since the review shows as "no longer removed".
func TestRemoveApplyPlanChangedNoLongerRemoved(t *testing.T) {
	s := newTestServer(t)
	lib, out := removeLibrary(t)
	var j Job
	s.do("POST", "/api/remove/analyse", removeRequest{Library: lib, Out: out, Patterns: "ABBA"}, &j)
	id := s.wait().Result.(map[string]any)["analysis"].(string)
	testutil.WriteFile(t, filepath.Join(lib, "A", "ABBA - SOS", "song.txt"), "#TITLE:SOS\n#ARTIST:Someone Else\n#MP3:song.mp3\n"+testutil.NotesTxt("other"))
	s.do("POST", "/api/remove/apply", map[string]any{"analysis": id}, &j)
	j = s.wait()
	if j.State != stateFailed || len(j.Details) != 1 || j.Details[0] != "no longer removed: A/ABBA - SOS" {
		t.Fatalf("job = %+v", j)
	}
	// The analysis holds the new, full plan: one song.
	var v struct {
		Report struct {
			Removed []struct{ Song string } `json:"removed"`
		} `json:"report"`
		Applied bool `json:"applied"`
	}
	s.do("GET", "/api/remove/analysis?id="+id, nil, &v)
	if len(v.Report.Removed) != 1 || v.Report.Removed[0].Song != "A/ABBA - Waterloo" || v.Applied {
		t.Errorf("analysis after the changed plan: %+v", v)
	}
}

// A stop while the apply plans again (before any move) ends the job cancelled, moves
// nothing, and leaves the analysis unapplied: the same review can be applied afterwards.
func TestRemoveApplyStoppedDuringReplan(t *testing.T) {
	s := newTestServer(t)
	lib, out := removeLibrary(t)
	before := testutil.Tree(t, lib)
	var j Job
	s.do("POST", "/api/remove/analyse", removeRequest{Library: lib, Out: out, Patterns: "ABBA"}, &j)
	id := s.wait().Result.(map[string]any)["analysis"].(string)

	var armed atomic.Bool
	armed.Store(true)
	log.AddHook(&logFunc{fire: func(e *log.Entry) {
		// The dry run's first planned move: the job is still planning.
		if strings.HasPrefix(e.Message, "[dry-run] would move ") && armed.CompareAndSwap(true, false) {
			s.srv.jobs.cancelRunning()
		}
	}})
	if got := s.do("POST", "/api/remove/apply", map[string]any{"analysis": id}, &j); got != http.StatusAccepted {
		t.Fatalf("apply: %d", got)
	}
	j = s.wait()
	if j.State != stateCancelled || j.Stats.Moved != 0 {
		t.Fatalf("stopped apply: %+v", j)
	}
	if armed.Load() {
		t.Fatal("the stop never happened: the hook saw no planned move")
	}
	if !maps.Equal(testutil.Tree(t, lib), before) {
		t.Error("a song was moved")
	}
	var v struct {
		Applied bool `json:"applied"`
	}
	s.do("GET", "/api/remove/analysis?id="+id, nil, &v)
	if v.Applied {
		t.Error("the analysis was marked applied by a job that stopped while planning")
	}
	if got := s.do("POST", "/api/remove/apply", map[string]any{"analysis": id}, &j); got != http.StatusAccepted {
		t.Fatalf("apply again: %d", got)
	}
	if j = s.wait(); j.State != stateDone || j.Stats.Moved != 2 {
		t.Errorf("second apply: %+v", j)
	}
}
