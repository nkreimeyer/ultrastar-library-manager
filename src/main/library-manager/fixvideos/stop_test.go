package fixvideos

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"testing"
	"time"

	"github.com/nkreimeyer/library-manager/fsops"
	"github.com/nkreimeyer/library-manager/internal/testutil"
	"github.com/nkreimeyer/library-manager/journal"
)

// hangingProbe makes ffmpeg and ffprobe on PATH answer "-version" with 7.1. ffprobe on a
// file matching the pattern *hang creates the returned mark file and then hangs until it
// is killed, on one matching *ok it prints probeJSON, and on anything else it fails.
// ffmpeg exits with ffmpegExit. A test cancels its ctx once the mark exists, so the stop
// hits exactly that probe.
func hangingProbe(t *testing.T, hang, ok, probeJSON, ffmpegExit string) string {
	t.Helper()
	sleep := testutil.RequireTool(t, "sleep")
	mark := filepath.Join(t.TempDir(), "probing")
	version := func(name string) string {
		return "#!/bin/sh\nif [ \"$1\" = -version ]; then echo '" + name + " version 7.1 Copyright (c) the FFmpeg developers'; exit 0; fi\n"
	}
	testutil.FakeTools(t, map[string]string{
		"ffmpeg": version("ffmpeg") + "exit " + ffmpegExit + "\n",
		"ffprobe": version("ffprobe") + "for a; do last=$a; done\ncase \"$last\" in\n*" + hang + ") : > '" + mark +
			"'; exec '" + sleep + "' 30;;\n*" + ok + ") echo '" + probeJSON + "'; exit 0;;\nesac\nexit 1\n",
	})
	return mark
}

// cancelWhen calls cancel once the file mark exists.
func cancelWhen(t *testing.T, mark string, cancel context.CancelFunc) {
	t.Helper()
	go func() {
		defer cancel()
		for deadline := time.Now().Add(20 * time.Second); time.Now().Before(deadline); time.Sleep(5 * time.Millisecond) {
			if testutil.Exists(mark) {
				return
			}
		}
	}()
}

// threeSeconds is ffprobe JSON of a 3 s video.
const threeSeconds = `{"streams":[{"codec_type":"video","duration":"3.0"}],"format":{"duration":"3.0"}}`

// stoppedProcessor returns a processor whose ctx is already done.
func stoppedProcessor(t *testing.T) (*processor, string, string) {
	t.Helper()
	p, songDir, outDir := newProcessor(t, false)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	p.ctx = ctx
	return p, songDir, outDir
}

// staleReport writes a corrupted-files report whose only entry is a file that no longer
// exists: every run that writes the report drops it.
func staleReport(t *testing.T, outDir string) (path, content string) {
	t.Helper()
	path = filepath.Join(outDir, corruptedReportName)
	content = filepath.Join(outDir, "gone.avi") + "\n"
	testutil.WriteFile(t, path, content)
	return path, content
}

// A run whose ctx is already done processes nothing and reports the stop, not a missing
// ffmpeg ("-version" cannot run with a done ctx).
func TestRunWithCancelledBeforeStart(t *testing.T) {
	testutil.FakeVersions(t, "7.1", "7.1")
	cfg, songDir, outDir := newLibrary(t, false)
	song := filepath.Join(songDir, "A - S")
	testutil.WriteFile(t, filepath.Join(song, "song.txt"), "#TITLE:S\n#ARTIST:A\n#MP3:a.mp3\n#VIDEO:v.avi\n")
	testutil.WriteFile(t, filepath.Join(song, "v.avi"), "video")
	report, content := staleReport(t, outDir)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	ops := fsops.New(false)
	err := RunWith(cfg, ops, Options{Ctx: ctx})
	if !errors.Is(err, context.Canceled) {
		t.Errorf("RunWith = %v, want context.Canceled", err)
	}
	if st := ops.Stats(); st != (fsops.Stats{}) {
		t.Errorf("Stats = %+v, want nothing done", st)
	}
	if got := testutil.ReadFile(t, report); got != content {
		t.Errorf("report changed to %q", got)
	}
}

