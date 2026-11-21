package fixaudio

import (
	"bytes"
	"errors"
	"maps"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/nkreimeyer/library-manager/fsops"
	"github.com/nkreimeyer/library-manager/journal"
)

// --- regressions of confirmed bugs ---

// "-movflags +use_metadata_tags" (needed for the custom tag) made the mov muxer write all
// metadata as QuickTime mdta keys: the cover ("covr") and the iTunes atoms ("©nam") were
// dropped.
func TestM4ACoverAndTagsKept(t *testing.T) {
	requireFFmpeg(t)
	// ALAC is not tested: ffmpeg cannot write an ALAC track next to a cover into MP4
	// ("codec frame size is not set"), so no fixture can be made here.
	for _, codec := range []string{"aac"} {
		t.Run(codec, func(t *testing.T) {
			cfg, ops := newLibrary(t, false)
			dir := addSong(t, cfg, "Artist - Cover", "song.m4a")
			audio := filepath.Join(dir, "song.m4a")
			ffmpeg(t, "-f", "lavfi", "-i", "sine=f=440:d=1", "-f", "lavfi", "-i", "color=red:s=16x16:d=1",
				"-map", "0:a", "-map", "1:v", "-frames:v", "1", "-c:v", "mjpeg", "-disposition:v", "attached_pic",
				"-af", "volume=-20dB", "-c:a", codec,
				"-metadata", "title=Cover Song", "-metadata", "artist=Tester", audio)
			raw0 := readFile(t, audio)
			if !bytes.Contains(raw0, []byte("covr")) || !bytes.Contains(raw0, []byte("\xa9nam")) {
				t.Fatalf("fixture lacks covr/©nam")
			}

			run(t, cfg, ops)

			if got := ops.Stats().Replaced; got != 1 {
				t.Fatalf("replaced = %d, want 1", got)
			}
			assertPeakNear(t, audio, targetPeakDB)
			if got := ffprobeValue(t, audio, "-select_streams", "v", "-show_entries", "stream=codec_name:stream_disposition=attached_pic"); got != "mjpeg\n1" {
				t.Errorf("cover stream = %q, want mjpeg attached_pic", got)
			}
			raw := readFile(t, audio)
			for _, atom := range []string{"covr", "\xa9nam", "\xa9ART"} {
				if !bytes.Contains(raw, []byte(atom)) {
					t.Errorf("no %q atom", atom)
				}
			}
			if got := ffprobeValue(t, audio, "-show_entries", "format_tags=title"); got != "Cover Song" {
				t.Errorf("title tag = %q", got)
			}
			if got := probe(t, audio).Codec; got != codec {
				t.Errorf("codec = %q, want %q", got, codec)
			}
			// Without the tag, the journal keeps the next run from re-encoding.
			again := fsops.New(false)
			run(t, cfg, again)
			if got := again.Stats().Replaced; got != 0 {
				t.Errorf("second run replaced %d files, want 0", got)
			}
		})
	}
}

func TestFLACCoverAndTagsKept(t *testing.T) {
	requireFFmpeg(t)
	cfg, ops := newLibrary(t, false)
	dir := addSong(t, cfg, "Artist - Cover", "song.flac")
	audio := filepath.Join(dir, "song.flac")
	ffmpeg(t, "-f", "lavfi", "-i", "sine=f=440:d=1", "-f", "lavfi", "-i", "color=red:s=16x16:d=1",
		"-map", "0:a", "-map", "1:v", "-frames:v", "1", "-c:v", "mjpeg", "-disposition:v", "attached_pic",
		"-af", "volume=-20dB", "-c:a", "flac", "-sample_fmt", "s16",
		"-metadata", "title=Cover Song", audio)

	run(t, cfg, ops)

	assertPeakNear(t, audio, targetPeakDB)
	if got := ffprobeValue(t, audio, "-select_streams", "v", "-show_entries", "stream=codec_name:stream_disposition=attached_pic"); got != "mjpeg\n1" {
		t.Errorf("cover stream = %q, want mjpeg attached_pic", got)
	}
	if got := ffprobeValue(t, audio, "-show_entries", "format_tags=title"); got != "Cover Song" {
		t.Errorf("title tag = %q", got)
	}
	if info := probe(t, audio); !info.Normalized || info.SampleFmt != "s16" {
		t.Errorf("result = %+v, want a tagged 16-bit flac", info)
	}
}

