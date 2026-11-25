package fsops

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/nkreimeyer/library-manager/internal/testutil"
)

// The journal path is a folder: opening fails, counted, nothing is written.
func TestAppendJournalPathIsDirectory(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "j")
	if err := os.Mkdir(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	ops := New(false)
	if err := ops.AppendJournal(dir, []byte("x")); err == nil || !strings.Contains(err.Error(), "journal") {
		t.Errorf("AppendJournal on a folder = %v", err)
	}
	if s := ops.Stats(); s.Failed != 1 {
		t.Errorf("Stats = %+v, want Failed=1", s)
	}
}

// An empty existing journal gets no leading newline; a dry run writes nothing at all.
func TestAppendJournalEmptyFileAndDryRun(t *testing.T) {
	p := filepath.Join(t.TempDir(), "j")
	writeFile(t, p, "", 0o644)
	if err := New(false).AppendJournal(p, []byte("a\n")); err != nil {
		t.Fatal(err)
	}
	if got := testutil.ReadFile(t, p); got != "a\n" {
		t.Errorf("journal = %q, want %q", got, "a\n")
	}
	missing := filepath.Join(t.TempDir(), "sub", "j")
	ops := New(true)
	if err := ops.AppendJournal(missing, []byte("a")); err != nil || testutil.Exists(filepath.Dir(missing)) {
		t.Errorf("dry run: err %v, folder created %v", err, testutil.Exists(filepath.Dir(missing)))
	}
}

func TestExists(t *testing.T) {
	root := t.TempDir()
	file := filepath.Join(root, "a.txt")
	writeFile(t, file, "x", 0o644)
	missing := filepath.Join(root, "missing")
	for _, dry := range []bool{false, true} {
		ops := New(dry)
		if !ops.Exists(file) || ops.Exists(missing) {
			t.Errorf("dry=%v: Exists(file) = %v, Exists(missing) = %v", dry, ops.Exists(file), ops.Exists(missing))
		}
	}
	// Dry run: a planned move away is gone, the planned target is there; a real run
	// would have the same view.
	ops := New(true)
	target, err := ops.MoveInto(file, filepath.Join(root, "out"), "a.txt")
	if err != nil {
		t.Fatal(err)
	}
	if ops.Exists(file) {
		t.Error("dry run: a file planned to be moved away still exists")
	}
	if !ops.Exists(target) {
		t.Error("dry run: the planned MoveInto target does not exist")
	}
	if !testutil.Exists(file) || testutil.Exists(target) {
		t.Error("dry run touched the disk")
	}
	// Exists cleans the path.
	if realOps := New(false); !realOps.Exists(root + "/./a.txt") {
		t.Error("Exists does not clean its path")
	}
}

func TestRemoveEmptyDirWithCacheFailures(t *testing.T) {
	testutil.SkipIfRoot(t)
	const name = ".cache.json.gz"
	t.Run("unreadable folder", func(t *testing.T) {
		dir := filepath.Join(t.TempDir(), "lib")
		writeFile(t, filepath.Join(dir, name), "x", 0o644)
		if err := os.Chmod(dir, 0o000); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { os.Chmod(dir, 0o755) })
		ops := New(false)
		if removed, err := ops.RemoveEmptyDirWithCache(dir, name); err == nil || removed {
			t.Errorf("removed %v, %v; want a failure", removed, err)
		}
		if s := ops.Stats(); s.Failed != 1 {
			t.Errorf("Stats = %+v, want Failed=1", s)
		}
	})
	t.Run("cache cannot be removed", func(t *testing.T) {
		dir := filepath.Join(t.TempDir(), "lib")
		writeFile(t, filepath.Join(dir, name), "x", 0o644)
		if err := os.Chmod(dir, 0o555); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { os.Chmod(dir, 0o755) })
		ops := New(false)
		if removed, err := ops.RemoveEmptyDirWithCache(dir, name); err == nil || removed ||
			!strings.Contains(err.Error(), "removing cache") {
			t.Errorf("removed %v, %v; want a failure", removed, err)
		}
		if s := ops.Stats(); s.Failed != 1 {
			t.Errorf("Stats = %+v, want Failed=1", s)
		}
		if !testutil.Exists(filepath.Join(dir, name)) {
			t.Error("cache file gone")
		}
	})
}

