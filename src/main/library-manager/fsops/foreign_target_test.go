package fsops

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"testing"

	"github.com/nkreimeyer/library-manager/internal/testutil"
)

// A target folder that something else creates after MoveTo checked that it was free (a
// second instance, a sync client) is never deleted by the failing copy across file systems:
// the copy stops before writing into it, and src stays in place.
func TestMoveCrossDeviceKeepsForeignTarget(t *testing.T) {
	simulateCrossDevice(t)
	srcRoot, dstRoot := t.TempDir(), t.TempDir()
	src := filepath.Join(srcRoot, "Song")
	writeFile(t, filepath.Join(src, "a.txt"), "ours", 0o644)
	dst := filepath.Join(dstRoot, "lib", "Song")
	copyAcross = func(from, to string) (bool, error) {
		writeFile(t, filepath.Join(to, "a.txt"), "theirs", 0o644)
		writeFile(t, filepath.Join(to, "other.txt"), "other", 0o644)
		return copyTree(from, to)
	}
	t.Cleanup(func() { copyAcross = copyTree })
	ops := New(false)
	if err := ops.MoveTo(src, dst); err == nil {
		t.Fatal("MoveTo onto a folder created during the move succeeded")
	}
	if testutil.ReadFile(t, filepath.Join(src, "a.txt")) != "ours" {
		t.Error("source changed")
	}
	if testutil.ReadFile(t, filepath.Join(dst, "a.txt")) != "theirs" || testutil.ReadFile(t, filepath.Join(dst, "other.txt")) != "other" {
		t.Error("the foreign target folder was changed")
	}
}

// The same for a single file moved across file systems: a file at dst that this move did
// not create stays.
func TestMoveCrossDeviceKeepsForeignTargetFile(t *testing.T) {
	simulateCrossDevice(t)
	srcRoot, dstRoot := t.TempDir(), t.TempDir()
	src := filepath.Join(srcRoot, "cover.jpg")
	writeFile(t, src, "ours", 0o644)
	dst := filepath.Join(dstRoot, "cover.jpg")
	copyAcross = func(from, to string) (bool, error) {
		writeFile(t, to, "theirs", 0o644)
		return copyTree(from, to)
	}
	t.Cleanup(func() { copyAcross = copyTree })
	if err := New(false).MoveTo(src, dst); err == nil {
		t.Fatal("MoveTo onto a file created during the move succeeded")
	}
	if testutil.ReadFile(t, src) != "ours" || testutil.ReadFile(t, dst) != "theirs" {
		t.Error("source or foreign target changed")
	}
}

// A partial copy of this move's own is still removed after a failure.
func TestMoveCrossDeviceRemovesOwnPartialCopy(t *testing.T) {
	testutil.SkipIfRoot(t)
	simulateCrossDevice(t)
	srcRoot, dstRoot := t.TempDir(), t.TempDir()
	src := filepath.Join(srcRoot, "Song")
	writeFile(t, filepath.Join(src, "a.txt"), "a", 0o644)
	writeFile(t, filepath.Join(src, "b.txt"), "b", 0o000) // unreadable: the copy fails
	dst := filepath.Join(dstRoot, "Song")
	if err := New(false).MoveTo(src, dst); err == nil {
		t.Fatal("MoveTo with an unreadable file succeeded")
	}
	if testutil.Exists(dst) {
		t.Error("partial copy left behind")
	}
}

// CopyInto never deletes a target that something else created after its reservation (e.g.
// another run's backup in originals/, the only copy of an original already replaced).
func TestCopyIntoKeepsForeignTarget(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "song.mp3")
	writeFile(t, src, "ours", 0o644)
	out := filepath.Join(dir, "out")
	var target string
	copyOne = func(from, to string, info os.FileInfo) error {
		target = to
		writeFile(t, to, "theirs", 0o644)
		return copyFile(from, to, info)
	}
	t.Cleanup(func() { copyOne = copyFile })
	ops := New(false)
	if _, err := ops.CopyInto(src, out, "song.mp3"); !errors.Is(err, fs.ErrExist) {
		t.Fatalf("CopyInto onto a file created meanwhile = %v, want ErrExist", err)
	}
	if testutil.ReadFile(t, target) != "theirs" {
		t.Error("the foreign target was deleted or changed")
	}
}

