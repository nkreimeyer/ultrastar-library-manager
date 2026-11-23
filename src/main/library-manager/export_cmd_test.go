package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/nkreimeyer/library-manager/internal/testutil"
)

// export writes <out>/song_list.csv, only reads the library and reports the count.
func TestRunExport(t *testing.T) {
	base, lib, _, _ := cliFixture(t)
	out := filepath.Join(base, "out")
	before := testutil.Snapshot(t, lib)
	code, _, logs := runCaptured(t, "export", "-l", lib, "-o", out)
	if code != exitOK {
		t.Fatalf("export = %d\n%s", code, strings.Join(logs, "\n"))
	}
	containsAll(t, "export", logs, []string{"Library: " + lib, "Wrote 2 songs to " + filepath.Join(out, "song_list.csv"), "Done: 2 songs, 0 failed operations"})
	data, err := os.ReadFile(filepath.Join(out, "song_list.csv"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(string(data), "\xEF\xBB\xBFArtist,Title,Year,Genre,BPM,Language,Length (s),Duet\n") {
		t.Errorf("CSV:\n%s", data)
	}
	if testutil.Snapshot(t, lib) != before {
		t.Error("export changed the library")
	}
}

func TestRunExportCSVFile(t *testing.T) {
	base, lib, _, _ := cliFixture(t)
	file := filepath.Join(base, "list", "mine.csv")
	code, _, logs := runCaptured(t, "export", "-l", lib, "-csv", file, "-bom=false")
	if code != exitOK {
		t.Fatalf("export = %d\n%s", code, strings.Join(logs, "\n"))
	}
	data, err := os.ReadFile(file)
	if err != nil || !strings.HasPrefix(string(data), "Artist,Title,") {
		t.Errorf("CSV %q, %v", data, err)
	}
	if _, err := os.Stat(filepath.Join(base, "organize-output")); !os.IsNotExist(err) {
		t.Errorf("-csv created the default output folder: %v", err)
	}
}

func TestRunExportUsageErrors(t *testing.T) {
	base, lib, _, _ := cliFixture(t)
	for name, tt := range map[string]struct {
		args []string
		want string
	}{
		"no library":     {[]string{"export"}, "Error: -l <library> is required."},
		"-o with -csv":   {[]string{"export", "-l", lib, "-o", filepath.Join(base, "o"), "-csv", filepath.Join(base, "x.csv")}, "Error: -o and -csv cannot be combined"},
		"leftover":       {[]string{"export", "-l", lib, "-bom", "false"}, "Error: unexpected arguments [\"false\"].\nBool flags must be written as -bom=false"},
		"csv in library": {[]string{"export", "-l", lib, "-csv", filepath.Join(lib, "list.csv")}, ""},
		"missing lib":    {[]string{"export", "-l", filepath.Join(base, "nope")}, ""},
	} {
		code, output, _ := runCaptured(t, tt.args...)
		if code != exitUsage || !strings.Contains(output, tt.want) {
			t.Errorf("%s: exit %d, output:\n%s", name, code, output)
		}
	}
	if _, err := os.Stat(filepath.Join(lib, "list.csv")); err == nil {
		t.Error("a CSV was written into the library")
	}
}
