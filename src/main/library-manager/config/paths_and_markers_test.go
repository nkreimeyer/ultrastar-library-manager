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

// OutsideSongDir: a path equal to the song dir, a not-yet-existing child below it, and a
// path reached through a symlink into it are refused; the error names the song dir.
func TestOutsideSongDirRefusals(t *testing.T) {
	root := mkdirs(t, "songs/Cat", "other")
	cfg, err := New(filepath.Join(root, "songs"), "", true)
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range []string{
		filepath.Join(root, "songs", "Cat"),
		filepath.Join(root, "songs", "does", "not", "exist"),
		filepath.Join(root, "songs", "Cat", "..", "Cat", "x"),
	} {
		got, err := cfg.OutsideSongDir(p)
		if err == nil || !strings.Contains(err.Error(), "must not be inside the song dir") {
			t.Errorf("OutsideSongDir(%q) = %q, %v", p, got, err)
		}
	}
	// A dangling symlink outside, whose target is inside the song dir.
	link := filepath.Join(root, "other", "dangling")
	if err := os.Symlink(filepath.Join(root, "songs", "new"), link); err != nil {
		t.Skipf("symlinks not supported: %v", err)
	}
	if got, err := cfg.OutsideSongDir(link); err == nil {
		t.Errorf("a dangling link into the song dir is accepted: %q", got)
	}
	// A relative path is made absolute.
	t.Chdir(filepath.Join(root, "other"))
	got, err := cfg.OutsideSongDir("rel")
	if err != nil || !filepath.IsAbs(got) {
		t.Errorf("OutsideSongDir(rel) = %q, %v", got, err)
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
