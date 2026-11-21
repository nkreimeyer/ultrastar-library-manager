package cleanup

import (
	"image"
	"image/png"
	"io/fs"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"testing"

	"github.com/nkreimeyer/library-manager/config"
	"github.com/nkreimeyer/library-manager/fsops"
	"github.com/nkreimeyer/library-manager/internal/testutil"
)

const songTxt = "#TITLE:Song\n#ARTIST:Artist\n#MP3:song.mp3\n#COVER:cover.jpg\n: 0 1 0 la\nE\n"

// newEnv returns a config with a fresh library and a separate fresh output folder.
func newEnv(t *testing.T, dryRun bool) config.Config {
	t.Helper()
	cfg, err := config.New(t.TempDir(), t.TempDir(), dryRun)
	if err != nil {
		t.Fatal(err)
	}
	return cfg
}

// run executes the module and fails the test on a module error or failed operations.
func run(t *testing.T, cfg config.Config) {
	t.Helper()
	ops := fsops.New(cfg.DryRun)
	if err := Run(cfg, ops); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if st := ops.Stats(); st.Failed != 0 {
		t.Fatalf("%d failed operations", st.Failed)
	}
}

func files(m map[string]string) []string {
	var out []string
	for k, v := range m {
		if v != "<dir>" {
			out = append(out, k)
		}
	}
	sort.Strings(out)
	return out
}

func TestRun(t *testing.T) {
	tests := []struct {
		name      string
		library   map[string]string
		wantLib   []string // files remaining in the library
		wantExtra []string // files below <out>/extra-files
	}{
		{
			name: "flat library",
			library: map[string]string{
				"Song/Song.txt":  songTxt,
				"Song/song.mp3":  "audio",
				"Song/cover.jpg": "cover",
				"Song/junk.jpg":  "junk",
			},
			wantLib:   []string{"Song/Song.txt", "Song/cover.jpg", "Song/song.mp3"},
			wantExtra: []string{"Song/junk.jpg"},
		},
		{
			name: "nested library",
			library: map[string]string{
				"Cat/Song/Song.txt":  songTxt,
				"Cat/Song/song.mp3":  "audio",
				"Cat/Song/cover.jpg": "cover",
				"Cat/Song/junk.jpg":  "junk",
			},
			wantLib:   []string{"Cat/Song/Song.txt", "Cat/Song/cover.jpg", "Cat/Song/song.mp3"},
			wantExtra: []string{"Cat/Song/junk.jpg"},
		},
		{
			name: "upper-case txt is kept",
			library: map[string]string{
				"Song/SONG.TXT": songTxt,
				"Song/song.mp3": "audio",
			},
			wantLib: []string{"Song/SONG.TXT", "Song/song.mp3"},
		},
		{
			name: "License.txt sorting before the song txt",
			library: map[string]string{
				"Song/License.txt": "Some license text mentioning nothing.\n",
				"Song/Song.txt":    songTxt,
				"Song/song.mp3":    "audio",
				"Song/cover.jpg":   "cover",
				"Song/junk.jpg":    "junk",
			},
			wantLib:   []string{"Song/License.txt", "Song/Song.txt", "Song/cover.jpg", "Song/song.mp3"},
			wantExtra: []string{"Song/junk.jpg"},
		},
		{
			name: "NFD filename referenced in NFC txt",
			library: map[string]string{
				"Song/Song.txt":              "#TITLE:M\u00e4dchen\n#MP3:M\u00e4dchen.mp3\n", // NFC
				"Song/Ma\u0308dchen.mp3":     "audio",                                        // NFD
				"Song/unreferenced file.png": "junk",
			},
			wantLib:   []string{"Song/Ma\u0308dchen.mp3", "Song/Song.txt"},
			wantExtra: []string{"Song/unreferenced file.png"},
		},
		{
			name: "CP1252 txt referencing a UTF-8 filename",
			library: map[string]string{
				"Song/Song.txt":         "#TITLE:M\xe4dchen\n#MP3:M\xe4dchen.mp3\n",
				"Song/M\u00e4dchen.mp3": "audio", // UTF-8 (NFC) filename
			},
			wantLib: []string{"Song/M\u00e4dchen.mp3", "Song/Song.txt"},
		},
		{
			name: "usdb is kept, leftover temp files of interrupted runs are moved away",
			library: map[string]string{
				"Song/Song.txt":                     songTxt,
				"Song/song.mp3":                     "audio",
				"Song/song.usdb":                    "usdb",
				"Song/.song.sanitizer-123456.mp3":   "tmp",
				"Song/.Song.txt.987654.tmp":         "tmp",
				"Song/.hidden":                      "junk",
				"Song/sub/nested-file-is-untouched": "x",
			},
			wantLib: []string{
				"Song/Song.txt", "Song/song.mp3", "Song/song.usdb", "Song/sub/nested-file-is-untouched",
			},
			wantExtra: []string{"Song/.Song.txt.987654.tmp", "Song/.hidden", "Song/.song.sanitizer-123456.mp3"},
		},
		{
			name: "folder with one file goes to invalid",
			library: map[string]string{
				"Cat/Good/Song.txt":     songTxt,
				"Cat/Good/song.mp3":     "audio",
				"Cat/Broken/lonely.mp3": "audio",
			},
			wantLib: []string{"Cat/Good/Song.txt", "Cat/Good/song.mp3"},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			cfg := newEnv(t, false)
			testutil.WriteFiles(t, cfg.SongDir, tc.library)
			run(t, cfg)

			if got := files(testutil.Tree(t, cfg.SongDir)); !reflect.DeepEqual(got, tc.wantLib) {
				t.Errorf("library files = %q, want %q", got, tc.wantLib)
			}
			var gotExtra []string
			if _, err := os.Stat(cfg.ExtraFilesDir()); err == nil {
				gotExtra = files(testutil.Tree(t, cfg.ExtraFilesDir()))
			}
			if !reflect.DeepEqual(gotExtra, tc.wantExtra) {
				t.Errorf("extra files = %q, want %q", gotExtra, tc.wantExtra)
			}
			for _, name := range []string{"invalid", "extra-files"} {
				if _, err := os.Stat(filepath.Join(cfg.SongDir, name)); err == nil {
					t.Errorf("%s/ was created inside the library", name)
				}
			}
		})
	}
}

