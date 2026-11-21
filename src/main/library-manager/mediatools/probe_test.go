package mediatools

import (
	"context"
	"encoding/json"
	"errors"
	"os/exec"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"testing"
)

func requireFFmpeg(t *testing.T) {
	t.Helper()
	for _, tool := range Names {
		if _, err := exec.LookPath(tool); err != nil {
			t.Skipf("%s not available: %v", tool, err)
		}
	}
}

// ffmpegGen runs ffmpeg with args, skipping the test if the media cannot be generated.
func ffmpegGen(t *testing.T, args ...string) {
	t.Helper()
	full := append([]string{"-hide_banner", "-nostdin", "-loglevel", "error", "-y"}, args...)
	if out, err := exec.Command("ffmpeg", full...).CombinedOutput(); err != nil {
		t.Skipf("cannot generate test media (%v): %s", err, out)
	}
}

// probeFixtures generates the kinds of files the media modules probe.
func probeFixtures(t *testing.T) map[string]string {
	t.Helper()
	dir := t.TempDir()
	path := func(name string) string { return filepath.Join(dir, name) }
	video := []string{"-f", "lavfi", "-i", "testsrc=size=320x240:rate=25:duration=1"}
	sine := []string{"-f", "lavfi", "-i", "sine=frequency=440:duration=1"}
	cat := func(parts ...[]string) []string {
		var all []string
		for _, p := range parts {
			all = append(all, p...)
		}
		return all
	}

	ffmpegGen(t, cat(video, sine, []string{"-c:v", "libx264", "-c:a", "aac", "-ac", "2", path("h264.mp4")})...)
	ffmpegGen(t, cat(video, sine, []string{"-c:v", "libx264", "-c:a", "libvorbis",
		"-metadata:s:v:0", "BPS=1234567", "-metadata:s:v:0", "BPS-ger=7654321", path("bps.mkv")})...)
	ffmpegGen(t, cat([]string{"-f", "lavfi", "-i", "testsrc=size=64x64:rate=1:duration=1", "-frames:v", "1"}, []string{path("cover.png")})...)
	ffmpegGen(t, cat(sine, []string{"-i", path("cover.png"), "-map", "0:a", "-map", "1:v", "-c:a", "libmp3lame", "-c:v", "png",
		"-disposition:v:0", "attached_pic", "-id3v2_version", "3", path("cover.mp3")})...)
	ffmpegGen(t, cat(sine, []string{"-c:a", "flac", path("audio.flac")})...)
	ffmpegGen(t, cat(video, sine, []string{"-c:v", "libx264", "-c:a", "aac", "-metadata", "SANITIZER_VIDEO=processed",
		"-movflags", "+use_metadata_tags", path("tagged.mp4")})...)
	ffmpegGen(t, cat(sine, []string{"-c:a", "libvorbis", "-metadata", "SANITIZER_NORMALIZED=-3.0dB", path("tagged.ogg")})...)
	ffmpegGen(t, cat(video, []string{"-c:v", "mpeg4", path("video-only.avi")})...)
	names := map[string]string{}
	for _, n := range []string{"h264.mp4", "bps.mkv", "cover.mp3", "audio.flac", "tagged.mp4", "tagged.ogg", "video-only.avi"} {
		names[n] = path(n)
	}
	return names
}

// ffprobeOld runs ffprobe the way the media modules did before Probe: one call per field.
func ffprobeOld(t *testing.T, file string, args ...string) string {
	t.Helper()
	out, err := exec.Command("ffprobe", append(append([]string{"-v", "error"}, args...), "--", file)...).Output()
	if err != nil {
		t.Fatalf("ffprobe %v %s: %v", args, file, err)
	}
	return strings.TrimSpace(string(out))
}

