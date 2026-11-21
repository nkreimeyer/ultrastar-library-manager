package organize

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/nkreimeyer/library-manager/config"
	"github.com/nkreimeyer/library-manager/fsops"
)

// addWithBetterSource sets up `add` of a source whose copy of the library's song is better
// (it has a video): the library copy loses and goes to duplicates/, the source copy is to
// take its place.
func addWithBetterSource(t *testing.T, dry bool) (cfg config.OrganizeConfig, lib, src string, before []string) {
	t.Helper()
	noFFprobe(t)
	base := t.TempDir()
	lib, src = filepath.Join(base, "lib"), filepath.Join(base, "usb", "songs")
	writeFiles(t, lib, songFiles("A/ABBA - SOS", "ABBA", "SOS"))
	writeFiles(t, src, songFiles("ABBA - SOS", "ABBA", "SOS", "video"))
	cfg = prepared(t)(config.NewAdd([][]string{{src}}, lib, filepath.Join(base, "out"), dry))
	return cfg, lib, src, tree(t, lib)
}

func duplicateOutcome(rep planReport, song string) string {
	for _, d := range rep.Duplicates {
		if d.Song == song {
			return d.Outcome
		}
	}
	return "<not a duplicate>"
}

// The library's copy goes to duplicates/ before the better copy's move into the library,
// which then fails (here: the source cannot be written). The library copy is moved back,
// so the library still has the song, and -undo still reverts the whole run.
func TestApplyRestoresLibrarySongWhenKeeperMoveFails(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root ignores permissions")
	}
	cfg, lib, src, before := addWithBetterSource(t, false)
	if err := os.Chmod(src, 0o555); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.Chmod(src, 0o755) })

	st, rep := runOrganize(t, cfg, Options{Mode: ModeAdd})
	if got := tree(t, lib); !reflect.DeepEqual(got, before) {
		t.Errorf("library:\n%s\nwant:\n%s", strings.Join(got, "\n"), strings.Join(before, "\n"))
	}
	if o := duplicateOutcome(rep, "lib:A/ABBA - SOS"); o != outcomeRestored {
		t.Errorf("library copy outcome = %q, want %q; report %+v", o, outcomeRestored, rep.Duplicates)
	}
	if len(rep.ToLibrary) != 1 || rep.ToLibrary[0].Outcome != outcomeFailed {
		t.Errorf("to library = %+v, want the failed keeper", rep.ToLibrary)
	}
	if st.Failed != 1 || st.Moved != 2 {
		t.Errorf("stats = %+v, want the failed keeper move, the move out and the move back", st)
	}
	if text := string(rep.text()); !strings.Contains(text, "[moved back]") {
		t.Errorf("text report does not show the move back:\n%s", text)
	}
	if err := os.Chmod(src, 0o755); err != nil {
		t.Fatal(err)
	}
	if st := runUndo(t, cfg); st.Failed != 0 || st.Moved != 2 {
		t.Errorf("undo: %+v, want both moves reverted", st)
	}
	if got := tree(t, lib); !reflect.DeepEqual(got, before) {
		t.Errorf("library after undo:\n%s", strings.Join(got, "\n"))
	}
	if got := files(t, cfg.DuplicatesDir()); len(got) != 0 {
		t.Errorf("files in duplicates/ after undo: %v", got)
	}
}

// A run stopped between the move out and the keeper's move moves the library copy back
// too, in a dry run as in a real one.
func TestApplyRestoresLibrarySongAfterStop(t *testing.T) {
	for _, dry := range []bool{false, true} {
		t.Run(fmt.Sprintf("dry=%v", dry), func(t *testing.T) {
			cfg, lib, src, before := addWithBetterSource(t, dry)
			srcBefore := tree(t, src)
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			progress := func(phase string, done, _ int) {
				if phase == PhaseMove && done == 1 {
					cancel() // the move out runs, the keeper's move is not attempted
				}
			}
			ops := fsops.New(dry)
			if err := Run(cfg, ops, Options{Mode: ModeAdd, Ctx: ctx, Progress: progress}); !errors.Is(err, context.Canceled) {
				t.Fatalf("Run = %v, want context.Canceled", err)
			}
			if got := tree(t, lib); !reflect.DeepEqual(got, before) {
				t.Errorf("library:\n%s\nwant:\n%s", strings.Join(got, "\n"), strings.Join(before, "\n"))
			}
			if got := tree(t, src); !reflect.DeepEqual(got, srcBefore) {
				t.Errorf("source changed:\n%s", strings.Join(got, "\n"))
			}
			if st := ops.Stats(); st.Moved != 2 || st.Failed != 0 {
				t.Errorf("stats = %+v, want the move out and back", st)
			}
			data, err := os.ReadFile(cfg.ReportPath(reportJSON))
			if err != nil {
				t.Fatal(err)
			}
			if !strings.Contains(string(data), `"outcome": "`+outcomeRestored+`"`) {
				t.Errorf("report does not show the move back:\n%s", data)
			}
		})
	}
}

