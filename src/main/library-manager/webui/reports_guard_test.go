package webui

import (
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/nkreimeyer/library-manager/internal/testutil"
)

// The report viewer reads only files of the output folder's top level that are reports:
// no path, no other extension, no link and no folder named like a report.
func TestReportsReadGuards(t *testing.T) {
	s := newTestServer(t)
	lib, out := removeLibrary(t)
	secret := filepath.Join(t.TempDir(), "secret.txt")
	testutil.WriteFile(t, secret, "do not show")
	testutil.WriteFile(t, filepath.Join(out, "dir.txt", "inner.txt"), "in a folder")
	testutil.WriteFile(t, filepath.Join(out, "notes.md"), "not a report")
	testutil.WriteFile(t, filepath.Join(out, "sub", "report.txt"), "below the top level")
	if err := os.Symlink(secret, filepath.Join(out, "link.txt")); err != nil {
		t.Skipf("symlinks not supported: %v", err)
	}
	if err := os.Symlink(filepath.Join(out, "dir.txt"), filepath.Join(out, "dirlink.json")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(out, "missing"), filepath.Join(out, "dangling.json")); err != nil {
		t.Fatal(err)
	}
	read := func(name string) (int, string) {
		var r struct {
			Text  string `json:"text"`
			Error string `json:"error"`
		}
		code := s.do("POST", "/api/reports/read", map[string]any{"library": lib, "out": out, "name": name}, &r)
		return code, r.Text + r.Error
	}
	for name, want := range map[string]int{
		"sub/report.txt":      http.StatusBadRequest, // a path
		"../" + "x.txt":       http.StatusBadRequest,
		secret:                http.StatusBadRequest, // an absolute path
		"notes.md":            http.StatusBadRequest, // not a report
		"media-journal.txt":   http.StatusBadRequest, // a journal
		".hidden.txt":         http.StatusBadRequest,
		"":                    http.StatusBadRequest,
		"nothing.txt":         http.StatusNotFound,
		"dir.txt":             http.StatusNotFound, // a folder
		"link.txt":            http.StatusNotFound, // a link is not followed
		"dirlink.json":        http.StatusNotFound,
		"dangling.json":       http.StatusNotFound,
		"CHECK_REPORT.TXT":    http.StatusNotFound, // the extension is matched in any case; the file is missing
		"organize_plan.json ": http.StatusBadRequest,
	} {
		code, msg := read(name)
		if code != want {
			t.Errorf("read %q: %d %q, want %d", name, code, msg, want)
		}
		if strings.Contains(msg, "do not show") || strings.Contains(msg, "in a folder") {
			t.Errorf("read %q leaked: %q", name, msg)
		}
	}
}

// A report of exactly maxReportBytes is shown whole; one byte more is cut off at the limit.
func TestReportsReadTruncationBoundary(t *testing.T) {
	s := newTestServer(t)
	lib, out := removeLibrary(t)
	testutil.WriteFile(t, filepath.Join(out, "exact.txt"), strings.Repeat("a", maxReportBytes))
	testutil.WriteFile(t, filepath.Join(out, "over.json"), strings.Repeat("b", maxReportBytes+1))
	var r struct {
		Name      string `json:"name"`
		Text      string `json:"text"`
		Truncated bool   `json:"truncated"`
	}
	if got := s.do("POST", "/api/reports/read", map[string]any{"library": lib, "out": out, "name": "exact.txt"}, &r); got != http.StatusOK ||
		r.Truncated || len(r.Text) != maxReportBytes || r.Name != "exact.txt" {
		t.Errorf("exact: %d truncated=%v len=%d", got, r.Truncated, len(r.Text))
	}
	if got := s.do("POST", "/api/reports/read", map[string]any{"library": lib, "out": out, "name": "over.json"}, &r); got != http.StatusOK ||
		!r.Truncated || len(r.Text) != maxReportBytes || strings.Trim(r.Text, "b") != "" {
		t.Errorf("one byte over: %d truncated=%v len=%d", got, r.Truncated, len(r.Text))
	}
}

// A report that is not valid UTF-8 is shown with replacement characters (the page needs
// valid JSON text).
func TestReportsReadInvalidUTF8(t *testing.T) {
	s := newTestServer(t)
	lib, out := removeLibrary(t)
	testutil.WriteFile(t, filepath.Join(out, "latin.txt"), "M\xe4dchen")
	var r struct {
		Text string `json:"text"`
	}
	if got := s.do("POST", "/api/reports/read", map[string]any{"library": lib, "out": out, "name": "latin.txt"}, &r); got != http.StatusOK || r.Text != "M�dchen" {
		t.Errorf("read: %d %q", got, r.Text)
	}
}

// The list holds the regular .txt and .json files of the top level, newest first (ties by
// name), without journals, hidden files, folders or links; no output folder gives [].
func TestReportsListFiltersAndOrder(t *testing.T) {
	s := newTestServer(t)
	lib, out := removeLibrary(t)
	now := time.Now().Truncate(time.Second)
	for name, age := range map[string]time.Duration{
		"old.txt": 3 * time.Hour, "newest.json": 0, "UPPER.TXT": time.Hour, "tie-b.txt": 2 * time.Hour, "tie-a.txt": 2 * time.Hour,
		"media-journal.txt": 0, "organize-journal.jsonl": 0, ".hidden.json": 0, "readme.md": 0, "noext": 0,
	} {
		p := filepath.Join(out, name)
		testutil.WriteFile(t, p, "x")
		if err := os.Chtimes(p, now.Add(-age), now.Add(-age)); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.MkdirAll(filepath.Join(out, "folder.txt"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(out, "newest.json"), filepath.Join(out, "link.json")); err != nil {
		t.Skipf("symlinks not supported: %v", err)
	}
	var l struct {
		Files []reportFile `json:"files"`
	}
	if got := s.do("POST", "/api/reports/list", folderRequest{Library: lib, Out: out}, &l); got != http.StatusOK {
		t.Fatalf("list: %d", got)
	}
	var names []string
	for _, f := range l.Files {
		names = append(names, f.Name)
		if f.Size != 1 || f.Modified.IsZero() {
			t.Errorf("%s: size %d, modified %v", f.Name, f.Size, f.Modified)
		}
	}
	if want := "newest.json,UPPER.TXT,tie-a.txt,tie-b.txt,old.txt"; strings.Join(names, ",") != want {
		t.Errorf("files = %v, want %s", names, want)
	}
	// Without an output folder the list is [], not null.
	var raw struct {
		Files json.RawMessage `json:"files"`
	}
	if got := s.do("POST", "/api/reports/list", folderRequest{Library: lib, Out: filepath.Join(filepath.Dir(out), "none")}, &raw); got != http.StatusOK || string(raw.Files) != "[]" {
		t.Errorf("no folder: %d files = %s", got, raw.Files)
	}
}
