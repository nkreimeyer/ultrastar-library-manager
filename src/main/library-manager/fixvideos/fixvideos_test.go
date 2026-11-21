package fixvideos

import (
	"cmp"
	"crypto/sha256"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/nkreimeyer/library-manager/config"
	"github.com/nkreimeyer/library-manager/fsops"
	"github.com/nkreimeyer/library-manager/journal"
)

func TestBuildFFmpegArgs(t *testing.T) {
	head := []string{"-hide_banner", "-nostdin", "-loglevel", "error", "-i", "/lib/song/v.avi", "-map", "0:v:0"}
	x264 := []string{"-c:v", "libx264", "-pix_fmt", "yuv420p", "-crf", "23", "-maxrate", "2.5M", "-bufsize", "5.0M", "-preset", "fast"}
	tail := []string{"-metadata", "SANITIZER_VIDEO=processed", "-movflags", "+use_metadata_tags", "-y", "/lib/song/.v.tmp.mp4"}
	scale := []string{"-vf", "scale=w=1920:h=1080:force_original_aspect_ratio=decrease:force_divisible_by=2"}
	even := []string{"-vf", "scale=trunc(iw/2)*2:trunc(ih/2)*2"}

	tests := []struct {
		name   string
		plan   encodePlan
		output string
		want   []string
	}{
		{
			name: "re-encode keeps audio",
			plan: encodePlan{reEncode: true, crf: 23},
			want: slices.Concat(head, []string{"-map", "0:a?", "-c:a", "copy"}, x264, even, tail),
		},
		{
			name: "re-encode at crf 28 without resize gets even dimensions",
			plan: encodePlan{reEncode: true, crf: 28, removeAudio: true},
			want: slices.Concat(head, []string{"-an", "-c:v", "libx264", "-pix_fmt", "yuv420p", "-crf", "28"}, x264[6:], even, tail),
		},
		{
			name: "strip audio only copies video",
			plan: encodePlan{removeAudio: true},
			want: slices.Concat(head, []string{"-an", "-c:v", "copy"}, tail),
		},
		{
			name: "resize and strip audio",
			plan: encodePlan{reEncode: true, resize: true, removeAudio: true, crf: 23},
			want: slices.Concat(head, []string{"-an"}, x264, scale, tail),
		},
		{
			name:   "non-mp4 output gets the tag without movflags",
			plan:   encodePlan{removeAudio: true},
			output: "/lib/song/.v.tmp.AVI",
			want:   slices.Concat(head, []string{"-an", "-c:v", "copy", "-metadata", "SANITIZER_VIDEO=processed", "-y", "/lib/song/.v.tmp.AVI"}),
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			output := cmp.Or(tt.output, "/lib/song/.v.tmp.mp4")
			got := buildFFmpegArgs("/lib/song/v.avi", output, tt.plan)
			if !slices.Equal(got, tt.want) {
				t.Errorf("args mismatch\n got: %q\nwant: %q", got, tt.want)
			}
		})
	}
}

func TestEncodePlanOutputExt(t *testing.T) {
	if got := (encodePlan{reEncode: true}).outputExt("a.AVI"); got != ".mp4" {
		t.Errorf("re-encode ext = %q, want .mp4", got)
	}
	if got := (encodePlan{removeAudio: true}).outputExt("a.MKV"); got != ".MKV" {
		t.Errorf("copy ext = %q, want .MKV", got)
	}
	if !(encodePlan{reEncode: true}).renames("a.avi") {
		t.Error("a.avi -> .mp4 must count as a rename")
	}
	if (encodePlan{reEncode: true}).renames("a.MP4") {
		t.Error("a.MP4 -> .mp4 only changes the case and must not count as a rename")
	}
}

func TestFitWithin(t *testing.T) {
	tests := []struct {
		w, h, wantW, wantH int
		resize             bool
	}{
		{1920, 1080, 1920, 1080, false},
		{1280, 720, 1280, 720, false},
		{853, 481, 852, 480, false}, // odd sizes are rounded down to even (evenFilter)
		{1921, 1080, 1920, 1078, true},
		{1920, 1081, 1918, 1080, true},
		{3840, 2160, 1920, 1080, true},
		{2560, 1080, 1920, 810, true}, // ultrawide: limited by the width
		{1080, 1920, 606, 1080, true}, // portrait: limited by the height, even width
		{1440, 1440, 1080, 1080, true},
	}
	for _, tt := range tests {
		gotW, gotH := fitWithin(tt.w, tt.h)
		if gotW != tt.wantW || gotH != tt.wantH {
			t.Errorf("fitWithin(%d, %d) = %dx%d, want %dx%d", tt.w, tt.h, gotW, gotH, tt.wantW, tt.wantH)
		}
		if got := needsResize(tt.w, tt.h); got != tt.resize {
			t.Errorf("needsResize(%d, %d) = %v", tt.w, tt.h, got)
		}
	}
}