// TestProbeMatchesFieldProbes compares Probe with the separate ffprobe calls it replaces
// in fixvideos, fixaudio and check, for the same files.
func TestProbeMatchesFieldProbes(t *testing.T) {
	requireFFmpeg(t)
	for name, file := range probeFixtures(t) {
		t.Run(name, func(t *testing.T) {
			p, err := Probe(context.Background(), file)
			if err != nil {
				t.Fatal(err)
			}
			v, hasVideo := p.FirstStream("video")
			a, hasAudio := p.FirstStream("audio")

			// fixvideos: codec, size, frame rate of v:0; container duration; stream types.
			if got := ffprobeOld(t, file, "-select_streams", "v:0", "-show_entries", "stream=codec_name", "-of", "csv=p=0"); got != v.CodecName {
				t.Errorf("v:0 codec = %q, Probe %q", got, v.CodecName)
			}
			if hasVideo {
				if got, want := ffprobeOld(t, file, "-select_streams", "v:0", "-show_entries", "stream=width,height", "-of", "csv=p=0"),
					strconv.Itoa(v.Width)+","+strconv.Itoa(v.Height); got != want {
					t.Errorf("v:0 size = %q, Probe %q", got, want)
				}
				if got := ffprobeOld(t, file, "-select_streams", "v:0", "-show_entries", "stream=r_frame_rate", "-of", "csv=p=0"); got != v.RFrameRate {
					t.Errorf("v:0 r_frame_rate = %q, Probe %q", got, v.RFrameRate)
				}
			}
			if got := ffprobeOld(t, file, "-show_entries", "format=duration", "-of", "default=noprint_wrappers=1:nokey=1"); got != p.Format.Duration {
				t.Errorf("format duration = %q, Probe %q", got, p.Format.Duration)
			}
			types := ffprobeOld(t, file, "-show_entries", "stream=codec_type", "-of", "default=nokey=1:noprint_wrappers=1")
			if strings.Contains(types, "audio") != hasAudio || strings.Contains(types, "video") != hasVideo {
				t.Errorf("stream types %q, Probe audio %v video %v", types, hasAudio, hasVideo)
			}

			// fixvideos: bit_rate, tags and duration of v:0; container and stream tags.
			var vOld struct {
				Streams []Stream `json:"streams"`
			}
			jsonOld(t, ffprobeOld(t, file, "-select_streams", "v:0", "-show_entries", "stream=bit_rate,duration:stream_tags", "-of", "json"), &vOld)
			if hasVideo != (len(vOld.Streams) == 1) {
				t.Fatalf("v:0 streams %+v, Probe has video %v", vOld.Streams, hasVideo)
			}
			if hasVideo {
				if o := vOld.Streams[0]; o.BitRate != v.BitRate || o.Duration != v.Duration || !sameTags(o.Tags, v.Tags) {
					t.Errorf("v:0 = %+v, Probe %+v", o, v)
				}
			}
			var tagsOld struct {
				Streams []Stream `json:"streams"`
				Format  Format   `json:"format"`
			}
			jsonOld(t, ffprobeOld(t, file, "-show_entries", "format=duration:format_tags:stream_tags", "-of", "json"), &tagsOld)
			if !sameTags(tagsOld.Format.Tags, p.Format.Tags) || len(tagsOld.Streams) != len(p.Streams) {
				t.Errorf("tags = %+v, Probe %+v", tagsOld, p)
			}
			for i := range min(len(tagsOld.Streams), len(p.Streams)) {
				if !sameTags(tagsOld.Streams[i].Tags, p.Streams[i].Tags) {
					t.Errorf("stream %d tags = %v, Probe %v", i, tagsOld.Streams[i].Tags, p.Streams[i].Tags)
				}
			}

			// fixaudio: a:0 and the container bit rate.
			var aOld struct {
				Streams []Stream `json:"streams"`
				Format  Format   `json:"format"`
			}
			jsonOld(t, ffprobeOld(t, file, "-select_streams", "a:0", "-show_entries",
				"stream=codec_name,profile,channels,sample_fmt,bit_rate:stream_tags:format=bit_rate:format_tags", "-of", "json"), &aOld)
			if hasAudio != (len(aOld.Streams) == 1) {
				t.Fatalf("a:0 streams %+v, Probe has audio %v", aOld.Streams, hasAudio)
			}
			if aOld.Format.BitRate != p.Format.BitRate {
				t.Errorf("format bit_rate = %q, Probe %q", aOld.Format.BitRate, p.Format.BitRate)
			}
			if hasAudio {
				o := aOld.Streams[0]
				if o.CodecName != a.CodecName || o.Profile != a.Profile || o.Channels != a.Channels || o.SampleFmt != a.SampleFmt ||
					o.BitRate != a.BitRate || !sameTags(o.Tags, a.Tags) {
					t.Errorf("a:0 = %+v, Probe %+v", o, a)
				}
			}

			// check: every stream's type, codec, size, bit rate and attached_pic.
			var cOld struct {
				Streams []Stream `json:"streams"`
				Format  Format   `json:"format"`
			}
			jsonOld(t, ffprobeOld(t, file, "-show_entries",
				"format=duration,bit_rate:stream=codec_type,codec_name,bit_rate,width,height:stream_tags=BPS,BPS-eng:stream_disposition=attached_pic",
				"-of", "json"), &cOld)
			if cOld.Format.Duration != p.Format.Duration || cOld.Format.BitRate != p.Format.BitRate || len(cOld.Streams) != len(p.Streams) {
				t.Fatalf("check probe = %+v, Probe %+v", cOld, p)
			}
			for i, o := range cOld.Streams {
				n := p.Streams[i]
				if o.CodecType != n.CodecType || o.CodecName != n.CodecName || o.BitRate != n.BitRate || o.Width != n.Width ||
					o.Height != n.Height || o.Disposition != n.Disposition || o.Tags["BPS"] != n.Tags["BPS"] || o.Tags["BPS-eng"] != n.Tags["BPS-eng"] {
					t.Errorf("check stream %d = %+v, Probe %+v", i, o, n)
				}
			}
		})
	}
}

