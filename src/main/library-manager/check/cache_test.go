package check

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/nkreimeyer/library-manager/config"
	"github.com/nkreimeyer/library-manager/fsops"
	"github.com/nkreimeyer/library-manager/internal/testutil"
	"github.com/nkreimeyer/library-manager/mediacache"
)

// ageFiles sets the mtime of the files to an old time, so that the media cache stores
// their results (files changed within mediacache.RacyWindow are never stored).
func ageFiles(t *testing.T, when time.Time, paths ...string) {
	t.Helper()
	for _, p := range paths {
		if err := os.Chtimes(p, when, when); err != nil {
			t.Fatal(err)
		}
	}
}

var longAgo = time.Date(2020, 1, 2, 3, 4, 5, 0, time.UTC)

// countingTools puts a fake ffprobe (and ffmpeg) on PATH that answer -version and append a
// line to the returned file for every other run. probe and decode are the script bodies
// for those runs (decode "" = no ffmpeg).
func countingTools(t *testing.T, probe, decode string) (count func() int, dir string) {
	t.Helper()
	log := filepath.Join(t.TempDir(), "runs")
	tool := func(name, body string) string {
		return "if [ \"$1\" = -version ]; then echo '" + name + " version 7.1 Copyright (c) the FFmpeg developers'; exit 0; fi\n" +
			"echo " + name + " >> '" + log + "'\n" + body
	}
	scripts := map[string]string{"ffprobe": tool("ffprobe", probe)}
	if decode != "" {
		scripts["ffmpeg"] = tool("ffmpeg", decode)
	}
	dir = fakeTools(t, scripts)
	return func() int {
		data, err := os.ReadFile(log)
		if os.IsNotExist(err) {
			return 0
		}
		if err != nil {
			t.Fatal(err)
		}
		return strings.Count(string(data), "\n")
	}, dir
}

// cacheLib is a library with one song (S) whose audio file is old enough to be cached.
func cacheLib(t *testing.T) (lib, folder, audio string) {
	t.Helper()
	lib = t.TempDir()
	testutil.WriteFiles(t, lib, map[string]string{"S/song.txt": song("a.mp3", 10), "S/a.mp3": "x"})
	folder, audio = filepath.Join(lib, "S"), filepath.Join(lib, "S", "a.mp3")
	ageFiles(t, longAgo, audio)
	return lib, folder, audio
}

func newCachedInspector(t *testing.T, ctx context.Context, ops *fsops.Ops, deep bool, c *mediacache.Cache) *Inspector {
	t.Helper()
	insp, _ := NewInspector(ctx, ops, deep)
	if !insp.MediaChecked() || insp.DeepChecked() != deep {
		t.Fatalf("media %v, deep %v", insp.MediaChecked(), insp.DeepChecked())
	}
	insp.UseCache(c)
	return insp
}