// Which losers are moved back when their keeper's move fails: library duplicates always,
// broken library songs only when the library stays in place (dedupe, add; organize sends
// broken songs to invalid/ whatever happens to their replacement), never songs from
// other sources, and nothing whose keeper is a library song (it is still in the library).
func TestRestoreLibraryLosersSelection(t *testing.T) {
	for _, inPlace := range []bool{false, true} {
		t.Run(fmt.Sprintf("inPlace=%v", inPlace), func(t *testing.T) {
			noFFprobe(t)
			base := t.TempDir()
			lib, src, out := filepath.Join(base, "lib"), filepath.Join(base, "src"), filepath.Join(base, "out")
			writeFiles(t, lib, merge(songFiles("A/dup", "A", "S"), songFiles("A/broken", "A", "T"),
				songFiles("B/kept", "B", "K"), songFiles("B/lost", "B", "K")))
			writeFiles(t, src, merge(songFiles("keeper1", "A", "S"), songFiles("keeper2", "A", "T"), songFiles("other", "A", "S")))
			cfg := organizeCfg(t, [][]string{{lib}, {src}}, lib, out, false)
			c := func(label, root, rel string, inLib bool) Candidate {
				return Candidate{Label: label, Rel: rel, Folder: filepath.Join(root, filepath.FromSlash(rel)), InLibrary: inLib}
			}
			cands := []Candidate{
				c("lib", lib, "A/dup", true),    // 0: loses to 4
				c("lib", lib, "A/broken", true), // 1: broken, replaced by 5
				c("lib", lib, "B/kept", true),   // 2: library keeper, its move fails
				c("lib", lib, "B/lost", true),   // 3: loses to 2
				c("src", src, "keeper1", false), // 4
				c("src", src, "keeper2", false), // 5
				c("src", src, "other", false),   // 6: loses to 4
			}
			plan := Plan{Decisions: []Decision{
				{Action: ActDuplicate, Other: 4},
				{Action: ActInvalid, Other: 5},
				{Action: ActMove, Target: "K/kept", Other: -1},
				{Action: ActDuplicate, Other: 2},
				{Action: ActMove, Target: "A/keeper1", Other: -1},
				{Action: ActMove, Target: "A/keeper2", Other: -1},
				{Action: ActDuplicate, Other: 4},
			}}
			ops := fsops.New(false)
			a := newApplier(cfg, ops, cands, map[int]moveOutcome{})
			a.libraryInPlace = inPlace
			a.moveTo = func(s, d string) error {
				if strings.HasPrefix(d, lib) && (s == cands[2].Folder || s == cands[4].Folder || s == cands[5].Folder) {
					ops.Fail()
					return errors.New("injected failure")
				}
				return ops.MoveTo(s, d)
			}
			a.apply(plan)
			want := map[int]string{0: outcomeRestored, 1: outcomeMoved, 3: outcomeMoved, 6: outcomeMoved}
			if inPlace {
				want[1] = outcomeRestored
			}
			for i, w := range want {
				if got := a.outcomes[i].Status; got != w {
					t.Errorf("candidate %d (%s): outcome %q, want %q", i, cands[i].Rel, got, w)
				}
				if _, err := os.Stat(cands[i].Folder); (err == nil) != (w == outcomeRestored) {
					t.Errorf("candidate %d (%s) in its place: %v, want %v", i, cands[i].Rel, err == nil, w == outcomeRestored)
				}
			}
		})
	}
}

