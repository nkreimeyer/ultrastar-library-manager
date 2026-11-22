package organize

import (
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/nkreimeyer/library-manager/fsops"
	"github.com/nkreimeyer/library-manager/internal/testutil"
)

// A remove stopped during the moves finishes the current move, skips the others (the
// report says so), returns the ctx error, and undo puts back what was moved.
func TestRemoveStoppedDuringMoves(t *testing.T) {
	cfg, lib, out := restoreSetup(t)
	before := tree(t, lib)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	opts := RemoveOptions{Ctx: ctx, Progress: func(phase string, done, _ int) {
		if phase == PhaseMove && done == 1 {
			cancel() // during the first move
		}
	}}
	ops := fsops.New(false)
	if err := RemoveWith(cfg, ops, removePatterns(t, "ABBA"), opts); !errors.Is(err, context.Canceled) {
		t.Fatalf("RemoveWith = %v, want context.Canceled", err)
	}
	if st := ops.Stats(); st.Moved != 1 || st.Failed != 0 {
		t.Errorf("stats = %+v, want 1 move", st)
	}
	for rel, want := range map[string]bool{"A/ABBA - SOS": false, "A/ABBA - Waterloo": true, "Q/Queen - Rock": true} {
		if got := testutil.Exists(filepath.Join(lib, filepath.FromSlash(rel), "song.txt")); got != want {
			t.Errorf("%s in the library = %v, want %v", rel, got, want)
		}
	}
	var rep removeReport
	if err := json.Unmarshal([]byte(testutil.ReadFile(t, filepath.Join(out, removeReportJSON))), &rep); err != nil {
		t.Fatal(err)
	}
	got := map[string]string{}
	for _, e := range rep.Removed {
		got[e.Song] = e.Outcome + "/" + e.OutcomeNote
	}
	want := map[string]string{"A/ABBA - SOS": outcomeMoved + "/", "A/ABBA - Waterloo": outcomeSkipped + "/" + noteStopped}
	if !reflect.DeepEqual(got, want) || rep.Summary.Moved != 1 {
		t.Errorf("report outcomes = %v (moved %d), want %v", got, rep.Summary.Moved, want)
	}
	if txt := testutil.ReadFile(t, filepath.Join(out, removeReportTxt)); !strings.Contains(txt, noteStopped) {
		t.Errorf("text report lacks the stop note:\n%s", txt)
	}
	if ust := runUndo(t, cfg); ust.Moved != 1 || ust.Failed != 0 {
		t.Errorf("undo stats = %+v, want 1 move back", ust)
	}
	if got := tree(t, lib); !reflect.DeepEqual(got, before) {
		t.Errorf("undo did not restore the library:\n%s", strings.Join(got, "\n"))
	}
}

// A remove stopped before or while the tags are read moves nothing.
func TestRemoveStoppedWhileReading(t *testing.T) {
	cfg, lib, _ := restoreSetup(t)
	before := tree(t, lib)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	ops := fsops.New(false)
	if err := RemoveWith(cfg, ops, removePatterns(t, "ABBA"), RemoveOptions{Ctx: ctx}); !errors.Is(err, context.Canceled) {
		t.Fatalf("RemoveWith = %v, want context.Canceled", err)
	}
	if st := ops.Stats(); st.Moved != 0 {
		t.Errorf("stats = %+v, want no move", st)
	}
	if got := tree(t, lib); !reflect.DeepEqual(got, before) {
		t.Errorf("a stopped remove changed the library:\n%s", strings.Join(got, "\n"))
	}
	if testutil.Exists(cfg.JournalPath()) {
		t.Error("a remove stopped while reading wrote a journal")
	}
}
