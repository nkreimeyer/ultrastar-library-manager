package mediacache

import (
	"bytes"
	"compress/gzip"
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/nkreimeyer/library-manager/internal/testutil"
)

func gzipBytes(t *testing.T, s string) []byte {
	t.Helper()
	var b bytes.Buffer
	zw := gzip.NewWriter(&b)
	if _, err := zw.Write([]byte(s)); err != nil {
		t.Fatal(err)
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	return b.Bytes()
}

// null folders, null files and null results in the file (hand-edited or damaged) must not
// panic: they are dropped or normalized.
func TestLoadNullEntries(t *testing.T) {
	root, clk := t.TempDir(), newClock()
	doc := `{"format":1,"tools":{"ffmpeg":"7.1","ffprobe":"7.1"},"folders":{
		"nullfolder":null,
		"empty":{"files":{}},
		"b":{"files":{"nullfile.mp3":null,"y.mp3":{"size":5,"mtimeNs":1,"results":null}}},
		"onlynull":{"files":{"z.mp3":null}}}}`
	if err := os.WriteFile(filepath.Join(root, FileName), gzipBytes(t, doc), 0o644); err != nil {
		t.Fatal(err)
	}
	c := openTest(t, root, clk) // must not panic
	c.mu.Lock()
	defer c.mu.Unlock()
	if fo := c.folders["b"]; fo == nil || len(fo.Files) != 1 || fo.Files["y.mp3"] == nil || fo.Files["y.mp3"].Results == nil {
		t.Errorf("folder b = %+v, want only y.mp3 with non-nil results", fo)
	}
	if c.folders["nullfolder"] != nil || c.folders["empty"] != nil {
		t.Errorf("null or empty folders kept: %v", c.folders)
	}
	if n := len(c.index); n != 1 {
		t.Errorf("index has %d keys, want 1", n)
	}
}

// A done ctx disables the cache, and the existing file is neither read into a cache nor
// touched.
func TestOpenDoneCtxLeavesFileAlone(t *testing.T) {
	root, clk := t.TempDir(), newClock()
	f := filepath.Join(root, "A", "song.mp3")
	writeOld(t, clk, f, "audio")
	c := openTest(t, root, clk)
	storeResult(t, c, f, result{N: 1})
	save(t, c)
	path := filepath.Join(root, FileName)
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	c2 := Open(ctx, root)
	if c2 != nil {
		t.Fatal("Open with a done ctx returned a cache")
	}
	if err := c2.Save(nil); err != nil { // a nil cache saves nothing
		t.Errorf("Save of a nil cache = %v", err)
	}
	after, err := os.ReadFile(path)
	if err != nil || !bytes.Equal(before, after) {
		t.Errorf("cache file changed (err %v)", err)
	}
}

// A value that cannot be marshaled is not stored (and not counted).
func TestStoreUnmarshalable(t *testing.T) {
	root, clk := t.TempDir(), newClock()
	f := filepath.Join(root, "A", "song.mp3")
	writeOld(t, clk, f, "audio")
	c := openTest(t, root, clk)
	var got result
	st, ok := c.Lookup(f, "test/1", &got)
	if ok {
		t.Fatal("unexpected hit")
	}
	c.Store(st, "test/1", make(chan int))
	c.Store(st, "test/1", func() {})
	if s := c.Stats(); s.Stored != 0 {
		t.Errorf("Stats = %+v, want Stored=0", s)
	}
	c.mu.Lock()
	n := len(c.folders)
	c.mu.Unlock()
	if n != 0 {
		t.Errorf("%d folders after a failed store", n)
	}
	if _, ok := lookup(c, f); ok {
		t.Error("hit for a value that was never stored")
	}
}

// The secondary lookup (base name, size, mtime) re-keys only an entry that matches: a
// file with the same name but another size or mtime is a miss and leaves the old entry
// alone; an index record without an entry behind it is no hit either.
func TestSecondaryLookupRequiresMatch(t *testing.T) {
	root, clk := t.TempDir(), newClock()
	old := filepath.Join(root, "A", "x.mp3")
	writeOld(t, clk, old, "audio")
	c := openTest(t, root, clk)
	storeResult(t, c, old, result{N: 1})

	diffSize := filepath.Join(root, "B", "x.mp3")
	writeOld(t, clk, diffSize, "audio, but longer")
	diffTime := filepath.Join(root, "C", "x.mp3")
	writeOld(t, clk, diffTime, "audio")
	setMtime(t, diffTime, clk.now().Add(-2*60*60e9))
	for _, p := range []string{diffSize, diffTime} {
		if _, ok := lookup(c, p); ok {
			t.Errorf("%s: hit for a file that is not the stored one", p)
		}
	}
	if got := cachedFolders(c); len(got) != 1 || got[0] != "A" {
		t.Errorf("folders %v, want only A", got)
	}
	if got, ok := lookup(c, old); !ok || got.N != 1 {
		t.Errorf("old entry lost: %+v, %v", got, ok)
	}

	// An index record that points to no entry (a stale index) is not a hit and creates
	// no entry for the new path.
	ghost := filepath.Join(root, "D", "g.mp3")
	writeOld(t, clk, ghost, "ghost")
	st := stampOf(ghost)
	c.mu.Lock()
	c.indexAdd("gone", "g.mp3", &fileEntry{Size: st.size, MtimeNs: st.mtimeNs})
	c.mu.Unlock()
	if _, ok := lookup(c, ghost); ok {
		t.Error("hit through an index record without an entry")
	}
	if got := cachedFolders(c); len(got) != 1 {
		t.Errorf("folders %v after the ghost lookup", got)
	}
}

// A folder that cannot be listed keeps its unseen entries, even if the file is gone.
func TestPruneKeepsEntriesOfUnlistableFolder(t *testing.T) {
	testutil.SkipIfRoot(t)
	root, clk := t.TempDir(), newClock()
	f := filepath.Join(root, "locked", "song.mp3")
	g := filepath.Join(root, "open", "song.mp3")
	writeOld(t, clk, f, "audio")
	writeOld(t, clk, g, "other audio")
	c := openTest(t, root, clk)
	storeResult(t, c, f, result{N: 1})
	storeResult(t, c, g, result{N: 2})
	save(t, c)

	if err := os.Remove(f); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(g); err != nil {
		t.Fatal(err)
	}
	dir := filepath.Dir(f)
	if err := os.Chmod(dir, 0o000); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.Chmod(dir, 0o755) })
	c2 := openTest(t, root, clk)
	save(t, c2)
	got := cachedFolders(openTest(t, root, clk))
	if len(got) != 1 || got[0] != "locked" {
		t.Errorf("folders after Save = %v, want only the unlistable one (the gone one is pruned)", got)
	}
}

