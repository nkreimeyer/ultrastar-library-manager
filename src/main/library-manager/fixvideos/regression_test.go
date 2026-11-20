package fixvideos

import (
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/nkreimeyer/library-manager/config"
	"github.com/nkreimeyer/library-manager/fsops"
)

// probeVideo returns ffprobe's csv line for the given stream entries of the first video
// stream of file.
func probeVideo(t *testing.T, file, entries string) string {
	t.Helper()
	out, err := exec.Command("ffprobe", "-v", "error", "-select_streams", "v:0", "-show_entries", "stream="+entries, "-of", "csv=p=0", file).Output()
	if err != nil {
		t.Fatalf("ffprobe %s: %v", file, err)
	}
	return strings.TrimSpace(string(out))
}

// An odd-sized video that needs a re-encode (but no downscale) is encoded with even
// dimensions, which libx264 requires; before, ffmpeg failed on every run.
func TestReEncodeOddDimensions(t *testing.T) {
	requireFFmpeg(t)
	cfg, songDir, _ := newLibrary(t, false)
	song := filepath.Join(songDir, "Artist", "Artist - Song")
	writeFile(t, filepath.Join(song, "song.txt"), "#TITLE:Song\n#ARTIST:Artist\n#MP3:video.avi\n#VIDEO:video.avi\n#VIDEOGAP:0\n: 0 4 0 La\nE\n")
	makeMediaFrom(t, filepath.Join(song, "video.avi"), "testsrc=size=853x481:rate=25:duration=1", "-c:v", "mpeg4", "-q:v", "1", "-c:a", "ac3")

	ops := fsops.New(false)
	if err := Run(cfg, ops); err != nil {
		t.Fatal(err)
	}
	if st := ops.Stats(); st.Failed != 0 {
		t.Errorf("stats = %+v, want 0 failed", st)
	}
	mp4 := filepath.Join(song, "video.mp4")
	if _, err := os.Stat(mp4); err != nil {
		t.Fatalf("video.mp4 not created: %v", err)
	}
	if got := probeVideo(t, mp4, "width,height"); got != "852,480" {
		t.Errorf("dimensions = %q, want 852,480", got)
	}
	assertNoTempFiles(t, songDir)
}

// A 10-bit source is re-encoded to 8-bit 4:2:0 H.264, which every player can decode.
func TestReEncode10BitGives8Bit420(t *testing.T) {
	requireFFmpeg(t)
	cfg, songDir, _ := newLibrary(t, false)
	song := filepath.Join(songDir, "A - S")
	writeFile(t, filepath.Join(song, "song.txt"), "#TITLE:S\n#ARTIST:A\n#VIDEO:video.mkv\n")
	// Lossless 10-bit VP9: the codec needs re-encoding and the input is far above the estimate.
	makeMedia(t, filepath.Join(song, "video.mkv"), "-pix_fmt", "yuv420p10le", "-c:v", "libvpx-vp9", "-lossless", "1", "-an")

	ops := fsops.New(false)
	if err := Run(cfg, ops); err != nil {
		t.Fatal(err)
	}
	mp4 := filepath.Join(song, "video.mp4")
	if _, err := os.Stat(mp4); err != nil {
		t.Fatalf("video.mp4 not created (stats %+v): %v", ops.Stats(), err)
	}
	if got := probeVideo(t, mp4, "codec_name,pix_fmt"); got != "h264,yuv420p" {
		t.Errorf("re-encoded video = %q, want h264,yuv420p", got)
	}
}

