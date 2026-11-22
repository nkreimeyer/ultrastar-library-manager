package organize

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/nkreimeyer/library-manager/config"
	"github.com/nkreimeyer/library-manager/fsops"
	"github.com/nkreimeyer/library-manager/internal/testutil"
)

// A journal append that fails only once (a short hiccup) still stops the recording: the
// move back of a park whose record is missing would, as the only record, make -undo park
// the song again.
func TestJournalGapStopsRecording(t *testing.T) {
	noFFprobe(t)
	base := t.TempDir()
	lib, out := filepath.Join(base, "lib"), filepath.Join(base, "out")
	testutil.WriteFiles(t, lib, merge(songFiles("A/a", "Bee", "B"), songFiles("B/b", "Abba", "A")))
	before := tree(t, lib)
	cfg := organizeCfg(t, [][]string{{lib}}, lib, out, false)
	if err := cfg.PrepareOutDir(); err != nil {
		t.Fatal(err)
	}
	ops := fsops.New(false)
	cands := []Candidate{
		{Label: "lib", Rel: "A/a", Folder: filepath.Join(lib, "A", "a"), InLibrary: true},
		{Label: "lib", Rel: "B/b", Folder: filepath.Join(lib, "B", "b"), InLibrary: true},
	}
	plan := Plan{Decisions: []Decision{{Action: ActMove, Target: "B/b", Other: -1}, {Action: ActMove, Target: "A/a", Other: -1}}}
	a := newApplier(cfg, ops, cands, map[int]moveOutcome{})
	failed := false
	a.j.write = func(path string, line []byte) error {
		if !failed && strings.Contains(string(line), tempPrefix) {
			failed = true
			ops.Fail()
			return fmt.Errorf("injected journal failure")
		}
		return ops.AppendJournal(path, line)
	}
	a.apply(plan)
	if got := tree(t, lib); !reflect.DeepEqual(got, before) {
		t.Fatalf("after the run:\n%s\nwant:\n%s", strings.Join(got, "\n"), strings.Join(before, "\n"))
	}
	if err := Undo(cfg, fsops.New(false), UndoOptions{}); err != nil {
		t.Fatal(err)
	}
	if got := tree(t, lib); !reflect.DeepEqual(got, before) {
		t.Errorf("after the undo:\n%s\nwant:\n%s", strings.Join(got, "\n"), strings.Join(before, "\n"))
	}
}

// organizeOne organizes the song "ABBA - SOS" of a source folder into a new library and
// returns the config, the source song folder and its library folder.
func organizeOne(t *testing.T) (cfg config.OrganizeConfig, srcSong, libSong string) {
	t.Helper()
	noFFprobe(t)
	base := t.TempDir()
	src, lib, out := filepath.Join(base, "usb", "songs"), filepath.Join(base, "lib"), filepath.Join(base, "out")
	testutil.WriteFiles(t, src, songFiles("ABBA - SOS", "ABBA", "SOS"))
	cfg = organizeCfg(t, [][]string{{src}}, lib, out, false)
	runOrganize(t, cfg, Options{})
	srcSong, libSong = filepath.Join(src, "ABBA - SOS"), filepath.Join(lib, "A", "ABBA - SOS")
	if !testutil.Exists(libSong) || testutil.Exists(srcSong) {
		t.Fatalf("the song was not organized into %s", libSong)
	}
	return cfg, srcSong, libSong
}

// undoRecorded reports whether the journal records the only run as undone.
func undoRecorded(t *testing.T, cfg config.OrganizeConfig) bool {
	t.Helper()
	runs, err := ListRuns(cfg)
	if err != nil || len(runs) != 1 {
		t.Fatalf("ListRuns = %+v, %v", runs, err)
	}
	return runs[0].Undone
}

