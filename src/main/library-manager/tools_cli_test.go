package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"
)

// fakeOldTools makes PATH a folder whose ffmpeg and ffprobe report the given versions
// for -version and fail on everything else.
func fakeOldTools(t *testing.T, ffmpeg, ffprobe string) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("fake tools are shell scripts")
	}
	dir := t.TempDir()
	for name, v := range map[string]string{"ffmpeg": ffmpeg, "ffprobe": ffprobe} {
		script := "#!/bin/sh\nif [ \"$1\" = -version ]; then echo '" + name + " version " + v +
			" Copyright (c) the FFmpeg developers'; exit 0; fi\nexit 1\n"
		if err := os.WriteFile(filepath.Join(dir, name), []byte(script), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv("PATH", dir)
}

// The CLI checks the tool versions like the web UI (mediatools.Detect): -m and -a refuse
// an ffmpeg older than 5.1 (the module fails: exit code 1); -k uses it with a note in the
// report (findings never change the exit code).
func TestRunOldFFmpeg(t *testing.T) {
	fakeOldTools(t, "4.4.2-0ubuntu0.22.04.1", "4.4.2-0ubuntu0.22.04.1")
	lib := t.TempDir()
	writeSong(t, filepath.Join(lib, "A - B"), map[string]string{
		"song.txt": "#ARTIST:A\n#TITLE:B\n#MP3:a.mp3\n#VIDEO:v.mp4\n" + validNotes(), "a.mp3": "a", "v.mp4": "v",
	})
	for _, tc := range []struct{ flag, module string }{{"-m", "fixvideos"}, {"-a", "fixaudio"}} {
		t.Run(tc.flag, func(t *testing.T) {
			var code int
			msgs := errorLogs(t, func() { code = runWithArgs(t, "-s", lib, "-o", t.TempDir(), "-t=false", "-c=false", tc.flag) })
			if code != exitFailures {
				t.Errorf("exit code %d, want %d", code, exitFailures)
			}
			if !slices.ContainsFunc(msgs, func(m string) bool {
				return strings.HasPrefix(m, tc.module+" failed") && strings.Contains(m, "is too old, 5.1 or newer is needed")
			}) {
				t.Errorf("error logs %q, want %s failed with the too-old message", msgs, tc.module)
			}
		})
	}
	t.Run("-k", func(t *testing.T) {
		out := t.TempDir()
		if code := runWithArgs(t, "-s", lib, "-o", out, "-t=false", "-c=false", "-k"); code != exitOK {
			t.Errorf("exit code %d, want %d", code, exitOK)
		}
		data, err := os.ReadFile(filepath.Join(out, "check_report.json"))
		if err != nil {
			t.Fatal(err)
		}
		var r struct {
			MediaChecked bool     `json:"mediaChecked"`
			Notes        []string `json:"notes"`
		}
		if err := json.Unmarshal(data, &r); err != nil {
			t.Fatal(err)
		}
		if !r.MediaChecked || len(r.Notes) != 1 || !strings.Contains(r.Notes[0], "ffprobe 4.4.2-0ubuntu0.22.04.1 is older than 5.1") {
			t.Errorf("report: media checked %v, notes %q; want the old-ffprobe note", r.MediaChecked, r.Notes)
		}
	})
}