func TestParseMarkerProbe(t *testing.T) {
	tests := []struct {
		name, json string
		want       markerProbe
	}{
		{"untagged", `{"streams":[{"tags":{"language":"und"}}],"format":{"duration":"12.500000","tags":{"encoder":"x"}}}`, markerProbe{duration: 12.5}},
		{"format tag", `{"format":{"duration":"1.0","tags":{"SANITIZER_VIDEO":"processed"}}}`, markerProbe{tagged: true, duration: 1}},
		{"stream tag, lower case key", `{"streams":[{},{"tags":{"sanitizer_video":"processed"}}],"format":{}}`, markerProbe{tagged: true}},
		{"unknown duration", `{"format":{"duration":"N/A"}}`, markerProbe{}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := parseMarkerProbe([]byte(tt.json))
			if err != nil || got != tt.want {
				t.Errorf("parseMarkerProbe = %+v, %v; want %+v", got, err, tt.want)
			}
		})
	}
}

func TestWriteCorruptedReportMergesSorted(t *testing.T) {
	cfg, songDir, outDir := newLibrary(t, false)
	a, b, c := filepath.Join(songDir, "a.avi"), filepath.Join(songDir, "b.avi"), filepath.Join(songDir, "c.avi")
	for _, f := range []string{a, b, c} {
		writeFile(t, f, "v")
	}
	report := filepath.Join(outDir, corruptedReportName)
	writeFile(t, report, b+"\n\n"+a+"\n")
	p := &processor{cfg: cfg, ops: fsops.New(false), corrupted: map[string]struct{}{}}
	p.flagCorrupted(c, "test")
	p.flagCorrupted(a, "test")
	p.writeCorruptedReport()

	got := readFile(t, report)
	if want := a + "\n" + b + "\n" + c + "\n"; got != want {
		t.Errorf("report = %q, want %q", got, want)
	}
}

// Regression: potentiallyCorruptedFiles.txt only ever grew, so videos that were fixed,
// converted or removed stayed listed forever. Entries of videos this run found readable
// and of files that are gone are dropped; a video this run did not read stays listed.
func TestWriteCorruptedReportDropsStaleEntries(t *testing.T) {
	p, songDir, outDir := newProcessor(t, false)
	still := filepath.Join(songDir, "Song", "still.avi")   // not read by this run: stays
	fixed := filepath.Join(songDir, "Song", "fixed.avi")   // journaled as checked: readable
	gone := filepath.Join(songDir, "Song", "gone.avi")     // converted or removed
	flagged := filepath.Join(songDir, "Song", "again.avi") // flagged again
	for _, f := range []string{still, fixed, flagged} {
		writeFile(t, f, "v "+f)
	}
	if err := p.journal.Add(journalChecked, fixed); err != nil {
		t.Fatal(err)
	}
	report := filepath.Join(outDir, corruptedReportName)
	writeFile(t, report, strings.Join([]string{fixed, gone, still, flagged}, "\n")+"\n")

	p.processVideoFile(filepath.Dir(fixed), fixed) // skipped via the journal, no ffmpeg needed
	p.flagCorrupted(flagged, "test")
	p.writeCorruptedReport()
	if got, want := readFile(t, report), flagged+"\n"+still+"\n"; got != want {
		t.Errorf("report = %q, want %q", got, want)
	}

	// Once nothing is left, the report is emptied rather than kept stale.
	if err := os.Remove(still); err != nil {
		t.Fatal(err)
	}
	p = &processor{cfg: p.cfg, ops: fsops.New(false), journal: p.journal, corrupted: map[string]struct{}{}} // the next run
	p.writeCorruptedReport()
	if got := readFile(t, report); got != flagged+"\n" {
		t.Errorf("report = %q, want only %q", got, flagged)
	}
	if err := os.Remove(flagged); err != nil {
		t.Fatal(err)
	}
	p.writeCorruptedReport()
	if got := readFile(t, report); got != "" {
		t.Errorf("report = %q, want it empty", got)
	}
}

// --- tests that need ffmpeg -------------------------------------------------------------

