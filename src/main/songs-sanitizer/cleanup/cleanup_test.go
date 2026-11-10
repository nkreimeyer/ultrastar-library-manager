package cleanup

import (
	"io/fs"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"testing"

	"github.com/nkreimeyer/ultrastar-songs-sanitizer/config"
	"github.com/nkreimeyer/ultrastar-songs-sanitizer/fsops"
)

const songTxt = "#TITLE:Song\n#ARTIST:Artist\n#MP3:song.mp3\n#COVER:cover.jpg\n: 0 1 0 la\nE\n"

// writeFiles creates the given files (path relative to root -> content) below root.
func writeFiles(t *testing.T, root string, files map[string]string) {
	t.Helper()
	for rel, content := range files {
		path := filepath.Join(root, rel)
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
}

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

// tree returns all paths below root (relative, slash-separated) mapped to their content
// ("<dir>" for folders).
func tree(t *testing.T, root string) map[string]string {
	t.Helper()
	got := map[string]string{}
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(root, path)
		if err != nil || rel == "." {
			return err
		}
		rel = filepath.ToSlash(rel)
		if d.IsDir() {
			got[rel] = "<dir>"
			return nil
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		got[rel] = string(data)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return got
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
			writeFiles(t, cfg.SongDir, tc.library)
			run(t, cfg)

			if got := files(tree(t, cfg.SongDir)); !reflect.DeepEqual(got, tc.wantLib) {
				t.Errorf("library files = %q, want %q", got, tc.wantLib)
			}
			var gotExtra []string
			if _, err := os.Stat(cfg.ExtraFilesDir()); err == nil {
				gotExtra = files(tree(t, cfg.ExtraFilesDir()))
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
	writeFiles(t, cfg.SongDir, good)

	for i, want := range []string{"Cat/Broken", "Cat/Broken (2)"} {
		writeFiles(t, cfg.SongDir, map[string]string{"Cat/Broken/lonely.mp3": "run " + want})
		run(t, cfg)
		got := tree(t, cfg.InvalidDir())
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
	writeFiles(t, cfg.SongDir, map[string]string{
		"Cat/Song/Song.txt":     songTxt,
		"Cat/Song/song.mp3":     "audio",
		"Cat/Song/junk.jpg":     "junk",
		"Cat/Broken/lonely.mp3": "audio",
		"Flat/SONG.TXT":         songTxt,
		"Flat/song.mp3":         "audio",
		"Flat/extra.nfo":        "junk",
	})
	before := tree(t, cfg.SongDir)
	run(t, cfg)
	if after := tree(t, cfg.SongDir); !reflect.DeepEqual(before, after) {
		t.Errorf("library changed in dry-run:\nbefore %q\nafter  %q", before, after)
	}
	for _, dir := range []string{cfg.InvalidDir(), cfg.ExtraFilesDir()} {
		if _, err := os.Stat(dir); !os.IsNotExist(err) {
			t.Errorf("%s created in dry-run (err=%v)", dir, err)
		}
	}
}
