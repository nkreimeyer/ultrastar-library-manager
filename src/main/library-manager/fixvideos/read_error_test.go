package fixvideos

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/nkreimeyer/library-manager/fsops"
	"github.com/nkreimeyer/library-manager/internal/testutil"
	"github.com/nkreimeyer/library-manager/mediatools"
)

// realTools returns the paths of the real ffmpeg and ffprobe, for wrapper scripts that
// FakeTools puts on PATH; it skips without ffmpeg.
func realTools(t *testing.T) (ffmpegPath, ffprobePath string) {
	t.Helper()
	testutil.RequireFFmpeg(t)
	ffmpegPath, err := exec.LookPath("ffmpeg")
	if err != nil {
		t.Fatal(err)
	}
	ffprobePath, err = exec.LookPath("ffprobe")
	if err != nil {
		t.Fatal(err)
	}
	return ffmpegPath, ffprobePath
}

// shortenOutput is a shell snippet that runs ffmpeg at ffmpegPath with "-t <seconds>"
// before its output file (its last argument): the result holds only the start.
func shortenOutput(ffmpegPath, seconds string) string {
	return "n=$#; i=0\nfor a; do\n  i=$((i+1))\n  [ $i -eq 1 ] && set --\n" +
		"  if [ $i -eq $n ]; then set -- \"$@\" -t " + seconds + " \"$a\"; else set -- \"$@\" \"$a\"; fi\ndone\n" +
		"'" + ffmpegPath + "' \"$@\"\n"
}

// A probe that says nothing about the file (ffprobe hit a read error of the file system,
// was killed, or printed no JSON) must not lead to an encode: plan would take the video for
// unreadable and re-encode it, and a non-MP4 one would be converted and deleted. The video
// is counted as a failure, stays as it is and keeps its place in the corrupted-files list.
// ffprobe failing on the file itself still leads to an encode attempt.
func TestProbeWithoutResultStartsNoEncode(t *testing.T) {
	const h264 = `{"streams":[{"codec_type":"video","codec_name":"h264","width":1920,"height":1080,"r_frame_rate":"25/1"}],"format":{"duration":"10.0"}}`
	for _, tt := range []struct {
		name, ffprobe string
		encodes       bool
	}{
		{"read error", "echo \"$f: Input/output error\" >&2\nexit 1\n", false},
		{"killed", "kill -9 $$\n", false},
		{"read error with exit 0", "echo '" + h264 + "'\necho 'Input/output error' >&2\nexit 0\n", false},
		{"no JSON", "echo garbage\nexit 0\n", false},
		{"unreadable file", "echo \"$f: Invalid data found when processing input\" >&2\nexit 1\n", true},
	} {
		t.Run(tt.name, func(t *testing.T) {
			p, songDir, _ := newProcessor(t, false)
			song := filepath.Join(songDir, "A - S")
			testutil.WriteFile(t, filepath.Join(song, "song.txt"), "#TITLE:S\n#ARTIST:A\n#VIDEO:video.avi\n")
			video := filepath.Join(song, "video.avi")
			testutil.WriteFile(t, video, "original video")
			ran := filepath.Join(t.TempDir(), "ffmpeg-ran")
			testutil.FakeTools(t, map[string]string{
				"ffprobe": "#!/bin/sh\nfor a; do f=\"$a\"; done\n" + tt.ffprobe,
				"ffmpeg":  "#!/bin/sh\n: > '" + ran + "'\necho 'Invalid data found when processing input' >&2\nexit 1\n",
			})
			p.processVideoFile(song, video)

			_, err := os.Stat(ran)
			if encoded := err == nil; encoded != tt.encodes {
				t.Errorf("ffmpeg ran: %v, want %v", encoded, tt.encodes)
			}
			if got := testutil.ReadFile(t, video); got != "original video" {
				t.Errorf("video = %q, want it unchanged", got)
			}
			if tt.encodes {
				return
			}
			if st := p.ops.Stats(); st.Failed != 1 {
				t.Errorf("Stats = %+v, want one failure", st)
			}
			if _, ok := p.readable[video]; ok {
				t.Error("the video counts as readable: it would be dropped from the corrupted-files list")
			}
			if len(p.corrupted) != 0 {
				t.Errorf("corrupted = %v, want none: nothing is known about the file", p.corrupted)
			}
		})
	}
}