// A second inspection with the same cache runs no ffprobe/ffmpeg: with the tools removed
// from PATH it gives the same Inspection and no failure. It also works through a saved and
// reopened cache file (the JSON round trip of mediaInfo).
func TestInspectCacheReal(t *testing.T) {
	testutil.RequireFFmpeg(t)
	lib := t.TempDir()
	dir := filepath.Join(lib, "S")
	mp3, mp4 := filepath.Join(dir, "a.mp3"), filepath.Join(dir, "v.mp4")
	testutil.GenerateMediaAt(t, mp3, "-f", "lavfi", "-i", "sine=d=2", "-c:a", "libmp3lame", "-b:a", "192k")
	testutil.GenerateMediaAt(t, mp4, "-f", "lavfi", "-i", "testsrc=d=2:s=96x64:r=10", "-c:v", "libx264", "-pix_fmt", "yuv420p")
	// The song is longer than its audio, so the cached durations show up in the findings.
	testutil.WriteFiles(t, lib, map[string]string{"S/song.txt": "#VIDEO:v.mp4\n" + song("a.mp3", 200)})
	ageFiles(t, longAgo, mp3, mp4)
	realPath := os.Getenv("PATH")

	ops := fsops.New(false)
	c := mediacache.Open(context.Background(), lib)
	if c == nil {
		t.Fatal("no cache")
	}
	want, err := newCachedInspector(t, context.Background(), ops, true, c).Inspect(dir)
	if err != nil || !want.Media.Probed || !want.Media.HasVideo || len(want.Findings) == 0 {
		t.Fatalf("Inspect = %+v, %v", want, err)
	}
	if st := c.Stats(); st != (mediacache.Stats{Misses: 4, Stored: 4}) {
		t.Fatalf("stats after the first inspection = %+v, want 2 files x (media, decode) probed and stored", st)
	}

	insp := newCachedInspector(t, context.Background(), ops, true, c)
	t.Setenv("PATH", t.TempDir()) // no ffprobe/ffmpeg: every run would fail
	got, err := insp.Inspect(dir)
	if err != nil || !reflect.DeepEqual(got, want) {
		t.Errorf("cached Inspect = %+v, %v\nwant %+v", got, err, want)
	}
	if st := c.Stats(); st.Hits != 4 || st.Misses != 4 {
		t.Errorf("stats = %+v, want 4 hits", st)
	}
	if st := ops.Stats(); st.Failed != 0 {
		t.Errorf("ops stats = %+v, want no tool run (and so no failure)", st)
	}

	// Saved and reopened (the tool versions must be the same: real PATH for Open).
	if err := c.Save(ops); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", realPath)
	c2 := mediacache.Open(context.Background(), lib)
	insp = newCachedInspector(t, context.Background(), ops, true, c2)
	t.Setenv("PATH", t.TempDir())
	got, err = insp.Inspect(dir)
	if err != nil || !reflect.DeepEqual(got, want) {
		t.Errorf("Inspect from the reopened cache = %+v, %v\nwant %+v", got, err, want)
	}
	if st := c2.Stats(); st != (mediacache.Stats{Hits: 4}) {
		t.Errorf("reopened stats = %+v, want 4 hits", st)
	}
	if st := ops.Stats(); st.Failed != 0 {
		t.Errorf("ops stats = %+v, want no failure", st)
	}

	// A changed file is probed again; the unchanged video still comes from the cache.
	t.Setenv("PATH", realPath)
	testutil.GenerateMediaAt(t, mp3, "-y", "-f", "lavfi", "-i", "sine=d=3", "-c:a", "libmp3lame", "-b:a", "128k")
	ageFiles(t, longAgo.Add(time.Hour), mp3)
	got, err = newCachedInspector(t, context.Background(), ops, true, c2).Inspect(dir)
	if err != nil || got.Media.AudioBitrate != 128000 {
		t.Errorf("Inspect after the change = %+v, %v, want the new audio bitrate", got.Media, err)
	}
	if st := c2.Stats(); st != (mediacache.Stats{Hits: 6, Misses: 2, Stored: 2}) {
		t.Errorf("stats = %+v, want the changed mp3 probed and decoded again, the video reused", st)
	}
}