// If the re-encode comes out larger but the audio has to be stripped, the audio is still
// stripped by a stream copy; before, the original was tagged with its audio and skipped
// forever.
func TestLargerReEncodeStillStripsAudio(t *testing.T) {
	requireFFmpeg(t)
	cfg, songDir, _ := newLibrary(t, false)
	song := filepath.Join(songDir, "A - S")
	txt := "#TITLE:S\n#ARTIST:A\n#MP3:song.mp3\n#VIDEO:video.mkv\n"
	writeFile(t, filepath.Join(song, "song.txt"), txt)
	writeFile(t, filepath.Join(song, "song.mp3"), "separate audio")
	video := filepath.Join(song, "video.mkv")
	// The tiny VP9 stream plus a small audio track is smaller than its H.264 re-encode.
	makeMediaFrom(t, video, "testsrc2=size=320x240:rate=25:duration=1",
		"-c:v", "libvpx-vp9", "-crf", "63", "-b:v", "0", "-c:a", "ac3", "-b:a", "64k")

	ops := fsops.New(false)
	if err := Run(cfg, ops); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(song, "video.mp4")); err == nil {
		t.Skip("re-encode was not larger in this ffmpeg build; scenario not hit")
	}
	if st := ops.Stats(); st.Failed != 0 || st.Replaced != 1 {
		t.Errorf("stats = %+v, want 1 replacement", st)
	}
	if got := probeStreams(t, video); !slices.Equal(got, []string{"video:vp9"}) {
		t.Errorf("streams = %v, want the vp9 video only (audio stripped)", got)
	}
	assertTagged(t, video, true)
	if got := readFile(t, filepath.Join(song, "song.txt")); got != txt {
		t.Errorf("txt changed to %q", got)
	}
	assertNoTempFiles(t, songDir)

	// The second run leaves the video alone.
	ops = fsops.New(false)
	if err := Run(cfg, ops); err != nil {
		t.Fatal(err)
	}
	if st := ops.Stats(); st != (fsops.Stats{}) {
		t.Errorf("second run Stats = %+v, want nothing done", st)
	}
}

// A 0-byte #MP3 file is not a usable separate audio source, so the video's audio
// (possibly the song's only audio) is kept.
func TestEmptySeparateAudioKeepsVideoAudio(t *testing.T) {
	requireFFmpeg(t)
	cfg, songDir, _ := newLibrary(t, false)
	song := filepath.Join(songDir, "A - S")
	writeFile(t, filepath.Join(song, "song.txt"), "#TITLE:S\n#ARTIST:A\n#MP3:song.mp3\n#VIDEO:video.mp4\n")
	writeFile(t, filepath.Join(song, "song.mp3"), "")
	video := filepath.Join(song, "video.mp4")
	makeMedia(t, video, "-c:v", "libx264", "-crf", "35", "-c:a", "aac")

	ops := fsops.New(false)
	if err := Run(cfg, ops); err != nil {
		t.Fatal(err)
	}
	if st := ops.Stats(); st != (fsops.Stats{}) {
		t.Errorf("Stats = %+v, want nothing done", st)
	}
	if streams := probeStreams(t, video); !slices.Contains(streams, "audio:aac") {
		t.Errorf("streams = %v: the video's audio was stripped although song.mp3 is empty", streams)
	}
}

// A conversion whose target name is taken is skipped before encoding, so the dry run plans
// what the real run does and the real run does not re-encode on every run.
func TestConversionBlockedByExistingMp4(t *testing.T) {
	requireFFmpeg(t)
	songDir, outDir := t.TempDir(), t.TempDir()
	song := filepath.Join(songDir, "A - S")
	txt := "#TITLE:S\n#ARTIST:A\n#MP3:song.mp3\n#VIDEO:video.avi\n"
	writeFile(t, filepath.Join(song, "song.txt"), txt)
	writeFile(t, filepath.Join(song, "song.mp3"), "audio")
	makeMedia(t, filepath.Join(song, "video.avi"), "-c:v", "mpeg4", "-q:v", "1", "-c:a", "ac3")
	// An unrelated, already fine mp4 (h264, no audio, low bitrate) occupies the target name.
	makeMedia(t, filepath.Join(song, "video.mp4"), "-c:v", "libx264", "-crf", "35", "-an")
	before := snapshot(t, songDir)

	dryCfg, err := config.New(songDir, outDir, true)
	if err != nil {
		t.Fatal(err)
	}
	dry := fsops.New(true)
	if err := Run(dryCfg, dry); err != nil {
		t.Fatal(err)
	}
	if st := dry.Stats(); st != (fsops.Stats{}) {
		t.Errorf("dry run Stats = %+v, want nothing planned", st)
	}

	cfg, err := config.New(songDir, outDir, false)
	if err != nil {
		t.Fatal(err)
	}
	real := fsops.New(false)
	if err := Run(cfg, real); err != nil {
		t.Fatal(err)
	}
	if st := real.Stats(); st != (fsops.Stats{}) {
		t.Errorf("real run Stats = %+v, want nothing done", st)
	}
	if after := snapshot(t, songDir); !slices.Equal(before, after) {
		t.Errorf("library changed\nbefore: %v\n after: %v", before, after)
	}
}

