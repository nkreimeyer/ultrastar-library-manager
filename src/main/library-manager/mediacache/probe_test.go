package mediacache

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"github.com/nkreimeyer/library-manager/mediatools"
)

func requireFFmpeg(t *testing.T) {
	t.Helper()
	for _, tool := range []string{"ffmpeg", "ffprobe"} {
		if _, err := exec.LookPath(tool); err != nil {
			t.Skipf("%s not found", tool)
		}
	}
}

// noTools makes ffprobe impossible to run, so only cached results can answer.
func noTools(t *testing.T) { t.Setenv("PATH", t.TempDir()) }

func TestProbeCachesInfoWithLog(t *testing.T) {
	requireFFmpeg(t)
	root, clk := t.TempDir(), newClock()
	f := filepath.Join(root, "s", "song.mp3")
	if err := os.MkdirAll(filepath.Dir(f), 0o755); err != nil {
		t.Fatal(err)
	}
	out, err := exec.Command("ffmpeg", "-v", "error", "-f", "lavfi", "-i", "sine=frequency=440:duration=1",
		"-c:a", "libmp3lame", "-b:a", "64k", f).CombinedOutput()
	if err != nil {
		t.Skipf("ffmpeg cannot write mp3: %v: %s", err, out)
	}
	setMtime(t, f, clk.now().Add(-time.Hour))
	ctx := context.Background()
	c := openTest(t, root, clk)
	want, err := Probe(ctx, c, f)
	if err != nil {
		t.Fatal(err)
	}
	want.Log = "a warning" // Log must round-trip: set it in the stored entry
	c.mu.Lock()
	delete(c.folders["s"].Files["song.mp3"].Results, KindProbe)
	c.mu.Unlock()
	var r probeResult
	st, _ := c.Lookup(f, KindProbe, &r)
	stored := want
	c.Store(st, KindProbe, probeResult{Info: &stored, Log: want.Log})
	save(t, c)

	noTools(t)
	c2 := openTest(t, root, clk)
	got, err := Probe(ctx, c2, f)
	if err != nil {
		t.Fatalf("cached probe: %v", err)
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("cached probe\n got %+v\nwant %+v", got, want)
	}
	if !got.HasStream("audio") {
		t.Error("no audio stream in the cached result")
	}
	if c2.Stats().Hits != 1 {
		t.Errorf("stats %+v", c2.Stats())
	}
	// A stopped run gets ctx.Err() even for a cached file.
	stopped, cancel := context.WithCancel(ctx)
	cancel()
	if _, err := Probe(stopped, c2, f); !errors.Is(err, context.Canceled) {
		t.Errorf("stopped: %v", err)
	}
}

func TestProbeErrorRoundTrip(t *testing.T) {
	requireFFmpeg(t)
	root, clk := t.TempDir(), newClock()
	f := filepath.Join(root, "s", "broken.mp4")
	writeOld(t, clk, f, "this is no video at all")
	ctx := context.Background()
	c := openTest(t, root, clk)
	info1, err1 := Probe(ctx, c, f)
	var pe1 *mediatools.ProbeError
	if !errors.As(err1, &pe1) {
		t.Fatalf("not a ProbeError: %v", err1)
	}
	save(t, c)

	noTools(t)
	c2 := openTest(t, root, clk)
	info2, err2 := Probe(ctx, c2, f)
	var pe2 *mediatools.ProbeError
	if !errors.As(err2, &pe2) {
		t.Fatalf("cached: not a ProbeError: %v", err2)
	}
	if pe2.Err.Error() != pe1.Err.Error() || pe2.Stderr != pe1.Stderr || err2.Error() != err1.Error() {
		t.Errorf("cached error %q (%q), want %q (%q)", err2, pe2.Err, err1, pe1.Err)
	}
	if info2.Log != info1.Log || info2.Log == "" {
		t.Errorf("log %q, want %q", info2.Log, info1.Log)
	}
	if errors.Is(err2, mediatools.ErrNotRun) {
		t.Error("cached error reads as ErrNotRun")
	}
}

