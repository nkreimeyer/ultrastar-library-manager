package fixvideos

import (
	"crypto/sha256"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/nkreimeyer/ultrastar-songs-sanitizer/config"
	"github.com/nkreimeyer/ultrastar-songs-sanitizer/fsops"
)

func TestBuildFFmpegArgs(t *testing.T) {
	head := []string{"-hide_banner", "-nostdin", "-loglevel", "error", "-i", "/lib/song/v.avi", "-map", "0:v:0"}
	x264 := []string{"-c:v", "libx264", "-crf", "23", "-maxrate", "2.5M", "-bufsize", "5.0M", "-preset", "fast"}
	tail := []string{"-y", "/lib/song/.v.tmp.mp4"}

	tests := []struct {
		name string
		plan encodePlan
		want []string
	}{
		{
			name: "re-encode keeps audio",
			plan: encodePlan{reEncode: true, crf: 23},
			want: slices.Concat(head, []string{"-map", "0:a?", "-c:a", "copy"}, x264, tail),
		},
		{
			name: "strip audio only copies video",
			plan: encodePlan{removeAudio: true},
			want: slices.Concat(head, []string{"-an", "-c:v", "copy"}, tail),
		},
		{
			name: "resize and strip audio",
			plan: encodePlan{reEncode: true, resize: true, removeAudio: true, crf: 23},
			want: slices.Concat(head, []string{"-an"}, x264, []string{"-vf", "scale=-2:1080"}, tail),
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := buildFFmpegArgs("/lib/song/v.avi", "/lib/song/.v.tmp.mp4", tt.plan)
			if !slices.Equal(got, tt.want) {
				t.Errorf("args mismatch\n got: %q\nwant: %q", got, tt.want)
			}
		})
	}
}

func TestEncodePlanOutputExt(t *testing.T) {
	if got := (encodePlan{reEncode: true}).outputExt("a.AVI"); got != ".mp4" {
		t.Errorf("re-encode ext = %q, want .mp4", got)
	}
	if got := (encodePlan{removeAudio: true}).outputExt("a.MKV"); got != ".MKV" {
		t.Errorf("copy ext = %q, want .MKV", got)
	}
}