// smallVideos creates song folders with tiny H.264 videos without audio, which need no
// processing, and returns their paths. "B - Two" holds two videos, "C - None" none.
func smallVideos(t *testing.T, songDir string) []string {
	t.Helper()
	var videos []string
	for _, v := range []string{"A - One/v.mp4", "B - Two/v.mp4", "B - Two/w.mp4"} {
		path := filepath.Join(songDir, v)
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		testutil.GenerateMedia(t, "-f", "lavfi", "-i", "testsrc=size=64x48:rate=10:duration=0.3",
			"-c:v", "libx264", "-an", path)
		testutil.WriteFile(t, filepath.Join(filepath.Dir(path), "song.txt"),
			"#TITLE:S\n#ARTIST:A\n#MP3:a.mp3\n#VIDEO:"+filepath.Base(path)+"\n")
		videos = append(videos, path)
	}
	testutil.WriteFile(t, filepath.Join(songDir, "C - None", "song.txt"), "#TITLE:N\n#ARTIST:C\n#MP3:a.mp3\n")
	return videos
}

// Progress is called once per video file, not per song folder.
func TestRunWithProgress(t *testing.T) {
	testutil.RequireFFmpeg(t)
	cfg, songDir, _ := newLibrary(t, false)
	smallVideos(t, songDir)

	var calls [][2]int
	ops := fsops.New(false)
	err := RunWith(cfg, ops, Options{Progress: func(done, total int) { calls = append(calls, [2]int{done, total}) }})
	if err != nil {
		t.Fatalf("RunWith: %v", err)
	}
	if want := [][2]int{{1, 3}, {2, 3}, {3, 3}}; !slices.Equal(calls, want) {
		t.Errorf("Progress calls = %v, want %v", calls, want)
	}
	if st := ops.Stats(); st != (fsops.Stats{}) {
		t.Errorf("Stats = %+v, want nothing done", st)
	}
}

// Once the ctx is done no further video starts, and the corrupted-files report is left as
// it is: the run did not look at every video.
func TestRunWithStopAfterFirstVideo(t *testing.T) {
	testutil.RequireFFmpeg(t)
	oldWorkers := workers
	workers = 1 // videos in order: the stop comes after the first
	t.Cleanup(func() { workers = oldWorkers })
	cfg, songDir, outDir := newLibrary(t, false)
	smallVideos(t, songDir)
	report, content := staleReport(t, outDir)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	var calls [][2]int
	err := RunWith(cfg, fsops.New(false), Options{Ctx: ctx, Progress: func(done, total int) {
		calls = append(calls, [2]int{done, total})
		cancel()
	}})
	if !errors.Is(err, context.Canceled) {
		t.Errorf("RunWith = %v, want context.Canceled", err)
	}
	if want := [][2]int{{1, 3}}; !slices.Equal(calls, want) {
		t.Errorf("Progress calls = %v, want %v", calls, want)
	}
	if got := testutil.ReadFile(t, report); got != content {
		t.Errorf("report changed to %q", got)
	}

	// A full run drops the stale entry (the check above means something).
	if err := Run(cfg, fsops.New(false)); err != nil {
		t.Fatal(err)
	}
	if got := testutil.ReadFile(t, report); got != "" {
		t.Errorf("report after a full run = %q, want it emptied", got)
	}
}

// A probe stopped by the ctx is no failure and no unreadable video (which would be
// encoded and flagged): the video is left alone, the report untouched, and the run,
// though every video was started, reports the stop.
func TestRunWithStoppedProbe(t *testing.T) {
	mark := hangingProbe(t, "v.avi", "never", "", "1")
	cfg, songDir, outDir := newLibrary(t, false)
	song := filepath.Join(songDir, "A - S")
	testutil.WriteFile(t, filepath.Join(song, "song.txt"), "#TITLE:S\n#ARTIST:A\n#MP3:a.mp3\n#VIDEO:v.avi\n")
	video := filepath.Join(song, "v.avi")
	testutil.WriteFile(t, video, "video")
	report, content := staleReport(t, outDir)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	cancelWhen(t, mark, cancel)

	ops := fsops.New(false)
	err := RunWith(cfg, ops, Options{Ctx: ctx})
	if !errors.Is(err, context.Canceled) {
		t.Errorf("RunWith = %v, want context.Canceled", err)
	}
	if st := ops.Stats(); st != (fsops.Stats{}) {
		t.Errorf("Stats = %+v, want nothing done and no failure", st)
	}
	if got := testutil.ReadFile(t, video); got != "video" {
		t.Errorf("video changed to %q", got)
	}
	if got := testutil.ReadFile(t, report); got != content {
		t.Errorf("report changed to %q", got)
	}
	assertNoTempFiles(t, songDir)
}

