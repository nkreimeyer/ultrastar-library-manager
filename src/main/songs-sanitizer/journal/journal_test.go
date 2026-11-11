package journal

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"testing"

	"github.com/nkreimeyer/ultrastar-songs-sanitizer/fsops"
	log "github.com/sirupsen/logrus"
)

func TestMain(m *testing.M) {
	log.SetOutput(io.Discard)
	os.Exit(m.Run())
}

func writeFile(t *testing.T, path string, data []byte) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, data, 0o644); err != nil {
		t.Fatal(err)
	}
}

func open(t *testing.T, path string) *Journal {
	t.Helper()
	j, err := Open(path)
	if err != nil {
		t.Fatalf("Open(%q): %v", path, err)
	}
	return j
}

func TestOpenMissingIsEmpty(t *testing.T) {
	dir := t.TempDir()
	jpath := filepath.Join(dir, "out", "media-journal.txt")
	media := filepath.Join(dir, "song.aac")
	writeFile(t, media, []byte("audio"))

	j := open(t, jpath)
	if j.Has("aac", media) {
		t.Error("empty journal has an entry")
	}
	// Nothing changed, so Save must not create the file.
	ops := fsops.New(false)
	if err := j.Save(ops); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(jpath); !os.IsNotExist(err) {
		t.Errorf("Save of an unchanged journal created %s (err %v)", jpath, err)
	}
}

func TestOpenUnreadable(t *testing.T) {
	// Only a missing journal is empty; any other error must not silently drop the entries.
	dir := t.TempDir()
	file := filepath.Join(dir, "file")
	writeFile(t, file, []byte("x"))
	for _, path := range []string{dir, filepath.Join(file, "journal.txt")} {
		if _, err := Open(path); err == nil {
			t.Errorf("Open(%q) succeeded", path)
		}
	}
}

func TestAddHas(t *testing.T) {
	dir := t.TempDir()
	media := filepath.Join(dir, "Song", "song.aac")
	writeFile(t, media, []byte("audio"))
	j := open(t, filepath.Join(dir, "journal.txt"))

	if err := j.Add("aac-normalized", media); err != nil {
		t.Fatal(err)
	}
	if !j.Has("aac-normalized", media) {
		t.Error("Has = false after Add")
	}
	if j.Has("avi-checked", media) {
		t.Error("entries must be separate per kind")
	}
	if err := j.Add("aac-normalized", filepath.Join(dir, "missing.aac")); err == nil {
		t.Error("Add of a missing file succeeded")
	}
	if j.Has("aac-normalized", filepath.Join(dir, "missing.aac")) {
		t.Error("Has = true for a missing file")
	}
}

func TestHasFalseAfterContentChange(t *testing.T) {
	dir := t.TempDir()
	media := filepath.Join(dir, "song.aac")
	writeFile(t, media, []byte("audio"))
	j := open(t, filepath.Join(dir, "journal.txt"))
	if err := j.Add("k", media); err != nil {
		t.Fatal(err)
	}
	for _, content := range []string{"AUDIO", "audio!", ""} {
		writeFile(t, media, []byte(content))
		if j.Has("k", media) {
			t.Errorf("Has = true after the content changed to %q", content)
		}
	}
}

