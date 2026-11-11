package config

import (
	"os"
	"path/filepath"
	"testing"
)

// mkdirs creates root/<rel> for every rel and returns root.
func mkdirs(t *testing.T, rels ...string) string {
	t.Helper()
	root := t.TempDir()
	for _, rel := range rels {
		if err := os.MkdirAll(filepath.Join(root, rel), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	return root
}

func TestNewDefaultOutDir(t *testing.T) {
	root := mkdirs(t, "songs")
	cfg, err := New(filepath.Join(root, "songs")+string(filepath.Separator), "", true)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if want := filepath.Join(root, "songs"); cfg.SongDir != want {
		t.Errorf("SongDir = %q, want %q", cfg.SongDir, want)
	}
	out := filepath.Join(root, DefaultOutDirName)
	if cfg.OutDir != out {
		t.Errorf("OutDir = %q, want %q", cfg.OutDir, out)
	}
	if !cfg.DryRun {
		t.Error("DryRun not kept")
	}
	if want := filepath.Join(out, "invalid"); cfg.InvalidDir() != want {
		t.Errorf("InvalidDir = %q, want %q", cfg.InvalidDir(), want)
	}
	if want := filepath.Join(out, "extra-files"); cfg.ExtraFilesDir() != want {
		t.Errorf("ExtraFilesDir = %q, want %q", cfg.ExtraFilesDir(), want)
	}
	if want := filepath.Join(out, "missing_files.txt"); cfg.ReportPath("missing_files.txt") != want {
		t.Errorf("ReportPath = %q, want %q", cfg.ReportPath("missing_files.txt"), want)
	}
}

func TestNewRejectsOutDirSymlinkedIntoSongDir(t *testing.T) {
	root := mkdirs(t, "songs/Cat")
	link := filepath.Join(root, "out-link")
	if err := os.Symlink(filepath.Join(root, "songs", "Cat"), link); err != nil {
		t.Skipf("symlinks not supported: %v", err)
	}
	if _, err := New(filepath.Join(root, "songs"), filepath.Join(link, "sub"), false); err == nil {
		t.Error("an out dir that resolves into the song dir must be rejected")
	}
}

func TestPrepareOutDir(t *testing.T) {
	root := mkdirs(t, "songs")
	cfg, err := New(filepath.Join(root, "songs"), "", false)
	if err != nil {
		t.Fatal(err)
	}
	if err := cfg.PrepareOutDir(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(cfg.OutDir, OutputMarker)); err != nil {
		t.Errorf("marker missing: %v", err)
	}
}

func TestNewRelativeSongDirIsMadeAbsolute(t *testing.T) {
	root := mkdirs(t, "songs")
	t.Chdir(root)
	cfg, err := New("songs", "", false)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if !filepath.IsAbs(cfg.SongDir) || !filepath.IsAbs(cfg.OutDir) {
		t.Errorf("paths not absolute: %+v", cfg)
	}
}

func TestNewOutDirValidation(t *testing.T) {
	root := mkdirs(t, "songs/Category", "out", "songs-out")
	songs := filepath.Join(root, "songs")
	tests := []struct {
		name    string
		outDir  string
		wantErr bool
	}{
		{"sibling", filepath.Join(root, "out"), false},
		{"sibling with common prefix", filepath.Join(root, "songs-out"), false},
		{"not yet existing sibling", filepath.Join(root, "new-out"), false},
		{"parent", root, false},
		{"same as song dir", songs, true},
		{"inside song dir", filepath.Join(songs, "Category"), true},
		{"inside song dir, not existing", filepath.Join(songs, "sanitizer-output"), true},
		{"inside song dir via ..", filepath.Join(root, "out", "..", "songs", "x"), true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg, err := New(songs, tt.outDir, false)
			if (err != nil) != tt.wantErr {
				t.Fatalf("New(%q, %q) err = %v, wantErr %v", songs, tt.outDir, err, tt.wantErr)
			}
			if err == nil && cfg.OutDir != filepath.Clean(tt.outDir) {
				t.Errorf("OutDir = %q, want %q", cfg.OutDir, filepath.Clean(tt.outDir))
			}
		})
	}
}

func TestNewRejectsMissingOrFileSongDir(t *testing.T) {
	root := t.TempDir()
	file := filepath.Join(root, "file")
	if err := os.WriteFile(file, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	for _, dir := range []string{filepath.Join(root, "missing"), file} {
		if _, err := New(dir, "", false); err == nil {
			t.Errorf("New(%q) succeeded, want error", dir)
		}
	}
}

func TestRelPath(t *testing.T) {
	cfg := Config{SongDir: "/lib/songs", OutDir: "/lib"}
	tests := []struct {
		path, want string
	}{
		{"/lib/songs/Song", "Song"},
		{"/lib/songs/Category/Song", filepath.Join("Category", "Song")},
		{"/lib/songs/Category/Song/cover.jpg", filepath.Join("Category", "Song", "cover.jpg")},
		{"/lib/songs/..hidden", "..hidden"},
		// Outside the library: fall back to the base name.
		{"/lib/songs", "songs"},
		{"/lib/other/Song", "Song"},
		{"/lib/songs-old/Song", "Song"},
		{"/lib", "lib"},
	}
	for _, tt := range tests {
		if got := cfg.RelPath(tt.path); got != tt.want {
			t.Errorf("RelPath(%q) = %q, want %q", tt.path, got, tt.want)
		}
	}
}

func TestIsWithin(t *testing.T) {
	tests := []struct {
		path, root string
		want       bool
	}{
		{"/a/b", "/a/b", true},
		{"/a/b/", "/a/b", true},
		{"/a/b/c", "/a/b", true},
		{"/a/b/c/d", "/a/b", true},
		{"/a/b/..c", "/a/b", true},
		{"/a/bc", "/a/b", false},
		{"/a/b-out", "/a/b", false},
		{"/a", "/a/b", false},
		{"/x/y", "/a/b", false},
		{"/a/b/../c", "/a/b", false},
		{"/", "/a", false},
		{"/a", "/", true},
		{"relative", "/a", false}, // filepath.Rel fails for mixed abs/rel
	}
	for _, tt := range tests {
		if got := IsWithin(tt.path, tt.root); got != tt.want {
			t.Errorf("IsWithin(%q, %q) = %v, want %v", tt.path, tt.root, got, tt.want)
		}
	}
}

func TestOriginalsDirAndJournalPath(t *testing.T) {
	cfg := Config{SongDir: "/lib/songs", OutDir: "/lib/out"}
	if want := filepath.Join("/lib/out", "originals"); cfg.OriginalsDir() != want {
		t.Errorf("OriginalsDir = %q, want %q", cfg.OriginalsDir(), want)
	}
	if want := filepath.Join("/lib/out", "media-journal.txt"); cfg.JournalPath() != want {
		t.Errorf("JournalPath = %q, want %q", cfg.JournalPath(), want)
	}
}

// Processing e.g. <out>/invalid with the same -o would move songs onto themselves.
func TestNewRejectsSongDirInsideOutputSubdirs(t *testing.T) {
	root := mkdirs(t,
		"out/invalid/Cat", "out/extra-files/Cat", "out/originals/Cat",
		"out/invalid-old/Cat", "out/reports",
		"lib/invalid", "lib/songs",
	)
	tests := []struct {
		name, songDir, outDir string
		wantErr               bool
	}{
		{"invalid", "out/invalid", "out", true},
		{"below invalid", "out/invalid/Cat", "out", true},
		{"extra-files", "out/extra-files", "out", true},
		{"below extra-files", "out/extra-files/Cat", "out", true},
		{"originals", "out/originals", "out", true},
		{"below originals", "out/originals/Cat", "out", true},
		{"common prefix with invalid", "out/invalid-old/Cat", "out", false},
		{"other folder of the out dir", "out/reports", "out", false},
		{"invalid with another -o", "out/invalid", "other-out", false},
		// -o = parent of -s is fine, unless -s is named like an output subfolder.
		{"-o is the parent", "lib/songs", "lib", false},
		{"-o is the parent of a song dir named invalid", "lib/invalid", "lib", true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := New(filepath.Join(root, tt.songDir), filepath.Join(root, tt.outDir), false)
			if (err != nil) != tt.wantErr {
				t.Errorf("New(%q, %q) err = %v, wantErr %v", tt.songDir, tt.outDir, err, tt.wantErr)
			}
		})
	}
}

func TestNewRejectsSongDirSymlinkedIntoOutputSubdir(t *testing.T) {
	root := mkdirs(t, "out/invalid/Cat")
	link := filepath.Join(root, "songs-link")
	if err := os.Symlink(filepath.Join(root, "out", "invalid", "Cat"), link); err != nil {
		t.Skipf("symlinks not supported: %v", err)
	}
	if _, err := New(link, filepath.Join(root, "out"), false); err == nil {
		t.Error("a song dir that resolves into <out>/invalid must be rejected")
	}
}
