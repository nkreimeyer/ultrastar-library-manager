package fixvideos

import (
	"errors"
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

// newProcessor returns a processor for cfg with an empty journal.
func newProcessor(t *testing.T, dryRun bool) (*processor, string, string) {
	t.Helper()
	cfg, songDir, outDir := newLibrary(t, dryRun)
	j, err := journal.Open(cfg.JournalPath())
	if err != nil {
		t.Fatal(err)
	}
	return &processor{cfg: cfg, ops: fsops.New(dryRun), journal: j, corrupted: map[string]struct{}{}}, songDir, outDir
}

// skipIfRoot skips tests that rely on permissions root ignores.
func skipIfRoot(t *testing.T) {
	t.Helper()
	if os.Geteuid() == 0 {
		t.Skip("permissions are not enforced for root")
	}
}

func TestReEncodeWorthIt(t *testing.T) {
	probeErr := errors.New("unknown")
	tests := []struct {
		name        string
		codec       string
		bitrate     int
		bitrateErr  error
		estimate    int64
		estimateErr error
		size        int64
		want        bool
	}{
		{"old codec, estimate smaller", "mpeg4", 0, probeErr, 100, nil, 1000, true},
		{"old codec, estimate larger", "mpeg4", 0, nil, 1000, nil, 999, false},
		{"old codec, estimate equal", "mpeg4", 0, nil, 1000, nil, 1000, false},
		{"old codec, no estimate", "mpeg4", 0, nil, 0, probeErr, 1, true},
		{"h264, high bitrate", "h264", BitrateThreshold + 1, nil, 100, nil, 1000, true},
		{"h264, bitrate at the threshold", "h264", BitrateThreshold, nil, 100, nil, 1000, false},
		{"h264, high bitrate, estimate larger", "h264", BitrateThreshold * 2, nil, 1000, nil, 100, false},
		{"h264, high bitrate unknown", "h264", BitrateThreshold * 2, probeErr, 100, nil, 1000, false},
		{"h264, high bitrate, no estimate", "h264", BitrateThreshold * 2, nil, 0, probeErr, 1000, false},
		{"h264, low bitrate", "h264", 1000, nil, 100, nil, 1000, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := reEncodeWorthIt(tt.codec, tt.bitrate, tt.bitrateErr, tt.estimate, tt.estimateErr, tt.size); got != tt.want {
				t.Errorf("reEncodeWorthIt = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestEstimateSize(t *testing.T) {
	tests := []struct {
		w, h          int
		fps, duration float64
		want          int64
	}{
		{1920, 1080, 30, 8, 1800000}, // 1.8 Mbit/s for 8 s
		{960, 1080, 30, 8, 900000},   // half the width
		{1920, 540, 30, 8, 900000},   // half the height
		{1920, 1080, 15, 8, 900000},  // half the framerate
		{1920, 1080, 30, 4, 900000},  // half the duration
		{320, 240, 25, 1, 6944},      // 1.8M * 1/6 * 2/9 * 5/6 / 8
		{1920, 1080, 30, 0, 0},
	}
	for _, tt := range tests {
		if got := estimateSize(tt.w, tt.h, tt.fps, tt.duration); got != tt.want {
			t.Errorf("estimateSize(%d, %d, %v, %v) = %d, want %d", tt.w, tt.h, tt.fps, tt.duration, got, tt.want)
		}
	}
}

func TestParseFramerate(t *testing.T) {
	for _, tt := range []struct {
		in   string
		want float64
	}{{"25/1\n", 25}, {"30000/1001", 30000.0 / 1001}, {" 50/2 ", 25}} {
		if got, err := parseFramerate(tt.in); err != nil || math.Abs(got-tt.want) > 1e-9 {
			t.Errorf("parseFramerate(%q) = %v, %v; want %v", tt.in, got, err, tt.want)
		}
	}
	for _, in := range []string{"0/0", "25/0", "0/1", "-25/1", "25/-1", "25", "", "a/1", "25/b", "1/2/3"} {
		if got, err := parseFramerate(in); err == nil {
			t.Errorf("parseFramerate(%q) = %v, want an error", in, got)
		}
	}
}

func TestDurationsMatch(t *testing.T) {
	tests := []struct {
		a, b float64
		want bool
	}{
		{1, 1, true},
		{1.5, 1, true}, // exactly the tolerance
		{1, 1.5, true},
		{1.75, 1, false},
		{1, 1.75, false},
		{2, 1, false},
		{-1, -1, true},
	}
	for _, tt := range tests {
		if got := durationsMatch(tt.a, tt.b); got != tt.want {
			t.Errorf("durationsMatch(%v, %v) = %v, want %v", tt.a, tt.b, got, tt.want)
		}
	}
}

func TestDescribe(t *testing.T) {
	tests := []struct {
		plan encodePlan
		want string
	}{
		{encodePlan{reEncode: true, resize: true, removeAudio: true}, "re-encode + resize + strip audio"},
		{encodePlan{removeAudio: true}, "strip audio"},
		{encodePlan{reEncode: true}, "re-encode"},
		{encodePlan{}, ""},
	}
	for _, tt := range tests {
		if got := tt.plan.describe(); got != tt.want {
			t.Errorf("describe(%+v) = %q, want %q", tt.plan, got, tt.want)
		}
	}
}

func TestParseMarkerProbeEdgeCases(t *testing.T) {
	if _, err := parseMarkerProbe([]byte("not json")); err == nil {
		t.Error("parseMarkerProbe(not json): want an error")
	}
	tests := []struct {
		json string
		want markerProbe
	}{
		{`{"format":{"duration":"0"}}`, markerProbe{}},
		{`{"format":{"duration":"-1"}}`, markerProbe{}},
		{`{"format":{"duration":"0.25","tags":{"SANITIZER_VIDEO":""}}}`, markerProbe{tagged: true, duration: 0.25}},
		{`{"streams":[{"tags":{"SANITIZER_VIDEO":"x"}},{"tags":{"title":"y"}}],"format":{}}`, markerProbe{tagged: true}},
		{`{"streams":[{"tags":{"SANITIZER":"processed"}}],"format":{"tags":{"VIDEO":"processed"}}}`, markerProbe{}},
	}
	for _, tt := range tests {
		if got, err := parseMarkerProbe([]byte(tt.json)); err != nil || got != tt.want {
			t.Errorf("parseMarkerProbe(%s) = %+v, %v; want %+v", tt.json, got, err, tt.want)
		}
	}
}

// An existing report that cannot be read is not overwritten, and the failure is counted.
func TestWriteCorruptedReportUnreadable(t *testing.T) {
	p, _, outDir := newProcessor(t, false)
	report := filepath.Join(outDir, corruptedReportName)
	if err := os.Mkdir(report, 0o755); err != nil {
		t.Fatal(err)
	}
	p.flagCorrupted("/lib/a.avi")
	p.writeCorruptedReport()
	if st := p.ops.Stats(); st.Failed != 1 || st.Written != 0 {
		t.Errorf("Stats = %+v, want 1 failure and nothing written", st)
	}
	if st, err := os.Stat(report); err != nil || !st.IsDir() {
		t.Errorf("the unreadable report must stay untouched: %v, %v", st, err)
	}
}

// Without flagged videos, no report is written (not even an empty one).
func TestWriteCorruptedReportNothingFlagged(t *testing.T) {
	p, _, outDir := newProcessor(t, false)
	p.writeCorruptedReport()
	if _, err := os.Stat(filepath.Join(outDir, corruptedReportName)); !os.IsNotExist(err) {
		t.Errorf("report written although nothing was flagged (err=%v)", err)
	}
}

// collectVideos returns only the visible regular video files; an unreadable folder is
// counted once, with the key the song folder discovery uses.
func TestCollectVideos(t *testing.T) {
	p, songDir, _ := newProcessor(t, false)
	song := filepath.Join(songDir, "A - S")
	writeFile(t, filepath.Join(song, "video.avi"), "v")
	writeFile(t, filepath.Join(song, "._video.avi"), "AppleDouble")
	writeFile(t, filepath.Join(song, ".video.sanitizer-1.mp4"), "leftover")
	writeFile(t, filepath.Join(song, "song.mp3"), "audio")
	writeFile(t, filepath.Join(song, "song.txt"), "#TITLE:S\n")
	writeFile(t, filepath.Join(song, "real.mkv"), "v")
	if err := os.Mkdir(filepath.Join(song, "clip.mp4"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("missing.mp4", filepath.Join(song, "dangling.mp4")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("real.mkv", filepath.Join(song, "link.mkv")); err != nil {
		t.Fatal(err)
	}
	got := p.collectVideos([]string{song})
	want := []string{filepath.Join(song, "link.mkv"), filepath.Join(song, "real.mkv"), filepath.Join(song, "video.avi")}
	if !slices.Equal(got, want) {
		t.Errorf("collectVideos = %v, want %v", got, want)
	}
	if st := p.ops.Stats(); st.Failed != 0 {
		t.Errorf("Stats = %+v, want no failure", st)
	}

	skipIfRoot(t)
	locked := filepath.Join(songDir, "B - T")
	writeFile(t, filepath.Join(locked, "video.avi"), "v")
	if err := os.Chmod(locked, 0); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.Chmod(locked, 0o755) })
	if got := p.collectVideos([]string{locked, song}); !slices.Equal(got, want) {
		t.Errorf("collectVideos with an unreadable folder = %v, want %v", got, want)
	}
	if st := p.ops.Stats(); st.Failed != 1 {
		t.Errorf("Stats = %+v, want 1 failure", st)
	}
	// Discovery reports the same folder with the same key: still one failure.
	p.ops.FailOnce("unreadable:" + locked)
	if st := p.ops.Stats(); st.Failed != 1 {
		t.Errorf("Stats = %+v after the discovery's FailOnce, want still 1 failure", st)
	}
}

// replaceOriginal with an empty ffmpeg output or a vanished original counts a failure,
// removes the temp file and keeps the original.
func TestReplaceOriginalUnusableOutput(t *testing.T) {
	p, songDir, _ := newProcessor(t, false)
	song := filepath.Join(songDir, "A - S")
	video := filepath.Join(song, "video.mp4")
	writeFile(t, video, "original video")
	tmp, err := fsops.TempFileNear(video, ".mp4") // empty, as if ffmpeg wrote nothing
	if err != nil {
		t.Fatal(err)
	}
	p.replaceOriginal(song, video, tmp, encodePlan{removeAudio: true})
	if st := p.ops.Stats(); st.Failed != 1 || st.Replaced != 0 {
		t.Errorf("Stats = %+v, want 1 failure and no replacement", st)
	}
	if got := readFile(t, video); got != "original video" {
		t.Errorf("original changed to %q", got)
	}
	assertNoTempFiles(t, songDir)

	tmp, err = fsops.TempFileNear(video, ".mp4")
	if err != nil {
		t.Fatal(err)
	}
	writeFile(t, tmp, "processed")
	p.replaceOriginal(song, filepath.Join(song, "gone.mp4"), tmp, encodePlan{removeAudio: true})
	if st := p.ops.Stats(); st.Failed != 2 || st.Replaced != 0 {
		t.Errorf("Stats = %+v, want 2 failures and no replacement", st)
	}
	assertNoTempFiles(t, songDir)
}

// A conversion whose txt cannot be pointed at the new file is undone: the new file is
// removed, the original stays referenced and the failure is counted.
func TestReplaceOriginalTxtUpdateFails(t *testing.T) {
	tests := []struct {
		name, txt string
	}{
		{"txt not UTF-8", "#TITLE:M\xe4dchen\n#ARTIST:A\n#VIDEO:video.avi\n"},
		{"no tag references the original", "#TITLE:S\n#ARTIST:A\n#VIDEO:other.avi\n"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			p, songDir, outDir := newProcessor(t, false)
			song := filepath.Join(songDir, "A - S")
			writeFile(t, filepath.Join(song, "song.txt"), tt.txt)
			video := filepath.Join(song, "video.avi")
			writeFile(t, video, "original video, larger than the result")
			tmp, err := fsops.TempFileNear(video, ".mp4")
			if err != nil {
				t.Fatal(err)
			}
			writeFile(t, tmp, "converted")

			p.replaceOriginal(song, video, tmp, encodePlan{reEncode: true})
			if st := p.ops.Stats(); st.Failed != 1 || st.Written != 0 || st.Removed != 1 {
				t.Errorf("Stats = %+v, want 1 failure, nothing written, the new file removed", st)
			}
			if got := dirNames(t, song); !slices.Equal(got, []string{"song.txt", "video.avi"}) {
				t.Errorf("song folder = %v, want song.txt and video.avi", got)
			}
			if got := readFile(t, video); got != "original video, larger than the result" {
				t.Errorf("original changed to %q", got)
			}
			if got := readFile(t, filepath.Join(song, "song.txt")); got != tt.txt {
				t.Errorf("txt changed to %q", got)
			}
			if _, err := os.Stat(filepath.Join(outDir, "media-journal.txt")); !os.IsNotExist(err) {
				t.Errorf("nothing must be journaled (err=%v)", err)
			}
		})
	}
}

// A conversion whose target appeared after processVideoFile checked (another worker) is
// discarded without touching the target.
func TestReplaceOriginalTargetAppeared(t *testing.T) {
	p, songDir, _ := newProcessor(t, false)
	song := filepath.Join(songDir, "A - S")
	writeFile(t, filepath.Join(song, "song.txt"), "#TITLE:S\n#ARTIST:A\n#VIDEO:video.avi\n")
	video := filepath.Join(song, "video.avi")
	writeFile(t, video, "original video")
	writeFile(t, filepath.Join(song, "video.mp4"), "other")
	tmp, err := fsops.TempFileNear(video, ".mp4")
	if err != nil {
		t.Fatal(err)
	}
	writeFile(t, tmp, "new")
	p.replaceOriginal(song, video, tmp, encodePlan{reEncode: true})
	if st := p.ops.Stats(); st != (fsops.Stats{}) {
		t.Errorf("Stats = %+v, want nothing done", st)
	}
	if got := readFile(t, filepath.Join(song, "video.mp4")); got != "other" {
		t.Errorf("video.mp4 changed to %q", got)
	}
	assertNoTempFiles(t, songDir)
}

// A video written in a container without the tag is journaled; if that fails, the failure
// is counted but the video stays replaced.
func TestRememberProcessedJournalFailure(t *testing.T) {
	skipIfRoot(t)
	p, songDir, outDir := newProcessor(t, false)
	song := filepath.Join(songDir, "A - S")
	video := filepath.Join(song, "video.avi")
	writeFile(t, video, "original video")
	tmp, err := fsops.TempFileNear(video, ".avi")
	if err != nil {
		t.Fatal(err)
	}
	writeFile(t, tmp, "stripped")
	if err := os.Chmod(outDir, 0o500); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.Chmod(outDir, 0o755) })

	p.replaceOriginal(song, video, tmp, encodePlan{removeAudio: true})
	if st := p.ops.Stats(); st.Replaced != 1 || st.Failed < 1 {
		t.Errorf("Stats = %+v, want 1 replacement and the journal failure counted", st)
	}
	if got := readFile(t, video); got != "stripped" {
		t.Errorf("video = %q, want the processed version", got)
	}
}

// A tag-capable video gets no journal entry.
func TestRememberProcessedTaggedContainer(t *testing.T) {
	p, songDir, outDir := newProcessor(t, false)
	video := filepath.Join(songDir, "A - S", "video.mkv")
	writeFile(t, video, "v")
	p.rememberProcessed(video)
	if err := p.journal.Save(p.ops); err != nil {
		t.Fatal(err)
	}
	if p.journal.Has(journalProcessed, video) {
		t.Error("an MKV carries the tag and must not be journaled")
	}
	if _, err := os.Stat(filepath.Join(outDir, "media-journal.txt")); !os.IsNotExist(err) {
		t.Errorf("journal written (err=%v)", err)
	}
}

// Run refuses to work without ffmpeg/ffprobe, without a readable song dir and with an
// unreadable journal (which it would otherwise overwrite).
func TestRunPreconditions(t *testing.T) {
	t.Run("no ffmpeg", func(t *testing.T) {
		cfg, _, _ := newLibrary(t, false)
		t.Setenv("PATH", t.TempDir())
		err := Run(cfg, fsops.New(false))
		if err == nil || !strings.Contains(err.Error(), "ffmpeg") {
			t.Errorf("Run = %v, want an error naming ffmpeg", err)
		}
	})
	t.Run("no ffprobe", func(t *testing.T) {
		requireFFmpeg(t)
		cfg, _, _ := newLibrary(t, false)
		bin := t.TempDir()
		ffmpeg, _ := lookPath(t, "ffmpeg")
		if err := os.Symlink(ffmpeg, filepath.Join(bin, "ffmpeg")); err != nil {
			t.Fatal(err)
		}
		t.Setenv("PATH", bin)
		err := Run(cfg, fsops.New(false))
		if err == nil || !strings.Contains(err.Error(), "ffprobe") {
			t.Errorf("Run = %v, want an error naming ffprobe", err)
		}
	})
	t.Run("song dir gone", func(t *testing.T) {
		requireFFmpeg(t)
		cfg, songDir, _ := newLibrary(t, false)
		if err := os.Remove(songDir); err != nil {
			t.Fatal(err)
		}
		if err := Run(cfg, fsops.New(false)); err == nil {
			t.Error("Run = nil, want an error for a missing song dir")
		}
	})
	t.Run("journal unreadable", func(t *testing.T) {
		requireFFmpeg(t)
		cfg, songDir, _ := newLibrary(t, false)
		song := filepath.Join(songDir, "A - S")
		writeFile(t, filepath.Join(song, "song.txt"), "#TITLE:S\n#ARTIST:A\n#VIDEO:video.avi\n")
		writeFile(t, filepath.Join(song, "video.avi"), "v")
		if err := os.Mkdir(cfg.JournalPath(), 0o755); err != nil {
			t.Fatal(err)
		}
		before := snapshot(t, songDir)
		ops := fsops.New(false)
		err := Run(cfg, ops)
		if err == nil || !strings.Contains(err.Error(), "journal") {
			t.Errorf("Run = %v, want a journal error", err)
		}
		if after := snapshot(t, songDir); !slices.Equal(before, after) {
			t.Errorf("library changed\nbefore: %v\n after: %v", before, after)
		}
	})
}

func lookPath(t *testing.T, tool string) (string, error) {
	t.Helper()
	p, err := exec.LookPath(tool)
	if err != nil {
		t.Skipf("%s not available: %v", tool, err)
	}
	return p, nil
}

// --- gaps that need ffmpeg ------------------------------------------------------------------

// plan picks crf 23 above 1280 px width and 28 up to it, re-encodes a video ffprobe cannot
// read, and skips a file without a video stream.
func TestPlan(t *testing.T) {
	requireFFmpeg(t)
	p, songDir, _ := newProcessor(t, false)
	song := filepath.Join(songDir, "A - S")
	writeFile(t, filepath.Join(song, "song.txt"), "#TITLE:S\n#ARTIST:A\n#VIDEO:video.avi\n")
	for _, tt := range []struct {
		size string
		crf  int
	}{{"1282x720", 23}, {"1280x720", 28}, {"320x240", 28}} {
		video := filepath.Join(song, "v"+tt.size+".avi")
		makeMediaFrom(t, video, "testsrc=size="+tt.size+":rate=25:duration=0.2", "-c:v", "mpeg4", "-q:v", "1", "-an")
		pl, ok := p.plan(song, video)
		if want := (encodePlan{reEncode: true, crf: tt.crf}); !ok || pl != want {
			t.Errorf("plan(%s) = %+v, %v; want %+v, true", tt.size, pl, ok, want)
		}
	}

	garbage := filepath.Join(song, "garbage.avi")
	writeFile(t, garbage, strings.Repeat("garbage", 200))
	if pl, ok := p.plan(song, garbage); !ok || !pl.reEncode || pl.resize || pl.removeAudio {
		t.Errorf("plan(garbage) = %+v, %v; want a re-encode attempt", pl, ok)
	}

	audioOnly := filepath.Join(song, "audio.mp4")
	makeMedia(t, audioOnly, "-vn", "-c:a", "aac")
	if pl, ok := p.plan(song, audioOnly); ok || pl != (encodePlan{}) {
		t.Errorf("plan(audio-only mp4) = %+v, %v; want nothing to do", pl, ok)
	}
	if st := p.ops.Stats(); st != (fsops.Stats{}) {
		t.Errorf("Stats = %+v, plan must not change anything", st)
	}
}

// An audio-only .mp4 is left alone by Run.
func TestAudioOnlyMp4IsSkipped(t *testing.T) {
	requireFFmpeg(t)
	cfg, songDir, _ := newLibrary(t, false)
	song := filepath.Join(songDir, "A - S")
	writeFile(t, filepath.Join(song, "song.txt"), "#TITLE:S\n#ARTIST:A\n#MP3:song.mp3\n#VIDEO:video.mp4\n")
	writeFile(t, filepath.Join(song, "song.mp3"), "audio")
	makeMedia(t, filepath.Join(song, "video.mp4"), "-vn", "-c:a", "aac")
	before := snapshot(t, songDir)
	ops := fsops.New(false)
	if err := Run(cfg, ops); err != nil {
		t.Fatal(err)
	}
	if st := ops.Stats(); st != (fsops.Stats{}) {
		t.Errorf("Stats = %+v, want nothing done", st)
	}
	if after := snapshot(t, songDir); !slices.Equal(before, after) {
		t.Errorf("library changed\nbefore: %v\n after: %v", before, after)
	}
}

func TestNeedsRemoveAudio(t *testing.T) {
	requireFFmpeg(t)
	src := t.TempDir()
	withAudio, silent := filepath.Join(src, "a.mp4"), filepath.Join(src, "s.mp4")
	makeMedia(t, withAudio, "-c:v", "libx264", "-crf", "35", "-c:a", "aac")
	makeMedia(t, silent, "-c:v", "libx264", "-crf", "35", "-an")

	tests := []struct {
		name, tags string
		silent     bool
		want       bool
	}{
		{"#MP3 names a separate file", "#MP3:song.mp3\n", false, true},
		{"#AUDIO wins over #MP3 naming the video", "#AUDIO:song.mp3\n#MP3:video.mp4\n", false, true},
		{"#AUDIO naming the video wins over #MP3", "#MP3:song.mp3\n#AUDIO:video.mp4\n", false, false},
		{"#AUDIO missing, #MP3 exists", "#AUDIO:missing.mp3\n#MP3:song.mp3\n", false, false},
		{"#MP3 names an image", "#MP3:cover.jpg\n", false, false},
		{"#MP3 names an empty file", "#MP3:empty.mp3\n", false, false},
		{"no audio tag", "", false, false},
		{"video without audio", "#MP3:song.mp3\n", true, false},
		{"#MP3 case differs", "#MP3:SONG.MP3\n", false, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			song := t.TempDir()
			writeFile(t, filepath.Join(song, "song.txt"), "#TITLE:S\n#ARTIST:A\n"+tt.tags+"#VIDEO:video.mp4\n")
			writeFile(t, filepath.Join(song, "song.mp3"), "audio")
			writeFile(t, filepath.Join(song, "cover.jpg"), "image")
			writeFile(t, filepath.Join(song, "empty.mp3"), "")
			video := filepath.Join(song, "video.mp4")
			writeFile(t, video, readFile(t, map[bool]string{false: withAudio, true: silent}[tt.silent]))
			if got := needsRemoveAudio(nil, song, video); got != tt.want {
				t.Errorf("needsRemoveAudio = %v, want %v", got, tt.want)
			}
		})
	}

	t.Run("no song txt", func(t *testing.T) {
		song := t.TempDir()
		writeFile(t, filepath.Join(song, "song.mp3"), "audio")
		video := filepath.Join(song, "video.mp4")
		writeFile(t, video, readFile(t, withAudio))
		if needsRemoveAudio(nil, song, video) {
			t.Error("needsRemoveAudio = true without a song txt")
		}
	})
}

// Stripping the audio is idempotent: the second run leaves the tagged result alone.
func TestStripAudioTwice(t *testing.T) {
	requireFFmpeg(t)
	cfg, songDir, _ := newLibrary(t, false)
	song := filepath.Join(songDir, "A - S")
	writeFile(t, filepath.Join(song, "song.txt"), "#TITLE:S\n#ARTIST:A\n#MP3:song.mp3\n#VIDEO:video.mp4\n")
	writeFile(t, filepath.Join(song, "song.mp3"), "audio")
	video := filepath.Join(song, "video.mp4")
	makeMedia(t, video, "-c:v", "libx264", "-crf", "35", "-c:a", "aac")
	ops := fsops.New(false)
	if err := Run(cfg, ops); err != nil {
		t.Fatal(err)
	}
	if st := ops.Stats(); st != (fsops.Stats{Replaced: 1}) {
		t.Errorf("first run Stats = %+v, want 1 replacement", st)
	}
	assertTagged(t, video, true)
	before := snapshot(t, songDir)
	ops = fsops.New(false)
	if err := Run(cfg, ops); err != nil {
		t.Fatal(err)
	}
	if st := ops.Stats(); st != (fsops.Stats{}) {
		t.Errorf("second run Stats = %+v, want nothing done", st)
	}
	if after := snapshot(t, songDir); !slices.Equal(before, after) {
		t.Errorf("second run changed the library\nbefore: %v\n after: %v", before, after)
	}
}

// A video ffmpeg can read but not process (an audio codec MP4 cannot hold) is a failure,
// not a corrupted file, and the original stays untouched.
func TestFFmpegFailureIsCounted(t *testing.T) {
	requireFFmpeg(t)
	cfg, songDir, outDir := newLibrary(t, false)
	song := filepath.Join(songDir, "A - S")
	txt := "#TITLE:S\n#ARTIST:A\n#MP3:video.avi\n#VIDEO:video.avi\n"
	writeFile(t, filepath.Join(song, "song.txt"), txt)
	video := filepath.Join(song, "video.avi")
	makeMedia(t, video, "-c:v", "mpeg4", "-q:v", "1", "-c:a", "adpcm_ms")
	before := snapshot(t, video)

	ops := fsops.New(false)
	if err := Run(cfg, ops); err != nil {
		t.Fatal(err)
	}
	if st := ops.Stats(); st != (fsops.Stats{Failed: 1}) {
		t.Errorf("Stats = %+v, want 1 failure", st)
	}
	if _, err := os.Stat(filepath.Join(outDir, corruptedReportName)); !os.IsNotExist(err) {
		t.Errorf("a processing failure must not be reported as corrupted (err=%v)", err)
	}
	if after := snapshot(t, video); !slices.Equal(before, after) {
		t.Errorf("original changed\nbefore: %v\n after: %v", before, after)
	}
	if got := dirNames(t, song); !slices.Equal(got, []string{"song.txt", "video.avi"}) {
		t.Errorf("song folder = %v, want song.txt and video.avi", got)
	}
	if got := readFile(t, filepath.Join(song, "song.txt")); got != txt {
		t.Errorf("txt changed to %q", got)
	}
}

// A high-bitrate H.264 MP4 is re-encoded, a low-bitrate one is not.
func TestGetNeedsReEncodeBitrate(t *testing.T) {
	requireFFmpeg(t)
	dir := t.TempDir()
	high, low := filepath.Join(dir, "high.mp4"), filepath.Join(dir, "low.mp4")
	makeMediaFrom(t, high, "testsrc2=size=640x480:rate=25:duration=1", "-c:v", "libx264", "-qp", "0", "-an")
	makeMedia(t, low, "-c:v", "libx264", "-crf", "35", "-an")
	if br, err := getVideoBitrate(high); err != nil || br <= BitrateThreshold {
		t.Fatalf("getVideoBitrate(high) = %d, %v; want above %d", br, err, BitrateThreshold)
	}
	if !getNeedsReEncode(high, "h264") {
		t.Error("high-bitrate H.264 must be re-encoded")
	}
	if getNeedsReEncode(low, "h264") {
		t.Error("low-bitrate H.264 must not be re-encoded")
	}
	if getNeedsReEncode(filepath.Join(dir, "missing.mp4"), "mpeg4") {
		t.Error("a missing file must not be re-encoded")
	}
	// Without a size estimate (unreadable file) the codec alone decides.
	garbage := filepath.Join(dir, "garbage.avi")
	writeFile(t, garbage, "garbage")
	if !getNeedsReEncode(garbage, "mpeg4") || getNeedsReEncode(garbage, "h264") {
		t.Error("without an estimate, the codec must decide")
	}
}

// The size estimate of a 1 s 320x240 clip at 25 fps.
func TestEstimatedSizeOfReEncodedVideo(t *testing.T) {
	requireFFmpeg(t)
	video := filepath.Join(t.TempDir(), "v.mp4")
	makeMedia(t, video, "-c:v", "libx264", "-crf", "35", "-an")
	got, err := estimatedSizeOfReEncodedVideo(video)
	if err != nil || got < 6900 || got > 7000 {
		t.Errorf("estimate = %d, %v; want about 6944", got, err)
	}
	if fps, err := getVideoFramerate(video); err != nil || fps != 25 {
		t.Errorf("getVideoFramerate = %v, %v; want 25", fps, err)
	}
	for _, f := range []func(string) error{
		func(f string) error { _, err := getVideoBitrate(f); return err },
		func(f string) error { _, err := getVideoFramerate(f); return err },
		func(f string) error { _, err := getVideoDuration(f); return err },
		func(f string) error { _, _, err := getVideoResolution(f); return err },
		func(f string) error { _, err := estimatedSizeOfReEncodedVideo(f); return err },
	} {
		if err := f(filepath.Join(t.TempDir(), "missing.mp4")); err == nil {
			t.Error("probing a missing file must fail")
		}
	}
}

// A video recorded as processed in the media journal is skipped.
func TestJournaledProcessedVideoIsSkipped(t *testing.T) {
	requireFFmpeg(t)
	cfg, songDir, _ := newLibrary(t, false)
	song := filepath.Join(songDir, "A - S")
	writeFile(t, filepath.Join(song, "song.txt"), "#TITLE:S\n#ARTIST:A\n#MP3:song.mp3\n#VIDEO:video.avi\n")
	writeFile(t, filepath.Join(song, "song.mp3"), "audio")
	video := filepath.Join(song, "video.avi")
	makeMedia(t, video, "-c:v", "mpeg4", "-q:v", "1", "-c:a", "ac3") // would be re-encoded and stripped
	j, err := journal.Open(cfg.JournalPath())
	if err != nil {
		t.Fatal(err)
	}
	if err := j.AddAndSave(journalProcessed, video, fsops.New(false)); err != nil {
		t.Fatal(err)
	}
	before := snapshot(t, songDir)
	ops := fsops.New(false)
	if err := Run(cfg, ops); err != nil {
		t.Fatal(err)
	}
	if st := ops.Stats(); st != (fsops.Stats{}) {
		t.Errorf("Stats = %+v, want nothing done", st)
	}
	if after := snapshot(t, songDir); !slices.Equal(before, after) {
		t.Errorf("journaled video was changed\nbefore: %v\n after: %v", before, after)
	}
}

// In a dry run, an unreferenced video that would need a conversion is not planned either.
func TestDryRunUnreferencedVideoIsNotPlanned(t *testing.T) {
	requireFFmpeg(t)
	cfg, songDir, _ := newLibrary(t, true)
	song := filepath.Join(songDir, "A - S")
	writeFile(t, filepath.Join(song, "song.txt"), "#TITLE:S\n#ARTIST:A\n#VIDEO:other.mp4\n")
	makeMedia(t, filepath.Join(song, "video.avi"), "-c:v", "mpeg4", "-q:v", "1", "-c:a", "ac3")
	ops := fsops.New(true)
	if err := Run(cfg, ops); err != nil {
		t.Fatal(err)
	}
	if st := ops.Stats(); st != (fsops.Stats{}) {
		t.Errorf("Stats = %+v, want nothing planned", st)
	}
}

// A remux that ffmpeg cannot write (mpeg2video in FLV) leaves the original untouched and
// records it in the journal instead.
func TestFailedRemuxFallsBackToJournal(t *testing.T) {
	requireFFmpeg(t)
	p, songDir, _ := newProcessor(t, false)
	video := filepath.Join(songDir, "A - S", "video.flv")
	if err := os.MkdirAll(filepath.Dir(video), 0o755); err != nil {
		t.Fatal(err)
	}
	makeMedia(t, video, "-c:v", "mpeg2video", "-c:a", "mp2", "-f", "matroska")
	before := snapshot(t, video)
	if err := p.tagByRemux(video); err == nil || !strings.Contains(err.Error(), "ffmpeg remux") {
		t.Errorf("tagByRemux = %v, want an ffmpeg remux error", err)
	}
	p.markChecked(video)
	if st := p.ops.Stats(); st.Replaced != 0 || st.Failed != 0 {
		t.Errorf("Stats = %+v, the failed remux must not replace the original", st)
	}
	if after := snapshot(t, video); !slices.Equal(before, after) {
		t.Errorf("original changed\nbefore: %v\n after: %v", before, after)
	}
	if !p.journal.Has(journalChecked, video) {
		t.Error("original not recorded as checked in the journal")
	}
	assertNoTempFiles(t, songDir)

	// A file ffprobe cannot read is not remuxed at all.
	garbage := filepath.Join(songDir, "A - S", "garbage.mkv")
	writeFile(t, garbage, "garbage")
	if err := p.tagByRemux(garbage); err == nil {
		t.Error("tagByRemux(garbage) = nil, want an error")
	}
}