// The journal's failing Save is already counted by ops (WriteReport); processFile once
// counted it a second time.
func TestJournalSaveFailureCountedOnce(t *testing.T) {
	requireFFmpeg(t)
	if os.Geteuid() == 0 {
		t.Skip("root ignores directory permissions")
	}
	cfg, ops := newLibrary(t, false)
	dir := addSong(t, cfg, "Artist - Quiet", "song.mp3")
	quietMP3(t, filepath.Join(dir, "song.mp3"), "1")
	if err := os.MkdirAll(cfg.OriginalsDir(), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(cfg.OutDir, 0o555); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.Chmod(cfg.OutDir, 0o755) })

	if err := Run(cfg, ops); err != nil {
		t.Fatalf("Run: %v", err)
	}
	st := ops.Stats()
	if st.Replaced != 1 || st.Copied != 1 {
		t.Fatalf("replaced/copied = %d/%d, want 1/1", st.Replaced, st.Copied)
	}
	// One failed save after the file and the final retry in Run.
	if st.Failed != 2 {
		t.Errorf("Failed = %d, want 2 (one per save attempt)", st.Failed)
	}
}

// UltraStar uses the last occurrence of a repeated tag, so the second #VOCALS names the
// stem the player mixes in. That file must not be normalized.
func TestRepeatedStemTagLastWins(t *testing.T) {
	requireFFmpeg(t)
	cfg, ops := newLibrary(t, false)
	dir := addSong(t, cfg, "Artist - Stems", "song.mp3", "#VOCALS:old.mp3", "#VOCALS:vocals.mp3")
	for _, name := range []string{"song.mp3", "vocals.mp3", "old.mp3"} {
		quietMP3(t, filepath.Join(dir, name), "1")
	}
	vocals := readFile(t, filepath.Join(dir, "vocals.mp3"))

	run(t, cfg, ops)

	if !slices.Equal(readFile(t, filepath.Join(dir, "vocals.mp3")), vocals) {
		t.Error("the #VOCALS stem UltraStar plays (the last occurrence) was normalized")
	}
	if got := ops.Stats().Replaced; got != 2 {
		t.Errorf("replaced = %d, want 2 (song.mp3 and the stale old.mp3)", got)
	}
}

// ffmpeg's native aac encoder only produces AAC-LC: an HE-AAC source must be left alone.
func TestHEAACIsNotDowngradedToLC(t *testing.T) {
	requireFFmpeg(t)
	if _, err := exec.LookPath("gst-launch-1.0"); err != nil {
		t.Skip("gst-launch-1.0 not available")
	}
	cfg, ops := newLibrary(t, false)
	dir := addSong(t, cfg, "Artist - HE", "song.m4a")
	audio := filepath.Join(dir, "song.m4a")
	// Quiet white noise (about -16 dBFS peak), HE-AAC v1 at 48 kbit/s.
	if out, err := exec.Command("gst-launch-1.0", "-q", "audiotestsrc", "wave=white-noise", "volume=0.15",
		"num-buffers=200", "!", "audio/x-raw,rate=44100,channels=2,format=S16LE", "!",
		"fdkaacenc", "bitrate=48000", "!", "audio/mpeg,profile=he-aac-v1,stream-format=raw", "!",
		"mp4mux", "!", "filesink", "location="+audio).CombinedOutput(); err != nil {
		t.Skipf("cannot encode HE-AAC: %v\n%s", err, out)
	}
	if p := probe(t, audio).Profile; p != "HE-AAC" {
		t.Skipf("fixture profile = %q, want HE-AAC", p)
	}
	before := readFile(t, audio)

	run(t, cfg, ops)

	if !bytes.Equal(readFile(t, audio), before) {
		t.Errorf("HE-AAC source was changed (now profile %q)", probe(t, audio).Profile)
	}
	if st := ops.Stats(); st.Replaced != 0 || st.Copied != 0 {
		t.Errorf("replaced/copied = %d/%d, want 0/0", st.Replaced, st.Copied)
	}
}

// --- pure unit tests ---

func TestCannotKeep(t *testing.T) {
	tests := []struct {
		info audioInfo
		want string // substring of the reason, "" = can be kept
	}{
		{audioInfo{Codec: "aac", Profile: "LC"}, ""},
		{audioInfo{Codec: "aac"}, ""},
		{audioInfo{Codec: "aac", Profile: "HE-AAC"}, "HE-AAC audio would become AAC-LC"},
		{audioInfo{Codec: "aac", Profile: "HE-AACv2"}, "HE-AACv2"},
		{audioInfo{Codec: "mp3", Profile: "HE-AAC"}, ""}, // the profile only matters for aac
		{audioInfo{Codec: "dts", Profile: "DTS"}, `no encoder known to keep its "dts" audio`},
		{audioInfo{Codec: "truehd"}, "truehd"},
		{audioInfo{Codec: "flac"}, ""},
		{audioInfo{Codec: "pcm_s24le"}, ""},
	}
	for _, tt := range tests {
		got := cannotKeep(tt.info)
		if (got == "") != (tt.want == "") || !strings.Contains(got, tt.want) {
			t.Errorf("cannotKeep(%+v) = %q, want %q", tt.info, got, tt.want)
		}
	}
}

