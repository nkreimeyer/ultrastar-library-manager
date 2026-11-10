package fixaudio

import (
	"crypto/sha256"
	"encoding/hex"
	"io/fs"
	"maps"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/nkreimeyer/ultrastar-songs-sanitizer/config"
	"github.com/nkreimeyer/ultrastar-songs-sanitizer/fsops"
)

// --- pure unit tests (no ffmpeg needed) ---

func TestParseMaxVolume(t *testing.T) {
	tests := []struct {
		name    string
		output  string
		want    float64
		wantErr bool
	}{
		{"negative", "[Parsed_volumedetect_0 @ 0x1] mean_volume: -20.1 dB\n[Parsed_volumedetect_0 @ 0x1] max_volume: -12.3 dB\n", -12.3, false},
		{"zero", "max_volume: 0.0 dB", 0, false},
		{"silence", "max_volume: -inf dB", math.Inf(-1), false},
		{"missing", "Invalid data found when processing input", 0, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := parseMaxVolume(tt.output)
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
		{"stream bitrate", `{"streams":[{"codec_name":"mp3","channels":2,"sample_fmt":"fltp","bit_rate":"320000"}],"format":{"bit_rate":"330000"}}`,
			audioInfo{Codec: "mp3", Channels: 2, SampleFmt: "fltp", Bitrate: 320000}, false},
		{"format bitrate fallback", `{"streams":[{"codec_name":"opus","channels":2,"sample_fmt":"fltp"}],"format":{"bit_rate":"131000"}}`,
			audioInfo{Codec: "opus", Channels: 2, SampleFmt: "fltp", Bitrate: 131000}, false},
		{"no bitrate", `{"streams":[{"codec_name":"flac","channels":1,"sample_fmt":"s16","bit_rate":"N/A"}],"format":{}}`,
			audioInfo{Codec: "flac", Channels: 1, SampleFmt: "s16"}, false},
		{"no audio stream", `{"streams":[],"format":{"bit_rate":"1000"}}`, audioInfo{}, true},
		{"zero channels", `{"streams":[{"codec_name":"mp3","channels":0}]}`, audioInfo{}, true},
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
		{"unknown codec, lossy extension", "/s/a/song.MP3", audioInfo{Codec: "mp2", Channels: 2, Bitrate: 256000},
			[][]string{{"-b:a", "256000"}}, []string{"-c:a", "-sample_fmt"}},
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

func requireFFmpeg(t *testing.T) {
	t.Helper()
	for _, tool := range []string{"ffmpeg", "ffprobe"} {
		if _, err := exec.LookPath(tool); err != nil {
			t.Skipf("%s not available", tool)
		}
	}
}

// newLibrary returns a config for an empty library and a separate output dir.
func newLibrary(t *testing.T, dryRun bool) (config.Config, *fsops.Ops) {
	t.Helper()
	cfg, err := config.New(t.TempDir(), t.TempDir(), dryRun)
	if err != nil {
		t.Fatal(err)
	}
	return cfg, fsops.New(dryRun)
}

// addSong creates a song folder with a minimal txt and returns its path.
func addSong(t *testing.T, cfg config.Config, rel, audio string) string {
	t.Helper()
	dir := filepath.Join(cfg.SongDir, rel)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	txt := "#TITLE:Test\n#ARTIST:Tester\n#MP3:" + audio + "\n#BPM:120\n#GAP:0\n: 0 4 0 la\nE\n"
	if err := os.WriteFile(filepath.Join(dir, "song.txt"), []byte(txt), 0o644); err != nil {
		t.Fatal(err)
	}
	return dir
}

func ffmpeg(t *testing.T, args ...string) {
	t.Helper()
	cmd := exec.Command("ffmpeg", append([]string{"-nostdin", "-v", "error", "-y"}, args...)...)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("ffmpeg %v: %v\n%s", args, err, out)
	}
}

// quietMP3 writes a 320 kbit/s stereo sine whose peak is about -38 dBFS.
func quietMP3(t *testing.T, path string, seconds string) {
	ffmpeg(t, "-f", "lavfi", "-i", "sine=f=440:d="+seconds, "-af", "volume=-20dB",
		"-ac", "2", "-c:a", "libmp3lame", "-b:a", "320k", path)
}

func probe(t *testing.T, path string) audioInfo {
	t.Helper()
	info, err := probeAudio(path)
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

func assertPeakNear(t *testing.T, path string, want float64) {
	t.Helper()
	got, err := getMaxVolume(path, false)
	if err != nil {
		t.Fatal(err)
	}
	if math.Abs(got-want) > 1.0 {
		t.Errorf("%s: max volume %.1f dB, want %.1f ± 1 dB", path, got, want)
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
	requireFFmpeg(t)
	cfg, ops := newLibrary(t, false)
	dir := addSong(t, cfg, "Artist - Six", "song.wav")
	audio := filepath.Join(dir, "song.wav")
	ffmpeg(t, "-f", "lavfi", "-i", "sine=f=440:d=0.5", "-ac", "6", "-c:a", "pcm_s16le", audio)

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

func TestQuietMP3GainKeepsBitrate(t *testing.T) {
	requireFFmpeg(t)
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
	requireFFmpeg(t)
	cfg, ops := newLibrary(t, false)
	dir := addSong(t, cfg, "Artist - Cover", "song.mp3")
	audio := filepath.Join(dir, "song.mp3")
	ffmpeg(t, "-f", "lavfi", "-i", "sine=f=440:d=1", "-f", "lavfi", "-i", "color=red:s=16x16:d=1",
		"-map", "0:a", "-map", "1:v", "-frames:v", "1", "-c:v", "mjpeg", "-disposition:v", "attached_pic",
		"-af", "volume=-20dB", "-c:a", "libmp3lame", "-b:a", "192k",
		"-metadata", "title=Cover Song", "-metadata", "artist=Tester", audio)

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
	requireFFmpeg(t)
	old := batchSize
	batchSize = 1 // one goroutine per song folder
	t.Cleanup(func() { batchSize = old })

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
	requireFFmpeg(t)
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
	requireFFmpeg(t)
	cfg, ops := newLibrary(t, true)
	quietMP3(t, filepath.Join(addSong(t, cfg, "Cat/A - Quiet", "song.mp3"), "song.mp3"), "1")
	six := filepath.Join(addSong(t, cfg, "Cat/B - Six", "song.wav"), "song.wav")
	ffmpeg(t, "-f", "lavfi", "-i", "sine=f=440:d=0.5", "-ac", "6", "-c:a", "pcm_s16le", six)

	before := snapshot(t, cfg.SongDir)
	run(t, cfg, ops)
	after := snapshot(t, cfg.SongDir)

	if !maps.Equal(before, after) {
		t.Errorf("dry-run modified the library:\nbefore %v\nafter  %v", before, after)
	}
	if got := ops.Stats().Replaced; got != 2 {
		t.Errorf("planned replacements = %d, want 2", got)
	}
	assertDirEntries(t, cfg.OutDir)
}

// snapshot maps every path below root to its content hash, mode and mtime.
func snapshot(t *testing.T, root string) map[string]string {
	t.Helper()
	m := map[string]string{}
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		info, err := d.Info()
		if err != nil {
			return err
		}
		entry := info.Mode().String() + " " + info.ModTime().String()
		if !d.IsDir() {
			data, err := os.ReadFile(path)
			if err != nil {
				return err
			}
			sum := sha256.Sum256(data)
			entry += " " + hex.EncodeToString(sum[:])
		}
		m[path] = entry
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return m
}

// Peak normalization of lossy files does not converge exactly; the marker tag must stop
// later runs from re-encoding (and losing quality) again and again.
func TestLossyNormalizationIsIdempotent(t *testing.T) {
	requireFFmpeg(t)
	for _, ext := range []string{".m4a", ".mp3", ".ogg"} {
		t.Run(ext, func(t *testing.T) {
			cfg, ops := newLibrary(t, false)
			dir := addSong(t, cfg, "Artist - Loud", "song"+ext)
			audio := filepath.Join(dir, "song"+ext)
			// A clipped (square-ish) signal: lossy encoders overshoot its peak, which drifts.
			ffmpeg(t, "-f", "lavfi", "-i", "anoisesrc=d=3:c=pink:a=0.9:seed=7", "-af", "volume=12dB", "-ac", "2", audio) // clipped, like loud masters

			run(t, cfg, ops)
			if got := ops.Stats().Replaced; got != 1 {
				t.Fatalf("first run replaced %d files, want 1", got)
			}
			if !probe(t, audio).Normalized {
				t.Errorf("normalized file must carry the %s tag", normalizedTag)
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
