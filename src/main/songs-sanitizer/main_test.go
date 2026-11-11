package main

import (
	"flag"
	"io"
	"os"
	"path/filepath"
	"slices"
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
