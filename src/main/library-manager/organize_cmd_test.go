package main

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/nkreimeyer/library-manager/internal/testutil"
)

func TestTierList(t *testing.T) {
	var tl tierList
	if tl.String() != "" {
		t.Errorf("empty = %q", tl.String())
	}
	sep := string(filepath.ListSeparator)
	for _, v := range []string{"a" + sep + "b", "c"} {
		if err := tl.Set(v); err != nil {
			t.Fatal(err)
		}
	}
	if len(tl) != 2 || len(tl[0]) != 2 || tl[1][0] != "c" {
		t.Errorf("tiers %v", [][]string(tl))
	}
	if got, want := tl.String(), "a"+sep+"b c"; got != want {
		t.Errorf("String = %q, want %q", got, want)
	}
}

func TestRunOrganizeExitCodes(t *testing.T) {
	t.Setenv("PATH", t.TempDir())
	base := t.TempDir()
	out := filepath.Join(base, "out")
	tests := []struct {
		name string
		args []string
		want int
	}{
		{"help", []string{"organize", "-h"}, exitOK},
		{"undo with -ignore-the", []string{"organize", "-undo", "-ignore-the", "-o", out}, exitUsage},
		{"undo with -deep", []string{"organize", "-undo", "-deep", "-o", out}, exitUsage},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := runWithArgs(t, tt.args...); got != tt.want {
				t.Errorf("run(%q) = %d, want %d", tt.args, got, tt.want)
			}
		})
	}
	if _, err := os.Stat(out); !os.IsNotExist(err) {
		t.Errorf("out dir created by a usage error (err=%v)", err)
	}
}

func TestRunOrganizeFailures(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root can read every file")
	}
	t.Setenv("PATH", t.TempDir())
	newSource := func(t *testing.T) (src, lib string) {
		base := t.TempDir()
		src, lib = filepath.Join(base, "src"), filepath.Join(base, "lib")
		testutil.WriteFiles(t, filepath.Join(src, "ABBA - SOS"), map[string]string{
			"song.txt": "#ARTIST:ABBA\n#TITLE:SOS\n#MP3:a.mp3\n" + validNotes(), "a.mp3": "a",
		})
		return
	}
	t.Run("success logs no error", func(t *testing.T) {
		src, lib := newSource(t)
		var code int
		msgs := testutil.ErrorLogs(t, func() { code = runWithArgs(t, "organize", "-s", src, "-l", lib) })
		if code != exitOK {
			t.Errorf("exit code %d, want %d", code, exitOK)
		}
		if len(msgs) != 0 {
			t.Errorf("error logs %q, want none", msgs)
		}
	})
	t.Run("unreadable song txt", func(t *testing.T) {
		src, lib := newSource(t)
		testutil.WriteFiles(t, filepath.Join(src, "Locked"), map[string]string{"song.txt": "#ARTIST:X\n#TITLE:Y\n", "a.mp3": "a"})
		txt := filepath.Join(src, "Locked", "song.txt")
		if err := os.Chmod(txt, 0); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { os.Chmod(txt, 0o644) })
		if got := runWithArgs(t, "organize", "-s", src, "-l", lib); got != exitFailures {
			t.Errorf("exit code %d, want %d", got, exitFailures)
		}
		if _, err := os.Stat(filepath.Join(src, "Locked", "a.mp3")); err != nil {
			t.Errorf("unreadable song was moved: %v", err)
		}
	})
	t.Run("out dir below a file", func(t *testing.T) {
		src, lib := newSource(t)
		file := filepath.Join(t.TempDir(), "file")
		if err := os.WriteFile(file, []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
		if got := runWithArgs(t, "organize", "-s", src, "-l", lib, "-o", filepath.Join(file, "out")); got != exitFailures {
			t.Errorf("exit code %d, want %d", got, exitFailures)
		}
		if _, err := os.Stat(filepath.Join(src, "ABBA - SOS", "song.txt")); err != nil {
			t.Errorf("song moved although the out dir could not be prepared: %v", err)
		}
	})
}

// The startup log names every source with its 1-based priority tier (the position of its
// -s flag), so the user can check the order before anything moves.
func TestRunOrganizeLogsSourceTiers(t *testing.T) {
	t.Setenv("PATH", t.TempDir())
	base, err := filepath.EvalSymlinks(t.TempDir()) // the log shows resolved paths
	if err != nil {
		t.Fatal(err)
	}
	first, second, third := filepath.Join(base, "first"), filepath.Join(base, "second"), filepath.Join(base, "third")
	for _, dir := range []string{first, second, third} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	lib := filepath.Join(base, "lib")
	hook := testutil.CaptureLogs(t)
	if code := runWithArgs(t, "organize", "-n", "-s", first, "-s", second+string(filepath.ListSeparator)+third, "-l", lib); code != exitOK {
		t.Fatalf("exit code %d, want %d", code, exitOK)
	}
	var got []string
	for _, e := range hook.AllEntries() {
		if strings.HasPrefix(e.Message, "Source (tier ") {
			got = append(got, e.Message)
		}
	}
	want := []string{
		"Source (tier 1): " + first,
		"Source (tier 2): " + second,
		"Source (tier 2): " + third,
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("source log lines = %q, want %q", got, want)
	}
}
