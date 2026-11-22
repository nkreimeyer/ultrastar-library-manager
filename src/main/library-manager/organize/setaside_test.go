package organize

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"os"
	"path/filepath"
	"testing"

	"github.com/nkreimeyer/library-manager/config"
	"github.com/nkreimeyer/library-manager/fsops"
	"github.com/nkreimeyer/library-manager/internal/testutil"
)

func restoreSetup(t *testing.T) (cfg config.OrganizeConfig, lib, out string) {
	t.Helper()
	base := t.TempDir()
	lib, out = filepath.Join(base, "library"), filepath.Join(base, "out")
	for _, s := range []struct{ dir, artist, title string }{
		{"A/ABBA - SOS", "ABBA", "SOS"}, {"A/ABBA - Waterloo", "ABBA", "Waterloo"}, {"Q/Queen - Rock", "Queen", "Rock"},
	} {
		testutil.WriteFiles(t, filepath.Join(lib, filepath.FromSlash(s.dir)), map[string]string{
			"song.txt": "#ARTIST:" + s.artist + "\n#TITLE:" + s.title + "\n#MP3:song.mp3\n" + testutil.NotesTxt(s.artist+s.title),
			"song.mp3": "audio " + s.dir,
		})
	}
	var err error
	if cfg, err = NewConfig(ModeRemove, nil, lib, out, false); err != nil {
		t.Fatal(err)
	}
	return cfg, cfg.Library, cfg.OutDir
}

func removePatterns(t *testing.T, text string) []Pattern {
	t.Helper()
	p, err := ParsePattern(PatternArtist, text, "test")
	if err != nil {
		t.Fatal(err)
	}
	return []Pattern{p}
}

func TestRestoreRemovedSongs(t *testing.T) {
	cfg, lib, _ := restoreSetup(t)
	if err := Remove(cfg, fsops.New(false), removePatterns(t, "ABBA")); err != nil {
		t.Fatal(err)
	}
	items, err := ListSetAside(cfg, fsops.New(false))
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 2 {
		t.Fatalf("items = %+v", items)
	}
	for _, it := range items {
		if it.Section != SectionRemoved || it.Kind != KindSong || !it.Exact || it.Blocked != "" || it.Name == "" ||
			it.Origin != filepath.Join(lib, "A", filepath.FromSlash(it.Rel[2:])) {
			t.Errorf("item = %+v", it)
		}
	}

	// A song whose old place is taken stays and is reported.
	testutil.WriteFile(t, filepath.Join(lib, "A", "ABBA - SOS", "other.txt"), "x")
	items, _ = ListSetAside(cfg, fsops.New(false))
	var refs []RestoreRef
	for _, it := range items {
		if it.Rel == "A/ABBA - SOS" {
			if it.Blocked != blockedInTheWay {
				t.Errorf("blocked = %q", it.Blocked)
			}
		}
		refs = append(refs, RestoreRef{it.Section, it.Rel})
	}
	ops := fsops.New(false)
	if err := Restore(cfg, ops, refs, RestoreOptions{}); err != nil {
		t.Fatal(err)
	}
	if st := ops.Stats(); st.Moved != 1 || st.Failed != 1 {
		t.Fatalf("stats = %+v", st)
	}
	if !testutil.Exists(filepath.Join(lib, "A", "ABBA - Waterloo", "song.txt")) {
		t.Error("Waterloo was not put back")
	}
	if got := testutil.ReadFile(t, filepath.Join(lib, "A", "ABBA - SOS", "other.txt")); got != "x" {
		t.Error("the song in the way was touched")
	}
	if !testutil.Exists(filepath.Join(cfg.RemovedDir(), "A", "ABBA - SOS", "song.txt")) {
		t.Error("the blocked song left the removed folder")
	}

	// The restore is a journal run: undo sets the song aside again.
	if err := Undo(cfg, fsops.New(false), UndoOptions{}); err != nil {
		t.Fatal(err)
	}
	if testutil.Exists(filepath.Join(lib, "A", "ABBA - Waterloo")) {
		t.Error("undo did not set the restored song aside again")
	}
}

