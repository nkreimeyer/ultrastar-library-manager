package fixaudio

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/nkreimeyer/library-manager/internal/testutil"
	"github.com/nkreimeyer/library-manager/journal"
)

// newFileProcessor returns a processor over a library with one song "A - S" whose audio
// is song.mp3, written by write.
func newFileProcessor(t *testing.T, write func(path string)) (*processor, string) {
	t.Helper()
	cfg, ops := newLibrary(t, false)
	j, err := journal.Open(cfg.JournalPath())
	if err != nil {
		t.Fatal(err)
	}
	dir := addSong(t, cfg, "A - S", "song.mp3")
	write(filepath.Join(dir, "song.mp3"))
	return &processor{cfg: cfg, ops: ops, journal: j, ctx: context.Background()}, dir
}

// assertKept checks that processFile kept the original (byte for byte), counted one
// failure, kept no copy in originals/, journaled nothing and left no temp file.
func assertKept(t *testing.T, p *processor, dir string, before []byte) {
	t.Helper()
	path := filepath.Join(dir, "song.mp3")
	if got := readFile(t, path); string(got) != string(before) {
		t.Error("the original was replaced")
	}
	if st := p.ops.Stats(); st.Failed != 1 || st.Replaced != 0 {
		t.Errorf("Stats = %+v, want one failure and no replacement", st)
	}
	if _, err := os.Stat(p.cfg.OriginalsDir()); !os.IsNotExist(err) {
		t.Errorf("originals/ exists (%v): nothing was replaced, nothing to keep", err)
	}
	if p.journal.Has(journalKind, path) {
		t.Error("the original was journaled as normalized")
	}
	assertDirEntries(t, dir, "song.mp3", "song.txt")
}

// ffmpeg exits 0 after a read error of its input (a NAS dropping out) and writes only what
// it read: such an encode must not replace the original, which must not be marked
// normalized either.
func TestEncodeReadErrorKeepsOriginal(t *testing.T) {
	p, dir := newFileProcessor(t, func(path string) { testutil.WriteFile(t, path, "original audio") })
	version := "if [ \"$1\" = -version ]; then echo \"$(basename \"$0\") version 7.1\"; exit 0; fi\n"
	testutil.FakeTools(t, map[string]string{
		"ffprobe": "#!/bin/sh\n" + version + "echo '" + stereoMP3JSON + "'\n",
		// The peak measurement ("-f null") is fine; the encode writes a part of the file
		// and reports the read error, exiting 0 as ffmpeg does.
		"ffmpeg": "#!/bin/sh\n" + version +
			"case \" $* \" in *\" -f null \"*) echo 'Peak level dB: -20.0' >&2; exit 0;; esac\n" +
			"for a; do out=\"$a\"; done\nprintf 'partial' > \"$out\"\n" +
			"echo '[in#0/mp3 @ 0x1] Error during demuxing: Input/output error' >&2\nexit 0\n",
	})
	p.processFile(dir, "song.mp3")
	assertKept(t, p, dir, []byte("original audio"))
}

// An encode that comes out shorter than the original (a part of the input lost, whatever
// ffmpeg printed) does not replace it.
func TestShortEncodeKeepsOriginal(t *testing.T) {
	testutil.RequireFFmpeg(t)
	ffmpegPath, err := exec.LookPath("ffmpeg")
	if err != nil {
		t.Fatal(err)
	}
	realProbe, err := exec.LookPath("ffprobe")
	if err != nil {
		t.Fatal(err)
	}
	var before []byte
	p, dir := newFileProcessor(t, func(path string) {
		quietMP3(t, path, "3")
		before = readFile(t, path)
	})
	// The encode (no "-f null") gets "-t 1" before its output file: only a second is kept.
	testutil.FakeTools(t, map[string]string{
		"ffprobe": "#!/bin/sh\nexec '" + realProbe + "' \"$@\"\n",
		"ffmpeg": "#!/bin/sh\ncase \" $* \" in *\" -f null \"*|*\" -version \"*) exec '" + ffmpegPath + "' \"$@\";; esac\n" +
			"n=$#; i=0\nfor a; do\n  i=$((i+1))\n  [ $i -eq 1 ] && set --\n" +
			"  if [ $i -eq $n ]; then set -- \"$@\" -t 1 \"$a\"; else set -- \"$@\" \"$a\"; fi\ndone\n" +
			"exec '" + ffmpegPath + "' \"$@\"\n",
	})
	p.processFile(dir, "song.mp3")
	assertKept(t, p, dir, before)
}