func TestEncoderFor(t *testing.T) {
	tests := []struct {
		codec, encoder string
		lossy, known   bool
	}{
		{"mp3", "libmp3lame", true, true},
		{"mp2", "mp2", true, true},
		{"ac3", "ac3", true, true},
		{"eac3", "eac3", true, true},
		{"aac", "aac", true, true},
		{"vorbis", "libvorbis", true, true},
		{"opus", "libopus", true, true},
		{"wmav1", "wmav1", true, true},
		{"wmav2", "wmav2", true, true},
		{"flac", "flac", false, true},
		{"alac", "alac", false, true},
		{"pcm_s16le", "pcm_s16le", false, true},
		{"pcm_f32le", "pcm_f32le", false, true},
		{"dts", "", false, false},
		{"truehd", "", false, false},
		{"xpcm_s16le", "", false, false},
	}
	for _, tt := range tests {
		enc, lossy, known := encoderFor(tt.codec)
		if enc != tt.encoder || lossy != tt.lossy || known != tt.known {
			t.Errorf("encoderFor(%q) = %q, %v, %v, want %q, %v, %v", tt.codec, enc, lossy, known, tt.encoder, tt.lossy, tt.known)
		}
	}
}

func TestParseProbeProfileAndMarker(t *testing.T) {
	tests := []struct {
		name string
		json string
		want audioInfo
	}{
		{"profile", `{"streams":[{"codec_type":"audio","codec_name":"aac","profile":"HE-AAC","channels":2,"bit_rate":"48000"}]}`,
			audioInfo{Codec: "aac", Profile: "HE-AAC", Channels: 2, Bitrate: 48000}},
		{"marker in stream tags, any case", `{"streams":[{"codec_type":"audio","codec_name":"vorbis","channels":2,"tags":{"sanitizer_normalized":"-3.0dB"}}]}`,
			audioInfo{Codec: "vorbis", Channels: 2, Normalized: true}},
		{"marker in format tags", `{"streams":[{"codec_type":"audio","codec_name":"mp3","channels":2}],"format":{"tags":{"SANITIZER_NORMALIZED":"-3.0dB"}}}`,
			audioInfo{Codec: "mp3", Channels: 2, Normalized: true}},
		// Only the container and the processed (first audio) stream count: a marker on
		// another stream says nothing about 0:a:0 (TestNormalizedMarkerOfProcessedStream).
		{"marker in the tags of another stream", `{"streams":[{"codec_type":"audio","codec_name":"mp3","channels":2},{"codec_type":"video","tags":{"SANITIZER_NORMALIZED":"-3.0dB"}}]}`,
			audioInfo{Codec: "mp3", Channels: 2}},
		{"unrelated tags", `{"streams":[{"codec_type":"audio","codec_name":"mp3","channels":2,"tags":{"title":"x"}}],"format":{"tags":{"SANITIZER_VIDEO":"1","artist":"y"}}}`,
			audioInfo{Codec: "mp3", Channels: 2}},
		{"negative and garbage bitrates ignored", `{"streams":[{"codec_type":"audio","codec_name":"mp3","channels":1,"bit_rate":"-5"}],"format":{"bit_rate":"x"}}`,
			audioInfo{Codec: "mp3", Channels: 1}},
		{"zero stream bitrate falls back", `{"streams":[{"codec_type":"audio","codec_name":"mp3","channels":1,"bit_rate":"0"}],"format":{"bit_rate":"64000"}}`,
			audioInfo{Codec: "mp3", Channels: 1, Bitrate: 64000}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := parseProbe([]byte(tt.json))
			if err != nil {
				t.Fatal(err)
			}
			if got != tt.want {
				t.Errorf("got %+v, want %+v", got, tt.want)
			}
		})
	}
	if _, err := parseProbe([]byte(`{"streams":[{"codec_type":"audio","codec_name":"mp3","channels":-1}]}`)); err == nil {
		t.Error("negative channel count accepted")
	}
}

