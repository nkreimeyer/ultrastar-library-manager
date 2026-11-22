package mediacache

import (
	"bytes"
	"compress/gzip"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/nkreimeyer/library-manager/fsops"
	"github.com/nkreimeyer/library-manager/internal/testutil"
	log "github.com/sirupsen/logrus"
	logtest "github.com/sirupsen/logrus/hooks/test"
)

func TestMain(m *testing.M) {
	log.SetOutput(io.Discard)
	os.Exit(m.Run())
}

var testTools = map[string]string{"ffprobe": "7.1", "ffmpeg": "7.1"}

// clock is a settable time source for the racy rule.
type clock struct {
	mu sync.Mutex
	t  time.Time
}

func (c *clock) now() time.Time { c.mu.Lock(); defer c.mu.Unlock(); return c.t }

func newClock() *clock { return &clock{t: time.Now()} }

// openTest opens the cache of root with the test tool versions.
func openTest(t *testing.T, root string, clk *clock) *Cache {
	t.Helper()
	c := open(root, testTools, clk.now)
	if c == nil {
		t.Fatal("open returned nil")
	}
	return c
}

// writeOld writes path with content and an mtime one hour before clk's time.
func writeOld(t *testing.T, clk *clock, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	setMtime(t, path, clk.now().Add(-time.Hour))
}

func setMtime(t *testing.T, path string, mt time.Time) {
	t.Helper()
	if err := os.Chtimes(path, mt, mt); err != nil {
		t.Fatal(err)
	}
}

type result struct {
	N int    `json:"n"`
	S string `json:"s"`
}

// storeResult looks path up (expecting a miss) and stores r.
func storeResult(t *testing.T, c *Cache, path string, r result) {
	t.Helper()
	var got result
	st, ok := c.Lookup(path, "test/1", &got)
	if ok {
		t.Fatalf("%s: unexpected hit %+v", path, got)
	}
	c.Store(st, "test/1", r)
}

func lookup(c *Cache, path string) (result, bool) {
	var got result
	_, ok := c.Lookup(path, "test/1", &got)
	return got, ok
}

func save(t *testing.T, c *Cache) {
	t.Helper()
	if err := c.Save(fsops.New(false)); err != nil {
		t.Fatal(err)
	}
}

func TestHitInRunAndAfterReopen(t *testing.T) {
	root, clk := t.TempDir(), newClock()
	f := filepath.Join(root, "A", "Artist - Song", "song.mp3")
	writeOld(t, clk, f, "audio")
	c := openTest(t, root, clk)
	storeResult(t, c, f, result{N: 7, S: "x"})
	if got, ok := lookup(c, f); !ok || got != (result{7, "x"}) {
		t.Fatalf("lookup = %+v, %v", got, ok)
	}
	var other result
	if _, ok := c.Lookup(f, "other/1", &other); ok {
		t.Error("hit for a kind never stored")
	}
	if st := c.Stats(); st != (Stats{Hits: 1, Misses: 2, Stored: 1}) {
		t.Errorf("stats %+v", st)
	}
	save(t, c)
	c2 := openTest(t, root, clk)
	if got, ok := lookup(c2, f); !ok || got != (result{7, "x"}) {
		t.Fatalf("after reopen: lookup = %+v, %v", got, ok)
	}
	// A relative path names the same file.
	wd, _ := os.Getwd()
	if rel, err := filepath.Rel(wd, f); err == nil {
		if _, ok := lookup(c2, rel); !ok {
			t.Error("relative path missed")
		}
	}
}