func TestHasSurvivesRename(t *testing.T) {
	dir := t.TempDir()
	media := filepath.Join(dir, "Old Artist - Song", "song.aac")
	writeFile(t, media, []byte("audio"))
	j := open(t, filepath.Join(dir, "journal.txt"))
	if err := j.Add("k", media); err != nil {
		t.Fatal(err)
	}
	moved := filepath.Join(dir, "Category", "New Artist - Song", "Song.aac")
	if err := os.MkdirAll(filepath.Dir(moved), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(media, moved); err != nil {
		t.Fatal(err)
	}
	if !j.Has("k", moved) {
		t.Error("Has = false after the file was renamed/moved")
	}
}

func TestSave(t *testing.T) {
	dir := t.TempDir()
	jpath := filepath.Join(dir, "out", "media-journal.txt")
	var files []string
	for i := range 5 {
		f := filepath.Join(dir, fmt.Sprintf("song%d.aac", i))
		writeFile(t, f, []byte(fmt.Sprint("audio ", i)))
		files = append(files, f)
	}

	j := open(t, jpath)
	// Add in reverse order under two kinds, so sorting is observable.
	for i := len(files) - 1; i >= 0; i-- {
		kind := "b-kind"
		if i%2 == 0 {
			kind = "a-kind"
		}
		if err := j.Add(kind, files[i]); err != nil {
			t.Fatal(err)
		}
	}
	ops := fsops.New(true) // the journal is outside the library: written in dry-run mode too
	if err := j.Save(ops); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(jpath)
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimSuffix(string(data), "\n"), "\n")
	if !strings.HasPrefix(lines[0], "#") {
		t.Errorf("first line %q is not a comment header", lines[0])
	}
	entries := lines[1:]
	if len(entries) != len(files) {
		t.Fatalf("got %d entries, want %d:\n%s", len(entries), len(files), data)
	}
	if !sort.StringsAreSorted(entries) {
		t.Errorf("entries not sorted:\n%s", data)
	}
	for _, e := range entries {
		if kind, fp, ok := strings.Cut(e, "\t"); !ok || kind == "" || fp == "" {
			t.Errorf("malformed entry %q", e)
		}
	}

	// Not dirty any more: Save must not write again.
	if err := os.Remove(jpath); err != nil {
		t.Fatal(err)
	}
	if err := j.Save(ops); err != nil {
		t.Fatal(err)
	}
	// Adding an entry that is already present does not make the journal dirty either.
	if err := j.Add("a-kind", files[0]); err != nil {
		t.Fatal(err)
	}
	if err := j.Save(ops); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(jpath); !os.IsNotExist(err) {
		t.Errorf("Save of an unchanged journal rewrote it (err %v)", err)
	}
	if s := ops.Stats(); s.Failed != 0 {
		t.Errorf("Stats = %+v", s)
	}
}

func TestReloadAfterSave(t *testing.T) {
	dir := t.TempDir()
	jpath := filepath.Join(dir, "media-journal.txt")
	a, b := filepath.Join(dir, "a.aac"), filepath.Join(dir, "b.avi")
	writeFile(t, a, []byte("a"))
	writeFile(t, b, []byte("b"))

	j := open(t, jpath)
	if err := j.Add("aac", a); err != nil {
		t.Fatal(err)
	}
	if err := j.Add("avi", b); err != nil {
		t.Fatal(err)
	}
	if err := j.Save(fsops.New(false)); err != nil {
		t.Fatal(err)
	}

	j2 := open(t, jpath)
	if !j2.Has("aac", a) || !j2.Has("avi", b) {
		t.Error("entries lost after reload")
	}
	if j2.Has("avi", a) || j2.Has("aac", b) {
		t.Error("reloaded journal mixes up kinds")
	}
	// The reloaded journal keeps the old entries when a new one is added and saved.
	c := filepath.Join(dir, "c.aac")
	writeFile(t, c, []byte("c"))
	if err := j2.Add("aac", c); err != nil {
		t.Fatal(err)
	}
	if err := j2.Save(fsops.New(false)); err != nil {
		t.Fatal(err)
	}
	j3 := open(t, jpath)
	if !j3.Has("aac", a) || !j3.Has("avi", b) || !j3.Has("aac", c) {
		t.Error("entries lost after the second save")
	}
}

func TestSaveFailureKeepsDirty(t *testing.T) {
	dir := t.TempDir()
	jpath := filepath.Join(dir, "journal.txt")
	media := filepath.Join(dir, "a.aac")
	writeFile(t, media, []byte("a"))

	j := open(t, jpath)
	if err := j.Add("k", media); err != nil {
		t.Fatal(err)
	}
	// A directory in the way makes writing the journal fail.
	if err := os.Mkdir(jpath, 0o755); err != nil {
		t.Fatal(err)
	}
	ops := fsops.New(false)
	if err := j.Save(ops); err == nil {
		t.Fatal("Save onto a directory succeeded")
	}
	if !j.dirty {
		t.Error("a failed Save must keep the journal dirty")
	}
	if s := ops.Stats(); s.Failed != 1 {
		t.Errorf("Failed = %d, want 1", s.Failed)
	}
}