func TestEncodeArgsDetails(t *testing.T) {
	tests := []struct {
		name       string
		in         string
		info       audioInfo
		wantSeq    [][]string
		notPresent []string
	}{
		{"wav: no tag, keeps sample format", "/s/a/song.wav", audioInfo{Codec: "pcm_s24le", Channels: 2, SampleFmt: "s32"},
			[][]string{{"-map_metadata", "0"}, {"-c:a", "pcm_s24le"}, {"-sample_fmt", "s32"}}, []string{"-metadata", "-b:a"}},
		{"raw aac: no tag", "/s/a/song.AAC", audioInfo{Codec: "aac", Channels: 2, Bitrate: 128000},
			[][]string{{"-c:a", "aac"}, {"-b:a", "128000"}}, []string{"-metadata", "0:v?", "-movflags"}},
		{"m4a: cover, no tag, no movflags", "/s/a/song.m4a", audioInfo{Codec: "aac", Channels: 2, Bitrate: 128000},
			[][]string{{"-map", "0:v?"}, {"-c:v", "copy"}, {"-map_metadata", "0"}}, []string{"-metadata", "-movflags"}},
		{"lossy without bitrate", "/s/a/song.m4a", audioInfo{Codec: "aac", Channels: 2},
			[][]string{{"-c:a", "aac"}}, []string{"-b:a", "-sample_fmt"}},
		{"lossless without sample format", "/s/a/song.flac", audioInfo{Codec: "flac", Channels: 2},
			[][]string{{"-c:a", "flac"}, {"-metadata", "SANITIZER_NORMALIZED=-3.0dB"}}, []string{"-b:a", "-sample_fmt"}},
		{"bitrate at the cap", "/s/a/song.mp3", audioInfo{Codec: "mp3", Channels: 2, Bitrate: maxLossyBitrate},
			[][]string{{"-b:a", "320000"}}, nil},
		{"bitrate just above the cap", "/s/a/song.mp3", audioInfo{Codec: "mp3", Channels: 2, Bitrate: maxLossyBitrate + 1},
			[][]string{{"-b:a", "320000"}}, nil},
		{"opus", "/s/a/song.opus", audioInfo{Codec: "opus", Channels: 2, Bitrate: 96000},
			[][]string{{"-c:a", "libopus"}, {"-b:a", "96000"}, {"-metadata", "SANITIZER_NORMALIZED=-3.0dB"}}, []string{"0:v?", "-sample_fmt"}},
		{"wma", "/s/a/song.wma", audioInfo{Codec: "wmav2", Channels: 2, Bitrate: 128000},
			[][]string{{"-c:a", "wmav2"}, {"-b:a", "128000"}}, []string{"0:v?"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			args := encodeArgs(tt.in, "/s/a/.tmp", tt.info, nil)
			if !slices.Equal(args[:8], []string{"-nostdin", "-hide_banner", "-v", "error", "-y", "-i", tt.in, "-map"}) || args[8] != "0:a:0" {
				t.Errorf("unexpected start of %v", args)
			}
			if args[len(args)-1] != "/s/a/.tmp" {
				t.Errorf("output must be the last argument: %v", args)
			}
			if slices.Contains(args, "-af") {
				t.Errorf("-af without filters: %v", args)
			}
			for _, seq := range tt.wantSeq {
				if !containsSeq(args, seq) {
					t.Errorf("missing %v in %v", seq, args)
				}
			}
			for _, a := range tt.notPresent {
				if slices.Contains(args, a) {
					t.Errorf("unexpected %q in %v", a, args)
				}
			}
		})
	}

	// The downmix runs before the gain, in one filter graph.
	filters := append(baseFilters(true), "volume=1.50dB")
	args := encodeArgs("/s/a/song.mp3", "/s/a/.tmp", audioInfo{Codec: "mp3", Channels: 6, Bitrate: 192000}, filters)
	if !containsSeq(args, []string{"-af", stereoFilter + ",volume=1.50dB"}) {
		t.Errorf("filter graph wrong: %v", args)
	}
	if baseFilters(false) != nil {
		t.Errorf("baseFilters(false) = %v, want none", baseFilters(false))
	}
}

func TestParsePeakLevelUnparseable(t *testing.T) {
	_, err := parsePeakLevel("Peak level dB: nan?x")
	if err == nil || !strings.Contains(err.Error(), `"nan?x"`) {
		t.Errorf("err = %v, want a parse error naming the value", err)
	}
}

func TestLastLines(t *testing.T) {
	tests := []struct {
		in   string
		n    int
		want string
	}{
		{"a\nb\nc\nd\n", 2, "c | d"},
		{"a\nb\nc", 3, "a | b | c"},
		{"a\nb\n", 5, "a | b"},
		{"\n\nonly\n\n", 3, "only"},
		{"", 2, ""},
	}
	for _, tt := range tests {
		if got := lastLines(tt.in, tt.n); got != tt.want {
			t.Errorf("lastLines(%q, %d) = %q, want %q", tt.in, tt.n, got, tt.want)
		}
	}
}

// --- fixture tests for untested paths ---

// ffmpegAt writes a wav whose sample peak is amplitude (linear) of full scale.
func ffmpegAt(t *testing.T, path string, amplitude string) {
	t.Helper()
	ffmpeg(t, "-f", "lavfi", "-i", "aevalsrc="+amplitude+"*sin(2*PI*440*t)|"+amplitude+"*sin(2*PI*440*t):s=44100:d=1",
		"-c:a", "pcm_s16le", path)
}

