package fsops

import (
	"crypto/sha256"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	log "github.com/sirupsen/logrus"
)

func TestMain(m *testing.M) {
	log.SetOutput(io.Discard)
	os.Exit(m.Run())
}

// writeFile creates path (and its parents) with the given content and mode.
func writeFile(t *testing.T, path, content string, mode os.FileMode) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), mode); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(path, mode); err != nil { // bypass the umask
		t.Fatal(err)
	}
}

func readFile(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

func exists(path string) bool {
	_, err := os.Lstat(path)
	return err == nil
}

// snapshot returns "relpath:mode:size:mtime:sha256" for every entry below root, so tests
// can assert that nothing at all changed.
func snapshot(t *testing.T, root string) []string {
	t.Helper()
	var out []string
	err := filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(root, path)
		entry := fmt.Sprintf("%s:%v:%d", rel, info.Mode(), info.Size())
		if info.Mode()&os.ModeSymlink == 0 { // symlink mtimes are not preserved by design
			entry += fmt.Sprintf(":%d", info.ModTime().UnixNano())
		}
		if info.Mode().IsRegular() {
			entry += fmt.Sprintf(":%x", sha256.Sum256([]byte(readFile(t, path))))
		}
		out = append(out, entry)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return out
}

func dirNames(t *testing.T, dir string) []string {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, e := range entries {
		names = append(names, e.Name())
	}
	return names
}

func TestWriteFileAtomicUnchanged(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "song.txt")
	writeFile(t, path, "#TITLE:x\n", 0o640)
	old := time.Now().Add(-time.Hour).Truncate(time.Second)
	if err := os.Chtimes(path, old, old); err != nil {
		t.Fatal(err)
	}

	ops := New(false)
	changed, err := ops.WriteFileAtomic(path, []byte("#TITLE:x\n"))
	if err != nil || changed {
		t.Fatalf("WriteFileAtomic = %v, %v; want false, nil", changed, err)
	}
	st, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if !st.ModTime().Equal(old) {
		t.Errorf("mtime changed: %v -> %v", old, st.ModTime())
	}
	if s := ops.Stats(); s != (Stats{}) {
		t.Errorf("Stats = %+v, want zero", s)
	}
	if names := dirNames(t, dir); len(names) != 1 {
		t.Errorf("directory contents = %v, want only song.txt", names)
	}
}

func TestWriteFileAtomicChanged(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "song.txt")
	writeFile(t, path, "old content that is longer than the new one\n", 0o664)

	ops := New(false)
	changed, err := ops.WriteFileAtomic(path, []byte("new\n"))
	if err != nil || !changed {
		t.Fatalf("WriteFileAtomic = %v, %v; want true, nil", changed, err)
	}
	if got := readFile(t, path); got != "new\n" {
		t.Errorf("content = %q", got)
	}
	st, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if st.Mode().Perm() != 0o664 {
		t.Errorf("mode = %v, want 0664", st.Mode().Perm())
	}
	if names := dirNames(t, dir); len(names) != 1 || names[0] != "song.txt" {
		t.Errorf("directory contents = %v, want only song.txt (no temp files)", names)
	}
	if s := ops.Stats(); s.Written != 1 || s.Failed != 0 {
		t.Errorf("Stats = %+v, want Written=1", s)
	}
}

func TestWriteFileAtomicNewFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "new.txt")
	changed, err := New(false).WriteFileAtomic(path, []byte("x"))
	if err != nil || !changed {
		t.Fatalf("WriteFileAtomic = %v, %v; want true, nil", changed, err)
	}
	st, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if st.Mode().Perm() != 0o644 {
		t.Errorf("mode = %v, want 0644", st.Mode().Perm())
	}
}

func TestWriteFileAtomicFailureCounted(t *testing.T) {
	ops := New(false)
	missingDir := filepath.Join(t.TempDir(), "missing", "song.txt")
	if _, err := ops.WriteFileAtomic(missingDir, []byte("x")); err == nil {
		t.Fatal("expected error for missing directory")
	}
	if s := ops.Stats(); s.Failed != 1 || s.Written != 0 {
		t.Errorf("Stats = %+v, want Failed=1", s)
	}
}

