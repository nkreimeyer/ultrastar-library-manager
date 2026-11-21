package fixaudio

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

// hangingProbe makes ffmpeg and ffprobe on PATH answer "-version" with 7.1 and fail on
// everything else, except that ffprobe on a file ending in suffix creates the returned
// mark file and then hangs until it is killed. A test cancels its ctx once the mark
// exists, so the stop hits exactly that probe.
func hangingProbe(t *testing.T, suffix string) string {
	t.Helper()
	sleep := testutil.RequireTool(t, "sleep")
	mark := filepath.Join(t.TempDir(), "probing")
	version := func(name string) string {
		return "#!/bin/sh\nif [ \"$1\" = -version ]; then echo '" + name + " version 7.1 Copyright (c) the FFmpeg developers'; exit 0; fi\n"
	}
	testutil.FakeTools(t, map[string]string{
		"ffmpeg": version("ffmpeg") + "exit 1\n",
		"ffprobe": version("ffprobe") + "for a; do last=$a; done\ncase \"$last\" in *" + suffix + ") : > '" + mark +
			"'; exec '" + sleep + "' 30;; esac\nexit 1\n",
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

// A run whose ctx is already done processes nothing and reports the stop, not a missing
// ffmpeg ("-version" cannot run with a done ctx).
func TestRunWithCancelledBeforeStart(t *testing.T) {
	testutil.FakeVersions(t, "7.1", "7.1")
	cfg, ops := newLibrary(t, false)
	addSong(t, cfg, "A - S", "song.mp3")
	testutil.WriteFile(t, filepath.Join(cfg.SongDir, "A - S", "song.mp3"), "audio")
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	err := RunWith(cfg, ops, Options{Ctx: ctx})
	if !errors.Is(err, context.Canceled) {
		t.Errorf("RunWith = %v, want context.Canceled", err)
	}
	if st := ops.Stats(); st != (fsops.Stats{}) {
		t.Errorf("Stats = %+v, want nothing done", st)
	}
	if entries, _ := os.ReadDir(cfg.OutDir); len(entries) != 0 {
		t.Errorf("out dir has %d entries, want none", len(entries))
	}
}

// Progress is called once per song folder (folders without audio count too).
func TestRunWithProgress(t *testing.T) {
	testutil.FakeVersions(t, "7.1", "7.1")
	cfg, ops := newLibrary(t, false)
	for _, rel := range []string{"A - One", "B - Two", "Cat/C - Three"} {
		addSong(t, cfg, rel, "missing.mp3") // no audio file: no ffprobe call
	}
	var calls [][2]int
	err := RunWith(cfg, ops, Options{Progress: func(done, total int) { calls = append(calls, [2]int{done, total}) }})
	if err != nil {
		t.Fatalf("RunWith: %v", err)
	}
	if want := [][2]int{{1, 3}, {2, 3}, {3, 3}}; !slices.Equal(calls, want) {
		t.Errorf("Progress calls = %v, want %v", calls, want)
	}
}

// A probe stopped by the ctx is no failure: the file is left alone, and the run, though
// every folder was started, reports the stop.
func TestRunWithStoppedProbe(t *testing.T) {
	mark := hangingProbe(t, "song.mp3")
	cfg, ops := newLibrary(t, false)
	dir := addSong(t, cfg, "A - S", "song.mp3")
	audio := filepath.Join(dir, "song.mp3")
	testutil.WriteFile(t, audio, "audio")
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	cancelWhen(t, mark, cancel)

	err := RunWith(cfg, ops, Options{Ctx: ctx})
	if !errors.Is(err, context.Canceled) {
		t.Errorf("RunWith = %v, want context.Canceled", err)
	}
	if st := ops.Stats(); st != (fsops.Stats{}) {
		t.Errorf("Stats = %+v, want nothing done and no failure", st)
	}
	if got := testutil.ReadFile(t, audio); got != "audio" {
		t.Errorf("audio changed to %q", got)
	}
}

// Once the ctx is done no further folder starts; the files done before stay done and
// journaled.
func TestRunWithStopAfterFirstFolder(t *testing.T) {
	testutil.RequireFFmpeg(t)
	oldWorkers := workers
	workers = 1 // folders in order: the stop comes between the two
	t.Cleanup(func() { workers = oldWorkers })
	cfg, ops := newLibrary(t, false)
	var audios []string
	for _, rel := range []string{"A - One", "B - Two"} {
		audio := filepath.Join(addSong(t, cfg, rel, "song.mp3"), "song.mp3")
		quietMP3(t, audio, "0.2")
		audios = append(audios, audio)
	}
	untouched := testutil.ReadFile(t, audios[1])
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	var calls [][2]int
	err := RunWith(cfg, ops, Options{Ctx: ctx, Progress: func(done, total int) {
		calls = append(calls, [2]int{done, total})
		cancel()
	}})
	if !errors.Is(err, context.Canceled) {
		t.Errorf("RunWith = %v, want context.Canceled", err)
	}
	if want := [][2]int{{1, 2}}; !slices.Equal(calls, want) {
		t.Errorf("Progress calls = %v, want %v", calls, want)
	}
	if st := ops.Stats(); st.Failed != 0 || st.Replaced != 1 {
		t.Errorf("Stats = %+v, want one file replaced, no failure", st)
	}
	if !probe(t, audios[0]).Normalized {
		t.Errorf("%s was not normalized", audios[0])
	}
	if got := testutil.ReadFile(t, audios[1]); got != untouched {
		t.Errorf("%s changed after the stop", audios[1])
	}
	j, err := journal.Open(cfg.JournalPath())
	if err != nil {
		t.Fatal(err)
	}
	if !j.Has(journalKind, audios[0]) || j.Has(journalKind, audios[1]) {
		t.Errorf("journal has first %v, second %v; want only the first", j.Has(journalKind, audios[0]), j.Has(journalKind, audios[1]))
	}
}

// probeAudio passes its ctx to ffprobe: a done ctx returns the ctx's error (also
// without ffprobe on PATH).
func TestProbeAudioStopped(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := probeAudio(ctx, filepath.Join(t.TempDir(), "a.mp3")); !errors.Is(err, context.Canceled) {
		t.Errorf("probeAudio = %v, want context.Canceled", err)
	}
}

// A stop while the peak is measured (that ffmpeg run does not stop with the ctx) starts
// no encode: the file is left alone, without a failure.
func TestRunWithStopDuringPeakMeasuring(t *testing.T) {
	sleep := testutil.RequireTool(t, "sleep")
	tmp := t.TempDir()
	mark, encoded := filepath.Join(tmp, "measuring"), filepath.Join(tmp, "encoded")
	version := func(name string) string {
		return "#!/bin/sh\nif [ \"$1\" = -version ]; then echo '" + name + " version 7.1 Copyright (c) the FFmpeg developers'; exit 0; fi\n"
	}
	testutil.FakeTools(t, map[string]string{
		"ffprobe": version("ffprobe") +
			`echo '{"streams":[{"codec_type":"audio","codec_name":"mp3","channels":2,"sample_fmt":"fltp"}],"format":{}}'` + "\n",
		"ffmpeg": version("ffmpeg") + "case \"$*\" in *astats*) : > '" + mark + "'; '" + sleep +
			"' 1; echo 'Peak level dB: -10.0'; exit 0;; esac\n: > '" + encoded + "'\nexit 1\n",
	})
	cfg, ops := newLibrary(t, false)
	audio := filepath.Join(addSong(t, cfg, "A - S", "song.mp3"), "song.mp3")
	testutil.WriteFile(t, audio, "audio")
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	cancelWhen(t, mark, cancel)

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
	if got := testutil.ReadFile(t, audio); got != "audio" {
		t.Errorf("audio changed to %q", got)
	}
}