func TestReEncodeAviToMp4(t *testing.T) {
	requireFFmpeg(t)
	cfg, songDir, outDir := newLibrary(t, false)
	song := filepath.Join(songDir, "Artist", "Artist - Song")
	writeFile(t, filepath.Join(song, "song.txt"), "#TITLE:Song\n#ARTIST:Artist\n#MP3:video.avi\n#VIDEO:video.avi\n#VIDEOGAP:0\n: 0 4 0 La\nE\n")
	// mpeg4 at maximum quality: the codec needs re-encoding and the estimate is far below the input size.
	makeMedia(t, filepath.Join(song, "video.avi"), "-c:v", "mpeg4", "-q:v", "1", "-c:a", "ac3")
	if err := os.Chmod(filepath.Join(song, "video.avi"), 0o640); err != nil {
		t.Fatal(err)
	}

	ops := fsops.New(false)
	if err := Run(cfg, ops); err != nil {
		t.Fatal(err)
	}

	if st := ops.Stats(); st.Failed != 0 || st.Removed != 1 {
		t.Errorf("stats = %+v, want 1 removed, 0 failed", st)
	}
	if _, err := os.Stat(filepath.Join(song, "video.avi")); !os.IsNotExist(err) {
		t.Errorf("original video.avi still in song folder (err=%v)", err)
	}
	// The original of a conversion is deleted, not kept (design decision, see CLAUDE.md).
	if _, err := os.Stat(filepath.Join(outDir, "extra-files")); !os.IsNotExist(err) {
		t.Errorf("original must be deleted, not moved to extra-files (err=%v)", err)
	}
	mp4 := filepath.Join(song, "video.mp4")
	if got := probeStreams(t, mp4); !slices.Equal(got, []string{"video:h264", "audio:ac3"}) {
		t.Errorf("streams of video.mp4 = %v, want h264 video + ac3 audio", got)
	}
	assertTagged(t, mp4, true)
	txt := readFile(t, filepath.Join(song, "song.txt"))
	if want := "#TITLE:Song\n#ARTIST:Artist\n#MP3:video.mp4\n#VIDEO:video.mp4\n#VIDEOGAP:0\n: 0 4 0 La\nE\n"; txt != want {
		t.Errorf("txt = %q, want %q", txt, want)
	}
	if st, err := os.Stat(filepath.Join(song, "video.mp4")); err != nil || st.Mode().Perm() != 0o640 {
		t.Errorf("converted video must keep the original permissions 0640: %v, %v", st, err)
	}
	assertNoTempFiles(t, songDir)
}

// If the txt cannot be pointed at the new file, the conversion is undone and the original
// stays referenced in place.
func TestReEncodeUpdatesReferenceSpelledDifferently(t *testing.T) {
	requireFFmpeg(t)
	cfg, songDir, outDir := newLibrary(t, false)
	song := filepath.Join(songDir, "A - S")
	// Referenced in a different case; the tag-aware replacement still updates it.
	writeFile(t, filepath.Join(song, "song.txt"), "#TITLE:S\n#ARTIST:A\n#VIDEO:VIDEO.AVI\n")
	makeMedia(t, filepath.Join(song, "video.avi"), "-c:v", "mpeg4", "-q:v", "1", "-c:a", "ac3")

	ops := fsops.New(false)
	if err := Run(cfg, ops); err != nil {
		t.Fatal(err)
	}
	if st := ops.Stats(); st.Failed != 0 {
		t.Errorf("Failed = %d, want 0", st.Failed)
	}
	if got := readFile(t, filepath.Join(song, "song.txt")); got != "#TITLE:S\n#ARTIST:A\n#VIDEO:video.mp4\n" {
		t.Errorf("txt = %q", got)
	}
	if _, err := os.Stat(filepath.Join(song, "video.avi")); !os.IsNotExist(err) {
		t.Errorf("original must be deleted (err=%v)", err)
	}
	if _, err := os.Stat(filepath.Join(outDir, "extra-files")); !os.IsNotExist(err) {
		t.Errorf("nothing must be moved to extra-files (err=%v)", err)
	}
	assertNoTempFiles(t, songDir)
}

func TestUnreferencedVideoIsNotConverted(t *testing.T) {
	requireFFmpeg(t)
	cfg, songDir, outDir := newLibrary(t, false)
	song := filepath.Join(songDir, "A - S")
	txt := "#TITLE:S\n#ARTIST:A\n#VIDEO:other.mp4\n"
	writeFile(t, filepath.Join(song, "song.txt"), txt)
	makeMedia(t, filepath.Join(song, "video.avi"), "-c:v", "mpeg4", "-q:v", "1", "-c:a", "ac3")

	ops := fsops.New(false)
	if err := Run(cfg, ops); err != nil {
		t.Fatal(err)
	}
	if st := ops.Stats(); st.Failed != 0 || st.Replaced != 0 || st.Moved != 0 {
		t.Errorf("Stats = %+v, want nothing done", st)
	}
	if _, err := os.Stat(filepath.Join(song, "video.avi")); err != nil {
		t.Errorf("original must stay in place: %v", err)
	}
	if _, err := os.Stat(filepath.Join(song, "video.mp4")); !os.IsNotExist(err) {
		t.Errorf("no video.mp4 must be created (err=%v)", err)
	}
	if got := readFile(t, filepath.Join(song, "song.txt")); got != txt {
		t.Errorf("txt changed to %q", got)
	}
	if _, err := os.Stat(filepath.Join(outDir, "extra-files")); !os.IsNotExist(err) {
		t.Errorf("nothing must be moved to extra-files (err=%v)", err)
	}
	assertNoTempFiles(t, songDir)
}

