package webui

import (
	"context"
	"errors"
	"net/http"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	log "github.com/sirupsen/logrus"

	"github.com/nkreimeyer/library-manager/fsops"
	"github.com/nkreimeyer/library-manager/internal/testutil"
	"github.com/nkreimeyer/library-manager/organize"
)

// markApplied decides from the stats of the job: any change counts as applied even when the
// job failed or was stopped; without a change only a clean finish does.
func TestMarkAppliedDecision(t *testing.T) {
	dir := t.TempDir()
	file := func(name string) string {
		t.Helper()
		p := filepath.Join(dir, name)
		testutil.WriteFile(t, p, "x")
		return p
	}
	changes := map[string]func(*fsops.Ops){
		"written": func(o *fsops.Ops) {
			if _, err := o.WriteFileAtomic(filepath.Join(dir, "w.txt"), []byte("w")); err != nil {
				t.Fatal(err)
			}
		},
		"moved": func(o *fsops.Ops) {
			if _, err := o.MoveInto(file("m.txt"), filepath.Join(dir, "moved"), "m.txt"); err != nil {
				t.Fatal(err)
			}
		},
		"removed": func(o *fsops.Ops) {
			if err := o.Remove(file("r.txt")); err != nil {
				t.Fatal(err)
			}
		},
		"replaced": func(o *fsops.Ops) {
			if err := o.Replace(file("new.txt"), file("old.txt")); err != nil {
				t.Fatal(err)
			}
		},
	}
	s := &Server{}
	for name, change := range changes {
		ops := fsops.New(false)
		change(ops)
		st := ops.Stats()
		if st.Written+st.Moved+st.Removed+st.Replaced != 1 {
			t.Fatalf("%s: stats %+v, want exactly one change", name, st)
		}
		var applied bool
		s.markApplied(&applied, ops, context.Background(), errors.New("failed after the change"))
		if !applied {
			t.Errorf("%s: a failed job that changed something is not marked applied", name)
		}
	}
	// Several kinds of change together are changes too.
	ops := fsops.New(false)
	changes["written"](ops)
	changes["moved"](ops)
	changes["removed"](ops)
	changes["replaced"](ops)
	var applied bool
	s.markApplied(&applied, ops, context.Background(), errors.New("x"))
	if !applied {
		t.Error("a failed job with every kind of change is not marked applied")
	}

	cancelled, cancel := context.WithCancel(context.Background())
	cancel()
	failedOps := fsops.New(false)
	failedOps.Fail()
	for _, c := range []struct {
		name string
		ops  *fsops.Ops
		ctx  context.Context
		err  error
		want bool
	}{
		{"clean finish without a change", fsops.New(false), context.Background(), nil, true},
		{"error without a change", fsops.New(false), context.Background(), errors.New("x"), false},
		{"stopped without a change", fsops.New(false), cancelled, nil, false},
		{"failed operation without a change", failedOps, context.Background(), nil, false},
	} {
		var applied bool
		s.markApplied(&applied, c.ops, c.ctx, c.err)
		if applied != c.want {
			t.Errorf("%s: applied = %v, want %v", c.name, applied, c.want)
		}
	}
}

// A check apply that finishes hands the check report to the page in its result.
func TestCheckApplyResultHasReport(t *testing.T) {
	s := newTestServer(t)
	lib, out := checkLibrary(t)
	var j Job
	s.do("POST", "/api/check/analyse", checkRequest{Library: lib, Out: out}, &j)
	id := s.wait().Result.(map[string]any)["analysis"].(string)
	if got := s.do("POST", "/api/check/apply", map[string]any{"analysis": id}, &j); got != http.StatusAccepted {
		t.Fatalf("apply: %d", got)
	}
	j = s.wait()
	if j.State != stateDone {
		t.Fatalf("apply job: %+v", j)
	}
	res := j.Result.(map[string]any)
	rep, ok := res["report"].(map[string]any)
	if !ok || len(rep) == 0 {
		t.Fatalf("result has no report: %+v", res)
	}
}

// The progress of the remove apply's re-plan is only its reading of the songs: its
// planned moves must not show as moves before the real ones start.
func TestRemoveApplyReplanProgressIsInspectOnly(t *testing.T) {
	s := newTestServer(t)
	lib, out := removeLibrary(t)
	var j Job
	s.do("POST", "/api/remove/analyse", removeRequest{Library: lib, Out: out, Patterns: "ABBA"}, &j)
	id := s.wait().Result.(map[string]any)["analysis"].(string)

	var mu sync.Mutex
	var phase string
	seen := false
	log.AddHook(&logFunc{fire: func(e *log.Entry) {
		if strings.HasPrefix(e.Message, "[dry-run] would move ") {
			mu.Lock()
			defer mu.Unlock()
			if !seen {
				seen = true
				cur, _, _, _ := s.srv.jobs.current(0)
				phase = cur.Phase
			}
		}
	}})
	if got := s.do("POST", "/api/remove/apply", map[string]any{"analysis": id}, &j); got != http.StatusAccepted {
		t.Fatalf("apply: %d", got)
	}
	if j = s.wait(); j.State != stateDone {
		t.Fatalf("apply job: %+v", j)
	}
	mu.Lock()
	defer mu.Unlock()
	if !seen {
		t.Fatal("no planned move was logged")
	}
	if phase != organize.PhaseInspect {
		t.Errorf("phase while the dry run plans its moves = %q, want %q", phase, organize.PhaseInspect)
	}
}

// A failing song step ends a tidy-up with details naming the step (the later steps did not
// run); a plain organize/dedupe/add run (no tidy options) is that one step and returns its
// error as it is.
func TestStepRunSongStepFailure(t *testing.T) {
	mk := func(tidy *tidyOptions) *stepRun {
		r, _ := newStepRun(t, tidy, false)
		// Keeping duplicates is refused outside dedupe: the song step fails with a plain error.
		r.songs = organize.NewOptions(organize.ModeAdd, false, false, false)
		r.songs.KeepDuplicates = true
		return r
	}
	_, err := mk(&tidyOptions{Broken: true}).run()
	var de *detailedError
	if err == nil || !errors.As(err, &de) {
		t.Fatalf("tidy-up: err = %v, want a detailedError", err)
	}
	if !strings.Contains(de.msg, "so the later steps did not run") || len(de.details) != 1 || !strings.HasPrefix(de.details[0], stepSongs+": ") {
		t.Errorf("tidy-up: %+v", de)
	}

	steps, err := mk(nil).run()
	if err == nil || errors.As(err, &de) {
		t.Fatalf("plain run: err = %v, want a plain error", err)
	}
	if len(steps) != 1 || steps[0].Step != stepSongs || steps[0].Error == "" {
		t.Errorf("plain run: steps = %+v", steps)
	}
}
