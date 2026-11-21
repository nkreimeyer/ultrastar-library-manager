package testutil

import (
	"bytes"
	"go/parser"
	"go/token"
	"image/png"
	"io/fs"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

const importPath = "github.com/nkreimeyer/library-manager/internal/testutil"

// testutil is for tests only: no file of the program may import it.
func TestNoNonTestImports(t *testing.T) {
	root := filepath.Join("..", "..")
	checked := 0
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			if name := d.Name(); path != root && (strings.HasPrefix(name, ".") || name == "testdata") {
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		f, err := parser.ParseFile(token.NewFileSet(), path, nil, parser.ImportsOnly)
		if err != nil {
			return err
		}
		checked++
		for _, imp := range f.Imports {
			if p, _ := strconv.Unquote(imp.Path.Value); p == importPath {
				t.Errorf("%s imports %s, which is for _test.go files only", path, importPath)
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if checked < 20 {
		t.Fatalf("checked only %d files: wrong module root %s?", checked, root)
	}
}

func TestWriteFilesTreeSnapshot(t *testing.T) {
	root := t.TempDir()
	WriteFiles(t, root, map[string]string{"A/b.txt": "b", "c.txt": "c"})
	want := map[string]string{"A": "<dir>", "A/b.txt": "b", "c.txt": "c"}
	got := Tree(t, root)
	if len(got) != len(want) {
		t.Fatalf("Tree = %v, want %v", got, want)
	}
	for k, v := range want {
		if got[k] != v {
			t.Errorf("Tree[%q] = %q, want %q", k, got[k], v)
		}
	}
	before := Snapshot(t, root)
	if again := Snapshot(t, root); again != before {
		t.Errorf("Snapshot is not stable:\n%s\n%s", before, again)
	}
	WriteFile(t, filepath.Join(root, "A", "b.txt"), "B")
	if after := Snapshot(t, root); after == before {
		t.Error("Snapshot missed a changed file")
	}
}

func TestNotesTxtDiffersPerLyric(t *testing.T) {
	if NotesTxt("a") == NotesTxt("b") {
		t.Error("NotesTxt ignores the lyric")
	}
	if n := strings.Count(NotesTxt("la"), "\n: "); n != 10 {
		t.Errorf("NotesTxt has %d notes, want 10", n)
	}
}

func TestPNG(t *testing.T) {
	cfg, err := png.DecodeConfig(bytes.NewReader([]byte(PNG(16, 9))))
	if err != nil || cfg.Width != 16 || cfg.Height != 9 {
		t.Errorf("PNG(16, 9) decodes as %+v, %v", cfg, err)
	}
}
