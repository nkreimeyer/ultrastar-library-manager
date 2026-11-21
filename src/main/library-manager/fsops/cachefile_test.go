package fsops

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// WriteCacheFile writes for real in a dry run, counts nothing and leaves no temp file.
func TestWriteCacheFileDryRunWritesWithoutStats(t *testing.T) {
	for _, dry := range []bool{true, false} {
		dir := t.TempDir()
		path := filepath.Join(dir, ".cache.json.gz")
		ops := New(dry)
		if err := ops.WriteCacheFile(path, []byte("one")); err != nil {
			t.Fatal(err)
		}
		if err := ops.WriteCacheFile(path, []byte("two")); err != nil {
			t.Fatal(err)
		}
		got, err := os.ReadFile(path)
		if err != nil || string(got) != "two" {
			t.Fatalf("dry=%v: content %q, %v; want two", dry, got, err)
		}
		if st := ops.Stats(); st != (Stats{}) {
			t.Errorf("dry=%v: stats %+v, want none", dry, st)
		}
		entries, _ := os.ReadDir(dir)
		if len(entries) != 1 {
			var names []string
			for _, e := range entries {
				names = append(names, e.Name())
			}
			t.Errorf("dry=%v: dir holds %v, want only the cache file", dry, names)
		}
	}
}

// The mode of an existing cache file is kept; a new one gets 0644.
func TestWriteCacheFileMode(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, ".c")
	ops := New(false)
	if err := ops.WriteCacheFile(path, []byte("a")); err != nil {
		t.Fatal(err)
	}
	if st, _ := os.Stat(path); st.Mode().Perm() != 0o644 {
		t.Errorf("new mode %v, want 0644", st.Mode().Perm())
	}
	if err := os.Chmod(path, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := ops.WriteCacheFile(path, []byte("b")); err != nil {
		t.Fatal(err)
	}
	if st, _ := os.Stat(path); st.Mode().Perm() != 0o600 {
		t.Errorf("mode %v, want 0600 kept", st.Mode().Perm())
	}
}

// A failure (missing directory, never created) is returned but not counted, and an
// existing file stays intact when the rename fails.
func TestWriteCacheFileFailure(t *testing.T) {
	dir := t.TempDir()
	ops := New(true)
	missing := filepath.Join(dir, "nope", ".c")
	if err := ops.WriteCacheFile(missing, []byte("x")); err == nil {
		t.Fatal("no error for a missing directory")
	}
	if _, err := os.Stat(filepath.Dir(missing)); !os.IsNotExist(err) {
		t.Errorf("directory was created: %v", err)
	}
	// The target is a non-empty directory: the rename fails, the temp file is removed.
	target := filepath.Join(dir, "target")
	writeFile(t, filepath.Join(target, "keep"), "k", 0o644)
	if err := ops.WriteCacheFile(target, []byte("x")); err == nil {
		t.Fatal("no error for a directory target")
	}
	if st := ops.Stats(); st.Failed != 0 {
		t.Errorf("failed = %d, want 0", st.Failed)
	}
	entries, _ := os.ReadDir(dir)
	for _, e := range entries {
		if strings.HasSuffix(e.Name(), ".tmp") {
			t.Errorf("temp file %s left behind", e.Name())
		}
	}
	// A nil *Ops works too.
	var nilOps *Ops
	if err := nilOps.WriteCacheFile(filepath.Join(dir, ".n"), []byte("n")); err != nil {
		t.Fatal(err)
	}
}

