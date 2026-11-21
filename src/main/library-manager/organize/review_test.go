package organize

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"sort"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/nkreimeyer/library-manager/fsops"
)

func TestMakePlanExclude(t *testing.T) {
	good := Quality{HasVideo: true, VideoHeight: 1080, AudioBitrate: 320000, HasCover: true}
	poor := Quality{AudioBitrate: 128000}
	ex := func(refs ...string) map[string]bool {
		m := map[string]bool{}
		for _, r := range refs {
			m[r] = true
		}
		return m
	}
	twoVersions := []Candidate{
		song(hq, "s", "ABBA", "SOS", good),
		song(low1, "s", "ABBA", "SOS", poor),
	}
	tests := []struct {
		name    string
		cands   []Candidate
		exclude map[string]bool
		inPlace bool
		occ     func(string) (string, string, bool)
		want    []string
	}{
		{"nothing excluded", twoVersions, nil, false, nil, []string{
			"hq:s: library A/ABBA - SOS",
			"low1:s: duplicate <hq:s> (no video)",
		}},
		{"a duplicate stays", twoVersions, ex("low1:s"), false, nil, []string{
			"hq:s: library A/ABBA - SOS",
			"low1:s: left (excluded in review)",
		}},
		{"without the keeper the next version wins", twoVersions, ex("hq:s"), false, nil, []string{
			"hq:s: left (excluded in review)",
			"low1:s: library A/ABBA - SOS",
		}},
		{"the whole song stays", twoVersions, ex("hq:s", "low1:s"), false, nil, []string{
			"hq:s: left (excluded in review)",
			"low1:s: left (excluded in review)",
		}},
		{"an unknown ref changes nothing", twoVersions, ex("low2:s"), false, nil, []string{
			"hq:s: library A/ABBA - SOS",
			"low1:s: duplicate <hq:s> (no video)",
		}},
		{"a broken song stays", []Candidate{
			brokenSong(hq, "b", "ABBA", "SOS", "no-notes"),
			song(low1, "s", "ABBA", "SOS", poor),
		}, ex("hq:b"), false, nil, []string{
			"hq:b: left (excluded in review)",
			"low1:s: library A/ABBA - SOS",
		}},
		{"an excluded library copy keeps its place", []Candidate{
			song(hq, "s", "ABBA", "SOS", good),
			song(lib, "A/ABBA - SOS", "ABBA", "SOS", poor),
		}, ex("library:A/ABBA - SOS"), true, func(rel string) (string, string, bool) {
			if NameKey(rel) == NameKey("A/ABBA - SOS") {
				return "A/ABBA - SOS", "", true
			}
			return "", "", false
		}, []string{
			"hq:s: left (target is occupied by library:A/ABBA - SOS, which stays in place)",
			"library:A/ABBA - SOS: left (excluded in review)",
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			p := MakePlan(tt.cands, PlanOptions{Exclude: tt.exclude, LibraryInPlace: tt.inPlace, Occupied: tt.occ})
			checkSummary(t, tt.cands, p, tt.want)
		})
	}
}

func TestPlanDiff(t *testing.T) {
	a := []Candidate{song(hq, "s", "ABBA", "SOS", Quality{}), song(low1, "s", "ABBA", "SOS", Quality{}), song(hq, "q", "Queen", "Rock", Quality{})}
	pa := Plan{Decisions: []Decision{
		{Action: ActMove, Target: "A/ABBA - SOS", Other: -1},
		{Action: ActDuplicate, Reason: "same quality", Other: 0},
		{Action: ActMove, Target: "Q/Queen - Rock", Other: -1},
	}}
	if d := PlanDiff(a, pa, a, pa); len(d) != 0 {
		t.Errorf("same plan: %v", d)
	}
	// Only the reason differs: same moves.
	pb := Plan{Decisions: append([]Decision(nil), pa.Decisions...)}
	pb.Decisions[1].Reason = "no video"
	if d := PlanDiff(a, pa, a, pb); len(d) != 0 {
		t.Errorf("other reason: %v", d)
	}
	// The keeper swapped, Queen is gone, a new song appeared (b is in another order).
	b := []Candidate{song(low2, "n", "New", "Song", Quality{}), song(low1, "s", "ABBA", "SOS", Quality{}), song(hq, "s", "ABBA", "SOS", Quality{})}
	pb = Plan{Decisions: []Decision{
		{Action: ActMove, Target: "N/New - Song", Other: -1},
		{Action: ActMove, Target: "A/ABBA - SOS", Other: -1},
		{Action: ActDuplicate, Other: 1},
	}}
	want := []string{
		"hq:q: gone (was: library Q/Queen - Rock)",
		"hq:s: duplicate (instead: low1:s), was: library A/ABBA - SOS",
		"low1:s: library A/ABBA - SOS, was: duplicate (instead: hq:s)",
		"low2:n: new (library N/New - Song)",
	}
	if d := PlanDiff(a, pa, b, pb); !reflect.DeepEqual(d, want) {
		t.Errorf("diff:\n  %s\nwant\n  %s", strings.Join(d, "\n  "), strings.Join(want, "\n  "))
	}
}