// A song whose library folder cannot be checked (missing permissions, a NAS dropping out)
// is not "gone": the undo counts a failure and does not record the run undone, so a
// retry moves it back.
func TestUndoUncheckableSongIsRetried(t *testing.T) {
	testutil.SkipIfRoot(t)
	cfg, srcSong, libSong := organizeOne(t)
	bucket := filepath.Dir(libSong)
	if err := os.Chmod(bucket, 0); err != nil {
		t.Fatal(err)
	}
	ops := fsops.New(false)
	err := Undo(cfg, ops, UndoOptions{})
	if cerr := os.Chmod(bucket, 0o755); cerr != nil {
		t.Fatal(cerr)
	}
	if err != nil {
		t.Fatal(err)
	}
	if st := ops.Stats(); st.Failed == 0 || st.Moved != 0 {
		t.Errorf("stats = %+v, want a failure and no move", st)
	}
	if undoRecorded(t, cfg) {
		t.Fatal("the run was recorded undone although its song was not moved back")
	}
	runUndo(t, cfg)
	if !testutil.Exists(srcSong) || testutil.Exists(libSong) {
		t.Error("the retry did not move the song back")
	}
	if !undoRecorded(t, cfg) {
		t.Error("the retry was not recorded undone")
	}
}

// The undo does not recreate a source folder that is gone (an unplugged drive): the
// song stays in the library, and the undo can be retried once the drive is back.
func TestUndoRefusesGoneSourceFolder(t *testing.T) {
	cfg, srcSong, libSong := organizeOne(t)
	srcRoot := filepath.Dir(srcSong)
	unplugged := srcRoot + ".unplugged"
	if err := os.Rename(srcRoot, unplugged); err != nil {
		t.Fatal(err)
	}
	ops := fsops.New(false)
	if err := Undo(cfg, ops, UndoOptions{}); err != nil {
		t.Fatal(err)
	}
	if st := ops.Stats(); st.Failed != 1 || st.Moved != 0 {
		t.Errorf("stats = %+v, want one failure and no move", st)
	}
	if testutil.Exists(srcRoot) {
		t.Error("the undo created the gone source folder")
	}
	if !testutil.Exists(libSong) {
		t.Error("the song left the library")
	}
	if undoRecorded(t, cfg) {
		t.Fatal("the run was recorded undone")
	}
	if err := os.Rename(unplugged, srcRoot); err != nil {
		t.Fatal(err)
	}
	runUndo(t, cfg)
	if !testutil.Exists(srcSong) || testutil.Exists(libSong) || !undoRecorded(t, cfg) {
		t.Error("the retry did not move the song back")
	}
}

// Without a library (the UI's restore with only an output folder) the origin of an item
// in extra-files/ or invalid/ cannot be guessed: it is not taken relative to the CWD.
func TestRestoreWithoutLibraryDoesNotGuess(t *testing.T) {
	noFFprobe(t)
	base := t.TempDir()
	out := filepath.Join(base, "out")
	if err := (config.Config{OutDir: out}).PrepareOutDir(); err != nil {
		t.Fatal(err)
	}
	testutil.WriteFiles(t, out, map[string]string{
		SectionExtraFiles + "/Song/notes.txt": "x",
		SectionInvalid + "/Broken/song.mp3":   "x",
	})
	cwd := filepath.Join(base, "cwd")
	if err := os.Mkdir(cwd, 0o755); err != nil {
		t.Fatal(err)
	}
	t.Chdir(cwd)
	cfg, err := NewUndoConfig("", out, false)
	if err != nil {
		t.Fatal(err)
	}
	items, err := ListSetAside(cfg, fsops.New(true))
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 2 {
		t.Fatalf("items = %+v, want two", items)
	}
	var refs []RestoreRef
	for _, it := range items {
		if it.Origin != "" || it.Blocked != blockedUnknown {
			t.Errorf("item %+v: want no origin, blocked %q", it, blockedUnknown)
		}
		refs = append(refs, RestoreRef{it.Section, it.Rel})
	}
	ops := fsops.New(false)
	if err := Restore(cfg, ops, refs, RestoreOptions{}); err != nil {
		t.Fatal(err)
	}
	if st := ops.Stats(); st.Moved != 0 {
		t.Errorf("stats = %+v, want nothing moved", st)
	}
	if entries, _ := os.ReadDir(cwd); len(entries) != 0 {
		t.Errorf("the restore wrote into the CWD: %v", entries)
	}
}