// A copy that fails after creating its target removes its own partial file.
func TestCopyFileRemovesOwnPartialCopy(t *testing.T) {
	dir := t.TempDir()
	src, dst := filepath.Join(dir, "a"), filepath.Join(dir, "b")
	writeFile(t, src, "abc", 0o644)
	info, err := os.Stat(src)
	if err != nil {
		t.Fatal(err)
	}
	writeFile(t, src, "abcdef", 0o644) // grew after the stat: the size check fails
	if err := copyFile(src, dst, info); err == nil {
		t.Fatal("copyFile with a size mismatch succeeded")
	}
	if testutil.Exists(dst) {
		t.Error("partial copy left behind")
	}
}

// A read-only media file can be processed: the temp file ffmpeg writes stays writable, and
// Replace gives the result the read-only mode of the file it replaces.
func TestReplaceReadOnlyTarget(t *testing.T) {
	testutil.SkipIfRoot(t)
	dir := t.TempDir()
	dst := filepath.Join(dir, "song.mp3")
	writeFile(t, dst, "old", 0o444)
	tmp, err := TempFileNear(dst, ".mp3")
	if err != nil {
		t.Fatal(err)
	}
	// What ffmpeg -y does with an existing output file.
	f, err := os.OpenFile(tmp, os.O_WRONLY|os.O_TRUNC, 0)
	if err != nil {
		t.Fatalf("the temp file of a read-only target cannot be written: %v", err)
	}
	if _, err := f.WriteString("new"); err != nil {
		t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
	ops := New(false)
	if err := ops.Replace(tmp, dst); err != nil {
		t.Fatal(err)
	}
	st, err := os.Stat(dst)
	if err != nil || st.Mode().Perm() != 0o444 || testutil.ReadFile(t, dst) != "new" {
		t.Errorf("replaced file: %v, %v, %q; want mode 0444 and the new content", st, err, testutil.ReadFile(t, dst))
	}
	if testutil.Exists(tmp) {
		t.Error("temp file left behind")
	}
}

// mergeInto merges a subfolder only under MoveInto's rules: a non-empty subfolder whose
// counterpart comes from an earlier run gets " (2)", the earlier run's files stay as they are.
func TestMergeIntoDoesNotMergeEarlierRunSubfolder(t *testing.T) {
	lib, out := t.TempDir(), t.TempDir()
	writeFile(t, filepath.Join(out, "Cat", "Sub", "old.txt"), "old", 0o644)
	writeFile(t, filepath.Join(lib, "Cat", "SongA", "song.txt"), "a", 0o644)
	writeFile(t, filepath.Join(lib, "Cat", "Sub", "new.txt"), "new", 0o644)
	ops := New(false)
	if _, err := ops.MoveInto(filepath.Join(lib, "Cat", "SongA"), out, filepath.Join("Cat", "SongA")); err != nil {
		t.Fatal(err)
	}
	got, err := ops.MoveInto(filepath.Join(lib, "Cat"), out, "Cat")
	if err != nil || got != filepath.Join(out, "Cat") {
		t.Fatalf("MoveInto(Cat) = %q, %v; want a merge into %s", got, err, filepath.Join(out, "Cat"))
	}
	if testutil.ReadFile(t, filepath.Join(out, "Cat", "Sub", "old.txt")) != "old" ||
		testutil.ReadFile(t, filepath.Join(out, "Cat", "Sub (2)", "new.txt")) != "new" ||
		testutil.Exists(filepath.Join(out, "Cat", "Sub", "new.txt")) {
		t.Errorf("out/Cat = %v, want SongA, Sub (old.txt) and Sub (2) (new.txt)", testutil.DirNames(t, filepath.Join(out, "Cat")))
	}
}