// In a dry run, a target an earlier module would move away does not block the conversion,
// as in the real run where it is gone by then.
func TestConversionTargetPlannedAwayIsPlanned(t *testing.T) {
	requireFFmpeg(t)
	cfg, songDir, _ := newLibrary(t, true)
	song := filepath.Join(songDir, "A - S")
	writeFile(t, filepath.Join(song, "song.txt"), "#TITLE:S\n#ARTIST:A\n#VIDEO:video.avi\n")
	makeMedia(t, filepath.Join(song, "video.avi"), "-c:v", "mpeg4", "-q:v", "1", "-c:a", "ac3")
	writeFile(t, filepath.Join(song, "video.mp4"), "unreferenced leftover")

	ops := fsops.New(true)
	if _, err := ops.MoveInto(filepath.Join(song, "video.mp4"), cfg.ExtraFilesDir(), "A - S/video.mp4"); err != nil {
		t.Fatal(err)
	}
	if err := Run(cfg, ops); err != nil {
		t.Fatal(err)
	}
	if st := ops.Stats(); st.Replaced != 1 || st.Failed != 0 {
		t.Errorf("Stats = %+v, want 1 planned conversion", st)
	}
}

// A low-bitrate H.264 video in MKV (no per-stream bitrate) is not re-encoded just because
// its audio track is large: the container bitrate includes the audio.
func TestGetNeedsReEncodeMKVAudioNotCountedAsVideoBitrate(t *testing.T) {
	requireFFmpeg(t)
	dir := t.TempDir()
	mkv := filepath.Join(dir, "video.mkv")
	makeMedia(t, mkv, "-c:v", "libx264", "-crf", "35", "-c:a", "pcm_s24le", "-ar", "96000", "-ac", "2")
	br, err := getVideoBitrate(mkv)
	if err != nil || br <= 0 || br > 200000 {
		t.Errorf("getVideoBitrate = %d, %v; want the video's own low bitrate", br, err)
	}
	if getNeedsReEncode(mkv, "h264") {
		t.Errorf("low-bitrate H.264 in MKV must not be re-encoded (bitrate %d)", br)
	}
}

// A high-bitrate H.264 video in MKV is still detected from its packet sizes.
func TestGetVideoBitrateMKVHighBitrate(t *testing.T) {
	requireFFmpeg(t)
	mkv := filepath.Join(t.TempDir(), "video.mkv")
	makeMediaFrom(t, mkv, "testsrc2=size=640x480:rate=25:duration=1", "-c:v", "libx264", "-qp", "0", "-an")
	br, err := getVideoBitrate(mkv)
	if err != nil || br <= BitrateThreshold {
		t.Fatalf("getVideoBitrate = %d, %v; want above %d", br, err, BitrateThreshold)
	}
	if !getNeedsReEncode(mkv, "h264") {
		t.Errorf("high-bitrate H.264 in MKV must be re-encoded (bitrate %d)", br)
	}
}

func TestParseStreamBitrate(t *testing.T) {
	tests := []struct {
		name, json string
		want       int
		ok         bool
	}{
		{"stream bit_rate", `{"streams":[{"bit_rate":"1500000","tags":{"BPS":"9"}}]}`, 1500000, true},
		{"BPS tag", `{"streams":[{"tags":{"DURATION":"x","BPS":"2500000"}}]}`, 2500000, true},
		{"BPS tag with language", `{"streams":[{"tags":{"bps-eng":" 700000 "}}]}`, 700000, true},
		{"zero bit_rate falls back to BPS", `{"streams":[{"bit_rate":"0","tags":{"BPS":"5"}}]}`, 5, true},
		{"other tags only", `{"streams":[{"tags":{"BPSX":"5","NUMBER_OF_BYTES":"7"}}]}`, 0, false},
		{"invalid BPS", `{"streams":[{"tags":{"BPS":"N/A"}}]}`, 0, false},
		{"zero BPS", `{"streams":[{"tags":{"BPS":"0"}}]}`, 0, false},
		{"no stream", `{"streams":[]}`, 0, false},
		{"not json", `N/A`, 0, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, ok := parseStreamBitrate([]byte(tt.json))
			if got != tt.want || ok != tt.ok {
				t.Errorf("parseStreamBitrate = %d, %v; want %d, %v", got, ok, tt.want, tt.ok)
			}
		})
	}
}