func TestStripAudio(t *testing.T) {
	requireFFmpeg(t)
	tests := []struct {
		name      string
		txt       string
		wantAudio bool
	}{
		{"separate audio referenced", "#TITLE:S\n#ARTIST:A\n#MP3:song.mp3\n#VIDEO:video.mp4\n", false},
		{"separate audio not referenced", "#TITLE:S\n#ARTIST:A\n#MP3:video.mp4\n#VIDEO:video.mp4\n", true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg, songDir, _ := newLibrary(t, false)
			song := filepath.Join(songDir, "A - S")
			writeFile(t, filepath.Join(song, "song.txt"), tt.txt)
			writeFile(t, filepath.Join(song, "song.mp3"), "not checked")
			// Low-bitrate H.264 does not need re-encoding, so only the audio decision matters.
			makeMedia(t, filepath.Join(song, "video.mp4"), "-c:v", "libx264", "-crf", "35", "-c:a", "aac")

			ops := fsops.New(false)
			if err := Run(cfg, ops); err != nil {
				t.Fatal(err)
			}
			if st := ops.Stats(); st.Failed != 0 {
				t.Errorf("Failed = %d, want 0", st.Failed)
			}
			streams := probeStreams(t, filepath.Join(song, "video.mp4"))
			if hasAudio := slices.Contains(streams, "audio:aac"); hasAudio != tt.wantAudio {
				t.Errorf("streams = %v, want audio = %v", streams, tt.wantAudio)
			}
			assertNoTempFiles(t, songDir)
		})
	}
}

func TestCorruptedVideoIsReported(t *testing.T) {
	requireFFmpeg(t)
	cfg, songDir, outDir := newLibrary(t, false)
	song := filepath.Join(songDir, "A - S")
	writeFile(t, filepath.Join(song, "song.txt"), "#TITLE:S\n#ARTIST:A\n#VIDEO:video.avi\n")
	bad := filepath.Join(song, "video.avi")
	writeFile(t, bad, strings.Repeat("garbage", 200))

	ops := fsops.New(false)
	if err := Run(cfg, ops); err != nil {
		t.Fatal(err)
	}
	if st := ops.Stats(); st.Failed != 0 {
		t.Errorf("Failed = %d, want 0 (corrupted input is reported, not a failure)", st.Failed)
	}
	if got, want := readFile(t, filepath.Join(outDir, corruptedReportName)), bad+"\n"; got != want {
		t.Errorf("report = %q, want %q", got, want)
	}
	if _, err := os.Stat(bad); err != nil {
		t.Errorf("corrupted original must stay in place: %v", err)
	}
	assertNoTempFiles(t, songDir)
}

func TestDryRunLeavesTreeIdentical(t *testing.T) {
	requireFFmpeg(t)
	cfg, songDir, outDir := newLibrary(t, true)
	song := filepath.Join(songDir, "A - S")
	writeFile(t, filepath.Join(song, "song.txt"), "#TITLE:S\n#ARTIST:A\n#MP3:song.mp3\n#VIDEO:video.avi\n")
	writeFile(t, filepath.Join(song, "song.mp3"), "audio")
	makeMedia(t, filepath.Join(song, "video.avi"), "-c:v", "mpeg4", "-q:v", "1", "-c:a", "ac3")

	before := snapshot(t, songDir)
	ops := fsops.New(true)
	if err := Run(cfg, ops); err != nil {
		t.Fatal(err)
	}
	if st := ops.Stats(); st.Replaced != 1 || st.Failed != 0 {
		t.Errorf("Stats = %+v, want 1 planned replacement", st)
	}
	if after := snapshot(t, songDir); !slices.Equal(before, after) {
		t.Errorf("dry run changed the library\nbefore: %v\n after: %v", before, after)
	}
	if entries, _ := os.ReadDir(outDir); len(entries) != 0 {
		t.Errorf("dry run created output: %v", entries)
	}
}

func TestTaggedVideoIsSkipped(t *testing.T) {
	requireFFmpeg(t)
	cfg, songDir, _ := newLibrary(t, false)
	song := filepath.Join(songDir, "A - S")
	writeFile(t, filepath.Join(song, "song.txt"), "#TITLE:S\n#ARTIST:A\n#MP3:song.mp3\n#VIDEO:video.mp4\n")
	writeFile(t, filepath.Join(song, "song.mp3"), "audio")
	// Would be re-encoded (mpeg4) and stripped of its audio, but an earlier run tagged it.
	makeMedia(t, filepath.Join(song, "video.mp4"), "-c:v", "mpeg4", "-q:v", "1", "-c:a", "aac",
		"-metadata", "SANITIZER_VIDEO=processed", "-movflags", "+use_metadata_tags")

	before := snapshot(t, songDir)
	ops := fsops.New(false)
	if err := Run(cfg, ops); err != nil {
		t.Fatal(err)
	}
	if st := ops.Stats(); st != (fsops.Stats{}) {
		t.Errorf("Stats = %+v, want nothing done", st)
	}
	if after := snapshot(t, songDir); !slices.Equal(before, after) {
		t.Errorf("tagged video was changed\nbefore: %v\n after: %v", before, after)
	}
}

