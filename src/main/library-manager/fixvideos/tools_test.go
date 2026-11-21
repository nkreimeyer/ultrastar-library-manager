package fixvideos

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/nkreimeyer/library-manager/fsops"
	"github.com/nkreimeyer/library-manager/internal/testutil"
)

// An ffmpeg older than the minimum is refused like a missing one: Run fails before it
// touches a video, instead of failing (or encoding differently) on every one.
func TestRunRefusesOldFFmpeg(t *testing.T) {
	for _, tc := range []struct{ ffmpeg, ffprobe, want string }{
		{"4.4.2-0ubuntu0.22.04.1", "7.1", "ffmpeg: version 4.4.2-0ubuntu0.22.04.1 is too old"},
		{"7.1", "5.0.3", "ffprobe: version 5.0.3 is too old"},
	} {
		t.Run(tc.want, func(t *testing.T) {
			testutil.FakeVersions(t, tc.ffmpeg, tc.ffprobe)
			cfg, songDir, outDir := newLibrary(t, false)
			song := filepath.Join(songDir, "A - S")
			testutil.WriteFile(t, filepath.Join(song, "song.txt"), "#TITLE:S\n#ARTIST:A\n#MP3:a.mp3\n#VIDEO:v.avi\n")
			testutil.WriteFile(t, filepath.Join(song, "v.avi"), "video")
			ops := fsops.New(false)
			err := Run(cfg, ops)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Errorf("Run = %v, want %q", err, tc.want)
			}
			if st := ops.Stats(); st != (fsops.Stats{}) {
				t.Errorf("Stats = %+v, want nothing done", st)
			}
			if entries, _ := os.ReadDir(outDir); len(entries) != 0 {
				t.Errorf("out dir has %d entries, want none", len(entries))
			}
		})
	}
}