// A probe that could not run, or was stopped, is never cached.
func TestProbeNotRunOrStoppedNotCached(t *testing.T) {
	root, clk := t.TempDir(), newClock()
	f := filepath.Join(root, "s", "song.mp3")
	writeOld(t, clk, f, "audio")
	c := openTest(t, root, clk)

	t.Run("not run", func(t *testing.T) {
		noTools(t)
		if _, err := Probe(context.Background(), c, f); !errors.Is(err, mediatools.ErrNotRun) {
			t.Fatalf("err %v", err)
		}
	})
	t.Run("stopped", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		if _, err := Probe(ctx, c, f); !errors.Is(err, context.Canceled) {
			t.Fatalf("err %v", err)
		}
	})
	if c.Stats().Stored != 0 {
		t.Errorf("stored %d results", c.Stats().Stored)
	}
	var r probeResult
	if _, ok := c.Lookup(f, KindProbe, &r); ok {
		t.Error("a result was cached")
	}
}

// A stop during the probe (ffprobe killed) is not cached either: the stop is checked
// after the probe, whatever it returned.
func TestProbeStoppedDuringRunNotCached(t *testing.T) {
	root, clk := t.TempDir(), newClock()
	dir := t.TempDir()
	script := filepath.Join(dir, "ffprobe")
	if err := os.WriteFile(script, []byte("#!/bin/sh\nexec sleep 10\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	if _, err := exec.LookPath("sleep"); err != nil {
		t.Skip("no sleep")
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	f := filepath.Join(root, "s", "song.mp3")
	writeOld(t, clk, f, "audio")
	c := openTest(t, root, clk)
	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	if _, err := Probe(ctx, c, f); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("err %v", err)
	}
	if c.Stats().Stored != 0 {
		t.Error("stopped probe cached")
	}
}

// Without a cache Probe is mediatools.Probe.
func TestProbeNilCache(t *testing.T) {
	requireFFmpeg(t)
	f := filepath.Join(t.TempDir(), "broken.mp4")
	if err := os.WriteFile(f, []byte("junk"), 0o644); err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	_, want := mediatools.Probe(ctx, f)
	_, got := Probe(ctx, nil, f)
	var wantPE, gotPE *mediatools.ProbeError
	if !errors.As(got, &gotPE) || !errors.As(want, &wantPE) || gotPE.Err.Error() != wantPE.Err.Error() {
		t.Errorf("got %v, want %v", got, want)
	}
}

// fakeProbe puts a fake ffprobe running the shell script body first on PATH (and keeps
// the rest of PATH, for sleep, kill and cat).
func fakeProbe(t *testing.T, body string) {
	t.Helper()
	if _, err := exec.LookPath("sh"); err != nil {
		t.Skip("no sh")
	}
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "ffprobe"), []byte("#!/bin/sh\n"+body), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
}

// A failure that is not about the file (ffprobe killed by a signal, a read error of a NAS)
// is not cached: the next run probes again and finds the file fine. A failure about the
// file is cached (the control case).
func TestProbeTransientFailureNotCached(t *testing.T) {
	for _, tt := range []struct {
		name, body string
		stored     bool
	}{
		{"killed by a signal", "kill -9 $$\n", false},
		{"I/O error", "echo 'song.mp3: Input/output error' >&2\nexit 1\n", false},
		{"success with an I/O error", "echo 'song.mp3: Input/output error' >&2\necho '{\"streams\":[],\"format\":{}}'\n", false},
		{"file error", "echo 'song.mp3: Invalid data found when processing input' >&2\nexit 1\n", true},
		{"success", "echo '{\"streams\":[],\"format\":{}}'\n", true},
	} {
		t.Run(tt.name, func(t *testing.T) {
			root, clk := t.TempDir(), newClock()
			f := filepath.Join(root, "s", "song.mp3")
			writeOld(t, clk, f, "audio")
			fakeProbe(t, tt.body)
			c := openTest(t, root, clk)
			_, _ = Probe(context.Background(), c, f)
			if got := c.Stats().Stored == 1; got != tt.stored {
				t.Errorf("stored %v (stats %+v), want %v", got, c.Stats(), tt.stored)
			}
		})
	}
}