// A video whose re-encode is larger than the original is not re-encoded on every run: the
// original gets the tag by a lossless remux.
func TestLargerResultTagsOriginal(t *testing.T) {
	requireFFmpeg(t)
	cfg, songDir, outDir := newLibrary(t, false)
	song := filepath.Join(songDir, "A - S")
	txt := "#TITLE:S\n#ARTIST:A\n#VIDEO:video.mkv\n"
	writeFile(t, filepath.Join(song, "song.txt"), txt)
	// VP9 needs re-encoding, but the tiny VP9 stream is smaller than its H.264 re-encode;
	// the big audio track keeps the size estimate below the input size.
	video := filepath.Join(song, "video.mkv")
	makeMediaFrom(t, video, "testsrc2=size=320x240:rate=25:duration=1",
		"-c:v", "libvpx-vp9", "-crf", "63", "-b:v", "0", "-c:a", "ac3", "-b:a", "448k")

	ops := fsops.New(false)
	if err := Run(cfg, ops); err != nil {
		t.Fatal(err)
	}
	if st := ops.Stats(); st.Failed != 0 || st.Replaced != 1 || st.Moved != 0 {
		t.Errorf("Stats = %+v, want only the tagging remux", st)
	}
	if _, err := os.Stat(filepath.Join(song, "video.mp4")); !os.IsNotExist(err) {
		t.Fatalf("the larger re-encode must be discarded (err=%v)", err)
	}
	assertTagged(t, video, true)
	if got := probeStreams(t, video); !slices.Equal(got, []string{"video:vp9", "audio:ac3"}) {
		t.Errorf("streams after remux = %v, want the original vp9 + ac3", got)
	}
	if got := readFile(t, filepath.Join(song, "song.txt")); got != txt {
		t.Errorf("txt changed to %q", got)
	}
	if _, err := os.Stat(filepath.Join(outDir, "media-journal.txt")); !os.IsNotExist(err) {
		t.Errorf("a tagged video needs no journal entry (err=%v)", err)
	}
	assertNoTempFiles(t, songDir)

	// The second run leaves the video alone.
	before := snapshot(t, songDir)
	ops = fsops.New(false)
	if err := Run(cfg, ops); err != nil {
		t.Fatal(err)
	}
	if st := ops.Stats(); st != (fsops.Stats{}) {
		t.Errorf("second run Stats = %+v, want nothing done", st)
	}
	if after := snapshot(t, songDir); !slices.Equal(before, after) {
		t.Errorf("second run changed the library\nbefore: %v\n after: %v", before, after)
	}
}

// An AVI cannot carry the tag, so a checked AVI is recorded in the media journal and
// skipped by later runs.
func TestCheckedAviIsJournaled(t *testing.T) {
	requireFFmpeg(t)
	cfg, songDir, _ := newLibrary(t, false)
	song := filepath.Join(songDir, "A - S")
	txt := "#TITLE:S\n#ARTIST:A\n#VIDEO:video.avi\n"
	writeFile(t, filepath.Join(song, "song.txt"), txt)
	video := filepath.Join(song, "video.avi")
	makeMedia(t, video, "-c:v", "mpeg4", "-q:v", "1", "-c:a", "ac3") // would be re-encoded

	j, err := journal.Open(cfg.JournalPath())
	if err != nil {
		t.Fatal(err)
	}
	ops := fsops.New(false)
	p := &processor{cfg: cfg, ops: ops, journal: j, corrupted: map[string]struct{}{}}
	p.markChecked(video)
	if err := j.Save(ops); err != nil {
		t.Fatal(err)
	}
	if st := ops.Stats(); st.Failed != 0 || st.Replaced != 0 {
		t.Errorf("Stats = %+v, want no replacement (AVI is not remuxed)", st)
	}
	if got := readFile(t, cfg.JournalPath()); !strings.Contains(got, "\nvideo-checked\t") {
		t.Errorf("journal = %q, want a video-checked entry", got)
	}

	before := snapshot(t, songDir)
	ops = fsops.New(false)
	if err := Run(cfg, ops); err != nil {
		t.Fatal(err)
	}
	if st := ops.Stats(); st.Replaced != 0 || st.Moved != 0 || st.Written != 0 || st.Failed != 0 {
		t.Errorf("Stats = %+v, want the journaled video skipped", st)
	}
	if after := snapshot(t, songDir); !slices.Equal(before, after) {
		t.Errorf("journaled video was changed\nbefore: %v\n after: %v", before, after)
	}
}

// A remux that does not carry the tag (simulated by treating AVI as tag-capable) is
// discarded; the original is kept and recorded in the journal instead.
func TestUnverifiedRemuxFallsBackToJournal(t *testing.T) {
	requireFFmpeg(t)
	markerExts[".avi"] = true
	t.Cleanup(func() { delete(markerExts, ".avi") })
	cfg, songDir, _ := newLibrary(t, false)
	video := filepath.Join(songDir, "A - S", "video.avi")
	writeFile(t, filepath.Join(songDir, "A - S", "song.txt"), "#TITLE:S\n#ARTIST:A\n#VIDEO:video.avi\n")
	makeMedia(t, video, "-c:v", "mpeg4", "-q:v", "1", "-c:a", "ac3")
	before := snapshot(t, video)

	j, err := journal.Open(cfg.JournalPath())
	if err != nil {
		t.Fatal(err)
	}
	ops := fsops.New(false)
	p := &processor{cfg: cfg, ops: ops, journal: j, corrupted: map[string]struct{}{}}
	p.markChecked(video)
	if st := ops.Stats(); st.Replaced != 0 || st.Failed != 0 {
		t.Errorf("Stats = %+v, the unverified remux must not replace the original", st)
	}
	if after := snapshot(t, video); !slices.Equal(before, after) {
		t.Errorf("original changed\nbefore: %v\n after: %v", before, after)
	}
	if !j.Has("video-checked", video) {
		t.Error("original not recorded as checked in the journal")
	}
	assertNoTempFiles(t, songDir)
}

