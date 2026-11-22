package main

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/nkreimeyer/library-manager/internal/testutil"
)

func hasLog(logs []string, prefix string) bool { return testutil.ContainsPrefix(logs, prefix) }

// The main command says a dry run updates the media cache only when it opens one: not with
// -no-cache, and not in a real run.
func TestMainDryRunCacheNotice(t *testing.T) {
	const notice = "Dry run: only the media cache "
	_, lib, _, _ := cliFixture(t)
	out := filepath.Join(t.TempDir(), "out")
	for _, c := range []struct {
		name string
		args []string
		want bool
	}{
		{"dry run with the cache", []string{"-s", lib, "-o", out, "-n", "-k"}, true},
		{"dry run without the cache", []string{"-s", lib, "-o", out, "-n", "-k", "-no-cache"}, false},
		{"real run with the cache", []string{"-s", lib, "-o", out, "-k"}, false},
	} {
		code, _, logs := runCaptured(t, c.args...)
		if code != exitOK {
			t.Fatalf("%s: exit %d", c.name, code)
		}
		if got := hasLog(logs, notice); got != c.want {
			t.Errorf("%s: notice logged = %v, want %v (%q)", c.name, got, c.want, logs)
		}
	}
}

// The same notice for organize, dedupe and add.
func TestOrganizeDryRunCacheNotice(t *testing.T) {
	const notice = "Dry run: only the media cache "
	_, lib, src, _ := cliFixture(t)
	for _, c := range []struct {
		name string
		args []string
		want bool
	}{
		{"organize dry run", []string{"organize", "-s", src, "-l", lib, "-n"}, true},
		{"organize dry run -no-cache", []string{"organize", "-s", src, "-l", lib, "-n", "-no-cache"}, false},
		{"dedupe dry run", []string{"dedupe", "-l", lib, "-n"}, true},
		{"add dry run -no-cache", []string{"add", "-s", src, "-l", lib, "-n", "-no-cache"}, false},
		{"add dry run", []string{"add", "-s", src, "-l", lib, "-n"}, true},
	} {
		code, _, logs := runCaptured(t, append(c.args, "-o", filepath.Join(t.TempDir(), "out"))...)
		if code != exitOK {
			t.Fatalf("%s: exit %d (%q)", c.name, code, logs)
		}
		if got := hasLog(logs, notice); got != c.want {
			t.Errorf("%s: notice logged = %v, want %v", c.name, got, c.want)
		}
	}
	// A real run saves the cache without a notice.
	code, _, logs := runCaptured(t, "dedupe", "-l", lib, "-o", filepath.Join(t.TempDir(), "out"))
	if code != exitOK || hasLog(logs, notice) {
		t.Errorf("real dedupe: exit %d, logs %q", code, logs)
	}
}

// Only add tells that the library counts as part of the first -s.
func TestAddHelpMentionsTheLibrary(t *testing.T) {
	const text = "the library counts as part of the first -s"
	for cmd, want := range map[string]bool{"add": true, "organize": false} {
		_, output, _ := runCaptured(t, cmd, "-h")
		if got := strings.Contains(output, text); got != want {
			t.Errorf("%s -h mentions it = %v, want %v:\n%s", cmd, got, want, output)
		}
	}
}
