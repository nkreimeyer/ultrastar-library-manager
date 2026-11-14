package config

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

// realRoot is mkdirs with the root resolved (t.TempDir may lie below a symlink).
func realRoot(t *testing.T, rels ...string) string {
	t.Helper()
	root, err := filepath.EvalSymlinks(mkdirs(t, rels...))
	if err != nil {
		t.Fatal(err)
	}
	return root
}

func TestNewOrganize(t *testing.T) {
	root := realRoot(t, "m/hq", "m/low1", "m/low2", "other/hq")
	j := func(rel string) string { return filepath.Join(root, filepath.FromSlash(rel)) }
	cfg, err := NewOrganize([][]string{{j("m/hq") + string(filepath.Separator)}, {j("m/low1"), j("m/low2"), j("other/hq")}},
		j("m/library"), "", true)
	if err != nil {
		t.Fatal(err)
	}
	want := []Source{
		{Path: j("m/hq"), Label: "hq", Tier: 0, Index: 0},
		{Path: j("m/low1"), Label: "low1", Tier: 1, Index: 1},
		{Path: j("m/low2"), Label: "low2", Tier: 1, Index: 2},
		{Path: j("other/hq"), Label: "hq (2)", Tier: 1, Index: 3},
	}
	if !reflect.DeepEqual(cfg.Sources, want) {
		t.Errorf("Sources = %+v\nwant %+v", cfg.Sources, want)
	}
	out := j("m/" + DefaultOrganizeOutDirName)
	if cfg.Library != j("m/library") || cfg.OutDir != out || !cfg.DryRun {
		t.Errorf("cfg = %+v", cfg)
	}
	if _, err := os.Stat(cfg.Library); !os.IsNotExist(err) {
		t.Errorf("NewOrganize created the library: %v", err)
	}
	for got, want := range map[string]string{
		cfg.InvalidDir():                                  filepath.Join(out, "invalid"),
		cfg.DuplicatesDir():                               filepath.Join(out, "duplicates"),
		cfg.JournalPath():                                 filepath.Join(out, "organize-journal.jsonl"),
		cfg.ReportPath("organize_plan.txt"):               filepath.Join(out, "organize_plan.txt"),
		cfg.RelPath(cfg.Sources[1], j("m/low1/Cat/Song")): filepath.Join("Cat", "Song"),
		cfg.RelPath(cfg.Sources[1], j("m/low2/Song")):     "Song", // not below the source
		cfg.RelPath(cfg.Sources[1], j("m/low1")):          "low1",
	} {
		if got != want {
			t.Errorf("got %q, want %q", got, want)
		}
	}
}

func TestNewOrganizeLibraryIsASource(t *testing.T) {
	root := realRoot(t, "library", "new")
	lib := filepath.Join(root, "library")
	cfg, err := NewOrganize([][]string{{lib}, {filepath.Join(root, "new")}}, lib, filepath.Join(root, "out"), false)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Library != lib || cfg.Sources[0].Path != lib || cfg.OutDir != filepath.Join(root, "out") {
		t.Errorf("cfg = %+v", cfg)
	}
}

func TestNewOrganizeResolvesSymlinks(t *testing.T) {
	root := realRoot(t, "real/src", "real/lib")
	for _, l := range []struct{ link, target string }{{"src-link", "real/src"}, {"lib-link", "real/lib"}, {"out-link", "real"}} {
		if err := os.Symlink(filepath.Join(root, l.target), filepath.Join(root, l.link)); err != nil {
			t.Skipf("symlinks not supported: %v", err)
		}
	}
	cfg, err := NewOrganize([][]string{{filepath.Join(root, "src-link")}}, filepath.Join(root, "lib-link"), filepath.Join(root, "x"), false)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Sources[0].Path != filepath.Join(root, "real/src") || cfg.Sources[0].Label != "src" || cfg.Library != filepath.Join(root, "real/lib") {
		t.Errorf("cfg = %+v", cfg)
	}
	// The same folder through a link is given twice; an out dir that resolves into a source.
	if _, err := NewOrganize([][]string{{filepath.Join(root, "src-link")}, {filepath.Join(root, "real/src")}}, filepath.Join(root, "lib"), filepath.Join(root, "x"), false); err == nil || !strings.Contains(err.Error(), "twice") {
		t.Errorf("source via symlink twice: %v", err)
	}
	if _, err := NewOrganize([][]string{{filepath.Join(root, "real/src")}}, filepath.Join(root, "lib"), filepath.Join(root, "out-link", "src", "out"), false); err == nil {
		t.Error("an out dir resolving into a source must be rejected")
	}
}

func TestNewOrganizeUndoWithoutSources(t *testing.T) {
	root := realRoot(t, "lib")
	cfg, err := NewOrganize(nil, "", filepath.Join(root, "out"), false)
	if err != nil || cfg.Library != "" || cfg.OutDir != filepath.Join(root, "out") || len(cfg.Sources) != 0 {
		t.Errorf("undo -o: %+v, %v", cfg, err)
	}
	cfg, err = NewOrganize(nil, filepath.Join(root, "lib"), "", false)
	if err != nil || cfg.OutDir != filepath.Join(root, DefaultOrganizeOutDirName) {
		t.Errorf("undo -l: %+v, %v", cfg, err)
	}
}