func TestInvalidFolderCollision(t *testing.T) {
	cfg := newEnv(t, false)
	good := map[string]string{"Cat/Good/Song.txt": songTxt, "Cat/Good/song.mp3": "audio"}
	testutil.WriteFiles(t, cfg.SongDir, good)

	for i, want := range []string{"Cat/Broken", "Cat/Broken (2)"} {
		testutil.WriteFiles(t, cfg.SongDir, map[string]string{"Cat/Broken/lonely.mp3": "run " + want})
		run(t, cfg)
		got := testutil.Tree(t, cfg.InvalidDir())
		if got[want+"/lonely.mp3"] != "run "+want {
			t.Fatalf("run %d: invalid tree = %q, want %s/lonely.mp3", i+1, got, want)
		}
		if _, err := os.Stat(filepath.Join(cfg.SongDir, "Cat", "Broken")); !os.IsNotExist(err) {
			t.Fatalf("run %d: Cat/Broken still in the library (err=%v)", i+1, err)
		}
	}
}

func TestDryRunLeavesLibraryUntouched(t *testing.T) {
	cfg := newEnv(t, true)
	testutil.WriteFiles(t, cfg.SongDir, map[string]string{
		"Cat/Song/Song.txt":     songTxt,
		"Cat/Song/song.mp3":     "audio",
		"Cat/Song/junk.jpg":     "junk",
		"Cat/Broken/lonely.mp3": "audio",
		"Flat/SONG.TXT":         songTxt,
		"Flat/song.mp3":         "audio",
		"Flat/extra.nfo":        "junk",
	})
	before := testutil.Tree(t, cfg.SongDir)
	run(t, cfg)
	if after := testutil.Tree(t, cfg.SongDir); !reflect.DeepEqual(before, after) {
		t.Errorf("library changed in dry-run:\nbefore %q\nafter  %q", before, after)
	}
	for _, dir := range []string{cfg.InvalidDir(), cfg.ExtraFilesDir()} {
		if _, err := os.Stat(dir); !os.IsNotExist(err) {
			t.Errorf("%s created in dry-run (err=%v)", dir, err)
		}
	}
}

// runOps executes the module with the given ops and fails the test on a module error.
func runOps(t *testing.T, cfg config.Config, ops *fsops.Ops) fsops.Stats {
	t.Helper()
	if err := Run(cfg, ops); err != nil {
		t.Fatalf("Run: %v", err)
	}
	return ops.Stats()
}

