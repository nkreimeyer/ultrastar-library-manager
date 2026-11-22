package webui

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	log "github.com/sirupsen/logrus"

	"github.com/nkreimeyer/library-manager/internal/testutil"
)

func TestCheckAddrPortRange(t *testing.T) {
	for addr, ok := range map[string]bool{
		"127.0.0.1:0": true, "127.0.0.1:65535": true, "127.0.0.1:65536": false, "127.0.0.1:-1": false, "[::1]:8080": true,
	} {
		if err := CheckAddr(addr); (err == nil) != ok {
			t.Errorf("CheckAddr(%q) = %v, want ok=%v", addr, err, ok)
		}
	}
}

// A response that was written fine logs nothing about a failed write.
func TestWriteJSONLogsOnlyFailures(t *testing.T) {
	hook := testutil.CaptureLogsAt(t, log.DebugLevel)
	s := newTestServer(t)
	hook.Reset()
	s.do("GET", "/api/system", nil, nil)
	for _, e := range hook.AllEntries() {
		if strings.Contains(e.Message, "writing a response") {
			t.Errorf("logged %q for a response that was written", e.Message)
		}
	}
}

// A relative ?path= is shown as the absolute path; the roots hold the home folder and "/".
func TestFSListRelativePathAndRoots(t *testing.T) {
	s := newTestServer(t)
	base := t.TempDir()
	home := filepath.Join(base, "home")
	if err := os.MkdirAll(filepath.Join(base, "work", "sub"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(home, 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	t.Chdir(filepath.Join(base, "work"))
	var l dirListing
	s.do("GET", "/api/fs/list?path=sub", nil, &l)
	want, _ := filepath.Abs("sub")
	if l.Path != want || !filepath.IsAbs(l.Path) {
		t.Errorf("path = %q, want %q", l.Path, want)
	}
	if len(l.Roots) < 2 || l.Roots[0].Name != "Home" || l.Roots[0].Path != home {
		t.Errorf("roots = %+v, want Home first", l.Roots)
	}
	found := false
	for _, r := range l.Roots {
		found = found || r.Path == "/"
	}
	if !found {
		t.Errorf("roots = %+v, want the file system root", l.Roots)
	}
}