// An undo with a move that fails for a reason that can be fixed (here: the original place
// is taken) does not record the run as undone: the next undo retries the same run,
// skipping what is back already, instead of reverting the run before it.
func TestUndoRetriesRunWithFailedMoves(t *testing.T) {
	noFFprobe(t)
	base := t.TempDir()
	src1, src2, lib, out := filepath.Join(base, "src1"), filepath.Join(base, "src2"), filepath.Join(base, "lib"), filepath.Join(base, "out")
	writeFiles(t, src1, songFiles("a", "Abba", "SOS"))
	writeFiles(t, src2, merge(songFiles("b", "Bee Gees", "Tragedy"), songFiles("c", "Cher", "Believe")))
	runOrganize(t, organizeCfg(t, [][]string{{src1}}, lib, out, false), Options{})
	runOrganize(t, organizeCfg(t, [][]string{{src2}}, lib, out, false), Options{})
	undoCfg := organizeCfg(t, nil, lib, out, false)
	runs, err := ListRuns(undoCfg)
	if err != nil || len(runs) != 2 {
		t.Fatalf("runs = %+v, %v", runs, err)
	}
	second := runs[1].ID

	blocker := filepath.Join(src2, "b")
	writeFiles(t, blocker, map[string]string{"new.txt": "put here since"})
	if st := runUndo(t, undoCfg); st.Moved != 1 || st.Failed != 1 {
		t.Errorf("undo with a blocked move: %+v, want c moved back and b failed", st)
	}
	if runs, _ = ListRuns(undoCfg); runs[1].Undone || !runs[1].Next || runs[1].ID != second {
		t.Fatalf("runs after the partial undo = %+v, want the second run still next", runs)
	}
	// Still blocked: c is skipped (back already), b fails again, the first run is untouched.
	if st := runUndo(t, undoCfg); st.Moved != 0 || st.Failed != 1 {
		t.Errorf("undo still blocked: %+v", st)
	}
	if _, err := os.Stat(filepath.Join(lib, "A", "Abba - SOS")); err != nil {
		t.Errorf("the first run's song was moved: %v", err)
	}
	if err := os.RemoveAll(blocker); err != nil {
		t.Fatal(err)
	}
	// A dry run plans exactly the one move left.
	if st := runUndo(t, organizeCfg(t, nil, lib, out, true)); st.Moved != 1 || st.Failed != 0 {
		t.Errorf("dry-run retry: %+v", st)
	}
	if st := runUndo(t, undoCfg); st.Moved != 1 || st.Failed != 0 {
		t.Errorf("retry: %+v", st)
	}
	if _, err := os.Stat(filepath.Join(src2, "b", "song.txt")); err != nil {
		t.Errorf("b not back: %v", err)
	}
	if runs, _ = ListRuns(undoCfg); !runs[1].Undone || !runs[0].Next {
		t.Errorf("runs after the retry = %+v, want the second undone and the first next", runs)
	}
	if st := runUndo(t, undoCfg); st.Moved != 1 || st.Failed != 0 {
		t.Errorf("undo of the first run: %+v", st)
	}
	if _, err := os.Stat(filepath.Join(src1, "a", "song.txt")); err != nil {
		t.Errorf("a not back: %v", err)
	}
}

// A dry-run undo of a run whose moves chain through a temporary name (two library songs
// swapping names) plans every move: the skip of moves that are back already must follow
// the planned moves, not the disk.
func TestUndoDryRunOfSwapPlansEveryMove(t *testing.T) {
	noFFprobe(t)
	base := t.TempDir()
	lib, out := filepath.Join(base, "lib"), filepath.Join(base, "out")
	// Each folder holds the song the other one's name belongs to.
	writeFiles(t, lib, merge(songFiles("A/Abba - A", "Bee", "B"), songFiles("B/Bee - B", "Abba", "A")))
	cfg := organizeCfg(t, [][]string{{lib}}, lib, out, false)
	st, _ := runOrganize(t, cfg, Options{})
	if st.Moved != 3 || st.Failed != 0 {
		t.Fatalf("run: %+v, want a swap through a temporary name (3 moves)", st)
	}
	after := tree(t, lib)
	if dst := runUndo(t, organizeCfg(t, nil, lib, out, true)); dst.Moved != 3 || dst.Failed != 0 {
		t.Errorf("dry-run undo: %+v, want 3 planned moves", dst)
	}
	if got := tree(t, lib); !reflect.DeepEqual(got, after) {
		t.Error("dry-run undo changed the library")
	}
	if rst := runUndo(t, organizeCfg(t, nil, lib, out, false)); rst.Moved != 3 || rst.Failed != 0 {
		t.Errorf("undo: %+v", rst)
	}
}

