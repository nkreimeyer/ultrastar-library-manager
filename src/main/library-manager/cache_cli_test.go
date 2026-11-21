package main

import (
	"compress/gzip"
	"encoding/json"
	"fmt"
	"io/fs"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/nkreimeyer/library-manager/internal/testutil"
	"github.com/nkreimeyer/library-manager/mediacache"
	logtest "github.com/sirupsen/logrus/hooks/test"
)

// cacheStats is the summary line the media cache logs when it is saved.
type cacheStats struct{ reused, scanned, stored int }

// lastCacheStats returns the last "Media cache: N reused, M scanned, K stored" line logged
// since hook was reset, and resets it.
func lastCacheStats(t *testing.T, hook *logtest.Hook) cacheStats {
	t.Helper()
	defer hook.Reset()
	var got *cacheStats
	for _, e := range hook.AllEntries() {
		var s cacheStats
		if _, err := fmt.Sscanf(e.Message, "Media cache: %d reused, %d scanned, %d stored", &s.reused, &s.scanned, &s.stored); err == nil {
			got = &s
		}
	}
	if got == nil {
		t.Fatal("no media cache summary logged")
	}
	return *got
}

// ageTree sets the mtime of every file below root to an hour ago: the cache never stores a
// result of a file changed within the last seconds (mediacache.RacyWindow).
func ageTree(t *testing.T, root string) {
	t.Helper()
	old := time.Now().Add(-time.Hour)
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		return os.Chtimes(path, old, old)
	})
	if err != nil {
		t.Fatal(err)
	}
}

// mediaSong writes the song folder dir: a txt (lyric makes its notes unique), a 3 s mp3
// and, with video, a small mpeg4 video.
func mediaSong(t *testing.T, dir, artist, title, lyric string, video bool) {
	t.Helper()
	txt := fmt.Sprintf("#ARTIST:%s\n#TITLE:%s\n#MP3:song.mp3\n", artist, title)
	if video {
		txt += "#VIDEO:video.mp4\n"
	}
	testutil.WriteFile(t, filepath.Join(dir, "song.txt"), txt+testutil.NotesTxt(lyric))
	testutil.GenerateMediaAt(t, filepath.Join(dir, "song.mp3"), "-f", "lavfi", "-i", "sine=frequency=440:duration=3", "-c:a", "libmp3lame", "-b:a", "128k")
	if video {
		testutil.GenerateMediaAt(t, filepath.Join(dir, "video.mp4"), "-f", "lavfi", "-i", "testsrc=size=160x120:rate=10:duration=3", "-c:v", "mpeg4")
	}
}

// cacheFolders returns the folder keys stored in the cache file of root.
func cacheFolders(t *testing.T, root string) []string {
	t.Helper()
	f, err := os.Open(filepath.Join(root, mediacache.FileName))
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	zr, err := gzip.NewReader(f)
	if err != nil {
		t.Fatal(err)
	}
	var d struct {
		Folders map[string]json.RawMessage `json:"folders"`
	}
	if err := json.NewDecoder(zr).Decode(&d); err != nil {
		t.Fatal(err)
	}
	return slices.Sorted(maps.Keys(d.Folders))
}

// TestRunCheckReusesProbes: a second run of the main command with -k probes nothing: every
// media result comes from the cache the first run saved in the song dir, and the report
// is the same.
func TestRunCheckReusesProbes(t *testing.T) {
	testutil.RequireFFmpeg(t)
	lib, out := t.TempDir(), t.TempDir()
	mediaSong(t, filepath.Join(lib, "Cat", "ABBA - SOS"), "ABBA", "SOS", "sos", true)
	mediaSong(t, filepath.Join(lib, "Cat", "Queen - Bohemian Rhapsody"), "Queen", "Bohemian Rhapsody", "bo", false)
	ageTree(t, lib)
	hook := testutil.CaptureLogs(t)

	if got := runWithArgs(t, "-s", lib, "-o", out, "-k", "-deep"); got != exitOK {
		t.Fatalf("first run = %d", got)
	}
	first := lastCacheStats(t, hook)
	if first.reused != 0 || first.scanned == 0 || first.stored != first.scanned {
		t.Fatalf("first run: %+v, want every lookup scanned and stored", first)
	}
	report := testutil.ReadFile(t, filepath.Join(out, "check_report.json"))

	if got := runWithArgs(t, "-s", lib, "-o", out, "-k", "-deep"); got != exitOK {
		t.Fatalf("second run = %d", got)
	}
	if second := lastCacheStats(t, hook); second != (cacheStats{reused: first.scanned}) {
		t.Errorf("second run: %+v, want %d reused and nothing scanned", second, first.scanned)
	}
	if again := testutil.ReadFile(t, filepath.Join(out, "check_report.json")); again != report {
		t.Errorf("report from the cache differs:\n%s\nfirst:\n%s", again, report)
	}
}