// Without tag and journal entry, a file already at the target is left alone, and one
// just outside the tolerance is processed.
func TestPeakTolerance(t *testing.T) {
	requireFFmpeg(t)
	cfg, ops := newLibrary(t, false)
	atTarget := filepath.Join(addSong(t, cfg, "A - At Target", "song.wav"), "song.wav")
	ffmpegAt(t, atTarget, "0.7079") // -3.0 dB
	inside := filepath.Join(addSong(t, cfg, "B - Inside", "song.wav"), "song.wav")
	ffmpegAt(t, inside, "0.7328") // -2.7 dB
	outside := filepath.Join(addSong(t, cfg, "C - Outside", "song.wav"), "song.wav")
	ffmpegAt(t, outside, "0.6607") // -3.6 dB
	beforeAt, beforeInside := readFile(t, atTarget), readFile(t, inside)
	for path, want := range map[string]float64{atTarget: -3.0, inside: -2.7, outside: -3.6} {
		if peak, err := measurePeak(path, false); err != nil || math.Abs(peak-want) > 0.05 {
			t.Fatalf("fixture %s peak = %.2f (%v), want %.1f", path, peak, err, want)
		}
	}

	run(t, cfg, ops)

	if st := ops.Stats(); st.Replaced != 1 || st.Copied != 1 {
		t.Errorf("replaced/copied = %d/%d, want 1/1 (only the -3.6 dB file)", st.Replaced, st.Copied)
	}
	if !bytes.Equal(readFile(t, atTarget), beforeAt) || !bytes.Equal(readFile(t, inside), beforeInside) {
		t.Error("a file within the tolerance was changed")
	}
	if peak, err := measurePeak(outside, false); err != nil || math.Abs(peak-targetPeakDB) > 0.05 {
		t.Errorf("-3.6 dB file now at %.2f dB (%v), want %.1f", peak, err, targetPeakDB)
	}
	if _, err := os.Stat(filepath.Join(cfg.OriginalsDir(), "C - Outside", "song.wav")); err != nil {
		t.Errorf("no backup of the processed file: %v", err)
	}
	if _, err := os.Stat(filepath.Join(cfg.OriginalsDir(), "A - At Target")); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("backup made of an unprocessed file: %v", err)
	}
}

func TestSilentAudioIsLeftAlone(t *testing.T) {
	requireFFmpeg(t)
	cfg, ops := newLibrary(t, false)
	wav := filepath.Join(addSong(t, cfg, "A - Silent", "song.wav"), "song.wav")
	ffmpeg(t, "-f", "lavfi", "-i", "anullsrc=r=44100:cl=stereo", "-t", "1", "-c:a", "pcm_s16le", wav)
	six := filepath.Join(addSong(t, cfg, "B - Silent Six", "song.wav"), "song.wav")
	ffmpeg(t, "-f", "lavfi", "-i", "anullsrc=r=44100:cl=5.1", "-t", "1", "-c:a", "pcm_s16le", six)
	before, beforeSix := readFile(t, wav), readFile(t, six)

	run(t, cfg, ops)

	if st := ops.Stats(); st.Replaced != 0 || st.Copied != 0 {
		t.Errorf("replaced/copied = %d/%d, want 0/0", st.Replaced, st.Copied)
	}
	if !bytes.Equal(readFile(t, wav), before) || !bytes.Equal(readFile(t, six), beforeSix) {
		t.Error("a silent file was changed")
	}
}