func TestNonUTF8FileNamesAreNotMoved(t *testing.T) {
	cfg := newEnv(t, false)
	// The txt was converted to UTF-8, the file name is still CP1252 ("M\xe4dchen").
	testutil.WriteFiles(t, cfg.SongDir, map[string]string{
		"Song/Song.txt":       "#TITLE:Mädchen\n#MP3:Mädchen.mp3\n",
		"Song/M\xe4dchen.mp3": "audio",
		"Song/junk.jpg":       "junk",
	})
	run(t, cfg)
	if got, want := files(testutil.Tree(t, cfg.SongDir)), []string{"Song/M\xe4dchen.mp3", "Song/Song.txt"}; !reflect.DeepEqual(got, want) {
		t.Errorf("library files = %q, want %q", got, want)
	}
	if got, want := files(testutil.Tree(t, cfg.ExtraFilesDir())), []string{"Song/junk.jpg"}; !reflect.DeepEqual(got, want) {
		t.Errorf("extra files = %q, want %q", got, want)
	}
}

func TestHiddenFilesDoNotCountAsSongContent(t *testing.T) {
	cfg := newEnv(t, false)
	testutil.WriteFiles(t, cfg.SongDir, map[string]string{
		"Cat/Good/Song.txt":           songTxt,
		"Cat/Good/song.mp3":           "audio",
		"Cat/Broken/lonely.mp3":       "audio",
		"Cat/Broken/._lonely.mp3":     "appledouble",
		"Cat/Broken/@eaDir/thumb.jpg": "thumb",
	})
	run(t, cfg)
	want := []string{"Cat/Broken/._lonely.mp3", "Cat/Broken/@eaDir/thumb.jpg", "Cat/Broken/lonely.mp3"}
	if got := files(testutil.Tree(t, cfg.InvalidDir())); !reflect.DeepEqual(got, want) {
		t.Errorf("invalid files = %q, want %q", got, want)
	}
}

func TestSymlinkToFolderIsLeftAlone(t *testing.T) {
	cfg := newEnv(t, false)
	testutil.WriteFiles(t, cfg.SongDir, map[string]string{"Song/Song.txt": songTxt, "Song/song.mp3": "audio"})
	if err := os.Symlink(t.TempDir(), filepath.Join(cfg.SongDir, "Song", "videos")); err != nil {
		t.Skipf("symlinks not supported: %v", err)
	}
	run(t, cfg)
	if _, err := os.Lstat(filepath.Join(cfg.SongDir, "Song", "videos")); err != nil {
		t.Errorf("symlink to a folder was moved: %v", err)
	}
	if _, err := os.Stat(cfg.ExtraFilesDir()); !os.IsNotExist(err) {
		t.Errorf("extra-files created (err=%v)", err)
	}
}

// TestEmptiedCategoryIsMerged simulates fixtxt moving the only song of a category to
// invalid/Cat/Song: the now empty Cat folder is merged into invalid/Cat.
func TestEmptiedCategoryIsMerged(t *testing.T) {
	cfg := newEnv(t, false)
	testutil.WriteFiles(t, cfg.SongDir, map[string]string{
		"Cat/Song/lonely.mp3": "audio",
		"Other/Good/Song.txt": songTxt,
		"Other/Good/song.mp3": "audio",
	})
	ops := fsops.New(false)
	song := filepath.Join(cfg.SongDir, "Cat", "Song")
	if _, err := ops.MoveInto(song, cfg.InvalidDir(), cfg.RelPath(song)); err != nil {
		t.Fatal(err)
	}
	if st := runOps(t, cfg, ops); st.Failed != 0 || st.Moved != 2 {
		t.Fatalf("stats = %+v, want 2 moves and no failures", st)
	}
	if _, err := os.Stat(filepath.Join(cfg.SongDir, "Cat")); !os.IsNotExist(err) {
		t.Errorf("Cat still in the library (err=%v)", err)
	}
	want := map[string]string{"Cat": "<dir>", "Cat/Song": "<dir>", "Cat/Song/lonely.mp3": "audio"}
	if got := testutil.Tree(t, cfg.InvalidDir()); !reflect.DeepEqual(got, want) {
		t.Errorf("invalid tree = %q, want %q (no \"Cat (2)\")", got, want)
	}
}

