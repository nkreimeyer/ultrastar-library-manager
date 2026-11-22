package webui

import (
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/nkreimeyer/library-manager/config"
	"github.com/nkreimeyer/library-manager/internal/testutil"
)

func TestOriginals(t *testing.T) {
	s := newTestServer(t)
	lib, out := removeLibrary(t)

	var v originalsView
	if got := s.do("POST", "/api/originals/list", folderRequest{Library: lib, Out: out}, &v); got != http.StatusOK || v.Files != 0 || v.Bytes != 0 || v.Largest == nil {
		t.Fatalf("no originals folder: %d %+v", got, v)
	}

	testutil.WriteFile(t, filepath.Join(out, config.OutputMarker), "")
	testutil.WriteFile(t, filepath.Join(out, "originals", "A", "ABBA - SOS", "song.mp3"), strings.Repeat("a", 300))
	testutil.WriteFile(t, filepath.Join(out, "originals", "Q", "Queen - Rock", "song.m4a"), strings.Repeat("b", 100))
	testutil.WriteFile(t, filepath.Join(out, "originals", "Q", "Queen - Rock", "song2.m4a"), strings.Repeat("c", 200))
	testutil.WriteFile(t, filepath.Join(out, "invalid", "keep.txt"), "must stay")
	testutil.WriteFile(t, filepath.Join(out, "extra-files", "x.jpg"), "must stay")

	if got := s.do("POST", "/api/originals/list", folderRequest{Library: lib, Out: out}, &v); got != http.StatusOK ||
		v.Files != 3 || v.Bytes != 600 || len(v.Largest) != 3 || v.Largest[0].Rel != "A/ABBA - SOS/song.mp3" || v.Largest[2].Bytes != 100 {
		t.Fatalf("list: %d %+v", got, v)
	}

	var j Job
	if got := s.do("POST", "/api/originals/delete", folderRequest{Library: lib, Out: out}, &j); got != http.StatusAccepted {
		t.Fatalf("delete: %d", got)
	}
	if j = s.wait(); j.State != stateDone || j.Stats.Removed != 3 || j.Stats.Failed != 0 {
		t.Fatalf("delete job: %+v", j)
	}
	if _, err := os.Stat(filepath.Join(out, "originals")); !os.IsNotExist(err) {
		t.Errorf("originals/ is still there: %v", err)
	}
	for _, p := range []string{"invalid/keep.txt", "extra-files/x.jpg"} {
		if !testutil.Exists(filepath.Join(out, p)) {
			t.Errorf("%s was deleted", p)
		}
	}
	// The library is untouched.
	if !testutil.Exists(filepath.Join(lib, "A", "ABBA - SOS", "song.mp3")) {
		t.Error("a library file was deleted")
	}
	// Deleting again is harmless.
	s.do("POST", "/api/originals/delete", folderRequest{Library: lib, Out: out}, &j)
	if j = s.wait(); j.State != stateDone || j.Stats.Removed != 0 {
		t.Errorf("second delete: %+v", j)
	}
}

// A folder without the output marker is not an output folder of this program: its
// originals/ is neither listed nor deleted (the output field is free text in advanced mode).
func TestOriginalsNeedOutputMarker(t *testing.T) {
	s := newTestServer(t)
	lib, _ := removeLibrary(t)
	out := filepath.Join(t.TempDir(), "Music")
	file := filepath.Join(out, "originals", "x.mp3")
	testutil.WriteFile(t, file, "not ours")

	var e apiError
	if got := s.do("POST", "/api/originals/list", folderRequest{Library: lib, Out: out}, &e); got != http.StatusBadRequest ||
		!strings.Contains(e.Error, "not an output folder") {
		t.Errorf("list: %d %q", got, e.Error)
	}
	e = apiError{}
	if got := s.do("POST", "/api/originals/delete", folderRequest{Library: lib, Out: out}, &e); got != http.StatusBadRequest ||
		!strings.Contains(e.Error, "not an output folder") {
		t.Fatalf("delete: %d %q", got, e.Error)
	}
	if !testutil.Exists(file) {
		t.Fatal("a file in a folder that is not an output folder was deleted")
	}

	// The legacy marker of the old program name counts.
	testutil.WriteFile(t, filepath.Join(out, config.LegacyOutputMarker), "")
	var v originalsView
	if got := s.do("POST", "/api/originals/list", folderRequest{Library: lib, Out: out}, &v); got != http.StatusOK || v.Files != 1 {
		t.Errorf("list with the legacy marker: %d %+v", got, v)
	}
	var j Job
	if got := s.do("POST", "/api/originals/delete", folderRequest{Library: lib, Out: out}, &j); got != http.StatusAccepted {
		t.Fatalf("delete with the legacy marker: %d", got)
	}
	if j = s.wait(); j.State != stateDone || j.Stats.Removed != 1 || testutil.Exists(file) {
		t.Errorf("delete job with the legacy marker: %+v", j)
	}
}
