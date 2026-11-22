package testutil

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

// Snapshot is what the dry-run "byte-identical" guarantee rests on: it must see a
// changed content (even of the same size and mtime), mtime, mode, a new/removed entry and
// a symlink target, and must be deterministic.
func TestSnapshotDetectsChanges(t *testing.T) {
	fixed := time.Date(2020, 1, 2, 3, 4, 5, 0, time.UTC)
	build := func() (root, f string) {
		root = t.TempDir()
		f = filepath.Join(root, "d", "f.txt")
		WriteFile(t, f, "aaaa")
		if err := os.Chtimes(f, fixed, fixed); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink("f.txt", filepath.Join(root, "d", "link")); err != nil {
			t.Skip("no symlinks:", err)
		}
		return root, f
	}
	root, _ := build()
	if a := Snapshot(t, root); a != Snapshot(t, root) {
		t.Fatal("not deterministic")
	}
	changes := map[string]func(root, f string){
		"content (same size, same mtime)": func(_, f string) {
			_ = os.WriteFile(f, []byte("bbbb"), 0o644)
			_ = os.Chtimes(f, fixed, fixed)
		},
		"mtime": func(_, f string) { _ = os.Chtimes(f, fixed.Add(time.Nanosecond), fixed.Add(time.Nanosecond)) },
		"mode":  func(_, f string) { _ = os.Chmod(f, 0o600) },
		"new empty file": func(root, _ string) {
			WriteFile(t, filepath.Join(root, "new"), "")
		},
		"new empty folder": func(root, _ string) { _ = os.Mkdir(filepath.Join(root, "e"), 0o755) },
		"symlink target": func(root, _ string) {
			l := filepath.Join(root, "d", "link")
			_ = os.Remove(l)
			_ = os.Symlink("other", l)
		},
		"removed file": func(_, f string) { _ = os.Remove(f) },
	}
	for name, change := range changes {
		root, f := build()
		before := Snapshot(t, root)
		change(root, f)
		if Snapshot(t, root) == before {
			t.Errorf("Snapshot missed a change of the %s", name)
		}
	}
}