// TestDryRunIgnoresPlannedMoves: in dry-run mode, a category whose songs an earlier module
// would move to invalid/ is still on disk, but is planned as empty, like in a real run.
func TestDryRunIgnoresPlannedMoves(t *testing.T) {
	cfg := newEnv(t, true)
	testutil.WriteFiles(t, cfg.SongDir, map[string]string{
		"Cat/A/lonely.mp3":    "audio",
		"Cat/B/lonely.mp3":    "audio",
		"Other/Good/Song.txt": songTxt,
		"Other/Good/song.mp3": "audio",
		"Other/Good/junk.jpg": "junk",
	})
	before := testutil.Tree(t, cfg.SongDir)
	ops := fsops.New(true)
	for _, rel := range []string{"Cat/A", "Cat/B", "Other/Good/junk.jpg"} {
		p := filepath.Join(cfg.SongDir, rel)
		if _, err := ops.MoveInto(p, cfg.InvalidDir(), rel); err != nil {
			t.Fatal(err)
		}
	}
	// Cat is planned as empty (+1); junk.jpg is already planned and not moved twice.
	if st := runOps(t, cfg, ops); st.Failed != 0 || st.Moved != 4 {
		t.Fatalf("stats = %+v, want 4 planned moves and no failures", st)
	}
	if after := testutil.Tree(t, cfg.SongDir); !reflect.DeepEqual(before, after) {
		t.Errorf("library changed in dry-run:\nbefore %q\nafter  %q", before, after)
	}
}

func TestUnreadableSongTxtMovesNothing(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root can read every file")
	}
	cfg := newEnv(t, false)
	testutil.WriteFiles(t, cfg.SongDir, map[string]string{
		// Discovery finds A.txt; B.txt may be the newer song txt that references junk.jpg.
		"Song/A.txt":     songTxt,
		"Song/B.txt":     "#TITLE:Song\n#MP3:song.mp3\n#BACKGROUND:junk.jpg\n",
		"Song/song.mp3":  "audio",
		"Song/junk.jpg":  "junk",
		"Song/other.png": "junk",
		// The only txt is unreadable: discovery skips the folder, cleanupDirectory too.
		"Locked/Song.txt": songTxt,
		"Locked/junk.jpg": "junk",
	})
	for _, rel := range []string{"Song/B.txt", "Locked/Song.txt"} {
		p := filepath.Join(cfg.SongDir, rel)
		if err := os.Chmod(p, 0); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { os.Chmod(p, 0o644) })
	}
	before := tree0(t, cfg.SongDir)

	ops := fsops.New(false)
	st := runOps(t, cfg, ops)
	cleanupDirectory(cfg, ops, filepath.Join(cfg.SongDir, "Locked"))
	cleanupDirectory(cfg, ops, filepath.Join(cfg.SongDir, "Locked"))
	if st.Moved != 0 {
		t.Errorf("%d files moved, want none", st.Moved)
	}
	// Song/B.txt is counted by cleanup, Locked/Song.txt by discovery and then never again.
	if got := ops.Stats().Failed; got != 2 {
		t.Errorf("failed = %d, want 2 (each unreadable txt counted once)", got)
	}
	if after := tree0(t, cfg.SongDir); !reflect.DeepEqual(before, after) {
		t.Errorf("library changed:\nbefore %q\nafter  %q", before, after)
	}
	if _, err := os.Stat(cfg.ExtraFilesDir()); !os.IsNotExist(err) {
		t.Errorf("extra-files created (err=%v)", err)
	}
}

// tree0 lists the paths below root without reading file contents (some are unreadable).
func tree0(t *testing.T, root string) []string {
	t.Helper()
	var got []string
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(root, path)
		got = append(got, filepath.ToSlash(rel))
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return got
}

