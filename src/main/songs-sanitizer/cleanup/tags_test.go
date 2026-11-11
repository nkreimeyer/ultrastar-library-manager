package cleanup

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/nkreimeyer/ultrastar-songs-sanitizer/fsops"

	log "github.com/sirupsen/logrus"
	logtest "github.com/sirupsen/logrus/hooks/test"
)

// checkTrees asserts the files left in the library and the files moved to extra-files.
func checkTrees(t *testing.T, libRoot, extraRoot string, wantLib, wantExtra []string) {
	t.Helper()
	if got := files(tree(t, libRoot)); !reflect.DeepEqual(got, wantLib) {
		t.Errorf("library files = %q, want %q", got, wantLib)
	}
	var gotExtra []string
	if _, err := os.Stat(extraRoot); err == nil {
		gotExtra = files(tree(t, extraRoot))
	}
	if !reflect.DeepEqual(gotExtra, wantExtra) {
		t.Errorf("extra files = %q, want %q", gotExtra, wantExtra)
	}
}

// A tag naming a missing file references none of the song's media: the files it may have
// meant are the song's media, not clutter (fixtxt could not choose between them, or did
// not run). Tags naming an existing file make the other candidates clutter.
func TestMediaTagsMustNameAnExistingFile(t *testing.T) {
	const notes = ": 0 1 0 la\nE\n"
	tests := []struct {
		name      string
		library   map[string]string
		pngs      map[string][2]int // name -> width, height
		wantLib   []string
		wantExtra []string
	}{
		{
			name: "stale #MP3, two audio candidates",
			library: map[string]string{
				"S/song.txt":  "#ARTIST:A\n#TITLE:S\n#MP3:old.mp3\n#BPM:300\n" + notes,
				"S/take1.mp3": "a",
				"S/take2.mp3": "b",
			},
			wantLib: []string{"S/song.txt", "S/take1.mp3", "S/take2.mp3"},
		},
		{
			name: "misspelled #MP3, the only audio file (-t=false)",
			library: map[string]string{
				"S/song.txt": "#ARTIST:A\n#TITLE:S\n#MP3:Sogn.mp3\n" + notes,
				"S/song.mp3": "a",
				"S/junk.nfo": "x",
			},
			wantLib:   []string{"S/song.mp3", "S/song.txt"},
			wantExtra: []string{"S/junk.nfo"},
		},
		{
			name: "#AUDIO names the audio file",
			library: map[string]string{
				"S/song.txt":  "#ARTIST:A\n#TITLE:S\n#AUDIO:one.mp3\n" + notes,
				"S/one.mp3":   "a",
				"S/other.mp3": "b",
			},
			wantLib:   []string{"S/one.mp3", "S/song.txt"},
			wantExtra: []string{"S/other.mp3"},
		},
		{
			name: "stale #MP3 but #AUDIO names the audio file",
			library: map[string]string{
				"S/song.txt":  "#ARTIST:A\n#TITLE:S\n#MP3:old.mp3\n#AUDIO:one.mp3\n" + notes,
				"S/one.mp3":   "a",
				"S/other.mp3": "b",
			},
			wantLib:   []string{"S/one.mp3", "S/song.txt"},
			wantExtra: []string{"S/other.mp3"},
		},
		{
			name: "#MP3 names the audio file, stale #AUDIO",
			library: map[string]string{
				"S/song.txt":  "#ARTIST:A\n#TITLE:S\n#MP3:one.mp3\n#AUDIO:old.mp3\n" + notes,
				"S/one.mp3":   "a",
				"S/other.mp3": "b",
			},
			wantLib:   []string{"S/one.mp3", "S/song.txt"},
			wantExtra: []string{"S/other.mp3"},
		},
		{
			name: "#VIDEO names one of two videos",
			library: map[string]string{
				"S/song.txt": "#ARTIST:A\n#TITLE:S\n#MP3:a.mp3\n#VIDEO:v1.mp4\n" + notes,
				"S/a.mp3":    "a",
				"S/v1.mp4":   "v",
				"S/v2.mp4":   "v",
			},
			wantLib:   []string{"S/a.mp3", "S/song.txt", "S/v1.mp4"},
			wantExtra: []string{"S/v2.mp4"},
		},
		{
			name: "stale #VIDEO, two videos",
			library: map[string]string{
				"S/song.txt": "#ARTIST:A\n#TITLE:S\n#MP3:a.mp3\n#VIDEO:old.mp4\n" + notes,
				"S/a.mp3":    "a",
				"S/v1.mp4":   "v",
				"S/v2.mp4":   "v",
			},
			wantLib: []string{"S/a.mp3", "S/song.txt", "S/v1.mp4", "S/v2.mp4"},
		},
		{
			// sq1/wide1 sort first and are the images FindCoverImage/FindBackgroundImage
			// would pick, but the tags name other existing images.
			name: "#COVER and #BACKGROUND name other images",
			library: map[string]string{
				"S/song.txt": "#ARTIST:A\n#TITLE:S\n#MP3:a.mp3\n#COVER:sq2.png\n#BACKGROUND:wide2.png\n" + notes,
				"S/a.mp3":    "a",
			},
			pngs:      map[string][2]int{"S/sq1.png": {10, 10}, "S/sq2.png": {10, 10}, "S/wide1.png": {64, 36}, "S/wide2.png": {64, 36}},
			wantLib:   []string{"S/a.mp3", "S/song.txt", "S/sq2.png", "S/wide2.png"},
			wantExtra: []string{"S/sq1.png", "S/wide1.png"},
		},
		{
			name: "stale #COVER and #BACKGROUND",
			library: map[string]string{
				"S/song.txt": "#ARTIST:A\n#TITLE:S\n#MP3:a.mp3\n#COVER:old.jpg\n#BACKGROUND:old-bg.jpg\n" + notes,
				"S/a.mp3":    "a",
			},
			pngs:      map[string][2]int{"S/sq1.png": {10, 10}, "S/sq2.png": {10, 10}, "S/wide1.png": {64, 36}, "S/wide2.png": {64, 36}},
			wantLib:   []string{"S/a.mp3", "S/song.txt", "S/sq1.png", "S/wide1.png"},
			wantExtra: []string{"S/sq2.png", "S/wide2.png"},
		},
	}
	for _, tc := range tests {
		for _, dry := range []bool{false, true} {
			name := tc.name
			if dry {
				name += " (dry run)"
			}
			t.Run(name, func(t *testing.T) {
				cfg := newEnv(t, dry)
				writeFiles(t, cfg.SongDir, tc.library)
				for rel, wh := range tc.pngs {
					writePNG(t, filepath.Join(cfg.SongDir, rel), wh[0], wh[1])
				}
				before := tree(t, cfg.SongDir)
				ops := fsops.New(dry)
				st := runOps(t, cfg, ops)
				if st.Failed != 0 {
					t.Fatalf("%d failed operations", st.Failed)
				}
				if st.Moved != int64(len(tc.wantExtra)) {
					t.Errorf("moved = %d, want %d", st.Moved, len(tc.wantExtra))
				}
				if dry {
					if after := tree(t, cfg.SongDir); !reflect.DeepEqual(before, after) {
						t.Errorf("library changed in dry-run:\nbefore %q\nafter  %q", before, after)
					}
					return
				}
				checkTrees(t, cfg.SongDir, cfg.ExtraFilesDir(), tc.wantLib, tc.wantExtra)
			})
		}
	}
}

