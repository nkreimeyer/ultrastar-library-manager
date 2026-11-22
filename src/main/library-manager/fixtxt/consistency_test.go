package fixtxt

import (
	"path/filepath"
	"strings"
	"testing"

	"golang.org/x/text/unicode/norm"

	"github.com/nkreimeyer/library-manager/internal/testutil"
)

// An #MP3 naming an image or another non-media file of a video-only song takes the video;
// only a video is a valid audio value there.
func TestRunReplacesNonMediaMP3OfVideoOnlySong(t *testing.T) {
	for _, mp3 := range []string{"cover.jpg", "notes.nfo", "song.txt"} {
		t.Run(mp3, func(t *testing.T) {
			got, _, _ := fixSong(t, "Song", "song.txt",
				map[string]string{"clip.mp4": "v", "cover.jpg": "c", "notes.nfo": "n"}, []string{"#MP3:" + mp3})
			if !strings.Contains(got, "#MP3:clip.mp4\n") || !strings.Contains(got, "#VIDEO:clip.mp4\n") || strings.Contains(got, "#MP3:"+mp3) {
				t.Errorf("want #MP3 and #VIDEO set to clip.mp4:\n%s", got)
			}
		})
	}
}

// The "never point both tags at one file" guard compares like utils.ResolveFileName:
// case- and Unicode-normalization-insensitively.
func TestSameFileNameIgnoresNormalization(t *testing.T) {
	tests := []struct {
		onDisk, tag string
		want        bool
	}{
		{"Café.jpg", norm.NFD.String("Café.jpg"), true},
		{"Café.jpg", " CAFÉ.JPG ", true},
		{"Café.jpg", "Cafe.jpg", false},
		{"Café.jpg", "", false},
	}
	for _, tc := range tests {
		if got := sameFileName(tc.onDisk, tc.tag); got != tc.want {
			t.Errorf("sameFileName(%q, %q) = %v, want %v", tc.onDisk, tc.tag, got, tc.want)
		}
	}
}

// With FileTags off the header keeps its repeated tags; fillNameTags fills the last
// occurrence (the one players read) and leaves the others alone.
func TestRunWithoutFileTagsFillsLastRepeatedTag(t *testing.T) {
	cfg := newLibrary(t, false)
	txt := filepath.Join(cfg.SongDir, "Artist - New", "song.txt")
	testutil.WriteFile(t, txt, "#TITLE:Old\n#ARTIST:Artist\n#TITLE:\n#MP3:song.mp3\n#BPM:300\n: 0 4 0 La\nE\n")
	testutil.WriteFile(t, filepath.Join(cfg.SongDir, "Artist - New", "song.mp3"), "a")
	runWith(t, cfg, without(func(o *Options) { o.FileTags = false }))
	want := "#TITLE:Old\n#ARTIST:Artist\n#TITLE:New\n#MP3:song.mp3\n#BPM:300\n: 0 4 0 La\nE\n"
	if got := testutil.ReadFile(t, txt); got != want {
		t.Errorf("txt = %q, want %q", got, want)
	}
}

// A single-byte txt that also holds the UTF-8 bytes of U+FFFD (damage from an earlier
// conversion, kept by an ANSI editor) is never rewritten: "ï¿½" would hide the damage.
func TestRunKeepsSingleByteTxtWithReplacementCharacter(t *testing.T) {
	cfg := newLibrary(t, false)
	raw := "#TITLE:M\xe4dchen\n#ARTIST:A\n#MP3:song.mp3\n#BPM:300\n: 0 4 0 F\xef\xbf\xbdr\nE\n"
	txt := filepath.Join(cfg.SongDir, "A - M", "song.txt")
	testutil.WriteFile(t, txt, raw)
	testutil.WriteFile(t, filepath.Join(cfg.SongDir, "A - M", "song.mp3"), "a")
	run(t, cfg)
	if got := testutil.ReadFile(t, txt); got != raw {
		t.Errorf("txt rewritten: %q", got)
	}
	if report := testutil.ReadFile(t, cfg.ReportPath(encodingIssuesReport)); !strings.Contains(report, txt) {
		t.Errorf("%s does not list %s:\n%s", encodingIssuesReport, txt, report)
	}
}