// Temp files of interrupted cache writes older than StaleCacheTemp are removed by the next
// write (also in a dry run); younger ones (a write of another process) and other files stay.
func TestWriteCacheFileRemovesStaleTemps(t *testing.T) {
	for _, dry := range []bool{true, false} {
		dir := t.TempDir()
		path := filepath.Join(dir, ".cache.json.gz")
		old := time.Now().Add(-StaleCacheTemp - time.Minute)
		files := map[string]bool{ // name -> kept
			".cache.json.gz.123.tmp":   false,
			".cache.json.gz.young.tmp": true,
			".other.json.gz.1.tmp":     true,
			"cache.json.gz.1.tmp":      true,
			".cache.json.gz..tmp":      true, // no random part: not a temp name
		}
		for name := range files {
			p := filepath.Join(dir, name)
			if err := os.WriteFile(p, []byte("x"), 0o644); err != nil {
				t.Fatal(err)
			}
			if name != ".cache.json.gz.young.tmp" {
				if err := os.Chtimes(p, old, old); err != nil {
					t.Fatal(err)
				}
			}
		}
		if err := New(dry).WriteCacheFile(path, []byte("data")); err != nil {
			t.Fatal(err)
		}
		for name, kept := range files {
			if _, err := os.Stat(filepath.Join(dir, name)); (err == nil) != kept {
				t.Errorf("dry=%v: %s exists %v, want %v", dry, name, err == nil, kept)
			}
		}
	}
}

// RemoveEmptyDirWithCache removes a folder that holds only the cache file and its temp
// files, and keeps one that holds anything else. A dry run removes nothing but reports the
// folder as removed, and sees entries planned away as gone.
func TestRemoveEmptyDirWithCache(t *testing.T) {
	const name = ".cache.json.gz"
	setup := func(t *testing.T, extra ...string) string {
		t.Helper()
		dir := filepath.Join(t.TempDir(), "lib")
		for _, n := range append([]string{name, name + ".abc.tmp"}, extra...) {
			p := filepath.Join(dir, n)
			if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(p, []byte("x"), 0o644); err != nil {
				t.Fatal(err)
			}
		}
		return dir
	}
	t.Run("only the cache", func(t *testing.T) {
		dir := setup(t)
		ops := New(false)
		if removed, err := ops.RemoveEmptyDirWithCache(dir, name); err != nil || !removed {
			t.Fatalf("removed %v, %v", removed, err)
		}
		if _, err := os.Stat(dir); !os.IsNotExist(err) {
			t.Errorf("folder still there: %v", err)
		}
		if st := ops.Stats(); st != (Stats{}) {
			t.Errorf("stats %+v", st)
		}
	})
	t.Run("something else too", func(t *testing.T) {
		dir := setup(t, "A/song.txt")
		if removed, err := New(false).RemoveEmptyDirWithCache(dir, name); err != nil || removed {
			t.Fatalf("removed %v, %v", removed, err)
		}
		if _, err := os.Stat(filepath.Join(dir, name)); err != nil {
			t.Errorf("cache file removed from a folder that stays: %v", err)
		}
	})
	t.Run("empty", func(t *testing.T) {
		dir := filepath.Join(t.TempDir(), "lib")
		if err := os.Mkdir(dir, 0o755); err != nil {
			t.Fatal(err)
		}
		if removed, err := New(false).RemoveEmptyDirWithCache(dir, name); err != nil || !removed {
			t.Fatalf("removed %v, %v", removed, err)
		}
	})
	t.Run("missing", func(t *testing.T) {
		if removed, err := New(false).RemoveEmptyDirWithCache(filepath.Join(t.TempDir(), "x"), name); err != nil || removed {
			t.Fatalf("removed %v, %v", removed, err)
		}
	})
	t.Run("dry run", func(t *testing.T) {
		dir := setup(t, "A/song.txt")
		ops := New(true)
		out := t.TempDir()
		if err := ops.MoveTo(filepath.Join(dir, "A"), filepath.Join(out, "A")); err != nil {
			t.Fatal(err)
		}
		if removed, err := ops.RemoveEmptyDirWithCache(dir, name); err != nil || !removed {
			t.Fatalf("removed %v, %v", removed, err)
		}
		for _, n := range []string{name, name + ".abc.tmp", "A/song.txt"} {
			if _, err := os.Stat(filepath.Join(dir, n)); err != nil {
				t.Errorf("dry run removed %s: %v", n, err)
			}
		}
	})
}
