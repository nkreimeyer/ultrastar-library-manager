package webui

import (
	"net/http"
	"path/filepath"
	"strings"
	"testing"

	"github.com/nkreimeyer/library-manager/internal/testutil"
)

func TestOverview(t *testing.T) {
	s := newTestServer(t)
	lib, out := removeLibrary(t)
	testutil.WriteFile(t, filepath.Join(lib, "A", "ABBA - SOS", "video.mp4"), "v")
	testutil.WriteFile(t, filepath.Join(lib, "A", "ABBA - SOS", "cover.jpg"), "c")
	testutil.WriteFile(t, filepath.Join(lib, "Q", "Queen - Rock", ".hidden"), strings.Repeat("h", 1000))
	testutil.WriteFiles(t, filepath.Join(lib, "Z", "No - Audio"), map[string]string{"song.txt": "#ARTIST:No\n#TITLE:Audio\n" + testutil.NotesTxt("no audio")})
	testutil.WriteFile(t, filepath.Join(lib, "Junk", "stray.txt"), "no song here") // a folder, not a song
	testutil.WriteFile(t, filepath.Join(out, "extra-files", "A", "x.jpg"), "x")
	writeSong(t, filepath.Join(out, "removed", "Q", "Gone - Song"), "Gone", "Song", nil)

	var j Job
	if got := s.do("POST", "/api/overview", folderRequest{Library: lib, Out: out}, &j); got != http.StatusAccepted {
		t.Fatalf("overview: %d", got)
	}
	if j = s.wait(); j.State != stateDone {
		t.Fatalf("job: %+v", j)
	}
	var res overviewResult
	s.do("GET", "/api/job", nil, &struct{ Result *overviewResult }{&res})
	if res.NoSong != 1 || res.Songs != 4 || res.WithVideo != 1 || res.WithImage != 1 || res.NoAudio != 1 {
		t.Errorf("counts = %+v", res)
	}
	want := map[string]int{"A": 2, "Q": 1, "Z": 1}
	for _, b := range res.Buckets {
		if want[b.Name] != b.Songs {
			t.Errorf("bucket %s = %d songs", b.Name, b.Songs)
		}
		delete(want, b.Name)
	}
	if len(want) != 0 || len(res.Buckets) != 3 || res.Buckets[0].Name != "A" {
		t.Errorf("buckets = %+v", res.Buckets)
	}
	if res.SetAside["extra-files"] != 1 || res.SetAside["removed"] != 1 || res.SetAside["invalid"] != 0 {
		t.Errorf("set aside = %v", res.SetAside)
	}
	if res.Bytes < 1 {
		t.Errorf("bytes = %d", res.Bytes)
	}
	var e apiError
	if got := s.do("POST", "/api/overview", folderRequest{Library: filepath.Join(lib, "none")}, &e); got != http.StatusBadRequest {
		t.Errorf("missing library: %d", got)
	}
}
