package fsops

import (
	"bytes"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"syscall"
	"testing"

	"github.com/nkreimeyer/library-manager/internal/testutil"
)

// simulateCrossDevice makes every rename in move() fail with EXDEV, as a move to another
// file system does, so the copy + delete fallback runs on a single file system. The
// renames inside the fallback (parking the source) are real.
func simulateCrossDevice(t *testing.T) {
	t.Helper()
	rename = func(src, dst string) error {
		return &os.LinkError{Op: "rename", Old: src, New: dst, Err: syscall.EXDEV}
	}
	t.Cleanup(func() { rename = os.Rename })
}

// makeWritable makes every folder below the roots writable, so they can be removed.
func makeWritable(roots ...string) {
	for _, root := range roots {
		filepath.Walk(root, func(p string, info os.FileInfo, err error) error {
			if err == nil && info.IsDir() {
				os.Chmod(p, 0o755)
			}
			return nil
		})
	}
}

// shmDir returns a temp folder on /dev/shm if that is another file system than
// t.TempDir(), and skips the test otherwise.
func shmDir(t *testing.T) (shm, tmp string) {
	t.Helper()
	shm, err := os.MkdirTemp("/dev/shm", "fsops-xdev")
	if err != nil {
		t.Skip("no /dev/shm")
	}
	t.Cleanup(func() {
		makeWritable(shm)
		os.RemoveAll(shm)
	})
	tmp = t.TempDir()
	if sameDevice(t, shm, tmp) {
		t.Skip("/dev/shm and the temp dir are on the same file system")
	}
	return shm, tmp
}

// A move across file systems whose source cannot be removed (its folder is not writable)
// must fail with nothing changed: no copy at the target and the source complete, just
// like a rename on the same file system fails.
func TestMoveCrossDeviceSrcRemovalFails(t *testing.T) {
	testutil.SkipIfRoot(t)
	for _, realRun := range []bool{false, true} {
		t.Run(fmt.Sprintf("realDevices=%v", realRun), func(t *testing.T) {
			var srcRoot, dstRoot string
			if realRun {
				srcRoot, dstRoot = shmDir(t)
			} else {
				simulateCrossDevice(t)
				srcRoot, dstRoot = t.TempDir(), t.TempDir()
			}
			src := filepath.Join(srcRoot, "p", "Song")
			writeFile(t, filepath.Join(src, "song.txt"), "x", 0o644)
			before := snapshot(t, src)
			if err := os.Chmod(filepath.Dir(src), 0o555); err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { os.Chmod(filepath.Dir(src), 0o755) })
			dst := filepath.Join(dstRoot, "lib", "A", "Song")
			ops := New(false)
			err := ops.MoveTo(src, dst)
			if err == nil {
				t.Fatal("moving a source that cannot be removed succeeded")
			}
			if testutil.Exists(dst) {
				t.Error("failed move left a copy at the target")
			}
			if got := snapshot(t, src); strings.Join(got, "\n") != strings.Join(before, "\n") {
				t.Errorf("failed move changed the source:\nbefore %v\nafter  %v", before, got)
			}
			if names := testutil.DirNames(t, filepath.Dir(src)); fmt.Sprint(names) != "[Song]" {
				t.Errorf("source folder holds %v, want only Song", names)
			}
			if s := ops.Stats(); s.Moved != 0 || s.Failed != 1 {
				t.Errorf("Stats = %+v, want Failed=1", s)
			}
			if ops.movedBelowThisRun(filepath.Dir(dst)) {
				t.Error("failed move recorded as a target of this run")
			}
		})
	}
}

// A move across file systems: the target is an exact copy, the source is gone, and no
// hidden leftover remains in the source folder.
func TestMoveCrossDevice(t *testing.T) {
	for _, realRun := range []bool{false, true} {
		t.Run(fmt.Sprintf("realDevices=%v", realRun), func(t *testing.T) {
			var srcRoot, dstRoot string
			if realRun {
				srcRoot, dstRoot = shmDir(t)
			} else {
				simulateCrossDevice(t)
				srcRoot, dstRoot = t.TempDir(), t.TempDir()
			}
			src := filepath.Join(srcRoot, "Cat", "Song")
			writeFile(t, filepath.Join(src, "song.txt"), "#TITLE:x\n", 0o640)
			writeFile(t, filepath.Join(src, "sub", "cover.jpg"), "img", 0o600)
			if err := os.Symlink("song.txt", filepath.Join(src, "link.txt")); err != nil {
				t.Fatal(err)
			}
			before := snapshot(t, src)
			hook := testutil.CaptureLogs(t)
			ops := New(false)
			dst := filepath.Join(dstRoot, "lib", "S", "Song")
			if err := ops.MoveTo(src, dst); err != nil {
				t.Fatal(err)
			}
			if w := warnings(hook); len(w) != 0 {
				t.Errorf("a complete move across file systems logged warnings: %q", w)
			}
			if got := snapshot(t, dst); strings.Join(got, "\n") != strings.Join(before, "\n") {
				t.Errorf("copy differs:\nsrc %v\ndst %v", before, got)
			}
			if testutil.Exists(src) {
				t.Error("source still exists")
			}
			if names := testutil.DirNames(t, filepath.Join(srcRoot, "Cat")); len(names) != 0 {
				t.Errorf("leftovers in the source folder: %v", names)
			}
			if s := ops.Stats(); s.Moved != 1 || s.Failed != 0 {
				t.Errorf("Stats = %+v, want Moved=1", s)
			}

			// MoveInto takes the same path.
			file := filepath.Join(srcRoot, "Cat", "extra.txt")
			writeFile(t, file, "e", 0o644)
			got, err := ops.MoveInto(file, filepath.Join(dstRoot, "extra"), "Cat/extra.txt")
			if err != nil || testutil.ReadFile(t, got) != "e" || testutil.Exists(file) {
				t.Errorf("MoveInto across file systems = %q, %v", got, err)
			}

			// A name close to NAME_MAX (80 x "あ" = 240 bytes): the hidden name used while
			// moving must not be longer than the name itself.
			long := filepath.Join(srcRoot, "Cat", strings.Repeat("あ", 80))
			writeFile(t, filepath.Join(long, "song.txt"), "l", 0o644)
			longDst := filepath.Join(dstRoot, "lib", "L", filepath.Base(long))
			if err := ops.MoveTo(long, longDst); err != nil {
				t.Fatalf("MoveTo of a 240-byte name across file systems: %v", err)
			}
			if testutil.ReadFile(t, filepath.Join(longDst, "song.txt")) != "l" || testutil.Exists(long) {
				t.Error("long-named folder not moved")
			}
		})
	}
}

