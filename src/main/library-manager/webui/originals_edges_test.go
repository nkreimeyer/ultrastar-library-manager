package webui

import (
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	log "github.com/sirupsen/logrus"

	"github.com/nkreimeyer/library-manager/config"
	"github.com/nkreimeyer/library-manager/internal/testutil"
)

// An output folder without originals/ lists nothing and its delete is a harmless no-op
// that leaves everything else alone.
func TestOriginalsMissingFolder(t *testing.T) {
	s := newTestServer(t)
	lib, out := removeLibrary(t)
	testutil.WriteFile(t, filepath.Join(out, config.OutputMarker), "")
	testutil.WriteFile(t, filepath.Join(out, "invalid", "keep.txt"), "stay")
	var v originalsView
	if got := s.do("POST", "/api/originals/list", folderRequest{Library: lib, Out: out}, &v); got != http.StatusOK ||
		v.Files != 0 || v.Bytes != 0 || v.Largest == nil || len(v.Largest) != 0 || v.Dir != filepath.Join(out, "originals") {
		t.Errorf("list: %d %+v", got, v)
	}
	var j Job
	if got := s.do("POST", "/api/originals/delete", folderRequest{Library: lib, Out: out}, &j); got != http.StatusAccepted {
		t.Fatalf("delete: %d", got)
	}
	if j = s.wait(); j.State != stateDone || j.Stats.Removed != 0 || j.Stats.Failed != 0 {
		t.Errorf("delete job: %+v", j)
	}
	if !testutil.Exists(filepath.Join(out, "invalid", "keep.txt")) {
		t.Error("another folder of the output was touched")
	}
}

// A symlink in originals/ is a file of its own: it is counted (with the size of the link),
// never followed, and only the link is deleted, not what it points to.
func TestOriginalsSymlinksAreNotFollowed(t *testing.T) {
	s := newTestServer(t)
	lib, out := removeLibrary(t)
	testutil.WriteFile(t, filepath.Join(out, config.OutputMarker), "")
	outside := t.TempDir()
	testutil.WriteFile(t, filepath.Join(outside, "precious", "song.mp3"), "precious audio")
	testutil.WriteFile(t, filepath.Join(outside, "single.mp3"), "single audio")
	orig := filepath.Join(out, "originals")
	testutil.WriteFile(t, filepath.Join(orig, "A", "real.mp3"), "real")
	for link, target := range map[string]string{
		"dirlink":    filepath.Join(outside, "precious"),
		"filelink":   filepath.Join(outside, "single.mp3"),
		"dangling":   filepath.Join(outside, "gone"),
		"A/nestlink": filepath.Join(outside, "precious"),
	} {
		if err := os.Symlink(target, filepath.Join(orig, link)); err != nil {
			t.Skipf("symlinks not supported: %v", err)
		}
	}
	var v originalsView
	if got := s.do("POST", "/api/originals/list", folderRequest{Library: lib, Out: out}, &v); got != http.StatusOK {
		t.Fatalf("list: %d", got)
	}
	var rels []string
	for _, f := range v.Largest {
		rels = append(rels, f.Rel)
	}
	// 1 real file + 4 links; the files below a linked folder are not listed.
	if v.Files != 5 || len(rels) != 5 || strings.Contains(strings.Join(rels, ","), "song.mp3") {
		t.Errorf("list: files %d, %v", v.Files, rels)
	}
	var j Job
	if got := s.do("POST", "/api/originals/delete", folderRequest{Library: lib, Out: out}, &j); got != http.StatusAccepted {
		t.Fatalf("delete: %d", got)
	}
	if j = s.wait(); j.State != stateDone || j.Stats.Removed != 5 || j.Stats.Failed != 0 {
		t.Fatalf("delete job: %+v", j)
	}
	if _, err := os.Lstat(orig); err == nil {
		t.Error("originals/ is still there")
	}
	for _, p := range []string{"precious/song.mp3", "single.mp3"} {
		if testutil.ReadFile(t, filepath.Join(outside, p)) == "" {
			t.Errorf("%s was emptied", p)
		}
	}
}