// ffmpeg exits 0 after a read error of its input (a NAS dropping out) and writes only what
// it read: the result must not replace or convert the original.
func TestEncodeReadErrorKeepsOriginal(t *testing.T) {
	testutil.RequireFFmpeg(t)
	ffmpegPath, err := exec.LookPath("ffmpeg")
	if err != nil {
		t.Fatal(err)
	}
	ffprobePath, err := exec.LookPath("ffprobe")
	if err != nil {
		t.Fatal(err)
	}
	p, songDir, _ := newProcessor(t, false)
	song := filepath.Join(songDir, "A - S")
	txt := "#TITLE:S\n#ARTIST:A\n#VIDEO:video.avi\n"
	testutil.WriteFile(t, filepath.Join(song, "song.txt"), txt)
	video := filepath.Join(song, "video.avi")
	makeMedia(t, video, "-c:v", "mpeg4", "-c:a", "mp3")
	before := testutil.ReadFile(t, video)
	testutil.FakeTools(t, map[string]string{
		"ffprobe": "#!/bin/sh\nexec '" + ffprobePath + "' \"$@\"\n",
		"ffmpeg": "#!/bin/sh\n'" + ffmpegPath + "' \"$@\"\nrc=$?\n" +
			"echo '[in#0/avi @ 0x1] Error during demuxing: Input/output error' >&2\nexit $rc\n",
	})
	p.processVideoFile(song, video)

	if got := testutil.ReadFile(t, video); got != before {
		t.Error("the original was replaced")
	}
	if got := testutil.ReadFile(t, filepath.Join(song, "song.txt")); got != txt {
		t.Errorf("song.txt = %q, want it unchanged", got)
	}
	if got := strings.Join(testutil.DirNames(t, song), ","); got != "song.txt,video.avi" {
		t.Errorf("song folder holds %s, want only song.txt and video.avi", got)
	}
	if st := p.ops.Stats(); st.Failed != 1 || st.Replaced != 0 || st.Removed != 0 {
		t.Errorf("Stats = %+v, want one failure and nothing replaced or removed", st)
	}
	if len(p.corrupted) != 0 {
		t.Errorf("corrupted = %v, want none: a read error says nothing about the file", p.corrupted)
	}
}

// When ffprobe read the original before the encode, it must read it again for the
// duration check: a failure then (the NAS gone again) is no match, unlike an original that
// was unreadable from the start.
func TestSameVideoDurationNeedsTheOriginalAgain(t *testing.T) {
	testutil.RequireFFmpeg(t)
	dir := t.TempDir()
	good := filepath.Join(dir, "good.mp4")
	testutil.GenerateMedia(t, "-f", "lavfi", "-i", "testsrc=size=64x48:rate=25:duration=1", "-c:v", "mpeg4", good)
	garbage := filepath.Join(dir, "garbage.mp4")
	testutil.WriteFile(t, garbage, "garbage")
	missing := filepath.Join(dir, "missing.mp4")
	for _, keepsAudio := range []bool{false, true} {
		for _, in := range []string{garbage, missing} {
			ok, why, err := sameVideoDuration(context.Background(), in, good, keepsAudio, true)
			if ok || err == nil || !strings.Contains(err.Error(), "cannot read the original again") {
				t.Errorf("%s, keepsAudio=%v: = %v, %q, %v; want the check to fail", filepath.Base(in), keepsAudio, ok, why, err)
			}
		}
	}
}

// A result without a video stream never matches an original that has one, even if the
// original's video duration is unknown and the audio is stripped.
func TestSameVideoDurationResultWithoutVideo(t *testing.T) {
	testutil.RequireFFmpeg(t)
	dir := t.TempDir()
	video := filepath.Join(dir, "video.mp4")
	makeMedia(t, video, "-c:v", "mpeg4", "-c:a", "aac")
	audioOnly := filepath.Join(dir, "audio.mp4")
	makeMedia(t, audioOnly, "-vn", "-c:a", "aac")
	ok, why, err := sameVideoDuration(context.Background(), video, audioOnly, false, true)
	if ok || err != nil || !strings.Contains(why, "no video stream") {
		t.Errorf("sameVideoDuration = %v, %q, %v; want a mismatch", ok, why, err)
	}
}

