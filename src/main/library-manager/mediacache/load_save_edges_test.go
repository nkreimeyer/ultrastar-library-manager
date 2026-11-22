package mediacache

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/nkreimeyer/library-manager/fsops"
	"github.com/nkreimeyer/library-manager/internal/testutil"
	log "github.com/sirupsen/logrus"
)

// A cache file of other tool versions that holds no results has nothing to keep: a run
// that stores nothing still replaces it with the current tool versions.
func TestToolChangeEmptyFileIsReplaced(t *testing.T) {
	root, clk := t.TempDir(), newClock()
	doc := `{"format":1,"tools":{"ffmpeg":"old","ffprobe":"old"},"folders":{}}`
	path := filepath.Join(root, FileName)
	if err := os.WriteFile(path, gzipBytes(t, doc), 0o644); err != nil {
		t.Fatal(err)
	}
	before, _ := os.ReadFile(path)
	c := openTest(t, root, clk)
	save(t, c)
	after, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(after) == string(before) {
		t.Error("the empty cache of other tool versions was kept")
	}
	c2 := openTest(t, root, clk)
	c2.mu.Lock()
	defer c2.mu.Unlock()
	if c2.dropped {
		t.Error("an empty file counted as holding dropped results")
	}
}

// Loading logs how many files and folders it read.
func TestLoadLogsFileCount(t *testing.T) {
	root, clk := t.TempDir(), newClock()
	a, b := filepath.Join(root, "A", "a.mp3"), filepath.Join(root, "B", "b.mp3")
	writeOld(t, clk, a, "audio1")
	writeOld(t, clk, b, "audio22")
	c := openTest(t, root, clk)
	storeResult(t, c, a, result{N: 1})
	storeResult(t, c, b, result{N: 2})
	save(t, c)

	hook := testutil.CaptureLogsAt(t, log.DebugLevel)
	openTest(t, root, clk)
	for _, e := range hook.AllEntries() {
		if strings.Contains(e.Message, ": 2 files of 2 folders") {
			return
		}
	}
	t.Errorf("no load log with the counts: %v", hook.AllEntries())
}

// absPath makes a relative path absolute.
func TestAbsPathRelative(t *testing.T) {
	got := absPath(filepath.Join("some", "rel"))
	if !filepath.IsAbs(got) || !strings.HasSuffix(got, filepath.Join("some", "rel")) {
		t.Errorf("absPath(relative) = %q, want an absolute path", got)
	}
}

// A successful Save logs no warning; a failing one does and returns the error.
func TestSaveWarnsOnlyOnFailure(t *testing.T) {
	root, clk := t.TempDir(), newClock()
	c := openTest(t, root, clk)
	hook := testutil.CaptureLogsAt(t, log.DebugLevel)
	save(t, c)
	for _, e := range hook.AllEntries() {
		if e.Level <= log.WarnLevel {
			t.Errorf("successful Save logged %q", e.Message)
		}
	}

	// the cache path is a non-empty folder: the final rename cannot replace it
	hook.Reset()
	if err := os.Remove(filepath.Join(root, FileName)); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(root, FileName, "x"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := c.Save(fsops.New(false)); err == nil {
		t.Fatal("Save over a folder succeeded")
	}
	warned := false
	for _, e := range hook.AllEntries() {
		if e.Level == log.WarnLevel && strings.Contains(e.Message, "could not be saved") {
			warned = true
		}
	}
	if !warned {
		t.Errorf("no warning for the failed Save: %v", hook.AllEntries())
	}
}
