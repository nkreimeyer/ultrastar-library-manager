package webui

import (
	"net/http"
	"path/filepath"
	"testing"
)

// raceFixture is a dedupe check with one duplicate and one other song; it returns the check
// and the refs of the other song and of one duplicate.
func raceFixture(t *testing.T, s *testServer) (v organizeView, abba, queen string) {
	t.Helper()
	base := t.TempDir()
	lib, out := filepath.Join(base, "library"), filepath.Join(base, "out")
	writeSong(t, filepath.Join(lib, "Q", "Queen - Rock"), "Queen", "Rock", nil)
	writeSong(t, filepath.Join(lib, "Q", "Queen - Rock (2)"), "Queen", "Rock", nil)
	writeSong(t, filepath.Join(lib, "A", "ABBA - SOS"), "ABBA", "SOS", nil)
	v = analyseOrganize(t, s, organizeRequest{Mode: "dedupe", Library: lib, Out: out})
	if v.Summary.Duplicates != 1 || v.Summary.Excluded != 0 {
		t.Fatalf("check: %+v", v.Summary)
	}
	for _, sv := range v.Songs {
		switch sv.Artist {
		case "ABBA":
			abba = sv.Ref
		case "Queen":
			queen = sv.Ref
		}
	}
	if abba == "" || queen == "" {
		t.Fatalf("songs: %+v", v.Songs)
	}
	return v, abba, queen
}

// TestEditAfterApplyAcceptedPlan: an exclusion or join whose new plan was made while an
// apply accepted the reviewed plan must not replace the plan that was applied.
func TestEditAfterApplyAcceptedPlan(t *testing.T) {
	for _, path := range []string{"/api/organize/exclude", "/api/organize/join"} {
		t.Run(path, func(t *testing.T) {
			s := newTestServer(t)
			v, abba, queen := raceFixture(t, s)
			body := map[string]any{"analysis": v.ID, "exclude": []string{abba}}
			if path == "/api/organize/join" {
				body = map[string]any{"analysis": v.ID, "join": [][2]string{{abba, queen}}}
			}

			reached, release := make(chan struct{}), make(chan struct{})
			replanHook = func() {
				reached <- struct{}{}
				<-release
			}
			t.Cleanup(func() { replanHook = nil })
			code := make(chan int, 1)
			go func() {
				var e apiError
				code <- s.do("POST", path, body, &e)
			}()
			<-reached // the new plan is made, not stored yet

			var j Job
			if got := s.do("POST", "/api/organize/apply", map[string]any{"analysis": v.ID}, &j); got != http.StatusAccepted {
				close(release)
				t.Fatalf("apply: %d", got)
			}
			j = s.wait()
			close(release)
			if j.State != stateDone || j.Stats.Moved != 1 {
				t.Fatalf("apply job: %+v", j)
			}
			if got := <-code; got != http.StatusConflict {
				t.Errorf("%s after the apply: %d, want %d", path, got, http.StatusConflict)
			}
			var after organizeView
			s.do("GET", "/api/organize/analysis?id="+v.ID, nil, &after)
			if !after.Applied || after.Summary != v.Summary || len(after.Excluded) != 0 || len(after.Joined) != 0 {
				t.Errorf("the applied plan was replaced: %+v excluded %v joined %v", after.Summary, after.Excluded, after.Joined)
			}
		})
	}
}

// TestEditWhileApplyRuns: while an apply runs (before it compares the plans), exclusions,
// joins and a second apply are refused, so the run applies the plan it was started with.
func TestEditWhileApplyRuns(t *testing.T) {
	s := newTestServer(t)
	v, abba, queen := raceFixture(t, s)
	reached, release := make(chan struct{}), make(chan struct{})
	applyJobHook = func() {
		reached <- struct{}{}
		<-release
	}
	t.Cleanup(func() { applyJobHook = nil })
	var j Job
	if got := s.do("POST", "/api/organize/apply", map[string]any{"analysis": v.ID}, &j); got != http.StatusAccepted {
		t.Fatalf("apply: %d", got)
	}
	<-reached
	var e apiError
	if got := s.do("POST", "/api/organize/exclude", map[string]any{"analysis": v.ID, "exclude": []string{abba}}, &e); got != http.StatusConflict {
		t.Errorf("exclude while applying: %d", got)
	}
	if got := s.do("POST", "/api/organize/join", map[string]any{"analysis": v.ID, "join": [][2]string{{abba, queen}}}, &e); got != http.StatusConflict {
		t.Errorf("join while applying: %d", got)
	}
	if got := s.do("POST", "/api/organize/apply", map[string]any{"analysis": v.ID}, &e); got != http.StatusConflict {
		t.Errorf("second apply while applying: %d", got)
	}
	close(release)
	if j = s.wait(); j.State != stateDone || j.Stats.Moved != 1 {
		t.Fatalf("apply job: %+v", j)
	}
	var after organizeView
	s.do("GET", "/api/organize/analysis?id="+v.ID, nil, &after)
	if !after.Applied || after.Summary != v.Summary || len(after.Excluded) != 0 || len(after.Joined) != 0 {
		t.Errorf("plan after the apply: %+v excluded %v joined %v", after.Summary, after.Excluded, after.Joined)
	}
}

// TestEditAfterPlanChangedApply: an apply that stopped at a changed plan ends its "applying"
// state, so the new plan can be reviewed and edited.
func TestEditAfterPlanChangedApply(t *testing.T) {
	s := newTestServer(t)
	v, abba, _ := raceFixture(t, s)
	base := filepath.Dir(v.Config.Library)
	writeSong(t, filepath.Join(base, "library", "B", "Blur - Song 2"), "Blur", "Song 2", nil)
	var j Job
	if got := s.do("POST", "/api/organize/apply", map[string]any{"analysis": v.ID}, &j); got != http.StatusAccepted {
		t.Fatalf("apply: %d", got)
	}
	if j = s.wait(); j.State != stateFailed {
		t.Fatalf("apply job: %+v", j)
	}
	var after organizeView
	if got := s.do("POST", "/api/organize/exclude", map[string]any{"analysis": v.ID, "exclude": []string{abba}}, &after); got != http.StatusOK {
		t.Fatalf("exclude after a changed plan: %d", got)
	}
	if after.Applied || len(after.Excluded) != 1 {
		t.Errorf("after the exclude: applied %v excluded %v", after.Applied, after.Excluded)
	}
}
