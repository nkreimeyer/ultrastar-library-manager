package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/nkreimeyer/library-manager/check"

	"github.com/nkreimeyer/library-manager/internal/testutil"
)

// fileNames returns the names of the regular files directly in dir, sorted.
func fileNames(t *testing.T, dir string) []string {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, e := range entries {
		if e.Type().IsRegular() {
			names = append(names, e.Name())
		}
	}
	return names
}

func validNotes() string {
	s := "#BPM:300\n"
	for i := range 10 {
		s += fmt.Sprintf(": %d 2 0 la\n", i*4)
	}
	return s + "E\n"
}

func TestRunUsageErrors(t *testing.T) {
	t.Setenv("PATH", t.TempDir())
	lib := t.TempDir()
	file := filepath.Join(t.TempDir(), "file")
	if err := os.WriteFile(file, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	tests := []struct {
		name string
		args []string
	}{
		{"missing song dir", []string{"-s", filepath.Join(lib, "missing")}},
		{"song dir is a file", []string{"-s", file}},
		{"out dir inside the song dir", []string{"-s", lib, "-o", filepath.Join(lib, "out")}},
		{"bad log level", []string{"-s", lib, "-o", t.TempDir(), "-v", "loud"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := runWithArgs(t, tt.args...); got != exitUsage {
				t.Errorf("run(%q) = %d, want %d", tt.args, got, exitUsage)
			}
		})
	}
	if _, err := os.Stat(filepath.Join(lib, "out")); !os.IsNotExist(err) {
		t.Errorf("out dir inside the song dir was created (err=%v)", err)
	}
}

func TestRunExitFailures(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root can read every file")
	}
	t.Setenv("PATH", t.TempDir())
	file := filepath.Join(t.TempDir(), "file")
	if err := os.WriteFile(file, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Run("out dir below a file", func(t *testing.T) {
		if got := runWithArgs(t, "-s", t.TempDir(), "-o", filepath.Join(file, "out")); got != exitFailures {
			t.Errorf("exit code %d, want %d", got, exitFailures)
		}
	})
	t.Run("unreadable song txt", func(t *testing.T) {
		lib := t.TempDir()
		song := filepath.Join(lib, "A - B")
		testutil.WriteFiles(t, song, map[string]string{"song.txt": "#ARTIST:A\n#TITLE:B\n#MP3:a.mp3\n" + validNotes(), "a.mp3": "a"})
		txt := filepath.Join(song, "song.txt")
		if err := os.Chmod(txt, 0); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { os.Chmod(txt, 0o644) })
		if got := runWithArgs(t, "-s", lib, "-o", t.TempDir()); got != exitFailures {
			t.Errorf("exit code %d, want %d", got, exitFailures)
		}
	})
	// The song dir itself cannot be listed: every module fails to run (no failed
	// operation is counted), which is exit code 1 as well.
	t.Run("unreadable song dir", func(t *testing.T) {
		lib := t.TempDir()
		if err := os.Chmod(lib, 0o300); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { os.Chmod(lib, 0o755) })
		var code int
		msgs := testutil.ErrorLogs(t, func() { code = runWithArgs(t, "-s", lib, "-o", t.TempDir()) })
		if code != exitFailures {
			t.Errorf("exit code %d, want %d", code, exitFailures)
		}
		for _, m := range []string{"fixtxt failed", "cleanup failed"} {
			if !testutil.ContainsPrefix(msgs, m) {
				t.Errorf("error logs %q, want one starting with %q", msgs, m)
			}
		}
	})
}

func TestModuleListEnabled(t *testing.T) {
	patterns := [][5]bool{
		{true, false, false, true, false},
		{false, true, true, false, true},
		{false, false, false, false, false},
	}
	for _, p := range patterns {
		got := map[string]bool{}
		for _, m := range moduleList(p[0], p[1], p[2], p[3], p[4], check.Options{}, nil) {
			got[m.name] = m.enabled
		}
		want := map[string]bool{"fixtxt": p[0], "cleanup": p[1], "check": p[2], "fixvideos": p[3], "fixaudio": p[4]}
		if !reflect.DeepEqual(got, want) {
			t.Errorf("moduleList(%v) enabled = %v, want %v", p, got, want)
		}
	}
}

// -t=false and -c=false really disable fixtxt and cleanup.
func TestRunDisablesModules(t *testing.T) {
	t.Setenv("PATH", t.TempDir())
	const txt = "#TITLE:B\r\n#ARTIST:A\r\n#MP3:old.mp3\r\n#BPM:300\r\n: 0 1 0 la\r\nE\r\n"
	setup := func(t *testing.T) (lib, song string) {
		lib = t.TempDir()
		song = filepath.Join(lib, "A - B")
		testutil.WriteFiles(t, song, map[string]string{"song.txt": txt, "a.mp3": "a", "junk.nfo": "x"})
		return
	}
	t.Run("-c=false keeps clutter", func(t *testing.T) {
		lib, song := setup(t)
		if got := runWithArgs(t, "-s", lib, "-o", t.TempDir(), "-c=false"); got != exitOK {
			t.Fatalf("exit code %d", got)
		}
		if got, want := fileNames(t, song), []string{"a.mp3", "junk.nfo", "song.txt"}; !reflect.DeepEqual(got, want) {
			t.Errorf("song folder = %q, want %q", got, want)
		}
		data, _ := os.ReadFile(filepath.Join(song, "song.txt"))
		if !strings.Contains(string(data), "#MP3:a.mp3") {
			t.Errorf("fixtxt did not run: %q", data)
		}
	})
	t.Run("-t=false keeps the txt", func(t *testing.T) {
		lib, song := setup(t)
		if got := runWithArgs(t, "-s", lib, "-o", t.TempDir(), "-t=false"); got != exitOK {
			t.Fatalf("exit code %d", got)
		}
		if data, _ := os.ReadFile(filepath.Join(song, "song.txt")); string(data) != txt {
			t.Errorf("txt changed with -t=false: %q", data)
		}
		if got, want := fileNames(t, song), []string{"a.mp3", "song.txt"}; !reflect.DeepEqual(got, want) {
			t.Errorf("song folder = %q, want %q (cleanup should have run)", got, want)
		}
	})
}

// -move-broken reaches the check module.
func TestRunPassesCheckOptions(t *testing.T) {
	t.Setenv("PATH", t.TempDir())
	lib, out := t.TempDir(), t.TempDir()
	song := filepath.Join(lib, "A - B")
	// No #BPM: a check error.
	testutil.WriteFiles(t, song, map[string]string{"song.txt": "#ARTIST:A\n#TITLE:B\n#MP3:a.mp3\n: 0 1 0 la\nE\n", "a.mp3": "a"})
	if got := runWithArgs(t, "-s", lib, "-o", out, "-k", "-move-broken", "-t=false", "-c=false"); got != exitOK {
		t.Fatalf("exit code %d", got)
	}
	if _, err := os.Stat(filepath.Join(out, "invalid", "A - B", "song.txt")); err != nil {
		t.Errorf("broken song not moved to invalid/: %v", err)
	}
	data, err := os.ReadFile(filepath.Join(out, "check_report.json"))
	if err != nil {
		t.Fatal(err)
	}
	var rep struct{ Songs []struct{ Moved bool } }
	if err := json.Unmarshal(data, &rep); err != nil {
		t.Fatal(err)
	}
	if len(rep.Songs) != 1 || !rep.Songs[0].Moved {
		t.Errorf("report songs = %+v, want one moved song", rep.Songs)
	}
}
