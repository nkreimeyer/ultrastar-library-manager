package fixvideos

import (
	"context"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	logtest "github.com/sirupsen/logrus/hooks/test"

	"github.com/nkreimeyer/library-manager/fsops"
	"github.com/nkreimeyer/library-manager/internal/testutil"
)

// logged reports whether an entry of hook contains all of parts.
func logged(hook *logtest.Hook, parts ...string) bool {
	for _, e := range hook.AllEntries() {
		ok := true
		for _, p := range parts {
			ok = ok && strings.Contains(e.Message, p)
		}
		if ok {
			return true
		}
	}
	return false
}

// A conversion that changes the extension is blocked before encoding when the txt cannot
// follow: a txt with a decode Problem, or one whose #VIDEO tag does not name the video.
// The original stays, nothing is encoded (no temp file, nothing counted) and a "Not
// converting" warning says so.
func TestConversionBlockedByTxtIsWarnedAboutBeforeEncoding(t *testing.T) {
	testutil.RequireFFmpeg(t)
	tests := []struct {
		name, txt string
		want      []string
	}{
		{"decode problem", "#TITLE:Песня\n#ARTIST:A\n#ENCODING:CP1252\n#MP3:video.avi\n#VIDEO:video.avi\n: 0 4 0 Ля\nE\n",
			[]string{"Not converting", "the song txt cannot be updated"}},
		{"video tag names another file", "#TITLE:S\n#ARTIST:A\n#VIDEO:other.avi\n",
			[]string{"Not converting", "does not reference it"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			p, songDir, _ := newProcessor(t, false)
			song := filepath.Join(songDir, "A - S")
			testutil.WriteFile(t, filepath.Join(song, "song.txt"), tt.txt)
			video := filepath.Join(song, "video.avi")
			makeMedia(t, video, "-c:v", "mpeg4", "-q:v", "1", "-c:a", "ac3")
			before := testutil.Snapshot(t, songDir)
			hook := testutil.CaptureLogs(t)

			p.processVideoFile(song, video)

			if !logged(hook, tt.want...) {
				t.Errorf("no warning containing %q logged", tt.want)
			}
			if st := p.ops.Stats(); st != (fsops.Stats{}) {
				t.Errorf("Stats = %+v, want nothing done", st)
			}
			if after := testutil.Snapshot(t, songDir); after != before {
				t.Errorf("song folder changed\nbefore: %v\n after: %v", before, after)
			}
			if logged(hook, "Processing video") {
				t.Error("the video was encoded although the conversion is blocked")
			}
			assertNoTempFiles(t, songDir)
		})
	}
}

func mkParent(t *testing.T, file string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(file), 0o755); err != nil {
		t.Fatal(err)
	}
}

// replaceFixture is a song folder with a txt, an original and a converted temp file.
func replaceFixture(t *testing.T, video, tmpExt string) (p *processor, song, tmp string) {
	t.Helper()
	p, songDir, _ := newProcessor(t, false)
	song = filepath.Join(songDir, "A - S")
	testutil.WriteFile(t, filepath.Join(song, "song.txt"), "#TITLE:S\n#ARTIST:A\n#VIDEO:"+filepath.Base(video)+"\n")
	video = filepath.Join(song, filepath.Base(video))
	testutil.WriteFile(t, video, "original video, larger than the result")
	tmp, err := fsops.TempFileNear(video, tmpExt)
	if err != nil {
		t.Fatal(err)
	}
	testutil.WriteFile(t, tmp, "converted")
	return p, song, tmp
}

// If the temp file cannot be renamed to the new name, the temp is discarded and the
// original stays referenced.
func TestReplaceOriginalRenameFails(t *testing.T) {
	p, song, tmp := replaceFixture(t, "video.avi", ".mp4")
	testutil.ChmodUnreadable(t, tmp) // fsops.Rename syncs it first and fails
	logs := testutil.ErrorLogs(t, func() { p.replaceOriginal(song, filepath.Join(song, "video.avi"), tmp, encodePlan{reEncode: true}) })
	if !testutil.ContainsPrefix(logs, "renaming") {
		t.Errorf("error logs = %q, want the rename error", logs)
	}
	if st := p.ops.Stats(); st.Failed != 1 || st.Written != 0 || st.Removed != 0 {
		t.Errorf("Stats = %+v, want exactly the rename failure", st)
	}
	if got := testutil.DirNames(t, song); !slices.Equal(got, []string{"song.txt", "video.avi"}) {
		t.Errorf("song folder = %v, want song.txt and video.avi (temp discarded, no new file)", got)
	}
	if got := testutil.ReadFile(t, filepath.Join(song, "song.txt")); !strings.Contains(got, "#VIDEO:video.avi") {
		t.Errorf("txt = %q, want it unchanged", got)
	}
}

