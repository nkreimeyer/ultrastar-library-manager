package organize

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	log "github.com/sirupsen/logrus"

	"github.com/nkreimeyer/library-manager/config"
	"github.com/nkreimeyer/library-manager/fsops"
	"github.com/nkreimeyer/library-manager/internal/testutil"
)

// ---- restore.go ----

// An item whose journaled origin lies inside the output folder is blocked, and Restore
// counts a failure and moves nothing (it would nest the output folder in itself).
func TestRestoreOriginInsideOutDirIsBlocked(t *testing.T) {
	cfg, lib, out := restoreSetup(t)
	item := filepath.Join(out, "duplicates", "library", "X - Y")
	testutil.WriteFiles(t, item, map[string]string{"song.txt": "#ARTIST:X\n#TITLE:Y\n"})
	line, _ := json.Marshal(journalEntry{Op: opMove, Run: "r1", Src: filepath.Join(out, "elsewhere", "X - Y"), Dst: item})
	testutil.WriteFile(t, cfg.JournalPath(), `{"op":"run","run":"r1"}`+"\n"+string(line)+"\n")
	before, outBefore := testutil.Tree(t, lib), testutil.Tree(t, out)

	items, err := ListSetAside(cfg, fsops.New(true))
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 1 || !items[0].Exact || items[0].Blocked != blockedOutside {
		t.Fatalf("items = %+v, want one blocked with %q", items, blockedOutside)
	}
	ops := fsops.New(false)
	if err := Restore(cfg, ops, []RestoreRef{{items[0].Section, items[0].Rel}}, RestoreOptions{}); err != nil {
		t.Fatal(err)
	}
	if st := ops.Stats(); st.Moved != 0 || st.Failed != 1 {
		t.Errorf("stats = %+v", st)
	}
	if !reflect.DeepEqual(testutil.Tree(t, lib), before) || !reflect.DeepEqual(testutil.Tree(t, out), outBefore) {
		t.Error("something was moved")
	}
}

// A journal that cannot be read is an error of ListSetAside and Restore; unparseable
// lines in it are only skipped.
func TestRestoreCorruptJournal(t *testing.T) {
	cfg, _, out := restoreSetup(t)
	testutil.WriteFile(t, filepath.Join(out, "extra-files", "A", "ABBA - SOS", "old.jpg"), "img")
	refs := []RestoreRef{{SectionExtraFiles, "A/ABBA - SOS/old.jpg"}}

	testutil.WriteFile(t, cfg.JournalPath(), "garbage\n{\"op\":\n")
	if items, err := ListSetAside(cfg, fsops.New(true)); err != nil || len(items) != 1 {
		t.Errorf("garbage lines: %v, %v", items, err)
	}
	if err := os.Remove(cfg.JournalPath()); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(cfg.JournalPath(), 0o755); err != nil { // cannot be read as a file
		t.Fatal(err)
	}
	if _, err := ListSetAside(cfg, fsops.New(true)); err == nil || !strings.Contains(err.Error(), "reading the journal") {
		t.Errorf("ListSetAside = %v", err)
	}
	ops := fsops.New(false)
	if err := Restore(cfg, ops, refs, RestoreOptions{}); err == nil || !strings.Contains(err.Error(), "reading the journal") {
		t.Errorf("Restore = %v", err)
	}
	if st := ops.Stats(); st.Moved != 0 {
		t.Errorf("stats = %+v", st)
	}
}