func TestPacketBitrate(t *testing.T) {
	if got, err := packetBitrate([]byte("1000\n500,\n500\n"), 2); err != nil || got != 8000 {
		t.Errorf("packetBitrate = %d, %v; want 8000", got, err)
	}
	for _, tt := range []struct {
		sizes    string
		duration float64
	}{
		{"1000\n", 0},
		{"1000\n", -1},
		{"", 1},
		{"0\n", 1},
		{"12\nN/A\n", 1},
	} {
		if got, err := packetBitrate([]byte(tt.sizes), tt.duration); err == nil {
			t.Errorf("packetBitrate(%q, %v) = %d, want an error", tt.sizes, tt.duration, got)
		}
	}
}

// A failing journal Save is already counted by ops (WriteReport); rememberProcessed and
// markChecked counted it a second time. A failing Add (the file cannot be fingerprinted)
// is not counted by ops, so they must count it themselves.
func TestJournalFailuresCountedOnce(t *testing.T) {
	skipIfRoot(t)
	record := map[string]func(p *processor, file string){
		"rememberProcessed": (*processor).rememberProcessed,
		"markChecked":       (*processor).markChecked,
	}
	for name, fn := range record {
		t.Run(name+" save fails", func(t *testing.T) {
			p, songDir, outDir := newProcessor(t, false)
			video := filepath.Join(songDir, "A - S", "video.avi") // cannot carry the tag
			writeFile(t, video, "video")
			if err := os.Chmod(outDir, 0o500); err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { os.Chmod(outDir, 0o755) })
			fn(p, video)
			if st := p.ops.Stats(); st.Failed != 1 {
				t.Errorf("Failed = %d, want 1 (the failed save, counted once)", st.Failed)
			}
		})
		t.Run(name+" add fails", func(t *testing.T) {
			p, songDir, outDir := newProcessor(t, false)
			missing := filepath.Join(songDir, "A - S", "missing.avi")
			fn(p, missing)
			if st := p.ops.Stats(); st.Failed != 1 {
				t.Errorf("Failed = %d, want 1 (the failed fingerprint)", st.Failed)
			}
			if _, err := os.Stat(filepath.Join(outDir, "media-journal.txt")); !os.IsNotExist(err) {
				t.Errorf("journal written for a file that could not be recorded (err=%v)", err)
			}
		})
	}
}

// With both a plain BPS tag and a language-suffixed one, the result must not depend on
// map iteration order: plain BPS wins, then the BPS-* tags in sorted key order.
func TestParseStreamBitrateDeterministic(t *testing.T) {
	tests := []struct {
		name, json string
		want       int
	}{
		{"plain BPS wins", `{"streams":[{"tags":{"BPS-eng":"111","BPS":"222","BPS-ger":"333"}}]}`, 222},
		{"plain bps any case wins", `{"streams":[{"tags":{"BPS-aaa":"111","bps":"222"}}]}`, 222},
		{"invalid plain BPS falls back to sorted BPS-*", `{"streams":[{"tags":{"BPS":"N/A","BPS-ger":"333","BPS-eng":"111"}}]}`, 111},
		{"sorted BPS-* keys", `{"streams":[{"tags":{"BPS-zzz":"9","BPS-ger":"333","BPS-eng":"111","BPS-fra":"5"}}]}`, 111},
		{"invalid first BPS-* skipped", `{"streams":[{"tags":{"BPS-aaa":"0","BPS-eng":"111","BPS-ger":"333"}}]}`, 111},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			for range 50 { // map order is randomized per iteration
				if got, ok := parseStreamBitrate([]byte(tt.json)); !ok || got != tt.want {
					t.Fatalf("parseStreamBitrate = %d, %v; want %d, true", got, ok, tt.want)
				}
			}
		})
	}
}