// reviewSources creates two sources with the same song and one other song.
func reviewSources(t *testing.T) (base, a, b string) {
	t.Helper()
	base = t.TempDir()
	a, b = filepath.Join(base, "a"), filepath.Join(base, "b")
	writeFiles(t, a, merge(songFiles("ABBA - SOS", "ABBA", "SOS", "cover"), songFiles("Queen - Rock", "Queen", "Rock")))
	writeFiles(t, b, songFiles("x/ABBA - SOS", "ABBA", "SOS"))
	return base, a, b
}

func TestRunReview(t *testing.T) {
	noFFprobe(t)
	base, a, b := reviewSources(t)
	lib, out := filepath.Join(base, "library"), filepath.Join(base, "out")
	before := [][]string{tree(t, a), tree(t, b)}
	cfg := organizeCfg(t, [][]string{{a, b}}, lib, out, false)

	var got *Review
	err := Run(cfg, fsops.New(false), Options{Review: func(r *Review) error {
		got = r
		return fmt.Errorf("%w: test", ErrPlanChanged)
	}})
	if !errors.Is(err, ErrPlanChanged) {
		t.Fatalf("Run = %v, want ErrPlanChanged", err)
	}
	if !reflect.DeepEqual([][]string{tree(t, a), tree(t, b)}, before) || tree(t, lib) != nil {
		t.Error("a Review error must stop Run before anything is moved")
	}
	for _, p := range []string{cfg.ReportPath(reportJSON), cfg.JournalPath()} {
		if _, err := os.Stat(p); !os.IsNotExist(err) {
			t.Errorf("%s exists after a Review error", p)
		}
	}
	if got == nil || len(got.Cands) != 3 || len(got.Plan.Decisions) != 3 {
		t.Fatalf("review = %+v", got)
	}
	if len(got.Notes) == 0 || got.Notes[len(got.Notes)-1] != noteNoMediaQuality {
		t.Errorf("notes = %v, want the ffprobe note", got.Notes)
	}

	// Replan with the keeper excluded: the other version takes its place.
	keeper := ""
	for i, d := range got.Plan.Decisions {
		if d.Action == ActMove && got.Cands[i].Artist == "ABBA" {
			keeper = got.Cands[i].Ref()
		}
	}
	if keeper != "a:ABBA - SOS" {
		t.Fatalf("keeper = %q", keeper)
	}
	re := got.Replan(map[string]bool{keeper: true})
	want := []string{
		"a:ABBA - SOS: left, was: library A/ABBA - SOS",
		"b:x/ABBA - SOS: library A/ABBA - SOS, was: duplicate (instead: a:ABBA - SOS)",
	}
	if d := PlanDiff(got.Cands, got.Plan, got.Cands, re); !reflect.DeepEqual(d, want) {
		t.Errorf("replan diff:\n  %s\nwant\n  %s", strings.Join(d, "\n  "), strings.Join(want, "\n  "))
	}
	if d := PlanDiff(got.Cands, got.Plan, got.Cands, got.Replan(nil)); len(d) != 0 {
		t.Errorf("Replan(nil) differs: %v", d)
	}

	// The real run with the exclusion moves what the replan said.
	var real *Review
	_, rep := runOrganize(t, cfg, Options{Exclude: map[string]bool{keeper: true}, Review: func(r *Review) error {
		real = r
		return nil
	}})
	if d := PlanDiff(got.Cands, re, real.Cands, real.Plan); len(d) != 0 {
		t.Errorf("real run differs from the replan: %v", d)
	}
	if _, err := os.Stat(filepath.Join(a, "ABBA - SOS", "cover.png")); err != nil {
		t.Errorf("the excluded keeper must stay: %v", err)
	}
	if len(rep.LeftInPlace) != 1 || rep.LeftInPlace[0].Reason != ReasonExcluded {
		t.Errorf("left in place = %+v", rep.LeftInPlace)
	}
	if rep.Summary.ToLibrary != 2 || rep.Summary.Duplicates != 0 {
		t.Errorf("summary = %+v", rep.Summary)
	}
}

