package fixvideos

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/nkreimeyer/library-manager/fsops"
	"github.com/nkreimeyer/library-manager/internal/testutil"
	"github.com/nkreimeyer/library-manager/mediacache"
)

// age sets the mtime of the files to t: the cache only stores results of files whose mtime
// is at least mediacache.RacyWindow old.
func age(t *testing.T, when time.Time, files ...string) {
	t.Helper()
	for _, f := range files {
		if err := os.Chtimes(f, when, when); err != nil {
			t.Fatal(err)
		}
	}
}

// A video fixvideos replaced (audio stripped) or converted (avi -> mp4) is probed again on
// the next run, an unchanged one is not; on the run after that nothing is probed at all
// (ffprobe and ffmpeg only answer "-version" then, so a probe would fail the video).
func TestRunWithCacheReusesProbes(t *testing.T) {
	testutil.RequireFFmpeg(t)
	cfg, songDir, _ := newLibrary(t, false)

	stripped := filepath.Join(songDir, "A - Strip", "video.mp4")
	testutil.WriteFile(t, filepath.Join(songDir, "A - Strip", "song.txt"), "#TITLE:Strip\n#ARTIST:A\n#MP3:song.mp3\n#VIDEO:video.mp4\n")
	testutil.WriteFile(t, filepath.Join(songDir, "A - Strip", "song.mp3"), "not checked")
	makeMedia(t, stripped, "-c:v", "libx264", "-crf", "35", "-c:a", "aac")

	avi := filepath.Join(songDir, "B - Convert", "video.avi")
	testutil.WriteFile(t, filepath.Join(songDir, "B - Convert", "song.txt"), "#TITLE:Convert\n#ARTIST:B\n#MP3:video.avi\n#VIDEO:video.avi\n")
	makeMedia(t, avi, "-c:v", "mpeg4", "-q:v", "1", "-c:a", "ac3")
	converted := filepath.Join(songDir, "B - Convert", "video.mp4")

	unchanged := filepath.Join(songDir, "C - Keep", "v.mp4")
	if err := os.MkdirAll(filepath.Dir(unchanged), 0o755); err != nil {
		t.Fatal(err)
	}
	testutil.GenerateMedia(t, "-f", "lavfi", "-i", "testsrc=size=64x48:rate=10:duration=0.3", "-c:v", "libx264", "-an", unchanged)
	testutil.WriteFile(t, filepath.Join(songDir, "C - Keep", "song.txt"), "#TITLE:Keep\n#ARTIST:C\n#MP3:a.mp3\n#VIDEO:v.mp4\n")

	old := time.Now().Add(-time.Hour)
	age(t, old, stripped, avi, unchanged)

	c := mediacache.Open(context.Background(), songDir)
	if c == nil {
		t.Fatal("no cache")
	}
	ops := fsops.New(false)
	if err := RunWith(cfg, ops, Options{Cache: c}); err != nil {
		t.Fatal(err)
	}
	if st := ops.Stats(); st.Failed != 0 || st.Replaced != 1 || st.Removed != 1 {
		t.Fatalf("first run: Stats = %+v, want 1 replaced, 1 removed, no failure", st)
	}
	if !testutil.Exists(converted) || testutil.Exists(avi) {
		t.Fatalf("video.avi was not converted")
	}
	first := c.Stats()

	// The processed files are new (another size); their mtime is made old enough to store.
	age(t, old.Add(10*time.Minute), stripped, converted)
	ops = fsops.New(false)
	if err := RunWith(cfg, ops, Options{Cache: c}); err != nil {
		t.Fatal(err)
	}
	if st := ops.Stats(); st != (fsops.Stats{}) {
		t.Errorf("second run: Stats = %+v, want nothing done", st)
	}
	second := c.Stats()
	if hits, misses := second.Hits-first.Hits, second.Misses-first.Misses; hits != 1 || misses != 2 {
		t.Errorf("second run: %d hits, %d misses; want 1 hit (the unchanged video) and 2 misses (the processed ones)", hits, misses)
	}
	if err := c.Save(ops); err != nil {
		t.Fatal(err)
	}

	// The next run reads the saved cache; the tools can no longer probe anything.
	c = mediacache.Open(context.Background(), songDir)
	testutil.FakeVersions(t, "7.1", "7.1")
	ops = fsops.New(false)
	if err := RunWith(cfg, ops, Options{Cache: c}); err != nil {
		t.Fatal(err)
	}
	if st := ops.Stats(); st != (fsops.Stats{}) {
		t.Errorf("third run: Stats = %+v, want nothing done (a probe would have failed)", st)
	}
	if st := c.Stats(); st.Hits != 3 || st.Misses != 0 {
		t.Errorf("third run: cache stats = %+v, want 3 hits, 0 misses", st)
	}
}

// fakeMeasuredVideo installs fake tools whose ffprobe describes an h264 video without a
// stream bitrate (so plan measures the packet sizes) that needs no processing. Each call
// on a file is logged to the returned file.
func fakeMeasuredVideo(t *testing.T) (calls string) {
	t.Helper()
	calls = filepath.Join(t.TempDir(), "calls")
	version := func(name string) string {
		return "#!/bin/sh\nif [ \"$1\" = -version ]; then echo '" + name + " version 7.1 Copyright (c) the FFmpeg developers'; exit 0; fi\n"
	}
	testutil.FakeTools(t, map[string]string{
		"ffprobe": version("ffprobe") +
			"case \"$*\" in *packet=size*) echo packets >> '" + calls + "'; echo 1000; echo 1000; exit 0;; esac\n" +
			"echo probe >> '" + calls + "'\n" +
			`echo '{"streams":[{"codec_type":"video","codec_name":"h264","width":64,"height":48,"r_frame_rate":"10/1"}],"format":{"duration":"3.0"}}'` + "\n",
		"ffmpeg": version("ffmpeg") + "echo ffmpeg >> '" + calls + "'\nexit 1\n",
	})
	return calls
}