// An unknown (0) or negative dimension gives no estimate; anything else is fitted.
func TestEncodedResolution(t *testing.T) {
	tests := []struct {
		w, h, wantW, wantH int
		ok                 bool
	}{
		{0, 1080, 0, 0, false},
		{1920, 0, 0, 0, false},
		{-1, 480, 0, 0, false},
		{640, -2, 0, 0, false},
		{1, 1, 2, 2, true},
		{640, 480, 640, 480, true},
		{3840, 2160, 1920, 1080, true},
	}
	for _, tt := range tests {
		w, h, err := encodedResolution(tt.w, tt.h)
		if (err == nil) != tt.ok || w != tt.wantW || h != tt.wantH {
			t.Errorf("encodedResolution(%d, %d) = %dx%d, %v; want %dx%d, ok=%v", tt.w, tt.h, w, h, err, tt.wantW, tt.wantH, tt.ok)
		}
	}
}

// A processed video exactly as large as the original is used (only a larger one is
// discarded).
func TestSameSizeResultReplacesOriginal(t *testing.T) {
	p, songDir, _ := newProcessor(t, false)
	song := filepath.Join(songDir, "A - S")
	video := filepath.Join(song, "video.mp4")
	writeFile(t, video, "original")
	tmp, err := fsops.TempFileNear(video, ".mp4")
	if err != nil {
		t.Fatal(err)
	}
	writeFile(t, tmp, "encoded!") // same length as "original"

	p.replaceOriginal(song, video, tmp, encodePlan{reEncode: true})

	if got := readFile(t, video); got != "encoded!" {
		t.Errorf("video = %q, want the processed version", got)
	}
	if st := p.ops.Stats(); st.Replaced != 1 || st.Failed != 0 {
		t.Errorf("Stats = %+v, want 1 replacement and no failure", st)
	}
	assertNoTempFiles(t, songDir)
}

// A video whose duration ffprobe cannot tell (Matroska written to a pipe) is not remuxed:
// the remux could not be checked against it.
func TestTagByRemuxNeedsKnownDuration(t *testing.T) {
	requireFFmpeg(t)
	p, songDir, _ := newProcessor(t, false)
	video := filepath.Join(songDir, "A - S", "video.mkv")
	if err := os.MkdirAll(filepath.Dir(video), 0o755); err != nil {
		t.Fatal(err)
	}
	f, err := os.Create(video)
	if err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command("ffmpeg", "-hide_banner", "-nostdin", "-loglevel", "error",
		"-f", "lavfi", "-i", "testsrc=size=64x48:rate=25:duration=1", "-c:v", "mpeg4", "-f", "matroska", "-")
	var stderr strings.Builder
	cmd.Stdout, cmd.Stderr = f, &stderr
	if err := cmd.Run(); err != nil {
		f.Close()
		t.Skipf("cannot generate test media (%v): %s", err, stderr.String())
	}
	f.Close()
	if m, err := probeMarker(video); err != nil || m.duration != 0 {
		t.Fatalf("fixture probe = %+v, %v; want an unknown duration", m, err)
	}
	before := snapshot(t, songDir)

	err = p.tagByRemux(video)
	if err == nil || err.Error() != "unknown duration" {
		t.Errorf("tagByRemux = %v, want the unknown-duration error", err)
	}
	if after := snapshot(t, songDir); !slices.Equal(before, after) {
		t.Errorf("song dir changed\nbefore: %v\n after: %v", before, after)
	}
	if st := p.ops.Stats(); st != (fsops.Stats{}) {
		t.Errorf("Stats = %+v, want nothing done", st)
	}
}