func TestWriteCorruptedReportMergesSorted(t *testing.T) {
	cfg, _, outDir := newLibrary(t, false)
	report := filepath.Join(outDir, corruptedReportName)
	if err := os.WriteFile(report, []byte("/lib/b.avi\n\n/lib/a.avi\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	p := &processor{cfg: cfg, ops: fsops.New(false), corrupted: map[string]struct{}{}}
	p.flagCorrupted("/lib/c.avi")
	p.flagCorrupted("/lib/a.avi")
	p.writeCorruptedReport()

	got := readFile(t, report)
	if want := "/lib/a.avi\n/lib/b.avi\n/lib/c.avi\n"; got != want {
		t.Errorf("report = %q, want %q", got, want)
	}
}

// --- tests that need ffmpeg -------------------------------------------------------------

func TestReEncodeAviToMp4(t *testing.T) {
	requireFFmpeg(t)
	cfg, songDir, outDir := newLibrary(t, false)
	song := filepath.Join(songDir, "Artist", "Artist - Song")
	writeFile(t, filepath.Join(song, "song.txt"), "#TITLE:Song\n#ARTIST:Artist\n#MP3:video.avi\n#VIDEO:video.avi\n#VIDEOGAP:0\n: 0 4 0 La\nE\n")
	// mpeg4 at maximum quality: the codec needs re-encoding and the estimate is far below the input size.
	makeMedia(t, filepath.Join(song, "video.avi"), "-c:v", "mpeg4", "-q:v", "1", "-c:a", "ac3")
	if err := os.Chmod(filepath.Join(song, "video.avi"), 0o640); err != nil {
		t.Fatal(err)
	}

	ops := fsops.New(false)
	if err := Run(cfg, ops); err != nil {
		t.Fatal(err)
	}

	if st := ops.Stats(); st.Failed != 0 {
		t.Errorf("Failed = %d, want 0", st.Failed)
	}
	if _, err := os.Stat(filepath.Join(song, "video.avi")); !os.IsNotExist(err) {
		t.Errorf("original video.avi still in song folder (err=%v)", err)
	}
	if _, err := os.Stat(filepath.Join(outDir, "extra-files", "Artist", "Artist - Song", "video.avi")); err != nil {
		t.Errorf("original not moved to extra-files: %v", err)
	}
	mp4 := filepath.Join(song, "video.mp4")
	if got := probeStreams(t, mp4); !slices.Equal(got, []string{"video:h264", "audio:ac3"}) {
		t.Errorf("streams of video.mp4 = %v, want h264 video + ac3 audio", got)
	}
	txt := readFile(t, filepath.Join(song, "song.txt"))
	if want := "#TITLE:Song\n#ARTIST:Artist\n#MP3:video.mp4\n#VIDEO:video.mp4\n#VIDEOGAP:0\n: 0 4 0 La\nE\n"; txt != want {
		t.Errorf("txt = %q, want %q", txt, want)
	}
	if st, err := os.Stat(filepath.Join(song, "video.mp4")); err != nil || st.Mode().Perm() != 0o640 {
		t.Errorf("converted video must keep the original permissions 0640: %v, %v", st, err)
	}
	assertNoTempFiles(t, songDir)
}

// If the txt cannot be pointed at the new file, the conversion is undone and the original
// stays referenced in place.
func TestReEncodeUpdatesReferenceSpelledDifferently(t *testing.T) {
	requireFFmpeg(t)
	cfg, songDir, outDir := newLibrary(t, false)
	song := filepath.Join(songDir, "A - S")
	// Referenced in a different case; the tag-aware replacement still updates it.
	writeFile(t, filepath.Join(song, "song.txt"), "#TITLE:S\n#ARTIST:A\n#VIDEO:VIDEO.AVI\n")
	makeMedia(t, filepath.Join(song, "video.avi"), "-c:v", "mpeg4", "-q:v", "1", "-c:a", "ac3")

	ops := fsops.New(false)
	if err := Run(cfg, ops); err != nil {
		t.Fatal(err)
	}
	if st := ops.Stats(); st.Failed != 0 {
		t.Errorf("Failed = %d, want 0", st.Failed)
	}
	if got := readFile(t, filepath.Join(song, "song.txt")); got != "#TITLE:S\n#ARTIST:A\n#VIDEO:video.mp4\n" {
		t.Errorf("txt = %q", got)
	}
	if _, err := os.Stat(filepath.Join(outDir, "extra-files", "A - S", "video.avi")); err != nil {
		t.Errorf("original must be kept in extra-files: %v", err)
	}
	assertNoTempFiles(t, songDir)
}

func TestUnreferencedVideoIsNotConverted(t *testing.T) {
	requireFFmpeg(t)
	cfg, songDir, outDir := newLibrary(t, false)
	song := filepath.Join(songDir, "A - S")
	txt := "#TITLE:S\n#ARTIST:A\n#VIDEO:other.mp4\n"
	writeFile(t, filepath.Join(song, "song.txt"), txt)
	makeMedia(t, filepath.Join(song, "video.avi"), "-c:v", "mpeg4", "-q:v", "1", "-c:a", "ac3")

	ops := fsops.New(false)
	if err := Run(cfg, ops); err != nil {
		t.Fatal(err)
	}
	if st := ops.Stats(); st.Failed != 0 || st.Replaced != 0 || st.Moved != 0 {
		t.Errorf("Stats = %+v, want nothing done", st)
	}
	if _, err := os.Stat(filepath.Join(song, "video.avi")); err != nil {
		t.Errorf("original must stay in place: %v", err)
	}
	if _, err := os.Stat(filepath.Join(song, "video.mp4")); !os.IsNotExist(err) {
		t.Errorf("no video.mp4 must be created (err=%v)", err)
	}
	if got := readFile(t, filepath.Join(song, "song.txt")); got != txt {
		t.Errorf("txt changed to %q", got)
	}
	if _, err := os.Stat(filepath.Join(outDir, "extra-files")); !os.IsNotExist(err) {
		t.Errorf("nothing must be moved to extra-files (err=%v)", err)
	}
	assertNoTempFiles(t, songDir)
}

func TestStripAudio(t *testing.T) {
	requireFFmpeg(t)
	tests := []struct {
		name      string
		txt       string
		wantAudio bool
	}{
		{"separate audio referenced", "#TITLE:S\n#ARTIST:A\n#MP3:song.mp3\n#VIDEO:video.mp4\n", false},
		{"separate audio not referenced", "#TITLE:S\n#ARTIST:A\n#MP3:video.mp4\n#VIDEO:video.mp4\n", true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg, songDir, _ := newLibrary(t, false)
			song := filepath.Join(songDir, "A - S")
			writeFile(t, filepath.Join(song, "song.txt"), tt.txt)
			writeFile(t, filepath.Join(song, "song.mp3"), "not checked")
			// Low-bitrate H.264 does not need re-encoding, so only the audio decision matters.
			makeMedia(t, filepath.Join(song, "video.mp4"), "-c:v", "libx264", "-crf", "35", "-c:a", "aac")

			ops := fsops.New(false)
			if err := Run(cfg, ops); err != nil {
				t.Fatal(err)
			}
			if st := ops.Stats(); st.Failed != 0 {
				t.Errorf("Failed = %d, want 0", st.Failed)
			}
			streams := probeStreams(t, filepath.Join(song, "video.mp4"))
			if hasAudio := slices.Contains(streams, "audio:aac"); hasAudio != tt.wantAudio {
				t.Errorf("streams = %v, want audio = %v", streams, tt.wantAudio)
			}
			assertNoTempFiles(t, songDir)
		})
	}
}

func TestCorruptedVideoIsReported(t *testing.T) {
	requireFFmpeg(t)
	cfg, songDir, outDir := newLibrary(t, false)
	song := filepath.Join(songDir, "A - S")
	writeFile(t, filepath.Join(song, "song.txt"), "#TITLE:S\n#ARTIST:A\n#VIDEO:video.avi\n")
	bad := filepath.Join(song, "video.avi")
	writeFile(t, bad, strings.Repeat("garbage", 200))

	ops := fsops.New(false)
	if err := Run(cfg, ops); err != nil {
		t.Fatal(err)
	}
	if st := ops.Stats(); st.Failed != 0 {
		t.Errorf("Failed = %d, want 0 (corrupted input is reported, not a failure)", st.Failed)
	}
	if got, want := readFile(t, filepath.Join(outDir, corruptedReportName)), bad+"\n"; got != want {
		t.Errorf("report = %q, want %q", got, want)
	}
	if _, err := os.Stat(bad); err != nil {
		t.Errorf("corrupted original must stay in place: %v", err)
	}
	assertNoTempFiles(t, songDir)
}

func TestDryRunLeavesTreeIdentical(t *testing.T) {
	requireFFmpeg(t)
	cfg, songDir, outDir := newLibrary(t, true)
	song := filepath.Join(songDir, "A - S")
	writeFile(t, filepath.Join(song, "song.txt"), "#TITLE:S\n#ARTIST:A\n#MP3:song.mp3\n#VIDEO:video.avi\n")
	writeFile(t, filepath.Join(song, "song.mp3"), "audio")
	makeMedia(t, filepath.Join(song, "video.avi"), "-c:v", "mpeg4", "-q:v", "1", "-c:a", "ac3")

	before := snapshot(t, songDir)
	ops := fsops.New(true)
	if err := Run(cfg, ops); err != nil {
		t.Fatal(err)
	}
	if after := snapshot(t, songDir); !slices.Equal(before, after) {
		t.Errorf("dry run changed the library\nbefore: %v\n after: %v", before, after)
	}
	if entries, _ := os.ReadDir(outDir); len(entries) != 0 {
		t.Errorf("dry run created output: %v", entries)
	}
}

// --- helpers ------------------------------------------------------------------------------

func newLibrary(t *testing.T, dryRun bool) (cfg config.Config, songDir, outDir string) {
	t.Helper()
	songDir, outDir = t.TempDir(), t.TempDir()
	cfg, err := config.New(songDir, outDir, dryRun)
	if err != nil {
		t.Fatal(err)
	}
	return cfg, cfg.SongDir, cfg.OutDir
}

func requireFFmpeg(t *testing.T) {
	t.Helper()
	for _, tool := range []string{"ffmpeg", "ffprobe"} {
		if _, err := exec.LookPath(tool); err != nil {
			t.Skipf("%s not available: %v", tool, err)
		}
	}
}

// makeMedia creates a 1 s 320x240 test video with a sine tone, encoded with codecArgs.
func makeMedia(t *testing.T, path string, codecArgs ...string) {
	t.Helper()
	args := slices.Concat([]string{
		"-hide_banner", "-nostdin", "-loglevel", "error",
		"-f", "lavfi", "-i", "testsrc=size=320x240:rate=25:duration=1",
		"-f", "lavfi", "-i", "sine=frequency=440:duration=1",
	}, codecArgs, []string{"-y", path})
	if out, err := exec.Command("ffmpeg", args...).CombinedOutput(); err != nil {
		t.Skipf("cannot generate test media (%v): %s", err, out)
	}
}

// probeStreams returns "type:codec" for every stream of file.
func probeStreams(t *testing.T, file string) []string {
	t.Helper()
	out, err := exec.Command("ffprobe", "-v", "error", "-show_entries", "stream=codec_type,codec_name", "-of", "csv=p=0", file).Output()
	if err != nil {
		t.Fatalf("ffprobe %s: %v", file, err)
	}
	var streams []string
	for _, line := range strings.Fields(string(out)) {
		f := strings.Split(strings.TrimSuffix(line, ","), ",")
		if len(f) >= 2 {
			streams = append(streams, f[1]+":"+f[0])
		}
	}
	return streams
}

func assertNoTempFiles(t *testing.T, root string) {
	t.Helper()
	filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err == nil && strings.HasPrefix(d.Name(), ".") && strings.Contains(d.Name(), ".sanitizer-") {
			t.Errorf("temp file left behind: %s", path)
		}
		return nil
	})
}

// snapshot lists every entry below root with its mode, size, mtime and content hash.
func snapshot(t *testing.T, root string) []string {
	t.Helper()
	var entries []string
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		info, err := d.Info()
		if err != nil {
			return err
		}
		entry := fmt.Sprintf("%s %v %d %d", path, info.Mode(), info.Size(), info.ModTime().UnixNano())
		if info.Mode().IsRegular() {
			entry += fmt.Sprintf(" %x", sha256.Sum256([]byte(readFile(t, path))))
		}
		entries = append(entries, entry)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return entries
}

func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func readFile(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}