// Stripping the audio from an AVI copies the video stream into an AVI, which cannot carry
// the tag; the result is recorded in the media journal.
func TestStrippedAviIsJournaled(t *testing.T) {
	requireFFmpeg(t)
	cfg, songDir, _ := newLibrary(t, false)
	song := filepath.Join(songDir, "A - S")
	writeFile(t, filepath.Join(song, "song.txt"), "#TITLE:S\n#ARTIST:A\n#MP3:song.mp3\n#VIDEO:video.avi\n")
	writeFile(t, filepath.Join(song, "song.mp3"), "audio")
	video := filepath.Join(song, "video.avi")
	makeMedia(t, video, "-c:v", "libx264", "-crf", "35", "-c:a", "ac3")

	ops := fsops.New(false)
	if err := Run(cfg, ops); err != nil {
		t.Fatal(err)
	}
	if st := ops.Stats(); st.Failed != 0 || st.Replaced != 1 {
		t.Errorf("Stats = %+v, want 1 replacement", st)
	}
	if got := probeStreams(t, video); !slices.Equal(got, []string{"video:h264"}) {
		t.Errorf("streams = %v, want video only", got)
	}
	j, err := journal.Open(cfg.JournalPath())
	if err != nil {
		t.Fatal(err)
	}
	if !j.Has("video-processed", video) {
		t.Errorf("journal %q has no video-processed entry for the stripped AVI", readFile(t, cfg.JournalPath()))
	}
}

// TestNeedsRemoveAudioSeesPlannedTxt: in a dry run, the strip-audio decision reads the song
// txt as fixtxt would write it. On disk #MP3 names the video (swapped tags), so the video's
// audio would be kept; the planned txt names the separate song.mp3, so the real run strips
// it, and the dry run must plan the same.
func TestNeedsRemoveAudioSeesPlannedTxt(t *testing.T) {
	requireFFmpeg(t)
	_, songDir, _ := newLibrary(t, false)
	song := filepath.Join(songDir, "A - S")
	txt := filepath.Join(song, "song.txt")
	writeFile(t, txt, "#TITLE:S\n#ARTIST:A\n#MP3:video.mp4\n#VIDEO:song.mp3\n")
	writeFile(t, filepath.Join(song, "song.mp3"), "audio")
	video := filepath.Join(song, "video.mp4")
	makeMedia(t, video, "-c:v", "libx264", "-crf", "35", "-c:a", "aac")

	ops := fsops.New(true)
	if needsRemoveAudio(ops, song, video) {
		t.Error("before the planned fix: needsRemoveAudio = true, but #MP3 names the video")
	}
	if _, err := ops.WriteFileAtomic(txt, []byte("#TITLE:S\n#ARTIST:A\n#MP3:song.mp3\n#VIDEO:video.mp4\n")); err != nil {
		t.Fatal(err)
	}
	if !needsRemoveAudio(ops, song, video) {
		t.Error("with the planned txt: needsRemoveAudio = false, want true")
	}
	if !isVideoReferenced(ops, song, video) || isVideoReferenced(nil, song, video) {
		t.Error("isVideoReferenced does not follow the planned txt")
	}
}

// A hidden file named by #MP3 (e.g. "._v.mp3" left by old versions) is not a separate audio
// file, so the video keeps its audio.
func TestHiddenAudioReferenceKeepsVideoAudio(t *testing.T) {
	requireFFmpeg(t)
	cfg, songDir, _ := newLibrary(t, false)
	song := filepath.Join(songDir, "A - S")
	writeFile(t, filepath.Join(song, "song.txt"), "#TITLE:S\n#ARTIST:A\n#MP3:._v.mp3\n#VIDEO:video.mp4\n")
	writeFile(t, filepath.Join(song, "._v.mp3"), "AppleDouble")
	video := filepath.Join(song, "video.mp4")
	makeMedia(t, video, "-c:v", "libx264", "-crf", "35", "-c:a", "aac")

	if needsRemoveAudio(nil, song, video) {
		t.Error("needsRemoveAudio = true for a hidden #MP3 reference")
	}
	ops := fsops.New(false)
	if err := Run(cfg, ops); err != nil {
		t.Fatal(err)
	}
	if st := ops.Stats(); st.Replaced != 0 || st.Failed != 0 {
		t.Errorf("Stats = %+v, want nothing replaced", st)
	}
	if got := probeStreams(t, video); !slices.Contains(got, "audio:aac") {
		t.Errorf("streams = %v, the video's audio must be kept", got)
	}
}