func TestWriteReport(t *testing.T) {
	out := filepath.Join(t.TempDir(), "out", "nested")
	for _, dry := range []bool{false, true} {
		path := filepath.Join(out, fmt.Sprintf("report-%v.txt", dry))
		if err := New(dry).WriteReport(path, []byte("a\nb\n")); err != nil {
			t.Fatalf("WriteReport(dry=%v): %v", dry, err)
		}
		if got := readFile(t, path); got != "a\nb\n" {
			t.Errorf("report content = %q", got)
		}
	}
}

func TestDryRunChangesNothing(t *testing.T) {
	lib := t.TempDir()
	out := t.TempDir()
	song := filepath.Join(lib, "Artist - Song")
	writeFile(t, filepath.Join(song, "song.txt"), "#TITLE:Song\n", 0o644)
	writeFile(t, filepath.Join(song, "song.mp3"), "audio", 0o644)
	writeFile(t, filepath.Join(song, "extra.jpg"), "img", 0o644)
	writeFile(t, filepath.Join(song, "old.avi"), "video", 0o644)
	writeFile(t, filepath.Join(song, "trash.nfo"), "nfo", 0o644)
	before := snapshot(t, lib)

	ops := New(true)
	changed, err := ops.WriteFileAtomic(filepath.Join(song, "song.txt"), []byte("#TITLE:Other\n"))
	if err != nil || !changed {
		t.Errorf("WriteFileAtomic = %v, %v; want true, nil", changed, err)
	}
	if changed, err := ops.WriteFileAtomic(filepath.Join(song, "song.txt"), []byte("#TITLE:Song\n")); err != nil || changed {
		t.Errorf("unchanged WriteFileAtomic = %v, %v; want false, nil", changed, err)
	}
	dst, err := ops.MoveInto(filepath.Join(song, "extra.jpg"), filepath.Join(out, "extra-files"), filepath.Join("Artist - Song", "extra.jpg"))
	if err != nil {
		t.Errorf("MoveInto: %v", err)
	}
	if want := filepath.Join(out, "extra-files", "Artist - Song", "extra.jpg"); dst != want {
		t.Errorf("MoveInto target = %q, want %q", dst, want)
	}
	if _, err := ops.MoveInto(song, filepath.Join(out, "invalid"), "Artist - Song"); err != nil {
		t.Errorf("MoveInto folder: %v", err)
	}
	if err := ops.Rename(filepath.Join(song, "old.avi"), filepath.Join(song, "new.avi")); err != nil {
		t.Errorf("Rename: %v", err)
	}
	// Replace with a temp file that does not exist (callers create none in dry-run mode).
	if err := ops.Replace(filepath.Join(song, ".song.sanitizer-123.mp3"), filepath.Join(song, "song.mp3")); err != nil {
		t.Errorf("Replace: %v", err)
	}
	if err := ops.Remove(filepath.Join(song, "trash.nfo")); err != nil {
		t.Errorf("Remove: %v", err)
	}

	if after := snapshot(t, lib); strings.Join(after, "\n") != strings.Join(before, "\n") {
		t.Errorf("library changed in dry-run:\nbefore %v\nafter  %v", before, after)
	}
	if names := dirNames(t, out); len(names) != 0 {
		t.Errorf("out dir changed in dry-run: %v", names)
	}
	want := Stats{Written: 1, Moved: 3, Removed: 1, Replaced: 1}
	if s := ops.Stats(); s != want {
		t.Errorf("Stats = %+v, want %+v", s, want)
	}
}

func TestDryRunMoveIntoReportsCollisionName(t *testing.T) {
	lib, out := t.TempDir(), t.TempDir()
	writeFile(t, filepath.Join(lib, "Song", "cover.jpg"), "new", 0o644)
	writeFile(t, filepath.Join(out, "Song", "cover.jpg"), "existing", 0o644)
	dst, err := New(true).MoveInto(filepath.Join(lib, "Song", "cover.jpg"), out, filepath.Join("Song", "cover.jpg"))
	if err != nil {
		t.Fatal(err)
	}
	if want := filepath.Join(out, "Song", "cover (2).jpg"); dst != want {
		t.Errorf("target = %q, want %q", dst, want)
	}
	if exists(dst) {
		t.Error("dry-run created the target")
	}
}