// Once the txt points to the new file, a failing deletion of the original is only
// logged: the conversion stays (the next cleanup moves the unreferenced original away).
func TestReplaceOriginalRemovingOriginalFails(t *testing.T) {
	p, song, tmp := replaceFixture(t, "video.avi", ".mp4")
	// The "original" is a folder with content, which os.Remove refuses; its size is
	// still larger than the one byte result.
	video := filepath.Join(song, "video.avi")
	if err := os.Remove(video); err != nil {
		t.Fatal(err)
	}
	testutil.WriteFile(t, filepath.Join(video, "x"), "x")
	testutil.WriteFile(t, tmp, "c")

	logs := testutil.ErrorLogs(t, func() { p.replaceOriginal(song, video, tmp, encodePlan{reEncode: true}) })
	if !testutil.ContainsPrefix(logs, "Deleting the original video") {
		t.Errorf("error logs = %q, want the deletion error", logs)
	}
	if got := testutil.ReadFile(t, filepath.Join(song, "song.txt")); !strings.Contains(got, "#VIDEO:video.mp4") {
		t.Errorf("txt = %q, want #VIDEO:video.mp4", got)
	}
	if got := testutil.ReadFile(t, filepath.Join(song, "video.mp4")); got != "c" {
		t.Errorf("video.mp4 = %q, want the converted file kept", got)
	}
	if st := p.ops.Stats(); st.Removed != 0 || st.Written != 1 || st.Failed != 1 {
		t.Errorf("Stats = %+v, want the txt written, nothing removed and the failed removal counted", st)
	}
	assertNoTempFiles(t, song)
}

// A same-extension replacement (also one differing in case) that fails keeps the original
// and removes the temp file.
func TestReplaceOriginalInPlaceFails(t *testing.T) {
	p, song, tmp := replaceFixture(t, "video.MP4", ".mp4")
	testutil.ChmodUnreadable(t, tmp) // fsops.Replace syncs it first and fails
	video := filepath.Join(song, "video.MP4")
	logs := testutil.ErrorLogs(t, func() { p.replaceOriginal(song, video, tmp, encodePlan{reEncode: true}) })
	if !testutil.ContainsPrefix(logs, "replacing") {
		t.Errorf("error logs = %q, want the replace error", logs)
	}
	if st := p.ops.Stats(); st.Failed != 1 || st.Replaced != 0 {
		t.Errorf("Stats = %+v, want 1 failure and no replacement", st)
	}
	if got := testutil.ReadFile(t, video); got != "original video, larger than the result" {
		t.Errorf("original changed to %q", got)
	}
	if got := testutil.DirNames(t, song); !slices.Equal(got, []string{"song.txt", "video.MP4"}) {
		t.Errorf("song folder = %v, want the temp gone and nothing new", got)
	}
}

// tagByRemux discards a remux that does not carry the tag or has another duration, and
// reports why; the original is untouched.
func TestTagByRemuxDiscardsBadRemux(t *testing.T) {
	testutil.RequireFFmpeg(t)
	ffmpeg, ffprobe := testutil.RequireTool(t, "ffmpeg"), testutil.RequireTool(t, "ffprobe")

	t.Run("no tag", func(t *testing.T) {
		markerExts[".avi"] = true // AVI drops the tag
		t.Cleanup(func() { delete(markerExts, ".avi") })
		p, songDir, _ := newProcessor(t, false)
		video := filepath.Join(songDir, "A - S", "video.avi")
		mkParent(t, video)
		makeMedia(t, video, "-c:v", "mpeg4", "-q:v", "1", "-c:a", "ac3")
		before := testutil.Snapshot(t, video)
		if err := p.tagByRemux(video); err == nil || !strings.Contains(err.Error(), "does not carry the tag") {
			t.Errorf("tagByRemux = %v, want the missing-tag error", err)
		}
		if after := testutil.Snapshot(t, video); after != before {
			t.Errorf("video changed\nbefore: %v\n after: %v", before, after)
		}
		assertNoTempFiles(t, songDir)
	})

	t.Run("other duration", func(t *testing.T) {
		p, songDir, _ := newProcessor(t, false)
		video := filepath.Join(songDir, "A - S", "video.mkv")
		mkParent(t, video)
		makeMedia(t, video, "-c:v", "mpeg4", "-c:a", "libvorbis")
		before := testutil.Snapshot(t, video)
		// ffmpeg wrapper that cuts every input to 0.4 s, so the remux is shorter.
		dir := testutil.FakeTools(t, map[string]string{"ffmpeg": "#!/bin/sh\nexec " + ffmpeg + " -t 0.4 \"$@\"\n"})
		if err := os.Symlink(ffprobe, filepath.Join(dir, "ffprobe")); err != nil {
			t.Fatal(err)
		}
		if err := p.tagByRemux(video); err == nil || !strings.Contains(err.Error(), "instead of") {
			t.Errorf("tagByRemux = %v, want the duration error", err)
		}
		if after := testutil.Snapshot(t, video); after != before {
			t.Errorf("video changed\nbefore: %v\n after: %v", before, after)
		}
		assertNoTempFiles(t, songDir)
		if st := p.ops.Stats(); st.Replaced != 0 {
			t.Errorf("Stats = %+v, want no replacement", st)
		}
	})
}

