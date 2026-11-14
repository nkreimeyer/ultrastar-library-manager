package check

import (
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

// A negative #GAP moves the first note before the audio just like a negative beat does:
// the check decides on song time, not on how the offset is written.
func TestNegativeGapNoteBeforeAudio(t *testing.T) {
	tests := []struct {
		name     string
		txt      string
		want     []string
		wantLine int
	}{
		{"negative beat", header + "#GAP:0\n: -10 2 0 x\n" + notes(10), []string{"warning:note-before-audio"}, 6},
		{"negative gap", header + "#GAP:-500\n" + notes(10), []string{"warning:note-before-audio"}, 6},
		// Beat 10 at BPM 300 is 500 ms: song time 0 with #GAP:-500.
		{"negative gap, first note at song time 0", header + "#GAP:-500\n" + pad(10), nil, 0},
		{"negative gap, first note at -50 ms", header + "#GAP:-500\n" + pad(9), []string{"warning:note-before-audio"}, 6},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			fs := analyze(input(tt.txt, healthyFiles()))
			want := tt.want
			if want == nil {
				want = []string{}
			}
			if got := codes(fs); !reflect.DeepEqual(got, want) {
				t.Fatalf("findings = %v, want %v", got, want)
			}
			if len(fs) == 1 && fs[0].Line != tt.wantLine {
				t.Errorf("line = %d, want %d", fs[0].Line, tt.wantLine)
			}
		})
	}
	fs := analyze(input(header+"#GAP:-500\n"+notes(10), healthyFiles()))
	if len(fs) != 1 {
		t.Fatalf("findings = %+v", fs)
	}
	if want := "the first note starts at -0:00.5, before the audio begins (beat 0; #GAP too small or negative)"; fs[0].Message != want {
		t.Errorf("message = %q, want %q", fs[0].Message, want)
	}
}

// #VIDEO naming an audio file whose only video stream is embedded cover art gets the
// no-video-stream warning, consistent with MediaSummary.HasVideo = false, and its length
// is not compared as a video length.
func TestVideoOnlyAttachedPicWarns(t *testing.T) {
	info, err := parseProbe([]byte(`{"streams":[{"codec_type":"audio","bit_rate":"128000"},
		{"codec_type":"video","width":500,"height":500,"disposition":{"attached_pic":1}}],
		"format":{"duration":"2.0"}}`))
	if err != nil {
		t.Fatal(err)
	}
	if info.HasVideo {
		t.Errorf("parseProbe: HasVideo = true for cover art")
	}
	// A 2 s audio file as #VIDEO of a 30 s song: no video-too-short, the file is no video.
	o := fileObs{Name: "v.mp3", Found: true, Size: 1000, Probed: true, Media: info}
	in := input(header+"#VIDEO:v.mp3\n"+notes(10), map[string]fileObs{"MP3": audio("a.mp3", 30), "VIDEO": o})
	if got, want := codes(analyze(in)), []string{"warning:no-video-stream"}; !reflect.DeepEqual(got, want) {
		t.Errorf("codes = %v, want %v", got, want)
	}
	if m := summarizeMedia(fileRefs(in.Song, in.Files), in.Files); m.HasVideo {
		t.Errorf("MediaSummary.HasVideo = true for cover art")
	}
}

// The same with real files: an mp3 with an embedded cover named by #VIDEO.
func TestRunVideoIsAudioWithCover(t *testing.T) {
	requireFFmpeg(t)
	lib := filepath.Join(t.TempDir(), "songs")
	writeFiles(t, lib, map[string]string{"Song/song.txt": "#VIDEO:v.mp3\n" + song("a.mp3", 10)})
	cover := filepath.Join(t.TempDir(), "cover.jpg")
	lavfi(t, cover, "-f", "lavfi", "-i", "color=c=red:s=32x32", "-frames:v", "1")
	sine(t, filepath.Join(lib, "Song", "a.mp3"), 30)
	lavfi(t, filepath.Join(lib, "Song", "v.mp3"), "-f", "lavfi", "-i", "sine=d=2", "-i", cover,
		"-map", "0:a", "-map", "1:v", "-c:v", "mjpeg", "-disposition:v", "attached_pic", "-id3v2_version", "3")
	_, r := run(t, newConfig(t, lib, false), Options{})
	if got, want := songCodes(r), map[string][]string{"Song": {"warning:no-video-stream"}}; !reflect.DeepEqual(got, want) {
		t.Errorf("findings = %v, want %v\n%+v", got, want, r.Songs)
	}
	if f := findSong(t, r, "Song").Findings[0]; f.File != "v.mp3" || !strings.Contains(f.Message, "#VIDEO") {
		t.Errorf("finding = %+v", f)
	}
}