// TestDedupeDryRunCacheServesRealRun: a dedupe dry run leaves the library byte-identical
// except for the cache file, and the real run after it reuses the dry run's probes.
func TestDedupeDryRunCacheServesRealRun(t *testing.T) {
	testutil.RequireFFmpeg(t)
	lib, out := t.TempDir(), t.TempDir()
	mediaSong(t, filepath.Join(lib, "A", "ABBA - SOS"), "ABBA", "SOS", "sos", true)
	mediaSong(t, filepath.Join(lib, "A", "ABBA - SOS (2)"), "ABBA", "SOS", "sos2", false) // the loser: no video
	mediaSong(t, filepath.Join(lib, "Q", "Queen - Bohemian Rhapsody"), "Queen", "Bohemian Rhapsody", "bo", false)
	ageTree(t, lib)
	// The root folder's own mtime changes when the cache file is written into it.
	below := func() string { s := testutil.Snapshot(t, lib); return s[strings.IndexByte(s, '\n')+1:] }
	before := below()
	hook := testutil.CaptureLogs(t)

	if got := runWithArgs(t, "dedupe", "-l", lib, "-o", out, "-n"); got != exitOK {
		t.Fatalf("dry run = %d", got)
	}
	dry := lastCacheStats(t, hook)
	if dry.scanned == 0 || dry.stored != dry.scanned {
		t.Fatalf("dry run: %+v, want every lookup scanned and stored", dry)
	}
	cache := filepath.Join(lib, mediacache.FileName)
	if _, err := os.Stat(cache); err != nil {
		t.Fatalf("the dry run saved no cache: %v", err)
	}
	moved := filepath.Join(t.TempDir(), mediacache.FileName)
	if err := os.Rename(cache, moved); err != nil {
		t.Fatal(err)
	}
	if after := below(); after != before {
		t.Errorf("the dry run changed the library beyond the cache file:\n%s\nbefore:\n%s", after, before)
	}
	if err := os.Rename(moved, cache); err != nil {
		t.Fatal(err)
	}

	if got := runWithArgs(t, "dedupe", "-l", lib, "-o", out); got != exitOK {
		t.Fatalf("real run = %d", got)
	}
	if real := lastCacheStats(t, hook); real.scanned != 0 || real.reused != dry.scanned {
		t.Errorf("real run: %+v, want %d reused and nothing scanned", real, dry.scanned)
	}
	if _, err := os.Stat(filepath.Join(out, "duplicates")); err != nil {
		t.Errorf("the real run set no duplicate aside: %v", err)
	}
}

// TestOrganizeMovedSongsKeepCacheEntries: organize probes the songs in their source and
// moves them into the library's buckets with their mtimes; the next run finds their results
// by name, size and mtime and stores them under the new folders.
func TestOrganizeMovedSongsKeepCacheEntries(t *testing.T) {
	testutil.RequireFFmpeg(t)
	base := t.TempDir()
	src, lib, out := filepath.Join(base, "src"), filepath.Join(base, "lib"), filepath.Join(base, "out")
	mediaSong(t, filepath.Join(src, "ABBA - SOS"), "ABBA", "SOS", "sos", true)
	mediaSong(t, filepath.Join(src, "x", "Queen - Bohemian Rhapsody"), "Queen", "Bohemian Rhapsody", "bo", false)
	ageTree(t, src)
	if err := os.Mkdir(lib, 0o755); err != nil {
		t.Fatal(err)
	}
	hook := testutil.CaptureLogs(t)

	if got := runWithArgs(t, "organize", "-s", src, "-l", lib, "-o", out); got != exitOK {
		t.Fatalf("organize = %d", got)
	}
	first := lastCacheStats(t, hook)
	if first.scanned == 0 || first.stored != first.scanned {
		t.Fatalf("organize: %+v, want every lookup scanned and stored", first)
	}
	if _, err := os.Stat(filepath.Join(lib, "Q", "Queen - Bohemian Rhapsody", "song.mp3")); err != nil {
		t.Fatalf("song not moved into its bucket: %v", err)
	}

	if got := runWithArgs(t, "dedupe", "-l", lib, "-o", out); got != exitOK {
		t.Fatalf("dedupe = %d", got)
	}
	if second := lastCacheStats(t, hook); second.scanned != 0 || second.reused != first.scanned {
		t.Errorf("dedupe after organize: %+v, want %d reused and nothing scanned", second, first.scanned)
	}
	if got, want := cacheFolders(t, lib), []string{"A/ABBA - SOS", "Q/Queen - Bohemian Rhapsody"}; !slices.Equal(got, want) {
		t.Errorf("cache folders = %q, want %q (moved from the source)", got, want)
	}
}