func TestRestoreMirroredAndGuards(t *testing.T) {
	cfg, lib, out := restoreSetup(t)
	// A leftover file and a broken folder set aside by fixtxt/cleanup (mirrored layout).
	testutil.WriteFile(t, filepath.Join(out, "extra-files", "A", "ABBA - SOS", "old.jpg"), "img")
	testutil.WriteFile(t, filepath.Join(out, "extra-files", "A", "ABBA - SOS", ".hidden"), "h")
	testutil.WriteFiles(t, filepath.Join(out, "invalid", "Z", "Gone - Song"), map[string]string{"song.txt": "#ARTIST:Gone\n#TITLE:Song\n"})
	// A duplicate without a journal entry: origin unknown.
	testutil.WriteFiles(t, filepath.Join(out, "duplicates", "library", "A", "X - Y"), map[string]string{"song.txt": "#ARTIST:X\n#TITLE:Y\n"})

	items, err := ListSetAside(cfg, fsops.New(false))
	if err != nil {
		t.Fatal(err)
	}
	by := map[string]SetAsideItem{}
	for _, it := range items {
		by[it.Section+"/"+it.Rel] = it
	}
	if len(items) != 3 {
		t.Fatalf("items = %+v", items)
	}
	if it := by["extra-files/A/ABBA - SOS/old.jpg"]; it.Kind != KindFile || it.Exact || it.Blocked != "" || it.Origin != filepath.Join(lib, "A", "ABBA - SOS", "old.jpg") {
		t.Errorf("extra file = %+v", it)
	}
	if it := by["invalid/Z/Gone - Song"]; it.Kind != KindSong || it.Blocked != "" || it.Origin != filepath.Join(lib, "Z", "Gone - Song") {
		t.Errorf("invalid = %+v", it)
	}
	if it := by["duplicates/library/A/X - Y"]; it.Blocked != blockedUnknown || it.Origin != "" {
		t.Errorf("duplicate = %+v", it)
	}

	ops := fsops.New(false)
	refs := []RestoreRef{
		{SectionExtraFiles, "A/ABBA - SOS/old.jpg"}, {SectionInvalid, "Z/Gone - Song"},
		{SectionDuplicates, "library/A/X - Y"}, // unknown origin
		{SectionInvalid, "../library"},         // outside the section
		{"originals", "x"},                     // not a section
	}
	if err := Restore(cfg, ops, refs, RestoreOptions{}); err != nil {
		t.Fatal(err)
	}
	if st := ops.Stats(); st.Moved != 2 || st.Failed != 3 {
		t.Fatalf("stats = %+v", st)
	}
	if !testutil.Exists(filepath.Join(lib, "A", "ABBA - SOS", "old.jpg")) || !testutil.Exists(filepath.Join(lib, "Z", "Gone - Song", "song.txt")) {
		t.Error("items not put back")
	}
	if testutil.Exists(filepath.Join(out, "invalid", "Z")) {
		t.Error("the emptied folder was not tidied away")
	}
	// Undo removes the library folder Z again, which the restore created.
	if err := Undo(cfg, fsops.New(false), UndoOptions{}); err != nil {
		t.Fatal(err)
	}
	if testutil.Exists(filepath.Join(lib, "Z")) || testutil.Exists(filepath.Join(lib, "A", "ABBA - SOS", "old.jpg")) {
		t.Error("undo left the restored items in the library")
	}
}