// If the source cannot be deleted completely after it was copied (a read-only subfolder),
// the complete copy is at the target: the move counts as done (recorded, counted, no
// error), so organize journals it; the leftover of the source stays under a hidden name.
func TestMoveCrossDeviceLeftoverSource(t *testing.T) {
	testutil.SkipIfRoot(t)
	simulateCrossDevice(t)
	srcRoot, dstRoot := t.TempDir(), t.TempDir()
	src := filepath.Join(srcRoot, "Song")
	writeFile(t, filepath.Join(src, "song.txt"), "x", 0o644)
	writeFile(t, filepath.Join(src, "ro", "video.mp4"), "v", 0o644)
	if err := os.Chmod(filepath.Join(src, "ro"), 0o555); err != nil {
		t.Fatal(err)
	}
	before := snapshot(t, src)
	t.Cleanup(func() { makeWritable(srcRoot, dstRoot) })
	hook := testutil.CaptureLogs(t)
	ops := New(false)
	dst := filepath.Join(dstRoot, "lib", "S", "Song")
	if err := ops.MoveTo(src, dst); err != nil {
		t.Fatalf("MoveTo = %v, want success (the complete copy is at the target)", err)
	}
	// The hidden leftover is logged, so it can be found and removed by hand.
	if w := warnings(hook); len(w) != 1 || !strings.Contains(w[0], "could not delete the source") || !strings.Contains(w[0], filepath.Join(srcRoot, ".fsops-move-")) {
		t.Errorf("warnings = %q, want one naming the hidden leftover", w)
	}
	if got := snapshot(t, dst); strings.Join(got, "\n") != strings.Join(before, "\n") {
		t.Errorf("copy differs:\nsrc %v\ndst %v", before, got)
	}
	if testutil.Exists(src) {
		t.Error("the source is still visible under its name")
	}
	names := testutil.DirNames(t, srcRoot)
	if len(names) != 1 || !strings.HasPrefix(names[0], ".fsops-move-") {
		t.Errorf("source folder = %v, want one hidden leftover", names)
	}
	if s := ops.Stats(); s.Moved != 1 || s.Failed != 0 {
		t.Errorf("Stats = %+v, want Moved=1", s)
	}
	if !ops.movedBelowThisRun(filepath.Dir(dst)) {
		t.Error("the move was not recorded as a target of this run")
	}
}

// A copy that fails (a FIFO cannot be copied) removes the partial copy and puts the
// source back under its name, unchanged.
func TestMoveCrossDeviceCopyFailureRestoresSource(t *testing.T) {
	simulateCrossDevice(t)
	srcRoot, dstRoot := t.TempDir(), t.TempDir()
	src := filepath.Join(srcRoot, "Song")
	writeFile(t, filepath.Join(src, "a.txt"), "x", 0o644)
	if err := mkfifo(filepath.Join(src, "z.fifo"), 0o644); err != nil {
		t.Skip("mkfifo:", err)
	}
	before := snapshot(t, src)
	ops := New(false)
	dst := filepath.Join(dstRoot, "lib", "Song")
	err := ops.MoveTo(src, dst)
	if err == nil || !strings.Contains(err.Error(), "unsupported file type") {
		t.Fatalf("MoveTo = %v, want an unsupported-file-type error", err)
	}
	if testutil.Exists(dst) {
		t.Error("partial copy left at the target")
	}
	if got := snapshot(t, src); strings.Join(got, "\n") != strings.Join(before, "\n") {
		t.Errorf("source changed:\nbefore %v\nafter  %v", before, got)
	}
	if names := testutil.DirNames(t, srcRoot); fmt.Sprint(names) != "[Song]" {
		t.Errorf("source folder = %v, want [Song]", names)
	}
	if s := ops.Stats(); s.Moved != 0 || s.Failed != 1 {
		t.Errorf("Stats = %+v, want Failed=1", s)
	}
}

// While the source is copied (which can take minutes), it stays visible under its own
// name, so an interrupted copy never leaves the only complete version hidden.
func TestMoveCrossDeviceSourceVisibleDuringCopy(t *testing.T) {
	simulateCrossDevice(t)
	srcRoot, dstRoot := t.TempDir(), t.TempDir()
	src := filepath.Join(srcRoot, "Song")
	writeFile(t, filepath.Join(src, "song.txt"), "x", 0o644)
	var during []string
	copyAcross = func(from, to string) (bool, error) {
		during = testutil.DirNames(t, srcRoot)
		if from != src {
			t.Errorf("copying from %s, want the source under its own name %s", from, src)
		}
		return copyTree(from, to)
	}
	t.Cleanup(func() { copyAcross = copyTree })
	ops := New(false)
	dst := filepath.Join(dstRoot, "lib", "Song")
	if err := ops.MoveTo(src, dst); err != nil {
		t.Fatal(err)
	}
	if fmt.Sprint(during) != "[Song]" {
		t.Errorf("source folder during the copy = %v, want [Song]", during)
	}
	if testutil.Exists(src) || testutil.ReadFile(t, filepath.Join(dst, "song.txt")) != "x" {
		t.Error("move not done")
	}
}