// checkEncoded compares the result's duration with the decoded length of the original,
// which the peak measurement reports.
func TestCheckEncoded(t *testing.T) {
	testutil.RequireFFmpeg(t)
	dir := t.TempDir()
	gen := func(name, seconds string) string {
		path := filepath.Join(dir, name)
		testutil.FFmpeg(t, "-f", "lavfi", "-i", "sine=f=440:d="+seconds, "-c:a", "pcm_s16le", path)
		return path
	}
	three, one, same := gen("three.wav", "3"), gen("one.wav", "1"), gen("same.wav", "3")
	zero := filepath.Join(dir, "zero.wav")
	testutil.WriteFile(t, zero, "")
	garbage := filepath.Join(dir, "garbage.wav")
	testutil.WriteFile(t, garbage, strings.Repeat("garbage", 100))
	_, decoded, _, err := measurePeakResult(three, false)
	if err != nil || decoded < 2.9 || decoded > 3.1 {
		t.Fatalf("measurePeakResult: decoded %v, %v; want 3 s", decoded, err)
	}
	ctx := context.Background()
	for _, tt := range []struct {
		name, tmp string
		decoded   float64
		wantErr   string
	}{
		{"same length", same, decoded, ""},
		{"shorter", one, decoded, "the result is 1.000 s long instead of"},
		{"empty", zero, decoded, "ffmpeg wrote an empty file"},
		{"unreadable result", garbage, decoded, "ffprobe cannot read the result"},
		{"decoded length unknown", one, 0, ""},
	} {
		err := checkEncoded(ctx, tt.tmp, tt.decoded)
		if (tt.wantErr == "") != (err == nil) || (err != nil && !strings.Contains(err.Error(), tt.wantErr)) {
			t.Errorf("%s: checkEncoded = %v, want %q", tt.name, err, tt.wantErr)
		}
	}
	stopped, cancel := context.WithCancel(ctx)
	cancel()
	if err := checkEncoded(stopped, same, decoded); err != context.Canceled {
		t.Errorf("stopped: checkEncoded = %v, want context.Canceled", err)
	}
}

// A result whose duration ffprobe only estimates (a VBR mp3 without a Xing header) is not
// compared: the estimate can be far off.
func TestCheckEncodedEstimatedResult(t *testing.T) {
	tmp := filepath.Join(t.TempDir(), "tmp.mp3")
	testutil.WriteFile(t, tmp, "encoded audio")
	const answer = `{"streams":[{"codec_type":"audio","codec_name":"mp3","channels":2}],"format":{"duration":"90.0"}}`
	for _, tt := range []struct {
		name, stderr string
		wantErr      bool
	}{
		{"estimated", "echo 'Estimating duration from bitrate, this may be inaccurate' >&2\n", false},
		{"exact", "", true},
	} {
		testutil.FakeTools(t, map[string]string{"ffprobe": "#!/bin/sh\n" + tt.stderr + "echo '" + answer + "'\n"})
		if err := checkEncoded(context.Background(), tmp, 200); (err != nil) != tt.wantErr {
			t.Errorf("%s: checkEncoded = %v, want an error: %v", tt.name, err, tt.wantErr)
		}
	}
}

// A cut-off download keeps the header of the whole file (an mp3's Info frame, an m4a's
// moov) but decodes to the part that is there, and so does its encode: it is normalized
// like any other file, never refused as too short on every run.
func TestTruncatedDownloadIsNormalized(t *testing.T) {
	testutil.RequireFFmpeg(t)
	for _, tt := range []struct {
		name  string
		codec []string
		keep  int64
	}{
		{"mp3", []string{"-c:a", "libmp3lame", "-b:a", "128k"}, 200000},
		{"m4a", []string{"-c:a", "aac", "-b:a", "128k", "-movflags", "+faststart"}, 200000},
	} {
		t.Run(tt.name, func(t *testing.T) {
			name := "song." + tt.name
			cfg, ops := newLibrary(t, false)
			j, err := journal.Open(cfg.JournalPath())
			if err != nil {
				t.Fatal(err)
			}
			dir := addSong(t, cfg, "A - S", name)
			path := filepath.Join(dir, name)
			testutil.FFmpeg(t, append(append([]string{"-f", "lavfi", "-i", "sine=f=440:d=30", "-af", "volume=-20dB", "-ac", "2"}, tt.codec...), path)...)
			if err := os.Truncate(path, tt.keep); err != nil {
				t.Fatal(err)
			}
			p := &processor{cfg: cfg, ops: ops, journal: j, ctx: context.Background()}
			p.processFile(dir, name)
			if st := ops.Stats(); st.Failed != 0 || st.Replaced != 1 {
				t.Errorf("Stats = %+v, want the file normalized", st)
			}
		})
	}
}