// A solo txt whose notes follow a single "P1" marker is the same song as the one without
// it: ContentHash already treats it as solo, and so does the duet flag, so the copies are
// joined and the keeper is not filed as "... [DUET]".
func TestSingleP1MarkerIsNoDuet(t *testing.T) {
	noFFprobe(t)
	base := t.TempDir()
	lib, src, out := filepath.Join(base, "lib"), filepath.Join(base, "src"), filepath.Join(base, "out")
	writeFiles(t, lib, songFiles("A/ABBA - SOS", "ABBA", "SOS"))
	marked := songFiles("SOS with P1", "ABBA", "SOS")
	txt := marked["SOS with P1/song.txt"]
	i := strings.Index(txt, ": 0 ")
	marked["SOS with P1/song.txt"] = txt[:i] + "P1\n" + txt[i:]
	writeFiles(t, src, marked)

	_, rep := runOrganize(t, prepared(t)(config.NewAdd([][]string{{src}}, lib, out, true)), Options{Mode: ModeAdd})
	if len(rep.Duplicates) != 1 || rep.Duplicates[0].Song != "src:SOS with P1" {
		t.Errorf("duplicates = %+v, want the P1 copy as a duplicate of the library song", rep.Duplicates)
	}
	if len(rep.ToLibrary) != 0 {
		t.Errorf("to library = %+v, want nothing (no \"[DUET]\" copy)", rep.ToLibrary)
	}
}

// A retry skips the moves an earlier undo of the run did. The paths cannot tell: after a
// swap is undone, both folders exist again, and moving them "back" once more would redo the
// swap. The earlier undo here lost its "undone" line, as when it stopped or a move failed.
func TestUndoRetrySkipsRevertedSwap(t *testing.T) {
	noFFprobe(t)
	base := t.TempDir()
	lib, out := filepath.Join(base, "lib"), filepath.Join(base, "out")
	writeFiles(t, lib, merge(songFiles("A/Abba - A", "Bee", "B"), songFiles("B/Bee - B", "Abba", "A")))
	before := tree(t, lib)
	runOrganize(t, organizeCfg(t, [][]string{{lib}}, lib, out, false), Options{})
	cfg := organizeCfg(t, nil, lib, out, false)
	if st := runUndo(t, cfg); st.Moved != 3 || st.Failed != 0 {
		t.Fatalf("undo: %+v", st)
	}
	data, err := os.ReadFile(cfg.JournalPath())
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.SplitAfter(strings.TrimSuffix(string(data), "\n"), "\n")
	if last := lines[len(lines)-1]; !strings.Contains(last, `"op":"undone"`) {
		t.Fatalf("last journal line %q", last)
	}
	if err := os.WriteFile(cfg.JournalPath(), []byte(strings.Join(lines[:len(lines)-1], "")), 0o644); err != nil {
		t.Fatal(err)
	}
	if st := runUndo(t, cfg); st.Moved != 0 || st.Failed != 0 {
		t.Errorf("retry: %+v, want nothing moved", st)
	}
	if got := tree(t, lib); !reflect.DeepEqual(got, before) {
		t.Errorf("after the retry:\n%v\nwant:\n%v", got, before)
	}
	runs, _ := readJournal(cfg.JournalPath())
	if len(runs) != 1 || !runs[0].Undone || len(runs[0].Reverted) != 3 {
		t.Errorf("journal: %+v", runs[0])
	}
}