func TestNewOrganizeValidation(t *testing.T) {
	root := realRoot(t, "m/a", "m/a/inner", "m/b", "m/lib/sub", "m/out/invalid/x", "file-parent")
	if err := os.WriteFile(filepath.Join(root, "m/file"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	j := func(rel string) string {
		if rel == "" {
			return ""
		}
		return filepath.Join(root, filepath.FromSlash(rel))
	}
	tests := []struct {
		name      string
		tiers     [][]string
		lib, out  string
		wantInErr string
	}{
		{"missing source", [][]string{{j("m/nope")}}, "m/lib", "o", "existing directory"},
		{"file source", [][]string{{j("m/file")}}, "m/lib", "o", "existing directory"},
		{"empty source path", [][]string{{""}}, "m/lib", "o", "empty source"},
		{"empty tier", [][]string{{j("m/a")}, {}}, "m/lib", "o", "source 2 is empty"},
		{"source twice", [][]string{{j("m/a")}, {j("m/a") + "/"}}, "m/lib", "o", "twice"},
		{"source inside source", [][]string{{j("m/a")}, {j("m/a/inner")}}, "m/lib", "o", "inside the source dir"},
		{"source containing an earlier source", [][]string{{j("m/a/inner")}, {j("m/a")}}, "m/lib", "o", "inside the source dir"},
		{"no library and no out", [][]string{{j("m/a")}}, "", "", "library dir"},
		{"library is a file", [][]string{{j("m/a")}}, "m/file", "o", "not a directory"},
		{"library inside a source", [][]string{{j("m/a")}}, "m/a/lib", "o", "library dir"},
		{"library contains a source", [][]string{{j("m/lib/sub")}}, "m/lib", "o", "inside the library dir"},
		{"out inside the library", [][]string{{j("m/a")}}, "m/lib", "m/lib/out", "inside the library dir"},
		{"out equals the library", [][]string{{j("m/a")}}, "m/lib", "m/lib", "inside the library dir"},
		{"out inside a source", [][]string{{j("m/a")}, {j("m/b")}}, "m/lib", "m/b/out", "inside the source dir"},
		{"source inside out", [][]string{{j("m/out/invalid/x")}}, "m/lib", "m/out", "use a different -o"},
		{"library inside out", [][]string{{j("m/a")}}, "m/out/invalid/x", "m/out", "use a different -o"},
		{"out is a file", [][]string{{j("m/a")}}, "m/lib", "m/file", "not a directory"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg, err := NewOrganize(tt.tiers, j(tt.lib), j(tt.out), false)
			if err == nil {
				t.Fatalf("NewOrganize = %+v, want an error containing %q", cfg, tt.wantInErr)
			}
			if !strings.Contains(err.Error(), tt.wantInErr) {
				t.Errorf("err = %v, want it to contain %q", err, tt.wantInErr)
			}
		})
	}
}

func TestUniqueLabel(t *testing.T) {
	taken := map[string]bool{}
	var got []string
	for _, n := range []string{"songs", "Songs", "songs (2)", "songs", "other"} {
		got = append(got, uniqueLabel(n, taken))
	}
	want := []string{"songs", "Songs (2)", "songs (2) (2)", "songs (3)", "other"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("labels = %q, want %q", got, want)
	}
}

func TestOrganizePrepareOutDir(t *testing.T) {
	root := realRoot(t)
	cfg, err := NewOrganize(nil, filepath.Join(root, "lib"), "", false)
	if err != nil {
		t.Fatal(err)
	}
	if err := cfg.PrepareOutDir(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(cfg.OutDir, OutputMarker)); err != nil {
		t.Errorf("marker missing: %v", err)
	}
	if _, err := os.Stat(cfg.Library); !os.IsNotExist(err) {
		t.Errorf("PrepareOutDir created the library: %v", err)
	}
}

func TestNewOrganizeRejectsDanglingSymlinks(t *testing.T) {
	root := realRoot(t, "src")
	link := filepath.Join(root, "dangling")
	if err := os.Symlink(filepath.Join(root, "missing"), link); err != nil {
		t.Skipf("symlinks not supported: %v", err)
	}
	src := [][]string{{filepath.Join(root, "src")}}
	if _, err := NewOrganize(src, link, filepath.Join(root, "out"), false); err == nil || !strings.Contains(err.Error(), "resolving library dir") {
		t.Errorf("dangling -l: %v", err)
	}
	if _, err := NewOrganize(src, filepath.Join(root, "lib"), filepath.Join(link, "out"), false); err == nil || !strings.Contains(err.Error(), "resolving output dir") {
		t.Errorf("dangling -o: %v", err)
	}
	if _, err := NewOrganize([][]string{{link}}, filepath.Join(root, "lib"), filepath.Join(root, "out"), false); err == nil || !strings.Contains(err.Error(), "existing directory") {
		t.Errorf("dangling -s: %v", err)
	}
}

// Labels become folder names below <out>/invalid and <out>/duplicates. Names that differ
// only in Unicode normalization are the same folder on macOS, so they must not share a label.
func TestNewOrganizeLabelsNormalizationInsensitive(t *testing.T) {
	nfc, nfd := "Mädchen", "Mädchen"
	root := realRoot(t, "a/"+nfc, "b/"+nfd)
	cfg, err := NewOrganize([][]string{{filepath.Join(root, "a", nfc)}, {filepath.Join(root, "b", nfd)}}, filepath.Join(root, "lib"), "", false)
	if err != nil {
		t.Fatal(err)
	}
	if got := []string{cfg.Sources[0].Label, cfg.Sources[1].Label}; !reflect.DeepEqual(got, []string{nfc, nfd + " (2)"}) {
		t.Errorf("labels = %q, want %q", got, []string{nfc, nfd + " (2)"})
	}
}
