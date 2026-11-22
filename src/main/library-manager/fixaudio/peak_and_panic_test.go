package fixaudio

import (
	"context"
	"math"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/nkreimeyer/library-manager/internal/testutil"
	"github.com/nkreimeyer/library-manager/journal"
	"github.com/nkreimeyer/library-manager/mediacache"
	log "github.com/sirupsen/logrus"
)

// stereoMP3JSON is the ffprobe answer for a stereo mp3.
const stereoMP3JSON = `{"streams":[{"codec_type":"audio","codec_name":"mp3","channels":2,"sample_fmt":"fltp"}],"format":{"bit_rate":"128000"}}`

// fakeAudioTools makes ffprobe answer probeJSON and ffmpeg print ffmpegOut and exit with
// ffmpegExit for everything but "-version".
func fakeAudioTools(t *testing.T, probeJSON, ffmpegOut string, ffmpegExit int) {
	t.Helper()
	version := func(name string) string {
		return "#!/bin/sh\nif [ \"$1\" = -version ]; then echo '" + name + " version 7.1 Copyright (c) the FFmpeg developers'; exit 0; fi\n"
	}
	testutil.FakeTools(t, map[string]string{
		"ffprobe": version("ffprobe") + "echo '" + probeJSON + "'\n",
		"ffmpeg":  version("ffmpeg") + "printf '%s\\n' '" + ffmpegOut + "'\nexit " + string(rune('0'+ffmpegExit)) + "\n",
	})
}

func newTestProcessor(t *testing.T, dryRun bool) (*processor, string) {
	t.Helper()
	cfg, ops := newLibrary(t, dryRun)
	j, err := journal.Open(cfg.JournalPath())
	if err != nil {
		t.Fatal(err)
	}
	dir := addSong(t, cfg, "A - S", "song.mp3")
	testutil.WriteFile(t, filepath.Join(dir, "song.mp3"), "not really audio")
	return &processor{cfg: cfg, ops: ops, journal: j, ctx: context.Background()}, dir
}

// A panic while one folder is processed is recovered, logged with the folder, counted as
// a failure, and the other folders are still processed.
func TestRunRecoversPanicInOneFolder(t *testing.T) {
	testutil.FakeVersions(t, "7.1", "7.1")
	hook := testutil.CaptureLogsAt(t, log.InfoLevel)
	cfg, ops := newLibrary(t, false)
	boom := addSong(t, cfg, "A - Boom", "missing.mp3")
	other := addSong(t, cfg, "B - Other", "missing.mp3")

	var mu sync.Mutex
	var seen []string
	orig := folderProcessor
	folderProcessor = func(p *processor, folder string) {
		mu.Lock()
		seen = append(seen, folder)
		mu.Unlock()
		if folder == boom {
			panic("boom")
		}
		orig(p, folder)
	}
	t.Cleanup(func() { folderProcessor = orig })

	if err := RunWith(cfg, ops, Options{}); err != nil {
		t.Fatal(err)
	}
	if f := ops.Stats().Failed; f != 1 {
		t.Errorf("Failed = %d, want 1", f)
	}
	found := false
	for _, e := range hook.AllEntries() {
		found = found || e.Message == "fixaudio: "+boom+" not finished: internal error (logged above)"
	}
	if !found {
		t.Error("panic not logged with the folder")
	}
	if len(seen) != 2 {
		t.Errorf("processed folders = %q, want both (the other one included: %s)", seen, other)
	}
}

// A failing peak measurement skips the file and counts a failure; nothing is changed and no
// temp file is left.
func TestProcessFilePeakMeasurementFails(t *testing.T) {
	fakeAudioTools(t, stereoMP3JSON, "boom", 1)
	hook := testutil.CaptureLogsAt(t, log.InfoLevel)
	p, dir := newTestProcessor(t, false)
	before := testutil.Snapshot(t, dir)
	p.processFile(dir, "song.mp3")
	if st := p.ops.Stats(); st.Failed != 1 || st.Replaced != 0 {
		t.Errorf("stats = %+v, want one failure", st)
	}
	found := false
	for _, e := range hook.AllEntries() {
		found = found || strings.Contains(e.Message, "measuring the peak failed")
	}
	if !found {
		t.Error("failure not logged")
	}
	if after := testutil.Snapshot(t, dir); after != before {
		t.Errorf("folder changed:\n%s\n%s", before, after)
	}
}