// If the source cannot be parked after the copy (its folder became read-only), the copy
// is removed again and the move fails with the source unchanged.
func TestMoveCrossDeviceParkAfterCopyFails(t *testing.T) {
	testutil.SkipIfRoot(t)
	simulateCrossDevice(t)
	srcRoot, dstRoot := t.TempDir(), t.TempDir()
	parent := filepath.Join(srcRoot, "p")
	src := filepath.Join(parent, "Song")
	writeFile(t, filepath.Join(src, "song.txt"), "x", 0o644)
	before := snapshot(t, src)
	copyAcross = func(from, to string) (bool, error) {
		created, err := copyTree(from, to)
		if cerr := os.Chmod(parent, 0o555); cerr != nil {
			t.Fatal(cerr)
		}
		return created, err
	}
	t.Cleanup(func() {
		copyAcross = copyTree
		os.Chmod(parent, 0o755)
	})
	ops := New(false)
	dst := filepath.Join(dstRoot, "lib", "Song")
	err := ops.MoveTo(src, dst)
	if err == nil {
		t.Fatal("MoveTo succeeded although the source could not be deleted")
	}
	// The copy was removed again, so the error must not claim that removing it failed.
	if msg := err.Error(); !strings.Contains(msg, "deleting the source after the copy") || strings.Contains(msg, "removing the copy") {
		t.Errorf("MoveTo = %v, want a failed delete of the source and nothing about the removed copy", err)
	}
	if testutil.Exists(dst) {
		t.Error("the copy was left at the target")
	}
	if got := snapshot(t, src); strings.Join(got, "\n") != strings.Join(before, "\n") {
		t.Errorf("source changed:\nbefore %v\nafter  %v", before, got)
	}
	if names := testutil.DirNames(t, parent); fmt.Sprint(names) != "[Song]" {
		t.Errorf("source folder = %v, want [Song]", names)
	}
	if s := ops.Stats(); s.Moved != 0 || s.Failed != 1 {
		t.Errorf("Stats = %+v, want Failed=1", s)
	}
}

// parkName never hands out one name twice in a process, even before anything is
// created under it: concurrent moves in one folder would otherwise collide.
func TestParkNameUnique(t *testing.T) {
	dir := t.TempDir()
	seen := map[string]bool{}
	for i := 0; i < 100; i++ {
		p, err := parkName(filepath.Join(dir, "x"))
		if err != nil || seen[p] {
			t.Fatalf("parkName = %q, %v (seen before: %v)", p, err, seen[p])
		}
		seen[p] = true
	}
}

// A rename error other than EXDEV is returned as is, without a copy.
func TestMoveOtherRenameError(t *testing.T) {
	boom := errors.New("boom")
	rename = func(string, string) error { return boom }
	t.Cleanup(func() { rename = os.Rename })
	root := t.TempDir()
	src := filepath.Join(root, "a.txt")
	writeFile(t, src, "x", 0o644)
	dst := filepath.Join(root, "b.txt")
	if err := move(src, dst); !errors.Is(err, boom) || testutil.Exists(dst) || !testutil.Exists(src) {
		t.Errorf("move = %v, dst exists %v", err, testutil.Exists(dst))
	}
}

// A rename to another drive fails with EXDEV on Unix and with ERROR_NOT_SAME_DEVICE (17)
// on Windows, where errno 17 is not EEXIST and EXDEV never occurs.
func TestIsCrossDevice(t *testing.T) {
	link := func(err error) error { return &os.LinkError{Op: "rename", Old: "a", New: "b", Err: err} }
	for _, tc := range []struct {
		goos string
		err  error
		want bool
	}{
		{"linux", link(syscall.EXDEV), true},
		{"darwin", link(syscall.EXDEV), true},
		{"windows", link(syscall.Errno(17)), true},
		{"windows", link(syscall.EXDEV), true},
		{"linux", link(syscall.Errno(17)), false}, // EEXIST
		{"darwin", link(syscall.Errno(17)), false},
		{"windows", link(syscall.Errno(5)), false}, // ERROR_ACCESS_DENIED
		{"linux", link(syscall.EACCES), false},
		{"windows", errors.New("boom"), false},
		{"linux", nil, false},
	} {
		if got := isCrossDevice(tc.goos, tc.err); got != tc.want {
			t.Errorf("isCrossDevice(%s, %v) = %v, want %v", tc.goos, tc.err, got, tc.want)
		}
	}
}

func TestParkName(t *testing.T) {
	dir := t.TempDir()
	p, err := parkName(filepath.Join(dir, "Song"))
	if err != nil || filepath.Dir(p) != dir || !strings.HasPrefix(filepath.Base(p), ".fsops-move-") {
		t.Fatalf("parkName = %q, %v", p, err)
	}
	writeFile(t, p, "taken", 0o644)
	q, err := parkName(filepath.Join(dir, "Song"))
	if err != nil || q == p || testutil.Exists(q) {
		t.Errorf("parkName with the first name taken = %q, %v", q, err)
	}
	// A parent that is a file: the name cannot be checked.
	file := filepath.Join(dir, "file")
	writeFile(t, file, "x", 0o644)
	if _, err := parkName(filepath.Join(file, "Song")); err == nil {
		t.Error("parkName below a file succeeded")
	}
}

// A journal whose last line was truncated by a crash: the next entry starts a new line
// instead of being merged into the garbage.
func TestAppendJournalAfterTruncatedLine(t *testing.T) {
	p := filepath.Join(t.TempDir(), "j")
	writeFile(t, p, "{\"a\":1}\n{\"tr", 0o644)
	ops := New(false)
	if err := ops.AppendJournal(p, []byte(`{"op":"undone"}`)); err != nil {
		t.Fatal(err)
	}
	if err := ops.AppendJournal(p, []byte(`{"op":"run"}`)); err != nil {
		t.Fatal(err)
	}
	if got, want := testutil.ReadFile(t, p), "{\"a\":1}\n{\"tr\n{\"op\":\"undone\"}\n{\"op\":\"run\"}\n"; got != want {
		t.Errorf("journal = %q, want %q", got, want)
	}
}

// AppendJournal never changes the caller's slice, even if it has spare capacity.
func TestAppendJournalKeepsCallerSlice(t *testing.T) {
	p := filepath.Join(t.TempDir(), "j")
	line := make([]byte, 3, 10)
	copy(line, "abc")
	spare := line[:cap(line)]
	spare[3] = 'Z'
	if err := New(false).AppendJournal(p, line); err != nil {
		t.Fatal(err)
	}
	if string(line) != "abc" || spare[3] != 'Z' {
		t.Errorf("caller slice changed: %q, spare %q", line, spare[3])
	}
	if got := testutil.ReadFile(t, p); got != "abc\n" {
		t.Errorf("journal = %q", got)
	}
}

