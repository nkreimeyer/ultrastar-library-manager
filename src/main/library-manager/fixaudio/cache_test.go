package fixaudio

import (
	"context"
	"math"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/nkreimeyer/library-manager/config"
	"github.com/nkreimeyer/library-manager/fsops"
	"github.com/nkreimeyer/library-manager/internal/testutil"
	"github.com/nkreimeyer/library-manager/mediacache"
)

// age sets the mtime of the files to an hour ago: the cache never stores a result for a
// file changed within mediacache.RacyWindow.
func age(t *testing.T, paths ...string) {
	t.Helper()
	old := time.Now().Add(-time.Hour)
	for _, p := range paths {
		if err := os.Chtimes(p, old, old); err != nil {
			t.Fatal(err)
		}
	}
}

// runCached runs RunWith with cache and returns the ops and what the cache did in this run.
func runCached(t *testing.T, cfg config.Config, cache *mediacache.Cache) (*fsops.Ops, mediacache.Stats) {
	t.Helper()
	before := cache.Stats()
	ops := fsops.New(false)
	if err := RunWith(cfg, ops, Options{Cache: cache}); err != nil {
		t.Fatalf("RunWith: %v", err)
	}
	after := cache.Stats()
	return ops, mediacache.Stats{Hits: after.Hits - before.Hits, Misses: after.Misses - before.Misses, Stored: after.Stored - before.Stored}
}

// With a cache, a later run reuses the probe and the peak of every unchanged file
// (ffmpeg and ffprobe then only answer "-version": any other call would fail), while a
// file re-encoded by a run is probed again, never served from its old entry.
func TestCacheReusesUnchangedFilesOnly(t *testing.T) {
	testutil.RequireFFmpeg(t)
	cfg, _ := newLibrary(t, false)
	quiet := filepath.Join(addSong(t, cfg, "A - Quiet", "song.mp3"), "song.mp3")
	quietMP3(t, quiet, "1")
	atTarget := filepath.Join(addSong(t, cfg, "B - At Target", "song.wav"), "song.wav")
	ffmpegAt(t, atTarget, "0.7079") // -3.0 dB: left alone
	silent := filepath.Join(addSong(t, cfg, "C - Silent", "song.wav"), "song.wav")
	testutil.FFmpeg(t, "-f", "lavfi", "-i", "anullsrc=r=44100:cl=stereo", "-t", "1", "-c:a", "pcm_s16le", silent)
	broken := filepath.Join(addSong(t, cfg, "D - Broken", "song.mp3"), "song.mp3")
	testutil.WriteFile(t, broken, "this is not audio")
	age(t, quiet, atTarget, silent, broken)

	cache := mediacache.Open(context.Background(), cfg.SongDir)
	if cache == nil {
		t.Fatal("no cache")
	}

	// Run 1: everything is scanned; the quiet file is re-encoded.
	ops, st := runCached(t, cfg, cache)
	if s := ops.Stats(); s.Replaced != 1 || s.Failed != 1 {
		t.Fatalf("run 1 stats = %+v, want 1 replaced (quiet) and 1 failed (broken)", s)
	}
	// Probes of 4 files, peaks of the 3 readable ones; nothing known yet.
	if st.Hits != 0 || st.Misses != 7 || st.Stored != 7 {
		t.Errorf("run 1 cache = %+v, want 0 hits, 7 misses, 7 stored", st)
	}

	// Run 2 without the media journal: only the tag the re-encode wrote stops another
	// encode, so a stale entry (untagged, -38 dB) would re-encode the quiet file again.
	if err := os.Remove(cfg.JournalPath()); err != nil {
		t.Fatal(err)
	}
	ops, st = runCached(t, cfg, cache)
	if s := ops.Stats(); s.Replaced != 0 || s.Failed != 1 {
		t.Errorf("run 2 stats = %+v, want nothing replaced and 1 failed (broken)", s)
	}
	// Hits: probe+peak of the two unchanged wavs, the broken file's probe. The re-encoded
	// file misses both (just written: nothing stored, racy).
	if st.Hits != 5 || st.Misses != 2 || st.Stored != 0 {
		t.Errorf("run 2 cache = %+v, want 5 hits, 2 misses, 0 stored", st)
	}

	// Run 3 with the re-encoded file aged: it is stored now.
	age(t, quiet)
	if _, st = runCached(t, cfg, cache); st.Misses != 2 || st.Stored != 2 {
		t.Errorf("run 3 cache = %+v, want 2 misses, 2 stored", st)
	}

	// Run 4: no real ffmpeg/ffprobe at all; every result comes from the cache, the broken
	// file still fails the same way.
	testutil.FakeVersions(t, "7.1", "7.1")
	logs := testutil.CaptureLogs(t)
	ops, st = runCached(t, cfg, cache)
	if s := ops.Stats(); s.Replaced != 0 || s.Failed != 1 {
		t.Errorf("run 4 stats = %+v, want nothing replaced and 1 failed (broken)", s)
	}
	if st.Hits != 7 || st.Misses != 0 {
		t.Errorf("run 4 cache = %+v, want 7 hits, 0 misses", st)
	}
	var brokenLogged bool
	for _, e := range logs.AllEntries() {
		if strings.HasPrefix(e.Message, "Skipping "+broken+": probing audio failed") {
			brokenLogged = true
		}
		if strings.Contains(e.Message, "measuring the peak failed") {
			t.Errorf("a peak was measured: %s", e.Message)
		}
	}
	if !brokenLogged {
		t.Error("the cached probe failure of the broken file was not logged as before")
	}
}