func TestInspectCacheFakeTools(t *testing.T) {
	t.Run("probe result reused until the file changes", func(t *testing.T) {
		count, _ := countingTools(t, printJSON(audioJSON, ""), "")
		lib, folder, audio := cacheLib(t)
		c := mediacache.Open(context.Background(), lib)
		insp := newCachedInspector(t, context.Background(), fsops.New(false), false, c)
		first, err := insp.Inspect(folder)
		if err != nil || !first.Media.Probed || first.Media.AudioBitrate != 128000 || count() != 1 {
			t.Fatalf("Inspect = %+v, %v, %d runs", first, err, count())
		}
		second, err := insp.Inspect(folder)
		if err != nil || !reflect.DeepEqual(first, second) || count() != 1 {
			t.Errorf("second Inspect = %+v, %v, %d runs, want the cached result", second, err, count())
		}
		ageFiles(t, longAgo.Add(time.Second), audio)
		if _, err := insp.Inspect(folder); err != nil || count() != 2 {
			t.Errorf("Inspect after an mtime change: %v, %d runs, want a new probe", err, count())
		}
	})

	t.Run("without a cache every inspection probes", func(t *testing.T) {
		count, _ := countingTools(t, printJSON(audioJSON, ""), "")
		_, folder, _ := cacheLib(t)
		insp := newCachedInspector(t, context.Background(), fsops.New(false), false, nil)
		for range 2 {
			if _, err := insp.Inspect(folder); err != nil {
				t.Fatal(err)
			}
		}
		if count() != 2 {
			t.Errorf("%d runs, want 2", count())
		}
	})

	t.Run("a file ffprobe cannot read stays unreadable", func(t *testing.T) {
		count, _ := countingTools(t, "echo 'Invalid data found when processing input' >&2\nexit 1\n", "")
		lib, folder, _ := cacheLib(t)
		c := mediacache.Open(context.Background(), lib)
		insp := newCachedInspector(t, context.Background(), fsops.New(false), false, c)
		first, err := insp.Inspect(folder)
		if err != nil || !first.Broken || !reflect.DeepEqual(codes(first.Findings), []string{"error:unreadable-media"}) {
			t.Fatalf("Inspect = %+v, %v", first, err)
		}
		second, err := insp.Inspect(folder)
		if err != nil || !reflect.DeepEqual(first, second) || count() != 1 {
			t.Errorf("second Inspect = %+v, %v, %d runs, want the cached failure", second, err, count())
		}
	})

	t.Run("unparsable output is not cached", func(t *testing.T) {
		count, _ := countingTools(t, "echo nope\n", "")
		lib, folder, _ := cacheLib(t)
		c := mediacache.Open(context.Background(), lib)
		insp := newCachedInspector(t, context.Background(), fsops.New(false), false, c)
		for range 2 {
			if _, err := insp.Inspect(folder); err != nil {
				t.Fatal(err)
			}
		}
		if count() != 2 || c.Stats().Stored != 0 {
			t.Errorf("%d runs, stats %+v, want no stored result", count(), c.Stats())
		}
	})

	t.Run("a tool failure is not cached", func(t *testing.T) {
		_, dir := countingTools(t, printJSON(audioJSON, ""), "")
		lib, folder, _ := cacheLib(t)
		c := mediacache.Open(context.Background(), lib)
		ops := fsops.New(false)
		insp := newCachedInspector(t, context.Background(), ops, false, c)
		writeBrokenTool(t, dir, "ffprobe") // found, but cannot be started
		for range 2 {
			if res, err := insp.Inspect(folder); err != nil || res.Media.Probed {
				t.Fatalf("Inspect = %+v, %v", res.Media, err)
			}
		}
		if st := c.Stats(); st != (mediacache.Stats{Misses: 2}) || ops.Stats().Failed != 2 {
			t.Errorf("cache stats %+v, ops stats %+v, want two misses and two failures", st, ops.Stats())
		}
	})

	t.Run("an estimated duration is not cached when ffmpeg cannot start", func(t *testing.T) {
		count, dir := countingTools(t, printJSON(audioJSON, estimatedMsg), "")
		writeBrokenTool(t, dir, "ffmpeg")
		lib, folder, _ := cacheLib(t)
		c := mediacache.Open(context.Background(), lib)
		insp := newCachedInspector(t, context.Background(), fsops.New(false), false, c)
		for range 2 {
			if _, err := insp.Inspect(folder); err != nil {
				t.Fatal(err)
			}
		}
		if count() != 2 || c.Stats().Stored != 0 {
			t.Errorf("%d probes, stats %+v, want none stored", count(), c.Stats())
		}
	})

	t.Run("a stopped inspection stores nothing and uses no cached result", func(t *testing.T) {
		sleep, err := exec.LookPath("sleep") // before PATH holds only the fake tools
		if err != nil {
			t.Skip("no sleep")
		}
		count, _ := countingTools(t, "exec '"+sleep+"' 10\n", "")
		lib, folder, _ := cacheLib(t)
		c := mediacache.Open(context.Background(), lib)
		ctx, cancel := context.WithCancel(context.Background())
		ops := fsops.New(false)
		insp := newCachedInspector(t, ctx, ops, false, c)
		time.AfterFunc(300*time.Millisecond, cancel)
		res, err := insp.Inspect(folder)
		if err != nil || res.Media.Probed || count() != 1 {
			t.Fatalf("stopped Inspect = %+v, %v, %d runs", res, err, count())
		}
		if st := c.Stats(); st.Stored != 0 || ops.Stats().Failed != 0 {
			t.Errorf("cache %+v, ops %+v, want nothing stored and no failure", st, ops.Stats())
		}
	})

	t.Run("a done ctx ignores cached results", func(t *testing.T) {
		count, _ := countingTools(t, printJSON(audioJSON, ""), "")
		lib, folder, _ := cacheLib(t)
		c := mediacache.Open(context.Background(), lib)
		if _, err := newCachedInspector(t, context.Background(), fsops.New(false), false, c).Inspect(folder); err != nil || count() != 1 {
			t.Fatalf("%v, %d runs", err, count())
		}
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		res, err := newCachedInspector(t, ctx, fsops.New(false), false, c).Inspect(folder)
		if err != nil || res.Media.Probed || count() != 1 {
			t.Errorf("Inspect with a done ctx = %+v, %v, %d runs, want no media result", res.Media, err, count())
		}
	})

	t.Run("deep decode results are cached separately", func(t *testing.T) {
		count, _ := countingTools(t, printJSON(audioJSON, ""), "echo '[mp3 @ 0x1234] bad frame' >&2\nexit 0\n")
		lib, folder, _ := cacheLib(t)
		c := mediacache.Open(context.Background(), lib)
		// Without -deep only the probe is stored.
		if _, err := newCachedInspector(t, context.Background(), fsops.New(false), false, c).Inspect(folder); err != nil || count() != 1 {
			t.Fatalf("%v, %d runs", err, count())
		}
		insp := newCachedInspector(t, context.Background(), fsops.New(false), true, c)
		first, err := insp.Inspect(folder)
		if err != nil || !reflect.DeepEqual(codes(first.Findings), []string{"warning:decode-warning"}) || count() != 2 {
			t.Fatalf("deep Inspect = %+v, %v, %d runs, want the decode (only) run", first, err, count())
		}
		second, err := insp.Inspect(folder)
		if err != nil || !reflect.DeepEqual(first, second) || count() != 2 {
			t.Errorf("second deep Inspect = %+v, %v, %d runs, want both results cached", second, err, count())
		}
		if st := c.Stats(); st != (mediacache.Stats{Hits: 3, Misses: 2, Stored: 2}) {
			t.Errorf("stats = %+v", st)
		}
	})

	t.Run("a deep decode tool failure is not cached", func(t *testing.T) {
		count, dir := countingTools(t, printJSON(audioJSON, ""), "exit 0\n")
		lib, folder, _ := cacheLib(t)
		c := mediacache.Open(context.Background(), lib)
		ops := fsops.New(false)
		insp := newCachedInspector(t, context.Background(), ops, true, c)
		writeBrokenTool(t, dir, "ffmpeg")
		if _, err := insp.Inspect(folder); err != nil || ops.Stats().Failed != 1 {
			t.Fatalf("%v, ops %+v", err, ops.Stats())
		}
		if st := c.Stats(); st.Stored != 1 || count() != 1 {
			t.Errorf("stats %+v, %d runs, want only the probe stored", st, count())
		}
	})

	t.Run("Run uses Options.Cache", func(t *testing.T) {
		count, _ := countingTools(t, printJSON(audioJSON, ""), "")
		lib, _, _ := cacheLib(t)
		c := mediacache.Open(context.Background(), lib)
		cfg, err := config.New(lib, t.TempDir(), false)
		if err != nil {
			t.Fatal(err)
		}
		for range 2 {
			if err := Run(cfg, fsops.New(false), Options{Cache: c}); err != nil {
				t.Fatal(err)
			}
		}
		if count() != 1 || c.Stats().Hits != 1 {
			t.Errorf("%d runs, stats %+v, want the second Run served from the cache", count(), c.Stats())
		}
	})
}

