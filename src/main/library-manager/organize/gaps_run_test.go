package organize

import (
	"bytes"
	"crypto/sha256"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/nkreimeyer/library-manager/fsops"
	"github.com/nkreimeyer/library-manager/internal/testutil"
)

// Only the category folders a run emptied are counted, each once; bucket folders of a
// library that is also a source are not.
func TestRunEmptiedCategoriesCount(t *testing.T) {
	noFFprobe(t)
	base := t.TempDir()
	lib, out := filepath.Join(base, "lib"), filepath.Join(base, "out")
	testutil.WriteFiles(t, lib, merge(
		songFiles("misc/Cher", "Cher", "Believe"),  // -> C/, misc emptied
		songFiles("A/Cat/Zed", "Zed", "Song"),      // -> Z/, A/Cat emptied, A (a bucket) too
		songFiles("B/Blondie", "Blondie", "Maria"), // -> B/Blondie - Maria, B stays a bucket
	))
	cfg := organizeCfg(t, [][]string{{lib}}, lib, out, false)
	_, rep := runOrganize(t, cfg, Options{})
	if rep.Summary.ToLibrary != 3 || rep.Summary.EmptyCategoriesLeft != 2 {
		t.Errorf("summary = %+v, want 3 moves and exactly 2 emptied categories (misc, A/Cat)", rep.Summary)
	}
}

// The report written before the moves must be writable, otherwise nothing moves.
func TestRunPendingReportUnwritable(t *testing.T) {
	noFFprobe(t)
	base := t.TempDir()
	src, lib, out := filepath.Join(base, "src"), filepath.Join(base, "lib"), filepath.Join(base, "out")
	testutil.WriteFiles(t, src, songFiles("ABBA - SOS", "ABBA", "SOS"))
	before := tree(t, src)
	cfg := organizeCfg(t, [][]string{{src}}, lib, out, false)
	testutil.WriteFiles(t, cfg.ReportPath(reportJSON), map[string]string{"x": "a folder where the report goes"})
	ops := fsops.New(false)
	if err := Run(cfg, ops, Options{}); err == nil {
		t.Error("Run did not fail")
	}
	if st := ops.Stats(); st.Moved != 0 {
		t.Errorf("stats = %+v, want nothing moved", st)
	}
	if got := tree(t, src); !reflect.DeepEqual(got, before) {
		t.Errorf("source changed:\n%s", strings.Join(got, "\n"))
	}
	if _, err := os.Stat(cfg.JournalPath()); !os.IsNotExist(err) {
		t.Errorf("journal written: %v", err)
	}
}

// A bucket folder holding media (a broken song folder) takes no songs.
func TestRunBucketHoldsMedia(t *testing.T) {
	noFFprobe(t)
	base := t.TempDir()
	src, lib, out := filepath.Join(base, "src"), filepath.Join(base, "lib"), filepath.Join(base, "out")
	testutil.WriteFiles(t, lib, map[string]string{"A/stray.mp3": "x", "A/notes.txt": "not a song", "A/.hidden.mp4": "x"})
	testutil.WriteFiles(t, src, merge(songFiles("ABBA - SOS", "ABBA", "SOS"), songFiles("Cher", "Cher", "Believe")))
	cfg := organizeCfg(t, [][]string{{src}}, lib, out, false)
	st, rep := runOrganize(t, cfg, Options{})
	if len(rep.LeftInPlace) != 1 || !strings.Contains(rep.LeftInPlace[0].Reason, "the bucket folder A holds the media file stray.mp3") {
		t.Errorf("left in place = %+v", rep.LeftInPlace)
	}
	if st.Moved != 1 || len(rep.ToLibrary) != 1 || rep.ToLibrary[0].Target != "C/Cher - Believe" {
		t.Errorf("stats %+v, to library %+v", st, rep.ToLibrary)
	}
}

func swapSetup(t *testing.T) (lib, out string, cands []Candidate, plan Plan) {
	t.Helper()
	base := t.TempDir()
	lib, out = filepath.Join(base, "lib"), filepath.Join(base, "out")
	testutil.WriteFiles(t, lib, merge(songFiles("A/a", "Bee", "B"), songFiles("B/b", "Abba", "A")))
	cands = []Candidate{
		{Label: "lib", Rel: "A/a", Folder: filepath.Join(lib, "A", "a"), InLibrary: true},
		{Label: "lib", Rel: "B/b", Folder: filepath.Join(lib, "B", "b"), InLibrary: true},
	}
	plan = Plan{Decisions: []Decision{{Action: ActMove, Target: "B/b", Other: -1}, {Action: ActMove, Target: "A/a", Other: -1}}}
	return
}