// An encode whose duration check is stopped is discarded: unchecked, it may have lost a
// damaged section. The original is neither replaced nor flagged.
func TestEncodeStoppedBeforeDurationCheck(t *testing.T) {
	testutil.RequireFFmpeg(t)
	p, songDir, _ := stoppedProcessor(t)
	song := filepath.Join(songDir, "A - S")
	testutil.WriteFile(t, filepath.Join(song, "song.txt"), "#TITLE:S\n#ARTIST:A\n#MP3:a.mp3\n#VIDEO:v.mkv\n")
	video := filepath.Join(song, "v.mkv")
	makeMediaFrom(t, video, "testsrc=size=64x48:rate=10:duration=1", "-c:v", "libx264", "-c:a", "aac")
	before := testutil.ReadFile(t, video)

	p.encode(song, video, encodePlan{removeAudio: true})
	if got := testutil.ReadFile(t, video); got != before {
		t.Error("the video was replaced after the stop")
	}
	if len(p.corrupted) != 0 {
		t.Errorf("corrupted = %v, want none", p.corrupted)
	}
	if st := p.ops.Stats(); st != (fsops.Stats{}) {
		t.Errorf("Stats = %+v, want nothing done and no failure", st)
	}
	if !p.stopped.Load() {
		t.Error("the stop was not recorded")
	}
	assertNoTempFiles(t, songDir)
}

// A stop during the probe of the original is no match: an unread original would
// otherwise let an unchecked result through.
func TestSameVideoDurationStoppedAtOriginal(t *testing.T) {
	mark := hangingProbe(t, "in.mkv", "out.mp4", threeSeconds, "1")
	dir := t.TempDir()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	cancelWhen(t, mark, cancel)

	ok, _, err := sameVideoDuration(ctx, filepath.Join(dir, "in.mkv"), filepath.Join(dir, "out.mp4"), false)
	if ok || !errors.Is(err, context.Canceled) {
		t.Errorf("sameVideoDuration = %v, %v; want false, context.Canceled", ok, err)
	}
}

// A stop during the probe of the result is no "cannot read" either.
func TestSameVideoDurationStoppedAtResult(t *testing.T) {
	mark := hangingProbe(t, "out.mp4", "in.mkv", threeSeconds, "1")
	dir := t.TempDir()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	cancelWhen(t, mark, cancel)

	ok, why, err := sameVideoDuration(ctx, filepath.Join(dir, "in.mkv"), filepath.Join(dir, "out.mp4"), false)
	if ok || why != "" || !errors.Is(err, context.Canceled) {
		t.Errorf("sameVideoDuration = %v, %q, %v; want false, \"\", context.Canceled", ok, why, err)
	}
}

// A stop during the probe of a marker remux discards the remux and journals nothing.
func TestMarkCheckedStoppedAtRemuxProbe(t *testing.T) {
	// The remux "succeeds" (exit 0, leaving the empty temp file), its probe hangs.
	mark := hangingProbe(t, "sanitizer-*.mkv", "v.mkv", threeSeconds, "0")
	p, songDir, outDir := newProcessor(t, false)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	p.ctx = ctx
	cancelWhen(t, mark, cancel)
	video := filepath.Join(songDir, "A - S", "v.mkv")
	testutil.WriteFile(t, video, "video")

	p.markChecked(video)
	if got := testutil.ReadFile(t, video); got != "video" {
		t.Errorf("video changed to %q", got)
	}
	if p.journal.Has(journalChecked, video) {
		t.Error("the video was journaled as checked")
	}
	if _, err := os.Stat(filepath.Join(outDir, "media-journal.txt")); !os.IsNotExist(err) {
		t.Errorf("nothing must be journaled (err=%v)", err)
	}
	if st := p.ops.Stats(); st != (fsops.Stats{}) || !p.stopped.Load() {
		t.Errorf("Stats = %+v, stopped = %v; want nothing done, true", st, p.stopped.Load())
	}
	assertNoTempFiles(t, songDir)
}