func TestMoveIntoMirrorsRelPath(t *testing.T) {
	lib, out := t.TempDir(), t.TempDir()
	src := filepath.Join(lib, "Category", "Artist - Song", "cover.jpg")
	writeFile(t, src, "img", 0o600)
	rel := filepath.Join("Category", "Artist - Song", "cover.jpg")

	ops := New(false)
	dst, err := ops.MoveInto(src, filepath.Join(out, "extra-files"), rel)
	if err != nil {
		t.Fatal(err)
	}
	if want := filepath.Join(out, "extra-files", rel); dst != want {
		t.Errorf("target = %q, want %q", dst, want)
	}
	if exists(src) {
		t.Error("source still exists")
	}
	if got := readFile(t, dst); got != "img" {
		t.Errorf("content = %q", got)
	}
	if s := ops.Stats(); s.Moved != 1 || s.Failed != 0 {
		t.Errorf("Stats = %+v", s)
	}
}

func TestMoveIntoCollisions(t *testing.T) {
	tests := []struct {
		name  string
		isDir bool
		item  string
		want  []string // expected targets of three consecutive moves
	}{
		{"file", false, "cover.jpg", []string{"cover.jpg", "cover (2).jpg", "cover (3).jpg"}},
		{"file without extension", false, "README", []string{"README", "README (2)", "README (3)"}},
		{"folder with dots", true, "Mr. Brightside", []string{"Mr. Brightside", "Mr. Brightside (2)", "Mr. Brightside (3)"}},
		{"folder with extension-like name", true, "Song.v2", []string{"Song.v2", "Song.v2 (2)", "Song.v2 (3)"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			out := t.TempDir()
			ops := New(false)
			for i, want := range tt.want {
				src := filepath.Join(t.TempDir(), tt.item)
				if tt.isDir {
					writeFile(t, filepath.Join(src, "song.txt"), fmt.Sprint(i), 0o644)
				} else {
					writeFile(t, src, fmt.Sprint(i), 0o644)
				}
				dst, err := ops.MoveInto(src, out, tt.item)
				if err != nil {
					t.Fatal(err)
				}
				if dst != filepath.Join(out, want) {
					t.Errorf("move %d: target = %q, want %q", i, filepath.Base(dst), want)
				}
				content := dst
				if tt.isDir {
					content = filepath.Join(dst, "song.txt")
				}
				if got := readFile(t, content); got != fmt.Sprint(i) {
					t.Errorf("move %d: content = %q", i, got)
				}
			}
		})
	}
}

func TestMoveIntoConcurrentSameName(t *testing.T) {
	const n = 20
	out := t.TempDir()
	srcs := make([]string, n)
	for i := range srcs {
		srcs[i] = filepath.Join(t.TempDir(), "cover.jpg")
		writeFile(t, srcs[i], fmt.Sprint(i), 0o644)
	}

	ops := New(false)
	targets := make([]string, n)
	var wg sync.WaitGroup
	for i := range srcs {
		wg.Add(1)
		go func() {
			defer wg.Done()
			dst, err := ops.MoveInto(srcs[i], filepath.Join(out, "extra-files"), filepath.Join("Song", "cover.jpg"))
			if err != nil {
				t.Errorf("MoveInto %d: %v", i, err)
			}
			targets[i] = dst
		}()
	}
	wg.Wait()

	seen := map[string]bool{}
	for _, dst := range targets {
		if seen[dst] {
			t.Errorf("target %q used twice", dst)
		}
		seen[dst] = true
	}
	// Every source content must arrive exactly once.
	dir := filepath.Join(out, "extra-files", "Song")
	names := dirNames(t, dir)
	if len(names) != n {
		t.Fatalf("got %d files, want %d: %v", len(names), n, names)
	}
	var contents []string
	for _, name := range names {
		contents = append(contents, readFile(t, filepath.Join(dir, name)))
	}
	sort.Strings(contents)
	var want []string
	for i := range n {
		want = append(want, fmt.Sprint(i))
	}
	sort.Strings(want)
	if strings.Join(contents, ",") != strings.Join(want, ",") {
		t.Errorf("contents = %v, want %v", contents, want)
	}
	if s := ops.Stats(); s.Moved != n || s.Failed != 0 {
		t.Errorf("Stats = %+v", s)
	}
}