// The journal cannot be written: at the run line nothing is moved, later (mkdir, move
// entry) Restore stops with an error and does not go on.
func TestRestoreJournalUnwritable(t *testing.T) {
	testutil.SkipIfRoot(t)
	setup := func(t *testing.T) (config.OrganizeConfig, string, string) {
		cfg, lib, out := restoreSetup(t)
		testutil.WriteFile(t, filepath.Join(out, "extra-files", "A", "ABBA - SOS", "one.jpg"), "1")
		testutil.WriteFile(t, filepath.Join(out, "extra-files", "A", "ABBA - SOS", "two.jpg"), "2")
		testutil.WriteFile(t, filepath.Join(out, "extra-files", "Z", "New - Folder", "three.jpg"), "3")
		return cfg, lib, out
	}
	lockJournal := func(t *testing.T, cfg config.OrganizeConfig) {
		if err := os.Chmod(cfg.JournalPath(), 0o444); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { os.Chmod(cfg.JournalPath(), 0o644) })
	}
	const want = "cannot be written, so nothing more is put back"

	t.Run("run line", func(t *testing.T) {
		cfg, lib, _ := setup(t)
		testutil.WriteFile(t, cfg.JournalPath(), "")
		lockJournal(t, cfg)
		before := testutil.Tree(t, lib)
		err := Restore(cfg, fsops.New(false), []RestoreRef{{SectionExtraFiles, "A/ABBA - SOS/one.jpg"}}, RestoreOptions{})
		if err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("Restore = %v", err)
		}
		if !reflect.DeepEqual(testutil.Tree(t, lib), before) {
			t.Error("a song was moved without a journal")
		}
	})
	// The first item is put back; then the journal is locked (in the Progress call before
	// the second item).
	second := func(t *testing.T, rel string) (error, config.OrganizeConfig, string, string) {
		cfg, lib, out := setup(t)
		refs := []RestoreRef{{SectionExtraFiles, "A/ABBA - SOS/one.jpg"}, {SectionExtraFiles, rel}}
		err := Restore(cfg, fsops.New(false), refs, RestoreOptions{Progress: func(n, _ int) {
			if n == 1 {
				lockJournal(t, cfg)
			}
		}})
		if !testutil.Exists(filepath.Join(lib, "A", "ABBA - SOS", "one.jpg")) {
			t.Error("the first item was not put back")
		}
		return err, cfg, lib, out
	}
	t.Run("mkdir", func(t *testing.T) {
		err, _, lib, out := second(t, "Z/New - Folder/three.jpg")
		if err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("Restore = %v", err)
		}
		if testutil.Exists(filepath.Join(lib, "Z")) || !testutil.Exists(filepath.Join(out, "extra-files", "Z", "New - Folder", "three.jpg")) {
			t.Error("the second item was moved although its folder could not be journaled")
		}
	})
	t.Run("move entry", func(t *testing.T) {
		err, _, lib, _ := second(t, "A/ABBA - SOS/two.jpg")
		if err == nil || !strings.Contains(err.Error(), "is not in the journal") {
			t.Errorf("Restore = %v", err)
		}
		if !testutil.Exists(filepath.Join(lib, "A", "ABBA - SOS", "two.jpg")) {
			t.Error("the move itself should have happened (and the error names it)")
		}
	})
}

// A failing move is a failed item (counted), the next items still go back, and the failed
// one has no move entry in the journal. An item that vanished after the listing is
// reported as gone. Progress ends with (n, n).
func TestRestoreMoveFailsVanishedAndProgress(t *testing.T) {
	testutil.SkipIfRoot(t)
	cfg, lib, out := restoreSetup(t)
	ex := func(rel string) string { return filepath.Join(out, "extra-files", filepath.FromSlash(rel)) }
	testutil.WriteFile(t, ex("A/ABBA - SOS/locked.jpg"), "1")
	testutil.WriteFile(t, ex("A/ABBA - SOS/vanish.jpg"), "2")
	testutil.WriteFile(t, ex("Q/Queen - Rock/ok.jpg"), "3")
	locked := filepath.Join(lib, "A", "ABBA - SOS")
	if err := os.Chmod(locked, 0o555); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.Chmod(locked, 0o755) })
	refs := []RestoreRef{
		{SectionExtraFiles, "A/ABBA - SOS/locked.jpg"}, // the move fails: the folder is read-only
		{SectionExtraFiles, "A/ABBA - SOS/vanish.jpg"},
		{SectionExtraFiles, "Q/Queen - Rock/ok.jpg"},
	}
	type call struct{ n, total int }
	var calls []call
	var logs []string
	ops := fsops.New(false)
	logs = testutil.ErrorLogs(t, func() {
		err := Restore(cfg, ops, refs, RestoreOptions{Progress: func(n, total int) {
			calls = append(calls, call{n, total})
			if n == 1 {
				os.Remove(ex("A/ABBA - SOS/vanish.jpg"))
			}
		}})
		if err != nil {
			t.Errorf("Restore = %v", err)
		}
	})
	if want := []call{{0, 3}, {1, 3}, {2, 3}, {3, 3}}; !reflect.DeepEqual(calls, want) {
		t.Errorf("progress = %v, want %v", calls, want)
	}
	if st := ops.Stats(); st.Moved != 1 || st.Failed != 2 {
		t.Errorf("stats = %+v, want 1 moved, 2 failed", st)
	}
	if !testutil.Exists(ex("A/ABBA - SOS/locked.jpg")) || !testutil.Exists(filepath.Join(lib, "Q", "Queen - Rock", "ok.jpg")) {
		t.Error("the failed item must stay and the next one must go back")
	}
	if !testutil.ContainsPrefix(logs, "Restore: "+ex("A/ABBA - SOS/vanish.jpg")+" is not there any more") {
		t.Errorf("logs = %q", logs)
	}
	runs, err := readJournal(cfg.JournalPath())
	if err != nil || len(runs) != 1 || runs[0].moves() != 1 || runs[0].Entries[0].Src != ex("Q/Queen - Rock/ok.jpg") {
		t.Errorf("journal = %+v, %v; want only the successful move", runs, err)
	}
}