func tempEntries(t *testing.T, lib string) []string {
	t.Helper()
	entries, err := os.ReadDir(lib)
	if err != nil {
		t.Fatal(err)
	}
	var out []string
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), tempPrefix) {
			out = append(out, e.Name())
		}
	}
	return out
}

// The bucket re-check refuses the final step of a parked song: it is moved back.
func TestApplyBucketRecheckRefusesParkedSong(t *testing.T) {
	noFFprobe(t)
	lib, out, cands, plan := swapSetup(t)
	ops := fsops.New(false)
	a := newApplier(organizeCfg(t, [][]string{{lib}}, lib, out, false), ops, cands, map[int]moveOutcome{})
	a.moveTo = func(src, dst string) error {
		if strings.HasPrefix(filepath.Base(dst), tempPrefix) {
			// After the park, a song txt appears in the bucket A, and the move of A/a fails.
			testutil.WriteFiles(t, lib, map[string]string{"A/song.txt": "#ARTIST:X\n#TITLE:Y\n#BPM:300\n: 0 1 0 a\nE\n"})
		} else if src == cands[0].Folder {
			ops.Fail()
			return errors.New("injected failure")
		}
		return ops.MoveTo(src, dst)
	}
	a.apply(plan)
	if tmp := tempEntries(t, lib); len(tmp) != 0 {
		t.Errorf("song left hidden: %v", tmp)
	}
	if a.outcomes[0].Status != outcomeFailed || a.outcomes[1].Status != outcomeFailed {
		t.Errorf("outcomes = %+v", a.outcomes)
	}
	if data, err := os.ReadFile(filepath.Join(lib, "B", "b", "song.txt")); err != nil || !strings.Contains(string(data), "#ARTIST:Abba") {
		t.Errorf("B/b not moved back: %q %v", data, err)
	}
}