func TestAppendJournalDirFailure(t *testing.T) {
	root := t.TempDir()
	file := filepath.Join(root, "file")
	writeFile(t, file, "x", 0o644)
	ops := New(false)
	if err := ops.AppendJournal(filepath.Join(file, "j"), []byte("x")); err == nil ||
		!strings.Contains(err.Error(), "creating journal dir") {
		t.Errorf("AppendJournal below a file = %v", err)
	}
	if s := ops.Stats(); s.Failed != 1 {
		t.Errorf("Stats = %+v, want Failed=1", s)
	}
}

func TestCopyTreeReadOnlyDir(t *testing.T) {
	testutil.SkipIfRoot(t)
	src := filepath.Join(t.TempDir(), "Song")
	writeFile(t, filepath.Join(src, "ro", "song.txt"), "x", 0o444)
	if err := os.Chmod(filepath.Join(src, "ro"), 0o555); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.Chmod(filepath.Join(src, "ro"), 0o755) })
	before := snapshot(t, src)
	dst := filepath.Join(t.TempDir(), "copy")
	t.Cleanup(func() { os.Chmod(filepath.Join(dst, "ro"), 0o755) })
	if _, err := copyTree(src, dst); err != nil {
		t.Fatal(err)
	}
	if got := snapshot(t, dst); strings.Join(got, "\n") != strings.Join(before, "\n") {
		t.Errorf("copy differs:\nsrc %v\ndst %v", before, got)
	}
	if st, err := os.Stat(filepath.Join(dst, "ro")); err != nil || st.Mode().Perm() != 0o555 {
		t.Errorf("read-only folder copied as %v, %v", st, err)
	}
}

func TestCopyTreeRejectsFIFO(t *testing.T) {
	src := filepath.Join(t.TempDir(), "Song")
	writeFile(t, filepath.Join(src, "a.txt"), "x", 0o644)
	if err := mkfifo(filepath.Join(src, "pipe"), 0o644); err != nil {
		t.Skip("mkfifo:", err)
	}
	if _, err := copyTree(src, filepath.Join(t.TempDir(), "copy")); err == nil ||
		!strings.Contains(err.Error(), "unsupported file type") {
		t.Errorf("copyTree with a FIFO = %v", err)
	}
}

func TestCopyTreeUnreadableSource(t *testing.T) {
	testutil.SkipIfRoot(t)
	src := filepath.Join(t.TempDir(), "Song")
	writeFile(t, filepath.Join(src, "a.txt"), "x", 0o000)
	if _, err := copyTree(src, filepath.Join(t.TempDir(), "copy")); !errors.Is(err, fs.ErrPermission) {
		t.Errorf("copyTree of an unreadable file = %v", err)
	}
	if _, err := copyTree(filepath.Join(t.TempDir(), "missing"), filepath.Join(t.TempDir(), "copy")); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("copyTree of a missing source = %v", err)
	}
}

// A category whose songs were moved into a target this run is merged into it: files that
// exist there get " (2)", subfolders this run moved something below are merged
// recursively, and a dry run plans the same moves (skipping what it planned away).
func TestMoveIntoMergesCategory(t *testing.T) {
	setup := func(t *testing.T) (lib, inv string) {
		root := t.TempDir()
		lib, inv = filepath.Join(root, "lib"), filepath.Join(root, "invalid")
		writeFile(t, filepath.Join(lib, "Cat", "S1", "song.txt"), "s1", 0o644)
		writeFile(t, filepath.Join(lib, "Cat", "Sub", "S2", "song.txt"), "s2", 0o644)
		writeFile(t, filepath.Join(lib, "Cat", "Sub", "notes.txt"), "notes", 0o644)
		writeFile(t, filepath.Join(lib, "Cat", "cover.jpg"), "new", 0o644)
		writeFile(t, filepath.Join(inv, "Cat", "cover.jpg"), "old", 0o644)
		return lib, inv
	}
	run := func(t *testing.T, dry bool) (string, Stats, *Ops, string, string) {
		lib, inv := setup(t)
		ops := New(dry)
		for _, rel := range []string{"Cat/S1", "Cat/Sub/S2"} {
			if _, err := ops.MoveInto(filepath.Join(lib, rel), inv, rel); err != nil {
				t.Fatal(err)
			}
		}
		got, err := ops.MoveInto(filepath.Join(lib, "Cat"), inv, "Cat")
		if err != nil {
			t.Fatal(err)
		}
		if len(ops.reserved) != 0 {
			t.Errorf("reservations left: %v", ops.reserved)
		}
		return got, ops.Stats(), ops, lib, inv
	}
	realGot, realStats, _, lib, inv := run(t, false)
	if realGot != filepath.Join(inv, "Cat") {
		t.Errorf("target = %q, want the existing invalid/Cat", realGot)
	}
	for rel, want := range map[string]string{
		"Cat/cover.jpg":       "old",
		"Cat/cover (2).jpg":   "new",
		"Cat/S1/song.txt":     "s1",
		"Cat/Sub/S2/song.txt": "s2",
		"Cat/Sub/notes.txt":   "notes",
	} {
		if p := filepath.Join(inv, rel); !testutil.Exists(p) || testutil.ReadFile(t, p) != want {
			t.Errorf("%s missing or wrong", rel)
		}
	}
	if testutil.Exists(filepath.Join(inv, "Cat (2)")) || testutil.Exists(filepath.Join(inv, "Cat", "Sub (2)")) {
		t.Error("merge created a numbered folder")
	}
	if testutil.Exists(filepath.Join(lib, "Cat")) {
		t.Error("merged category still in the library")
	}
	if realStats.Moved != 6 || realStats.Failed != 0 {
		t.Errorf("real Stats = %+v, want Moved=6", realStats)
	}

	dryGot, dryStats, dryOps, dryLib, dryInv := run(t, true)
	if dryGot != filepath.Join(dryInv, "Cat") || dryStats != realStats {
		t.Errorf("dry run = %q %+v, real run = %q %+v", dryGot, dryStats, realGot, realStats)
	}
	if !dryOps.WouldBeMoved(filepath.Join(dryLib, "Cat", "cover.jpg")) || !testutil.Exists(filepath.Join(dryLib, "Cat", "cover.jpg")) {
		t.Error("dry run: the category must be planned away and left on disk")
	}
}