// ---- restoreLibraryLosers ----

// loserScenario: the library's copy lib/Gone/dup loses to the keeper src/keeper, whose
// move into the library fails (injected). It returns the applier after apply.
func loserScenario(t *testing.T, target string, setup func(a *applier, ops *fsops.Ops, lib string)) (a *applier, lib, out string) {
	t.Helper()
	noFFprobe(t)
	base := t.TempDir()
	lib, src, out := filepath.Join(base, "lib"), filepath.Join(base, "src"), filepath.Join(base, "out")
	testutil.WriteFiles(t, lib, songFiles("Gone/dup", "A", "S"))
	testutil.WriteFiles(t, src, songFiles("keeper", "A", "S", "video"))
	cfg := organizeCfg(t, [][]string{{lib}, {src}}, lib, out, false)
	cands := []Candidate{
		{Label: "lib", Rel: "Gone/dup", Folder: filepath.Join(lib, "Gone", "dup"), InLibrary: true},
		{Label: "src", Rel: "keeper", Folder: filepath.Join(src, "keeper")},
	}
	plan := Plan{Decisions: []Decision{{Action: ActDuplicate, Other: 1}, {Action: ActMove, Target: target, Other: -1}}}
	ops := fsops.New(false)
	a = newApplier(cfg, ops, cands, map[int]moveOutcome{})
	a.libraryInPlace = true
	a.moveTo = func(s, d string) error {
		if s == cands[1].Folder {
			os.Remove(filepath.Join(lib, "Gone")) // empty since the duplicate left: its move back must recreate it
			ops.Fail()
			return errors.New("injected failure")
		}
		return ops.MoveTo(s, d)
	}
	setup(a, ops, lib)
	a.apply(plan)
	return a, lib, out
}

func logMessages(hook interface{ AllEntries() []*log.Entry }, level log.Level) []string {
	var out []string
	for _, e := range hook.AllEntries() {
		if e.Level == level {
			out = append(out, e.Message)
		}
	}
	return out
}

