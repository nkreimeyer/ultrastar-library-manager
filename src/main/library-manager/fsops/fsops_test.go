package fsops

import (
	"crypto/sha256"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/nkreimeyer/library-manager/internal/testutil"
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
		entry := fmt.Sprintf("%s:%v", rel, info.Mode())
		if !info.IsDir() { // a folder's size is the file system's bookkeeping (tmpfs and btrfs differ)
			entry += fmt.Sprintf(":%d", info.Size())
		}
		if info.Mode()&os.ModeSymlink == 0 { // symlink mtimes are not preserved by design
			entry += fmt.Sprintf(":%d", info.ModTime().UnixNano())
		}
		if info.Mode().IsRegular() {
			entry += fmt.Sprintf(":%x", sha256.Sum256([]byte(testutil.ReadFile(t, path))))
		}
		out = append(out, entry)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return out
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
	if names := testutil.DirNames(t, dir); len(names) != 1 {
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
	if got := testutil.ReadFile(t, path); got != "new\n" {
		t.Errorf("content = %q", got)
	}
	st, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if st.Mode().Perm() != 0o664 {
		t.Errorf("mode = %v, want 0664", st.Mode().Perm())
	}
	if names := testutil.DirNames(t, dir); len(names) != 1 || names[0] != "song.txt" {
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
		if got := testutil.ReadFile(t, path); got != "a\nb\n" {
			t.Errorf("report content = %q", got)
		}
	}
}

// JSON reports keep "&", "<" and ">" literal, are indented by two spaces and end with
// exactly one newline, in real and dry runs. A value that cannot be encoded writes nothing
// and is counted as a failure.
func TestWriteJSONReport(t *testing.T) {
	out := filepath.Join(t.TempDir(), "out", "nested")
	type entry struct {
		Name string `json:"name"`
	}
	v := struct {
		Songs []entry `json:"songs"`
	}{Songs: []entry{{Name: "Simon & Garfunkel - <3 > 2"}}}
	want := "{\n  \"songs\": [\n    {\n      \"name\": \"Simon & Garfunkel - <3 > 2\"\n    }\n  ]\n}\n"
	for _, dry := range []bool{false, true} {
		path := filepath.Join(out, fmt.Sprintf("report-%v.json", dry))
		ops := New(dry)
		if err := ops.WriteJSONReport(path, v); err != nil {
			t.Fatalf("WriteJSONReport(dry=%v): %v", dry, err)
		}
		if got := testutil.ReadFile(t, path); got != want {
			t.Errorf("dry=%v: report = %q, want %q", dry, got, want)
		}
		if s := ops.Stats(); s != (Stats{}) {
			t.Errorf("dry=%v: Stats = %+v, want zero", dry, s)
		}
	}

	ops := New(false)
	bad := filepath.Join(out, "bad.json")
	if err := ops.WriteJSONReport(bad, map[string]any{"f": func() {}}); err == nil {
		t.Error("an unencodable value must return an error")
	}
	if _, err := os.Lstat(bad); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("an unencodable value must write nothing: %v", err)
	}
	if entries, err := os.ReadDir(out); err != nil || len(entries) != 2 {
		t.Errorf("out dir = %v (%v), want only the two reports (no temp file)", entries, err)
	}
	if s := ops.Stats(); s.Failed != 1 {
		t.Errorf("Stats = %+v, want Failed=1", s)
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
	// Compared with the planned content, as a real run compares with the first write.
	if changed, err := ops.WriteFileAtomic(filepath.Join(song, "song.txt"), []byte("#TITLE:Other\n")); err != nil || changed {
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
	if names := testutil.DirNames(t, out); len(names) != 0 {
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
	if testutil.Exists(dst) {
		t.Error("dry-run created the target")
	}
}

// A dry run hands out the targets a real run does: a planned MoveInto or CopyInto takes
// its target (the next one to the same path gets " (2)"), a folder emptied by planned moves
// is merged into an existing folder, and so is an empty folder whose target a planned move
// put in place, but not one whose target is a file a planned move or copy put there (also
// at a path that held a planned folder before it was moved away).
func TestMoveIntoCopyIntoDryRunTargetsMatchRealRun(t *testing.T) {
	run := func(dry bool) ([]string, Stats) {
		lib, out := t.TempDir(), t.TempDir()
		writeFile(t, filepath.Join(lib, "a", "cover.jpg"), "a", 0o644)
		writeFile(t, filepath.Join(lib, "b", "cover.jpg"), "b", 0o644)
		writeFile(t, filepath.Join(lib, "c", "song.mp3"), "c", 0o644)
		writeFile(t, filepath.Join(lib, "Cat", "f.txt"), "f", 0o644)
		writeFile(t, filepath.Join(out, "Cat", "Old", "song.txt"), "old", 0o644)
		writeFile(t, filepath.Join(lib, "x", "Song", "song.txt"), "x", 0o644)
		writeFile(t, filepath.Join(lib, "d", "f.mp3"), "d", 0o644)
		writeFile(t, filepath.Join(lib, "g", "song.txt"), "g", 0o644)
		for _, d := range []string{"y/Song", "e/Empty", "k/Empty"} {
			if err := os.MkdirAll(filepath.Join(lib, d), 0o755); err != nil {
				t.Fatal(err)
			}
		}
		before := snapshot(t, lib)

		ops := New(dry)
		var got []string
		add := func(dst string, err error) {
			t.Helper()
			if err != nil {
				t.Fatalf("dry=%v: %v", dry, err)
			}
			rel, _ := filepath.Rel(out, dst)
			got = append(got, filepath.ToSlash(rel))
		}
		add(ops.MoveInto(filepath.Join(lib, "a", "cover.jpg"), out, "S/cover.jpg"))
		add(ops.MoveInto(filepath.Join(lib, "b", "cover.jpg"), out, "S/cover.jpg"))
		add(ops.CopyInto(filepath.Join(lib, "c", "song.mp3"), out, "O/song.mp3"))
		add(ops.CopyInto(filepath.Join(lib, "c", "song.mp3"), out, "O/song.mp3"))
		// Cat is empty once f.txt is moved away: merged into the existing out/Cat.
		add(ops.MoveInto(filepath.Join(lib, "Cat", "f.txt"), out, "extra/f.txt"))
		add(ops.MoveInto(filepath.Join(lib, "Cat"), out, "Cat"))
		// The empty y/Song is merged into the folder x/Song was just moved to.
		add(ops.MoveInto(filepath.Join(lib, "x", "Song"), out, "New"))
		add(ops.MoveInto(filepath.Join(lib, "y", "Song"), out, "New"))
		// An empty folder is not merged onto the file a planned move put at its target.
		add(ops.MoveInto(filepath.Join(lib, "d", "f.mp3"), out, "F"))
		add(ops.MoveInto(filepath.Join(lib, "e", "Empty"), out, "F"))
		// Nor onto the file a planned copy put where a planned folder was moved away from.
		add(ops.MoveInto(filepath.Join(lib, "g"), out, "G"))
		if err := ops.MoveTo(filepath.Join(out, "G"), filepath.Join(out, "H")); err != nil {
			t.Fatalf("dry=%v: %v", dry, err)
		}
		add(ops.CopyInto(filepath.Join(lib, "c", "song.mp3"), out, "G"))
		add(ops.MoveInto(filepath.Join(lib, "k", "Empty"), out, "G"))
		if dry {
			if after := snapshot(t, lib); strings.Join(after, "\n") != strings.Join(before, "\n") {
				t.Errorf("library changed in dry-run: %v", after)
			}
		}
		return got, ops.Stats()
	}
	realRun, realStats := run(false)
	want := []string{"S/cover.jpg", "S/cover (2).jpg", "O/song.mp3", "O/song (2).mp3",
		"extra/f.txt", "Cat", "New", "New", "F", "F (2)", "G", "G", "G (2)"}
	if strings.Join(realRun, "|") != strings.Join(want, "|") {
		t.Fatalf("real run targets = %q, want %q", realRun, want)
	}
	dry, dryStats := run(true)
	if strings.Join(dry, "|") != strings.Join(realRun, "|") {
		t.Errorf("dry run targets = %q, real run %q", dry, realRun)
	}
	if dryStats != realStats {
		t.Errorf("dry run stats %+v, real run %+v", dryStats, realStats)
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
	if testutil.Exists(src) {
		t.Error("source still exists")
	}
	if got := testutil.ReadFile(t, dst); got != "img" {
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
		{"dotfile", false, ".DS_Store", []string{".DS_Store", ".DS_Store (2)", ".DS_Store (3)"}},
		{"hidden file with extension", false, "._song.mp3", []string{"._song.mp3", "._song (2).mp3", "._song (3).mp3"}},
		{"double extension", false, "archive.tar.gz", []string{"archive.tar.gz", "archive.tar (2).gz", "archive.tar (3).gz"}},
		{"hidden folder", true, ".Song", []string{".Song", ".Song (2)", ".Song (3)"}},
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
				if got := testutil.ReadFile(t, content); got != fmt.Sprint(i) {
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
	names := testutil.DirNames(t, dir)
	if len(names) != n {
		t.Fatalf("got %d files, want %d: %v", len(names), n, names)
	}
	var contents []string
	for _, name := range names {
		contents = append(contents, testutil.ReadFile(t, filepath.Join(dir, name)))
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
		if testutil.ReadFile(t, a) != "a" || testutil.ReadFile(t, b) != "b" {
			t.Errorf("dry=%v: files changed after refused rename", dry)
		}
	}

	ops := New(false)
	if err := ops.Rename(a, c); err != nil {
		t.Fatal(err)
	}
	if testutil.Exists(a) || testutil.ReadFile(t, c) != "a" {
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
	if testutil.Exists(tmp) || testutil.ReadFile(t, dst) != "new" {
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
	if testutil.Exists(path) {
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
	if st.Mode().Perm() != 0o640 || !st.ModTime().Equal(mt) || testutil.ReadFile(t, dst) != "img" {
		t.Errorf("copy: mode %v mtime %v content %q", st.Mode().Perm(), st.ModTime(), testutil.ReadFile(t, dst))
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
	if testutil.ReadFile(t, dst) != "b" {
		t.Error("existing target modified")
	}
}

func TestCopyInto(t *testing.T) {
	lib, out := t.TempDir(), t.TempDir()
	src := filepath.Join(lib, "Cat", "Song", "song.mp3")
	// 0666 is reduced by any usual umask, so copying the mode must be explicit.
	writeFile(t, src, "audio", 0o666)
	mt := time.Date(2019, 3, 4, 5, 6, 7, 0, time.UTC)
	if err := os.Chtimes(src, mt, mt); err != nil {
		t.Fatal(err)
	}
	rel := filepath.Join("Cat", "Song", "song.mp3")
	before := snapshot(t, lib)

	ops := New(false)
	dst, err := ops.CopyInto(src, filepath.Join(out, "originals"), rel)
	if err != nil {
		t.Fatal(err)
	}
	if want := filepath.Join(out, "originals", rel); dst != want {
		t.Errorf("target = %q, want %q", dst, want)
	}
	st, err := os.Stat(dst)
	if err != nil {
		t.Fatal(err)
	}
	if testutil.ReadFile(t, dst) != "audio" || st.Mode().Perm() != 0o666 || !st.ModTime().Equal(mt) {
		t.Errorf("copy: content %q mode %v mtime %v; want %q %v %v", testutil.ReadFile(t, dst), st.Mode().Perm(), st.ModTime(), "audio", os.FileMode(0o666), mt)
	}
	if after := snapshot(t, lib); strings.Join(after, "\n") != strings.Join(before, "\n") {
		t.Errorf("CopyInto changed the source:\nbefore %v\nafter  %v", before, after)
	}

	// A second backup of the same file must not overwrite the first one.
	writeFile(t, src, "audio v2", 0o666)
	dst2, err := ops.CopyInto(src, filepath.Join(out, "originals"), rel)
	if err != nil {
		t.Fatal(err)
	}
	if want := filepath.Join(out, "originals", "Cat", "Song", "song (2).mp3"); dst2 != want {
		t.Errorf("second target = %q, want %q", dst2, want)
	}
	if testutil.ReadFile(t, dst) != "audio" || testutil.ReadFile(t, dst2) != "audio v2" {
		t.Errorf("contents = %q, %q", testutil.ReadFile(t, dst), testutil.ReadFile(t, dst2))
	}
	if s := ops.Stats(); s != (Stats{Copied: 2}) {
		t.Errorf("Stats = %+v, want Copied=2", s)
	}
}

func TestCopyIntoDryRun(t *testing.T) {
	lib, out := t.TempDir(), t.TempDir()
	src := filepath.Join(lib, "Song", "song.mp3")
	writeFile(t, src, "audio", 0o644)
	writeFile(t, filepath.Join(out, "Song", "song.mp3"), "older backup", 0o644)
	beforeLib, beforeOut := snapshot(t, lib), snapshot(t, out)

	ops := New(true)
	dst, err := ops.CopyInto(src, out, filepath.Join("Song", "song.mp3"))
	if err != nil {
		t.Fatal(err)
	}
	// The planned target honours the collision suffix, but nothing is created.
	if want := filepath.Join(out, "Song", "song (2).mp3"); dst != want {
		t.Errorf("target = %q, want %q", dst, want)
	}
	if after := snapshot(t, lib); strings.Join(after, "\n") != strings.Join(beforeLib, "\n") {
		t.Errorf("library changed in dry-run: %v", after)
	}
	if after := snapshot(t, out); strings.Join(after, "\n") != strings.Join(beforeOut, "\n") {
		t.Errorf("out dir changed in dry-run: %v", after)
	}
	if s := ops.Stats(); s != (Stats{Copied: 1}) {
		t.Errorf("Stats = %+v, want Copied=1", s)
	}
}

func TestCopyIntoRejectsNonRegularSource(t *testing.T) {
	lib := t.TempDir()
	folder := filepath.Join(lib, "Song")
	writeFile(t, filepath.Join(folder, "song.txt"), "x", 0o644)
	for _, dry := range []bool{false, true} {
		out := t.TempDir()
		ops := New(dry)
		if _, err := ops.CopyInto(folder, out, "Song"); err == nil {
			t.Errorf("dry=%v: copying a folder succeeded", dry)
		}
		if _, err := ops.CopyInto(filepath.Join(lib, "missing.mp3"), out, "missing.mp3"); err == nil {
			t.Errorf("dry=%v: copying a missing file succeeded", dry)
		}
		if s := ops.Stats(); s != (Stats{Failed: 2}) {
			t.Errorf("dry=%v: Stats = %+v, want Failed=2", dry, s)
		}
		if names := testutil.DirNames(t, out); len(names) != 0 {
			t.Errorf("dry=%v: out dir changed: %v", dry, names)
		}
	}
}

func TestFailOnce(t *testing.T) {
	ops := New(false)
	ops.FailOnce("unreadable:/a")
	ops.FailOnce("unreadable:/a")
	ops.FailOnce("unreadable:/b")
	ops.Fail() // plain failures are independent of the keys
	ops.FailOnce("unreadable:/a")
	if s := ops.Stats(); s.Failed != 3 {
		t.Errorf("Failed = %d, want 3 (two keys + one Fail)", s.Failed)
	}
}

// TestFailUnreadable pins the shared key: FailUnreadable, SkipUnreadable and a hand-written
// FailOnce("unreadable:"+path) (as in tests elsewhere) count one path once, and
// SkipUnreadable counts the path of a *fs.PathError, not the folder.
func TestFailUnreadable(t *testing.T) {
	ops := New(false)
	ops.FailOnce("unreadable:/a")
	ops.FailUnreadable("/a")
	ops.SkipUnreadable("/a", errors.New("boom"))
	if s := ops.Stats(); s.Failed != 1 {
		t.Errorf("Failed = %d, want 1 (one key)", s.Failed)
	}
	txtErr := &fs.PathError{Op: "open", Path: "/a/song.txt", Err: fs.ErrPermission}
	ops.SkipUnreadable("/a", fmt.Errorf("wrapped: %w", txtErr))
	ops.FailUnreadable("/a/song.txt")
	if s := ops.Stats(); s.Failed != 2 {
		t.Errorf("Failed = %d, want 2 (the txt path is its own key)", s.Failed)
	}
	if got := ErrPath(errors.New("plain"), "/x"); got != "/x" {
		t.Errorf("ErrPath(plain) = %q, want the fallback", got)
	}
}

func TestFailOnceConcurrent(t *testing.T) {
	const keys, callers = 5, 40
	ops := New(true)
	var wg sync.WaitGroup
	for i := range callers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			ops.FailOnce(fmt.Sprint("key", i%keys))
		}()
	}
	wg.Wait()
	if s := ops.Stats(); s.Failed != keys {
		t.Errorf("Failed = %d, want %d", s.Failed, keys)
	}
}

func TestMoveIntoMergesEmptyFolder(t *testing.T) {
	lib, out := t.TempDir(), t.TempDir()
	src := filepath.Join(lib, "Category")
	if err := os.MkdirAll(src, 0o755); err != nil {
		t.Fatal(err)
	}
	want := filepath.Join(out, "invalid", "Category")
	writeFile(t, filepath.Join(want, "Song", "song.txt"), "moved earlier", 0o644)

	ops := New(false)
	dst, err := ops.MoveInto(src, filepath.Join(out, "invalid"), "Category")
	if err != nil {
		t.Fatal(err)
	}
	if dst != want {
		t.Errorf("target = %q, want %q", dst, want)
	}
	if testutil.Exists(src) {
		t.Error("empty source folder still exists")
	}
	if names := testutil.DirNames(t, filepath.Join(out, "invalid")); strings.Join(names, ",") != "Category" {
		t.Errorf("invalid/ = %v, want only Category (no \"Category (2)\")", names)
	}
	if got := testutil.ReadFile(t, filepath.Join(want, "Song", "song.txt")); got != "moved earlier" {
		t.Errorf("existing target content changed: %q", got)
	}
	if s := ops.Stats(); s != (Stats{Moved: 1}) {
		t.Errorf("Stats = %+v, want Moved=1", s)
	}
	if ops.WouldBeMoved(src) {
		t.Error("WouldBeMoved must be false outside dry-run mode")
	}
}

func TestMoveIntoMergesEmptyFolderDryRun(t *testing.T) {
	lib, out := t.TempDir(), t.TempDir()
	src := filepath.Join(lib, "Category")
	if err := os.MkdirAll(src, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(out, "Category"), 0o755); err != nil {
		t.Fatal(err)
	}
	before := snapshot(t, lib)

	ops := New(true)
	dst, err := ops.MoveInto(src, out, "Category")
	if err != nil {
		t.Fatal(err)
	}
	if want := filepath.Join(out, "Category"); dst != want {
		t.Errorf("target = %q, want %q", dst, want)
	}
	if after := snapshot(t, lib); strings.Join(after, "\n") != strings.Join(before, "\n") {
		t.Errorf("library changed in dry-run: %v", after)
	}
	if !ops.WouldBeMoved(src) || !ops.WouldBeMoved(filepath.Join(src, "child")) {
		t.Error("the merged folder (and anything below it) must be planned as moved")
	}
	if ops.WouldBeMoved(lib) || ops.WouldBeMoved(src+" 2") {
		t.Error("parents and siblings with a common prefix must not be planned as moved")
	}
	if s := ops.Stats(); s != (Stats{Moved: 1}) {
		t.Errorf("Stats = %+v, want Moved=1", s)
	}
}

func TestMoveIntoEmptyFolderWithoutMergeTarget(t *testing.T) {
	out := t.TempDir()
	// An empty folder without an existing target is moved normally.
	src := filepath.Join(t.TempDir(), "Empty")
	if err := os.MkdirAll(src, 0o755); err != nil {
		t.Fatal(err)
	}
	ops := New(false)
	dst, err := ops.MoveInto(src, out, "Empty")
	if err != nil {
		t.Fatal(err)
	}
	if st, err := os.Stat(dst); err != nil || !st.IsDir() || testutil.Exists(src) || dst != filepath.Join(out, "Empty") {
		t.Errorf("empty folder not moved to %q (dst %q, err %v)", filepath.Join(out, "Empty"), dst, err)
	}

	// A file in the way is not a folder to merge into: the suffix is used.
	src2 := filepath.Join(t.TempDir(), "Blocked")
	if err := os.MkdirAll(src2, 0o755); err != nil {
		t.Fatal(err)
	}
	writeFile(t, filepath.Join(out, "Blocked"), "file", 0o644)
	dst2, err := ops.MoveInto(src2, out, "Blocked")
	if err != nil {
		t.Fatal(err)
	}
	if want := filepath.Join(out, "Blocked (2)"); dst2 != want {
		t.Errorf("target = %q, want %q", dst2, want)
	}
}

func TestMoveIntoNonEmptyFolderNotMerged(t *testing.T) {
	for _, dry := range []bool{false, true} {
		lib, out := t.TempDir(), t.TempDir()
		src := filepath.Join(lib, "Song")
		writeFile(t, filepath.Join(src, "song.txt"), "new", 0o644)
		writeFile(t, filepath.Join(out, "Song", "song.txt"), "old", 0o644)

		ops := New(dry)
		dst, err := ops.MoveInto(src, out, "Song")
		if err != nil {
			t.Fatal(err)
		}
		if want := filepath.Join(out, "Song (2)"); dst != want {
			t.Errorf("dry=%v: target = %q, want %q", dry, dst, want)
		}
		if got := testutil.ReadFile(t, filepath.Join(out, "Song", "song.txt")); got != "old" {
			t.Errorf("dry=%v: existing target changed: %q", dry, got)
		}
		if dry {
			if !testutil.Exists(src) || testutil.Exists(dst) || !ops.WouldBeMoved(src) {
				t.Errorf("dry-run: source must stay, target must not exist, source must be planned")
			}
		} else if testutil.Exists(src) || testutil.ReadFile(t, filepath.Join(dst, "song.txt")) != "new" {
			t.Error("folder not moved to the suffixed target")
		}
	}
}

func TestReplaceDryRunLeavesTempFile(t *testing.T) {
	dir := t.TempDir()
	dst := filepath.Join(dir, "song.mp3")
	writeFile(t, dst, "old", 0o644)
	tmp, err := TempFileNear(dst, ".mp3")
	if err != nil {
		t.Fatal(err)
	}
	writeFile(t, tmp, "new", 0o644)
	before := snapshot(t, dir)

	ops := New(true)
	if err := ops.Replace(tmp, dst); err != nil {
		t.Fatal(err)
	}
	if after := snapshot(t, dir); strings.Join(after, "\n") != strings.Join(before, "\n") {
		t.Errorf("dry-run Replace changed files:\nbefore %v\nafter  %v", before, after)
	}
	if s := ops.Stats(); s != (Stats{Replaced: 1}) {
		t.Errorf("Stats = %+v, want Replaced=1", s)
	}
}

func TestPlanReplace(t *testing.T) {
	ops := New(true)
	ops.PlanReplace("/lib/Song/song.mp3", "re-encode")
	if s := ops.Stats(); s != (Stats{Replaced: 1}) {
		t.Errorf("Stats = %+v, want Replaced=1", s)
	}
}

// TestReadFileSeesPlannedWrites: in dry-run mode, ReadFile returns the content of a
// planned write (and of the latest one, if a file is planned twice), while the disk stays
// unchanged. A write that would not change the file is not recorded.
func TestReadFileSeesPlannedWrites(t *testing.T) {
	dir := t.TempDir()
	changed, same, other := filepath.Join(dir, "a.txt"), filepath.Join(dir, "b.txt"), filepath.Join(dir, "c.txt")
	for _, p := range []string{changed, same, other} {
		writeFile(t, p, "old", 0o644)
	}
	before := snapshot(t, dir)
	o := New(true)

	if ok, err := o.WriteFileAtomic(changed, []byte("new")); err != nil || !ok {
		t.Fatalf("WriteFileAtomic = %v, %v", ok, err)
	}
	if ok, err := o.WriteFileAtomic(same, []byte("old")); err != nil || ok {
		t.Fatalf("unchanged WriteFileAtomic = %v, %v", ok, err)
	}
	for path, want := range map[string]string{changed: "new", same: "old", other: "old"} {
		if got, err := o.ReadFile(path); err != nil || string(got) != want {
			t.Errorf("ReadFile(%s) = %q, %v; want %q", filepath.Base(path), got, err, want)
		}
	}
	if _, ok := o.plannedWrites[filepath.Clean(same)]; ok {
		t.Error("a write that changes nothing was recorded")
	}

	// A second planned write is compared with the first one, not with the disk.
	if ok, err := o.WriteFileAtomic(changed, []byte("new")); err != nil || ok {
		t.Errorf("repeating the planned write = %v, %v; want unchanged", ok, err)
	}
	if ok, err := o.WriteFileAtomic(changed, []byte("newer")); err != nil || !ok {
		t.Errorf("second planned write = %v, %v", ok, err)
	}
	if got, _ := o.ReadFile(changed); string(got) != "newer" {
		t.Errorf("ReadFile after the second write = %q, want %q", got, "newer")
	}
	// The returned data is a copy: changing it does not change the plan.
	got, _ := o.ReadFile(changed)
	got[0] = 'X'
	if again, _ := o.ReadFile(changed); string(again) != "newer" {
		t.Errorf("plan changed through the returned slice: %q", again)
	}
	if after := snapshot(t, dir); strings.Join(after, "\n") != strings.Join(before, "\n") {
		t.Error("dry run modified the files")
	}
	if st := o.Stats(); st.Written != 2 {
		t.Errorf("planned writes = %d, want 2", st.Written)
	}
}

// TestReadFileMovedAway: in dry-run mode a file that a planned move takes away (itself or
// with its folder) reads as not existing, even if a write was planned before.
func TestReadFileMovedAway(t *testing.T) {
	dir := t.TempDir()
	song := filepath.Join(dir, "lib", "Song")
	txt, other := filepath.Join(song, "song.txt"), filepath.Join(dir, "lib", "Other", "song.txt")
	writeFile(t, txt, "old", 0o644)
	writeFile(t, other, "other", 0o644)
	o := New(true)
	if _, err := o.WriteFileAtomic(txt, []byte("new")); err != nil {
		t.Fatal(err)
	}
	if _, err := o.MoveInto(song, filepath.Join(dir, "out"), "Song"); err != nil {
		t.Fatal(err)
	}
	if _, err := o.ReadFile(txt); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("ReadFile of a file in a moved folder: err = %v, want fs.ErrNotExist", err)
	}
	if got, err := o.ReadFile(other); err != nil || string(got) != "other" {
		t.Errorf("ReadFile(other) = %q, %v", got, err)
	}
	if !testutil.Exists(txt) {
		t.Error("dry run moved the file")
	}
}

// TestReadFileRealRun: outside dry-run mode (and for a nil *Ops) ReadFile reads the disk.
func TestReadFileRealRun(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "a.txt")
	writeFile(t, path, "old", 0o644)
	o := New(false)
	if _, err := o.WriteFileAtomic(path, []byte("new")); err != nil {
		t.Fatal(err)
	}
	if len(o.plannedWrites) != 0 {
		t.Error("a real write was recorded as planned")
	}
	writeFile(t, path, "changed on disk", 0o644)
	for name, ops := range map[string]*Ops{"real": o, "nil": nil} {
		if got, err := ops.ReadFile(path); err != nil || string(got) != "changed on disk" {
			t.Errorf("%s: ReadFile = %q, %v", name, got, err)
		}
		if _, err := ops.ReadFile(filepath.Join(dir, "missing")); !errors.Is(err, fs.ErrNotExist) {
			t.Errorf("%s: ReadFile(missing) err = %v", name, err)
		}
		if ops.WouldBeMoved(path) {
			t.Errorf("%s: WouldBeMoved = true", name)
		}
	}
}

// TestReadFileConcurrent plans writes and reads them back from many goroutines (run with
// -race).
func TestReadFileConcurrent(t *testing.T) {
	dir := t.TempDir()
	o := New(true)
	var wg sync.WaitGroup
	for i := range 50 {
		path := filepath.Join(dir, fmt.Sprintf("%d.txt", i))
		writeFile(t, path, "old", 0o644)
		wg.Add(1)
		go func() {
			defer wg.Done()
			want := fmt.Sprintf("new %d", i)
			if _, err := o.WriteFileAtomic(path, []byte(want)); err != nil {
				t.Error(err)
				return
			}
			if got, err := o.ReadFile(path); err != nil || string(got) != want {
				t.Errorf("ReadFile = %q, %v; want %q", got, err, want)
			}
			_ = o.WouldBeMoved(path)
		}()
	}
	wg.Wait()
}

func TestMoveTo(t *testing.T) {
	root := t.TempDir()
	src := filepath.Join(root, "src", "Song")
	writeFile(t, filepath.Join(src, "song.txt"), "x", 0o640)
	dst := filepath.Join(root, "lib", "A", "Artist - Title") // parents do not exist yet

	ops := New(false)
	if err := ops.MoveTo(src, dst); err != nil {
		t.Fatal(err)
	}
	if testutil.Exists(src) || testutil.ReadFile(t, filepath.Join(dst, "song.txt")) != "x" {
		t.Errorf("not moved: src exists %v, dst %v", testutil.Exists(src), testutil.DirNames(t, dst))
	}
	if st, _ := os.Stat(filepath.Join(dst, "song.txt")); st.Mode().Perm() != 0o640 {
		t.Errorf("mode = %v", st.Mode())
	}
	if s := ops.Stats(); s != (Stats{Moved: 1}) {
		t.Errorf("Stats = %+v", s)
	}
	// A single file, back to where it came from.
	file := filepath.Join(dst, "song.txt")
	back := filepath.Join(root, "src", "Song", "song.txt")
	if err := ops.MoveTo(file, back); err != nil || testutil.ReadFile(t, back) != "x" {
		t.Errorf("MoveTo file = %v", err)
	}
}

func TestMoveToRefusesExistingTarget(t *testing.T) {
	for _, tt := range []struct {
		name   string
		create func(t *testing.T, dst string)
	}{
		{"file", func(t *testing.T, dst string) { writeFile(t, dst, "old", 0o644) }},
		{"empty folder", func(t *testing.T, dst string) {
			if err := os.MkdirAll(dst, 0o755); err != nil {
				t.Fatal(err)
			}
		}},
		{"dangling symlink", func(t *testing.T, dst string) {
			if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.Symlink(filepath.Join(filepath.Dir(dst), "nowhere"), dst); err != nil {
				t.Fatal(err)
			}
		}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			for _, dry := range []bool{false, true} {
				root := t.TempDir()
				src := filepath.Join(root, "Song")
				writeFile(t, filepath.Join(src, "song.txt"), "new", 0o644)
				dst := filepath.Join(root, "lib", "Song")
				tt.create(t, dst)
				before := snapshot(t, root)
				ops := New(dry)
				if err := ops.MoveTo(src, dst); err == nil {
					t.Fatalf("dry=%v: MoveTo onto an existing %s succeeded", dry, tt.name)
				}
				if s := ops.Stats(); s != (Stats{Failed: 1}) {
					t.Errorf("dry=%v: Stats = %+v", dry, s)
				}
				if after := snapshot(t, root); strings.Join(after, "\n") != strings.Join(before, "\n") {
					t.Errorf("dry=%v: tree changed:\n%v\nwant\n%v", dry, after, before)
				}
				if ops.WouldBeMoved(src) {
					t.Errorf("dry=%v: failed move recorded as planned", dry)
				}
			}
		})
	}
}

func TestMoveToMissingSource(t *testing.T) {
	for _, dry := range []bool{false, true} {
		root := t.TempDir()
		ops := New(dry)
		if err := ops.MoveTo(filepath.Join(root, "missing"), filepath.Join(root, "dst")); !errors.Is(err, fs.ErrNotExist) {
			t.Errorf("dry=%v: err = %v, want ErrNotExist", dry, err)
		}
		if s := ops.Stats(); s != (Stats{Failed: 1}) {
			t.Errorf("dry=%v: Stats = %+v", dry, s)
		}
		if testutil.Exists(filepath.Join(root, "dst")) {
			t.Errorf("dry=%v: dst created", dry)
		}
	}
}

func TestMoveToReservedTarget(t *testing.T) {
	root := t.TempDir()
	src := filepath.Join(root, "Song")
	writeFile(t, filepath.Join(src, "song.txt"), "x", 0o644)
	dst := filepath.Join(root, "lib", "Song")
	ops := New(false)
	ops.reserved[dst] = true // a concurrent MoveInto/MoveTo picked this name
	if err := ops.MoveTo(src, dst); err == nil || !testutil.Exists(src) || testutil.Exists(dst) {
		t.Errorf("MoveTo onto a reserved target = %v", err)
	}
	ops.release(dst)
	if err := ops.MoveTo(src, dst); err != nil {
		t.Errorf("MoveTo after release: %v", err)
	}
	if len(ops.reserved) != 0 {
		t.Errorf("reservations left: %v", ops.reserved)
	}
}

// Files, not folders: renaming a file onto an existing one would silently replace it.
func TestMoveToConcurrentSameTarget(t *testing.T) {
	const n = 50
	root := t.TempDir()
	dst := filepath.Join(root, "lib", "A", "song.txt")
	srcs := make([]string, n)
	for i := range srcs {
		srcs[i] = filepath.Join(root, fmt.Sprint("src", i), "song.txt")
		writeFile(t, srcs[i], fmt.Sprint(i), 0o644)
	}
	ops := New(false)
	var wg sync.WaitGroup
	var mu sync.Mutex
	won := -1
	start := make(chan struct{})
	for i := range srcs {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			if err := ops.MoveTo(srcs[i], dst); err == nil {
				mu.Lock()
				won = i
				mu.Unlock()
			}
		}()
	}
	close(start)
	wg.Wait()
	if s := ops.Stats(); s.Moved != 1 || s.Failed != n-1 {
		t.Errorf("Stats = %+v, want exactly one move", s)
	}
	if won < 0 || testutil.ReadFile(t, dst) != fmt.Sprint(won) || testutil.Exists(srcs[won]) {
		t.Errorf("winner %d not at the target", won)
	}
	for i, src := range srcs {
		if i != won && !testutil.Exists(src) {
			t.Errorf("source %d is gone", i)
		}
	}
}

// A dry run plans what a real run does: a planned target is taken, a planned source is
// gone, and a path moved away can be refilled (a cycle broken by a temporary name).
func TestMoveToDryRunMatchesRealRun(t *testing.T) {
	setup := func(t *testing.T) string {
		root := t.TempDir()
		writeFile(t, filepath.Join(root, "lib", "A", "a.txt"), "a", 0o644)
		writeFile(t, filepath.Join(root, "lib", "B", "b.txt"), "b", 0o644)
		writeFile(t, filepath.Join(root, "new", "C", "c.txt"), "c", 0o644)
		return root
	}
	type step struct {
		src, dst string
		ok       bool
	}
	steps := []step{
		{"lib/A", "lib/.tmp", true}, // break the cycle A <-> B
		{"lib/B", "lib/A", true},    // A is free again
		{"lib/.tmp", "lib/B", true}, // the temporary name exists now
		{"lib/.tmp", "lib/X", false},
		{"new/C", "lib/A", false}, // refilled
		{"new/C", "lib/Z/C", true},
		{"new/C", "lib/Y", false}, // already moved away
		{"new/D", "lib/D", false}, // never existed
		{"lib/A/a.txt", "lib/a.txt", false},
	}
	run := func(root string, dry bool) (Stats, []bool) {
		ops := New(dry)
		var oks []bool
		for _, s := range steps {
			err := ops.MoveTo(filepath.Join(root, s.src), filepath.Join(root, s.dst))
			oks = append(oks, err == nil)
		}
		return ops.Stats(), oks
	}

	realRoot := setup(t)
	realStats, realOK := run(realRoot, false)
	for i, s := range steps {
		if realOK[i] != s.ok {
			t.Errorf("real run: %s -> %s ok = %v, want %v", s.src, s.dst, realOK[i], s.ok)
		}
	}
	if testutil.ReadFile(t, filepath.Join(realRoot, "lib", "A", "b.txt")) != "b" || testutil.ReadFile(t, filepath.Join(realRoot, "lib", "B", "a.txt")) != "a" {
		t.Errorf("real run: cycle not resolved")
	}

	dryRoot := setup(t)
	before := snapshot(t, dryRoot)
	dryStats, dryOK := run(dryRoot, true)
	if strings.Join(snapshot(t, dryRoot), "\n") != strings.Join(before, "\n") {
		t.Errorf("dry run changed the tree")
	}
	if fmt.Sprint(dryOK) != fmt.Sprint(realOK) || dryStats != realStats {
		t.Errorf("dry run = %v %+v, real run = %v %+v", dryOK, dryStats, realOK, realStats)
	}
}

func TestMoveToDryRunMarksSourceMoved(t *testing.T) {
	root := t.TempDir()
	src := filepath.Join(root, "Song")
	writeFile(t, filepath.Join(src, "song.txt"), "x", 0o644)
	ops := New(true)
	if err := ops.MoveTo(src, filepath.Join(root, "lib", "Song")); err != nil {
		t.Fatal(err)
	}
	if !ops.WouldBeMoved(filepath.Join(src, "song.txt")) {
		t.Error("source not planned as moved away")
	}
	if _, err := ops.ReadFile(filepath.Join(src, "song.txt")); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("ReadFile of a planned-away txt = %v", err)
	}
	if testutil.Exists(filepath.Join(root, "lib")) {
		t.Error("dry run created the target parent")
	}
}

func TestAppendJournal(t *testing.T) {
	path := filepath.Join(t.TempDir(), "out", "journal.jsonl")

	dry := New(true)
	if err := dry.AppendJournal(path, []byte(`{"op":"run"}`)); err != nil || testutil.Exists(path) || testutil.Exists(filepath.Dir(path)) {
		t.Errorf("dry run wrote a journal: %v", err)
	}

	ops := New(false)
	for _, line := range []string{`{"op":"run"}`, "{\"op\":\"move\"}\n", `{"op":"undone"}`} {
		if err := ops.AppendJournal(path, []byte(line)); err != nil {
			t.Fatal(err)
		}
	}
	if got, want := testutil.ReadFile(t, path), "{\"op\":\"run\"}\n{\"op\":\"move\"}\n{\"op\":\"undone\"}\n"; got != want {
		t.Errorf("journal = %q, want %q", got, want)
	}
	// A second Ops (the next run) appends instead of truncating.
	if err := New(false).AppendJournal(path, []byte("x")); err != nil || !strings.HasSuffix(testutil.ReadFile(t, path), "}\nx\n") {
		t.Errorf("second run: %v, %q", err, testutil.ReadFile(t, path))
	}
	if st, _ := os.Stat(path); st.Mode().Perm() != 0o644&^currentUmask(t) {
		t.Errorf("mode = %v", st.Mode())
	}
	if s := ops.Stats(); s != (Stats{}) {
		t.Errorf("Stats = %+v, journal entries are no library operations", s)
	}

	// The path is a folder: a counted failure.
	bad := t.TempDir()
	if err := ops.AppendJournal(bad, []byte("x")); err == nil {
		t.Error("appending to a folder succeeded")
	}
	if s := ops.Stats(); s.Failed != 1 {
		t.Errorf("Stats = %+v, want 1 failure", s)
	}
}

// currentUmask returns the process umask (files are created with 0644 &^ umask).
func currentUmask(t *testing.T) os.FileMode {
	t.Helper()
	p := filepath.Join(t.TempDir(), "probe")
	if err := os.WriteFile(p, nil, 0o777); err != nil {
		t.Fatal(err)
	}
	st, err := os.Stat(p)
	if err != nil {
		t.Fatal(err)
	}
	return 0o777 &^ st.Mode().Perm()
}

// TestRemoveEmptyDir: the undo of organize removes the folders a run created, but only
// while they are empty; a dry run plans the same as a real run (after planned moves).
func TestRemoveEmptyDir(t *testing.T) {
	setup := func(t *testing.T) string {
		root := t.TempDir()
		writeFile(t, filepath.Join(root, "lib", "B", "Song", "song.txt"), "x", 0o644)
		writeFile(t, filepath.Join(root, "lib", "H", ".DS_Store"), "x", 0o644)
		writeFile(t, filepath.Join(root, "lib", "file"), "x", 0o644)
		if err := os.MkdirAll(filepath.Join(root, "lib", "E"), 0o755); err != nil {
			t.Fatal(err)
		}
		return root
	}
	type step struct {
		move     [2]string // MoveTo before the removal, if set
		dir      string
		want     bool
		wantGone bool
	}
	steps := []step{
		{dir: "lib/E", want: true, wantGone: true},
		{dir: "lib/E", want: false, wantGone: true}, // gone: no error
		{dir: "lib/H", want: false},                 // a hidden file is data, too
		{dir: "lib/file", want: false},              // not a folder
		{dir: "lib/B", want: false},                 // not empty
		{move: [2]string{"lib/B/Song", "new/Song"}, dir: "lib/B", want: true, wantGone: true},
		{move: [2]string{"new/Song", "lib/B"}, dir: "lib/B", want: false}, // refilled by a move
	}
	run := func(root string, dry bool) ([]bool, Stats) {
		ops := New(dry)
		var got []bool
		for _, s := range steps {
			if s.move[0] != "" {
				if err := ops.MoveTo(filepath.Join(root, s.move[0]), filepath.Join(root, s.move[1])); err != nil {
					t.Fatal(err)
				}
			}
			removed, err := ops.RemoveEmptyDir(filepath.Join(root, s.dir))
			if err != nil {
				t.Fatalf("RemoveEmptyDir(%s): %v", s.dir, err)
			}
			got = append(got, removed)
		}
		return got, ops.Stats()
	}
	realRoot := setup(t)
	realGot, realStats := run(realRoot, false)
	for i, s := range steps {
		if realGot[i] != s.want {
			t.Errorf("step %d (%s): removed = %v, want %v", i, s.dir, realGot[i], s.want)
		}
	}
	if _, err := os.Stat(filepath.Join(realRoot, "lib", "H", ".DS_Store")); err != nil {
		t.Errorf("hidden file: %v", err)
	}
	if testutil.ReadFile(t, filepath.Join(realRoot, "lib", "B", "song.txt")) != "x" {
		t.Errorf("refilled folder lost its content")
	}
	dryRoot := setup(t)
	before := snapshot(t, dryRoot)
	dryGot, dryStats := run(dryRoot, true)
	if strings.Join(snapshot(t, dryRoot), "\n") != strings.Join(before, "\n") {
		t.Errorf("dry run changed the tree")
	}
	if fmt.Sprint(dryGot) != fmt.Sprint(realGot) || dryStats != realStats {
		t.Errorf("dry run = %v %+v, real run = %v %+v", dryGot, dryStats, realGot, realStats)
	}
}