func TestRunMoveProgress(t *testing.T) {
	noFFprobe(t)
	base, a, b := reviewSources(t)
	cfg := organizeCfg(t, [][]string{{a}, {b}}, filepath.Join(base, "library"), filepath.Join(base, "out"), false)
	var moves []string // PhaseMove is reported by the (sequential) moves only
	err := Run(cfg, fsops.New(false), Options{Progress: func(phase string, done, total int) {
		if phase == PhaseMove {
			moves = append(moves, fmt.Sprintf("%d/%d", done, total))
		}
	}})
	if err != nil {
		t.Fatal(err)
	}
	// One duplicate to the out dir, then two songs into the library.
	if got := strings.Join(moves, " "); got != "0/3 1/3 2/3 3/3" {
		t.Errorf("move progress = %s", got)
	}
}

func TestRunCancelBeforeMoves(t *testing.T) {
	noFFprobe(t)
	base, a, b := reviewSources(t)
	lib, out := filepath.Join(base, "library"), filepath.Join(base, "out")
	before := [][]string{tree(t, a), tree(t, b)}
	cfg := organizeCfg(t, [][]string{{a}, {b}}, lib, out, false)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	reviewed := false
	var inspected atomic.Int64
	err := Run(cfg, fsops.New(false), Options{Ctx: ctx, Review: func(*Review) error { reviewed = true; return nil },
		Progress: func(_ string, done, _ int) { inspected.Store(max(inspected.Load(), int64(done))) }})
	if inspected.Load() != 0 {
		t.Errorf("%d song(s) inspected after the cancel", inspected.Load())
	}
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("Run = %v, want context.Canceled", err)
	}
	if reviewed {
		t.Error("a run cancelled while inspecting must not plan")
	}
	if !reflect.DeepEqual([][]string{tree(t, a), tree(t, b)}, before) || tree(t, lib) != nil {
		t.Error("a cancelled run moved something")
	}
	if runs, err := ListRuns(cfg); err != nil || len(runs) != 0 {
		t.Errorf("runs = %v, %v; want none", runs, err)
	}
}

func TestRunCancelDuringMovesAndUndo(t *testing.T) {
	noFFprobe(t)
	base, a, b := reviewSources(t)
	c := filepath.Join(base, "c") // a second duplicate: two moves to duplicates/
	writeFiles(t, c, songFiles("ABBA - SOS", "ABBA", "SOS"))
	lib, out := filepath.Join(base, "library"), filepath.Join(base, "out")
	before := [][]string{tree(t, a), tree(t, b), tree(t, c)}
	cfg := organizeCfg(t, [][]string{{a}, {b}, {c}}, lib, out, false)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var inspected, moveCalls atomic.Int64
	ops := fsops.New(false)
	err := Run(cfg, ops, Options{Ctx: ctx, Progress: func(phase string, done, total int) {
		switch phase {
		case PhaseInspect:
			if total != 4 {
				t.Errorf("inspect total = %d", total)
			}
			inspected.Store(int64(done))
		case PhaseMove:
			moveCalls.Add(1)
			if total != 4 {
				t.Errorf("move total = %d", total)
			}
			if done == 1 {
				cancel() // the first move still runs, nothing after it
			}
		}
	}})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("Run = %v, want context.Canceled", err)
	}
	if inspected.Load() != 4 {
		t.Errorf("inspected = %d, want 4", inspected.Load())
	}
	if st := ops.Stats(); st.Moved != 1 || st.Failed != 0 {
		t.Errorf("stats = %+v, want exactly one move", st)
	}
	if moveCalls.Load() != 2 { // done=0 and done=1
		t.Errorf("move progress calls = %d", moveCalls.Load())
	}
	runs, err := ListRuns(cfg)
	if err != nil || len(runs) != 1 || runs[0].Moves != 1 || runs[0].Undone || !runs[0].Next || runs[0].Time == "" {
		t.Fatalf("runs = %+v, %v", runs, err)
	}
	// The moves the stop skipped are reported as skipped, not as pending.
	data, err := os.ReadFile(cfg.ReportPath(reportJSON))
	if err != nil {
		t.Fatal(err)
	}
	var rep planReport
	if err := json.Unmarshal(data, &rep); err != nil {
		t.Fatal(err)
	}
	var outcomes []string
	for _, e := range rep.ToLibrary {
		outcomes = append(outcomes, e.Outcome+": "+e.OutcomeNote)
	}
	for _, e := range rep.Duplicates {
		outcomes = append(outcomes, e.Outcome+": "+e.OutcomeNote)
	}
	sort.Strings(outcomes)
	want := []string{"moved: ", "skipped: " + noteStopped, "skipped: " + noteStopped, "skipped: " + noteStopped}
	if !reflect.DeepEqual(outcomes, want) {
		t.Errorf("outcomes = %q, want %q", outcomes, want)
	}
	if txt, err := os.ReadFile(cfg.ReportPath(reportTxt)); err != nil || strings.Contains(string(txt), outcomePending) ||
		strings.Count(string(txt), "[skipped: the run was stopped]") != 3 {
		t.Errorf("report text (%v):\n%s", err, txt)
	}
	runUndo(t, cfg)
	if !reflect.DeepEqual([][]string{tree(t, a), tree(t, b), tree(t, c)}, before) {
		t.Error("undo did not restore the sources")
	}
	if runs, _ = ListRuns(cfg); len(runs) != 1 || !runs[0].Undone || runs[0].Next {
		t.Errorf("runs after undo = %+v", runs)
	}
}