func TestRestoreLibraryLosersErrorBranches(t *testing.T) {
	t.Run("journal failed: named, not moved back", func(t *testing.T) {
		hook := testutil.CaptureLogs(t)
		a, lib, out := loserScenario(t, "N/keeper", func(a *applier, ops *fsops.Ops, _ string) {
			// The keeper's move into the new folder N cannot journal its mkdir: the
			// journal is failed (and so is the keeper) when the loser would go back.
			failingJournal(ops, a.j, func(line string) bool { return strings.Contains(line, `"op":"mkdir"`) })
		})
		if !a.j.failed {
			t.Fatal("the journal did not fail")
		}
		if got := a.outcomes[0].Status; got != outcomeMoved {
			t.Errorf("outcome = %q, want %q", got, outcomeMoved)
		}
		if testutil.Exists(filepath.Join(lib, "Gone", "dup")) || !testutil.Exists(filepath.Join(out, "duplicates", "lib", "Gone", "dup", "song.txt")) {
			t.Error("the loser must stay in duplicates/")
		}
		if !testutil.ContainsPrefix(logMessages(hook, log.ErrorLevel), "src:keeper did not reach the library, and lib:Gone/dup (moved to ") {
			t.Errorf("errors = %q", logMessages(hook, log.ErrorLevel))
		}
	})
	t.Run("journal of the mkdir fails: outcome unchanged", func(t *testing.T) {
		a, lib, out := loserScenario(t, "keeper", func(a *applier, ops *fsops.Ops, _ string) {
			failingJournal(ops, a.j, func(line string) bool { return strings.Contains(line, `"op":"mkdir"`) })
		})
		if got := a.outcomes[0]; got.Status != outcomeMoved || got.Note != "" {
			t.Errorf("outcome = %+v, want the move to duplicates/ unchanged", got)
		}
		if testutil.Exists(filepath.Join(lib, "Gone")) || !testutil.Exists(filepath.Join(out, "duplicates", "lib", "Gone", "dup", "song.txt")) {
			t.Error("the loser was moved back without a journal entry")
		}
	})
	t.Run("move back fails: stays a duplicate, log names undo", func(t *testing.T) {
		hook := testutil.CaptureLogs(t)
		a, lib, out := loserScenario(t, "keeper", func(a *applier, ops *fsops.Ops, lib string) {
			inner := a.moveTo
			a.moveTo = func(s, d string) error {
				if strings.HasPrefix(d, lib) {
					ops.Fail()
					return errors.New("injected failure")
				}
				return inner(s, d)
			}
		})
		if got := a.outcomes[0].Status; got != outcomeMoved {
			t.Errorf("outcome = %q, want %q", got, outcomeMoved)
		}
		if testutil.Exists(filepath.Join(lib, "Gone", "dup")) || !testutil.Exists(filepath.Join(out, "duplicates", "lib", "Gone", "dup", "song.txt")) {
			t.Error("the loser must stay in duplicates/")
		}
		var found bool
		for _, m := range logMessages(hook, log.ErrorLevel) {
			found = found || (strings.Contains(m, "moving lib:Gone/dup back from") && strings.Contains(m, "-undo moves it back"))
		}
		if !found {
			t.Errorf("errors = %q", logMessages(hook, log.ErrorLevel))
		}
	})
}

// ---- journal.go, setup.go ----

func TestReadJournalRevertedEntries(t *testing.T) {
	for _, tc := range []struct {
		name, lines string
		want        map[int]bool
		warns       int
	}{
		{"no entry", `{"op":"reverted","run":"r1"}`, nil, 1},
		{"zero", `{"op":"reverted","run":"r1","entry":0}`, nil, 1},
		{"negative", `{"op":"reverted","run":"r1","entry":-3}`, nil, 1},
		{"valid", `{"op":"reverted","run":"r1","entry":2}`, map[int]bool{2: true}, 0},
		{"mixed", `{"op":"reverted","run":"r1","entry":0}` + "\n" + `{"op":"reverted","run":"r1","entry":1}`, map[int]bool{1: true}, 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			hook := testutil.CaptureLogsAt(t, log.WarnLevel)
			path := filepath.Join(t.TempDir(), "organize-journal.jsonl")
			data := `{"op":"run","run":"r1"}` + "\n" + `{"op":"move","run":"r1","src":"/a","dst":"/b"}` + "\n" + tc.lines + "\n"
			if err := os.WriteFile(path, []byte(data), 0o644); err != nil {
				t.Fatal(err)
			}
			runs, err := readJournal(path)
			if err != nil || len(runs) != 1 {
				t.Fatalf("runs = %+v, %v", runs, err)
			}
			if !reflect.DeepEqual(runs[0].Reverted, tc.want) {
				t.Errorf("Reverted = %v, want %v", runs[0].Reverted, tc.want)
			}
			w := 0
			for _, m := range logMessages(hook, log.WarnLevel) {
				if strings.Contains(m, "reverted without entry, skipping it") {
					w++
				}
			}
			if w != tc.warns {
				t.Errorf("%d warnings, want %d", w, tc.warns)
			}
		})
	}
}

func TestListRunsBadJournal(t *testing.T) {
	base := t.TempDir()
	cfg := organizeCfg(t, nil, filepath.Join(base, "lib"), filepath.Join(base, "out"), false)
	if runs, err := ListRuns(cfg); err != nil || len(runs) != 0 {
		t.Fatalf("no journal: %v, %v", runs, err)
	}
	if err := os.MkdirAll(cfg.JournalPath(), 0o755); err != nil {
		t.Fatal(err)
	}
	if _, err := ListRuns(cfg); err == nil {
		t.Error("ListRuns on a directory at the journal path: no error")
	}
}