func TestRestoreDryRunAndStop(t *testing.T) {
	cfg, lib, out := restoreSetup(t)
	testutil.WriteFile(t, filepath.Join(out, "extra-files", "A", "ABBA - SOS", "old.jpg"), "img")
	refs := []RestoreRef{{SectionExtraFiles, "A/ABBA - SOS/old.jpg"}}
	before, outBefore := testutil.Tree(t, lib), testutil.Tree(t, out)
	dry := cfg
	dry.DryRun = true
	if err := Restore(dry, fsops.New(true), refs, RestoreOptions{}); err != nil {
		t.Fatal(err)
	}
	if !maps.Equal(testutil.Tree(t, lib), before) || !maps.Equal(testutil.Tree(t, out), outBefore) {
		t.Error("a dry run changed something")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := Restore(cfg, fsops.New(false), refs, RestoreOptions{Ctx: ctx}); !errors.Is(err, context.Canceled) {
		t.Errorf("stopped Restore = %v", err)
	}
	if !maps.Equal(testutil.Tree(t, lib), before) {
		t.Error("a stopped run moved something")
	}
}

// Only what ListSetAside shows can be put back: a category folder, a label folder or a
// file inside a song folder would restore a whole category or part of a song.
func TestRestoreRefusesUnlistedRefs(t *testing.T) {
	cfg, lib, out := restoreSetup(t)
	testutil.WriteFiles(t, filepath.Join(out, "invalid", "K", "K - Song"), map[string]string{"song.txt": "#ARTIST:K\n#TITLE:Song\n"})
	testutil.WriteFiles(t, filepath.Join(out, "invalid", "K", "K - Other"), map[string]string{"song.txt": "#ARTIST:K\n#TITLE:Other\n"})
	testutil.WriteFile(t, filepath.Join(out, "extra-files", "N", "Nobody - X", "old.jpg"), "img")
	before, outBefore := testutil.Tree(t, lib), testutil.Tree(t, out)

	ops := fsops.New(false)
	refs := []RestoreRef{
		{SectionInvalid, "K"},                   // a category of two songs
		{SectionInvalid, "K/K - Song/song.txt"}, // a file inside a song folder
		{SectionExtraFiles, "N"},                // a folder of leftover files
		{SectionExtraFiles, "N/Nobody - X"},
	}
	if err := Restore(cfg, ops, refs, RestoreOptions{}); err != nil {
		t.Fatal(err)
	}
	if st := ops.Stats(); st.Moved != 0 || st.Failed != int64(len(refs)) {
		t.Errorf("stats = %+v, want every ref refused", st)
	}
	if !maps.Equal(testutil.Tree(t, lib), before) || !maps.Equal(testutil.Tree(t, out), outBefore) {
		t.Error("an unlisted ref was moved")
	}
	// The listed items themselves still go back.
	ops = fsops.New(false)
	if err := Restore(cfg, ops, []RestoreRef{{SectionInvalid, "K/K - Song"}, {SectionExtraFiles, "N/Nobody - X/old.jpg"}}, RestoreOptions{}); err != nil {
		t.Fatal(err)
	}
	if st := ops.Stats(); st.Moved != 2 || st.Failed != 0 {
		t.Errorf("stats = %+v", st)
	}
}

// A song set aside from a source folder outside the library goes back only while that
// folder is there: a gone source (an unplugged drive) is not created again.
func TestRestoreBlockedWhenSourceFolderGone(t *testing.T) {
	noFFprobe(t)
	base := t.TempDir()
	lib, usb := filepath.Join(base, "lib"), filepath.Join(base, "usb")
	src := filepath.Join(usb, "songs")
	testutil.WriteFiles(t, lib, songFiles("A/ABBA - SOS", "ABBA", "SOS", "video"))
	testutil.WriteFiles(t, src, songFiles("ABBA - SOS", "ABBA", "SOS"))
	cfg := prepared(t)(config.NewAdd([][]string{{src}}, lib, filepath.Join(base, "out"), false))
	runOrganize(t, cfg, Options{Mode: ModeAdd})
	if err := os.RemoveAll(usb); err != nil {
		t.Fatal(err)
	}

	items, err := ListSetAside(cfg, fsops.New(true))
	if err != nil {
		t.Fatal(err)
	}
	want := fmt.Sprintf(blockedNoFolder, src)
	if len(items) != 1 || !items[0].Exact || items[0].Origin != filepath.Join(src, "ABBA - SOS") || items[0].Blocked != want {
		t.Fatalf("items = %+v, want the duplicate blocked with %q", items, want)
	}
	ops := fsops.New(false)
	if err := Restore(cfg, ops, []RestoreRef{{items[0].Section, items[0].Rel}}, RestoreOptions{}); err != nil {
		t.Fatal(err)
	}
	if st := ops.Stats(); st.Moved != 0 || st.Failed != 1 {
		t.Errorf("stats = %+v", st)
	}
	if testutil.Exists(usb) {
		t.Error("the restore created the gone source folder")
	}

	// With the source folder back, it can be put back.
	if err := os.MkdirAll(src, 0o755); err != nil {
		t.Fatal(err)
	}
	if items, _ = ListSetAside(cfg, fsops.New(true)); len(items) != 1 || items[0].Blocked != "" {
		t.Fatalf("items = %+v", items)
	}
	ops = fsops.New(false)
	if err := Restore(cfg, ops, []RestoreRef{{items[0].Section, items[0].Rel}}, RestoreOptions{}); err != nil {
		t.Fatal(err)
	}
	if st := ops.Stats(); st.Moved != 1 || !testutil.Exists(filepath.Join(src, "ABBA - SOS", "song.txt")) {
		t.Errorf("stats = %+v, song not put back", st)
	}
}