// A run cancelled after a song was parked under the temporary name (a swap of two
// library songs) moves it back: nothing stays hidden, and the library is as before.
func TestApplyCancelAfterPark(t *testing.T) {
	noFFprobe(t)
	base := t.TempDir()
	lib, out := filepath.Join(base, "lib"), filepath.Join(base, "out")
	writeFiles(t, lib, merge(songFiles("A/a", "Bee", "B"), songFiles("B/b", "Abba", "A")))
	before := tree(t, lib)
	cfg := organizeCfg(t, [][]string{{lib}}, lib, out, false)
	ops := fsops.New(false)
	cands := []Candidate{
		{Label: "lib", Rel: "A/a", Folder: filepath.Join(lib, "A", "a"), InLibrary: true},
		{Label: "lib", Rel: "B/b", Folder: filepath.Join(lib, "B", "b"), InLibrary: true},
	}
	plan := Plan{Decisions: []Decision{{Action: ActMove, Target: "B/b", Other: -1}, {Action: ActMove, Target: "A/a", Other: -1}}}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	a := newApplier(cfg, ops, cands, map[int]moveOutcome{})
	a.ctx = ctx
	parks := 0
	a.moveTo = func(src, dst string) error {
		if strings.HasPrefix(filepath.Base(dst), tempPrefix) {
			parks++
			defer cancel()
		}
		return ops.MoveTo(src, dst)
	}
	a.apply(plan)
	if parks != 1 || !a.stopped {
		t.Fatalf("parks = %d, stopped = %v", parks, a.stopped)
	}
	if got := tree(t, lib); !reflect.DeepEqual(got, before) {
		t.Errorf("library:\n%s\nwant:\n%s", strings.Join(got, "\n"), strings.Join(before, "\n"))
	}
	if st := ops.Stats(); st.Moved != 2 || st.Failed != 0 {
		t.Errorf("stats = %+v, want the park and the move back", st)
	}
	if st := runUndo(t, organizeCfg(t, nil, lib, out, false)); st.Failed != 0 {
		t.Errorf("undo: %+v", st)
	}
	if got := tree(t, lib); !reflect.DeepEqual(got, before) {
		t.Errorf("library after undo:\n%s", strings.Join(got, "\n"))
	}
}

// A run cancelled after the middle move of a swap (A parked, B moved into A's place)
// cannot move A back, so A goes to its planned target. The journal still works, so that
// move is journaled and -undo restores the library.
func TestApplyCancelAfterSwapMoveIsJournaled(t *testing.T) {
	noFFprobe(t)
	base := t.TempDir()
	lib, out := filepath.Join(base, "lib"), filepath.Join(base, "out")
	writeFiles(t, lib, merge(songFiles("A/a", "Bee", "B"), songFiles("B/b", "Abba", "A")))
	before := tree(t, lib)
	cfg := organizeCfg(t, [][]string{{lib}}, lib, out, false)
	ops := fsops.New(false)
	cands := []Candidate{
		{Label: "lib", Rel: "A/a", Folder: filepath.Join(lib, "A", "a"), InLibrary: true},
		{Label: "lib", Rel: "B/b", Folder: filepath.Join(lib, "B", "b"), InLibrary: true},
	}
	plan := Plan{Decisions: []Decision{{Action: ActMove, Target: "B/b", Other: -1}, {Action: ActMove, Target: "A/a", Other: -1}}}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	a := newApplier(cfg, ops, cands, map[int]moveOutcome{})
	a.ctx = ctx
	a.moveTo = func(src, dst string) error {
		if !strings.HasPrefix(filepath.Base(src), tempPrefix) && !strings.HasPrefix(filepath.Base(dst), tempPrefix) {
			defer cancel() // the swap's middle move completes, then the run stops
		}
		return ops.MoveTo(src, dst)
	}
	a.apply(plan)
	if !a.stopped {
		t.Fatal("the run did not stop")
	}
	for _, p := range tree(t, lib) {
		if strings.Contains(p, tempPrefix) {
			t.Fatalf("a song stays hidden: %s", p)
		}
	}
	if st := runUndo(t, organizeCfg(t, nil, lib, out, false)); st.Failed != 0 {
		t.Errorf("undo: %+v", st)
	}
	if got := tree(t, lib); !reflect.DeepEqual(got, before) {
		t.Errorf("library after undo:\n%s\nwant:\n%s", strings.Join(got, "\n"), strings.Join(before, "\n"))
	}
}