// fakeFFmpeg puts a wrapper named ffmpeg first on PATH. It runs the real ffmpeg, except for
// the encode (recognized by "-map_metadata"), where it runs onEncode (a shell snippet; $out
// is the output file, the last argument).
func fakeFFmpeg(t *testing.T, onEncode string) {
	t.Helper()
	realFFmpeg, err := exec.LookPath("ffmpeg")
	if err != nil {
		t.Skip("ffmpeg not available")
	}
	bin := t.TempDir()
	script := "#!/bin/sh\nfor a in \"$@\"; do out=$a; done\nfor a in \"$@\"; do\n" +
		"  if [ \"$a\" = -map_metadata ]; then\n" + onEncode + "\n  fi\ndone\n" +
		"exec '" + realFFmpeg + "' \"$@\"\n"
	if err := os.WriteFile(filepath.Join(bin, "ffmpeg"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
}

func TestEncodeFailureLeavesNoTrace(t *testing.T) {
	requireFFmpeg(t)
	if _, err := exec.LookPath("sh"); err != nil {
		t.Skip("no sh")
	}
	fakeFFmpeg(t, "    echo 'encoder exploded' >&2; exit 1")
	cfg, ops := newLibrary(t, false)
	dir := addSong(t, cfg, "Artist - Quiet", "song.mp3")
	audio := filepath.Join(dir, "song.mp3")
	quietMP3(t, audio, "1")
	original := readFile(t, audio)

	if err := Run(cfg, ops); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if st := ops.Stats(); st.Failed != 1 || st.Replaced != 0 || st.Copied != 0 {
		t.Errorf("stats = %+v, want 1 failure, nothing replaced or copied", st)
	}
	if !bytes.Equal(readFile(t, audio), original) {
		t.Error("the original was changed")
	}
	assertDirEntries(t, dir, "song.txt", "song.mp3") // the temp file is removed
	if _, err := os.Stat(cfg.OriginalsDir()); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("originals/ created for a failed encode: %v", err)
	}
	// The error message carries ffmpeg's output.
	if err := encode(audio, filepath.Join(t.TempDir(), "x.mp3"), audioInfo{Codec: "mp3", Channels: 2}, nil); err == nil ||
		!strings.Contains(err.Error(), "encoder exploded") {
		t.Errorf("encode err = %v, want ffmpeg's message", err)
	}
}

// A failing replace (the folder became read-only during the encode) leaves the original
// in place, is counted once, and the file is not journaled.
func TestReplaceFailureKeepsOriginal(t *testing.T) {
	requireFFmpeg(t)
	if os.Geteuid() == 0 {
		t.Skip("root ignores directory permissions")
	}
	fakeFFmpeg(t, "    '"+mustLookPath(t, "ffmpeg")+"' \"$@\" || exit 1; chmod 555 \"$(dirname \"$out\")\"; exit 0")
	cfg, ops := newLibrary(t, false)
	dir := addSong(t, cfg, "Artist - Quiet", "song.mp3")
	audio := filepath.Join(dir, "song.mp3")
	quietMP3(t, audio, "1")
	original := readFile(t, audio)
	t.Cleanup(func() { os.Chmod(dir, 0o755) })

	if err := Run(cfg, ops); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if st := ops.Stats(); st.Failed != 1 || st.Replaced != 0 {
		t.Errorf("stats = %+v, want 1 failure and no replacement", st)
	}
	if !bytes.Equal(readFile(t, audio), original) {
		t.Error("the original was changed")
	}
	j, err := journal.Open(cfg.JournalPath())
	if err != nil {
		t.Fatal(err)
	}
	if j.Has(journalKind, audio) {
		t.Error("an unprocessed file was journaled")
	}
}

func mustLookPath(t *testing.T, tool string) string {
	t.Helper()
	p, err := exec.LookPath(tool)
	if err != nil {
		t.Skipf("%s not available", tool)
	}
	return p
}

// In a dry run a failing (planned) backup means the replacement is not planned either.
func TestDryRunBackupFailureIsNotPlanned(t *testing.T) {
	requireFFmpeg(t)
	cfg, ops := newLibrary(t, true)
	dir := addSong(t, cfg, "Artist - Quiet", "song.mp3")
	quietMP3(t, filepath.Join(dir, "song.mp3"), "1")
	if err := os.WriteFile(cfg.OriginalsDir(), []byte("in the way"), 0o644); err != nil {
		t.Fatal(err)
	}
	before := snapshot(t, cfg.SongDir)

	if err := Run(cfg, ops); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if st := ops.Stats(); st.Failed != 1 || st.Replaced != 0 || st.Copied != 0 {
		t.Errorf("stats = %+v, want 1 failure, nothing planned", st)
	}
	if after := snapshot(t, cfg.SongDir); !maps.Equal(before, after) {
		t.Error("dry run changed the library")
	}
}

// Hidden leftovers and directories named like audio files are never processed; a folder
// without a song txt still has its audio normalized (it has no stems).
func TestProcessFolderSelection(t *testing.T) {
	requireFFmpeg(t)
	cfg, ops := newLibrary(t, false)
	dir := addSong(t, cfg, "Artist - Leftovers", "song.mp3")
	for _, name := range []string{"song.mp3", ".song.sanitizer-1.mp3", "._song.mp3"} {
		quietMP3(t, filepath.Join(dir, name), "1")
	}
	if err := os.Mkdir(filepath.Join(dir, "folder.mp3"), 0o755); err != nil {
		t.Fatal(err)
	}
	hidden := readFile(t, filepath.Join(dir, ".song.sanitizer-1.mp3"))
	apple := readFile(t, filepath.Join(dir, "._song.mp3"))
	noTxt := filepath.Join(cfg.SongDir, "No Txt")
	if err := os.Mkdir(noTxt, 0o755); err != nil {
		t.Fatal(err)
	}
	quietMP3(t, filepath.Join(noTxt, "audio.mp3"), "1")

	run(t, cfg, ops)

	if got := ops.Stats().Replaced; got != 2 {
		t.Errorf("replaced = %d, want 2 (song.mp3 and the txt-less audio.mp3)", got)
	}
	if !bytes.Equal(readFile(t, filepath.Join(dir, ".song.sanitizer-1.mp3")), hidden) ||
		!bytes.Equal(readFile(t, filepath.Join(dir, "._song.mp3")), apple) {
		t.Error("a hidden file was processed")
	}
	assertPeakNear(t, filepath.Join(noTxt, "audio.mp3"), targetPeakDB)
}

// An unreadable folder is counted under the same key as in discovery, so it is counted
// once.
func TestUnreadableFolderCountedOnce(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root ignores directory permissions")
	}
	cfg, ops := newLibrary(t, false)
	dir := filepath.Join(cfg.SongDir, "Locked")
	if err := os.Mkdir(dir, 0o000); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.Chmod(dir, 0o755) })
	p := &processor{cfg: cfg, ops: ops}
	p.processFolder(dir)
	if got := ops.Stats().Failed; got != 1 {
		t.Fatalf("Failed = %d, want 1", got)
	}
	ops.FailOnce("unreadable:" + dir) // what discovery does for the same folder
	p.processFolder(dir)
	if got := ops.Stats().Failed; got != 1 {
		t.Errorf("Failed = %d after discovery and a second attempt, want 1", got)
	}
}

