package webui

import (
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/nkreimeyer/library-manager/internal/testutil"
)

func TestOriginals(t *testing.T) {
	s := newTestServer(t)
	lib, out := removeLibrary(t)

	var v originalsView
	if got := s.do("POST", "/api/originals/list", folderRequest{Library: lib, Out: out}, &v); got != http.StatusOK || v.Files != 0 || v.Bytes != 0 || v.Largest == nil {
		t.Fatalf("no originals folder: %d %+v", got, v)
	}

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
