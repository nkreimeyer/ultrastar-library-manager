package webui

import (
	"net/http"
	"path/filepath"
	"strings"
	"testing"

	"github.com/nkreimeyer/library-manager/internal/testutil"
)

func TestReports(t *testing.T) {
	s := newTestServer(t)
	lib, out := removeLibrary(t)
	for name, content := range map[string]string{
		"check_report.txt": "1 song(s) checked", "organize_plan.json": "{}", "media-journal.txt": "secret",
		".hidden.txt": "h", "notes.md": "m", "organize-journal.jsonl": "{}",
	} {
		testutil.WriteFile(t, filepath.Join(out, name), content)
	}
	testutil.WriteFile(t, filepath.Join(out, "invalid", "inner.txt"), "not top level")

	var list struct {
		Files []reportFile `json:"files"`
	}
	if got := s.do("POST", "/api/reports/list", folderRequest{Library: lib, Out: out}, &list); got != http.StatusOK {
		t.Fatalf("list: %d", got)
	}
	var names []string
	for _, f := range list.Files {
		names = append(names, f.Name)
	}
	if len(names) != 2 || !(names[0] == "check_report.txt" || names[1] == "check_report.txt") {
		t.Fatalf("files = %v", names)
	}

	var rd struct {
		Text      string `json:"text"`
		Truncated bool   `json:"truncated"`
	}
	if got := s.do("POST", "/api/reports/read", map[string]any{"library": lib, "out": out, "name": "check_report.txt"}, &rd); got != http.StatusOK || rd.Text != "1 song(s) checked" || rd.Truncated {
		t.Fatalf("read: %d %+v", got, rd)
	}
	var e apiError
	for _, name := range []string{"media-journal.txt", "../library/A/x.txt", "organize-journal.jsonl", "missing.txt", "invalid/inner.txt", ""} {
		if got := s.do("POST", "/api/reports/read", map[string]any{"library": lib, "out": out, "name": name}, &e); got != http.StatusBadRequest && got != http.StatusNotFound {
			t.Errorf("%q: %d", name, got)
		}
	}

	// A large report is cut off.
	testutil.WriteFile(t, filepath.Join(out, "big.txt"), strings.Repeat("x", maxReportBytes+10))
	s.do("POST", "/api/reports/read", map[string]any{"library": lib, "out": out, "name": "big.txt"}, &rd)
	if !rd.Truncated || len(rd.Text) != maxReportBytes {
		t.Errorf("big report: truncated=%v len=%d", rd.Truncated, len(rd.Text))
	}
	// No output folder yet: an empty list.
	if got := s.do("POST", "/api/reports/list", folderRequest{Library: lib, Out: filepath.Join(filepath.Dir(out), "nothing")}, &list); got != http.StatusOK || len(list.Files) != 0 {
		t.Errorf("no folder: %d %v", got, list.Files)
	}
}