// TestFingerprintCoversWholeSmallAndTail checks that a change anywhere in a file up to two
// chunks long, and in the last chunk of a larger file, changes the fingerprint.
func TestFingerprintCoversWholeSmallAndTail(t *testing.T) {
	for _, size := range []int{1, chunk, chunk + 1, chunk + chunk/2, 2 * chunk, 2*chunk + 1, 200 << 10} {
		t.Run(fmt.Sprint(size), func(t *testing.T) {
			dir := t.TempDir()
			data := bytes.Repeat([]byte("0123456789abcdef"), size/16+1)[:size]
			path := filepath.Join(dir, "video.avi")
			writeFile(t, path, data)
			fp1, err := Fingerprint(path)
			if err != nil {
				t.Fatal(err)
			}
			// Same size, only the last byte differs.
			changed := bytes.Clone(data)
			changed[size-1] ^= 0xff
			writeFile(t, path, changed)
			fp2, err := Fingerprint(path)
			if err != nil {
				t.Fatal(err)
			}
			if fp1 == fp2 {
				t.Errorf("fingerprint unchanged after changing the last byte: %s", fp1)
			}
			if !strings.HasPrefix(fp1, fmt.Sprint(size, "-")) {
				t.Errorf("fingerprint %q does not start with the size", fp1)
			}
		})
	}
}

func TestFingerprintMissingFile(t *testing.T) {
	if _, err := Fingerprint(filepath.Join(t.TempDir(), "missing")); err == nil {
		t.Error("expected an error")
	}
}

func TestConcurrentAdd(t *testing.T) {
	const n = 30
	dir := t.TempDir()
	jpath := filepath.Join(dir, "journal.txt")
	files := make([]string, n)
	for i := range files {
		files[i] = filepath.Join(dir, fmt.Sprintf("f%d.aac", i))
		writeFile(t, files[i], []byte(fmt.Sprint("content ", i)))
	}

	j := open(t, jpath)
	var wg sync.WaitGroup
	for i := range files {
		wg.Add(2)
		go func() {
			defer wg.Done()
			if err := j.Add("k", files[i]); err != nil {
				t.Error(err)
			}
		}()
		go func() { // concurrent readers and duplicate adds
			defer wg.Done()
			j.Has("k", files[(i+1)%n])
			if err := j.Add("k", files[0]); err != nil {
				t.Error(err)
			}
		}()
	}
	wg.Wait()
	if err := j.Save(fsops.New(false)); err != nil {
		t.Fatal(err)
	}

	j2 := open(t, jpath)
	for _, f := range files {
		if !j2.Has("k", f) {
			t.Errorf("entry for %s lost", f)
		}
	}
	data, err := os.ReadFile(jpath)
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.Count(string(data), "\n") - 1; got != n {
		t.Errorf("journal has %d entries, want %d", got, n)
	}
}

// TestFingerprintFormat pins the fingerprint of small and large files, so journals written
// by earlier runs stay valid: size, "-", and the first 32 hex digits of the SHA-256 of the
// first and last chunk (the whole file if it is at most two chunks long).
func TestFingerprintFormat(t *testing.T) {
	for _, size := range []int{0, 10, chunk, chunk + 100, 3 * chunk} {
		data := make([]byte, size)
		for i := range data {
			data[i] = byte(i * 7)
		}
		hashed := data
		if size > 2*chunk {
			hashed = append(bytes.Clone(data[:chunk]), data[size-chunk:]...)
		}
		sum := sha256.Sum256(hashed)
		want := fmt.Sprintf("%d-%s", size, hex.EncodeToString(sum[:])[:32])

		path := filepath.Join(t.TempDir(), "f")
		writeFile(t, path, data)
		if got, err := Fingerprint(path); err != nil || got != want {
			t.Errorf("size %d: Fingerprint = %q, %v; want %q", size, got, err, want)
		}
	}
}

// Open ignores the header comment, blank lines and surrounding whitespace (CRLF), so a
// reloaded and re-saved journal keeps exactly one header.
func TestOpenIgnoresHeaderBlankLinesAndCRLF(t *testing.T) {
	dir := t.TempDir()
	jpath := filepath.Join(dir, "media-journal.txt")
	a, b := filepath.Join(dir, "a.aac"), filepath.Join(dir, "b.aac")
	writeFile(t, a, []byte("a"))
	writeFile(t, b, []byte("b"))
	fpA, err := Fingerprint(a)
	if err != nil {
		t.Fatal(err)
	}
	writeFile(t, jpath, []byte("# songs-sanitizer media journal: header\r\n\r\n  \n  k\t"+fpA+"\r\n"))

	j := open(t, jpath)
	if !j.Has("k", a) {
		t.Error("CRLF/indented entry not found")
	}
	if len(j.entries) != 1 {
		t.Errorf("entries = %v, want only the one entry", j.entries)
	}
	if err := j.Add("k", b); err != nil {
		t.Fatal(err)
	}
	if err := j.Save(fsops.New(false)); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(jpath)
	if err != nil {
		t.Fatal(err)
	}
	fpB, _ := Fingerprint(b)
	want := []string{"k\t" + fpA, "k\t" + fpB}
	sort.Strings(want)
	lines := strings.Split(strings.TrimSuffix(string(data), "\n"), "\n")
	if len(lines) != 3 || !strings.HasPrefix(lines[0], "# ") || lines[1] != want[0] || lines[2] != want[1] {
		t.Errorf("saved journal:\n%s", data)
	}
	if n := strings.Count(string(data), "#"); n != 1 {
		t.Errorf("%d header lines, want 1:\n%s", n, data)
	}
}