// What the fixtures are meant to cover is really in them.
func TestProbeFixtures(t *testing.T) {
	requireFFmpeg(t)
	f := probeFixtures(t)
	probe := func(name string) ProbeInfo {
		p, err := Probe(context.Background(), f[name])
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		return p
	}
	if v, _ := probe("h264.mp4").FirstStream("video"); v.CodecName != "h264" || v.Width != 320 || v.PixFmt == "" || ParseBitrate(v.BitRate) == 0 {
		t.Errorf("h264.mp4 video = %+v", v)
	}
	if v, _ := probe("bps.mkv").FirstStream("video"); v.BitRate != "" || v.Tags["BPS"] != "1234567" || v.Tags["BPS-ger"] != "7654321" {
		t.Errorf("bps.mkv video = %+v, want BPS tags and no bit_rate", v)
	}
	if v, ok := probe("cover.mp3").FirstStream("video"); !ok || v.Disposition.AttachedPic != 1 {
		t.Errorf("cover.mp3 video = %+v, want an attached picture", v)
	}
	if p := probe("audio.flac"); p.HasStream("video") || !p.HasStream("audio") {
		t.Errorf("audio.flac = %+v, want audio only", p)
	}
	if p := probe("tagged.mp4"); p.Format.Tags["SANITIZER_VIDEO"] != "processed" {
		t.Errorf("tagged.mp4 format tags = %v", p.Format.Tags)
	}
	if a, _ := probe("tagged.ogg").FirstStream("audio"); a.Tags["SANITIZER_NORMALIZED"] == "" {
		t.Errorf("tagged.ogg audio tags = %v, want the marker as a stream tag", a.Tags)
	}
	if p := probe("video-only.avi"); p.HasStream("audio") || !p.HasStream("video") {
		t.Errorf("video-only.avi = %+v", p)
	}
}

func jsonOld(t *testing.T, out string, v any) {
	t.Helper()
	if err := json.Unmarshal([]byte(out), v); err != nil {
		t.Fatalf("parsing %q: %v", out, err)
	}
}

func sameTags(a, b map[string]string) bool {
	return len(a) == 0 && len(b) == 0 || reflect.DeepEqual(a, b)
}