// ffmpeg exits 0 on an AVI with a damaged section and drops what it cannot decode, so the
// re-encode comes out shorter: its video would run ahead of the song from there on, and the
// conversion deletes the original. The original must stay, referenced and reported.
func TestDamagedAviIsNotConverted(t *testing.T) {
	requireFFmpeg(t)
	cfg, songDir, outDir := newLibrary(t, false)
	song := filepath.Join(songDir, "Artist - Song")
	txt := "#TITLE:Song\n#ARTIST:Artist\n#MP3:video.avi\n#VIDEO:video.avi\n: 0 4 0 La\nE\n"
	writeFile(t, filepath.Join(song, "song.txt"), txt)
	video := filepath.Join(song, "video.avi")
	args := []string{"-hide_banner", "-nostdin", "-loglevel", "error",
		"-f", "lavfi", "-i", "testsrc=size=320x240:rate=25:duration=4",
		"-f", "lavfi", "-i", "sine=frequency=440:duration=4",
		"-c:v", "mpeg4", "-q:v", "1", "-c:a", "ac3", "-y", video}
	if out, err := exec.Command("ffmpeg", args...).CombinedOutput(); err != nil {
		t.Skipf("cannot generate test media (%v): %s", err, out)
	}
	// Zero 30 % of the file from 30 % on: the header and the index stay intact.
	data, err := os.ReadFile(video)
	if err != nil {
		t.Fatal(err)
	}
	clear(data[len(data)*3/10 : len(data)*6/10])
	if err := os.WriteFile(video, data, 0o644); err != nil {
		t.Fatal(err)
	}
	if d, err := videoStreamDuration(video); err != nil || !durationsMatch(d, 4) {
		t.Fatalf("fixture video duration = %v, %v; want 4 s", d, err)
	}

	ops := fsops.New(false)
	if err := Run(cfg, ops); err != nil {
		t.Fatal(err)
	}
	if st := ops.Stats(); st.Failed != 0 || st.Removed != 0 || st.Replaced != 0 {
		t.Errorf("stats = %+v, want nothing removed, replaced or failed", st)
	}
	if got := readFile(t, video); got != string(data) {
		t.Error("the damaged original changed")
	}
	if got := readFile(t, filepath.Join(song, "song.txt")); got != txt {
		t.Errorf("txt = %q, want it unchanged", got)
	}
	if _, err := os.Stat(filepath.Join(song, "video.mp4")); !os.IsNotExist(err) {
		t.Errorf("video.mp4 exists (err=%v)", err)
	}
	if got, want := readFile(t, filepath.Join(outDir, corruptedReportName)), video+"\n"; got != want {
		t.Errorf("report = %q, want %q", got, want)
	}
	assertNoTempFiles(t, songDir)
}

func TestParseVideoStreamDuration(t *testing.T) {
	for _, tc := range []struct {
		json string
		want float64
	}{
		{`{"streams":[{"duration":"4.000000"}]}`, 4},
		{`{"streams":[{"duration":"N/A","tags":{"DURATION":"00:03:21.120000000"}}]}`, 201.12},
		{`{"streams":[{"tags":{"DURATION-eng":"01:00:00.5"}}]}`, 3600.5},
		{`{"streams":[{"tags":{"duration":"00:00:02.000000000"}}]}`, 2},
		{`{"streams":[{"duration":"0","tags":{"DURATION":"00:00:02"}}]}`, 2},
		{`{"streams":[{"tags":{"DURATIONS":"00:00:02","TITLE":"x"}}]}`, 0},
		{`{"streams":[{"tags":{"DURATION":"garbage"}}]}`, 0},
		{`{"streams":[{"tags":{"DURATION":"00:61:00"}}]}`, 0},
		{`{"streams":[{"tags":{"DURATION":"00:00:00.000"}}]}`, 0},
		{`{"streams":[{"duration":"N/A"}]}`, 0},
		{`{"streams":[]}`, 0},
		{`{}`, 0},
	} {
		got, err := parseVideoStreamDuration([]byte(tc.json))
		if err != nil || math.Abs(got-tc.want) > 1e-9 {
			t.Errorf("parseVideoStreamDuration(%s) = %v, %v; want %v", tc.json, got, err, tc.want)
		}
	}
	if _, err := parseVideoStreamDuration([]byte("{")); err == nil {
		t.Error("parseVideoStreamDuration of broken JSON: no error")
	}
}