// A run cancelled in a cycle whose parked song cannot go back (the move before the stop
// created its old place as the parent of its own target) and whose planned target lies in
// a folder that no longer exists: the fallback move creates that folder, and the journal
// records it, so -undo removes it again and restores the library.
func TestApplyCancelFallbackCreatesFolderIsJournaled(t *testing.T) {
	noFFprobe(t)
	base := t.TempDir()
	lib, out := filepath.Join(base, "lib"), filepath.Join(base, "out")
	// x wants to go below y's folder and y below x's: y is parked, x moves to B/b/x
	// (creating B/b), then the run stops; y cannot go back to B/b and goes to A/a/y,
	// which needs A/a again (x has moved away from there).
	writeFiles(t, lib, merge(songFiles("A/a", "Ex", "X"), songFiles("B/b", "Why", "Y")))
	before := tree(t, lib)
	cfg := organizeCfg(t, [][]string{{lib}}, lib, out, false)
	ops := fsops.New(false)
	cands := []Candidate{
		{Label: "lib", Rel: "A/a", Folder: filepath.Join(lib, "A", "a"), InLibrary: true},
		{Label: "lib", Rel: "B/b", Folder: filepath.Join(lib, "B", "b"), InLibrary: true},
	}
	plan := Plan{Decisions: []Decision{{Action: ActMove, Target: "B/b/x", Other: -1}, {Action: ActMove, Target: "A/a/y", Other: -1}}}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	a := newApplier(cfg, ops, cands, map[int]moveOutcome{})
	a.ctx = ctx
	a.moveTo = func(src, dst string) error {
		if !strings.HasPrefix(filepath.Base(src), tempPrefix) && !strings.HasPrefix(filepath.Base(dst), tempPrefix) {
			defer cancel() // x's move completes, then the run stops
		}
		return ops.MoveTo(src, dst)
	}
	a.apply(plan)
	if !a.stopped {
		t.Fatal("the run did not stop")
	}
	if _, err := os.Stat(filepath.Join(lib, "A", "a", "y", "song.txt")); err != nil {
		t.Fatalf("y did not reach its planned target: %v\n%s", err, strings.Join(tree(t, lib), "\n"))
	}
	if st := runUndo(t, organizeCfg(t, nil, lib, out, false)); st.Failed != 0 {
		t.Errorf("undo: %+v", st)
	}
	if got := tree(t, lib); !reflect.DeepEqual(got, before) {
		t.Errorf("library after undo:\n%s\nwant:\n%s", strings.Join(got, "\n"), strings.Join(before, "\n"))
	}
}

// Stopping a run stops the ffprobe runs of the inspection instead of waiting for them,
// and a stopped probe is no failure.
func TestRunCancelStopsProbes(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("the fake ffprobe is a shell script")
	}
	dir := t.TempDir()
	script := "#!/bin/sh\nfor a; do [ \"$a\" = -version ] && { echo 'ffprobe version 6.0'; exit 0; }; done\nPATH=/usr/bin:/bin\nsleep 10\n"
	if err := os.WriteFile(filepath.Join(dir, "ffprobe"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir)
	base, a, b := reviewSources(t)
	cfg := organizeCfg(t, [][]string{{a}, {b}}, filepath.Join(base, "library"), filepath.Join(base, "out"), false)
	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel()
	ops := fsops.New(false)
	start := time.Now()
	err := Run(cfg, ops, Options{Ctx: ctx})
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("Run = %v, want context.DeadlineExceeded", err)
	}
	if d := time.Since(start); d > 6*time.Second {
		t.Errorf("Run returned %v after the start; the probes were not stopped", d)
	}
	if st := ops.Stats(); st.Failed != 0 {
		t.Errorf("failed = %d; a stopped probe is no failure", st.Failed)
	}
}