// If the song txt cannot be read, the stems are unknown: the folder is left alone.
func TestUnreadableTxtSkipsFolder(t *testing.T) {
	requireFFmpeg(t)
	if os.Geteuid() == 0 {
		t.Skip("root ignores file permissions")
	}
	cfg, ops := newLibrary(t, false)
	dir := addSong(t, cfg, "Artist - Locked", "song.mp3", "#VOCALS:vocals.mp3")
	audio := filepath.Join(dir, "song.mp3")
	quietMP3(t, audio, "1")
	before := readFile(t, audio)
	txt := filepath.Join(dir, "song.txt")
	if err := os.Chmod(txt, 0o000); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.Chmod(txt, 0o644) })

	p := &processor{cfg: cfg, ops: ops}
	p.processFolder(dir)

	if st := ops.Stats(); st.Failed != 1 || st.Replaced != 0 {
		t.Errorf("stats = %+v, want 1 failure, nothing replaced", st)
	}
	if !bytes.Equal(readFile(t, audio), before) {
		t.Error("audio of a folder with an unreadable txt was changed")
	}
}

func TestStemFiles(t *testing.T) {
	cfg, _ := newLibrary(t, false)
	dir := addSong(t, cfg, "Artist - Stems", "song.mp3", "#VOCALS:v.mp3", "#INSTRUMENTAL:missing.mp3")
	for _, name := range []string{"song.mp3", "V.mp3"} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	stems, err := stemFiles(nil, dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(stems) != 1 || !stems["V.mp3"] {
		t.Errorf("stems = %v, want only V.mp3 (the on-disk name)", stems)
	}
	empty := t.TempDir()
	if stems, err := stemFiles(nil, empty); err != nil || stems != nil {
		t.Errorf("folder without a txt: stems %v, err %v; want none", stems, err)
	}
}

func TestMeasurePeakErrors(t *testing.T) {
	requireFFmpeg(t)
	dir := t.TempDir()
	garbage := filepath.Join(dir, "garbage.mp3")
	if err := os.WriteFile(garbage, []byte("this is not audio"), 0o644); err != nil {
		t.Fatal(err)
	}
	_, err := measurePeak(garbage, false)
	if err == nil || !strings.Contains(err.Error(), "ffmpeg astats") || !strings.Contains(err.Error(), "garbage.mp3") {
		t.Errorf("err = %v, want the ffmpeg failure with its message", err)
	}
	// A file without an audio stream: ffmpeg fails to map 0:a:0.
	if _, err := measurePeak(filepath.Join(dir, "missing.mp3"), true); err == nil {
		t.Error("no error for a missing file")
	}
}

func TestRunErrors(t *testing.T) {
	t.Run("no ffmpeg", func(t *testing.T) {
		cfg, ops := newLibrary(t, false)
		t.Setenv("PATH", t.TempDir())
		if err := Run(cfg, ops); err == nil || !strings.Contains(err.Error(), "ffmpeg was not found") {
			t.Errorf("err = %v, want ffmpeg not found", err)
		}
	})
	t.Run("no ffprobe", func(t *testing.T) {
		realFFmpeg := mustLookPath(t, "ffmpeg")
		bin := t.TempDir()
		if err := os.Symlink(realFFmpeg, filepath.Join(bin, "ffmpeg")); err != nil {
			t.Fatal(err)
		}
		cfg, ops := newLibrary(t, false)
		t.Setenv("PATH", bin)
		if err := Run(cfg, ops); err == nil || !strings.Contains(err.Error(), "ffprobe was not found") {
			t.Errorf("err = %v, want ffprobe not found", err)
		}
	})
	t.Run("journal is a directory", func(t *testing.T) {
		requireFFmpeg(t)
		cfg, ops := newLibrary(t, false)
		dir := addSong(t, cfg, "Artist - Quiet", "song.mp3")
		audio := filepath.Join(dir, "song.mp3")
		quietMP3(t, audio, "1")
		before := readFile(t, audio)
		if err := os.MkdirAll(cfg.JournalPath(), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := Run(cfg, ops); err == nil || !strings.Contains(err.Error(), "media journal") {
			t.Errorf("err = %v, want a journal error", err)
		}
		if !bytes.Equal(readFile(t, audio), before) || ops.Stats().Replaced != 0 {
			t.Error("files were processed without a journal")
		}
	})
	t.Run("missing song dir", func(t *testing.T) {
		requireFFmpeg(t)
		cfg, ops := newLibrary(t, false)
		cfg.SongDir = filepath.Join(cfg.SongDir, "gone")
		if err := Run(cfg, ops); err == nil || !strings.Contains(err.Error(), "reading song dir") {
			t.Errorf("err = %v, want a song dir error", err)
		}
	})
}

// squareWAV writes a float wav whose samples are all +-amplitude, so the measured peak is
// exactly 20*log10(float32(amplitude)).
func squareWAV(t *testing.T, path string, amplitude string) {
	t.Helper()
	wave := amplitude + "*(2*gte(sin(2*PI*441*t)\\,0)-1)"
	ffmpeg(t, "-f", "lavfi", "-i", "aevalsrc="+wave+"|"+wave+":s=44100:d=1", "-c:a", "pcm_f32le", path)
}

// A peak exactly peakToleranceDB away from the target is still within the tolerance.
func TestPeakExactlyAtToleranceIsLeftAlone(t *testing.T) {
	requireFFmpeg(t)
	cfg, ops := newLibrary(t, false)
	below := filepath.Join(addSong(t, cfg, "A - Below", "song.wav"), "song.wav")
	squareWAV(t, below, "0.6683439175686147") // -3.5 dB
	above := filepath.Join(addSong(t, cfg, "B - Above", "song.wav"), "song.wav")
	squareWAV(t, above, "0.7498942093324559") // -2.5 dB
	for path, want := range map[string]float64{below: -3.5, above: -2.5} {
		peak, err := measurePeak(path, false)
		if err != nil || math.Abs(targetPeakDB-peak) != peakToleranceDB || peak != want {
			t.Fatalf("fixture %s peak = %v (%v), want exactly %v", path, peak, err, want)
		}
	}
	beforeBelow, beforeAbove := readFile(t, below), readFile(t, above)

	run(t, cfg, ops)

	if st := ops.Stats(); st.Replaced != 0 || st.Copied != 0 {
		t.Errorf("replaced/copied = %d/%d, want 0/0", st.Replaced, st.Copied)
	}
	if !bytes.Equal(readFile(t, below), beforeBelow) || !bytes.Equal(readFile(t, above), beforeAbove) {
		t.Error("a file exactly at the tolerance was changed")
	}
}

// A symlinked audio file is left alone: replacing it would turn the link into a regular
// copy, and the file it points to (maybe shared by other songs) would stay as it is.
func TestSymlinkedAudioIsNotProcessed(t *testing.T) {
	requireFFmpeg(t)
	cfg, ops := newLibrary(t, false)
	pool := filepath.Join(t.TempDir(), "pool.mp3")
	quietMP3(t, pool, "1")
	orig := readFile(t, pool)
	dir := addSong(t, cfg, "Artist - Linked", "song.mp3")
	link := filepath.Join(dir, "song.mp3")
	if err := os.Symlink(pool, link); err != nil {
		t.Fatal(err)
	}

	run(t, cfg, ops)

	if st := ops.Stats(); st != (fsops.Stats{}) {
		t.Errorf("stats = %+v, want nothing done", st)
	}
	if target, err := os.Readlink(link); err != nil || target != pool {
		t.Errorf("song.mp3 is no longer the link to %s: %q, %v", pool, target, err)
	}
	if !bytes.Equal(readFile(t, pool), orig) {
		t.Error("the linked file changed")
	}
	if _, err := os.Stat(filepath.Join(cfg.OutDir, "originals")); !os.IsNotExist(err) {
		t.Errorf("a backup was made (err=%v)", err)
	}
}