// A larger re-encode whose audio must go is not followed by the strip-only encode after a
// stop (no further encode starts); the original stays and nothing is recorded.
func TestLargerReEncodeAfterStopStartsNoEncode(t *testing.T) {
	p, songDir, outDir := stoppedProcessor(t)
	song := filepath.Join(songDir, "A - S")
	testutil.WriteFile(t, filepath.Join(song, "song.txt"), "#TITLE:S\n#ARTIST:A\n#MP3:a.mp3\n#VIDEO:v.mkv\n")
	video := filepath.Join(song, "v.mkv")
	testutil.WriteFile(t, video, "video")
	tmp, err := fsops.TempFileNear(video, ".mp4")
	if err != nil {
		t.Fatal(err)
	}
	testutil.WriteFile(t, tmp, "a re-encode larger than the original")

	p.replaceOriginal(song, video, tmp, encodePlan{reEncode: true, removeAudio: true})
	if got := testutil.ReadFile(t, video); got != "video" {
		t.Errorf("video changed to %q", got)
	}
	if st := p.ops.Stats(); st != (fsops.Stats{}) {
		t.Errorf("Stats = %+v, want nothing done and no failure", st)
	}
	if len(p.corrupted) != 0 || !p.stopped.Load() {
		t.Errorf("corrupted = %v, stopped = %v; want none, true", p.corrupted, p.stopped.Load())
	}
	if _, err := os.Stat(filepath.Join(outDir, "media-journal.txt")); !os.IsNotExist(err) {
		t.Errorf("nothing must be journaled (err=%v)", err)
	}
	assertNoTempFiles(t, songDir)
}

// A video whose marker remux is stopped is not journaled as checked: this run never
// checked it, and the next one tries again.
func TestMarkCheckedStopped(t *testing.T) {
	p, songDir, outDir := stoppedProcessor(t)
	video := filepath.Join(songDir, "A - S", "v.mkv")
	testutil.WriteFile(t, video, "video")

	p.markChecked(video)
	if p.journal.Has(journalChecked, video) {
		t.Error("the video was journaled as checked")
	}
	if _, err := os.Stat(filepath.Join(outDir, "media-journal.txt")); !os.IsNotExist(err) {
		t.Errorf("nothing must be journaled (err=%v)", err)
	}
	if st := p.ops.Stats(); st != (fsops.Stats{}) || !p.stopped.Load() {
		t.Errorf("Stats = %+v, stopped = %v; want nothing done, true", st, p.stopped.Load())
	}
	j, err := journal.Open(p.cfg.JournalPath())
	if err != nil || j.Has(journalChecked, video) {
		t.Errorf("saved journal has the video (err=%v)", err)
	}
}

// A stop while plan measures the packet sizes (that ffprobe run does not stop with the
// ctx) starts no encode: the video is left alone, without a failure.
func TestRunWithStopDuringBitrateMeasuring(t *testing.T) {
	sleep := testutil.RequireTool(t, "sleep")
	tmp := t.TempDir()
	mark, encoded := filepath.Join(tmp, "measuring"), filepath.Join(tmp, "encoded")
	version := func(name string) string {
		return "#!/bin/sh\nif [ \"$1\" = -version ]; then echo '" + name + " version 7.1 Copyright (c) the FFmpeg developers'; exit 0; fi\n"
	}
	// An h264 video without a stream bitrate, with audio that a separate #MP3 replaces.
	testutil.FakeTools(t, map[string]string{
		"ffprobe": version("ffprobe") + "case \"$*\" in *packet=size*) : > '" + mark + "'; '" + sleep + "' 1; exit 1;; esac\n" +
			`echo '{"streams":[{"codec_type":"video","codec_name":"h264","width":64,"height":48},{"codec_type":"audio"}],"format":{"duration":"3.0"}}'` + "\n",
		"ffmpeg": version("ffmpeg") + ": > '" + encoded + "'\nexit 1\n",
	})
	cfg, songDir, outDir := newLibrary(t, false)
	song := filepath.Join(songDir, "A - S")
	testutil.WriteFile(t, filepath.Join(song, "song.txt"), "#TITLE:S\n#ARTIST:A\n#MP3:a.mp3\n#VIDEO:v.mkv\n")
	testutil.WriteFile(t, filepath.Join(song, "a.mp3"), "audio")
	video := filepath.Join(song, "v.mkv")
	testutil.WriteFile(t, video, "video")
	report, content := staleReport(t, outDir)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	cancelWhen(t, mark, cancel)

	ops := fsops.New(false)
	err := RunWith(cfg, ops, Options{Ctx: ctx})
	if !errors.Is(err, context.Canceled) {
		t.Errorf("RunWith = %v, want context.Canceled", err)
	}
	if testutil.Exists(encoded) {
		t.Error("an encode started after the stop")
	}
	if st := ops.Stats(); st != (fsops.Stats{}) {
		t.Errorf("Stats = %+v, want nothing done and no failure", st)
	}
	if got := testutil.ReadFile(t, video); got != "video" {
		t.Errorf("video changed to %q", got)
	}
	if got := testutil.ReadFile(t, report); got != content {
		t.Errorf("report changed to %q", got)
	}
	assertNoTempFiles(t, songDir)
}