func TestUntaggedMediaIsKept(t *testing.T) {
	cfg := newEnv(t, false)
	testutil.WriteFiles(t, cfg.SongDir, map[string]string{
		// No #MP3/#AUDIO/#VIDEO yet (fixtxt could not choose, or dry-run): media stays.
		"A/song.txt": "#TITLE:A\n#ARTIST:A\n: 0 1 0 la\nE\n",
		"A/one.mp3":  "a",
		"A/two.mp3":  "a",
		"A/clip.mp4": "v",
		"A/junk.nfo": "x",
		// #MP3 set, so the second audio file is clutter; no #VIDEO, so the video stays.
		"B/song.txt":  "#TITLE:B\n#ARTIST:B\n#MP3:one.mp3\n: 0 1 0 la\nE\n",
		"B/one.mp3":   "a",
		"B/other.mp3": "a",
		"B/clip.mp4":  "v",
	})
	run(t, cfg)
	want := []string{"A/clip.mp4", "A/one.mp3", "A/song.txt", "A/two.mp3", "B/clip.mp4", "B/one.mp3", "B/song.txt"}
	if got := files(testutil.Tree(t, cfg.SongDir)); !reflect.DeepEqual(got, want) {
		t.Errorf("library files = %q, want %q", got, want)
	}
	if got := files(testutil.Tree(t, cfg.ExtraFilesDir())); !reflect.DeepEqual(got, []string{"A/junk.nfo", "B/other.mp3"}) {
		t.Errorf("extra files = %q", got)
	}
}

func TestUntaggedCoverAndProblemTxtsAreKept(t *testing.T) {
	cfg := newEnv(t, false)
	testutil.WriteFiles(t, cfg.SongDir, map[string]string{
		// No #COVER tag yet: the square image fixtxt would pick stays, other clutter goes.
		"A/song.txt": "#TITLE:A\n#ARTIST:A\n#MP3:song.mp3\n: 0 1 0 la\nE\n",
		"A/song.mp3": "a",
		"A/junk.nfo": "x",
		// The txt has an encoding problem (byte 0x81): fixtxt leaves it alone, so cleanup does too.
		"B/song.txt": "#TITLE:B\x81\n#ARTIST:B\n#MP3:song.mp3\n: 0 1 0 la\nE\n",
		"B/song.mp3": "a",
		"B/junk.nfo": "x",
	})
	writePNG(t, filepath.Join(cfg.SongDir, "A", "cover.png"), 4, 4)
	run(t, cfg)
	want := []string{"A/cover.png", "A/song.mp3", "A/song.txt", "B/junk.nfo", "B/song.mp3", "B/song.txt"}
	if got := files(testutil.Tree(t, cfg.SongDir)); !reflect.DeepEqual(got, want) {
		t.Errorf("library files = %q, want %q", got, want)
	}
}

func writePNG(t *testing.T, path string, w, h int) {
	t.Helper()
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	if err := png.Encode(f, image.NewGray(image.Rect(0, 0, w, h))); err != nil {
		t.Fatal(err)
	}
}

// Untagged images fixtxt would choose by name hint or by near-square shape stay; the
// other images are clutter.
func TestUntaggedHintedImagesAreKept(t *testing.T) {
	cfg := newEnv(t, false)
	testutil.WriteFiles(t, cfg.SongDir, map[string]string{
		"A/song.txt": "#TITLE:A\n#ARTIST:A\n#MP3:song.mp3\n: 0 1 0 la\nE\n",
		"A/song.mp3": "a",
		// Lossless WebP header (VP8L) of 640x360, hinted as background.
		"A/Song [BG].webp": "RIFF\x15\x00\x00\x00WEBPVP8L\x05\x00\x00\x00\x2f\x7f\xc2\x59\x00",
		"B/song.txt":       "#TITLE:B\n#ARTIST:B\n#MP3:song.mp3\n: 0 1 0 la\nE\n",
		"B/song.mp3":       "a",
	})
	writePNG(t, filepath.Join(cfg.SongDir, "A", "Song [CO].png"), 64, 36) // hint beats shape
	writePNG(t, filepath.Join(cfg.SongDir, "A", "square.png"), 10, 10)
	writePNG(t, filepath.Join(cfg.SongDir, "A", "wide.png"), 64, 36)
	writePNG(t, filepath.Join(cfg.SongDir, "B", "a.png"), 600, 598) // square within 5 %
	writePNG(t, filepath.Join(cfg.SongDir, "B", "b.png"), 600, 600)
	run(t, cfg)
	want := []string{"A/Song [BG].webp", "A/Song [CO].png", "A/song.mp3", "A/song.txt", "B/a.png", "B/song.mp3", "B/song.txt"}
	if got := files(testutil.Tree(t, cfg.SongDir)); !reflect.DeepEqual(got, want) {
		t.Errorf("library files = %q, want %q", got, want)
	}
	if got, want := files(testutil.Tree(t, cfg.ExtraFilesDir())), []string{"A/square.png", "A/wide.png", "B/b.png"}; !reflect.DeepEqual(got, want) {
		t.Errorf("extra files = %q, want %q", got, want)
	}
}
