package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/nkreimeyer/ultrastar-songs-sanitizer/check"

	log "github.com/sirupsen/logrus"
	logtest "github.com/sirupsen/logrus/hooks/test"
)

// writeSong creates dir with the given files (name -> content).
func writeSong(t *testing.T, dir string, files map[string]string) {
	t.Helper()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	for name, content := range files {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
}

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

// A stale #MP3 that fixtxt cannot fix (two audio candidates, none preferred) must not make
// cleanup move every audio file away; neither in a real nor in a dry run. With -t=false
// the same holds for a misspelled #MP3 next to the only audio file.
func TestStaleAudioTagKeepsAudio(t *testing.T) {
	t.Setenv("PATH", t.TempDir())
	tests := []struct {
		name  string
		files map[string]string
		args  []string
		want  []string // files left in the song folder
	}{
		{"ambiguous audio", map[string]string{
			"song.txt":  "#ARTIST:Artist\n#TITLE:Song\n#MP3:old.mp3\n#BPM:300\n: 0 1 0 la\nE\n",
			"take1.mp3": "a", "take2.mp3": "b",
		}, nil, []string{"song.txt", "take1.mp3", "take2.mp3"}},
		{"misspelled #MP3 with -t=false", map[string]string{
			"song.txt": "#ARTIST:Artist\n#TITLE:Song\n#MP3:Sogn.mp3\n#BPM:300\n: 0 1 0 la\nE\n",
			"song.mp3": "a", "junk.nfo": "x",
		}, []string{"-t=false"}, []string{"song.mp3", "song.txt"}},
	}
	for _, tt := range tests {
		for _, dry := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s dry=%v", tt.name, dry), func(t *testing.T) {
				lib, out := t.TempDir(), t.TempDir()
				song := filepath.Join(lib, "Artist - Song")
				writeSong(t, song, tt.files)
				before := fileNames(t, song)
				args := append([]string{"-s", lib, "-o", out}, tt.args...)
				if dry {
					args = append(args, "-n")
				}
				if code := runWithArgs(t, args...); code != exitOK {
					t.Errorf("exit code %d", code)
				}
				want := tt.want
				if dry {
					want = before
				}
				if got := fileNames(t, song); !reflect.DeepEqual(got, want) {
					t.Errorf("song folder = %q, want %q", got, want)
				}
				for _, n := range []string{"take1.mp3", "take2.mp3", "song.mp3"} {
					if _, err := os.Stat(filepath.Join(out, "extra-files", "Artist - Song", n)); err == nil {
						t.Errorf("%s moved to extra-files", n)
					}
				}
			})
		}
	}
}

func TestRunWithoutArguments(t *testing.T) {
	if got := runWithArgs(t); got != exitUsage {
		t.Errorf("run() without arguments = %d, want %d", got, exitUsage)
	}
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
		writeSong(t, song, map[string]string{"song.txt": "#ARTIST:A\n#TITLE:B\n#MP3:a.mp3\n" + validNotes(), "a.mp3": "a"})
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
		msgs := errorLogs(t, func() { code = runWithArgs(t, "-s", lib, "-o", t.TempDir()) })
		if code != exitFailures {
			t.Errorf("exit code %d, want %d", code, exitFailures)
		}
		for _, m := range []string{"fixtxt failed", "cleanup failed"} {
			if !containsPrefix(msgs, m) {
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
		for _, m := range moduleList(p[0], p[1], p[2], p[3], p[4], check.Options{}) {
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
		writeSong(t, song, map[string]string{"song.txt": txt, "a.mp3": "a", "junk.nfo": "x"})
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
	writeSong(t, song, map[string]string{"song.txt": "#ARTIST:A\n#TITLE:B\n#MP3:a.mp3\n: 0 1 0 la\nE\n", "a.mp3": "a"})
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

// errorLogs returns the messages of the error-level entries logged during fn.
func errorLogs(t *testing.T, fn func()) []string {
	t.Helper()
	hook := logtest.NewGlobal()
	defer log.StandardLogger().ReplaceHooks(make(log.LevelHooks))
	fn()
	var msgs []string
	for _, e := range hook.AllEntries() {
		if e.Level <= log.ErrorLevel {
			msgs = append(msgs, e.Message)
		}
	}
	return msgs
}

func containsPrefix(msgs []string, prefix string) bool {
	for _, m := range msgs {
		if strings.HasPrefix(m, prefix) {
			return true
		}
	}
	return false
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
		writeSong(t, filepath.Join(src, "ABBA - SOS"), map[string]string{
			"song.txt": "#ARTIST:ABBA\n#TITLE:SOS\n#MP3:a.mp3\n" + validNotes(), "a.mp3": "a",
		})
		return
	}
	t.Run("success logs no error", func(t *testing.T) {
		src, lib := newSource(t)
		var code int
		msgs := errorLogs(t, func() { code = runWithArgs(t, "organize", "-s", src, "-l", lib) })
		if code != exitOK {
			t.Errorf("exit code %d, want %d", code, exitOK)
		}
		if len(msgs) != 0 {
			t.Errorf("error logs %q, want none", msgs)
		}
	})
	t.Run("unreadable song txt", func(t *testing.T) {
		src, lib := newSource(t)
		writeSong(t, filepath.Join(src, "Locked"), map[string]string{"song.txt": "#ARTIST:X\n#TITLE:Y\n", "a.mp3": "a"})
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
	hook := logtest.NewGlobal()
	defer log.StandardLogger().ReplaceHooks(make(log.LevelHooks))
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