// If the temp file for the remux cannot be created, the video goes into the media journal.
func TestMarkCheckedTempCreationFails(t *testing.T) {
	testutil.RequireFFmpeg(t)
	testutil.SkipIfRoot(t)
	p, songDir, _ := newProcessor(t, false)
	song := filepath.Join(songDir, "A - S")
	video := filepath.Join(song, "video.mkv")
	mkParent(t, video)
	makeMedia(t, video, "-c:v", "mpeg4", "-c:a", "libvorbis")
	if err := os.Chmod(song, 0o555); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.Chmod(song, 0o755) })
	if f, err := os.CreateTemp(song, "probe"); err == nil {
		f.Close()
		t.Skip("permissions are not enforced")
	}
	if err := p.tagByRemux(video); err == nil || !strings.Contains(err.Error(), "creating temp file") {
		t.Errorf("tagByRemux = %v, want the temp creation error", err)
	}
	p.markChecked(video)
	if !p.journal.Has(journalChecked, video) {
		t.Error("video not recorded as checked in the journal")
	}
	if st := p.ops.Stats(); st.Replaced != 0 || st.Failed != 0 {
		t.Errorf("Stats = %+v, want no replacement and no failure", st)
	}
	assertTagged(t, video, false)
}

// With an original that cannot be read there is nothing to compare: the result counts as
// matching, with or without the audio. An output that cannot be read never matches.
func TestSameVideoDurationUnknownInputAndUnreadableOutput(t *testing.T) {
	testutil.RequireFFmpeg(t)
	dir := t.TempDir()
	good := filepath.Join(dir, "good.mp4")
	testutil.GenerateMedia(t, "-f", "lavfi", "-i", "testsrc=size=64x48:rate=25:duration=1", "-c:v", "mpeg4", good)
	garbage := filepath.Join(dir, "garbage.mp4")
	testutil.WriteFile(t, garbage, "garbage")
	missing := filepath.Join(dir, "missing.mp4")
	ctx := context.Background()

	for _, keepsAudio := range []bool{false, true} {
		for _, in := range []string{garbage, missing} {
			if ok, why, stop := sameVideoDuration(ctx, in, good, keepsAudio); !ok || why != "" || stop != nil {
				t.Errorf("unreadable input %s, keepsAudio=%v: = %v, %q, %v; want a match", filepath.Base(in), keepsAudio, ok, why, stop)
			}
		}
		ok, why, stop := sameVideoDuration(ctx, good, garbage, keepsAudio)
		if ok || stop != nil || !strings.HasPrefix(why, "ffprobe cannot read the processed video") {
			t.Errorf("unreadable output, keepsAudio=%v: = %v, %q, %v; want a mismatch", keepsAudio, ok, why, stop)
		}
	}
}

// A real run whose rebuilt list equals the existing report does not write it.
func TestWriteCorruptedReportUnchangedIsNotWritten(t *testing.T) {
	p, songDir, outDir := newProcessor(t, false)
	still := filepath.Join(songDir, "still.avi")
	testutil.WriteFile(t, still, "v")
	report := filepath.Join(outDir, corruptedReportName)
	testutil.WriteFile(t, report, still+"\n")
	old := time.Now().Add(-48 * time.Hour).Truncate(time.Second)
	if err := os.Chtimes(report, old, old); err != nil {
		t.Fatal(err)
	}
	p.flagCorrupted(still, "test") // flagged again: the list stays the same
	p.writeCorruptedReport()
	st, err := os.Stat(report)
	if err != nil {
		t.Fatal(err)
	}
	if !st.ModTime().Equal(old) {
		t.Errorf("report rewritten: mtime %v, want %v", st.ModTime(), old)
	}
	if s := p.ops.Stats(); s.Written != 0 || s.Failed != 0 {
		t.Errorf("Stats = %+v, want nothing written", s)
	}
}

// A dry run only logs how many videos the report would list.
func TestWriteCorruptedReportDryRunLogsCount(t *testing.T) {
	p, songDir, outDir := newProcessor(t, true)
	still := filepath.Join(songDir, "still.avi")
	testutil.WriteFile(t, still, "v")
	gone := filepath.Join(songDir, "gone.avi")
	report := filepath.Join(outDir, corruptedReportName)
	old := still + "\n" + gone + "\n"
	testutil.WriteFile(t, report, old)
	p.flagCorrupted(filepath.Join(songDir, "new.avi"), "test")
	hook := testutil.CaptureLogs(t)
	p.writeCorruptedReport()
	if !logged(hook, "would list 2 video(s)") {
		t.Errorf("no \"would list 2 video(s)\" logged")
	}
	if got := testutil.ReadFile(t, report); got != old {
		t.Errorf("dry run rewrote the report: %q", got)
	}
	if s := p.ops.Stats(); s.Written != 0 {
		t.Errorf("Stats = %+v, want nothing written", s)
	}
}
