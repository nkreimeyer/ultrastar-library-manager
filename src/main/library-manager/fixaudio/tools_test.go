package fixaudio

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/nkreimeyer/library-manager/fsops"
)

// An ffmpeg older than the minimum (its aresample has no ochl option) is refused like a
// missing one: Run fails before it touches a file.
func TestRunRefusesOldFFmpeg(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("fake tools are shell scripts")
	}
	dir := t.TempDir()
	for name, v := range map[string]string{"ffmpeg": "5.0.3", "ffprobe": "7.1"} {
		script := "#!/bin/sh\nif [ \"$1\" = -version ]; then echo '" + name + " version " + v + " Copyright (c) the FFmpeg developers'; exit 0; fi\nexit 1\n"
		if err := os.WriteFile(filepath.Join(dir, name), []byte(script), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv("PATH", dir)
	cfg, ops := newLibrary(t, false)
	addSong(t, cfg, "Artist - Song", "song.mp3")
	if err := os.WriteFile(filepath.Join(cfg.SongDir, "Artist - Song", "song.mp3"), []byte("mp3"), 0o644); err != nil {
		t.Fatal(err)
	}
	err := Run(cfg, ops)
	if err == nil || !strings.Contains(err.Error(), "ffmpeg: version 5.0.3 is too old, 5.1 or newer is needed") {
		t.Errorf("Run = %v, want the too-old error", err)
	}
	if st := ops.Stats(); st != (fsops.Stats{}) {
		t.Errorf("Stats = %+v, want nothing done", st)
	}
}