// The list shows the biggest maxOriginalsListed files, biggest first (ties by path), but
// counts all of them.
func TestOriginalsListCapAndOrder(t *testing.T) {
	s := newTestServer(t)
	lib, out := removeLibrary(t)
	testutil.WriteFile(t, filepath.Join(out, config.OutputMarker), "")
	total := maxOriginalsListed + 5
	var bytes int64
	for i := 0; i < total; i++ {
		size := 1 + i%7 // many ties, so the order by path is exercised
		bytes += int64(size)
		testutil.WriteFile(t, filepath.Join(out, "originals", fmt.Sprintf("d%d", i%3), fmt.Sprintf("f%03d.mp3", i)), strings.Repeat("x", size))
	}
	var v originalsView
	if got := s.do("POST", "/api/originals/list", folderRequest{Library: lib, Out: out}, &v); got != http.StatusOK {
		t.Fatalf("list: %d", got)
	}
	if v.Files != total || v.Bytes != bytes || len(v.Largest) != maxOriginalsListed {
		t.Fatalf("files %d bytes %d listed %d, want %d %d %d", v.Files, v.Bytes, len(v.Largest), total, bytes, maxOriginalsListed)
	}
	for i := 1; i < len(v.Largest); i++ {
		a, b := v.Largest[i-1], v.Largest[i]
		if a.Bytes < b.Bytes || (a.Bytes == b.Bytes && a.Rel >= b.Rel) {
			t.Fatalf("order broken at %d: %+v before %+v", i, a, b)
		}
	}
	if v.Largest[0].Bytes != 7 {
		t.Errorf("the biggest file has %d bytes, want 7", v.Largest[0].Bytes)
	}
	// 15 files of each size 1..7: the 5 left out are of size 1.
	ones := 0
	for _, f := range v.Largest {
		if f.Bytes == 1 {
			ones++
		}
	}
	if ones != 10 {
		t.Errorf("%d one-byte files listed, want 10: a bigger one was left out", ones)
	}
}

// A stop during the delete ends the job "cancelled" with the folders in its result; the
// files not yet deleted are still there, and a second delete finishes the work.
func TestOriginalsDeleteStopped(t *testing.T) {
	s := newTestServer(t)
	lib, out := removeLibrary(t)
	testutil.WriteFile(t, filepath.Join(out, config.OutputMarker), "")
	for i := 0; i < 5; i++ {
		testutil.WriteFile(t, filepath.Join(out, "originals", fmt.Sprintf("f%d.mp3", i)), "audio")
	}
	var removed atomic.Int32
	log.AddHook(&logFunc{fire: func(e *log.Entry) {
		if strings.HasPrefix(e.Message, "Removed ") && removed.Add(1) == 2 {
			s.srv.jobs.cancelRunning() // after the second file: the next check of the loop stops
		}
	}})
	var j Job
	if got := s.do("POST", "/api/originals/delete", folderRequest{Library: lib, Out: out}, &j); got != http.StatusAccepted {
		t.Fatalf("delete: %d", got)
	}
	j = s.wait()
	if j.State != stateCancelled || j.Stats.Removed != 2 || j.Stats.Failed != 0 {
		t.Fatalf("stopped delete: %+v", j)
	}
	if res, _ := j.Result.(map[string]any); res["library"] != lib || res["outDir"] != out {
		t.Errorf("result = %#v", j.Result)
	}
	if left := testutil.DirNames(t, filepath.Join(out, "originals")); len(left) != 3 {
		t.Errorf("left in originals/: %v, want 3 files", left)
	}
	removed.Store(-100) // the hook does not stop the second job
	if got := s.do("POST", "/api/originals/delete", folderRequest{Library: lib, Out: out}, &j); got != http.StatusAccepted {
		t.Fatalf("second delete: %d", got)
	}
	if j = s.wait(); j.State != stateDone || j.Stats.Removed != 3 || testutil.Exists(filepath.Join(out, "originals")) {
		t.Errorf("second delete: %+v", j)
	}
}

// logFunc is a logrus hook that calls fire for every entry.
type logFunc struct{ fire func(*log.Entry) }

func (h *logFunc) Levels() []log.Level     { return log.AllLevels }
func (h *logFunc) Fire(e *log.Entry) error { h.fire(e); return nil }
