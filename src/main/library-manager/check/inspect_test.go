package check

import (
	"context"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/nkreimeyer/library-manager/fsops"
)

func TestParseProbeBitrateAndSize(t *testing.T) {
	tests := []struct {
		name string
		json string
		want mediaInfo
	}{
		{"mp3 with cover art: stream rate, the picture is no video size",
			`{"streams":[{"codec_type":"audio","bit_rate":"192000","disposition":{"attached_pic":0}},
			{"codec_type":"video","width":500,"height":500,"disposition":{"attached_pic":1}}],
			"format":{"duration":"2.0","bit_rate":"199332"}}`,
			mediaInfo{DurationSec: 2, HasDuration: true, HasAudio: true, AudioBitrate: 192000}},
		{"ogg: no stream rate, the container rate is used",
			`{"streams":[{"codec_type":"audio","disposition":{"attached_pic":0}}],"format":{"duration":"2.0","bit_rate":"73540"}}`,
			mediaInfo{DurationSec: 2, HasDuration: true, HasAudio: true, AudioBitrate: 73540}},
		{"webm with video: no stream rate, the container rate includes the video",
			`{"streams":[{"codec_type":"video","width":1280,"height":720},{"codec_type":"audio","bit_rate":"N/A"}],
			"format":{"duration":"2.0","bit_rate":"2000000"}}`,
			mediaInfo{DurationSec: 2, HasDuration: true, HasAudio: true, HasVideo: true, VideoWidth: 1280, VideoHeight: 720}},
		{"first audio stream and first moving video stream",
			`{"streams":[{"codec_type":"video","width":64,"height":64,"disposition":{"attached_pic":1}},
			{"codec_type":"video","width":1920,"height":1080},{"codec_type":"video","width":640,"height":360},
			{"codec_type":"audio","bit_rate":"128000"},{"codec_type":"audio","bit_rate":"320000"}],"format":{}}`,
			mediaInfo{HasAudio: true, HasVideo: true, AudioBitrate: 128000, VideoWidth: 1920, VideoHeight: 1080}},
		{"no audio: no audio bitrate, the container rate is the video rate", `{"streams":[{"codec_type":"video","width":2,"height":2}],"format":{"bit_rate":"1000"}}`,
			mediaInfo{HasVideo: true, VideoWidth: 2, VideoHeight: 2, VideoBitrate: 1000}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := parseProbe([]byte(tt.json))
			if err != nil || got != tt.want {
				t.Errorf("parseProbe = %+v, %v, want %+v", got, err, tt.want)
			}
		})
	}
}