// v.MP4 re-encoded to .mp4 only changes the extension's case: the original is replaced in
// place (on case-insensitive file systems "v.mp4" is the original itself).
func TestCaseOnlyExtensionChangeReplacesInPlace(t *testing.T) {
	requireFFmpeg(t)
	cfg, songDir, outDir := newLibrary(t, false)
	song := filepath.Join(songDir, "A - S")
	txt := "#TITLE:S\n#ARTIST:A\n#VIDEO:video.MP4\n"
	writeFile(t, filepath.Join(song, "song.txt"), txt)
	video := filepath.Join(song, "video.MP4")
	makeMediaFrom(t, video, "testsrc=size=320x240:rate=25:duration=1", "-f", "mp4", "-c:v", "mpeg4", "-q:v", "1", "-c:a", "aac")

	ops := fsops.New(false)
	if err := Run(cfg, ops); err != nil {
		t.Fatal(err)
	}
	if st := ops.Stats(); st.Failed != 0 || st.Replaced != 1 || st.Moved != 0 || st.Written != 0 {
		t.Errorf("Stats = %+v, want 1 in-place replacement", st)
	}
	if got := dirNames(t, song); !slices.Equal(got, []string{"song.txt", "video.MP4"}) {
		t.Errorf("song folder = %v, want song.txt and video.MP4", got)
	}
	if got := probeStreams(t, video); !slices.Equal(got, []string{"video:h264", "audio:aac"}) {
		t.Errorf("streams = %v, want h264 + aac", got)
	}
	assertTagged(t, video, true)
	if got := readFile(t, filepath.Join(song, "song.txt")); got != txt {
		t.Errorf("txt changed to %q", got)
	}
	if _, err := os.Stat(filepath.Join(outDir, "extra-files")); !os.IsNotExist(err) {
		t.Errorf("nothing must be moved to extra-files (err=%v)", err)
	}
}

// Portrait videos taller than the limit are downscaled too, keeping the aspect ratio.
func TestDownscalePortraitVideo(t *testing.T) {
	requireFFmpeg(t)
	lowerLimits(t, 320, 180) // stands in for 1920x1080
	cfg, songDir, _ := newLibrary(t, false)
	song := filepath.Join(songDir, "A - S")
	writeFile(t, filepath.Join(song, "song.txt"), "#TITLE:S\n#ARTIST:A\n#VIDEO:video.mp4\n")
	video := filepath.Join(song, "video.mp4")
	// 180x320 is narrower than 320 but taller than 180; H.264 needs no re-encode otherwise.
	makeMediaFrom(t, video, "testsrc=size=180x320:rate=25:duration=1", "-c:v", "libx264", "-crf", "10", "-c:a", "aac")

	ops := fsops.New(false)
	if err := Run(cfg, ops); err != nil {
		t.Fatal(err)
	}
	if st := ops.Stats(); st.Failed != 0 || st.Replaced != 1 {
		t.Errorf("Stats = %+v, want 1 replacement", st)
	}
	w, h, err := getVideoResolution(video)
	if err != nil {
		t.Fatal(err)
	}
	if w > 320 || h > 180 || w%2 != 0 || h%2 != 0 || h != 180 || w < 98 || w > 102 {
		t.Errorf("resolution = %dx%d, want ~100x180 (fits 320x180, even, aspect ratio kept)", w, h)
	}
}

// In dry-run mode, videos an earlier module would move away are not planned.
func TestDryRunSkipsPlannedMoves(t *testing.T) {
	requireFFmpeg(t)
	cfg, songDir, _ := newLibrary(t, true)
	song := filepath.Join(songDir, "A - S")
	writeFile(t, filepath.Join(song, "song.txt"), "#TITLE:S\n#ARTIST:A\n#VIDEO:video.avi\n")
	makeMedia(t, filepath.Join(song, "video.avi"), "-c:v", "mpeg4", "-q:v", "1", "-c:a", "ac3")

	ops := fsops.New(true)
	// cleanup would move the video to extra-files.
	if _, err := ops.MoveInto(filepath.Join(song, "video.avi"), cfg.ExtraFilesDir(), "A - S/video.avi"); err != nil {
		t.Fatal(err)
	}
	if err := Run(cfg, ops); err != nil {
		t.Fatal(err)
	}
	if st := ops.Stats(); st.Replaced != 0 || st.Failed != 0 {
		t.Errorf("Stats = %+v, want no planned replacement", st)
	}
}

