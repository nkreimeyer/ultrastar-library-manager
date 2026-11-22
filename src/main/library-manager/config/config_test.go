package config

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/nkreimeyer/library-manager/fsops"
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

func TestIsWithinIgnoresCaseOnDarwinAndWindows(t *testing.T) {
	tests := []struct {
		path, root, goos string
		want             bool
	}{
		{"/home/u/karaoke/songs/out", "/home/u/Karaoke/Songs", "darwin", true},
		{"/home/u/karaoke/songs/out", "/home/u/Karaoke/Songs", "windows", true},
		{"/home/u/KARAOKE/SONGS", "/home/u/karaoke/songs", "darwin", true},
		{"/home/u/ÄRZTE/x", "/home/u/ärzte", "darwin", true},
		{"/home/u/A\u0308rzte/out", "/home/u/\u00c4rzte", "darwin", true}, // NFD vs. NFC
		{"/home/u/a\u0308rzte/out", "/home/u/\u00c4RZTE", "darwin", true},
		{"/home/u/A\u0308rzte/out", "/home/u/\u00c4rzte", "windows", false}, // NTFS keeps NFD and NFC apart
		{"/home/u/karaoke/songs/out", "/home/u/Karaoke/Songs", "linux", false},
		{"/home/u/karaoke/songs-out", "/home/u/Karaoke/Songs", "darwin", false},
		{"/home/u/Karaoke", "/home/u/karaoke/songs", "darwin", false},
	}
	for _, tt := range tests {
		// On a Windows host filepath.Rel itself ignores case, so a case-only difference is
		// within the root whatever goos says: the case-sensitive linux row cannot hold there.
		if tt.goos == "linux" && runtime.GOOS == "windows" {
			continue
		}
		if got := isWithin(tt.path, tt.root, tt.goos); got != tt.want {
			t.Errorf("isWithin(%q, %q, %s) = %v, want %v", tt.path, tt.root, tt.goos, got, tt.want)
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

// The not yet existing tail of the out dir is appended in order, also below a symlink.
func TestNewOutDirSeveralMissingComponents(t *testing.T) {
	root, err := filepath.EvalSymlinks(mkdirs(t, "songs", "target"))
	if err != nil {
		t.Fatal(err)
	}
	songs := filepath.Join(root, "songs")
	cfg, err := New(songs, filepath.Join(root, "new1", "new2", "out"), false)
	if err != nil {
		t.Fatal(err)
	}
	if want := filepath.Join(root, "new1", "new2", "out"); cfg.OutDir != want {
		t.Errorf("OutDir = %q, want %q", cfg.OutDir, want)
	}
	link := filepath.Join(root, "link")
	if err := os.Symlink(filepath.Join(root, "target"), link); err != nil {
		t.Skipf("symlinks not supported: %v", err)
	}
	cfg, err = New(songs, filepath.Join(link, "a", "b"), false)
	if err != nil {
		t.Fatal(err)
	}
	if want := filepath.Join(root, "target", "a", "b"); cfg.OutDir != want {
		t.Errorf("OutDir via symlink = %q, want %q", cfg.OutDir, want)
	}
}

func TestNewRejectsDanglingSymlinkOutDir(t *testing.T) {
	root := mkdirs(t, "songs")
	link := filepath.Join(root, "dangling")
	if err := os.Symlink(filepath.Join(root, "missing"), link); err != nil {
		t.Skipf("symlinks not supported: %v", err)
	}
	for _, out := range []string{link, filepath.Join(link, "sub")} {
		if cfg, err := New(filepath.Join(root, "songs"), out, false); err == nil || !strings.Contains(err.Error(), "resolving output dir") {
			t.Errorf("New(-o %q) = %+v, %v; want a resolving error", out, cfg, err)
		}
	}
}

// PrepareOutDir removes only stale temp files of reports/the journal (named as WriteReport
// names them, older than fsops.StaleTempAge), directly in an out dir that already holds the
// output marker.
func TestPrepareOutDirRemovesOnlyStaleTempFiles(t *testing.T) {
	root := mkdirs(t, "songs", "out/sub")
	cfg, err := New(filepath.Join(root, "songs"), filepath.Join(root, "out"), false)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(cfg.OutDir, OutputMarker), nil, 0o644); err != nil {
		t.Fatal(err)
	}
	stale := filepath.Join(cfg.OutDir, ".report.txt.123.tmp")
	fresh := filepath.Join(cfg.OutDir, ".check_report.json.456.tmp") // maybe another run's write
	keep := []string{
		fresh,
		filepath.Join(cfg.OutDir, "report.txt"),
		filepath.Join(cfg.OutDir, "media-journal.txt"),
		filepath.Join(cfg.OutDir, "notes.tmp"),  // not hidden: not one of ours
		filepath.Join(cfg.OutDir, ".notes.tmp"), // a user's file: no random part
		filepath.Join(cfg.OutDir, ".x.abc.tmp"), // the random part is digits only
		filepath.Join(cfg.OutDir, "sub", ".x.123.tmp"),
	}
	old := time.Now().Add(-2 * fsops.StaleTempAge)
	for _, f := range append([]string{stale}, keep...) {
		if err := os.WriteFile(f, []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
		if f != fresh {
			if err := os.Chtimes(f, old, old); err != nil {
				t.Fatal(err)
			}
		}
	}
	if err := cfg.PrepareOutDir(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(stale); !os.IsNotExist(err) {
		t.Errorf("stale temp file not removed: %v", err)
	}
	for _, f := range keep {
		if _, err := os.Stat(f); err != nil {
			t.Errorf("%s removed: %v", f, err)
		}
	}
	data, err := os.ReadFile(filepath.Join(cfg.OutDir, OutputMarker))
	if err != nil || len(data) == 0 {
		t.Errorf("marker = %q, %v", data, err)
	}
}

// A folder that is not an output folder yet (a general folder given as -o) can hold no
// temp files of ours: another program's ".<name>.<digits>.tmp" there stays. In a folder
// with the legacy marker of the old program name the stale temp files are ours.
func TestPrepareOutDirSweepsOnlyOwnOutputFolder(t *testing.T) {
	for _, tc := range []struct {
		marker  string
		removed bool
	}{
		{"", false},
		{LegacyOutputMarker, true},
		{OutputMarker, true},
	} {
		root := mkdirs(t, "songs", "home")
		cfg, err := New(filepath.Join(root, "songs"), filepath.Join(root, "home"), true)
		if err != nil {
			t.Fatal(err)
		}
		if tc.marker != "" {
			if err := os.WriteFile(filepath.Join(cfg.OutDir, tc.marker), nil, 0o644); err != nil {
				t.Fatal(err)
			}
		}
		foreign := filepath.Join(cfg.OutDir, ".session.483920.tmp")
		if err := os.WriteFile(foreign, []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
		old := time.Now().Add(-2 * fsops.StaleTempAge)
		if err := os.Chtimes(foreign, old, old); err != nil {
			t.Fatal(err)
		}
		if err := cfg.PrepareOutDir(); err != nil {
			t.Fatal(err)
		}
		if _, err := os.Stat(foreign); (err != nil) != tc.removed {
			t.Errorf("marker %q: temp file removed = %v, want %v", tc.marker, err != nil, tc.removed)
		}
	}
}

func TestPrepareOutDirErrors(t *testing.T) {
	root := mkdirs(t, "songs", "ro")
	file := filepath.Join(root, "file")
	if err := os.WriteFile(file, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	// The out dir cannot be created below a file.
	if err := (Config{OutDir: filepath.Join(file, "out")}).PrepareOutDir(); err == nil || !strings.Contains(err.Error(), "creating output dir") {
		t.Errorf("PrepareOutDir below a file: %v", err)
	}
	// The marker cannot be written into a read-only out dir.
	ro := filepath.Join(root, "ro")
	if err := os.Chmod(ro, 0o555); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.Chmod(ro, 0o755) })
	if f, err := os.Create(filepath.Join(ro, "probe")); err == nil {
		f.Close()
		t.Skip("running as root: read-only dirs are writable")
	}
	if err := (Config{OutDir: ro}).PrepareOutDir(); err == nil || !strings.Contains(err.Error(), OutputMarker) {
		t.Errorf("PrepareOutDir into a read-only dir: %v", err)
	}
}

func TestIsOutputDir(t *testing.T) {
	root := t.TempDir()
	for _, tc := range []struct {
		marker string
		want   bool
	}{{"", false}, {OutputMarker, true}, {LegacyOutputMarker, true}, {".other-marker", false}} {
		dir := filepath.Join(root, "d"+tc.marker)
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
		if tc.marker != "" {
			if err := os.WriteFile(filepath.Join(dir, tc.marker), nil, 0o644); err != nil {
				t.Fatal(err)
			}
		}
		if got, err := IsOutputDir(dir); got != tc.want || err != nil {
			t.Errorf("marker %q: %v, %v; want %v", tc.marker, got, err, tc.want)
		}
	}
	if got, err := IsOutputDir(filepath.Join(root, "missing")); got || err != nil {
		t.Errorf("missing dir: %v, %v", got, err)
	}
	// PrepareOutDir makes an output folder.
	c := Config{OutDir: filepath.Join(root, "out")}
	if err := c.PrepareOutDir(); err != nil {
		t.Fatal(err)
	}
	if got, err := IsOutputDir(c.OutDir); !got || err != nil {
		t.Errorf("after PrepareOutDir: %v, %v", got, err)
	}
}