func TestSummarizeMedia(t *testing.T) {
	full := header + "#VIDEO:v.mp4\n#COVER:c.jpg\n#BACKGROUND:b.jpg\n" + notes(10)
	src := func() fileObs { o := audio("a.mp3", 2); o.Media.AudioBitrate = 320000; return o }
	vid := func() fileObs {
		o := video("v.mp4", 2)
		o.Media.VideoWidth, o.Media.VideoHeight, o.Media.VideoBitrate = 1920, 1080, 2000000
		return o
	}
	healthy := func() map[string]fileObs {
		return map[string]fileObs{"MP3": src(), "VIDEO": vid(), "COVER": image("c.jpg"), "BACKGROUND": image("b.jpg")}
	}
	all := MediaSummary{Probed: true, AudioBitrate: 320000, HasVideo: true, VideoWidth: 1920, VideoHeight: 1080, VideoBitrate: 2000000, HasCover: true, HasBackground: true}
	without := func(f func(*MediaSummary)) MediaSummary { m := all; f(&m); return m }
	noVideo := func(m *MediaSummary) { m.HasVideo, m.VideoWidth, m.VideoHeight, m.VideoBitrate = false, 0, 0, 0 }
	tests := []struct {
		name  string
		txt   string
		files map[string]fileObs
		want  MediaSummary
	}{
		{"everything", full, healthy(), all},
		{"nothing referenced", header + notes(10), map[string]fileObs{"MP3": src()}, MediaSummary{Probed: true, AudioBitrate: 320000}},
		{"not probed (no ffprobe)", full, map[string]fileObs{"MP3": {Name: "a.mp3", Found: true, Size: 5},
			"VIDEO": {Name: "v.mp4", Found: true, Size: 5}, "COVER": image("c.jpg"), "BACKGROUND": image("b.jpg")},
			MediaSummary{HasVideo: true, HasCover: true, HasBackground: true}},
		{"not probed, video missing", full, map[string]fileObs{"MP3": {Name: "a.mp3", Found: true, Size: 5},
			"VIDEO": {}, "COVER": image("c.jpg"), "BACKGROUND": image("b.jpg")},
			MediaSummary{HasCover: true, HasBackground: true}},
		{"not probed, video empty", full, map[string]fileObs{"MP3": {Name: "a.mp3", Found: true, Size: 5},
			"VIDEO": {Name: "v.mp4", Found: true}, "COVER": image("c.jpg"), "BACKGROUND": image("b.jpg")},
			MediaSummary{HasCover: true, HasBackground: true}},
		{"source unreadable by ffprobe", full, with(healthy(), "MP3", fileObs{Name: "a.mp3", Found: true, Size: 9, Probed: true, ProbeErr: "x"}),
			without(func(m *MediaSummary) { m.Probed, m.AudioBitrate = false, 0 })},
		{"source empty", full, with(healthy(), "MP3", func() fileObs { o := src(); o.Size = 0; return o }()),
			without(func(m *MediaSummary) { m.Probed, m.AudioBitrate = false, 0 })},
		{"#AUDIO is the source", header + "#AUDIO:x.ogg\n" + notes(10),
			map[string]fileObs{"MP3": src(), "AUDIO": func() fileObs { o := audio("x.ogg", 2); o.Media.AudioBitrate = 96000; return o }()},
			MediaSummary{Probed: true, AudioBitrate: 96000}},
		{"video missing", full, with(healthy(), "VIDEO", fileObs{}), without(noVideo)},
		{"video without permission", full, with(healthy(), "VIDEO", func() fileObs { o := vid(); o.Unreadable = true; return o }()), without(noVideo)},
		{"video unreadable", full, with(healthy(), "VIDEO", fileObs{Name: "v.mp4", Found: true, Size: 9, Probed: true, ProbeErr: "x"}), without(noVideo)},
		{"video only cover art", full, with(healthy(), "VIDEO", video("v.mp4", 2)), without(noVideo)},
		{"video decode error", full, with(healthy(), "VIDEO", func() fileObs { o := vid(); o.DecodeErr = "x"; return o }()), without(noVideo)},
		{"video decode warning still counts", full, with(healthy(), "VIDEO", func() fileObs { o := vid(); o.DecodeWarn = "x"; return o }()), all},
		{"cover unreadable", full, with(healthy(), "COVER", fileObs{Name: "c.jpg", Found: true, Size: 9, ImageErr: "bad"}),
			without(func(m *MediaSummary) { m.HasCover = false })},
		{"background empty", full, with(healthy(), "BACKGROUND", fileObs{Name: "b.jpg", Found: true}),
			without(func(m *MediaSummary) { m.HasBackground = false })},
		{"background cannot be stat'ed", full, with(healthy(), "BACKGROUND", fileObs{Name: "b.jpg", Found: true, Size: 9, StatErr: "x"}),
			without(func(m *MediaSummary) { m.HasBackground = false })},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			in := input(tt.txt, tt.files)
			if got := summarizeMedia(fileRefs(in.Song, in.Files), in.Files); got != tt.want {
				t.Errorf("summary = %+v, want %+v", got, tt.want)
			}
		})
	}
}

