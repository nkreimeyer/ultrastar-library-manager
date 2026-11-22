package webui

import (
	"maps"
	"net/http"
	"os"
	"path/filepath"
	"testing"
)

// stopApplyAtStart makes the apply jobs stop right before their work starts.
func stopApplyAtStart(t *testing.T, s *testServer) {
	t.Helper()
	applyStartHook = func() { s.srv.jobs.cancelRunning() }
	t.Cleanup(func() { applyStartHook = nil })
}

// A check apply stopped before it moved anything leaves the check not applied, so setting
// the broken songs aside can be retried; one that ran is applied.
func TestCheckApplyStoppedCanRetry(t *testing.T) {
	s := newTestServer(t)
	lib, out := checkLibrary(t)
	var j Job
	if got := s.do("POST", "/api/check/analyse", checkRequest{Library: lib, Out: out}, &j); got != http.StatusAccepted {
		t.Fatalf("analyse: %d", got)
	}
	if j = s.wait(); j.State != stateDone {
		t.Fatalf("analyse job: %+v", j)
	}
	id := j.Result.(map[string]any)["analysis"].(string)
	applied := func() bool {
		t.Helper()
		var v struct {
			Applied bool `json:"applied"`
		}
		if got := s.do("GET", "/api/check/analysis?id="+id, nil, &v); got != http.StatusOK {
			t.Fatalf("analysis: %d", got)
		}
		return v.Applied
	}

	before := libTree(t, lib)
	stopApplyAtStart(t, s)
	if got := s.do("POST", "/api/check/apply", map[string]any{"analysis": id}, &j); got != http.StatusAccepted {
		t.Fatalf("apply: %d", got)
	}
	if j = s.wait(); j.State != stateCancelled || j.Stats.Moved != 0 {
		t.Fatalf("stopped apply: %+v", j)
	}
	if !maps.Equal(libTree(t, lib), before) {
		t.Fatal("the stopped apply changed the library")
	}
	if applied() {
		t.Fatal("a stopped apply that moved nothing marked the check applied")
	}

	applyStartHook = nil
	if got := s.do("POST", "/api/check/apply", map[string]any{"analysis": id}, &j); got != http.StatusAccepted {
		t.Fatalf("retry: %d", got)
	}
	if j = s.wait(); j.State != stateDone || j.Stats.Moved != 1 {
		t.Fatalf("retried apply: %+v", j)
	}
	if !applied() {
		t.Error("the apply ran, but the check is not applied")
	}
	if _, err := os.Stat(filepath.Join(out, "invalid", "Q", "Queen - Broken", "song.txt")); err != nil {
		t.Errorf("not moved: %v", err)
	}
	if got := s.do("POST", "/api/check/apply", map[string]any{"analysis": id}, nil); got != http.StatusConflict {
		t.Errorf("apply after the run: %d", got)
	}
}

// A tidy-up without a song step that was stopped before it changed anything is not
// applied: Go ahead stays possible. One that ran is applied.
func TestTidyApplyWithoutSongStepStoppedCanRetry(t *testing.T) {
	s := newTestServer(t)
	lib, out := tidyLibrary(t)
	q := organizeRequest{Mode: "dedupe", Library: lib, Out: out, Tidy: &tidyOptions{UTF8: true, Leftovers: true}}
	v := analyseOrganize(t, s, q)
	applied := func() bool {
		t.Helper()
		var v2 organizeView
		if got := s.do("GET", "/api/organize/analysis?id="+v.ID, nil, &v2); got != http.StatusOK {
			t.Fatalf("analysis: %d", got)
		}
		return v2.Applied
	}

	before := libTree(t, lib)
	stopApplyAtStart(t, s)
	var j Job
	if got := s.do("POST", "/api/organize/apply", map[string]any{"analysis": v.ID}, &j); got != http.StatusAccepted {
		t.Fatalf("apply: %d", got)
	}
	if j = s.wait(); j.State != stateCancelled || j.Stats.Moved != 0 || j.Stats.Written != 0 {
		t.Fatalf("stopped apply: %+v", j)
	}
	if !maps.Equal(libTree(t, lib), before) {
		t.Fatal("the stopped apply changed the library")
	}
	if applied() {
		t.Fatal("a stopped tidy-up that changed nothing is marked applied")
	}

	applyStartHook = nil
	if got := s.do("POST", "/api/organize/apply", map[string]any{"analysis": v.ID}, &j); got != http.StatusAccepted {
		t.Fatalf("retry: %d", got)
	}
	if j = s.wait(); j.State != stateDone || j.Stats.Written == 0 || j.Stats.Moved == 0 {
		t.Fatalf("retried apply: %+v", j)
	}
	if !applied() {
		t.Error("the tidy-up ran, but it is not applied")
	}
	if got := s.do("POST", "/api/organize/apply", map[string]any{"analysis": v.ID}, nil); got != http.StatusConflict {
		t.Errorf("apply after the run: %d", got)
	}
}

// An organize apply that accepted the reviewed plan but failed before its first move (the
// pending report cannot be written) is not applied: the plan can be applied again.
func TestOrganizeApplyFailedBeforeMovesCanRetry(t *testing.T) {
	s := newTestServer(t)
	v, _, _ := raceFixture(t, s)
	lib, out := v.Config.Library, v.Config.OutDir
	report := filepath.Join(out, "organize_plan.json")
	if err := os.Remove(report); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(report, 0o755); err != nil { // in the way of the report
		t.Fatal(err)
	}
	before := libTree(t, lib)
	var j Job
	if got := s.do("POST", "/api/organize/apply", map[string]any{"analysis": v.ID}, &j); got != http.StatusAccepted {
		t.Fatalf("apply: %d", got)
	}
	if j = s.wait(); j.State != stateFailed || j.Stats.Moved != 0 {
		t.Fatalf("failed apply: %+v", j)
	}
	if !maps.Equal(libTree(t, lib), before) {
		t.Fatal("the failed apply changed the library")
	}
	var after organizeView
	if s.do("GET", "/api/organize/analysis?id="+v.ID, nil, &after); after.Applied {
		t.Fatal("an apply that moved nothing marked the plan applied")
	}

	if err := os.Remove(report); err != nil {
		t.Fatal(err)
	}
	if got := s.do("POST", "/api/organize/apply", map[string]any{"analysis": v.ID}, &j); got != http.StatusAccepted {
		t.Fatalf("retry: %d", got)
	}
	if j = s.wait(); j.State != stateDone || j.Stats.Moved != 1 {
		t.Fatalf("retried apply: %+v", j)
	}
	if got := s.do("POST", "/api/organize/apply", map[string]any{"analysis": v.ID}, nil); got != http.StatusConflict {
		t.Errorf("apply after the run: %d", got)
	}
}