// A cover stored as the first video stream (attached_pic) is not the video: size, frame
// rate, duration and bitrate come from the moving video after it.
func TestCoverBeforeVideoIsNotTheVideo(t *testing.T) {
	probe, err := mediatools.ParseProbe([]byte(`{"streams":[
		{"index":0,"codec_type":"video","codec_name":"mjpeg","width":600,"height":600,"r_frame_rate":"90000/1","disposition":{"attached_pic":1}},
		{"index":1,"codec_type":"video","codec_name":"h264","width":1920,"height":1080,"r_frame_rate":"25/1","bit_rate":"9000000","duration":"200.0"},
		{"index":2,"codec_type":"audio","codec_name":"aac"}],
		"format":{"duration":"200.0"}}`))
	if err != nil {
		t.Fatal(err)
	}
	if w, h, err := videoResolution(probe); err != nil || w != 1920 || h != 1080 {
		t.Errorf("videoResolution = %dx%d, %v; want 1920x1080", w, h, err)
	}
	if fps, err := videoFramerate(probe); err != nil || fps != 25 {
		t.Errorf("videoFramerate = %v, %v; want 25", fps, err)
	}
	if d := videoDuration(probe); d != 200 {
		t.Errorf("videoDuration = %v, want 200", d)
	}
	if br, err := videoBitrate(nil, "unused", probe); err != nil || br != 9000000 {
		t.Errorf("videoBitrate = %v, %v; want 9000000", br, err)
	}
}

// ffprobe exiting 0 with a read error, failing with one, being killed or printing no JSON,
// on the result or (read before the encode) on the original: the check cannot be made, so
// neither a match nor a mismatch (a corrupted original) is reported.
func TestSameVideoDurationCannotCheck(t *testing.T) {
	const answer = `{"streams":[{"codec_type":"video","codec_name":"h264","width":64,"height":48,"r_frame_rate":"25/1","duration":"1.0"}],"format":{"duration":"1.0"}}`
	dir := t.TempDir()
	in, out := filepath.Join(dir, "in.avi"), filepath.Join(dir, "out.mp4")
	testutil.WriteFile(t, in, "in")
	testutil.WriteFile(t, out, "out")
	for _, trouble := range []struct{ name, script string }{
		{"read error, exit 0", "echo '" + answer + "'\necho \"$f: Input/output error\" >&2\nexit 0\n"},
		{"read error", "echo \"$f: Input/output error\" >&2\nexit 1\n"},
		{"killed", "kill -9 $$\n"},
		{"no JSON", "echo garbage\nexit 0\n"},
	} {
		for _, bad := range []string{in, out} {
			testutil.FakeTools(t, map[string]string{
				"ffprobe": "#!/bin/sh\nfor a; do f=\"$a\"; done\nif [ \"$f\" = '" + bad + "' ]; then\n" + trouble.script + "fi\necho '" + answer + "'\n",
			})
			for _, keepsAudio := range []bool{false, true} {
				ok, why, err := sameVideoDuration(context.Background(), in, out, keepsAudio, true)
				if ok || why != "" || err == nil {
					t.Errorf("%s on %s, keepsAudio=%v: = %v, %q, %v; want the check to fail", trouble.name, filepath.Base(bad), keepsAudio, ok, why, err)
				}
			}
		}
	}
}

// A damaged file makes ffmpeg print system messages that are no read error, e.g. a
// demuxer's ENOMEM for a bogus sample size, and exit 0 with a short result: that result
// fails the duration check and the video is reported as potentially corrupted (not counted
// as a failure, which the next run would repeat).
func TestDamagedFileMessagesAreNoReadError(t *testing.T) {
	ffmpegPath, ffprobePath := realTools(t)
	p, songDir, _ := newProcessor(t, false)
	song := filepath.Join(songDir, "A - S")
	testutil.WriteFile(t, filepath.Join(song, "song.txt"), "#TITLE:S\n#ARTIST:A\n#VIDEO:video.avi\n")
	video := filepath.Join(song, "video.avi")
	makeMedia(t, video, "-c:v", "mpeg4", "-c:a", "mp3")
	before := testutil.ReadFile(t, video)
	testutil.FakeTools(t, map[string]string{
		"ffprobe": "#!/bin/sh\nexec '" + ffprobePath + "' \"$@\"\n",
		"ffmpeg": "#!/bin/sh\n" + shortenOutput(ffmpegPath, "0.3") +
			"echo '[in#0/avi @ 0x1] Error during demuxing: Cannot allocate memory' >&2\nexit 0\n",
	})
	p.processVideoFile(song, video)
	if got := testutil.ReadFile(t, video); got != before {
		t.Error("the original was replaced")
	}
	if _, ok := p.corrupted[video]; !ok {
		t.Errorf("corrupted = %v, want the video reported", p.corrupted)
	}
	if st := p.ops.Stats(); st.Failed != 0 {
		t.Errorf("Stats = %+v, want no failure", st)
	}
}