func TestOpenLineTooLong(t *testing.T) {
	jpath := filepath.Join(t.TempDir(), "journal.txt")
	writeFile(t, jpath, append(bytes.Repeat([]byte("x"), 70<<10), '\n'))
	if _, err := Open(jpath); err == nil {
		t.Error("Open of a journal with a 70 KiB line succeeded")
	}
}

// AddAndSave persists right away: a reopened journal has the entry without a Save call.
func TestAddAndSavePersistsImmediately(t *testing.T) {
	dir := t.TempDir()
	jpath := filepath.Join(dir, "out", "media-journal.txt")
	media := filepath.Join(dir, "a.aac")
	writeFile(t, media, []byte("a"))
	ops := fsops.New(false)

	j := open(t, jpath)
	if err := j.AddAndSave("k", media, ops); err != nil {
		t.Fatal(err)
	}
	if !open(t, jpath).Has("k", media) {
		t.Error("entry not persisted by AddAndSave")
	}
	if j.dirty {
		t.Error("journal still dirty after AddAndSave")
	}

	// A missing file is an error and writes nothing.
	jpath2 := filepath.Join(dir, "out", "other-journal.txt")
	j2 := open(t, jpath2)
	if err := j2.AddAndSave("k", filepath.Join(dir, "missing.aac"), ops); err == nil {
		t.Error("AddAndSave of a missing file succeeded")
	}
	if _, err := os.Stat(jpath2); !os.IsNotExist(err) {
		t.Errorf("AddAndSave of a missing file wrote the journal (err %v)", err)
	}
	if s := ops.Stats(); s.Failed != 0 {
		t.Errorf("Stats = %+v", s)
	}
}

func TestConcurrentAddAndSave(t *testing.T) {
	const n = 30
	dir := t.TempDir()
	jpath := filepath.Join(dir, "journal.txt")
	files := make([]string, n)
	for i := range files {
		files[i] = filepath.Join(dir, fmt.Sprintf("f%d.aac", i))
		writeFile(t, files[i], []byte(fmt.Sprint("content ", i)))
	}
	j := open(t, jpath)
	ops := fsops.New(false)
	var wg sync.WaitGroup
	for _, f := range files {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if err := j.AddAndSave("k", f, ops); err != nil {
				t.Error(err)
			}
		}()
	}
	wg.Wait()
	j2 := open(t, jpath)
	for _, f := range files {
		if !j2.Has("k", f) {
			t.Errorf("entry for %s lost", f)
		}
	}
	if len(j2.entries) != n {
		t.Errorf("reloaded journal has %d entries, want %d", len(j2.entries), n)
	}
}

// Only the first and last chunk are hashed: a same-size change in the middle of a larger
// file is deliberately not detected (this keeps fingerprints of large videos fast).
func TestFingerprintIgnoresMiddleOfLargeFile(t *testing.T) {
	data := bytes.Repeat([]byte("0123456789abcdef"), 3*chunk/16)
	path := filepath.Join(t.TempDir(), "video.avi")
	writeFile(t, path, data)
	fp1, err := Fingerprint(path)
	if err != nil {
		t.Fatal(err)
	}
	data[len(data)/2] ^= 0xff
	writeFile(t, path, data)
	if fp2, err := Fingerprint(path); err != nil || fp2 != fp1 {
		t.Errorf("Fingerprint after a change in the middle = %q, %v; want unchanged %q", fp2, err, fp1)
	}
}

func TestFingerprintDirectory(t *testing.T) {
	if fp, err := Fingerprint(t.TempDir()); err == nil {
		t.Errorf("Fingerprint of a directory = %q, want an error", fp)
	}
}