// A stopped undo is not recorded as undone: the next undo finishes the same run. It never
// stops while a song is at a temporary name: in the swap, the song parked by one step
// comes out only two steps later. Every stop point is tried.
func TestUndoStopped(t *testing.T) {
	stoppedOnce, keptGoing := false, false
	total := 2
	for stopAt := 1; stopAt < total; stopAt++ {
		t.Run(fmt.Sprint(stopAt), func(t *testing.T) {
			noFFprobe(t)
			base := t.TempDir()
			lib, out := filepath.Join(base, "lib"), filepath.Join(base, "out")
			writeFiles(t, lib, merge(songFiles("A/Abba - A", "Bee", "B"), songFiles("B/Bee - B", "Abba", "A"), songFiles("Pop/Cee - C", "Cee", "C")))
			before := tree(t, lib)
			runOrganize(t, organizeCfg(t, [][]string{{lib}}, lib, out, false), Options{})
			cfg := organizeCfg(t, nil, lib, out, false)
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			last := 0
			err := Undo(cfg, fsops.New(false), UndoOptions{Ctx: ctx, Progress: func(phase string, done, n int) {
				if phase != PhaseUndo {
					t.Errorf("phase %q", phase)
				}
				total, last = n, done
				if done == stopAt {
					cancel()
				}
			}})
			for _, rel := range tree(t, lib) {
				if strings.Contains(rel, tempPrefix) {
					t.Errorf("%s is left under its temporary name", rel)
				}
			}
			switch {
			case errors.Is(err, context.Canceled):
				stoppedOnce = true
				if last != stopAt {
					keptGoing = true // a song was parked at the stop
				}
				runUndo(t, cfg)
			case err != nil:
				t.Fatal(err)
			default:
				keptGoing = true // the stop came while parked, and then the run was done
			}
			if got := tree(t, lib); !reflect.DeepEqual(got, before) {
				t.Errorf("after the undo:\n%v\nwant:\n%v", got, before)
			}
		})
	}
	if !stoppedOnce || !keptGoing {
		t.Errorf("of %d stop points: stopped %v, kept going while parked %v", total-1, stoppedOnce, keptGoing)
	}
}

// An undo whose journal cannot record the moves back reports an error: the run is not
// recorded as undone either, and a retry could repeat the moves (here a swap).
func TestUndoJournalUnwritable(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root writes a read-only file")
	}
	noFFprobe(t)
	base := t.TempDir()
	lib, out := filepath.Join(base, "lib"), filepath.Join(base, "out")
	writeFiles(t, lib, merge(songFiles("A/Abba - A", "Bee", "B"), songFiles("B/Bee - B", "Abba", "A")))
	before := tree(t, lib)
	runOrganize(t, organizeCfg(t, [][]string{{lib}}, lib, out, false), Options{})
	cfg := organizeCfg(t, nil, lib, out, false)
	if err := os.Chmod(cfg.JournalPath(), 0o444); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.Chmod(cfg.JournalPath(), 0o644) })
	err := Undo(cfg, fsops.New(false), UndoOptions{})
	if err == nil || !strings.Contains(err.Error(), "could repeat them") {
		t.Errorf("Undo = %v, want the unrecorded moves reported", err)
	}
	if got := tree(t, lib); !reflect.DeepEqual(got, before) {
		t.Errorf("the moves back were not done:\n%v", got)
	}
}

// A song whose move out of the temporary name fails stays there, and a stop asked for
// while it was parked applies after that entry: the rest of the run is not undone.
func TestUndoStopAfterFailedUnpark(t *testing.T) {
	noFFprobe(t)
	base := t.TempDir()
	lib, out := filepath.Join(base, "lib"), filepath.Join(base, "out")
	writeFiles(t, lib, merge(songFiles("A/Abba - A", "Bee", "B"), songFiles("B/Bee - B", "Abba", "A"), songFiles("Pop/Cee - C", "Cee", "C")))
	runOrganize(t, organizeCfg(t, [][]string{{lib}}, lib, out, false), Options{})
	cfg := organizeCfg(t, nil, lib, out, false)
	runs, _ := readJournal(cfg.JournalPath())
	if e := runs[0].Entries; len(e) < 4 || e[len(e)-1].Op != opMove || !strings.Contains(e[len(e)-1].Src, tempPrefix) || !strings.Contains(e[len(e)-1].Dst, "Abba - A") {
		t.Fatalf("want the swap journaled after the move of C: %+v", e)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	err := Undo(cfg, fsops.New(false), UndoOptions{Ctx: ctx, Progress: func(_ string, done, _ int) {
		if done == 1 { // Abba - A is parked: block its way back, and stop
			cancel()
			if err := os.MkdirAll(filepath.Join(lib, "A", "Abba - A"), 0o755); err != nil {
				t.Fatal(err)
			}
		}
	}})
	if !errors.Is(err, context.Canceled) {
		t.Errorf("Undo = %v, want it stopped", err)
	}
	if _, err := os.Stat(filepath.Join(lib, "C", "Cee - C")); err != nil {
		t.Errorf("the move of C was undone after the stop: %v", err)
	}
}