// A broken folder without a song txt but with several files is left alone: without a txt
// nothing is known to be unreferenced, and it has too many entries for invalid/.
func TestFolderWithoutTxtIsLeftAlone(t *testing.T) {
	cfg := newEnv(t, false)
	writeFiles(t, cfg.SongDir, map[string]string{"Broken/a.mp3": "a", "Broken/b.jpg": "b"})
	ops := fsops.New(false)
	if st := runOps(t, cfg, ops); st.Failed != 0 || st.Moved != 0 {
		t.Errorf("stats = %+v, want no moves and no failures", st)
	}
	checkTrees(t, cfg.SongDir, cfg.ExtraFilesDir(), []string{"Broken/a.mp3", "Broken/b.jpg"}, nil)
	if _, err := os.Stat(cfg.InvalidDir()); !os.IsNotExist(err) {
		t.Errorf("invalid/ created (err=%v)", err)
	}
}

func TestUpperCaseUsdbIsKept(t *testing.T) {
	cfg := newEnv(t, false)
	writeFiles(t, cfg.SongDir, map[string]string{
		"Song/Song.txt": songTxt, "Song/song.mp3": "a", "Song/song.USDB": "u", "Song/junk.nfo": "x",
	})
	run(t, cfg)
	checkTrees(t, cfg.SongDir, cfg.ExtraFilesDir(),
		[]string{"Song/Song.txt", "Song/song.USDB", "Song/song.mp3"}, []string{"Song/junk.nfo"})
}

