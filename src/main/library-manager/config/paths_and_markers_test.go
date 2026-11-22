package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/nkreimeyer/library-manager/internal/testutil"
)

func TestSamePathCurrentOS(t *testing.T) {
	root := t.TempDir()
	if !SamePath(root, root) || SamePath(root, filepath.Join(root, "sub")) || SamePath(filepath.Join(root, "sub"), root) {
		t.Error("SamePath: equal paths differ or a sub folder counts as the same")
	}
	if SamePath(filepath.Join(root, "a"), filepath.Join(root, "b")) {
		t.Error("SamePath: different folders are the same")
	}
}

func TestIsOutputMarker(t *testing.T) {
	for name, want := range map[string]bool{
		OutputMarker:               true,
		LegacyOutputMarker:         true,
		"":                         false,
		"library-manager-output":   false,
		".Library-Manager-Output":  false,
		".library-manager-output2": false,
		"song.txt":                 false,
	} {
		if got := IsOutputMarker(name); got != want {
			t.Errorf("IsOutputMarker(%q) = %v, want %v", name, got, want)
		}
	}
}

func TestRemovedDir(t *testing.T) {
	c := OrganizeConfig{OutDir: filepath.Join("o", "out")}
	if got, want := c.RemovedDir(), filepath.Join("o", "out", "removed"); got != want {
		t.Errorf("RemovedDir = %q, want %q", got, want)
	}
}

func TestResolveSource(t *testing.T) {
	root := realRoot(t, "src")
	file := filepath.Join(root, "file.txt")
	testutil.WriteFile(t, file, "x")
	for name, p := range map[string]string{
		"empty":       "",
		"a file":      file,
		"nonexistent": filepath.Join(root, "nope"),
	} {
		got, err := resolveSource(p)
		if err == nil {
			t.Errorf("%s: resolveSource(%q) = %q, want an error", name, p, got)
			continue
		}
		want := "must be an existing directory"
		if p == "" {
			want = "empty source dir"
		}
		if !strings.Contains(err.Error(), want) {
			t.Errorf("%s: error %q does not contain %q", name, err, want)
		}
	}
	link := filepath.Join(root, "link")
	if err := os.Symlink(filepath.Join(root, "src"), link); err != nil {
		t.Skipf("symlinks not supported: %v", err)
	}
	if got, err := resolveSource(link); err != nil || got != filepath.Join(root, "src") {
		t.Errorf("resolveSource(symlink) = %q, %v; want %q", got, err, filepath.Join(root, "src"))
	}
	// A relative path is made absolute.
	t.Chdir(root)
	if got, err := resolveSource("src"); err != nil || got != filepath.Join(root, "src") {
		t.Errorf("resolveSource(src) = %q, %v", got, err)
	}
}