func TestProbeErrors(t *testing.T) {
	path := filepath.Join(t.TempDir(), "x.mp4")
	t.Run("ffprobe cannot run", func(t *testing.T) {
		fakeTools(t, nil)
		if _, err := Probe(context.Background(), path); !errors.Is(err, ErrNotRun) {
			t.Errorf("err = %v, want ErrNotRun", err)
		}
	})
	t.Run("ffprobe fails", func(t *testing.T) {
		fakeTools(t, map[string]string{"ffprobe": "#!/bin/sh\nfor a; do last=$a; done\necho \"$last: Invalid data\" >&2\nexit 1\n"})
		info, err := Probe(context.Background(), path)
		var pe *ProbeError
		if !errors.As(err, &pe) || errors.Is(err, ErrNotRun) || pe.Stderr != path+": Invalid data\n" || info.Log != pe.Stderr {
			t.Fatalf("Probe = %+v, %v; want a ProbeError with the message", info, err)
		}
		var exitErr *exec.ExitError
		if !errors.As(err, &exitErr) || exitErr.ExitCode() != 1 {
			t.Errorf("err = %v, want it to wrap the exit error", err)
		}
		if want := "ffprobe: exit status 1: " + path + ": Invalid data"; err.Error() != want {
			t.Errorf("Error() = %q, want %q", err.Error(), want)
		}
	})
	t.Run("ffprobe fails silently", func(t *testing.T) {
		fakeTools(t, map[string]string{"ffprobe": "#!/bin/sh\nexit 3\n"})
		if _, err := Probe(context.Background(), path); err == nil || err.Error() != "ffprobe: exit status 3" {
			t.Errorf("err = %v", err)
		}
	})
	t.Run("output is no JSON", func(t *testing.T) {
		fakeTools(t, map[string]string{"ffprobe": "#!/bin/sh\necho nope\necho warned >&2\n"})
		info, err := Probe(context.Background(), path)
		var pe *ProbeError
		if err == nil || errors.As(err, &pe) || errors.Is(err, ErrNotRun) || !strings.Contains(err.Error(), "parsing ffprobe output") {
			t.Errorf("err = %v, want a parse error", err)
		}
		if info.Log != "warned\n" {
			t.Errorf("Log = %q", info.Log)
		}
	})
	t.Run("warnings are kept", func(t *testing.T) {
		fakeTools(t, map[string]string{"ffprobe": "#!/bin/sh\necho '{\"format\":{\"duration\":\"2.5\"}}'\necho 'Estimating duration from bitrate' >&2\n"})
		info, err := Probe(context.Background(), path)
		if err != nil || info.Log != "Estimating duration from bitrate\n" || info.Format.Duration != "2.5" {
			t.Errorf("Probe = %+v, %v", info, err)
		}
	})
	t.Run("a file name like an option", func(t *testing.T) {
		fakeTools(t, map[string]string{"ffprobe": "#!/bin/sh\nfor a; do prev=$last; last=$a; done\necho \"{\\\"format\\\":{\\\"bit_rate\\\":\\\"$prev $last\\\"}}\"\n"})
		info, err := Probe(context.Background(), "-x.mp4")
		if err != nil || info.Format.BitRate != "-- -x.mp4" {
			t.Errorf("Probe = %+v, %v; want the name after --", info, err)
		}
	})
}

func TestProbeInfoHelpers(t *testing.T) {
	p, err := ParseProbe([]byte(`{"streams":[{"index":0,"codec_type":"video","disposition":{"attached_pic":1}},
		{"index":1,"codec_type":"audio","codec_name":"mp3"},{"index":2,"codec_type":"video","codec_name":"h264"}],"format":{}}`))
	if err != nil {
		t.Fatal(err)
	}
	if v, ok := p.FirstStream("video"); !ok || v.Index != 0 || v.Disposition.AttachedPic != 1 {
		t.Errorf("FirstStream(video) = %+v, %v; want stream 0 (as v:0 selects it)", v, ok)
	}
	if a, ok := p.FirstStream("audio"); !ok || a.CodecName != "mp3" {
		t.Errorf("FirstStream(audio) = %+v, %v", a, ok)
	}
	if _, ok := p.FirstStream("subtitle"); ok || p.HasStream("subtitle") || !p.HasStream("audio") {
		t.Error("subtitle stream found")
	}
	if _, err := ParseProbe([]byte("{")); err == nil {
		t.Error("ParseProbe({) = nil error")
	}
	for in, want := range map[string]int64{"128000": 128000, " 700000 ": 700000, "N/A": 0, "": 0, "0": 0, "-5": 0, "1.5": 0} {
		if got := ParseBitrate(in); got != want {
			t.Errorf("ParseBitrate(%q) = %d, want %d", in, got, want)
		}
	}
	for in, want := range map[string]float64{"123.456000": 123.456, "N/A": 0, "": 0, "0": 0, "-1": 0} {
		if got, ok := ParseSeconds(in); got != want || ok != (want > 0) {
			t.Errorf("ParseSeconds(%q) = %v, %v; want %v", in, got, ok, want)
		}
	}
}