func TestSongWarnings(t *testing.T) {
	full := header + "#VIDEO:v.mp4\n#COVER:c.jpg\n#BACKGROUND:b.jpg\n#VOCALS:voc.mp3\n" + notes(10)
	healthy := func() map[string]fileObs {
		return map[string]fileObs{"MP3": audio("a.mp3", 2), "VIDEO": video("v.mp4", 2), "COVER": image("c.jpg"),
			"BACKGROUND": image("b.jpg"), "VOCALS": audio("voc.mp3", 2)}
	}
	decodeWarn := func(o fileObs) fileObs { o.DecodeWarn = "x"; return o }
	tests := []struct {
		name  string
		txt   string
		files map[string]fileObs
		want  int
	}{
		{"healthy", full, healthy(), 0},
		{"few notes", header + notes(5), healthyFiles(), 1},
		{"few notes and no end", header + ": 0 2 0 la\n", healthyFiles(), 2},
		{"missing video", full, with(healthy(), "VIDEO", fileObs{}), 0},
		{"missing cover", full, with(healthy(), "COVER", fileObs{}), 0},
		{"empty background", full, with(healthy(), "BACKGROUND", fileObs{Name: "b.jpg", Found: true}), 0},
		{"unreadable cover image", full, with(healthy(), "COVER", fileObs{Name: "c.jpg", Found: true, Size: 9, ImageErr: "x"}), 0},
		{"missing stem", full, with(healthy(), "VOCALS", fileObs{}), 0},
		{"video without video stream", full, with(healthy(), "VIDEO", audio("v.mp4", 2)), 0},
		{"decode warning of the video", full, with(healthy(), "VIDEO", decodeWarn(video("v.mp4", 2))), 0},
		{"decode warning of the source", full, with(healthy(), "MP3", decodeWarn(audio("a.mp3", 2))), 1},
		{"video too short", header + "#VIDEO:v.mp4\n" + notes(10), map[string]fileObs{"MP3": audio("a.mp3", 30), "VIDEO": video("v.mp4", 5)}, 0},
		{"stale #MP3 next to #AUDIO", header + "#AUDIO:new.ogg\n" + notes(10),
			map[string]fileObs{"MP3": {}, "AUDIO": audio("new.ogg", 2)}, 1},
		{"errors are not counted", header + "#BPM:0\n" + notes(10), healthyFiles(), 1}, // duplicate-tag warning only
		{"bad #VIDEOGAP", header + "#VIDEOGAP:abc\n#VIDEO:v.mp4\n" + notes(10), with(healthyFiles(), "VIDEO", video("v.mp4", 2)), 0},
		{"repeated media tags", header + "#VIDEO:v.mp4\n#VIDEO:v.mp4\n#COVER:c.jpg\n#COVER:c.jpg\n#BACKGROUND:b.jpg\n#BACKGROUND:b.jpg\n" + notes(10),
			healthy(), 0},
		{"repeated #MP3 counts", header + "#MP3:a.mp3\n" + notes(10), healthyFiles(), 1},
		{"repeated #GENRE counts", header + "#GENRE:Pop\n#GENRE:Rock\n" + notes(10), healthyFiles(), 1},
		{"video-only song: a decode warning of the shared file is about the source", "#TITLE:T\n#ARTIST:A\n#MP3:v.mp4\n#BPM:300\n#VIDEO:v.mp4\n" + notes(10),
			func() map[string]fileObs {
				v := decodeWarn(video("v.mp4", 2))
				v.Media.HasAudio = true
				return map[string]fileObs{"MP3": v, "VIDEO": v}
			}(), 1},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			fs := analyze(input(tt.txt, tt.files))
			if got := SongWarnings(fs); got != tt.want {
				t.Errorf("SongWarnings = %d, want %d; findings %+v", got, tt.want, fs)
			}
		})
	}
	// Findings built by hand (as other packages' tests do): the tag decides.
	hand := []Finding{
		{Severity: SevWarning, Code: codeMissingFile, Tag: "VIDEO"},
		{Severity: SevWarning, Code: codeMissingFile, Tag: "INSTRUMENTAL"},
		{Severity: SevWarning, Code: codeMissingFile, Tag: "MP3"},
		{Severity: SevWarning, Code: codeFewNotes},
		{Severity: SevError, Code: codeNoNotes},
		{Severity: SevWarning, Code: codeVideoTooShort}, // no tag: the code decides
	}
	if got := SongWarnings(hand); got != 2 {
		t.Errorf("SongWarnings(hand-built) = %d, want 2", got)
	}
}