func TestFileLayout(t *testing.T) {
	root, clk := t.TempDir(), newClock()
	f := filepath.Join(root, "A", "Song", "song.mp3")
	writeOld(t, clk, f, "audio")
	c := openTest(t, root, clk)
	storeResult(t, c, f, result{N: 1})
	save(t, c)
	raw, err := os.ReadFile(filepath.Join(root, FileName))
	if err != nil {
		t.Fatal(err)
	}
	zr, err := gzip.NewReader(bytes.NewReader(raw))
	if err != nil {
		t.Fatal(err)
	}
	var d map[string]any
	// Numbers stay json.Number: a float64 cannot hold a nanosecond mtime exactly.
	dec := json.NewDecoder(zr)
	dec.UseNumber()
	if err := dec.Decode(&d); err != nil {
		t.Fatal(err)
	}
	if d["format"] != json.Number("1") {
		t.Errorf("format %v", d["format"])
	}
	if _, err := time.Parse(time.RFC3339, d["updated"].(string)); err != nil {
		t.Errorf("updated: %v", err)
	}
	fo := d["folders"].(map[string]any)["A/Song"].(map[string]any)
	entry := fo["files"].(map[string]any)["song.mp3"].(map[string]any)
	if entry["size"] != json.Number("5") {
		t.Errorf("size %v", entry["size"])
	}
	st, _ := os.Stat(f)
	if entry["mtimeNs"] != json.Number(strconv.FormatInt(st.ModTime().UnixNano(), 10)) {
		t.Errorf("mtimeNs %v", entry["mtimeNs"])
	}
	if got := fmt.Sprint(entry["results"]); got != "map[test/1:map[n:1 s:]]" {
		t.Errorf("results %s", got)
	}
	// Saving again with the same clock gives the same bytes.
	save(t, c)
	raw2, _ := os.ReadFile(filepath.Join(root, FileName))
	if !bytes.Equal(raw, raw2) {
		t.Error("saving twice gave different files")
	}
}

func TestMissOnChange(t *testing.T) {
	root, clk := t.TempDir(), newClock()
	f := filepath.Join(root, "S", "song.mp3")
	writeOld(t, clk, f, "audio")
	st0, _ := os.Stat(f)
	c := openTest(t, root, clk)
	storeResult(t, c, f, result{N: 1})

	// Same size, other mtime.
	setMtime(t, f, st0.ModTime().Add(time.Nanosecond))
	if _, ok := lookup(c, f); ok {
		t.Error("hit after an mtime change")
	}
	// Other size, same mtime.
	writeOld(t, clk, f, "audio!")
	setMtime(t, f, st0.ModTime())
	if _, ok := lookup(c, f); ok {
		t.Error("hit after a size change")
	}
	// A new result replaces the old entry.
	storeResult(t, c, f, result{N: 2})
	if got, ok := lookup(c, f); !ok || got.N != 2 {
		t.Errorf("lookup = %+v, %v", got, ok)
	}
	// A missing file and a directory are misses with an invalid stamp.
	for _, p := range []string{filepath.Join(root, "nope.mp3"), filepath.Join(root, "S")} {
		var r result
		st, ok := c.Lookup(p, "test/1", &r)
		if ok || st.Valid() {
			t.Errorf("%s: ok=%v valid=%v", p, ok, st.Valid())
		}
		c.Store(st, "test/1", result{N: 9})
	}
	if c.Stats().Stored != 2 {
		t.Errorf("stored %d, want 2", c.Stats().Stored)
	}
}