// A failed move of a child aborts the merge: the folder is not removed and the failure is
// returned (counted once, by the child's move).
func TestMoveIntoMergeChildFailure(t *testing.T) {
	testutil.SkipIfRoot(t)
	root := t.TempDir()
	lib, inv := filepath.Join(root, "lib"), filepath.Join(root, "invalid")
	writeFile(t, filepath.Join(lib, "Cat", "S1", "song.txt"), "s1", 0o644)
	writeFile(t, filepath.Join(lib, "Cat", "Locked", "x.txt"), "x", 0o644)
	ops := New(false)
	if _, err := ops.MoveInto(filepath.Join(lib, "Cat", "S1"), inv, "Cat/S1"); err != nil {
		t.Fatal(err)
	}
	// The target folder is read-only: the child cannot be moved into it.
	if err := os.Chmod(filepath.Join(inv, "Cat"), 0o555); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.Chmod(filepath.Join(inv, "Cat"), 0o755) })
	got, err := ops.MoveInto(filepath.Join(lib, "Cat"), inv, "Cat")
	if err == nil || !strings.Contains(err.Error(), "merging") {
		t.Errorf("merge with a failing child = %q, %v", got, err)
	}
	if !testutil.Exists(filepath.Join(lib, "Cat", "Locked", "x.txt")) {
		t.Error("the unmovable child was lost")
	}
	if s := ops.Stats(); s.Moved != 1 || s.Failed != 1 {
		t.Errorf("Stats = %+v, want Moved=1 Failed=1", s)
	}
}

func TestMoveIntoCopyIntoReleaseReservations(t *testing.T) {
	for _, dry := range []bool{false, true} {
		root := t.TempDir()
		src := filepath.Join(root, "lib", "a.mp3")
		writeFile(t, src, "a", 0o644)
		ops := New(dry)
		if _, err := ops.CopyInto(src, filepath.Join(root, "orig"), "a.mp3"); err != nil {
			t.Fatal(err)
		}
		if _, err := ops.MoveInto(src, filepath.Join(root, "extra"), "a.mp3"); err != nil {
			t.Fatal(err)
		}
		if len(ops.reserved) != 0 {
			t.Errorf("dry=%v: reservations left: %v", dry, ops.reserved)
		}
	}
}

// A parent of the target that is a file: the target cannot be checked, the move fails
// and the source is untouched.
func TestMoveIntoTargetBelowFile(t *testing.T) {
	for _, dry := range []bool{false, true} {
		root := t.TempDir()
		src := filepath.Join(root, "a.txt")
		writeFile(t, src, "a", 0o644)
		file := filepath.Join(root, "out")
		writeFile(t, file, "f", 0o644)
		ops := New(dry)
		if got, err := ops.MoveInto(src, file, "x"); err == nil || got != "" || !strings.Contains(err.Error(), "checking") {
			t.Errorf("dry=%v: MoveInto below a file = %q, %v", dry, got, err)
		}
		if !testutil.Exists(src) || testutil.ReadFile(t, file) != "f" {
			t.Errorf("dry=%v: source or file changed", dry)
		}
		if s := ops.Stats(); s.Failed != 1 || s.Moved != 0 {
			t.Errorf("dry=%v: Stats = %+v", dry, s)
		}
		if _, err := ops.CopyInto(src, file, "x"); err == nil {
			t.Errorf("dry=%v: CopyInto below a file succeeded", dry)
		}
	}
}

// Real-run failures of MoveTo: the parent of the target is a file, or the target is
// inside the source.
func TestMoveToRealFailures(t *testing.T) {
	root := t.TempDir()
	src := filepath.Join(root, "A")
	writeFile(t, filepath.Join(src, "song.txt"), "x", 0o644)
	file := filepath.Join(root, "file")
	writeFile(t, file, "f", 0o644)
	ops := New(false)
	// The target cannot be checked (ENOTDIR): it fails before anything is created.
	if err := ops.MoveTo(src, filepath.Join(file, "child", "A")); err == nil || !strings.Contains(err.Error(), "not a directory") {
		t.Errorf("MoveTo below a file = %v", err)
	}
	if err := ops.MoveTo(src, filepath.Join(src, "sub")); err == nil {
		t.Error("MoveTo into itself succeeded")
	}
	if testutil.ReadFile(t, filepath.Join(src, "song.txt")) != "x" {
		t.Error("source changed")
	}
	if s := ops.Stats(); s.Failed != 2 || s.Moved != 0 {
		t.Errorf("Stats = %+v, want Failed=2", s)
	}
	if ops.movedBelowThisRun(root) || len(ops.reserved) != 0 {
		t.Errorf("failed moves recorded: targets %v, reserved %v", ops.runTargets, ops.reserved)
	}
}

func TestMoveIntoRealFailures(t *testing.T) {
	root := t.TempDir()
	src := filepath.Join(root, "A")
	writeFile(t, filepath.Join(src, "song.txt"), "x", 0o644)
	ops := New(false)
	// The target is inside the source.
	if got, err := ops.MoveInto(src, src, "sub"); err == nil || got != "" {
		t.Errorf("MoveInto into itself = %q, %v", got, err)
	}
	if s := ops.Stats(); s.Failed != 1 || s.Moved != 0 || ops.movedBelowThisRun(root) {
		t.Errorf("Stats = %+v, targets %v", s, ops.runTargets)
	}
}