// A failure that is not about the file (the tool killed by a signal, a read error of a NAS
// share) is not cached: the next inspection runs the tool again and finds the file fine.
func TestInspectCacheTransientFailures(t *testing.T) {
	const ioErr = "echo 'a.mp3: Input/output error' >&2\n"
	for _, tt := range []struct {
		name, probe, decode string
		deep                bool
		stored              int64 // results stored by the first inspection
	}{
		{"ffprobe killed", "kill -9 $$\n", "", false, 0},
		{"ffprobe read error", ioErr + "exit 1\n", "", false, 0},
		{"ffprobe read error, exit 0", ioErr + printJSON(audioJSON, ""), "", false, 0},
		{"duration measurement killed", printJSON(audioJSON, estimatedMsg), "kill -9 $$\n", false, 0},
		{"duration measurement read error", printJSON(audioJSON, estimatedMsg), ioErr + "exit 1\n", false, 0},
		{"deep decode killed", printJSON(audioJSON, ""), "kill -9 $$\n", true, 1},
		{"deep decode read error", printJSON(audioJSON, ""), ioErr + "exit 1\n", true, 1},
		{"deep decode read error, exit 0", printJSON(audioJSON, ""), ioErr + "exit 0\n", true, 1},
		// The control cases: failures about the file are cached.
		{"ffprobe file error", "echo 'Invalid data found when processing input' >&2\nexit 1\n", "", false, 1},
		{"duration measurement file error", printJSON(audioJSON, estimatedMsg), "echo 'Header missing' >&2\nexit 1\n", false, 1},
		{"deep decode file error", printJSON(audioJSON, ""), "echo 'Invalid data found' >&2\nexit 1\n", true, 2},
	} {
		t.Run(tt.name, func(t *testing.T) {
			countingTools(t, tt.probe, tt.decode)
			lib, folder, _ := cacheLib(t)
			c := mediacache.Open(context.Background(), lib)
			insp := newCachedInspector(t, context.Background(), fsops.New(false), tt.deep, c)
			if _, err := insp.Inspect(folder); err != nil {
				t.Fatal(err)
			}
			if st := c.Stats(); st.Stored != tt.stored {
				t.Errorf("stats %+v, want %d stored", st, tt.stored)
			}
		})
	}
}