func TestMoveIntoMissingSourceCounted(t *testing.T) {
	ops := New(false)
	if _, err := ops.MoveInto(filepath.Join(t.TempDir(), "missing"), t.TempDir(), "missing"); err == nil {
		t.Fatal("expected error")
	}
	if s := ops.Stats(); s.Failed != 1 || s.Moved != 0 {
		t.Errorf("Stats = %+v, want Failed=1", s)
	}
}

func TestRename(t *testing.T) {
	dir := t.TempDir()
	a, b, c := filepath.Join(dir, "a.avi"), filepath.Join(dir, "b.avi"), filepath.Join(dir, "c.avi")
	writeFile(t, a, "a", 0o644)
	writeFile(t, b, "b", 0o644)

	for _, dry := range []bool{true, false} {
		ops := New(dry)
		if err := ops.Rename(a, b); err == nil {
			t.Errorf("dry=%v: Rename onto existing target succeeded", dry)
		}
		if s := ops.Stats(); s.Failed != 1 || s.Moved != 0 {
			t.Errorf("dry=%v: Stats = %+v, want Failed=1", dry, s)
		}
		if readFile(t, a) != "a" || readFile(t, b) != "b" {
			t.Errorf("dry=%v: files changed after refused rename", dry)
		}
	}

	ops := New(false)
	if err := ops.Rename(a, c); err != nil {
		t.Fatal(err)
	}
	if exists(a) || readFile(t, c) != "a" {
		t.Error("rename did not move the file")
	}
	if s := ops.Stats(); s.Moved != 1 {
		t.Errorf("Stats = %+v, want Moved=1", s)
	}
	if err := ops.Rename(filepath.Join(dir, "missing"), filepath.Join(dir, "x")); err == nil {
		t.Error("Rename of missing source succeeded")
	}
	if s := ops.Stats(); s.Failed != 1 {
		t.Errorf("Stats = %+v, want Failed=1", s)
	}
}

func TestReplace(t *testing.T) {
	dir := t.TempDir()
	dst := filepath.Join(dir, "song.mp3")
	writeFile(t, dst, "old", 0o644)
	tmp, err := TempFileNear(dst, ".mp3")
	if err != nil {
		t.Fatal(err)
	}
	writeFile(t, tmp, "new", 0o644)

	ops := New(false)
	if err := ops.Replace(tmp, dst); err != nil {
		t.Fatal(err)
	}
	if exists(tmp) || readFile(t, dst) != "new" {
		t.Error("Replace did not move tmp onto dst")
	}
	if err := ops.Replace(filepath.Join(dir, "missing.tmp"), dst); err == nil {
		t.Error("Replace with missing tmp succeeded")
	}
	if s := ops.Stats(); s.Replaced != 1 || s.Failed != 1 {
		t.Errorf("Stats = %+v, want Replaced=1 Failed=1", s)
	}
}

func TestRemove(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "x.nfo")
	writeFile(t, path, "x", 0o644)
	ops := New(false)
	if err := ops.Remove(path); err != nil {
		t.Fatal(err)
	}
	if exists(path) {
		t.Error("file still exists")
	}
	if err := ops.Remove(path); err == nil {
		t.Error("removing a missing file succeeded")
	}
	if s := ops.Stats(); s.Removed != 1 || s.Failed != 1 {
		t.Errorf("Stats = %+v, want Removed=1 Failed=1", s)
	}
}

func TestFail(t *testing.T) {
	ops := New(false)
	ops.Fail()
	ops.Fail()
	if s := ops.Stats(); s.Failed != 2 {
		t.Errorf("Failed = %d, want 2", s.Failed)
	}
}