func TestInspect(t *testing.T) {
	withoutFFmpeg(t)
	lib := t.TempDir()
	writeFiles(t, lib, map[string]string{
		"Good/song.txt":     "#COVER:c.jpg\n#VERSION:1.0.0\n" + song("song.mp3", 10),
		"Good/song.mp3":     "x",
		"Good/c.jpg":        heicHeader,
		"Broken/song.txt":   song("missing.mp3", 10),
		"NoTxt/song.mp3":    "x",
		"CP1250/song.txt":   "#TITLE:Za\xbf\xf3\xb3\xe6\n#ARTIST:A\n#MP3:\xf8\xedk\xe1m.mp3\n#BPM:300\n" + notes(10),
		"CP1250/říkám.mp3":  "x",
		"Unreadable/a.mp3":  "x",
		"Unreadable/s.txt":  song("a.mp3", 10),
		"Suspicious/s.txt":  song("a.mp3", 5),
		"Suspicious/a.mp3":  "x",
		"Suspicious/v.webm": "x",
	})
	chmodUnreadable(t, filepath.Join(lib, "Unreadable", "s.txt"))
	ops := fsops.New(false)
	insp, notes := NewInspector(context.Background(), ops, true)
	if !reflect.DeepEqual(notes, []string{noteNoFFprobe, noteNoFFmpeg}) || insp.MediaChecked() || insp.DeepChecked() {
		t.Errorf("notes = %q, media %v, deep %v", notes, insp.MediaChecked(), insp.DeepChecked())
	}

	good, err := insp.Inspect(filepath.Join(lib, "Good"))
	if err != nil || good.Txt != "song.txt" || good.Broken || good.EncodingProblem || len(good.Findings) != 0 || good.Song == nil {
		t.Fatalf("Good = %+v, %v", good, err)
	}
	if v, _ := good.Song.Tag("VERSION"); v != "1.0.0" {
		t.Errorf("Good song tags = %+v", good.Song.Tags)
	}
	if good.Media != (MediaSummary{HasCover: true}) {
		t.Errorf("Good media = %+v, want only a cover (no ffprobe)", good.Media)
	}

	broken, err := insp.Inspect(filepath.Join(lib, "Broken"))
	if err != nil || !broken.Broken || broken.EncodingProblem || !reflect.DeepEqual(codes(broken.Findings), []string{"error:missing-file"}) {
		t.Errorf("Broken = %+v, %v", broken, err)
	}

	noTxt, err := insp.Inspect(filepath.Join(lib, "NoTxt"))
	if err != nil || !noTxt.Broken || noTxt.Txt != "" || noTxt.Song != nil || !reflect.DeepEqual(codes(noTxt.Findings), []string{"error:no-song-txt"}) {
		t.Errorf("NoTxt = %+v, %v", noTxt, err)
	}

	enc, err := insp.Inspect(filepath.Join(lib, "CP1250"))
	if err != nil || !enc.Broken || !enc.EncodingProblem {
		t.Errorf("CP1250 = %+v, %v", enc, err)
	}

	sus, err := insp.Inspect(filepath.Join(lib, "Suspicious"))
	if err != nil || sus.Broken || SongWarnings(sus.Findings) != 1 {
		t.Errorf("Suspicious = %+v, %v", sus, err)
	}

	if st := ops.Stats(); st.Failed != 0 {
		t.Fatalf("stats = %+v before the unreadable folder", st)
	}
	for range 2 {
		if res, err := insp.Inspect(filepath.Join(lib, "Unreadable")); err == nil {
			t.Errorf("Unreadable = %+v, want an error", res)
		}
	}
	if st := ops.Stats(); st.Failed != 1 {
		t.Errorf("stats = %+v, want the unreadable txt counted once", st)
	}
}

// Dry run: the txt is read as an earlier module planned to write it.
func TestInspectReadsPlannedTxt(t *testing.T) {
	withoutFFmpeg(t)
	lib := t.TempDir()
	writeFiles(t, lib, map[string]string{"S/song.txt": song("gone.mp3", 10), "S/song.mp3": "x"})
	ops := fsops.New(true)
	if _, err := ops.WriteFileAtomic(filepath.Join(lib, "S", "song.txt"), []byte(song("song.mp3", 10))); err != nil {
		t.Fatal(err)
	}
	insp, _ := NewInspector(context.Background(), ops, false)
	res, err := insp.Inspect(filepath.Join(lib, "S"))
	if err != nil || res.Broken || len(res.Findings) != 0 {
		t.Errorf("Inspect = %+v, %v, want the planned (fixed) txt", res, err)
	}
}

func TestInspectMedia(t *testing.T) {
	requireFFmpeg(t)
	lib := t.TempDir()
	dir := filepath.Join(lib, "S")
	lavfi(t, filepath.Join(dir, "a.mp3"), "-f", "lavfi", "-i", "sine=d=2", "-c:a", "libmp3lame", "-b:a", "192k")
	lavfi(t, filepath.Join(dir, "v.mp4"), "-f", "lavfi", "-i", "testsrc=d=2:s=96x64:r=10", "-c:v", "libx264", "-pix_fmt", "yuv420p")
	writeFiles(t, lib, map[string]string{"S/song.txt": "#VIDEO:v.mp4\n" + song("a.mp3", 10)})

	insp, notes := NewInspector(context.Background(), fsops.New(false), false)
	if len(notes) != 0 || !insp.MediaChecked() || insp.DeepChecked() {
		t.Errorf("notes = %q, media %v, deep %v", notes, insp.MediaChecked(), insp.DeepChecked())
	}
	res, err := insp.Inspect(dir)
	if err != nil || res.Broken {
		t.Fatalf("Inspect = %+v, %v", res, err)
	}
	if res.Media.VideoBitrate <= 0 {
		t.Errorf("video bitrate = %d, want it known", res.Media.VideoBitrate)
	}
	res.Media.VideoBitrate = 0 // depends on the encoder
	want := MediaSummary{Probed: true, AudioBitrate: 192000, HasVideo: true, VideoWidth: 96, VideoHeight: 64}
	if res.Media != want {
		t.Errorf("media = %+v, want %+v", res.Media, want)
	}
}