func TestMoveIntoMkdirFailure(t *testing.T) {
	testutil.SkipIfRoot(t)
	root := t.TempDir()
	src := filepath.Join(root, "a.txt")
	writeFile(t, src, "a", 0o644)
	out := filepath.Join(root, "out")
	if err := os.MkdirAll(out, 0o555); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.Chmod(out, 0o755) })
	ops := New(false)
	if got, err := ops.MoveInto(src, out, "S/a.txt"); err == nil || got != "" || !strings.Contains(err.Error(), "creating") {
		t.Errorf("MoveInto into a read-only out dir = %q, %v", got, err)
	}
	if !testutil.Exists(src) || ops.Stats().Failed != 1 {
		t.Errorf("source gone or failure not counted: %+v", ops.Stats())
	}
}

// After an empty folder got a planned move below it, it is not empty any more, in a dry
// run as in a real run; a move to a sibling with a common name prefix does not count.
func TestRemoveEmptyDirAfterMoveBelow(t *testing.T) {
	for _, dry := range []bool{false, true} {
		root := t.TempDir()
		for _, d := range []string{"E", "F"} {
			if err := os.MkdirAll(filepath.Join(root, "lib", d), 0o755); err != nil {
				t.Fatal(err)
			}
		}
		writeFile(t, filepath.Join(root, "new", "X", "song.txt"), "x", 0o644)
		writeFile(t, filepath.Join(root, "new", "Y", "song.txt"), "y", 0o644)
		ops := New(dry)
		if err := ops.MoveTo(filepath.Join(root, "new", "Y"), filepath.Join(root, "lib", "EY")); err != nil {
			t.Fatal(err)
		}
		if removed, err := ops.RemoveEmptyDir(filepath.Join(root, "lib", "E")); err != nil || !removed {
			t.Errorf("dry=%v: RemoveEmptyDir(E) after a move to EY = %v, %v; want removed", dry, removed, err)
		}
		if err := ops.MoveTo(filepath.Join(root, "new", "X"), filepath.Join(root, "lib", "F", "X")); err != nil {
			t.Fatal(err)
		}
		if removed, err := ops.RemoveEmptyDir(filepath.Join(root, "lib", "F")); err != nil || removed {
			t.Errorf("dry=%v: RemoveEmptyDir of a refilled folder = %v, %v", dry, removed, err)
		}
	}
}

// A symlink to an empty folder is no folder of the run: it is left alone (removing it
// would delete the link).
func TestRemoveEmptyDirSymlink(t *testing.T) {
	for _, dry := range []bool{false, true} {
		root := t.TempDir()
		if err := os.MkdirAll(filepath.Join(root, "empty"), 0o755); err != nil {
			t.Fatal(err)
		}
		link := filepath.Join(root, "link")
		if err := os.Symlink("empty", link); err != nil {
			t.Fatal(err)
		}
		ops := New(dry)
		if removed, err := ops.RemoveEmptyDir(link); err != nil || removed || !testutil.Exists(link) {
			t.Errorf("dry=%v: RemoveEmptyDir(symlink) = %v, %v; link exists %v", dry, removed, err, testutil.Exists(link))
		}
		if !isDir(filepath.Join(root, "empty")) {
			t.Errorf("dry=%v: the link target was removed", dry)
		}
	}
}

// An unreadable folder cannot be checked: a counted failure in both modes, left alone.
func TestRemoveEmptyDirUnreadable(t *testing.T) {
	testutil.SkipIfRoot(t)
	var results []string
	for _, dry := range []bool{false, true} {
		root := t.TempDir()
		dir := filepath.Join(root, "D")
		writeFile(t, filepath.Join(dir, "x"), "x", 0o644)
		if err := os.Chmod(dir, 0o000); err != nil {
			t.Fatal(err)
		}
		ops := New(dry)
		removed, err := ops.RemoveEmptyDir(dir)
		os.Chmod(dir, 0o755)
		if err == nil || removed {
			t.Errorf("dry=%v: RemoveEmptyDir(unreadable) = %v, %v", dry, removed, err)
		}
		if !testutil.Exists(filepath.Join(dir, "x")) {
			t.Errorf("dry=%v: content lost", dry)
		}
		results = append(results, fmt.Sprint(removed, err != nil, ops.Stats()))
	}
	if results[0] != results[1] {
		t.Errorf("real run %s, dry run %s", results[0], results[1])
	}
}

// A real run whose os.Remove of an empty folder fails (its parent is read-only).
func TestRemoveEmptyDirRemoveFails(t *testing.T) {
	testutil.SkipIfRoot(t)
	root := t.TempDir()
	dir := filepath.Join(root, "P", "E")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(filepath.Dir(dir), 0o555); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.Chmod(filepath.Dir(dir), 0o755) })
	ops := New(false)
	if removed, err := ops.RemoveEmptyDir(dir); err == nil || removed || !isDir(dir) {
		t.Errorf("RemoveEmptyDir with a read-only parent = %v, %v", removed, err)
	}
	if s := ops.Stats(); s.Failed != 1 {
		t.Errorf("Stats = %+v, want Failed=1", s)
	}
}

func TestTempFileNearMode(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "song.mp3")
	writeFile(t, target, "a", 0o640)
	tmp, err := TempFileNear(target, ".mp3")
	if err != nil {
		t.Fatal(err)
	}
	if st, err := os.Stat(tmp); err != nil || st.Mode().Perm() != 0o640 {
		t.Errorf("temp file mode = %v, %v; want the target's 0640", st.Mode().Perm(), err)
	}
	tmp2, err := TempFileNear(filepath.Join(dir, "new.mp4"), ".mp4")
	if err != nil {
		t.Fatal(err)
	}
	if st, err := os.Stat(tmp2); err != nil || st.Mode().Perm() != 0o644 {
		t.Errorf("temp file mode for a missing target = %v, %v; want 0644", st.Mode().Perm(), err)
	}
}

func TestWriteReportOverwriteAndMode(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "out")
	path := filepath.Join(dir, "report.txt")
	ops := New(false)
	for _, content := range []string{"first\n", "second\n"} {
		if err := ops.WriteReport(path, []byte(content)); err != nil {
			t.Fatal(err)
		}
	}
	if got := testutil.ReadFile(t, path); got != "second\n" {
		t.Errorf("report = %q, want the latest content", got)
	}
	if st, _ := os.Stat(path); st.Mode().Perm() != 0o644 {
		t.Errorf("report mode = %v, want 0644", st.Mode().Perm())
	}
	if names := testutil.DirNames(t, dir); fmt.Sprint(names) != "[report.txt]" {
		t.Errorf("out dir = %v, want only the report", names)
	}
	if s := ops.Stats(); s != (Stats{}) {
		t.Errorf("Stats = %+v, reports are no library operations", s)
	}
}