func TestTempFileNear(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "Artist - Song.avi")
	writeFile(t, target, "v", 0o644)

	a, err := TempFileNear(target, ".mp4")
	if err != nil {
		t.Fatal(err)
	}
	b, err := TempFileNear(target, ".mp4")
	if err != nil {
		t.Fatal(err)
	}
	if a == b {
		t.Errorf("two temp files share the name %q", a)
	}
	for _, tmp := range []string{a, b} {
		if filepath.Dir(tmp) != dir {
			t.Errorf("temp file %q not in %q", tmp, dir)
		}
		if filepath.Ext(tmp) != ".mp4" {
			t.Errorf("temp file %q has wrong extension", tmp)
		}
		if !strings.HasPrefix(filepath.Base(tmp), ".Artist - Song.") {
			t.Errorf("temp file %q should be hidden and named after the target", tmp)
		}
		st, err := os.Stat(tmp)
		if err != nil || st.Size() != 0 {
			t.Errorf("temp file %q: stat %v, err %v; want empty file", tmp, st, err)
		}
	}
	if _, err := TempFileNear(filepath.Join(dir, "missing", "x.mp3"), ".mp3"); err == nil {
		t.Error("TempFileNear in missing dir succeeded")
	}
}

func TestCopyTree(t *testing.T) {
	src := filepath.Join(t.TempDir(), "Mr. Brightside")
	writeFile(t, filepath.Join(src, "song.txt"), "#TITLE:Mr. Brightside\n", 0o664)
	writeFile(t, filepath.Join(src, "song.mp3"), strings.Repeat("a", 100_000), 0o600)
	writeFile(t, filepath.Join(src, "covers", "nested", "cover.jpg"), "img", 0o640)
	if err := os.Symlink("song.mp3", filepath.Join(src, "link.mp3")); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(filepath.Join(src, "covers"), 0o775); err != nil {
		t.Fatal(err)
	}
	// Distinct, old mtimes for files and directories (set deepest first).
	base := time.Date(2020, 1, 2, 3, 4, 5, 0, time.UTC)
	for i, rel := range []string{"song.txt", "song.mp3", "covers/nested/cover.jpg", "covers/nested", "covers", "."} {
		ts := base.Add(time.Duration(i) * time.Hour)
		if err := os.Chtimes(filepath.Join(src, rel), ts, ts); err != nil {
			t.Fatal(err)
		}
	}

	dst := filepath.Join(t.TempDir(), "copy")
	if err := copyTree(src, dst); err != nil {
		t.Fatal(err)
	}
	if a, b := snapshot(t, src), snapshot(t, dst); strings.Join(a, "\n") != strings.Join(b, "\n") {
		t.Errorf("copy differs:\nsrc %v\ndst %v", a, b)
	}
	if link, err := os.Readlink(filepath.Join(dst, "link.mp3")); err != nil || link != "song.mp3" {
		t.Errorf("symlink = %q, %v", link, err)
	}
}

func TestCopyTreeSingleFile(t *testing.T) {
	src := filepath.Join(t.TempDir(), "cover.jpg")
	writeFile(t, src, "img", 0o640)
	mt := time.Date(2021, 5, 6, 7, 8, 9, 0, time.UTC)
	if err := os.Chtimes(src, mt, mt); err != nil {
		t.Fatal(err)
	}
	dst := filepath.Join(t.TempDir(), "cover.jpg")
	if err := copyTree(src, dst); err != nil {
		t.Fatal(err)
	}
	st, err := os.Stat(dst)
	if err != nil {
		t.Fatal(err)
	}
	if st.Mode().Perm() != 0o640 || !st.ModTime().Equal(mt) || readFile(t, dst) != "img" {
		t.Errorf("copy: mode %v mtime %v content %q", st.Mode().Perm(), st.ModTime(), readFile(t, dst))
	}
}

func TestCopyFileRefusesExistingTarget(t *testing.T) {
	dir := t.TempDir()
	src, dst := filepath.Join(dir, "a"), filepath.Join(dir, "b")
	writeFile(t, src, "a", 0o644)
	writeFile(t, dst, "b", 0o644)
	info, err := os.Stat(src)
	if err != nil {
		t.Fatal(err)
	}
	if err := copyFile(src, dst, info); err == nil {
		t.Error("copyFile overwrote an existing file")
	}
	if readFile(t, dst) != "b" {
		t.Error("existing target modified")
	}
}