// Without a known video duration, the containers are compared if the audio is kept; a
// stripped audio track may have been the longer stream, so then nothing is compared.
func TestSameVideoDurationFallbacks(t *testing.T) {
	requireFFmpeg(t)
	dir := t.TempDir()
	gen := func(name, dur string, extra ...string) string {
		path := filepath.Join(dir, name)
		args := slices.Concat([]string{"-hide_banner", "-nostdin", "-loglevel", "error",
			"-f", "lavfi", "-i", "testsrc=size=64x48:rate=25:duration=" + dur,
			"-f", "lavfi", "-i", "sine=frequency=440:duration=" + dur,
			"-c:v", "mpeg4"}, extra, []string{"-y", path})
		if out, err := exec.Command("ffmpeg", args...).CombinedOutput(); err != nil {
			t.Skipf("cannot generate test media (%v): %s", err, out)
		}
		return path
	}
	long, short := gen("long.mp4", "3"), gen("short.mp4", "1")
	if ok, msg := sameVideoDuration(long, short, true); ok || !strings.Contains(msg, "instead of 3.000 s") {
		t.Errorf("3 s vs 1 s = %v, %q; want a mismatch", ok, msg)
	}
	if ok, _ := sameVideoDuration(long, gen("same.mp4", "3"), false); !ok {
		t.Error("3 s vs 3 s: mismatch")
	}
	// FLV has a container duration, but none per stream.
	noTags := gen("nostream.flv", "3", "-c:v", "flv1")
	if d, err := videoStreamDuration(noTags); err != nil || d != 0 {
		t.Fatalf("fixture has a video duration (%v, %v)", d, err)
	}
	if ok, _ := sameVideoDuration(noTags, short, false); !ok {
		t.Error("unknown video duration, audio stripped: compared anyway")
	}
	if ok, _ := sameVideoDuration(noTags, short, true); ok {
		t.Error("unknown video duration, audio kept: the containers (3 s vs 1 s) were not compared")
	}
	if ok, _ := sameVideoDuration(noTags, gen("same.mkv", "3"), true); !ok {
		t.Error("unknown video duration, audio kept, same container duration: mismatch")
	}
	broken := filepath.Join(dir, "broken.mp4")
	if err := os.WriteFile(broken, []byte("garbage"), 0o644); err != nil {
		t.Fatal(err)
	}
	if ok, msg := sameVideoDuration(long, broken, true); ok || !strings.Contains(msg, "cannot read") {
		t.Errorf("unreadable output = %v, %q; want a mismatch", ok, msg)
	}
}

// A symlinked video is left alone: replacing it would turn the link into a regular copy,
// and a conversion would delete only the link.
func TestSymlinkedVideoIsNotProcessed(t *testing.T) {
	requireFFmpeg(t)
	cfg, songDir, _ := newLibrary(t, false)
	pool := filepath.Join(t.TempDir(), "pool.avi")
	makeMedia(t, pool, "-c:v", "mpeg4", "-q:v", "1", "-c:a", "ac3")
	orig := readFile(t, pool)
	song := filepath.Join(songDir, "A - S")
	txt := "#TITLE:S\n#ARTIST:A\n#MP3:video.avi\n#VIDEO:video.avi\n: 0 4 0 La\nE\n"
	writeFile(t, filepath.Join(song, "song.txt"), txt)
	link := filepath.Join(song, "video.avi")
	if err := os.Symlink(pool, link); err != nil {
		t.Fatal(err)
	}

	ops := fsops.New(false)
	if err := Run(cfg, ops); err != nil {
		t.Fatal(err)
	}
	if st := ops.Stats(); st != (fsops.Stats{}) {
		t.Errorf("stats = %+v, want nothing done", st)
	}
	if target, err := os.Readlink(link); err != nil || target != pool {
		t.Errorf("video.avi is no longer the link to %s: %q, %v", pool, target, err)
	}
	if readFile(t, pool) != orig || readFile(t, filepath.Join(song, "song.txt")) != txt {
		t.Error("the linked video or the txt changed")
	}
	if _, err := os.Stat(filepath.Join(song, "video.mp4")); !os.IsNotExist(err) {
		t.Errorf("video.mp4 exists (err=%v)", err)
	}
}