// A tagging remux that exits 0 after a read error of its input must not replace the
// original.
func TestRemuxReadErrorKeepsOriginal(t *testing.T) {
	ffmpegPath, ffprobePath := realTools(t)
	p, songDir, _ := newProcessor(t, false)
	video := filepath.Join(songDir, "A - S", "video.mkv")
	if err := os.MkdirAll(filepath.Dir(video), 0o755); err != nil {
		t.Fatal(err)
	}
	makeMedia(t, video, "-c:v", "mpeg4", "-c:a", "mp3")
	before := testutil.ReadFile(t, video)
	testutil.FakeTools(t, map[string]string{
		"ffprobe": "#!/bin/sh\nexec '" + ffprobePath + "' \"$@\"\n",
		"ffmpeg": "#!/bin/sh\n'" + ffmpegPath + "' \"$@\"\nrc=$?\n" +
			"echo '[in#0 @ 0x1] Error during demuxing: Input/output error' >&2\nexit $rc\n",
	})
	if err := p.tagByRemux(video); err == nil || !strings.Contains(err.Error(), "read error") {
		t.Errorf("tagByRemux = %v, want a read error", err)
	}
	if testutil.ReadFile(t, video) != before {
		t.Error("the original was replaced")
	}
	if got := strings.Join(testutil.DirNames(t, filepath.Dir(video)), ","); got != "video.mkv" {
		t.Errorf("song folder holds %s, want only video.mkv", got)
	}
	if st := p.ops.Stats(); st.Replaced != 0 {
		t.Errorf("Stats = %+v, want nothing replaced", st)
	}
}

// The strip-only fallback after a larger re-encode checks its result against the original,
// which was read before: if ffprobe cannot read it again, the original stays.
func TestLargerFallbackNeedsTheOriginalAgain(t *testing.T) {
	p, songDir, _ := newProcessor(t, false)
	song := filepath.Join(songDir, "A - S")
	testutil.WriteFile(t, filepath.Join(song, "song.txt"), "#TITLE:S\n#ARTIST:A\n#MP3:song.mp3\n#VIDEO:video.mp4\n")
	video := filepath.Join(song, "video.mp4")
	testutil.WriteFile(t, video, "original video")
	tmp, err := fsops.TempFileNear(video, ".mp4")
	if err != nil {
		t.Fatal(err)
	}
	testutil.WriteFile(t, tmp, "a re-encode larger than the original")
	const answer = `{"streams":[{"codec_type":"video","codec_name":"h264","width":64,"height":48,"r_frame_rate":"25/1","duration":"1.0"}],"format":{"duration":"1.0"}}`
	testutil.FakeTools(t, map[string]string{
		"ffprobe": "#!/bin/sh\nfor a; do f=\"$a\"; done\nif [ \"$f\" = '" + video + "' ]; then echo \"$f: Input/output error\" >&2; exit 1; fi\necho '" + answer + "'\n",
		"ffmpeg":  "#!/bin/sh\nfor a; do f=\"$a\"; done\nprintf x > \"$f\"\n",
	})
	p.replaceOriginal(song, video, tmp, encodePlan{reEncode: true, removeAudio: true, probed: true})
	if got := testutil.ReadFile(t, video); got != "original video" {
		t.Errorf("video = %q, want the original", got)
	}
	if st := p.ops.Stats(); st.Failed != 1 || st.Replaced != 0 {
		t.Errorf("Stats = %+v, want one failure and nothing replaced", st)
	}
	assertNoTempFiles(t, songDir)
}

// The packet sizes are measured on the video ("V:0"), not on a cover picture stored before
// it ("v:0").
func TestVideoBitrateMeasuresTheVideoNotTheCover(t *testing.T) {
	testutil.FakeTools(t, map[string]string{
		"ffprobe": "#!/bin/sh\ncase \" $* \" in *\" V:0 \"*) echo 1250000; echo 1250000;; *) echo 100;; esac\n",
	})
	probe, err := mediatools.ParseProbe([]byte(`{"streams":[
		{"index":0,"codec_type":"video","codec_name":"mjpeg","disposition":{"attached_pic":1}},
		{"index":1,"codec_type":"video","codec_name":"h264","width":1920,"height":1080}],
		"format":{"duration":"10.0"}}`))
	if err != nil {
		t.Fatal(err)
	}
	if br, err := videoBitrate(nil, filepath.Join(t.TempDir(), "v.mkv"), probe); err != nil || br != 2000000 {
		t.Errorf("videoBitrate = %v, %v; want 2000000 (the video's packets)", br, err)
	}
}