func TestIsAliasAndSameFileAliasFalseCases(t *testing.T) {
	root, clk := t.TempDir(), newClock()
	lower, upper := filepath.Join(root, "abba", "x.mp3"), filepath.Join(root, "ABBA", "x.mp3")
	writeOld(t, clk, lower, "one")
	writeOld(t, clk, upper, "two") // another file, same name up to the folder's case
	missing := filepath.Join(root, "Abba", "x.mp3")
	c := openTest(t, root, clk)

	if c.isAlias(lower, "x.mp3", nil) {
		t.Error("isAlias without candidate folders")
	}
	if c.isAlias(missing, "x.mp3", []string{"ABBA"}) {
		t.Error("isAlias of a missing file")
	}
	if c.isAlias(lower, "x.mp3", []string{"ABBA"}) {
		t.Error("isAlias of a different file")
	}
	if c.isAlias(lower, "x.mp3", []string{"nowhere"}) {
		t.Error("isAlias with a candidate folder that does not exist")
	}
	if c.sameFileAlias(lower, upper) {
		t.Error("sameFileAlias of two different files")
	}
	if c.sameFileAlias(lower, filepath.Join(root, "other", "x.mp3")) {
		t.Error("sameFileAlias of paths that differ by more than case")
	}
	if c.sameFileAlias(missing, lower) || c.sameFileAlias(lower, missing) {
		t.Error("sameFileAlias with a missing file")
	}
	if !c.sameFileAlias(lower, lower) {
		t.Error("sameFileAlias of a path and itself")
	}
}
