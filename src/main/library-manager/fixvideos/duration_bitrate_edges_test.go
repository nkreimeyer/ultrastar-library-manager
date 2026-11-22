package fixvideos

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	log "github.com/sirupsen/logrus"

	"github.com/nkreimeyer/library-manager/internal/testutil"
	"github.com/nkreimeyer/library-manager/mediacache"
	"github.com/nkreimeyer/library-manager/mediatools"
)

// parseClock accepts exactly the clock values a Matroska DURATION tag can hold: minutes
// and seconds below 60, a positive total.
func TestParseClockBoundaries(t *testing.T) {
	tests := []struct {
		in   string
		want float64
		ok   bool
	}{
		{"00:59:00", 3540, true},
		{"00:60:00", 0, false},
		{"00:00:59.5", 59.5, true},
		{"00:00:60", 0, false},
		{"00:01:00.0", 60, true}, // seconds may be exactly 0
		{"01:00:00", 3600, true}, // minutes may be exactly 0
		{"00:00:01", 1, true},    // hours may be exactly 0
		{"-1:00:05", 0, false},
		{"01:-1:05", 0, false},
		{"01:00:-1", 0, false}, // total is positive, the seconds are not
		{"00:00:00", 0, false},
		{"00:00:00.000000001", 1e-9, true},
		{"1:2", 0, false},
		{"a:00:01", 0, false},
	}
	for _, tt := range tests {
		got, ok := parseClock(tt.in)
		if ok != tt.ok || (ok && got != tt.want) {
			t.Errorf("parseClock(%q) = %v, %v; want %v, %v", tt.in, got, ok, tt.want, tt.ok)
		}
	}
}

// fakeDurations puts an ffprobe on PATH that answers with the JSON in files[name] for the
// file name given last. Anything else fails.
func fakeDurations(t *testing.T, files map[string]string) {
	t.Helper()
	dir := t.TempDir()
	script := "#!/bin/sh\nfor a; do f=\"$a\"; done\ncase \"${f##*/}\" in\n"
	for name, json := range files {
		jf := filepath.Join(dir, name+".json")
		testutil.WriteFile(t, jf, json+"\n")
		script += "  " + name + ") while IFS= read -r l; do printf '%s\\n' \"$l\"; done < '" + jf + "'; exit 0;;\n"
	}
	script += "esac\nexit 1\n"
	testutil.FakeTools(t, map[string]string{"ffprobe": script})
}

func videoJSON(streamDuration, formatDuration string) string {
	s := `{"codec_type":"video","codec_name":"h264","width":64,"height":48`
	if streamDuration != "" {
		s += `,"duration":"` + streamDuration + `"`
	}
	return `{"streams":[` + s + `}],"format":{"duration":"` + formatDuration + `"}}`
}

// The decision between "nothing to compare" and a comparison, at its edges: an unknown
// (0) video duration of the original or of the result, and an unknown (0) container
// duration of the original when the audio is kept.
func TestSameVideoDurationUnknownBoundaries(t *testing.T) {
	fakeDurations(t, map[string]string{
		"in3":       videoJSON("3.0", "3.0"),
		"in1":       videoJSON("1.0", "1.0"),
		"inzero":    videoJSON("0.000000", "0.000000"),
		"inzerofmt": `{"streams":[{"codec_type":"video","codec_name":"h264"}],"format":{"duration":"0.000000"}}`,
		"out3":      videoJSON("3.0", "3.0"),
		"outzero":   videoJSON("0.000000", "3.0"),
		"outfmt3":   `{"streams":[{"codec_type":"video","codec_name":"h264"}],"format":{"duration":"3.0"}}`,
	})
	ctx := context.Background()
	check := func(name, in, out string, keepsAudio, want bool) {
		t.Helper()
		ok, why, stop := sameVideoDuration(ctx, in, out, keepsAudio, true)
		if ok != want || stop != nil {
			t.Errorf("%s: sameVideoDuration = %v, %q, %v; want %v", name, ok, why, stop, want)
		}
	}
	check("both known and equal", "in3", "out3", false, true)
	check("both known, 3 s vs 1 s", "in1", "out3", false, false)
	check("both known, 3 s vs 1 s, audio kept", "in1", "out3", true, false)
	check("original unknown, audio stripped", "inzero", "out3", false, true)
	check("result unknown, audio stripped", "in3", "outzero", false, true)
	// Audio kept and no video duration of the original: the container durations decide.
	check("container of the original 0 s: nothing to compare", "inzerofmt", "out3", true, true)
	check("container 3 s vs 3 s", "outfmt3", "out3", true, true)
	check("container 3 s vs 1 s", "outfmt3", "in1", true, false)
	// The container duration of the original is unknown and the result's video duration is
	// too: nothing to compare, even with the audio kept.
	check("both containers unknown", "inzerofmt", "outzero", true, true)
}