// errorLogs returns the messages of the error-level entries logged during fn.
func errorLogs(t *testing.T, fn func()) []string {
	t.Helper()
	hook := logtest.NewGlobal()
	defer log.StandardLogger().ReplaceHooks(make(log.LevelHooks))
	fn()
	var msgs []string
	for _, e := range hook.AllEntries() {
		if e.Level <= log.ErrorLevel {
			msgs = append(msgs, e.Message)
		}
	}
	return msgs
}

func containsPrefix(msgs []string, prefix string) bool {
	for _, m := range msgs {
		if strings.HasPrefix(m, prefix) {
			return true
		}
	}
	return false
}

// Successful moves to invalid/ and extra-files/ log no error.
func TestSuccessfulMovesLogNoError(t *testing.T) {
	cfg := newEnv(t, false)
	writeFiles(t, cfg.SongDir, map[string]string{
		"Good/Song.txt": songTxt, "Good/song.mp3": "a", "Good/junk.nfo": "x", "Broken/lonely.mp3": "a",
	})
	ops := fsops.New(false)
	msgs := errorLogs(t, func() { runOps(t, cfg, ops) })
	if st := ops.Stats(); st.Moved != 2 || st.Failed != 0 {
		t.Errorf("stats = %+v, want 2 moves and no failures", st)
	}
	if len(msgs) != 0 {
		t.Errorf("error logs %q, want none", msgs)
	}
}

// A move into invalid/ or extra-files/ that fails is logged, counted as failed, and
// leaves the source in place; Run itself still succeeds.
func TestFailedMovesAreLoggedAndCounted(t *testing.T) {
	tests := []struct {
		name, blocked, logPrefix string
		library                  map[string]string
	}{
		{"invalid", "invalid", "moving broken song folder", map[string]string{
			"Good/Song.txt": songTxt, "Good/song.mp3": "a", "Broken/lonely.mp3": "a",
		}},
		{"extra-files", "extra-files", "moving unreferenced file to extra-files", map[string]string{
			"Good/Song.txt": songTxt, "Good/song.mp3": "a", "Good/junk.nfo": "x",
		}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			cfg := newEnv(t, false)
			writeFiles(t, cfg.SongDir, tc.library)
			// A regular file where the target folder should be makes every move into it fail.
			if err := os.WriteFile(filepath.Join(cfg.OutDir, tc.blocked), []byte("file"), 0o644); err != nil {
				t.Fatal(err)
			}
			before := tree(t, cfg.SongDir)
			ops := fsops.New(false)
			msgs := errorLogs(t, func() { runOps(t, cfg, ops) })
			if st := ops.Stats(); st.Failed == 0 || st.Moved != 0 {
				t.Errorf("stats = %+v, want a failure and no moves", st)
			}
			if !containsPrefix(msgs, tc.logPrefix) {
				t.Errorf("error logs %q, want one starting with %q", msgs, tc.logPrefix)
			}
			if after := tree(t, cfg.SongDir); !reflect.DeepEqual(before, after) {
				t.Errorf("library changed:\nbefore %q\nafter  %q", before, after)
			}
		})
	}
}

// filterEntries skips a folder it cannot list and counts it once.
func TestFilterEntriesSkipsUnreadableFolder(t *testing.T) {
	cfg := newEnv(t, false)
	writeFiles(t, cfg.SongDir, map[string]string{"Good/Song.txt": songTxt, "Good/song.mp3": "a"})
	good, gone := filepath.Join(cfg.SongDir, "Good"), filepath.Join(cfg.SongDir, "Gone")
	ops := fsops.New(false)
	for range 2 {
		if got := filterEntries(cfg, ops, []string{gone, good}); !reflect.DeepEqual(got, []string{good}) {
			t.Errorf("filterEntries = %q, want %q", got, []string{good})
		}
	}
	if st := ops.Stats(); st.Failed != 1 || st.Moved != 0 {
		t.Errorf("stats = %+v, want 1 failure (counted once) and no moves", st)
	}
}
