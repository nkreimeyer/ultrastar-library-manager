package fixvideos

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/nkreimeyer/library-manager/fsops"
	"github.com/nkreimeyer/library-manager/internal/testutil"
	"github.com/nkreimeyer/library-manager/mediacache"
	"github.com/nkreimeyer/library-manager/mediatools"
	log "github.com/sirupsen/logrus"
)

// A panic while one video is processed is recovered, logged with the file, counted as a
// failure, and the other videos are still processed.
func TestRunRecoversPanicInOneVideo(t *testing.T) {
	testutil.FakeVersions(t, "7.1", "7.1")
	hook := testutil.CaptureLogsAt(t, log.InfoLevel)
	cfg, songDir, _ := newLibrary(t, false)
	for _, n := range []string{"A - Boom", "B - Other"} {
		testutil.WriteFiles(t, songDir, map[string]string{
			n + "/song.txt": "#TITLE:S\n#ARTIST:A\n#VIDEO:v.mp4\n",
			n + "/v.mp4":    "video",
		})
	}
	boom := filepath.Join(songDir, "A - Boom", "v.mp4")

	var mu sync.Mutex
	var seen []string
	orig := videoProcessor
	videoProcessor = func(p *processor, dir, file string) {
		mu.Lock()
		seen = append(seen, file)
		mu.Unlock()
		if file == boom {
			panic("boom")
		}
		// the other video is only recorded: Failed is then exactly the panic
	}
	t.Cleanup(func() { videoProcessor = orig })

	ops := fsops.New(false)
	_ = RunWith(cfg, ops, Options{})
	if f := ops.Stats().Failed; f != 1 {
		t.Errorf("Failed = %d, want 1 (the panic)", f)
	}
	found := false
	for _, e := range hook.AllEntries() {
		found = found || e.Message == "fixvideos: "+boom+" not finished: internal error (logged above)"
	}
	if !found {
		t.Error("panic not logged with the file")
	}
	if len(seen) != 2 {
		t.Errorf("processed videos = %q, want both", seen)
	}
}

// A temp file that cannot be created (read-only folder) fails the video; ffmpeg is not run
// and nothing changes.
func TestEncodeTempFileFails(t *testing.T) {
	testutil.SkipIfRoot(t)
	hook := testutil.CaptureLogsAt(t, log.InfoLevel)
	p, songDir, _ := newProcessor(t, false)
	dir := filepath.Join(songDir, "A - S")
	video := filepath.Join(dir, "v.avi")
	testutil.WriteFile(t, video, "video")
	before := testutil.Snapshot(t, dir)
	if err := os.Chmod(dir, 0o555); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.Chmod(dir, 0o755) })

	p.encode(dir, video, encodePlan{reEncode: true})
	if st := p.ops.Stats(); st.Failed != 1 || st.Replaced != 0 {
		t.Errorf("stats = %+v, want one failure", st)
	}
	found := false
	for _, e := range hook.AllEntries() {
		found = found || strings.Contains(e.Message, "creating temp file for "+video)
	}
	if !found {
		t.Error("failure not logged")
	}
	os.Chmod(dir, 0o755)
	if after := testutil.Snapshot(t, dir); after != before {
		t.Errorf("folder changed:\n%s\n%s", before, after)
	}
}

// An unknown resolution makes the size estimate fail with the resolution error, so the
// codec alone decides.
func TestEstimatedReEncodedSizeInvalidResolution(t *testing.T) {
	probe := mediatools.ProbeInfo{
		Streams: []mediatools.Stream{{CodecType: "video", Width: 0, Height: 480, RFrameRate: "25/1"}},
		Format:  mediatools.Format{Duration: "10.0"},
	}
	if _, err := estimatedReEncodedSize(probe); err == nil || !strings.Contains(err.Error(), "invalid resolution 0x480") {
		t.Errorf("err = %v, want invalid resolution 0x480", err)
	}
	probe.Streams[0].Width = 640
	if got, err := estimatedReEncodedSize(probe); err != nil || got <= 0 {
		t.Errorf("valid probe: %d, %v", got, err)
	}
}

// A container without a usable duration makes the packet bitrate measurement fail before
// ffprobe is run, and nothing is cached.
func TestVideoBitrateWithoutDurationRunsNothingAndCachesNothing(t *testing.T) {
	calls := filepath.Join(t.TempDir(), "calls")
	version := func(name string) string {
		return "#!/bin/sh\nif [ \"$1\" = -version ]; then echo '" + name + " version 7.1 Copyright (c) the FFmpeg developers'; exit 0; fi\n"
	}
	testutil.FakeTools(t, map[string]string{
		"ffprobe": version("ffprobe") + "case \"$*\" in *packet=size*) echo packets >> '" + calls + "'; echo 1000; exit 0;; esac\n" +
			`echo '{"streams":[{"codec_type":"video","codec_name":"h264","width":64,"height":48,"r_frame_rate":"10/1"}],"format":{"duration":"N/A"}}'` + "\n",
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
	stored := c.Stats().Stored
	if got, err := videoBitrate(c, video, probe); err == nil {
		t.Errorf("videoBitrate = %d, want the duration error", got)
	}
	if testutil.Exists(calls) {
		t.Error("ffprobe measured packets without a duration")
	}
	if got := c.Stats().Stored; got != stored {
		t.Errorf("Stored = %d, want %d (nothing cached)", got, stored)
	}
}
