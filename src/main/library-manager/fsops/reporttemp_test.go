package fsops

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

// RemoveStaleReportTemps removes only old temp files with WriteReport's names, directly in
// the folder.
func TestRemoveStaleReportTemps(t *testing.T) {
	dir := t.TempDir()
	if err := os.Mkdir(filepath.Join(dir, "sub"), 0o755); err != nil {
		t.Fatal(err)
	}
	old := time.Now().Add(-StaleTempAge - time.Minute)
	files := map[string]struct {
		old, removed bool
	}{
		".report.txt.123.tmp":         {old: true, removed: true},
		".organize_plan.json.9.tmp":   {old: true, removed: true},
		"..hidden.txt.4294967295.tmp": {old: true, removed: true},
		".report.txt.124.tmp":         {old: false, removed: false}, // fresh: maybe another run's
		".notes.tmp":                  {old: true, removed: false},
		"notes.tmp":                   {old: true, removed: false},
		".report.txt.12a.tmp":         {old: true, removed: false},
		".report.txt..tmp":            {old: true, removed: false},
		"..123.tmp":                   {old: true, removed: false},
		".report.txt.123.tmp.bak":     {old: true, removed: false},
		"sub/.report.txt.123.tmp":     {old: true, removed: false},
	}
	for name, f := range files {
		p := filepath.Join(dir, filepath.FromSlash(name))
		if err := os.WriteFile(p, []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
		if f.old {
			if err := os.Chtimes(p, old, old); err != nil {
				t.Fatal(err)
			}
		}
	}
	// A folder with a temp name is not a file of ours.
	dirTemp := filepath.Join(dir, ".dir.txt.5.tmp")
	if err := os.Mkdir(dirTemp, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(dirTemp, old, old); err != nil {
		t.Fatal(err)
	}

	RemoveStaleReportTemps(dir)

	for name, f := range files {
		_, err := os.Stat(filepath.Join(dir, filepath.FromSlash(name)))
		if gone := os.IsNotExist(err); gone != f.removed {
			t.Errorf("%s: removed = %v, want %v (%v)", name, gone, f.removed, err)
		}
	}
	if _, err := os.Stat(dirTemp); err != nil {
		t.Errorf("folder %s removed: %v", dirTemp, err)
	}
	RemoveStaleReportTemps(filepath.Join(dir, "missing")) // no panic, nothing to do
}

// The temp names WriteReport and WriteFileAtomic create (tempPattern) match isReportTempName.
func TestWriteReportTempNameMatches(t *testing.T) {
	for _, base := range []string{"report.txt", ".hidden", "organize-journal.jsonl", "x"} {
		dir := t.TempDir()
		f, err := os.CreateTemp(dir, tempPattern(base))
		if err != nil {
			t.Fatal(err)
		}
		f.Close()
		if name := filepath.Base(f.Name()); !isReportTempName(name) {
			t.Errorf("isReportTempName(%q) = false for a WriteReport temp name", name)
		}
	}
}