func TestNewUndoConfig(t *testing.T) {
	base, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	lib := filepath.Join(base, "lib")
	if err := os.MkdirAll(lib, 0o755); err != nil {
		t.Fatal(err)
	}
	cfg, err := NewUndoConfig(lib, "", true)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Library != lib || cfg.OutDir != filepath.Join(base, "organize-output") || len(cfg.Sources) != 0 || !cfg.DryRun {
		t.Errorf("default: %+v", cfg)
	}
	custom := filepath.Join(base, "my-out")
	if cfg, err = NewUndoConfig(lib, custom, false); err != nil {
		t.Fatal(err)
	}
	if cfg.OutDir != custom || len(cfg.Sources) != 0 || cfg.DryRun {
		t.Errorf("explicit: %+v", cfg)
	}
}

// ---- remove.go ----

func TestParsePatternFileTooLongLine(t *testing.T) {
	_, err := ParsePatternFile(strings.NewReader("ABBA\n"+strings.Repeat("x", 2<<20)+"\n"), "list.txt")
	if err == nil || !strings.HasPrefix(err.Error(), "reading list.txt") {
		t.Errorf("error = %v, want 'reading list.txt: ...'", err)
	}
}

// A txt that cannot be read is counted as unreadable once and its song never matches.
func TestRemoveUnreadableTxtNeverMatches(t *testing.T) {
	testutil.SkipIfRoot(t)
	cfg, lib, _ := restoreSetup(t)
	txt := filepath.Join(lib, "A", "ABBA - SOS", "song.txt")
	testutil.ChmodUnreadable(t, txt)
	ops := fsops.New(false)
	if err := Remove(cfg, ops, removePatterns(t, "ABBA")); err != nil {
		t.Fatal(err)
	}
	if !testutil.Exists(filepath.Join(lib, "A", "ABBA - SOS")) {
		t.Error("the song with the unreadable txt was moved")
	}
	if testutil.Exists(filepath.Join(lib, "A", "ABBA - Waterloo")) {
		t.Error("the readable ABBA song stayed")
	}
	if st := ops.Stats(); st.Failed != 1 || st.Moved != 1 {
		t.Errorf("stats = %+v, want 1 unreadable (failed) and 1 moved", st)
	}
}

func TestRemoveLibraryCannotBeListed(t *testing.T) {
	cfg, lib, out := restoreSetup(t)
	if err := os.RemoveAll(lib); err != nil {
		t.Fatal(err)
	}
	ops := fsops.New(false)
	err := Remove(cfg, ops, removePatterns(t, "ABBA"))
	if err == nil || !strings.Contains(err.Error(), "finding song folders") {
		t.Errorf("Remove = %v", err)
	}
	if testutil.Exists(filepath.Join(out, removeReportJSON)) || testutil.Exists(cfg.JournalPath()) {
		t.Error("a report or journal was written")
	}
}

// A panic while a song's tags are read counts as a failure; the song stays and never
// matches, the others are removed.
func TestRemoveTagReadingPanic(t *testing.T) {
	cfg, lib, _ := restoreSetup(t)
	old := readSong
	t.Cleanup(func() { readSong = old })
	readSong = func(ops *fsops.Ops, l, folder string) removeSong {
		if filepath.Base(folder) == "ABBA - SOS" {
			panic("tag bug")
		}
		return old(ops, l, folder)
	}
	ops := fsops.New(false)
	if err := Remove(cfg, ops, removePatterns(t, "ABBA")); err != nil {
		t.Fatal(err)
	}
	if st := ops.Stats(); st.Failed != 1 || st.Moved != 1 {
		t.Errorf("stats = %+v, want 1 failed (the panic) and 1 moved", st)
	}
	if !testutil.Exists(filepath.Join(lib, "A", "ABBA - SOS", "song.txt")) || testutil.Exists(filepath.Join(lib, "A", "ABBA - Waterloo")) {
		t.Error("the panicked song must stay and the other must be removed")
	}
	// The same panic in the set-aside listing leaves the item without a name.
	items, err := ListSetAside(cfg, fsops.New(true))
	if err != nil || len(items) != 1 || items[0].Name == "" {
		t.Errorf("items = %+v, %v", items, err)
	}
	readSong = func(ops *fsops.Ops, l, folder string) removeSong { panic("tag bug") }
	if items, err = ListSetAside(cfg, fsops.New(true)); err != nil || len(items) != 1 || items[0].Name != "" {
		t.Errorf("after a panic: %+v, %v, want the item without a name", items, err)
	}
}