func TestMovedFolderKeepsEntries(t *testing.T) {
	root, clk := t.TempDir(), newClock()
	oldDir := filepath.Join(root, "in", "Artist - Song")
	f := filepath.Join(oldDir, "song.mp3")
	writeOld(t, clk, f, "audio")
	c := openTest(t, root, clk)
	storeResult(t, c, f, result{N: 3})
	save(t, c)

	newDir := filepath.Join(root, "A", "Artist - Song")
	if err := os.MkdirAll(filepath.Dir(newDir), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(oldDir, newDir); err != nil { // keeps mtimes
		t.Fatal(err)
	}
	c2 := openTest(t, root, clk)
	nf := filepath.Join(newDir, "song.mp3")
	if got, ok := lookup(c2, nf); !ok || got.N != 3 {
		t.Fatalf("moved file: %+v, %v", got, ok)
	}
	c2.mu.Lock()
	_, oldKept := c2.folders["in/Artist - Song"]
	c2.mu.Unlock()
	if oldKept {
		t.Error("the old entry of a moved file was kept")
	}
	// A different name with the same size and mtime is no match.
	other := filepath.Join(root, "B", "other.mp3")
	writeOld(t, clk, other, "audio")
	st, _ := os.Stat(nf)
	setMtime(t, other, st.ModTime())
	if _, ok := lookup(c2, other); ok {
		t.Error("a file of another name matched")
	}
	// A copy with the same name, size and mtime: both keep the entry.
	cp := filepath.Join(root, "C", "song.mp3")
	writeOld(t, clk, cp, "audio")
	setMtime(t, cp, st.ModTime())
	if got, ok := lookup(c2, cp); !ok || got.N != 3 {
		t.Fatalf("copy: %+v, %v", got, ok)
	}
	hitsBefore := c2.Stats().Hits
	if _, ok := lookup(c2, nf); !ok {
		t.Error("the original lost its entry to the copy")
	}
	c2.mu.Lock()
	inPlace := c2.entry("A/Artist - Song", "song.mp3") != nil && c2.entry("C", "song.mp3") != nil
	c2.mu.Unlock()
	if !inPlace || c2.Stats().Hits != hitsBefore+1 {
		t.Error("copy and original are not both cached")
	}
}

// A stale entry at the path is replaced by a matching one found by name, size and mtime
// (a file moved over another one).
func TestSecondaryReplacesStaleEntry(t *testing.T) {
	root, clk := t.TempDir(), newClock()
	a := filepath.Join(root, "a", "song.mp3")
	b := filepath.Join(root, "b", "song.mp3")
	writeOld(t, clk, a, "first")
	writeOld(t, clk, b, "second!")
	setMtime(t, b, clk.now().Add(-2*time.Hour))
	c := openTest(t, root, clk)
	storeResult(t, c, a, result{N: 1})
	storeResult(t, c, b, result{N: 2})
	if err := os.Rename(b, a); err != nil {
		t.Fatal(err)
	}
	if got, ok := lookup(c, a); !ok || got.N != 2 {
		t.Fatalf("lookup = %+v, %v", got, ok)
	}
}

func TestRacyRule(t *testing.T) {
	root, clk := t.TempDir(), newClock()
	f := filepath.Join(root, "s", "song.mp3")
	writeOld(t, clk, f, "audio")
	base := clk.now()
	cases := []struct {
		age    time.Duration
		stored bool
	}{
		{-time.Hour, false}, // in the future
		{0, false},
		{RacyWindow - time.Millisecond, false},
		{RacyWindow, true},
		{time.Hour, true},
	}
	for _, tc := range cases {
		setMtime(t, f, base.Add(-tc.age))
		c := openTest(t, root, clk)
		var r result
		st, _ := c.Lookup(f, "test/1", &r)
		c.Store(st, "test/1", result{N: 1})
		_, ok := lookup(c, f)
		if ok != tc.stored || (c.Stats().Stored == 1) != tc.stored {
			t.Errorf("age %v: stored=%v, want %v", tc.age, ok, tc.stored)
		}
	}
}

func TestStoreRefusedWhenFileChangedDuringScan(t *testing.T) {
	root, clk := t.TempDir(), newClock()
	f := filepath.Join(root, "s", "song.mp3")
	writeOld(t, clk, f, "audio")
	st0, _ := os.Stat(f)
	c := openTest(t, root, clk)
	var r result
	stamp, _ := c.Lookup(f, "test/1", &r)
	// Changed while it was "probed": other size, same old mtime.
	writeOld(t, clk, f, "audio, changed")
	setMtime(t, f, st0.ModTime())
	c.Store(stamp, "test/1", result{N: 1})
	if c.Stats().Stored != 0 {
		t.Error("a result for a changed file was stored")
	}
	// Removed during the scan.
	stamp, _ = c.Lookup(f, "test/1", &r)
	if err := os.Remove(f); err != nil {
		t.Fatal(err)
	}
	c.Store(stamp, "test/1", result{N: 1})
	if c.Stats().Stored != 0 {
		t.Error("a result for a removed file was stored")
	}
}

func TestToolChangeDropsEntries(t *testing.T) {
	root, clk := t.TempDir(), newClock()
	f := filepath.Join(root, "s", "song.mp3")
	writeOld(t, clk, f, "audio")
	c := openTest(t, root, clk)
	storeResult(t, c, f, result{N: 1})
	save(t, c)
	for _, tools := range []map[string]string{
		{"ffprobe": "7.2", "ffmpeg": "7.1"},
		{"ffprobe": "7.1", "ffmpeg": ""},
		{"ffprobe": "7.1"},
	} {
		c2 := open(root, tools, clk.now)
		if _, ok := lookup(c2, f); ok {
			t.Errorf("tools %v: entry kept", tools)
		}
	}
	if _, ok := lookup(openTest(t, root, clk), f); !ok {
		t.Error("same tools: entry lost")
	}
}

func TestUnusableFileStartsEmpty(t *testing.T) {
	gz := func(s string) []byte {
		var b bytes.Buffer
		zw := gzip.NewWriter(&b)
		zw.Write([]byte(s))
		zw.Close()
		return b.Bytes()
	}
	cases := map[string][]byte{
		"not gzip":       []byte("garbage"),
		"truncated gzip": gz(`{"format":1,"folders":{}}`)[:12],
		"bad json":       gz(`{"format":1,`),
		"other format":   gz(`{"format":2,"tools":{"ffprobe":"7.1","ffmpeg":"7.1"},"folders":{"s":{"files":{"song.mp3":{"size":5,"mtimeNs":1,"results":{"test/1":{"n":1}}}}}}}`),
		"no format":      gz(`{"folders":{}}`),
	}
	for name, data := range cases {
		t.Run(name, func(t *testing.T) {
			root, clk := t.TempDir(), newClock()
			path := filepath.Join(root, FileName)
			if err := os.WriteFile(path, data, 0o644); err != nil {
				t.Fatal(err)
			}
			hook := logtest.NewGlobal()
			defer log.StandardLogger().ReplaceHooks(make(log.LevelHooks))
			c := openTest(t, root, clk)
			warned := false
			for _, e := range hook.AllEntries() {
				if e.Level == log.WarnLevel && strings.Contains(e.Message, path) {
					warned = true
				}
			}
			if !warned {
				t.Error("no warning naming the file")
			}
			c.mu.Lock()
			n := len(c.folders)
			c.mu.Unlock()
			if n != 0 {
				t.Errorf("%d folders loaded", n)
			}
		})
	}
}

// A file that becomes readable again after Save: the second open does not warn.
func TestSaveReplacesCorruptFile(t *testing.T) {
	root, clk := t.TempDir(), newClock()
	if err := os.WriteFile(filepath.Join(root, FileName), []byte("garbage"), 0o644); err != nil {
		t.Fatal(err)
	}
	save(t, openTest(t, root, clk))
	hook := logtest.NewGlobal()
	defer log.StandardLogger().ReplaceHooks(make(log.LevelHooks))
	openTest(t, root, clk)
	for _, e := range hook.AllEntries() {
		if e.Level <= log.WarnLevel {
			t.Errorf("warning after save: %s", e.Message)
		}
	}
}

func TestNilCache(t *testing.T) {
	var c *Cache
	var r result
	st, ok := c.Lookup("/nonexistent", "test/1", &r)
	if ok || st.Valid() {
		t.Error("nil cache hit")
	}
	c.Store(st, "test/1", r)
	if err := c.Save(fsops.New(false)); err != nil {
		t.Error(err)
	}
	if c.Stats() != (Stats{}) || c.Path() != "" {
		t.Error("nil cache stats/path")
	}
	if Open(context.Background(), "") != nil {
		t.Error("Open(\"\") is not nil")
	}
}

func TestRootSongFolderDisablesCache(t *testing.T) {
	root, clk := t.TempDir(), newClock()
	if err := os.WriteFile(filepath.Join(root, "song.txt"), []byte("#TITLE:x\n#ARTIST:y\n: 0 1 0 la\nE\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if c := open(root, testTools, clk.now); c != nil {
		t.Fatal("cache opened in a song folder")
	}
	// A txt that is no song (License.txt) does not.
	root2 := t.TempDir()
	if err := os.WriteFile(filepath.Join(root2, "License.txt"), []byte("Some license\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if c := open(root2, testTools, clk.now); c == nil {
		t.Fatal("cache disabled by a non-song txt")
	}
}

func TestSavePrunes(t *testing.T) {
	root, clk := t.TempDir(), newClock()
	keep := filepath.Join(root, "keep", "song.mp3")
	gone := filepath.Join(root, "keep", "video.mp4")
	goneDir := filepath.Join(root, "gone", "song.mp3")
	seenGone := filepath.Join(root, "moved", "song.ogg")
	for i, f := range []string{keep, gone, goneDir, seenGone} {
		writeOld(t, clk, f, strings.Repeat("x", i+1)) // no two alike
	}
	c := openTest(t, root, clk)
	for _, f := range []string{keep, gone, goneDir, seenGone} {
		storeResult(t, c, f, result{N: 1})
	}
	save(t, c)

	c2 := openTest(t, root, clk)
	if _, ok := lookup(c2, seenGone); !ok { // seen in this run, then removed
		t.Fatal("miss")
	}
	for _, p := range []string{gone, goneDir, seenGone} {
		if err := os.Remove(p); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.Remove(filepath.Dir(goneDir)); err != nil {
		t.Fatal(err)
	}
	save(t, c2)
	c2.mu.Lock()
	if n := len(c2.index); n != 2 {
		t.Errorf("index of %d keys after pruning, want 2", n)
	}
	c2.mu.Unlock()

	c3 := openTest(t, root, clk)
	c3.mu.Lock()
	var got []string
	for key, fo := range c3.folders {
		for name := range fo.Files {
			got = append(got, key+"/"+name)
		}
	}
	nIndex := len(c3.index)
	c3.mu.Unlock()
	want := map[string]bool{"keep/song.mp3": true, "moved/song.ogg": true}
	if len(got) != len(want) || nIndex != len(want) {
		t.Fatalf("entries %v (index %d), want %v", got, nIndex, want)
	}
	for _, g := range got {
		if !want[g] {
			t.Errorf("unexpected entry %s", g)
		}
	}
}

// A dry run saves the cache too; a library root that does not exist is not created.
func TestSaveDryRunAndMissingRoot(t *testing.T) {
	root, clk := t.TempDir(), newClock()
	c := openTest(t, root, clk)
	if err := c.Save(fsops.New(true)); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(root, FileName)); err != nil {
		t.Fatalf("dry run did not save: %v", err)
	}
	missing := filepath.Join(t.TempDir(), "library")
	c = openTest(t, missing, clk)
	if err := c.Save(fsops.New(false)); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(missing); !os.IsNotExist(err) {
		t.Errorf("library root created: %v", err)
	}
}

// Files outside the root are keyed by their absolute folder.
func TestFileOutsideRoot(t *testing.T) {
	root, clk := t.TempDir(), newClock()
	outside := filepath.Join(t.TempDir(), "x", "song.mp3")
	writeOld(t, clk, outside, "a")
	c := openTest(t, root, clk)
	storeResult(t, c, outside, result{N: 5})
	save(t, c)
	c2 := openTest(t, root, clk)
	if got, ok := lookup(c2, outside); !ok || got.N != 5 {
		t.Fatalf("lookup = %+v, %v", got, ok)
	}
	c2.mu.Lock()
	_, abs := c2.folders[filepath.ToSlash(filepath.Dir(outside))]
	c2.mu.Unlock()
	if !abs {
		t.Error("not keyed by the absolute folder")
	}
}

func TestConcurrentUse(t *testing.T) {
	root, clk := t.TempDir(), newClock()
	var files []string
	for i := range 40 {
		f := filepath.Join(root, fmt.Sprintf("d%d", i%7), fmt.Sprintf("f%d.mp3", i))
		writeOld(t, clk, f, fmt.Sprint(i))
		files = append(files, f)
	}
	c := openTest(t, root, clk)
	var wg sync.WaitGroup
	for w := range 8 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for round := range 3 {
				for i, f := range files {
					var r result
					st, ok := c.Lookup(f, "test/1", &r)
					if ok && r.N != i {
						t.Errorf("%s: got %d", f, r.N)
					}
					if !ok {
						c.Store(st, "test/1", result{N: i})
					}
				}
				if w == 0 && round == 1 {
					if err := c.Save(fsops.New(false)); err != nil {
						t.Error(err)
					}
				}
			}
		}()
	}
	wg.Wait()
	save(t, c)
	c2 := openTest(t, root, clk)
	for i, f := range files {
		if got, ok := lookup(c2, f); !ok || got.N != i {
			t.Errorf("%s: %+v, %v", f, got, ok)
		}
	}
}

// A stored result that does not decode into v is a miss.
func TestUndecodableResultIsMiss(t *testing.T) {
	root, clk := t.TempDir(), newClock()
	f := filepath.Join(root, "s", "a.mp3")
	writeOld(t, clk, f, "a")
	c := openTest(t, root, clk)
	var r result
	st, _ := c.Lookup(f, "test/1", &r)
	c.Store(st, "test/1", "a string")
	if _, ok := lookup(c, f); ok {
		t.Error("undecodable result was a hit")
	}
}

// A run with other tool versions (ffprobe missing from PATH) that stores nothing leaves the
// file alone, so the next run with the usual tools still has its results; one that stores
// a result replaces it.
func TestToolChangeKeepsFileUntilStored(t *testing.T) {
	root, clk := t.TempDir(), newClock()
	f := filepath.Join(root, "s", "song.mp3")
	writeOld(t, clk, f, "audio")
	c := openTest(t, root, clk)
	storeResult(t, c, f, result{N: 1})
	save(t, c)

	noTools := map[string]string{"ffprobe": "", "ffmpeg": ""}
	c2 := open(root, noTools, clk.now)
	if _, ok := lookup(c2, f); ok {
		t.Fatal("entry of other tool versions used")
	}
	save(t, c2)
	if got, ok := lookup(openTest(t, root, clk), f); !ok || got.N != 1 {
		t.Fatalf("a run that stored nothing replaced the file: %+v, %v", got, ok)
	}

	c3 := open(root, noTools, clk.now)
	storeResult(t, c3, f, result{N: 2})
	save(t, c3)
	if _, ok := lookup(openTest(t, root, clk), f); ok {
		t.Error("the old tools' file was not replaced by a run that stored a result")
	}
	if got, ok := lookup(open(root, noTools, clk.now), f); !ok || got.N != 2 {
		t.Errorf("new result = %+v, %v", got, ok)
	}
}

// A tool that is found but whose "-version" run fails (a timeout on a busy machine) must not
// look like a tool change: Open disables the cache for the run, so the file of the usual
// versions is neither dropped nor replaced. A missing tool keeps today's behaviour (a cache
// of version "").
func TestOpenVersionUnreadableDisablesCache(t *testing.T) {
	root, clk := t.TempDir(), newClock()
	f := filepath.Join(root, "s", "song.mp3")
	writeOld(t, clk, f, "audio")
	testutil.FakeVersions(t, "7.1", "7.1")
	c := Open(context.Background(), root)
	if c == nil {
		t.Fatal("Open with working tools returned nil")
	}
	storeResult(t, c, f, result{N: 1})
	save(t, c)
	before := testutil.ReadFile(t, c.Path())

	okScript := "#!/bin/sh\nif [ \"$1\" = -version ]; then echo 'ffmpeg version 7.1 Copyright'; exit 0; fi\nexit 1\n"
	testutil.FakeTools(t, map[string]string{
		"ffprobe": "#!/bin/sh\nexit 1\n",
		"ffmpeg":  okScript,
	})
	if c2 := Open(context.Background(), root); c2 != nil {
		t.Fatal("Open returned a cache although ffprobe -version failed")
	}
	if got := testutil.ReadFile(t, c.Path()); got != before {
		t.Error("the cache file changed")
	}

	testutil.FakeTools(t, map[string]string{"ffmpeg": okScript})
	if c3 := Open(context.Background(), root); c3 == nil {
		t.Error("Open with ffprobe missing returned nil")
	}
}

func TestOpenStoppedCtx(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if c := Open(ctx, t.TempDir()); c != nil {
		t.Error("Open with a done ctx returned a cache")
	}
	if c := Open(context.Background(), t.TempDir()); c == nil {
		t.Error("Open returned nil")
	}
}

// caseAlias makes root/abba/x.mp3 and root/ABBA/x.mp3 the same file (a hard link), as both
// spellings of one folder are on a case-insensitive file system (NTFS, APFS).
func caseAlias(t *testing.T, clk *clock, root string) (lower, upper string) {
	t.Helper()
	lower, upper = filepath.Join(root, "abba", "x.mp3"), filepath.Join(root, "ABBA", "x.mp3")
	writeOld(t, clk, lower, "audio")
	if err := os.Mkdir(filepath.Dir(upper), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Link(lower, upper); err != nil {
		t.Skipf("no hard links: %v", err)
	}
	return lower, upper
}

func cachedFolders(c *Cache) []string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return slices.Sorted(maps.Keys(c.folders))
}

// A folder renamed only in case is a move: the entry under the old spelling is dropped,
// though that spelling still resolves to the same file.
func TestCaseOnlyRenameMovesEntry(t *testing.T) {
	root, clk := t.TempDir(), newClock()
	lower, upper := caseAlias(t, clk, root)
	c := openTest(t, root, clk)
	storeResult(t, c, lower, result{N: 1})
	if got, ok := lookup(c, upper); !ok || got.N != 1 {
		t.Fatalf("lookup under the new spelling = %+v, %v", got, ok)
	}
	if got := cachedFolders(c); !slices.Equal(got, []string{"ABBA"}) {
		t.Errorf("folders %q, want only ABBA", got)
	}
	// A real copy (another file with the same name, size and mtime) keeps its entry.
	other := filepath.Join(root, "Other", "x.mp3")
	writeOld(t, clk, other, "audio")
	if _, ok := lookup(c, other); !ok {
		t.Fatal("copy: miss")
	}
	if got := cachedFolders(c); !slices.Equal(got, []string{"ABBA", "Other"}) {
		t.Errorf("folders %q after a copy, want ABBA and Other", got)
	}
}

// An entry left under the old spelling by an earlier version is pruned once the file was
// seen under the new one.
func TestPruneDropsCaseAlias(t *testing.T) {
	root, clk := t.TempDir(), newClock()
	lower, upper := caseAlias(t, clk, root)
	c := openTest(t, root, clk)
	for _, p := range []string{lower, upper} {
		c.Store(stampOf(p), "test/1", result{N: 1})
	}
	save(t, c)
	c2 := openTest(t, root, clk)
	if _, ok := lookup(c2, upper); !ok {
		t.Fatal("miss")
	}
	save(t, c2)
	if got := cachedFolders(c2); !slices.Equal(got, []string{"ABBA"}) {
		t.Errorf("folders %q, want the stale abba entry pruned", got)
	}
	// Without a seen alias both stay (two real folders on a case-sensitive file system).
	c3 := openTest(t, root, clk)
	save(t, c3)
	if got := cachedFolders(c3); !slices.Equal(got, []string{"ABBA"}) {
		t.Errorf("folders %q", got)
	}
}

// prune lists the folders in parallel and without holding the cache's lock: after a run
// that looked at only some files, nearly every folder has an unseen entry.
func TestPruneListsFoldersInParallel(t *testing.T) {
	root, clk := t.TempDir(), newClock()
	for i := range 8 {
		writeOld(t, clk, filepath.Join(root, fmt.Sprintf("s%d", i), "video.mp4"), strings.Repeat("v", i+1))
	}
	c := openTest(t, root, clk)
	for i := range 8 {
		storeResult(t, c, filepath.Join(root, fmt.Sprintf("s%d", i), "video.mp4"), result{N: i})
	}
	save(t, c)
	c2 := openTest(t, root, clk) // sees nothing: every folder is listed
	var mu sync.Mutex
	inFlight, most, calls := 0, 0, 0
	c2.listDir = func(dir string) ([]os.DirEntry, error) {
		_ = c2.Stats() // would deadlock if prune held c.mu
		mu.Lock()
		inFlight++
		calls++
		most = max(most, inFlight)
		mu.Unlock()
		deadline := time.Now().Add(2 * time.Second)
		for time.Now().Before(deadline) {
			mu.Lock()
			n := most
			mu.Unlock()
			if n >= 2 {
				break
			}
			time.Sleep(time.Millisecond)
		}
		mu.Lock()
		inFlight--
		mu.Unlock()
		return os.ReadDir(dir)
	}
	done := make(chan struct{})
	var saveErr error
	go func() { saveErr = c2.Save(fsops.New(false)); close(done) }()
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("Save hangs: prune lists folders while holding the lock")
	}
	if saveErr != nil {
		t.Fatal(saveErr)
	}
	if calls != 8 || most < 2 {
		t.Errorf("%d listings, at most %d at a time; want 8, in parallel", calls, most)
	}
	if got := len(cachedFolders(c2)); got != 8 {
		t.Errorf("%d folders kept, want 8 (all files exist)", got)
	}
}
