package fixaudio

import (
	"bytes"
	"context"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/nkreimeyer/library-manager/config"
	"github.com/nkreimeyer/library-manager/fsops"
	"github.com/nkreimeyer/library-manager/internal/testutil"
	"github.com/nkreimeyer/library-manager/journal"
	"github.com/nkreimeyer/library-manager/mediatools"
)

// --- pure unit tests (no ffmpeg needed) ---

func TestParsePeakLevel(t *testing.T) {
	tests := []struct {
		name    string
		output  string
		want    float64
		wantErr bool
	}{
		{"overall", "[Parsed_astats_1 @ 0x1] Overall\n[Parsed_astats_1 @ 0x1] Peak level dB: -12.345678\n", -12.345678, false},
		{"above full scale", "Peak level dB: 2.739996", 2.739996, false},
		{"per channel then overall", "Channel: 1\nPeak level dB: -1.0\nChannel: 2\nPeak level dB: -2.0\nOverall\nPeak level dB: -0.5\n", -0.5, false},
		{"silence", "Peak level dB: -inf", math.Inf(-1), false},
		{"missing", "Invalid data found when processing input", 0, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := parsePeakLevel(tt.output)
			if (err != nil) != tt.wantErr {
				t.Fatalf("err = %v, wantErr %v", err, tt.wantErr)
			}
			if !tt.wantErr && got != tt.want {
				t.Errorf("got %v, want %v", got, tt.want)
			}
		})
	}
}

func TestParseProbe(t *testing.T) {
	tests := []struct {
		name    string
		json    string
		want    audioInfo
		wantErr bool
	}{
		{"stream bitrate", `{"streams":[{"codec_type":"audio","codec_name":"mp3","channels":2,"sample_fmt":"fltp","bit_rate":"320000"}],"format":{"bit_rate":"330000"}}`,
			audioInfo{Codec: "mp3", Channels: 2, SampleFmt: "fltp", Bitrate: 320000}, false},
		{"format bitrate fallback", `{"streams":[{"codec_type":"audio","codec_name":"opus","channels":2,"sample_fmt":"fltp"}],"format":{"bit_rate":"131000"}}`,
			audioInfo{Codec: "opus", Channels: 2, SampleFmt: "fltp", Bitrate: 131000}, false},
		{"no bitrate", `{"streams":[{"codec_type":"audio","codec_name":"flac","channels":1,"sample_fmt":"s16","bit_rate":"N/A"}],"format":{}}`,
			audioInfo{Codec: "flac", Channels: 1, SampleFmt: "s16"}, false},
		{"no audio stream", `{"streams":[],"format":{"bit_rate":"1000"}}`, audioInfo{}, true},
		{"zero channels", `{"streams":[{"codec_type":"audio","codec_name":"mp3","channels":0}]}`, audioInfo{}, true},
		{"garbage", `not json`, audioInfo{}, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := parseProbe([]byte(tt.json))
			if (err != nil) != tt.wantErr {
				t.Fatalf("err = %v, wantErr %v", err, tt.wantErr)
			}
			if got != tt.want {
				t.Errorf("got %+v, want %+v", got, tt.want)
			}
		})
	}
}