func TestVideoBitrate(t *testing.T) {
	tests := []struct {
		name, json string
		want       int64
	}{
		{"stream bit_rate", `{"streams":[{"codec_type":"video","bit_rate":"1500000","tags":{"BPS":"9"}}],"format":{"bit_rate":"7"}}`, 1500000},
		{"BPS tag", `{"streams":[{"codec_type":"video","tags":{"DURATION":"x","BPS":"2500000"}},{"codec_type":"audio"}]}`, 2500000},
		{"BPS-eng with blanks", `{"streams":[{"codec_type":"video","tags":{"bps-eng":" 700000 "}}]}`, 700000},
		{"BPS of another language", `{"streams":[{"codec_type":"video","tags":{"BPS-ger":"4500000"}},{"codec_type":"audio"}],"format":{"bit_rate":"9000000"}}`, 4500000},
		{"plain BPS wins", `{"streams":[{"codec_type":"video","tags":{"BPS-eng":"111","BPS":"222","BPS-ger":"333"}}]}`, 222},
		{"sorted BPS-* keys", `{"streams":[{"codec_type":"video","tags":{"BPS-zzz":"9","BPS-ger":"333","BPS-eng":"111"}}]}`, 111},
		{"invalid tags skipped", `{"streams":[{"codec_type":"video","tags":{"BPS":"N/A","BPS-aaa":"0","BPS-eng":"111"}}]}`, 111},
		{"BPS- alone is no tag", `{"streams":[{"codec_type":"video","tags":{"BPS-":"5","BPSX":"6"}}],"format":{"bit_rate":"7"}}`, 7},
		{"container without audio", `{"streams":[{"codec_type":"video"},{"codec_type":"subtitle"}],"format":{"bit_rate":"1500000"}}`, 1500000},
		{"container with audio", `{"streams":[{"codec_type":"video"},{"codec_type":"audio","bit_rate":"128000"}],"format":{"bit_rate":"1500000"}}`, 0},
		{"unknown", `{"streams":[{"codec_type":"video","bit_rate":"N/A"}],"format":{"bit_rate":"N/A"}}`, 0},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			p, err := ParseProbe([]byte(tt.json))
			if err != nil {
				t.Fatal(err)
			}
			for range 20 { // map order is randomized
				if got := VideoBitrate(p, p.Streams[0]); got != tt.want {
					t.Fatalf("VideoBitrate = %d, want %d", got, tt.want)
				}
			}
		})
	}
}

func TestHasTag(t *testing.T) {
	tests := []struct {
		json string
		want bool
	}{
		{`{"format":{"tags":{"a":"1","Sanitizer_Normalized":""}}}`, true},
		{`{"streams":[{"tags":{"title":"x"}},{"tags":{"sanitizer_normalized":"-3.0dB"}}]}`, true},
		{`{"streams":[{"tags":{"SANITIZER_NORMALIZED_X":"1"}}],"format":{"tags":{"XSANITIZER_NORMALIZED":"1"}}}`, false},
		{`{"streams":[{}],"format":{}}`, false},
		{`{}`, false},
	}
	for _, tt := range tests {
		p, err := ParseProbe([]byte(tt.json))
		if err != nil {
			t.Fatal(err)
		}
		if got := p.HasTag("SANITIZER_NORMALIZED"); got != tt.want {
			t.Errorf("HasTag in %s = %v, want %v", tt.json, got, tt.want)
		}
	}
}

// The marker tags the modules write are found in real files: in the container (mp4 with
// use_metadata_tags) and in the stream (Ogg comments).
func TestHasTagInFiles(t *testing.T) {
	requireFFmpeg(t)
	f := probeFixtures(t)
	for name, tag := range map[string]string{"tagged.mp4": "SANITIZER_VIDEO", "tagged.ogg": "SANITIZER_NORMALIZED"} {
		p, err := Probe(context.Background(), f[name])
		if err != nil {
			t.Fatal(err)
		}
		if !p.HasTag(tag) || p.HasTag("SANITIZER_OTHER") {
			t.Errorf("%s: HasTag(%s) = %v, want only that tag", name, tag, p.HasTag(tag))
		}
	}
	p, err := Probe(context.Background(), f["h264.mp4"])
	if err != nil || p.HasTag("SANITIZER_VIDEO") {
		t.Errorf("h264.mp4: HasTag = true or %v, want no marker", err)
	}
}