func TestRemoveReportAndJournalFailures(t *testing.T) {
	t.Run("pending report", func(t *testing.T) {
		cfg, lib, out := restoreSetup(t)
		testutil.WriteFile(t, filepath.Join(out, removeReportJSON, "x"), "a folder where the report goes")
		before := tree(t, lib)
		ops := fsops.New(false)
		if err := Remove(cfg, ops, removePatterns(t, "ABBA")); err == nil {
			t.Error("Remove did not fail")
		}
		if !reflect.DeepEqual(tree(t, lib), before) || ops.Stats().Moved != 0 {
			t.Error("a song was moved")
		}
		if testutil.Exists(cfg.JournalPath()) {
			t.Error("journal written")
		}
	})
	t.Run("final report", func(t *testing.T) {
		cfg, lib, out := restoreSetup(t)
		ops := fsops.New(false)
		err := RemoveWith(cfg, ops, removePatterns(t, "ABBA"), RemoveOptions{Progress: func(phase string, done, _ int) {
			if phase == PhaseMove && done == 2 { // after the pending report was written
				txt := filepath.Join(out, removeReportTxt)
				os.Remove(txt)
				os.MkdirAll(filepath.Join(txt, "x"), 0o755)
			}
		}})
		if err == nil {
			t.Error("Remove did not fail")
		}
		if ops.Stats().Moved != 2 || testutil.Exists(filepath.Join(lib, "A", "ABBA - SOS")) {
			t.Errorf("the moves were not done: %+v", ops.Stats())
		}
	})
	t.Run("journal", func(t *testing.T) {
		cfg, lib, _ := restoreSetup(t)
		if err := os.MkdirAll(cfg.JournalPath(), 0o755); err != nil { // appends fail
			t.Fatal(err)
		}
		before := tree(t, lib)
		ops := fsops.New(false)
		err := Remove(cfg, ops, removePatterns(t, "ABBA"))
		if err == nil || !strings.Contains(err.Error(), "journal") {
			t.Errorf("Remove = %v, want a journal error", err)
		}
		if !reflect.DeepEqual(tree(t, lib), before) || ops.Stats().Moved != 0 {
			t.Error("a song was moved without a journal")
		}
		var rep removeReport
		if err := json.Unmarshal([]byte(testutil.ReadFile(t, filepath.Join(cfg.OutDir, removeReportJSON))), &rep); err != nil {
			t.Fatal(err)
		}
		for _, e := range rep.Removed {
			if e.Outcome != outcomeSkipped || e.OutcomeNote != noteJournalFailed {
				t.Errorf("entry %+v, want skipped with the journal note", e)
			}
		}
	})
}

// Failed moves are counted in the report's summary and in the stats; the other songs
// are still removed.
func TestRemoveFailedMovesCounted(t *testing.T) {
	testutil.SkipIfRoot(t)
	cfg, lib, out := restoreSetup(t)
	locked := filepath.Join(lib, "A")
	if err := os.Chmod(locked, 0o555); err != nil { // the songs cannot be renamed out of A/
		t.Fatal(err)
	}
	t.Cleanup(func() { os.Chmod(locked, 0o755) })
	p := append(removePatterns(t, "ABBA"), removePatterns(t, "Queen")...)
	ops := fsops.New(false)
	if err := Remove(cfg, ops, p); err != nil {
		t.Fatal(err)
	}
	if st := ops.Stats(); st.Moved != 1 || st.Failed != 2 {
		t.Errorf("stats = %+v, want 1 moved, 2 failed", st)
	}
	var rep removeReport
	if err := json.Unmarshal([]byte(testutil.ReadFile(t, filepath.Join(out, removeReportJSON))), &rep); err != nil {
		t.Fatal(err)
	}
	if rep.Summary.Failed != 2 || rep.Summary.Moved != 1 || rep.Summary.Removed != 3 {
		t.Errorf("summary = %+v", rep.Summary)
	}
	if !strings.Contains(testutil.ReadFile(t, filepath.Join(out, removeReportTxt)), "1 moved, 2 failed") {
		t.Error("the text report does not count the failures")
	}
}

// ---- Run error paths ----