// The packet-size measurement is cached as kindPackets: with a cache, a second run neither
// probes nor measures the unchanged video; without one, every run does both.
func TestRunWithCacheReusesPacketBitrate(t *testing.T) {
	calls := fakeMeasuredVideo(t)
	cfg, songDir, _ := newLibrary(t, false)
	song := filepath.Join(songDir, "A - S")
	testutil.WriteFile(t, filepath.Join(song, "song.txt"), "#TITLE:S\n#ARTIST:A\n#MP3:a.mp3\n#VIDEO:v.mkv\n")
	video := filepath.Join(song, "v.mkv")
	testutil.WriteFile(t, video, "video")
	age(t, time.Now().Add(-time.Hour), video)

	run := func(c *mediacache.Cache) string {
		t.Helper()
		if err := os.Remove(calls); err != nil && !os.IsNotExist(err) {
			t.Fatal(err)
		}
		ops := fsops.New(false)
		if err := RunWith(cfg, ops, Options{Cache: c}); err != nil {
			t.Fatal(err)
		}
		if st := ops.Stats(); st != (fsops.Stats{}) {
			t.Errorf("Stats = %+v, want nothing done", st)
		}
		if !testutil.Exists(calls) {
			return ""
		}
		return strings.Join(strings.Fields(testutil.ReadFile(t, calls)), " ")
	}

	// No cache: unchanged behaviour, every run probes and measures.
	for i := range 2 {
		if got := run(nil); got != "probe packets" {
			t.Errorf("run %d without a cache: calls %q, want \"probe packets\"", i+1, got)
		}
	}

	c := mediacache.Open(context.Background(), songDir)
	if got := run(c); got != "probe packets" {
		t.Errorf("first cached run: calls %q, want \"probe packets\"", got)
	}
	if got := run(c); got != "" {
		t.Errorf("second cached run: calls %q, want none", got)
	}
	if st := c.Stats(); st.Hits != 2 || st.Stored != 2 {
		t.Errorf("cache stats = %+v, want 2 hits (probe, packets) and 2 stored", st)
	}

	// A changed video is probed and measured again.
	testutil.WriteFile(t, video, "other video")
	age(t, time.Now().Add(-time.Hour), video)
	if got := run(c); got != "probe packets" {
		t.Errorf("run after a change: calls %q, want \"probe packets\"", got)
	}
}

// A failed packet measurement is not cached: the next run measures again.
func TestPacketMeasurementFailureIsNotCached(t *testing.T) {
	calls := filepath.Join(t.TempDir(), "calls")
	version := func(name string) string {
		return "#!/bin/sh\nif [ \"$1\" = -version ]; then echo '" + name + " version 7.1 Copyright (c) the FFmpeg developers'; exit 0; fi\n"
	}
	testutil.FakeTools(t, map[string]string{
		"ffprobe": version("ffprobe") + "case \"$*\" in *packet=size*) echo packets >> '" + calls + "'; exit 1;; esac\n" +
			`echo '{"streams":[{"codec_type":"video","codec_name":"h264","width":64,"height":48,"r_frame_rate":"10/1"}],"format":{"duration":"3.0"}}'` + "\n",
		"ffmpeg": version("ffmpeg") + "exit 1\n",
	})
	dir := t.TempDir()
	video := filepath.Join(dir, "v.mkv")
	testutil.WriteFile(t, video, "video")
	age(t, time.Now().Add(-time.Hour), video)
	c := mediacache.Open(context.Background(), dir)
	probe, err := mediacache.Probe(context.Background(), c, video)
	if err != nil {
		t.Fatal(err)
	}
	for range 2 {
		if _, err := videoBitrate(c, video, probe); err == nil {
			t.Error("videoBitrate succeeded, want the ffprobe failure")
		}
	}
	if got := strings.Count(testutil.ReadFile(t, calls), "packets"); got != 2 {
		t.Errorf("packet measurements = %d, want 2 (a failure is not cached)", got)
	}
}

// A packet list that ended early on a read error of the file system (ffprobe exit 0 with
// "Input/output error") is not cached: its sum would be too low until the file changes.
func TestPacketMeasurementReadErrorIsNotCached(t *testing.T) {
	version := func(name string) string {
		return "#!/bin/sh\nif [ \"$1\" = -version ]; then echo '" + name + " version 7.1 Copyright (c) the FFmpeg developers'; exit 0; fi\n"
	}
	testutil.FakeTools(t, map[string]string{
		"ffprobe": version("ffprobe") + "case \"$*\" in *packet=size*) echo 1000; echo 'v.mkv: Input/output error' >&2; exit 0;; esac\n" +
			`echo '{"streams":[{"codec_type":"video","codec_name":"h264","width":64,"height":48,"r_frame_rate":"10/1"}],"format":{"duration":"3.0"}}'` + "\n",
		"ffmpeg": version("ffmpeg") + "exit 1\n",
	})
	dir := t.TempDir()
	video := filepath.Join(dir, "v.mkv")
	testutil.WriteFile(t, video, "video")
	age(t, time.Now().Add(-time.Hour), video)
	c := mediacache.Open(context.Background(), dir)
	probe, err := mediacache.Probe(context.Background(), c, video)
	if err != nil || c.Stats().Stored != 1 {
		t.Fatalf("probe: %v, stats %+v", err, c.Stats())
	}
	if _, err := videoBitrate(c, video, probe); err != nil {
		t.Fatal(err)
	}
	if st := c.Stats(); st.Stored != 1 {
		t.Errorf("stats %+v, want only the probe stored", st)
	}
}