// A bucket that cannot be listed at apply time takes no song, even if it could be written.
func TestApplyBucketUnreadable(t *testing.T) {
	noFFprobe(t)
	if os.Geteuid() == 0 {
		t.Skip("root reads every folder")
	}
	base := t.TempDir()
	src, lib, out := filepath.Join(base, "src"), filepath.Join(base, "lib"), filepath.Join(base, "out")
	testutil.WriteFiles(t, src, songFiles("s", "ABBA", "SOS"))
	bucket := filepath.Join(lib, "A")
	if err := os.MkdirAll(bucket, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(bucket, 0o300); err != nil { // write + search, no read
		t.Fatal(err)
	}
	t.Cleanup(func() { os.Chmod(bucket, 0o755) })
	ops := fsops.New(false)
	cands := []Candidate{{Label: "src", Rel: "s", Folder: filepath.Join(src, "s")}}
	a := newApplier(organizeCfg(t, [][]string{{src}}, lib, out, false), ops, cands, map[int]moveOutcome{})
	a.apply(Plan{Decisions: []Decision{{Action: ActMove, Target: "A/ABBA - SOS", Other: -1}}})
	if a.outcomes[0].Status != outcomeFailed {
		t.Errorf("outcome = %+v", a.outcomes[0])
	}
	if st := ops.Stats(); st.Moved != 0 || st.Failed != 1 {
		t.Errorf("stats = %+v", st)
	}
	if _, err := os.Stat(filepath.Join(src, "s", "song.txt")); err != nil {
		t.Errorf("song moved: %v", err)
	}
}

// A mkdir entry that cannot be journaled stops the move before it runs.
func TestApplyMkdirJournalFailure(t *testing.T) {
	noFFprobe(t)
	base := t.TempDir()
	src, lib, out := filepath.Join(base, "src"), filepath.Join(base, "lib"), filepath.Join(base, "out")
	testutil.WriteFiles(t, src, songFiles("s", "ABBA", "SOS"))
	before := tree(t, src)
	ops := fsops.New(false)
	cands := []Candidate{{Label: "src", Rel: "s", Folder: filepath.Join(src, "s")}}
	a := newApplier(organizeCfg(t, [][]string{{src}}, lib, out, false), ops, cands, map[int]moveOutcome{})
	failingJournal(ops, a.j, func(line string) bool { return strings.Contains(line, `"op":"mkdir"`) })
	a.apply(Plan{Decisions: []Decision{{Action: ActMove, Target: "A/ABBA - SOS", Other: -1}}})
	if a.outcomes[0].Status != outcomeSkipped || !a.j.failed {
		t.Errorf("outcome = %+v, journal failed = %v", a.outcomes[0], a.j.failed)
	}
	if got := tree(t, src); !reflect.DeepEqual(got, before) {
		t.Errorf("source changed:\n%s", strings.Join(got, "\n"))
	}
	if _, err := os.Stat(lib); !os.IsNotExist(err) {
		t.Errorf("library created: %v", err)
	}
}

// The out-dir moves run invalid first, then by Ref; after a journal failure the rest is
// skipped, the library keeper too.
func TestApplyOutDirOrderAfterJournalFailure(t *testing.T) {
	noFFprobe(t)
	base := t.TempDir()
	src, lib, out := filepath.Join(base, "src"), filepath.Join(base, "lib"), filepath.Join(base, "out")
	testutil.WriteFiles(t, src, merge(songFiles("a", "A", "A"), songFiles("i1", "I", "I"), songFiles("i2", "J", "J"), songFiles("k", "K", "K")))
	ops := fsops.New(false)
	var cands []Candidate
	for _, n := range []string{"a", "i2", "i1", "k"} {
		cands = append(cands, Candidate{Label: "src", Rel: n, Folder: filepath.Join(src, n)})
	}
	plan := Plan{Decisions: []Decision{
		{Action: ActDuplicate, Other: 3}, {Action: ActInvalid, Other: -1}, {Action: ActInvalid, Other: -1},
		{Action: ActMove, Target: "K/K - K", Other: -1},
	}}
	a := newApplier(organizeCfg(t, [][]string{{src}}, lib, out, false), ops, cands, map[int]moveOutcome{})
	failingJournal(ops, a.j, func(line string) bool { return strings.Contains(line, `"op":"move"`) })
	a.apply(plan)
	want := map[int]string{0: outcomeSkipped, 1: outcomeSkipped, 2: outcomeMoved, 3: outcomeSkipped}
	for i, w := range want {
		if a.outcomes[i].Status != w {
			t.Errorf("%s: outcome %+v, want %s", cands[i].Ref(), a.outcomes[i], w)
		}
	}
	if a.outcomes[2].Dst != filepath.Join(out, "invalid", "src", "i1") {
		t.Errorf("i1 dst = %q", a.outcomes[2].Dst)
	}
}

// If the final move and the move back of a parked song fail, it stays at the temporary
// name, the park is journaled, and -undo restores the library.
func TestApplyParkedSongStaysThenUndo(t *testing.T) {
	noFFprobe(t)
	lib, out, cands, plan := swapSetup(t)
	before := tree(t, lib)
	ops := fsops.New(false)
	cfg := organizeCfg(t, [][]string{{lib}}, lib, out, false)
	a := newApplier(cfg, ops, cands, map[int]moveOutcome{})
	a.moveTo = func(src, dst string) error {
		if strings.HasPrefix(filepath.Base(src), tempPrefix) {
			ops.Fail()
			return errors.New("injected failure")
		}
		return ops.MoveTo(src, dst)
	}
	a.apply(plan)
	if tmp := tempEntries(t, lib); len(tmp) != 1 {
		t.Errorf("temporary entries = %v, want the parked song", tmp)
	}
	if a.outcomes[0].Status != outcomeMoved || a.outcomes[1].Status != outcomeFailed {
		t.Errorf("outcomes = %+v", a.outcomes)
	}
	if st := runUndo(t, cfg); st.Failed != 0 || st.Moved != 2 {
		t.Errorf("undo: %+v", st)
	}
	if got := tree(t, lib); !reflect.DeepEqual(got, before) {
		t.Errorf("library after undo:\n%s\nwant\n%s", strings.Join(got, "\n"), strings.Join(before, "\n"))
	}
}

// unpark: the journal fails after the swap move, the move back is impossible (the place
// is taken) and the move to the target fails too: the song stays parked and is not
// reported as moved.
func TestApplyUnparkFails(t *testing.T) {
	noFFprobe(t)
	lib, out, cands, plan := swapSetup(t)
	ops := fsops.New(false)
	a := newApplier(organizeCfg(t, [][]string{{lib}}, lib, out, false), ops, cands, map[int]moveOutcome{})
	moves := 0
	failingJournal(ops, a.j, func(line string) bool {
		if strings.Contains(line, `"op":"move"`) {
			moves++
		}
		return moves == 2
	})
	a.moveTo = func(src, dst string) error {
		if strings.HasPrefix(filepath.Base(src), tempPrefix) && dst == filepath.Join(lib, "A", "a") {
			ops.Fail()
			return errors.New("injected failure")
		}
		return ops.MoveTo(src, dst)
	}
	a.apply(plan)
	if tmp := tempEntries(t, lib); len(tmp) != 1 {
		t.Errorf("temporary entries = %v", tmp)
	}
	if a.outcomes[1].Status != outcomeSkipped {
		t.Errorf("parked song outcome = %+v, want skipped", a.outcomes[1])
	}
}

func TestIsEmptyTree(t *testing.T) {
	base := t.TempDir()
	testutil.WriteFiles(t, base, map[string]string{
		"hiddenonly/.git/objects/x": "x",
		"hiddenfile/.DS_Store":      "x",
		"nested/sub/x":              "x",
		"direct/x":                  "x",
	})
	for _, d := range []string{"emptydir", "emptynested/a/b"} {
		if err := os.MkdirAll(filepath.Join(base, d), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	for dir, want := range map[string]bool{
		"hiddenonly": true, "hiddenfile": true, "emptydir": true, "emptynested": true,
		"nested": false, "direct": false,
	} {
		if got := isEmptyTree(filepath.Join(base, dir)); got != want {
			t.Errorf("isEmptyTree(%s) = %v, want %v", dir, got, want)
		}
	}
	if os.Geteuid() != 0 {
		locked := filepath.Join(base, "locked", "sub")
		if err := os.MkdirAll(locked, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.Chmod(locked, 0); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { os.Chmod(locked, 0o755) })
		if isEmptyTree(filepath.Join(base, "locked")) {
			t.Error("a folder with an unreadable subfolder counts as empty")
		}
	}
}

// The last #ARTIST wins, error codes are sorted and unique.
func TestRunCandidateTagsAndErrors(t *testing.T) {
	noFFprobe(t)
	base := t.TempDir()
	src, lib, out := filepath.Join(base, "src"), filepath.Join(base, "lib"), filepath.Join(base, "out")
	testutil.WriteFiles(t, src, merge(
		songFiles("s", "Old", "Song", "#ARTIST:New"),
		map[string]string{
			"broken/song.txt": "#ARTIST:Q\n#TITLE:T\n#MP3:song.mp3\n#BPM:300\n#GAP:x\n: 0 x 0 a\n: 4 y 0 b\nE\n",
			"broken/song.mp3": "x",
		},
	))
	cfg := organizeCfg(t, [][]string{{src}}, lib, out, true)
	_, rep := runOrganize(t, cfg, Options{})
	if len(rep.ToLibrary) != 1 || rep.ToLibrary[0].Target != "N/New - Song" {
		t.Errorf("to library = %+v", rep.ToLibrary)
	}
	if len(rep.Invalid) != 1 {
		t.Fatalf("invalid = %+v", rep.Invalid)
	}
	if got, want := rep.Invalid[0].Errors, []string{"bad-gap", "malformed-note", "no-notes"}; !reflect.DeepEqual(got, want) {
		t.Errorf("errors = %q, want %q", got, want)
	}
}

// Undo keeps a folder the run created when something was put into it since.
func TestUndoKeepsUserFileInCreatedBucket(t *testing.T) {
	noFFprobe(t)
	base := t.TempDir()
	src, lib, out := filepath.Join(base, "src"), filepath.Join(base, "lib"), filepath.Join(base, "out")
	testutil.WriteFiles(t, src, songFiles("ABBA - SOS", "ABBA", "SOS"))
	before := tree(t, src)
	cfg := organizeCfg(t, [][]string{{src}}, lib, out, false)
	runOrganize(t, cfg, Options{})
	testutil.WriteFiles(t, lib, map[string]string{"A/user.txt": "mine"})
	if st := runUndo(t, cfg); st.Moved != 1 || st.Failed != 0 {
		t.Errorf("undo: %+v", st)
	}
	if got := tree(t, src); !reflect.DeepEqual(got, before) {
		t.Errorf("source after undo:\n%s", strings.Join(got, "\n"))
	}
	if got, want := files(t, lib), []string{fmt.Sprintf("A/user.txt %x", sha256.Sum256([]byte("mine")))}; !reflect.DeepEqual(got, want) {
		t.Errorf("library after undo: %q, want %q", got, want)
	}
}

func TestUndoJournalUnreadable(t *testing.T) {
	base := t.TempDir()
	cfg := organizeCfg(t, nil, filepath.Join(base, "lib"), filepath.Join(base, "out"), false)
	testutil.WriteFiles(t, cfg.JournalPath(), map[string]string{"x": "x"})
	err := Undo(cfg, fsops.New(false), UndoOptions{})
	if err == nil || !strings.Contains(err.Error(), "reading the journal") {
		t.Errorf("Undo = %v", err)
	}
}

func TestReadJournalSkips(t *testing.T) {
	path := filepath.Join(t.TempDir(), "organize-journal.jsonl")
	data := `{"op":"run","run":"r1","time":"t1"}
{"op":"mkdir","run":"r1","dst":"/lib/A"}

{"op":"mkdir","run":"r1"}
{"op":"bogus","run":"r1","src":"/x","dst":"/y"}
{"op":"move","run":"r1","src":"/s/a","dst":"/lib/A/a"}
{"op":"move","src":"/s/z","dst":"/lib/Z/z"}
{"op":"run","run":"r5","time":"t5"}
{"op":"mkdir","run":"r5","dst":"/lib/B"}
`
	if err := os.WriteFile(path, []byte(data), 0o644); err != nil {
		t.Fatal(err)
	}
	runs, err := readJournal(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(runs) != 2 || runs[0].ID != "r1" || runs[0].Time != "t1" || len(runs[0].Entries) != 2 || runs[1].ID != "r5" {
		t.Fatalf("runs = %+v", runs)
	}
	if r := lastUndoable(runs); r == nil || r.ID != "r1" {
		t.Errorf("last undoable = %+v, want r1 (r5 has no moves)", r)
	}
	// A line longer than the scanner buffer is an error, not a silent skip.
	if err := os.WriteFile(path, append(bytes.Repeat([]byte("x"), 16*1024*1024+1), '\n'), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := readJournal(path); err == nil {
		t.Error("oversized line: no error")
	}
}

// One run line per run; a run that moves nothing leaves the journal untouched.
func TestJournalRunLineOnce(t *testing.T) {
	noFFprobe(t)
	base := t.TempDir()
	src, lib, out := filepath.Join(base, "src"), filepath.Join(base, "lib"), filepath.Join(base, "out")
	testutil.WriteFiles(t, src, merge(songFiles("ABBA - SOS", "ABBA", "SOS"), songFiles("Cher", "Cher", "Believe")))
	if err := os.MkdirAll(lib, 0o755); err != nil {
		t.Fatal(err)
	}
	cfg := organizeCfg(t, [][]string{{src}, {lib}}, lib, out, false)
	if st, _ := runOrganize(t, cfg, Options{}); st.Moved != 2 {
		t.Fatalf("run: %+v", st)
	}
	j1, err := os.ReadFile(cfg.JournalPath())
	if err != nil {
		t.Fatal(err)
	}
	if n := strings.Count(string(j1), `"op":"run"`); n != 1 {
		t.Errorf("%d run lines:\n%s", n, j1)
	}
	if st, rep := runOrganize(t, cfg, Options{}); st.Moved != 0 || rep.Summary.AlreadyInPlace != 2 {
		t.Fatalf("second run: %+v %+v", st, rep.Summary)
	}
	if j2, _ := os.ReadFile(cfg.JournalPath()); !bytes.Equal(j1, j2) {
		t.Errorf("idempotent run changed the journal:\n%s", j2)
	}
}

// Parallel inspection gives the same plan as a single worker.
func TestRunParallelInspection(t *testing.T) {
	noFFprobe(t)
	base := t.TempDir()
	hqDir, low1Dir, low2Dir := threeSources(t, base)
	lib := filepath.Join(base, "library")
	tiers := [][]string{{hqDir}, {low1Dir, low2Dir}}
	old := workers
	t.Cleanup(func() { workers = old })
	var reps []planReport
	for _, w := range []int{1, 8} {
		workers = w
		_, rep := runOrganize(t, organizeCfg(t, tiers, lib, filepath.Join(base, fmt.Sprintf("out%d", w)), true), Options{Priority: true})
		reps = append(reps, rep)
	}
	if !reflect.DeepEqual(reps[0], reps[1]) {
		t.Errorf("plans differ:\n%+v\n%+v", reps[0], reps[1])
	}
}