func TestWriteReportFailures(t *testing.T) {
	root := t.TempDir()
	file := filepath.Join(root, "file")
	writeFile(t, file, "f", 0o644)
	ops := New(false)
	if err := ops.WriteReport(filepath.Join(file, "r.txt"), []byte("x")); err == nil ||
		!strings.Contains(err.Error(), "creating report dir") {
		t.Errorf("WriteReport below a file = %v", err)
	}
	// The report path is a non-empty folder: the rename fails and the temp file is removed.
	dir := filepath.Join(root, "out")
	writeFile(t, filepath.Join(dir, "r.txt", "x"), "x", 0o644)
	if err := ops.WriteReport(filepath.Join(dir, "r.txt"), []byte("x")); err == nil {
		t.Error("WriteReport onto a folder succeeded")
	}
	if names := testutil.DirNames(t, dir); fmt.Sprint(names) != "[r.txt]" {
		t.Errorf("leftovers: %v", names)
	}
	if s := ops.Stats(); s.Failed != 2 {
		t.Errorf("Stats = %+v, want Failed=2", s)
	}
}

func TestWriteReportTempFailure(t *testing.T) {
	testutil.SkipIfRoot(t)
	dir := filepath.Join(t.TempDir(), "ro")
	if err := os.MkdirAll(dir, 0o555); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.Chmod(dir, 0o755) })
	ops := New(false)
	if err := ops.WriteReport(filepath.Join(dir, "r.txt"), []byte("x")); err == nil || ops.Stats().Failed != 1 {
		t.Errorf("WriteReport into a read-only dir = %v, %+v", err, ops.Stats())
	}
}

// The planned content is a copy: the caller may reuse its buffer.
func TestWriteFileAtomicDryRunClonesData(t *testing.T) {
	p := filepath.Join(t.TempDir(), "song.txt")
	writeFile(t, p, "old", 0o644)
	ops := New(true)
	buf := []byte("new")
	if changed, err := ops.WriteFileAtomic(p, buf); err != nil || !changed {
		t.Fatal(changed, err)
	}
	buf[0] = 'X'
	if got, err := ops.ReadFile(p); err != nil || string(got) != "new" {
		t.Errorf("ReadFile = %q, %v; want the planned %q", got, err, "new")
	}
	got, _ := ops.ReadFile(p)
	got[0] = 'Y'
	if again, _ := ops.ReadFile(p); string(again) != "new" {
		t.Errorf("ReadFile returned the stored buffer: %q", again)
	}
}

// A path that cannot be read (a folder): a counted failure, nothing written.
func TestWriteFileAtomicReadError(t *testing.T) {
	for _, dry := range []bool{false, true} {
		dir := t.TempDir()
		target := filepath.Join(dir, "song.txt")
		if err := os.Mkdir(target, 0o755); err != nil {
			t.Fatal(err)
		}
		ops := New(dry)
		if changed, err := ops.WriteFileAtomic(target, []byte("x")); err == nil || changed ||
			!strings.Contains(err.Error(), "reading") {
			t.Errorf("dry=%v: WriteFileAtomic on a folder = %v, %v", dry, changed, err)
		}
		if s := ops.Stats(); s.Failed != 1 || s.Written != 0 {
			t.Errorf("dry=%v: Stats = %+v", dry, s)
		}
		if names := testutil.DirNames(t, dir); fmt.Sprint(names) != "[song.txt]" {
			t.Errorf("dry=%v: leftovers %v", dry, names)
		}
	}
}

// The temp file cannot be created (a read-only folder): a counted failure, the target
// is unchanged and nothing is left behind.
func TestWriteFileAtomicTempFailure(t *testing.T) {
	testutil.SkipIfRoot(t)
	dir := t.TempDir()
	target := filepath.Join(dir, "song.txt")
	writeFile(t, target, "old", 0o644)
	if err := os.Chmod(dir, 0o555); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.Chmod(dir, 0o755) })
	ops := New(false)
	if changed, err := ops.WriteFileAtomic(target, []byte("new")); err == nil || changed {
		t.Errorf("WriteFileAtomic in a read-only folder = %v, %v", changed, err)
	}
	if testutil.ReadFile(t, target) != "old" || ops.Stats().Failed != 1 {
		t.Errorf("target changed or failure not counted: %+v", ops.Stats())
	}
	if names := testutil.DirNames(t, dir); fmt.Sprint(names) != "[song.txt]" {
		t.Errorf("leftovers %v", names)
	}
}

// Unchanged content of a planned write is no new write.
func TestWriteFileAtomicDryRunUnchangedAfterPlan(t *testing.T) {
	p := filepath.Join(t.TempDir(), "song.txt")
	writeFile(t, p, "old", 0o644)
	ops := New(true)
	for i, want := range []bool{true, false} {
		if changed, err := ops.WriteFileAtomic(p, []byte("new")); err != nil || changed != want {
			t.Errorf("write %d: changed = %v, %v; want %v", i, changed, err, want)
		}
	}
	if s := ops.Stats(); s.Written != 1 {
		t.Errorf("Stats = %+v, want Written=1", s)
	}
	if !bytes.Equal([]byte(testutil.ReadFile(t, p)), []byte("old")) {
		t.Error("dry run changed the file")
	}
}

// realSyncFile is syncFile before any test replaced it.
var realSyncFile = syncFile

// recordSyncs makes syncFile record the paths it is called with and return err (or sync
// for real if err is nil).
func recordSyncs(t *testing.T, err error) *[]string {
	t.Helper()
	var synced []string
	syncFile = func(path string) error {
		synced = append(synced, path)
		if err != nil {
			return err
		}
		return realSyncFile(path)
	}
	t.Cleanup(func() { syncFile = realSyncFile })
	return &synced
}