// TestNoCacheFlag: -no-cache neither writes nor reads the cache file, for the main command
// and the organize commands; without it a cache file is written (also without ffprobe).
func TestNoCacheFlag(t *testing.T) {
	t.Setenv("PATH", t.TempDir()) // no ffmpeg: placeholder media are enough
	newLib := func(t *testing.T) string {
		lib := t.TempDir()
		testutil.WriteFiles(t, lib, map[string]string{
			"A/ABBA - SOS/song.txt": "#ARTIST:ABBA\n#TITLE:SOS\n#MP3:song.mp3\n" + testutil.NotesTxt("sos"),
			"A/ABBA - SOS/song.mp3": "x",
		})
		return lib
	}
	for _, args := range [][]string{
		{"-k", "-s"},
		{"-k", "-n", "-s"},
		{"dedupe", "-n", "-l"},
		{"dedupe", "-l"},
		{"add", "-s", "{src}", "-l"},
	} {
		t.Run(strings.Join(args, " "), func(t *testing.T) {
			for _, noCache := range []bool{false, true} {
				lib := newLib(t)
				a := slices.Clone(args)
				if i := slices.Index(a, "{src}"); i >= 0 {
					src := t.TempDir()
					testutil.WriteFiles(t, src, map[string]string{
						"Queen - Bohemian Rhapsody/song.txt": "#ARTIST:Queen\n#TITLE:Bohemian Rhapsody\n#MP3:song.mp3\n" + testutil.NotesTxt("bo"),
						"Queen - Bohemian Rhapsody/song.mp3": "x",
					})
					a[i] = src
				}
				a = append(a, lib, "-o", t.TempDir())
				if noCache {
					a = append(a[:1:1], append([]string{"-no-cache"}, a[1:]...)...)
				}
				if got := runWithArgs(t, a...); got != exitOK {
					t.Fatalf("%q = %d", a, got)
				}
				_, err := os.Stat(filepath.Join(lib, mediacache.FileName))
				if noCache && !os.IsNotExist(err) {
					t.Errorf("%q wrote a cache file (%v)", a, err)
				}
				if !noCache && err != nil {
					t.Errorf("%q wrote no cache file: %v", a, err)
				}
			}
		})
	}
	// A run without a media module needs no cache.
	lib := newLib(t)
	if got := runWithArgs(t, "-s", lib, "-o", t.TempDir()); got != exitOK {
		t.Fatalf("run = %d", got)
	}
	if _, err := os.Stat(filepath.Join(lib, mediacache.FileName)); !os.IsNotExist(err) {
		t.Errorf("a run without -k/-m/-a wrote a cache file (%v)", err)
	}
	// -no-cache cannot be combined with -undo.
	if got := runWithArgs(t, "dedupe", "-undo", "-no-cache", "-l", lib); got != exitUsage {
		t.Errorf("-undo -no-cache = %d, want %d", got, exitUsage)
	}
}

// TestUndoRemovesCreatedLibraryWithCache: organize into a library that did not exist saves
// the media cache into the new library root; its undo removes the root anyway (the folder
// was created by the run), cache file included. A dry-run undo removes nothing.
func TestUndoRemovesCreatedLibraryWithCache(t *testing.T) {
	t.Setenv("PATH", t.TempDir()) // no ffmpeg: placeholder media are enough
	base := t.TempDir()
	src, lib, out := filepath.Join(base, "src"), filepath.Join(base, "lib"), filepath.Join(base, "out")
	testutil.WriteFiles(t, src, map[string]string{
		"song/song.txt": "#ARTIST:ABBA\n#TITLE:SOS\n#MP3:song.mp3\n" + testutil.NotesTxt("sos"),
		"song/song.mp3": "x",
	})
	if got := runWithArgs(t, "organize", "-s", src, "-l", lib, "-o", out); got != exitOK {
		t.Fatalf("organize = %d", got)
	}
	if _, err := os.Stat(filepath.Join(lib, mediacache.FileName)); err != nil {
		t.Fatalf("no cache file in the new library: %v", err)
	}
	if got := runWithArgs(t, "organize", "-undo", "-n", "-o", out); got != exitOK {
		t.Fatalf("dry-run undo = %d", got)
	}
	if _, err := os.Stat(filepath.Join(lib, mediacache.FileName)); err != nil {
		t.Fatalf("dry-run undo removed the cache file: %v", err)
	}
	if got := runWithArgs(t, "organize", "-undo", "-o", out); got != exitOK {
		t.Fatalf("undo = %d", got)
	}
	if _, err := os.Stat(filepath.Join(src, "song", "song.txt")); err != nil {
		t.Errorf("song not moved back: %v", err)
	}
	if _, err := os.Stat(lib); !os.IsNotExist(err) {
		entries, _ := os.ReadDir(lib)
		t.Errorf("library root created by the run is still there (%v): %v", err, entries)
	}
}