// A relative origin (a hand-edited journal) would be taken relative to the CWD.
func TestBlockedRelativeOrigin(t *testing.T) {
	base := t.TempDir()
	cfg := config.OrganizeConfig{Library: filepath.Join(base, "lib"), OutDir: filepath.Join(base, "out")}
	if got := blocked(cfg, fsops.New(true), filepath.Join("A", "Song")); got != blockedUnknown {
		t.Errorf("blocked = %q, want %q", got, blockedUnknown)
	}
}

// An undo given only the output folder (-undo -o) knows the library from the run line:
// a library folder that is gone (an emptied category removed by hand) is created again,
// while a gone source folder is still refused.
func TestUndoWithoutLibraryFlag(t *testing.T) {
	noFFprobe(t)
	base := t.TempDir()
	lib, usb, out := filepath.Join(base, "lib"), filepath.Join(base, "usb"), filepath.Join(base, "out")
	testutil.WriteFiles(t, lib, songFiles("Old/ABBA - SOS", "ABBA", "SOS"))
	testutil.WriteFiles(t, usb, songFiles("Queen - Innuendo", "Queen", "Innuendo"))
	cfg := organizeCfg(t, [][]string{{lib}, {usb}}, lib, out, false)
	runOrganize(t, cfg, Options{})
	if err := os.Remove(filepath.Join(lib, "Old")); err != nil {
		t.Fatal(err) // the emptied category
	}
	if err := os.Rename(usb, usb+".unplugged"); err != nil {
		t.Fatal(err)
	}
	outOnly, err := NewUndoConfig("", out, false)
	if err != nil {
		t.Fatal(err)
	}
	ops := fsops.New(false)
	if err := Undo(outOnly, ops, UndoOptions{}); err != nil {
		t.Fatal(err)
	}
	if st := ops.Stats(); st.Moved != 1 || st.Failed != 1 {
		t.Errorf("stats = %+v, want the library song moved back and the source song refused", st)
	}
	if !testutil.Exists(filepath.Join(lib, "Old", "ABBA - SOS", "song.txt")) {
		t.Error("the library song was not moved back into its recreated category")
	}
	if testutil.Exists(usb) {
		t.Error("the undo created the gone source folder")
	}
}

// A journal of an older version names no library: an undo given only the output folder
// cannot tell a library folder from a source and recreates any gone folder, as before.
func TestUndoOldJournalWithoutLibrary(t *testing.T) {
	noFFprobe(t)
	base := t.TempDir()
	lib, out := filepath.Join(base, "lib"), filepath.Join(base, "out")
	testutil.WriteFiles(t, lib, songFiles("Old/ABBA - SOS", "ABBA", "SOS"))
	cfg := organizeCfg(t, [][]string{{lib}}, lib, out, false)
	runOrganize(t, cfg, Options{})
	data, err := os.ReadFile(cfg.JournalPath())
	if err != nil {
		t.Fatal(err)
	}
	old := strings.Replace(string(data), `,"library":`+strconvQuote(lib), "", 1)
	if old == string(data) {
		t.Fatalf("the run line names no library:\n%s", data)
	}
	if err := os.WriteFile(cfg.JournalPath(), []byte(old), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(filepath.Join(lib, "Old")); err != nil {
		t.Fatal(err)
	}
	outOnly, err := NewUndoConfig("", out, false)
	if err != nil {
		t.Fatal(err)
	}
	if st := runUndo(t, outOnly); st.Moved != 1 || st.Failed != 0 {
		t.Errorf("stats = %+v", st)
	}
	if !testutil.Exists(filepath.Join(lib, "Old", "ABBA - SOS")) {
		t.Error("the song was not moved back")
	}
}

func strconvQuote(s string) string {
	b, _ := json.Marshal(s)
	return string(b)
}
