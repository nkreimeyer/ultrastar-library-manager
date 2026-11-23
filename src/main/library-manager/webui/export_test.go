package webui

import (
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/nkreimeyer/library-manager/internal/testutil"
)

func TestExport(t *testing.T) {
	s := newTestServer(t)
	lib, out := removeLibrary(t)
	before := testutil.Snapshot(t, lib)

	var j Job
	if got := s.do("POST", "/api/export", exportRequest{Library: lib, Out: out}, &j); got != http.StatusAccepted {
		t.Fatalf("export: %d", got)
	}
	if j = s.wait(); j.State != stateDone || j.Kind != "export" {
		t.Fatalf("job: %+v", j)
	}
	var res exportResult
	s.do("GET", "/api/job", nil, &struct{ Result *exportResult }{&res})
	file := filepath.Join(out, "song_list.csv")
	if res.Songs != 3 || res.File != file || res.OutDir != out || res.Library != lib {
		t.Fatalf("result = %+v", res)
	}
	disk, err := os.ReadFile(file)
	if err != nil || !strings.Contains(string(disk), "ABBA,SOS,") || !strings.HasPrefix(string(disk), "\xEF\xBB\xBFArtist,") {
		t.Errorf("file %q, %v", disk, err)
	}
	if testutil.Snapshot(t, lib) != before {
		t.Error("the export changed the library")
	}

	// The download is the file, as an attachment.
	resp, err := s.client.Get(s.base + "/api/export/download?id=" + res.ID)
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK || string(body) != string(disk) ||
		resp.Header.Get("Content-Type") != "text/csv; charset=utf-8" ||
		resp.Header.Get("Content-Disposition") != `attachment; filename="song_list.csv"` {
		t.Errorf("download: %d %v\n%s", resp.StatusCode, resp.Header, body)
	}

	// Only the newest export, by id, is served; a path in the request is not.
	var e apiError
	for _, q := range []string{"", "?id=x999"} {
		if got := s.do("GET", "/api/export/download"+q, nil, &e); got != http.StatusNotFound {
			t.Errorf("download%s: %d", q, got)
		}
	}
	resp, err = s.client.Get(s.base + "/api/export/download?id=" + res.ID + "&path=/etc/passwd")
	if err != nil {
		t.Fatal(err)
	}
	body, _ = io.ReadAll(resp.Body)
	resp.Body.Close()
	if string(body) != string(disk) {
		t.Errorf("a path in the request changed what is served:\n%s", body)
	}
	if got := s.do("POST", "/api/export", exportRequest{Library: filepath.Join(lib, "none")}, &e); got != http.StatusBadRequest {
		t.Errorf("missing library: %d", got)
	}
	if got := s.do("POST", "/api/export", map[string]any{"library": lib, "path": "/tmp/x"}, &e); got != http.StatusBadRequest {
		t.Errorf("unknown field: %d", got)
	}
}