// ffmpeg does not fsync its output and the replaced file is not kept, so the processed
// file is fsynced before it replaces the original, also when it is read-only.
func TestReplaceSyncsBeforeRename(t *testing.T) {
	testutil.SkipIfRoot(t)
	dir := t.TempDir()
	dst, tmp := filepath.Join(dir, "video.mp4"), filepath.Join(dir, ".video.tmp.mp4")
	writeFile(t, dst, "old", 0o644)
	writeFile(t, tmp, "new", 0o444)
	synced := recordSyncs(t, nil)

	ops := New(false)
	if err := ops.Replace(tmp, dst); err != nil {
		t.Fatal(err)
	}
	if want := []string{tmp}; !slices.Equal(*synced, want) || testutil.ReadFile(t, dst) != "new" {
		t.Errorf("synced %v, dst %q; want %v synced and dst replaced", *synced, testutil.ReadFile(t, dst), want)
	}

	// A failing fsync keeps the original and removes the processed file.
	boom := errors.New("boom")
	recordSyncs(t, boom)
	writeFile(t, tmp, "newer", 0o644)
	if err := ops.Replace(tmp, dst); !errors.Is(err, boom) {
		t.Errorf("Replace with a failing fsync = %v, want boom", err)
	}
	if testutil.ReadFile(t, dst) != "new" || testutil.Exists(tmp) {
		t.Errorf("dst = %q, tmp exists %v; want the original kept and tmp removed", testutil.ReadFile(t, dst), testutil.Exists(tmp))
	}
	if s := ops.Stats(); s.Replaced != 1 || s.Failed != 1 {
		t.Errorf("Stats = %+v, want Replaced=1 Failed=1", s)
	}
}

// Rename fsyncs a file before the rename (fixvideos deletes the original once the txt
// points to it), but not a folder; a failing fsync leaves src in place.
func TestRenameSyncsFiles(t *testing.T) {
	dir := t.TempDir()
	src, dst := filepath.Join(dir, ".v.tmp.mp4"), filepath.Join(dir, "v.mp4")
	writeFile(t, src, "new", 0o644)
	sub := filepath.Join(dir, "sub")
	if err := os.Mkdir(sub, 0o755); err != nil {
		t.Fatal(err)
	}
	boom := errors.New("boom")
	recordSyncs(t, boom)
	ops := New(false)
	if err := ops.Rename(src, dst); !errors.Is(err, boom) || !testutil.Exists(src) || testutil.Exists(dst) {
		t.Errorf("Rename with a failing fsync = %v, src exists %v, dst exists %v", err, testutil.Exists(src), testutil.Exists(dst))
	}

	synced := recordSyncs(t, nil)
	if err := ops.Rename(src, dst); err != nil || testutil.ReadFile(t, dst) != "new" {
		t.Fatalf("Rename = %v", err)
	}
	if err := ops.Rename(sub, filepath.Join(dir, "sub2")); err != nil {
		t.Fatal(err)
	}
	if want := []string{src}; !slices.Equal(*synced, want) {
		t.Errorf("synced %v, want %v", *synced, want)
	}
	if s := ops.Stats(); s.Moved != 2 || s.Failed != 1 {
		t.Errorf("Stats = %+v, want Moved=2 Failed=1", s)
	}
}

func TestSyncDirIsBestEffort(t *testing.T) {
	syncDir(filepath.Join(t.TempDir(), "missing")) // logs, never fails or panics
	syncDir(t.TempDir())
}

// ExistsErr tells a path that is not there from one that cannot be checked, which Exists
// reports as absent too.
func TestExistsErr(t *testing.T) {
	testutil.SkipIfRoot(t)
	root := t.TempDir()
	locked := filepath.Join(root, "locked")
	if err := os.Mkdir(locked, 0); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(locked, 0o755) })
	ops := New(false)
	if there, err := ops.ExistsErr(filepath.Join(root, "missing")); there || err != nil {
		t.Errorf("missing: %v, %v; want false, nil", there, err)
	}
	if there, err := ops.ExistsErr(locked); !there || err != nil {
		t.Errorf("locked: %v, %v; want true, nil", there, err)
	}
	if there, err := ops.ExistsErr(filepath.Join(locked, "x")); there || err == nil {
		t.Errorf("below locked: %v, %v; want false and an error", there, err)
	}
	if err := ops.MoveTo(locked, filepath.Join(locked, "x", "y")); err == nil || !strings.Contains(err.Error(), "permission denied") {
		t.Errorf("MoveTo to an unchecked target = %v", err)
	}
}

// A target whose check fails (EIO on a share) may exist: MoveTo does not rename onto it,
// which would replace it.
func TestMoveToUncheckableTarget(t *testing.T) {
	root := t.TempDir()
	src, dst := filepath.Join(root, "src"), filepath.Join(root, "dst")
	writeFile(t, src, "new", 0o644)
	writeFile(t, dst, "old", 0o644)
	orig := lstatTaken
	t.Cleanup(func() { lstatTaken = orig })
	lstatTaken = func(p string) (os.FileInfo, error) {
		if p == dst {
			return nil, &fs.PathError{Op: "lstat", Path: p, Err: syscall.EIO}
		}
		return orig(p)
	}
	ops := New(false)
	if err := ops.MoveTo(src, dst); err == nil || !errors.Is(err, syscall.EIO) {
		t.Errorf("MoveTo = %v, want the EIO", err)
	}
	if data, _ := os.ReadFile(dst); string(data) != "old" {
		t.Errorf("target = %q: it was replaced", data)
	}
	// The source check fails the same way.
	lstatTaken = func(p string) (os.FileInfo, error) {
		if p == src {
			return nil, &fs.PathError{Op: "lstat", Path: p, Err: syscall.EIO}
		}
		return orig(p)
	}
	if err := ops.MoveTo(src, filepath.Join(root, "other")); err == nil || !errors.Is(err, syscall.EIO) {
		t.Errorf("MoveTo with an unchecked source = %v, want the EIO", err)
	}
	if st := ops.Stats(); st.Failed != 2 || st.Moved != 0 {
		t.Errorf("stats = %+v", st)
	}
}