// A temp file that cannot be created (read-only folder) fails the file, leaves it and the
// folder as they are.
func TestProcessFileTempFileFails(t *testing.T) {
	testutil.SkipIfRoot(t)
	fakeAudioTools(t, stereoMP3JSON, "Peak level dB: -10.0", 0)
	hook := testutil.CaptureLogsAt(t, log.InfoLevel)
	p, dir := newTestProcessor(t, false)
	before := testutil.Snapshot(t, dir)
	if err := os.Chmod(dir, 0o555); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.Chmod(dir, 0o755) })

	p.processFile(dir, "song.mp3")
	if st := p.ops.Stats(); st.Failed != 1 || st.Replaced != 0 || st.Written != 0 {
		t.Errorf("stats = %+v, want one failure", st)
	}
	found := false
	for _, e := range hook.AllEntries() {
		found = found || strings.Contains(e.Message, "creating temp file")
	}
	if !found {
		t.Error("failure not logged")
	}
	os.Chmod(dir, 0o755)
	if after := testutil.Snapshot(t, dir); after != before {
		t.Errorf("folder changed:\n%s\n%s", before, after)
	}
	if testutil.Exists(p.cfg.OriginalsDir()) {
		t.Error("an original was copied before the temp file existed")
	}
}

// A peak that JSON cannot hold (NaN, +Inf) is returned but never stored in the cache; a
// normal one is.
func TestCachedPeakNeverStoresNaNOrInf(t *testing.T) {
	for _, tt := range []struct {
		out    string
		stored int64
	}{{"Peak level dB: nan", 0}, {"Peak level dB: inf", 0}, {"Peak level dB: -3.5", 1}} {
		t.Run(tt.out, func(t *testing.T) {
			fakeAudioTools(t, stereoMP3JSON, tt.out, 0)
			dir := t.TempDir()
			path := filepath.Join(dir, "song.mp3")
			testutil.WriteFile(t, path, "x")
			age(t, path)
			cache := mediacache.Open(context.Background(), dir)
			if cache == nil {
				t.Fatal("no cache")
			}
			peak, err := cachedPeak(cache, path, false)
			if err != nil {
				t.Fatal(err)
			}
			if tt.stored == 0 && !math.IsNaN(peak) && !math.IsInf(peak, 1) {
				t.Errorf("peak = %v", peak)
			}
			if got := cache.Stats().Stored; got != tt.stored {
				t.Errorf("Stored = %d, want %d", got, tt.stored)
			}
		})
	}
}

// Unparsable astats output after an ffmpeg error returns the error with the last three
// lines; without an error it is a parse error. Output naming a system error is not
// cacheable, and a failure is never stored.
func TestMeasurePeakResultOutputHandling(t *testing.T) {
	const lines = "l1\nl2\nl3\nl4\nl5"
	t.Run("error includes the last lines", func(t *testing.T) {
		fakeAudioTools(t, stereoMP3JSON, lines, 1)
		_, ok, err := measurePeakResult("x.mp3", false)
		if err == nil || ok {
			t.Fatalf("err = %v, ok = %v", err, ok)
		}
		if !strings.Contains(err.Error(), "l3 | l4 | l5") || strings.Contains(err.Error(), "l2") {
			t.Errorf("err = %q, want only the last 3 lines", err)
		}
	})
	t.Run("no error, no peak", func(t *testing.T) {
		fakeAudioTools(t, stereoMP3JSON, lines, 0)
		if _, ok, err := measurePeakResult("x.mp3", false); err == nil || ok || !strings.Contains(err.Error(), "peak level not found") {
			t.Errorf("err = %v, ok = %v", err, ok)
		}
	})
	t.Run("system error is not cacheable", func(t *testing.T) {
		fakeAudioTools(t, stereoMP3JSON, "Peak level dB: -4.0\nInput/output error", 0)
		peak, ok, err := measurePeakResult("x.mp3", false)
		if err != nil || ok || peak != -4 {
			t.Errorf("peak = %v, ok = %v, err = %v; want -4, false, nil", peak, ok, err)
		}
	})
	t.Run("system error result is not stored", func(t *testing.T) {
		fakeAudioTools(t, stereoMP3JSON, "Peak level dB: -4.0\nInput/output error", 0)
		dir := t.TempDir()
		path := filepath.Join(dir, "song.mp3")
		testutil.WriteFile(t, path, "x")
		age(t, path)
		cache := mediacache.Open(context.Background(), dir)
		if _, err := cachedPeak(cache, path, false); err != nil {
			t.Fatal(err)
		}
		if got := cache.Stats().Stored; got != 0 {
			t.Errorf("Stored = %d, want 0", got)
		}
	})
	t.Run("failure is not stored", func(t *testing.T) {
		fakeAudioTools(t, stereoMP3JSON, lines, 1)
		dir := t.TempDir()
		path := filepath.Join(dir, "song.mp3")
		testutil.WriteFile(t, path, "x")
		age(t, path)
		cache := mediacache.Open(context.Background(), dir)
		if _, err := cachedPeak(cache, path, false); err == nil {
			t.Fatal("no error")
		}
		if got := cache.Stats().Stored; got != 0 {
			t.Errorf("Stored = %d, want 0", got)
		}
	})
}
