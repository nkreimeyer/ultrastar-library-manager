package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/nkreimeyer/ultrastar-songs-sanitizer/check"
)

// runWithArgs calls run() with a fresh flag set, as if started with args.
func runWithArgs(t *testing.T, args ...string) int {
	t.Helper()
	oldArgs, oldFlags := os.Args, flag.CommandLine
	t.Cleanup(func() { os.Args, flag.CommandLine = oldArgs, oldFlags })
	os.Args = append([]string{"songs-sanitizer"}, args...)
	flag.CommandLine = flag.NewFlagSet("songs-sanitizer", flag.ContinueOnError)
	flag.CommandLine.SetOutput(io.Discard)
	return run()
}

func TestRunUsage(t *testing.T) {
	t.Setenv("PATH", t.TempDir()) // no ffmpeg: check skips the media checks
	lib, out := t.TempDir(), t.TempDir()
	tests := []struct {
		name string
		args []string
		want int
	}{
		{"no song dir", []string{"-k"}, exitUsage},
		{"leftover argument", []string{"-s", lib, "-t", "false"}, exitUsage},
		{"-deep without -k", []string{"-s", lib, "-o", out, "-deep"}, exitUsage},
		{"-move-broken without -k", []string{"-s", lib, "-o", out, "-move-broken"}, exitUsage},
		{"check with its options", []string{"-s", lib, "-o", out, "-k", "-deep", "-move-broken", "-t=false", "-c=false"}, exitOK},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := runWithArgs(t, tt.args...); got != tt.want {
				t.Errorf("run(%q) = %d, want %d", tt.args, got, tt.want)
			}
		})
	}
	if _, err := os.Stat(filepath.Join(out, "check_report.json")); err != nil {
		t.Errorf("check did not run: %v", err)
	}
}

func TestModuleOrder(t *testing.T) {
	var names []string
	for _, m := range moduleList(true, true, true, false, true, check.Options{}) {
		names = append(names, m.name)
		if m.enabled != (m.name != "fixvideos") {
			t.Errorf("%s enabled = %v", m.name, m.enabled)
		}
	}
	if want := []string{"fixtxt", "cleanup", "check", "fixvideos", "fixaudio"}; !slices.Equal(names, want) {
		t.Errorf("module order = %v, want %v", names, want)
	}
}

// TestRunFillsNameTagsBeforeCheck: main tells check whether fixtxt runs. In a dry run with
// fixtxt, a missing #ARTIST that fixtxt would fill from the folder name is only a warning,
// so -move-broken plans no move; the real run fills the tag and check finds nothing to
// move either.
func TestRunFillsNameTagsBeforeCheck(t *testing.T) {
	t.Setenv("PATH", t.TempDir()) // no ffmpeg: check skips the media checks
	lib := t.TempDir()
	dir := filepath.Join(lib, "Electric Callboy - Hypa Hypa")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	txt := filepath.Join(dir, "song.txt")
	notes := "#BPM:300\n"
	for i := range 10 {
		notes += fmt.Sprintf(": %d 2 0 la\n", i*4)
	}
	for name, content := range map[string]string{txt: "#TITLE:Hypa Hypa\n#MP3:song.mp3\n" + notes + "E\n", filepath.Join(dir, "song.mp3"): "x"} {
		if err := os.WriteFile(name, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	missingTag := func(t *testing.T, out string) string {
		t.Helper()
		data, err := os.ReadFile(filepath.Join(out, "check_report.json"))
		if err != nil {
			t.Fatal(err)
		}
		var rep struct {
			Songs []struct {
				Moved    bool
				Findings []struct{ Severity, Code string }
			}
		}
		if err := json.Unmarshal(data, &rep); err != nil {
			t.Fatal(err)
		}
		for _, s := range rep.Songs {
			if s.Moved {
				t.Errorf("song (planned to be) moved: %s", data)
			}
			for _, f := range s.Findings {
				if f.Code == "missing-tag" {
					return f.Severity
				}
			}
		}
		return ""
	}

	dryOut := t.TempDir()
	if got := runWithArgs(t, "-s", lib, "-o", dryOut, "-n", "-k", "-move-broken"); got != exitOK {
		t.Fatalf("dry run exit code %d", got)
	}
	if got := missingTag(t, dryOut); got != "warning" {
		t.Errorf("dry run: missing #ARTIST severity %q, want warning", got)
	}

	out := t.TempDir()
	if got := runWithArgs(t, "-s", lib, "-o", out, "-k", "-move-broken"); got != exitOK {
		t.Fatalf("real run exit code %d", got)
	}
	if got := missingTag(t, out); got != "" {
		t.Errorf("real run: missing-tag finding %q after fixtxt", got)
	}
	data, err := os.ReadFile(txt)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(string(data), "#ARTIST:Electric Callboy\n#TITLE:Hypa Hypa\n") {
		t.Errorf("txt = %q", data)
	}
}