// In a dry run the cache and a stale temp file of its write are only planned away: the
// folder counts as removed, later checks see it gone, and the disk is untouched. Any
// other entry keeps the folder, in a real run and in a dry run.
func TestRemoveEmptyDirWithCacheDryRunTempAndOtherEntry(t *testing.T) {
	const name = ".cache.json.gz"
	files := []string{name, name + ".12345.tmp"}
	mk := func(t *testing.T, extra ...string) string {
		dir := filepath.Join(t.TempDir(), "lib")
		for _, f := range append(files[:2:2], extra...) {
			writeFile(t, filepath.Join(dir, f), "x", 0o644)
		}
		return dir
	}
	t.Run("temp file, dry run", func(t *testing.T) {
		dir := mk(t)
		ops := New(true)
		if removed, err := ops.RemoveEmptyDirWithCache(dir, name); err != nil || !removed {
			t.Fatalf("removed %v, %v", removed, err)
		}
		if ops.Exists(dir) {
			t.Error("folder still exists for the dry run")
		}
		for _, f := range files {
			if !testutil.Exists(filepath.Join(dir, f)) {
				t.Errorf("dry run removed %s", f)
			}
		}
		if s := ops.Stats(); s != (Stats{}) {
			t.Errorf("Stats = %+v", s)
		}
	})
	t.Run("temp file, real run", func(t *testing.T) {
		dir := mk(t)
		if removed, err := New(false).RemoveEmptyDirWithCache(dir, name); err != nil || !removed || testutil.Exists(dir) {
			t.Fatalf("removed %v, %v, exists %v", removed, err, testutil.Exists(dir))
		}
	})
	for _, extra := range []string{"song.txt", ".hidden", "other.tmp"} {
		for _, dry := range []bool{false, true} {
			dir := mk(t, extra)
			ops := New(dry)
			if removed, err := ops.RemoveEmptyDirWithCache(dir, name); err != nil || removed {
				t.Errorf("%s dry=%v: removed %v, %v", extra, dry, removed, err)
			}
			for _, f := range append(files[:2:2], extra) {
				if !testutil.Exists(filepath.Join(dir, f)) {
					t.Errorf("%s dry=%v: %s removed", extra, dry, f)
				}
			}
			if !ops.Exists(dir) {
				t.Errorf("%s dry=%v: folder planned away", extra, dry)
			}
		}
	}
}

// A merge whose source folder cannot be read is a counted failure and moves nothing.
func TestMergeIntoReadDirFailure(t *testing.T) {
	testutil.SkipIfRoot(t)
	root := t.TempDir()
	lib, inv := filepath.Join(root, "lib"), filepath.Join(root, "invalid")
	writeFile(t, filepath.Join(lib, "Cat", "S1", "song.txt"), "s1", 0o644)
	ops := New(false)
	if _, err := ops.MoveInto(filepath.Join(lib, "Cat", "S1"), inv, "Cat/S1"); err != nil {
		t.Fatal(err)
	}
	cat := filepath.Join(lib, "Cat")
	if err := os.Chmod(cat, 0o000); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.Chmod(cat, 0o755) })
	if _, err := ops.MoveInto(cat, inv, "Cat"); err == nil || !strings.Contains(err.Error(), "merging") {
		t.Errorf("MoveInto of an unreadable category = %v", err)
	}
	if s := ops.Stats(); s.Failed != 1 || s.Moved != 1 {
		t.Errorf("Stats = %+v, want Failed=1, Moved=1", s)
	}
	if len(ops.reserved) != 0 {
		t.Errorf("reservations left: %v", ops.reserved)
	}
}

// A failing copy of a single file (CopyInto, an unreadable source) leaves no target and
// counts as a failure; the source is intact.
func TestCopyIntoUnreadableSourceLeavesNoTarget(t *testing.T) {
	testutil.SkipIfRoot(t)
	root := t.TempDir()
	src := filepath.Join(root, "a.mp3")
	writeFile(t, src, "audio", 0o000)
	dstDir := filepath.Join(root, "originals")
	ops := New(false)
	if _, err := ops.CopyInto(src, dstDir, "a.mp3"); err == nil {
		t.Fatal("CopyInto of an unreadable file succeeded")
	}
	if testutil.Exists(filepath.Join(dstDir, "a.mp3")) {
		t.Error("partial copy left behind")
	}
	if s := ops.Stats(); s.Failed != 1 || s.Copied != 0 {
		t.Errorf("Stats = %+v", s)
	}
}