func TestEncodeArgs(t *testing.T) {
	gain := []string{"volume=2.00dB"}
	tests := []struct {
		name       string
		in         string
		info       audioInfo
		wantSeq    [][]string // argument pairs that must be present
		notPresent []string   // flags that must be absent
	}{
		{"mp3 keeps bitrate and cover", "/s/a/song.mp3", audioInfo{Codec: "mp3", Channels: 2, Bitrate: 320000},
			[][]string{{"-y"}, {"-map", "0:v?"}, {"-c:v", "copy"}, {"-map_metadata", "0"}, {"-c:a", "libmp3lame"}, {"-b:a", "320000"}}, []string{"-sample_fmt"}},
		{"bitrate capped", "/s/a/song.m4a", audioInfo{Codec: "aac", Channels: 6, Bitrate: 640000},
			[][]string{{"-map", "0:v?"}, {"-c:a", "aac"}, {"-b:a", "320000"}}, nil},
		{"flac lossless keeps bit depth", "/s/a/song.flac", audioInfo{Codec: "flac", Channels: 2, SampleFmt: "s16", Bitrate: 900000},
			[][]string{{"-c:a", "flac"}, {"-sample_fmt", "s16"}, {"-map", "0:v?"}}, []string{"-b:a"}},
		{"wav pcm", "/s/a/song.wav", audioInfo{Codec: "pcm_s24le", Channels: 2, SampleFmt: "s32", Bitrate: 2116800},
			[][]string{{"-c:a", "pcm_s24le"}}, []string{"-b:a", "0:v?"}},
		{"ogg audio only", "/s/a/song.ogg", audioInfo{Codec: "vorbis", Channels: 2, Bitrate: 192000},
			[][]string{{"-c:a", "libvorbis"}, {"-b:a", "192000"}}, []string{"0:v?"}},
		{"mp2 in an .MP3 file", "/s/a/song.MP3", audioInfo{Codec: "mp2", Channels: 2, Bitrate: 256000},
			[][]string{{"-c:a", "mp2"}, {"-b:a", "256000"}}, []string{"-sample_fmt"}},
		{"ac3 in matroska keeps codec and bitrate", "/s/a/song.mka", audioInfo{Codec: "ac3", Channels: 6, SampleFmt: "fltp", Bitrate: 448000},
			[][]string{{"-c:a", "ac3"}, {"-b:a", "320000"}, {"-metadata", "SANITIZER_NORMALIZED=-3.0dB"}}, []string{"-sample_fmt", "0:v?"}},
		{"eac3", "/s/a/song.mka", audioInfo{Codec: "eac3", Channels: 2, Bitrate: 192000},
			[][]string{{"-c:a", "eac3"}, {"-b:a", "192000"}}, nil},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			args := encodeArgs(tt.in, "/s/a/.tmp", tt.info, gain)
			if args[len(args)-1] != "/s/a/.tmp" {
				t.Errorf("output must be the last argument: %v", args)
			}
			if !containsSeq(args, []string{"-af", "volume=2.00dB"}) {
				t.Errorf("missing filter: %v", args)
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
}

func containsSeq(args, seq []string) bool {
	for i := 0; i+len(seq) <= len(args); i++ {
		if slices.Equal(args[i:i+len(seq)], seq) {
			return true
		}
	}
	return false
}

// --- fixture tests (need ffmpeg/ffprobe) ---

// newLibrary returns a config for an empty library and a separate output dir.
func newLibrary(t *testing.T, dryRun bool) (config.Config, *fsops.Ops) {
	t.Helper()
	cfg, err := config.New(t.TempDir(), t.TempDir(), dryRun)
	if err != nil {
		t.Fatal(err)
	}
	return cfg, fsops.New(dryRun)
}

// addSong creates a song folder with a minimal txt and returns its path. extraTags are
// additional header lines such as "#VOCALS:vocals.mp3".
func addSong(t *testing.T, cfg config.Config, rel, audio string, extraTags ...string) string {
	t.Helper()
	dir := filepath.Join(cfg.SongDir, rel)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	header := "#TITLE:Test\n#ARTIST:Tester\n#MP3:" + audio + "\n"
	for _, tag := range extraTags {
		header += tag + "\n"
	}
	txt := header + "#BPM:120\n#GAP:0\n: 0 4 0 la\nE\n"
	if err := os.WriteFile(filepath.Join(dir, "song.txt"), []byte(txt), 0o644); err != nil {
		t.Fatal(err)
	}
	return dir
}

// quietMP3 writes a 320 kbit/s stereo sine whose peak is about -38 dBFS.
func quietMP3(t *testing.T, path string, seconds string) {
	testutil.FFmpeg(t, "-f", "lavfi", "-i", "sine=f=440:d="+seconds, "-af", "volume=-20dB",
		"-ac", "2", "-c:a", "libmp3lame", "-b:a", "320k", path)
}

func probe(t *testing.T, path string) audioInfo {
	t.Helper()
	info, err := probeAudio(context.Background(), path)
	if err != nil {
		t.Fatalf("probing %s: %v", path, err)
	}
	return info
}

func ffprobeValue(t *testing.T, path string, args ...string) string {
	t.Helper()
	out, err := exec.Command("ffprobe", append(append([]string{"-v", "error"}, args...),
		"-of", "default=noprint_wrappers=1:nokey=1", path)...).Output()
	if err != nil {
		t.Fatalf("ffprobe %s: %v", path, err)
	}
	return strings.TrimSpace(string(out))
}

// audioWithCover writes one second of a 440 Hz sine with a JPEG cover (attached_pic) to
// path, encoded with args (codec, filters, metadata). The cover is a one-frame image input:
// "-frames:v 1" on a lavfi video source ends the whole output after its first frame, which
// left about one audio frame with ffmpeg 9.1 and none with 7.1.
func audioWithCover(t *testing.T, path string, args ...string) {
	t.Helper()
	cover := filepath.Join(t.TempDir(), "cover.jpg")
	testutil.FFmpeg(t, "-f", "lavfi", "-i", "color=red:s=16x16", "-frames:v", "1", cover)
	full := append([]string{"-f", "lavfi", "-i", "sine=f=440:d=1", "-i", cover,
		"-map", "0:a", "-map", "1:v", "-c:v", "mjpeg", "-disposition:v", "attached_pic"}, args...)
	testutil.FFmpeg(t, append(full, path)...)
	if d, ok := mediatools.ParseSeconds(ffprobeValue(t, path, "-show_entries", "format=duration")); !ok || d < 0.9 {
		t.Fatalf("fixture %s holds %.3f s of audio, want 1 s", path, d)
	}
}

func assertPeakNear(t *testing.T, path string, want float64) {
	t.Helper()
	got, err := measurePeak(path, false)
	if err != nil {
		t.Fatal(err)
	}
	if math.Abs(got-want) > 1.0 {
		t.Errorf("%s: peak %.1f dB, want %.1f ± 1 dB", path, got, want)
	}
}

func assertDirEntries(t *testing.T, dir string, want ...string) {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, e := range entries {
		got = append(got, e.Name())
	}
	slices.Sort(want)
	if !slices.Equal(got, want) {
		t.Errorf("%s contains %v, want %v", dir, got, want)
	}
}

func run(t *testing.T, cfg config.Config, ops *fsops.Ops) {
	t.Helper()
	if err := Run(cfg, ops); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if f := ops.Stats().Failed; f != 0 {
		t.Errorf("%d operations failed", f)
	}
}

func TestDownmixSixChannels(t *testing.T) {
	testutil.RequireFFmpeg(t)
	cfg, ops := newLibrary(t, false)
	dir := addSong(t, cfg, "Artist - Six", "song.wav")
	audio := filepath.Join(dir, "song.wav")
	testutil.FFmpeg(t, "-f", "lavfi", "-i", "sine=f=440:d=0.5", "-ac", "6", "-c:a", "pcm_s16le", audio)

	run(t, cfg, ops)

	info := probe(t, audio)
	if info.Channels != 2 {
		t.Errorf("channels = %d, want 2", info.Channels)
	}
	if info.Codec != "pcm_s16le" {
		t.Errorf("codec = %s, want pcm_s16le (bit depth kept)", info.Codec)
	}
	assertPeakNear(t, audio, targetPeakDB)
	assertDirEntries(t, dir, "song.txt", "song.wav")

	// The measured downmix peak must match the encoded one, so a second run is a no-op.
	ops2 := fsops.New(false)
	run(t, cfg, ops2)
	if got := ops2.Stats().Replaced; got != 0 {
		t.Errorf("second run replaced %d files, want 0", got)
	}
}

// Matroska audio (.mka): an AC3 5.1 track stays AC3 (downmixed, bitrate kept), a codec
// without a known encoder (DTS) is left alone instead of becoming default-quality Vorbis.
func TestMatroskaAudio(t *testing.T) {
	testutil.RequireFFmpeg(t)
	cfg, ops := newLibrary(t, false)
	ac3Dir := addSong(t, cfg, "Artist - AC3", "a.mka")
	ac3 := filepath.Join(ac3Dir, "a.mka")
	testutil.FFmpeg(t, "-f", "lavfi", "-i", "sine=f=440:d=1", "-af", "volume=-20dB", "-ac", "6", "-c:a", "ac3", "-b:a", "448k", ac3)
	dtsDir := addSong(t, cfg, "Artist - DTS", "d.mka")
	dts := filepath.Join(dtsDir, "d.mka")
	testutil.GenerateMedia(t, "-f", "lavfi", "-i", "sine=f=440:d=1",
		"-af", "volume=-20dB", "-ac", "6", "-c:a", "dca", "-strict", "-2", dts)
	before, err := os.ReadFile(dts)
	if err != nil {
		t.Fatal(err)
	}

	run(t, cfg, ops)

	if info := probe(t, ac3); info.Codec != "ac3" || info.Channels != 2 || info.Bitrate < 300000 || !info.Normalized {
		t.Errorf("ac3 result = %+v, want stereo ac3 at 320 kbit/s, normalized", info)
	}
	if after, err := os.ReadFile(dts); err != nil || !bytes.Equal(after, before) {
		t.Errorf("the DTS file was changed (err %v)", err)
	}
	if got := ops.Stats().Replaced; got != 1 {
		t.Errorf("replaced %d files, want 1", got)
	}
}

func TestQuietMP3GainKeepsBitrate(t *testing.T) {
	testutil.RequireFFmpeg(t)
	cwd := t.TempDir()
	t.Chdir(cwd) // the old code wrote temp files to the CWD
	cfg, ops := newLibrary(t, false)
	dir := addSong(t, cfg, "Artist - Quiet", "song.mp3")
	audio := filepath.Join(dir, "song.mp3")
	quietMP3(t, audio, "2")
	if err := os.Chmod(audio, 0o640); err != nil {
		t.Fatal(err)
	}

	run(t, cfg, ops)

	if got := ops.Stats().Replaced; got != 1 {
		t.Errorf("replaced = %d, want 1", got)
	}
	assertPeakNear(t, audio, targetPeakDB)
	if br := probe(t, audio).Bitrate; br < 310000 || br > 330000 {
		t.Errorf("bitrate = %d, want ~320000", br)
	}
	if st, err := os.Stat(audio); err != nil || st.Mode().Perm() != 0o640 {
		t.Errorf("mode not preserved: %v %v", st.Mode(), err)
	}
	assertDirEntries(t, dir, "song.txt", "song.mp3")
	assertDirEntries(t, cwd)

	// A second run finds nothing left to do.
	ops2 := fsops.New(false)
	run(t, cfg, ops2)
	if got := ops2.Stats().Replaced; got != 0 {
		t.Errorf("second run replaced %d files, want 0", got)
	}
}

func TestMP3CoverAndTagsKept(t *testing.T) {
	testutil.RequireFFmpeg(t)
	cfg, ops := newLibrary(t, false)
	dir := addSong(t, cfg, "Artist - Cover", "song.mp3")
	audio := filepath.Join(dir, "song.mp3")
	audioWithCover(t, audio,
		"-af", "volume=-20dB", "-c:a", "libmp3lame", "-b:a", "192k",
		"-metadata", "title=Cover Song", "-metadata", "artist=Tester")

	run(t, cfg, ops)

	assertPeakNear(t, audio, targetPeakDB)
	if got := ffprobeValue(t, audio, "-select_streams", "v", "-show_entries", "stream=codec_name:stream_disposition=attached_pic"); got != "mjpeg\n1" {
		t.Errorf("cover stream = %q, want mjpeg attached_pic", got)
	}
	if got := ffprobeValue(t, audio, "-show_entries", "format_tags=title"); got != "Cover Song" {
		t.Errorf("title tag = %q", got)
	}
	if br := probe(t, audio).Bitrate; br != 192000 {
		t.Errorf("bitrate = %d, want 192000", br)
	}
}

func TestSameFileNamesProcessedConcurrently(t *testing.T) {
	testutil.RequireFFmpeg(t)
	old := workers
	workers = 2 // both folders at the same time, even on a single-CPU machine
	t.Cleanup(func() { workers = old })

	cfg, ops := newLibrary(t, false)
	durations := map[string]string{"A - One": "1", "B - Two": "3"}
	for rel, d := range durations {
		quietMP3(t, filepath.Join(addSong(t, cfg, rel, "song.mp3"), "song.mp3"), d)
	}

	run(t, cfg, ops)

	if got := ops.Stats().Replaced; got != 2 {
		t.Errorf("replaced = %d, want 2", got)
	}
	for rel, d := range durations {
		dir := filepath.Join(cfg.SongDir, rel)
		audio := filepath.Join(dir, "song.mp3")
		assertPeakNear(t, audio, targetPeakDB)
		// Each file must still have its own content (duration), not the other song's.
		got := ffprobeValue(t, audio, "-show_entries", "format=duration")
		if !strings.HasPrefix(got, d+".") {
			t.Errorf("%s: duration %s, want ~%s s", rel, got, d)
		}
		assertDirEntries(t, dir, "song.txt", "song.mp3")
	}
}

func TestUnreadableAudioIsSkipped(t *testing.T) {
	testutil.RequireFFmpeg(t)
	cfg, ops := newLibrary(t, false)
	dir := addSong(t, cfg, "Artist - Broken", "song.mp3")
	audio := filepath.Join(dir, "song.mp3")
	garbage := []byte("this is not audio")
	if err := os.WriteFile(audio, garbage, 0o644); err != nil {
		t.Fatal(err)
	}

	if err := Run(cfg, ops); err != nil {
		t.Fatalf("Run must not fail for one broken file: %v", err)
	}
	if st := ops.Stats(); st.Failed != 1 || st.Replaced != 0 {
		t.Errorf("stats = %+v, want 1 failure and no replacement", st)
	}
	if data, _ := os.ReadFile(audio); string(data) != string(garbage) {
		t.Error("broken file was modified")
	}
	assertDirEntries(t, dir, "song.txt", "song.mp3")
}

func TestDryRunLeavesTreeIdentical(t *testing.T) {
	testutil.RequireFFmpeg(t)
	cfg, ops := newLibrary(t, true)
	quietMP3(t, filepath.Join(addSong(t, cfg, "Cat/A - Quiet", "song.mp3"), "song.mp3"), "1")
	six := filepath.Join(addSong(t, cfg, "Cat/B - Six", "song.wav"), "song.wav")
	testutil.FFmpeg(t, "-f", "lavfi", "-i", "sine=f=440:d=0.5", "-ac", "6", "-c:a", "pcm_s16le", six)

	before := testutil.Snapshot(t, cfg.SongDir)
	run(t, cfg, ops)
	after := testutil.Snapshot(t, cfg.SongDir)

	if after != before {
		t.Errorf("dry-run modified the library:\nbefore %v\nafter  %v", before, after)
	}
	if st := ops.Stats(); st.Replaced != 2 || st.Copied != 2 {
		t.Errorf("planned replacements/backups = %d/%d, want 2/2", st.Replaced, st.Copied)
	}
	assertDirEntries(t, cfg.OutDir)
}

// Peak normalization of lossy files does not converge exactly; the marker tag must stop
// later runs from re-encoding (and losing quality) again and again.
func TestLossyNormalizationIsIdempotent(t *testing.T) {
	testutil.RequireFFmpeg(t)
	for _, ext := range []string{".m4a", ".mp3", ".ogg"} {
		t.Run(ext, func(t *testing.T) {
			cfg, ops := newLibrary(t, false)
			dir := addSong(t, cfg, "Artist - Loud", "song"+ext)
			audio := filepath.Join(dir, "song"+ext)
			// A clipped (square-ish) signal: lossy encoders overshoot its peak, which drifts.
			testutil.FFmpeg(t, "-f", "lavfi", "-i", "anoisesrc=d=3:c=pink:a=0.9:seed=7", "-af", "volume=12dB", "-ac", "2", audio) // clipped, like loud masters

			run(t, cfg, ops)
			if got := ops.Stats().Replaced; got != 1 {
				t.Fatalf("first run replaced %d files, want 1", got)
			}
			// MP4 cannot carry the tag without losing its iTunes atoms (see
			// noMetadataExts); the media journal stops the re-encodes instead.
			if tagged := probe(t, audio).Normalized; tagged == noMetadataExts[ext] {
				t.Errorf("tagged = %v, want %v (%s tag)", tagged, !noMetadataExts[ext], normalizedTag)
			}
			for i := 2; i <= 3; i++ {
				again := fsops.New(false)
				run(t, cfg, again)
				if got := again.Stats().Replaced; got != 0 {
					t.Errorf("run %d replaced %d files, want 0", i, got)
				}
			}
		})
	}
}

// readFile returns the content of path.
func readFile(t *testing.T, path string) []byte {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return data
}

func TestOriginalIsKeptBeforeReplacing(t *testing.T) {
	testutil.RequireFFmpeg(t)
	cfg, ops := newLibrary(t, false)
	dir := addSong(t, cfg, "Cat/Artist - Quiet", "song.mp3")
	audio := filepath.Join(dir, "song.mp3")
	quietMP3(t, audio, "1")
	original := readFile(t, audio)

	run(t, cfg, ops)

	if st := ops.Stats(); st.Replaced != 1 || st.Copied != 1 {
		t.Errorf("replaced/copied = %d/%d, want 1/1", st.Replaced, st.Copied)
	}
	backup := filepath.Join(cfg.OriginalsDir(), "Cat", "Artist - Quiet", "song.mp3")
	if !slices.Equal(readFile(t, backup), original) {
		t.Errorf("%s is not a copy of the original", backup)
	}
	if slices.Equal(readFile(t, audio), original) {
		t.Error("the library file was not processed")
	}
	assertDirEntries(t, dir, "song.txt", "song.mp3")

	// Nothing is processed again, so no second backup is made.
	ops2 := fsops.New(false)
	run(t, cfg, ops2)
	if st := ops2.Stats(); st.Replaced != 0 || st.Copied != 0 {
		t.Errorf("second run replaced/copied = %d/%d, want 0/0", st.Replaced, st.Copied)
	}
	assertDirEntries(t, filepath.Dir(backup), "song.mp3")
}

func TestFailedBackupLeavesOriginalUntouched(t *testing.T) {
	testutil.RequireFFmpeg(t)
	cfg, ops := newLibrary(t, false)
	dir := addSong(t, cfg, "Artist - Quiet", "song.mp3")
	audio := filepath.Join(dir, "song.mp3")
	quietMP3(t, audio, "1")
	original := readFile(t, audio)
	// A file in place of the originals folder makes every backup fail.
	if err := os.WriteFile(cfg.OriginalsDir(), []byte("in the way"), 0o644); err != nil {
		t.Fatal(err)
	}

	if err := Run(cfg, ops); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if st := ops.Stats(); st.Failed != 1 || st.Replaced != 0 || st.Copied != 0 {
		t.Errorf("stats = %+v, want 1 failure, nothing replaced or copied", st)
	}
	if !slices.Equal(readFile(t, audio), original) {
		t.Error("the original was replaced although its backup failed")
	}
	assertDirEntries(t, dir, "song.txt", "song.mp3") // temp file removed
}

// loudFloatSource is a stereo sine whose sample peak is +3 dBFS. Float sources (and lossy
// files decoded to float) can exceed full scale.
const loudFloatSource = "aevalsrc=1.4125*sin(2*PI*440*t)|1.4125*sin(2*PI*440*t):s=44100:d=1"

// volumedetect measures on s16 and clamps such peaks to 0 dB, so the old code applied
// -3 dB, left the file at about 0 dBFS and tagged it as normalized.
func TestPeakAboveFullScaleIsNormalized(t *testing.T) {
	testutil.RequireFFmpeg(t)
	for _, tc := range []struct{ ext, codec string }{
		{".wav", "pcm_f32le"},
		{".mp3", "libmp3lame"},
		{".m4a", "aac"},
	} {
		t.Run(tc.ext, func(t *testing.T) {
			cfg, ops := newLibrary(t, false)
			dir := addSong(t, cfg, "Artist - Loud", "song"+tc.ext)
			audio := filepath.Join(dir, "song"+tc.ext)
			testutil.FFmpeg(t, "-f", "lavfi", "-i", loudFloatSource, "-c:a", tc.codec, audio)
			if peak, err := measurePeak(audio, false); err != nil || peak < 2 {
				t.Fatalf("fixture peak = %.2f dB (%v), want about +3 dB", peak, err)
			}

			run(t, cfg, ops)

			// Within 1 dB: lossy encoders overshoot the peak a little.
			assertPeakNear(t, audio, targetPeakDB)
			if peak, err := measurePeak(audio, false); err == nil {
				t.Logf("peak after normalization: %.2f dB", peak)
			}
		})
	}
}

// Raw ADTS AAC cannot carry the marker tag; the media journal must stop the peak, which
// drifts with every lossy encode, from being "fixed" again on every run. Whether the
// encoder misses the target depends on the ffmpeg version (9.1 hits -3.00 dB), so the
// two halves are tested apart: a run journals what it normalized, and a journaled file
// that misses the target is left alone.
func TestAACNormalizationIsIdempotentViaJournal(t *testing.T) {
	testutil.RequireFFmpeg(t)
	loudAAC := func(path string) {
		// Clipped pink noise: the peak is near 0 dBFS, far from the target.
		testutil.FFmpeg(t, "-f", "lavfi", "-i", "anoisesrc=d=3:c=pink:a=0.9:seed=7", "-af", "volume=12dB", "-ac", "2", "-c:a", "aac", "-b:a", "64k", path)
		if peak, err := measurePeak(path, false); err != nil || math.Abs(peak-targetPeakDB) <= peakToleranceDB {
			t.Fatalf("fixture peak = %.2f dB (%v); it must miss the target", peak, err)
		}
	}

	t.Run("a run journals the file", func(t *testing.T) {
		cfg, ops := newLibrary(t, false)
		audio := filepath.Join(addSong(t, cfg, "Artist - Loud", "song.aac"), "song.aac")
		loudAAC(audio)
		run(t, cfg, ops)
		if got := ops.Stats().Replaced; got != 1 {
			t.Fatalf("first run replaced %d files, want 1", got)
		}
		j, err := journal.Open(cfg.JournalPath())
		if err != nil || !j.Has(journalKind, audio) {
			t.Fatalf("the normalized file is not in the media journal (%v)", err)
		}
		for i := 2; i <= 3; i++ {
			again := fsops.New(false)
			run(t, cfg, again)
			if got := again.Stats().Replaced; got != 0 {
				t.Errorf("run %d replaced %d files, want 0", i, got)
			}
		}
	})

	t.Run("a journaled file is not normalized again", func(t *testing.T) {
		cfg, ops := newLibrary(t, false)
		audio := filepath.Join(addSong(t, cfg, "Artist - Loud", "song.aac"), "song.aac")
		loudAAC(audio)
		j, err := journal.Open(cfg.JournalPath())
		if err != nil {
			t.Fatal(err)
		}
		if err := j.Add(journalKind, audio); err != nil {
			t.Fatal(err)
		}
		if err := j.Save(ops); err != nil {
			t.Fatal(err)
		}
		before := readFile(t, audio)
		run(t, cfg, ops)
		if got := ops.Stats().Replaced; got != 0 {
			t.Errorf("replaced %d files, want 0: the journal must stop a second normalization", got)
		}
		if !slices.Equal(readFile(t, audio), before) {
			t.Error("the journaled file was changed")
		}
	})
}

func TestStemsAreNotNormalized(t *testing.T) {
	testutil.RequireFFmpeg(t)
	cfg, ops := newLibrary(t, false)
	// The references differ in case from the files on disk.
	dir := addSong(t, cfg, "Artist - Stems", "song.mp3", "#VOCALS:Vocals.mp3", "#instrumental:inst.MP3")
	for _, name := range []string{"song.mp3", "vocals.mp3", "inst.mp3"} {
		quietMP3(t, filepath.Join(dir, name), "1")
	}
	vocals := readFile(t, filepath.Join(dir, "vocals.mp3"))
	inst := readFile(t, filepath.Join(dir, "inst.mp3"))

	run(t, cfg, ops)

	if got := ops.Stats().Replaced; got != 1 {
		t.Errorf("replaced = %d, want 1 (only the main audio)", got)
	}
	assertPeakNear(t, filepath.Join(dir, "song.mp3"), targetPeakDB)
	if !slices.Equal(readFile(t, filepath.Join(dir, "vocals.mp3")), vocals) {
		t.Error("#VOCALS stem was modified")
	}
	if !slices.Equal(readFile(t, filepath.Join(dir, "inst.mp3")), inst) {
		t.Error("#INSTRUMENTAL stem was modified")
	}
}

// In dry-run mode the cleanup module only plans its moves; fixaudio must not plan to
// process files that would no longer be in the library.
func TestDryRunSkipsFilesPlannedToBeMoved(t *testing.T) {
	testutil.RequireFFmpeg(t)
	cfg, ops := newLibrary(t, true)
	dir := addSong(t, cfg, "Artist - Extra", "song.mp3")
	quietMP3(t, filepath.Join(dir, "song.mp3"), "1")
	extra := filepath.Join(dir, "unreferenced.mp3")
	quietMP3(t, extra, "1")
	if _, err := ops.MoveInto(extra, cfg.ExtraFilesDir(), cfg.RelPath(extra)); err != nil {
		t.Fatal(err)
	}

	run(t, cfg, ops)

	if st := ops.Stats(); st.Replaced != 1 || st.Copied != 1 {
		t.Errorf("planned replacements/backups = %d/%d, want 1/1 (only song.mp3)", st.Replaced, st.Copied)
	}
}
