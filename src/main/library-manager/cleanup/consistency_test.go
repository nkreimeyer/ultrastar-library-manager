package cleanup

import (
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"testing"
	"time"

	"github.com/nkreimeyer/library-manager/fsops"
	"github.com/nkreimeyer/library-manager/internal/testutil"
)

// TestLeftoversDoNotDelayBrokenFolder: a folder that only its leftover files kept above
// one entry is set aside in the same run (cleanup with fixtxt off, -t=false -c), so a
// second run changes nothing, and the dry run plans the same moves.
func TestLeftoversDoNotDelayBrokenFolder(t *testing.T) {
	library := map[string]string{
		"X/song.txt":    "#TITLE:Song\n#ARTIST:Artist\n: 0 1 0 la\nE\n",
		"X/junk.nfo":    "junk",
		"Good/Song.txt": songTxt,
		"Good/song.mp3": "audio",
	}

	dry := newEnv(t, true)
	testutil.WriteFiles(t, dry.SongDir, library)
	ops := fsops.New(true)
	runOps(t, dry, ops)
	var planned []string
	for _, m := range ops.PlannedMoves() {
		rel, _ := filepath.Rel(dry.SongDir, m.Src)
		planned = append(planned, rel)
	}
	slices.Sort(planned)
	if want := []string{"X", filepath.Join("X", "junk.nfo")}; !reflect.DeepEqual(planned, want) {
		t.Errorf("dry run planned moves of %q, want %q", planned, want)
	}

	cfg := newEnv(t, false)
	testutil.WriteFiles(t, cfg.SongDir, library)
	run(t, cfg)
	if testutil.Exists(filepath.Join(cfg.SongDir, "X")) {
		t.Error("X is still in the library after the first run")
	}
	if got, want := files(testutil.Tree(t, cfg.InvalidDir())), []string{"X/song.txt"}; !reflect.DeepEqual(got, want) {
		t.Errorf("invalid files = %q, want %q", got, want)
	}
	if got, want := files(testutil.Tree(t, cfg.ExtraFilesDir())), []string{"X/junk.nfo"}; !reflect.DeepEqual(got, want) {
		t.Errorf("extra files = %q, want %q", got, want)
	}

	before := testutil.Snapshot(t, cfg.SongDir)
	if st := runOps(t, cfg, fsops.New(false)); st.Moved != 0 {
		t.Errorf("second run moved %d entries, want 0", st.Moved)
	}
	if after := testutil.Snapshot(t, cfg.SongDir); after != before {
		t.Errorf("second run changed the library:\n%s\nwant\n%s", after, before)
	}
}

// TestFilesOfOtherSongTxtsAreKept: with fixtxt off, every song txt of a folder stays, so
// the files any of them references are no clutter.
func TestFilesOfOtherSongTxtsAreKept(t *testing.T) {
	cfg := newEnv(t, false)
	testutil.WriteFiles(t, cfg.SongDir, map[string]string{
		"A - S/song.txt":      "#TITLE:S\n#ARTIST:A\n#MP3:song.mp3\n: 0 1 0 la\nE\n",
		"A - S/song duet.txt": "#TITLE:S\n#ARTIST:A\n#MP3:duet.mp3\n#COVER:duet.jpg\n: 0 1 0 la\nE\n",
		"A - S/song.mp3":      "audio",
		"A - S/duet.mp3":      "audio",
		"A - S/duet.jpg":      "cover",
		"A - S/junk.nfo":      "junk",
	})
	// song.txt is the newest one, the one FindSongTxt returns.
	old := filepath.Join(cfg.SongDir, "A - S", "song duet.txt")
	past := time.Now().Add(-time.Hour)
	if err := os.Chtimes(old, past, past); err != nil {
		t.Fatal(err)
	}
	run(t, cfg)
	want := []string{"A - S/duet.jpg", "A - S/duet.mp3", "A - S/song duet.txt", "A - S/song.mp3", "A - S/song.txt"}
	if got := files(testutil.Tree(t, cfg.SongDir)); !reflect.DeepEqual(got, want) {
		t.Errorf("library files = %q, want %q", got, want)
	}
	if got, want := files(testutil.Tree(t, cfg.ExtraFilesDir())), []string{"A - S/junk.nfo"}; !reflect.DeepEqual(got, want) {
		t.Errorf("extra files = %q, want %q", got, want)
	}
}

// TestCategoryOfSymlinkedSongsStays: a category holding only symlinks to song folders is
// no broken song and stays in the library.
func TestCategoryOfSymlinkedSongsStays(t *testing.T) {
	cfg := newEnv(t, false)
	store := t.TempDir()
	testutil.WriteFiles(t, store, map[string]string{"A - B/Song.txt": songTxt, "A - B/song.mp3": "audio"})
	testutil.WriteFiles(t, cfg.SongDir, map[string]string{"Real - Song/Song.txt": songTxt, "Real - Song/song.mp3": "audio"})
	if err := os.MkdirAll(filepath.Join(cfg.SongDir, "Favourites"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(store, "A - B"), filepath.Join(cfg.SongDir, "Favourites", "A - B")); err != nil {
		t.Skipf("symlinks not supported: %v", err)
	}
	run(t, cfg)
	if _, err := os.Lstat(filepath.Join(cfg.SongDir, "Favourites", "A - B")); err != nil {
		t.Errorf("the category was moved: %v", err)
	}
	if testutil.Exists(cfg.InvalidDir()) {
		t.Errorf("invalid/ created: %q", files(testutil.Tree(t, cfg.InvalidDir())))
	}
}