// A packet bitrate of 0 in the cache is no result: the measurement runs.
func TestVideoBitrateIgnoresZeroCachedBitrate(t *testing.T) {
	version := func(name string) string {
		return "#!/bin/sh\nif [ \"$1\" = -version ]; then echo '" + name + " version 7.1 Copyright (c) the FFmpeg developers'; exit 0; fi\n"
	}
	testutil.FakeTools(t, map[string]string{
		"ffprobe": version("ffprobe") + "case \"$*\" in *packet=size*) echo 1000; echo 1000; exit 0;; esac\n" +
			`echo '{"streams":[{"codec_type":"video","codec_name":"h264","width":64,"height":48,"r_frame_rate":"10/1"}],"format":{"duration":"2.0"}}'` + "\n",
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
	var cached packetResult
	st, ok := c.Lookup(video, kindPackets, &cached)
	if ok {
		t.Fatal("unexpected hit")
	}
	c.Store(st, kindPackets, packetResult{Bitrate: 0})
	if got, err := videoBitrate(c, video, probe); err != nil || got != 8000 {
		t.Errorf("videoBitrate = %d, %v; want the measured 8000 (2000 bytes in 2 s)", got, err)
	}
}

// What cannot be read about a video is logged at debug level, and only then.
func TestNeedsReEncodeAndPlanLogOnlyRealProblems(t *testing.T) {
	file := filepath.Join(t.TempDir(), "v.mp4")
	testutil.WriteFile(t, file, "video")
	good := mediatools.ProbeInfo{
		Streams: []mediatools.Stream{{CodecType: "video", CodecName: "h264", Width: 64, Height: 48, RFrameRate: "25/1", BitRate: "1000"}},
		Format:  mediatools.Format{Duration: "3"},
	}

	hook := testutil.CaptureLogsAt(t, log.DebugLevel)
	needsReEncode(nil, file, "h264", good)
	for _, m := range []string{"Error getting bitrate", "Error estimating"} {
		if logged(hook, m) {
			t.Errorf("a readable video logged %q", m)
		}
	}
	hook.Reset()
	needsReEncode(nil, file, "h264", mediatools.ProbeInfo{})
	for _, m := range []string{"Error getting bitrate", "Error estimating"} {
		if !logged(hook, m) {
			t.Errorf("an unreadable video did not log %q", m)
		}
	}

	p, songDir, _ := newProcessor(t, true)
	video := filepath.Join(songDir, "v.mp4")
	testutil.WriteFile(t, video, "video")
	hook.Reset()
	p.plan(songDir, video, good, nil)
	if logged(hook, "Error getting resolution") {
		t.Error("plan of a readable video logged a resolution error")
	}
	hook.Reset()
	p.plan(songDir, video, mediatools.ProbeInfo{}, errors.New("unreadable"))
	if !logged(hook, "Error getting resolution") {
		t.Error("plan of an unreadable video did not log the resolution error")
	}
}

// A failing write of the media journal or of the corrupted-files report is logged as an
// error; a successful one is not.
func TestJournalAndReportWriteErrorsAreLogged(t *testing.T) {
	testutil.SkipIfRoot(t)
	p, songDir, outDir := newProcessor(t, false)
	video := filepath.Join(songDir, "v.avi")
	testutil.WriteFile(t, video, "video")
	hook := testutil.CaptureLogs(t)

	p.record("test", video)
	p.flagCorrupted(video, "test")
	p.writeCorruptedReport()
	if logged(hook, "Saving the media journal") || hook.LastEntry() != nil && hook.LastEntry().Level <= log.ErrorLevel {
		t.Errorf("successful writes logged an error: %v", hook.AllEntries())
	}

	if err := os.Chmod(outDir, 0o555); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.Chmod(outDir, 0o755) })
	if f, err := os.CreateTemp(outDir, "probe"); err == nil {
		f.Close()
		t.Skip("permissions are not enforced")
	}
	hook.Reset()
	other := filepath.Join(songDir, "w.avi")
	testutil.WriteFile(t, other, "other video")
	p.record("test", other)
	if !logged(hook, "Saving the media journal after") {
		t.Errorf("failed journal write not logged: %v", hook.AllEntries())
	}
	hook.Reset()
	p.flagCorrupted(other, "test")
	p.writeCorruptedReport()
	failed := false
	for _, e := range hook.AllEntries() {
		failed = failed || e.Level == log.ErrorLevel && strings.Contains(e.Message, "tmp")
	}
	if !failed {
		t.Errorf("failed report write not logged: %v", hook.AllEntries())
	}
}