func TestRunNeedsSourcesAndLibrary(t *testing.T) {
	base := t.TempDir()
	lib, src := filepath.Join(base, "lib"), filepath.Join(base, "src")
	testutil.WriteFiles(t, src, songFiles("a", "A", "A"))
	for name, cfg := range map[string]config.OrganizeConfig{
		"no sources": {Library: lib, OutDir: filepath.Join(base, "out")},
		"no library": {Sources: []config.Source{{Path: src, Label: "src"}}, OutDir: filepath.Join(base, "out")},
	} {
		ops := fsops.New(false)
		if err := Run(cfg, ops, Options{}); err == nil || !strings.Contains(err.Error(), "at least one source and a library") {
			t.Errorf("%s: Run = %v", name, err)
		}
		if st := ops.Stats(); st.Moved != 0 {
			t.Errorf("%s: stats = %+v", name, st)
		}
	}
	if testutil.Exists(filepath.Join(base, "out")) || testutil.Exists(lib) {
		t.Error("a failed Run created folders")
	}
}

// A source that cannot be listed aborts the run before anything is moved, even if
// another source could be.
func TestRunUnlistableSourceAbortsBeforeMoves(t *testing.T) {
	noFFprobe(t)
	base := t.TempDir()
	good, gone, lib, out := filepath.Join(base, "good"), filepath.Join(base, "gone"), filepath.Join(base, "lib"), filepath.Join(base, "out")
	testutil.WriteFiles(t, good, songFiles("a", "Abba", "SOS"))
	testutil.WriteFiles(t, gone, songFiles("b", "Cher", "Believe"))
	cfg := organizeCfg(t, [][]string{{good}, {gone}}, lib, out, false)
	if err := os.RemoveAll(gone); err != nil {
		t.Fatal(err)
	}
	before := tree(t, good)
	ops := fsops.New(false)
	err := Run(cfg, ops, Options{})
	if err == nil || !strings.Contains(err.Error(), "finding song folders in") {
		t.Errorf("Run = %v", err)
	}
	if st := ops.Stats(); st.Moved != 0 {
		t.Errorf("stats = %+v", st)
	}
	if !reflect.DeepEqual(tree(t, good), before) || testutil.Exists(lib) {
		t.Error("something was moved")
	}
	if testutil.Exists(cfg.ReportPath(reportJSON)) || testutil.Exists(cfg.JournalPath()) {
		t.Error("a report or journal was written")
	}
}

// A report that cannot be written at the end is an error (the moves are done and
// journaled); in a dry run the report is written only then.
func TestRunFinalReportUnwritable(t *testing.T) {
	noFFprobe(t)
	t.Run("dry run", func(t *testing.T) {
		base := t.TempDir()
		src, lib, out := filepath.Join(base, "src"), filepath.Join(base, "lib"), filepath.Join(base, "out")
		testutil.WriteFiles(t, src, songFiles("a", "Abba", "SOS"))
		cfg := organizeCfg(t, [][]string{{src}}, lib, out, true)
		testutil.WriteFile(t, filepath.Join(cfg.ReportPath(reportJSON), "x"), "a folder")
		if err := Run(cfg, fsops.New(true), Options{}); err == nil {
			t.Error("Run did not fail")
		}
	})
	t.Run("real run", func(t *testing.T) {
		base := t.TempDir()
		src, lib, out := filepath.Join(base, "src"), filepath.Join(base, "lib"), filepath.Join(base, "out")
		testutil.WriteFiles(t, src, songFiles("a", "Abba", "SOS"))
		cfg := organizeCfg(t, [][]string{{src}}, lib, out, false)
		ops := fsops.New(false)
		err := Run(cfg, ops, Options{Progress: func(phase string, done, _ int) {
			if phase == PhaseMove && done == 1 { // the pending report is written
				txt := cfg.ReportPath(reportTxt)
				os.Remove(txt)
				os.MkdirAll(filepath.Join(txt, "x"), 0o755)
			}
		}})
		if err == nil {
			t.Fatal("Run did not fail")
		}
		if ops.Stats().Moved != 1 || !testutil.Exists(filepath.Join(lib, "A", "Abba - SOS", "song.txt")) {
			t.Errorf("the move was not done: %+v", ops.Stats())
		}
		if runs, err := ListRuns(cfg); err != nil || len(runs) != 1 || runs[0].Moves != 1 {
			t.Errorf("journal runs = %+v, %v", runs, err)
		}
	})
}
