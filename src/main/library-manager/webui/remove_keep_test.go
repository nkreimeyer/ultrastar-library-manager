package webui

import (
	"net/http"
	"os"
	"path/filepath"
	"testing"

	"github.com/nkreimeyer/library-manager/organize"
)

func TestRemoveApplyKeep(t *testing.T) {
	s := newTestServer(t)
	lib, out := removeLibrary(t)
	var j Job
	s.do("POST", "/api/remove/analyse", removeRequest{Library: lib, Out: out, Patterns: "ABBA"}, &j)
	id := s.wait().Result.(map[string]any)["analysis"].(string)

	var e apiError
	for name, keep := range map[string][]string{
		"not in the plan": {"Q/Queen - Rock"},
		"everything":      {"A/ABBA - SOS", "A/ABBA - Waterloo"},
	} {
		if got := s.do("POST", "/api/remove/apply", map[string]any{"analysis": id, "keep": keep}, &e); got != http.StatusBadRequest || e.Error == "" {
			t.Errorf("%s: %d %q", name, got, e.Error)
		}
	}
	if _, err := os.Stat(filepath.Join(lib, "A", "ABBA - SOS", "song.txt")); err != nil {
		t.Fatalf("a refused request moved a song: %v", err)
	}

	if got := s.do("POST", "/api/remove/apply", map[string]any{"analysis": id, "keep": []string{"A/ABBA - SOS"}}, &j); got != http.StatusAccepted {
		t.Fatalf("apply: %d", got)
	}
	if j = s.wait(); j.State != stateDone || j.Stats.Moved != 1 || j.Stats.Failed != 0 {
		t.Fatalf("apply job: %+v", j)
	}
	if _, err := os.Stat(filepath.Join(lib, "A", "ABBA - SOS", "song.txt")); err != nil {
		t.Errorf("the kept song was moved: %v", err)
	}
	if _, err := os.Stat(filepath.Join(out, "removed", "A", "ABBA - Waterloo", "song.txt")); err != nil {
		t.Errorf("the other song was not removed: %v", err)
	}
	rep := j.Result.(map[string]any)["report"].(map[string]any)
	left := rep["leftInPlace"].([]any)
	if len(left) != 1 || left[0].(map[string]any)["reason"] != organize.ReasonKept {
		t.Errorf("leftInPlace = %v", left)
	}
}

// The song to keep is still there when the library changed between check and apply: the
// reviewer's choice does not hide a changed plan.
func TestRemoveApplyKeepPlanChanged(t *testing.T) {
	s := newTestServer(t)
	lib, out := removeLibrary(t)
	var j Job
	s.do("POST", "/api/remove/analyse", removeRequest{Library: lib, Out: out, Patterns: "ABBA"}, &j)
	id := s.wait().Result.(map[string]any)["analysis"].(string)
	writeSong(t, filepath.Join(lib, "A", "ABBA - Mamma Mia"), "ABBA", "Mamma Mia", nil)
	s.do("POST", "/api/remove/apply", map[string]any{"analysis": id, "keep": []string{"A/ABBA - SOS"}}, &j)
	if j = s.wait(); j.State != stateFailed || j.Stats.Moved != 0 {
		t.Fatalf("apply job: %+v", j)
	}
	if _, err := os.Stat(filepath.Join(lib, "A", "ABBA - Waterloo", "song.txt")); err != nil {
		t.Errorf("a song was moved: %v", err)
	}
	// The new plan has all three songs again; the choice has to be made again.
	var v struct {
		Report struct {
			Removed []struct{ Song string } `json:"removed"`
		} `json:"report"`
	}
	s.do("GET", "/api/remove/analysis?id="+id, nil, &v)
	if len(v.Report.Removed) != 3 {
		t.Errorf("new plan removes %d songs, want 3", len(v.Report.Removed))
	}
}