// A cached peak is used only for the same downmix, and silence (-Inf, which JSON cannot
// hold) survives the round trip.
func TestCachedPeak(t *testing.T) {
	testutil.RequireFFmpeg(t)
	dir := t.TempDir()
	silent := filepath.Join(dir, "silent.wav")
	testutil.FFmpeg(t, "-f", "lavfi", "-i", "anullsrc=r=44100:cl=stereo", "-t", "1", "-c:a", "pcm_s16le", silent)
	loud := filepath.Join(dir, "loud.wav")
	squareWAV(t, loud, "0.6683439175686147") // -3.5 dB
	age(t, silent, loud)
	cache := mediacache.Open(context.Background(), dir)

	for _, path := range []string{silent, loud} {
		if _, _, err := cachedPeak(cache, path, false); err != nil {
			t.Fatal(err)
		}
	}
	// The decoded length is stored with the peak.
	if _, d, err := cachedPeak(cache, silent, false); err != nil || d < 0.9 || d > 1.1 || cache.Stats().Hits == 0 {
		t.Errorf("cached decoded length = %v, %v (stats %+v); want 1 s from the cache", d, err, cache.Stats())
	}
	if st := cache.Stats(); st.Stored != 2 {
		t.Fatalf("stored = %d, want 2", st.Stored)
	}
	testutil.FakeVersions(t, "7.1", "7.1") // a measurement would fail now
	if p, _, err := cachedPeak(cache, silent, false); err != nil || !math.IsInf(p, -1) {
		t.Errorf("cached silent peak = %v, %v; want -Inf", p, err)
	}
	if p, _, err := cachedPeak(cache, loud, false); err != nil || p != -3.5 {
		t.Errorf("cached peak = %v, %v; want -3.5", p, err)
	}
	if _, _, err := cachedPeak(cache, loud, true); err == nil {
		t.Error("a peak measured without downmix was used for the downmix")
	}
	// Without a cache it measures (and fails with the fake ffmpeg).
	if _, _, err := cachedPeak(nil, loud, false); err == nil {
		t.Error("cachedPeak(nil) did not measure")
	}
}

// A peak measured while ffmpeg reported a read error of the file system (the decode may
// have stopped early) is used but not cached.
func TestCachedPeakReadErrorNotStored(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "a.wav")
	if err := os.WriteFile(path, []byte("audio"), 0o644); err != nil {
		t.Fatal(err)
	}
	age(t, path)
	for _, tt := range []struct {
		name, out string
		stored    int64
	}{
		// A read error: the peak may be that of a part of the file, so it is an error.
		{"read error", "a.wav: Input/output error\\nPeak level dB: -20.0\\n", 0},
		{"clean", "Peak level dB: -20.0\\n", 1},
	} {
		t.Run(tt.name, func(t *testing.T) {
			testutil.FakeTools(t, map[string]string{
				"ffmpeg": "#!/bin/sh\nif [ \"$1\" = -version ]; then echo 'ffmpeg version 7.1'; exit 0; fi\nprintf '" + tt.out + "' >&2\n",
			})
			cache := mediacache.Open(context.Background(), dir)
			p, _, err := cachedPeak(cache, path, false)
			if tt.stored == 0 && (err == nil || !strings.Contains(err.Error(), "read error")) {
				t.Fatalf("cachedPeak = %v, %v; want a read error", p, err)
			}
			if tt.stored == 1 && (err != nil || p != -20) {
				t.Fatalf("cachedPeak = %v, %v", p, err)
			}
			if st := cache.Stats(); st.Stored != tt.stored {
				t.Errorf("stats %+v, want %d stored", st, tt.stored)
			}
		})
	}
}