// The worker pool processes every video of every folder exactly once.
func TestProcessesAllVideosConcurrently(t *testing.T) {
	requireFFmpeg(t)
	cfg, songDir, _ := newLibrary(t, false)
	src := filepath.Join(t.TempDir(), "v.mp4")
	makeMedia(t, src, "-c:v", "libx264", "-crf", "35", "-c:a", "aac")
	data := readFile(t, src)
	const songs = 7
	var videos []string
	for i := range songs {
		song := filepath.Join(songDir, fmt.Sprintf("A - S%d", i))
		writeFile(t, filepath.Join(song, "song.txt"), "#TITLE:S\n#ARTIST:A\n#MP3:song.mp3\n#VIDEO:video.mp4\n")
		writeFile(t, filepath.Join(song, "song.mp3"), "audio")
		writeFile(t, filepath.Join(song, "video.mp4"), data)
		videos = append(videos, filepath.Join(song, "video.mp4"))
	}

	ops := fsops.New(false)
	if err := Run(cfg, ops); err != nil {
		t.Fatal(err)
	}
	if st := ops.Stats(); st.Failed != 0 || st.Replaced != songs {
		t.Errorf("Stats = %+v, want %d replacements", st, songs)
	}
	for _, v := range videos {
		if got := probeStreams(t, v); !slices.Equal(got, []string{"video:h264"}) {
			t.Errorf("streams of %s = %v, want the audio stripped", v, got)
		}
	}
	assertNoTempFiles(t, songDir)
}

// --- helpers ------------------------------------------------------------------------------

// lowerLimits sets the downscale limits for the duration of the test.
func lowerLimits(t *testing.T, w, h int) {
	t.Helper()
	oldW, oldH := maxWidth, maxHeight
	maxWidth, maxHeight = w, h
	t.Cleanup(func() { maxWidth, maxHeight = oldW, oldH })
}

func assertTagged(t *testing.T, file string, want bool) {
	t.Helper()
	m, err := probeMarker(file)
	if err != nil {
		t.Fatalf("probing %s: %v", file, err)
	}
	if m.tagged != want {
		t.Errorf("%s tagged = %v, want %v", file, m.tagged, want)
	}
}

func dirNames(t *testing.T, dir string) []string {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, e := range entries {
		names = append(names, e.Name())
	}
	return names
}

func newLibrary(t *testing.T, dryRun bool) (cfg config.Config, songDir, outDir string) {
	t.Helper()
	songDir, outDir = t.TempDir(), t.TempDir()
	cfg, err := config.New(songDir, outDir, dryRun)
	if err != nil {
		t.Fatal(err)
	}
	return cfg, cfg.SongDir, cfg.OutDir
}

func requireFFmpeg(t *testing.T) {
	t.Helper()
	for _, tool := range []string{"ffmpeg", "ffprobe"} {
		if _, err := exec.LookPath(tool); err != nil {
			t.Skipf("%s not available: %v", tool, err)
		}
	}
}

// makeMedia creates a 1 s 320x240 test video with a sine tone, encoded with codecArgs.
func makeMedia(t *testing.T, path string, codecArgs ...string) {
	t.Helper()
	makeMediaFrom(t, path, "testsrc=size=320x240:rate=25:duration=1", codecArgs...)
}

// makeMediaFrom creates a test video from the lavfi video source with a 1 s sine tone.
func makeMediaFrom(t *testing.T, path, videoSrc string, codecArgs ...string) {
	t.Helper()
	args := slices.Concat([]string{
		"-hide_banner", "-nostdin", "-loglevel", "error",
		"-f", "lavfi", "-i", videoSrc,
		"-f", "lavfi", "-i", "sine=frequency=440:duration=1",
	}, codecArgs, []string{"-y", path})
	if out, err := exec.Command("ffmpeg", args...).CombinedOutput(); err != nil {
		t.Skipf("cannot generate test media (%v): %s", err, out)
	}
}

// probeStreams returns "type:codec" for every stream of file.
func probeStreams(t *testing.T, file string) []string {
	t.Helper()
	out, err := exec.Command("ffprobe", "-v", "error", "-show_entries", "stream=codec_type,codec_name", "-of", "csv=p=0", file).Output()
	if err != nil {
		t.Fatalf("ffprobe %s: %v", file, err)
	}
	var streams []string
	for _, line := range strings.Fields(string(out)) {
		f := strings.Split(strings.TrimSuffix(line, ","), ",")
		if len(f) >= 2 {
			streams = append(streams, f[1]+":"+f[0])
		}
	}
	return streams
}

func assertNoTempFiles(t *testing.T, root string) {
	t.Helper()
	filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err == nil && strings.HasPrefix(d.Name(), ".") && strings.Contains(d.Name(), ".sanitizer-") {
			t.Errorf("temp file left behind: %s", path)
		}
		return nil
	})
}

// snapshot lists every entry below root with its mode, size, mtime and content hash.
func snapshot(t *testing.T, root string) []string {
	t.Helper()
	var entries []string
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		info, err := d.Info()
		if err != nil {
			return err
		}
		entry := fmt.Sprintf("%s %v %d %d", path, info.Mode(), info.Size(), info.ModTime().UnixNano())
		if info.Mode().IsRegular() {
			entry += fmt.Sprintf(" %x", sha256.Sum256([]byte(readFile(t, path))))
		}
		entries = append(entries, entry)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return entries
}

func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func readFile(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}
