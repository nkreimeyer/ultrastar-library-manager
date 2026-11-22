package fixtxt

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/nkreimeyer/library-manager/fsops"
	"github.com/nkreimeyer/library-manager/internal/testutil"
	"github.com/nkreimeyer/library-manager/songtxt"
	log "github.com/sirupsen/logrus"
	logtest "github.com/sirupsen/logrus/hooks/test"
)

// messages returns the logged messages that contain substr.
func messages(hook *logtest.Hook, substr string) []string {
	var got []string
	for _, e := range hook.AllEntries() {
		if strings.Contains(e.Message, substr) {
			got = append(got, e.Message)
		}
	}
	return got
}

// tagValues returns the values of the tags with one of keys, as songtxt (and players) read them.
func tagValues(t *testing.T, txt string, keys ...string) []string {
	t.Helper()
	s, _ := songtxt.Parse(txt)
	var got []string
	for _, tg := range s.Tags {
		for _, k := range keys {
			if strings.EqualFold(tg.Key, k) {
				got = append(got, tg.Value)
			}
		}
	}
	return got
}

// Regression: a UTF-32LE txt was read as UTF-16LE without a Problem (its BOM starts with
// the UTF-16LE one), so fixtxt rewrote it as UTF-8 with a NUL after every character. Such
// txts, and UTF-16 with a U+0000, are listed in encoding_issues.txt and left as they are.
func TestRunLeavesUTF32AndUTF16WithNULUnchanged(t *testing.T) {
	cfg := newLibrary(t, false)
	utf32 := []byte{0xff, 0xfe, 0, 0}
	for _, c := range []byte("#TITLE:T\n#ARTIST:A\n#MP3:old.mp3\n: 0 1 0 x\nE\n") {
		utf32 = append(utf32, c, 0, 0, 0)
	}
	files := map[string]string{
		"Utf32/song.txt": string(utf32),
		"Utf32/song.mp3": "audio",
		"Utf16/song.txt": utf16LE("#TITLE:T\x00\n#ARTIST:A\n#MP3:song.mp3\n: 0 1 0 x\nE\n"),
		"Utf16/song.mp3": "audio",
	}
	testutil.WriteFiles(t, cfg.SongDir, files)
	run(t, cfg)
	report := readReport(t, cfg, encodingIssuesReport)
	for _, rel := range []string{"Utf32/song.txt", "Utf16/song.txt"} {
		if got := testutil.ReadFile(t, filepath.Join(cfg.SongDir, rel)); got != files[rel] {
			t.Errorf("%s = %q, want it unchanged", rel, got)
		}
		if !strings.Contains(report, filepath.Join(cfg.SongDir, rel)+": ") {
			t.Errorf("encoding_issues.txt = %q, want a line for %s", report, rel)
		}
	}
}

// Regression: a "#" line after the notes counted as the last occurrence of a header tag, so
// the header #BPM was dropped and the ignored body copy kept.
func TestRemoveDuplicateTagsIgnoresBody(t *testing.T) {
	in := []string{"#TITLE:T\n", "#ARTIST:A\n", "#BPM:300\n", ": 0 2 0 la\n", "#BPM:30\n", "E\n"}
	got, _, _ := fixTxtLines(in, t.TempDir(), songFiles{}, DefaultOptions())
	if !slices.Equal(got, in) {
		t.Errorf("fixTxtLines = %q, want the lines unchanged %q", got, in)
	}
}

func TestRunKeepsHeaderTagWhenRepeatedAfterEnd(t *testing.T) {
	cfg := newLibrary(t, false)
	txt := "#TITLE:Song\n#ARTIST:Artist\n#MP3:a.mp3\n#BPM:300\n: 0 4 0 La\n#MP3:b.mp3\nE\n#BPM:30\n#mp3:c.mp3\n"
	testutil.WriteFiles(t, cfg.SongDir, map[string]string{"Artist - Song/song.txt": txt, "Artist - Song/a.mp3": "x"})
	run(t, cfg)
	if got := testutil.ReadFile(t, filepath.Join(cfg.SongDir, "Artist - Song", "song.txt")); got != txt {
		t.Errorf("txt changed:\n%s\nwant:\n%s", got, txt)
	}
}

// Note lines (also with ":" in the lyrics) and "#" lines in the body are copied unchanged.
func TestFixTxtLinesKeepsBody(t *testing.T) {
	dir := t.TempDir()
	testutil.WriteFiles(t, dir, map[string]string{"a.mp3": "a"})
	in := []string{"#TITLE:T\n", "#ARTIST:A\n", "#MP3:gone.mp3\n", ": 0 4 0 Hey:\n", ": 4 4 0 you\n", "- 8\n", ": 10 2 0 la\n", "#VIDEO:x.mp4\n", "E\n"}
	got, _, _ := fixTxtLines(in, dir, songFiles{audio: []string{"a.mp3"}}, DefaultOptions())
	want := slices.Clone(in)
	want[2] = "#MP3:a.mp3\n"
	if !slices.Equal(got, want) {
		t.Errorf("fixTxtLines = %q, want %q", got, want)
	}
}

// A txt without any header line gets the inserted tags at the top, without a panic.
func TestFixTxtLinesWithoutHeader(t *testing.T) {
	dir := t.TempDir()
	in := []string{": 0 4 0 La\n", ": 4 4 0 la\n", "E\n"}
	got, _, _ := fixTxtLines(in, dir, songFiles{audio: []string{"a.mp3"}}, DefaultOptions())
	want := append([]string{"#MP3:a.mp3\n"}, in...)
	if !slices.Equal(got, want) {
		t.Errorf("fixTxtLines = %q, want %q", got, want)
	}
}

// Txts with fewer than 3 lines are returned unchanged; from 3 lines on they are fixed.
func TestFixTxtLinesShortFiles(t *testing.T) {
	dir := t.TempDir()
	testutil.WriteFiles(t, dir, map[string]string{"a.mp3": "a"})
	files := songFiles{audio: []string{"a.mp3"}}
	two := []string{"#TITLE:x\n", "#MP3:gone.mp3\n"}
	if got, notes, filled := fixTxtLines(slices.Clone(two), dir, files, DefaultOptions()); !slices.Equal(got, two) || notes != nil || filled != nil {
		t.Errorf("2 lines: fixTxtLines = %q, %q, %q, want the lines unchanged", got, notes, filled)
	}
	three := []string{"#TITLE:x\n", "#ARTIST:y\n", "#MP3:gone.mp3\n"}
	want := []string{"#TITLE:x\n", "#ARTIST:y\n", "#MP3:a.mp3\n"}
	if got, _, _ := fixTxtLines(three, dir, files, DefaultOptions()); !slices.Equal(got, want) {
		t.Errorf("3 lines: fixTxtLines = %q, want %q", got, want)
	}
}

// Regression: a symlinked song txt was not seen by fixtxt, and the working song was moved to
// invalid/. Now the folder stays, the link is never replaced by a regular file, and a link
// whose txt needs changes is noted.
func TestRunKeepsSongWithSymlinkedTxt(t *testing.T) {
	for _, tc := range []struct {
		name, mp3 string
		wantNote  bool
	}{
		{"valid txt", "#MP3:song.mp3", false},
		{"txt needs a fix", "#MP3:gone.mp3", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cfg := newLibrary(t, false)
			ext := t.TempDir()
			target := filepath.Join(ext, "real.txt")
			txt := songTxt(tc.mp3)
			testutil.WriteFiles(t, ext, map[string]string{"real.txt": txt})
			testutil.WriteFiles(t, cfg.SongDir, map[string]string{"Song/song.mp3": "audio"})
			link := filepath.Join(cfg.SongDir, "Song", "song.txt")
			if err := os.Symlink(target, link); err != nil {
				t.Fatal(err)
			}
			ops := run(t, cfg)
			if !testutil.Exists(filepath.Join(cfg.SongDir, "Song", "song.mp3")) {
				t.Fatal("working song with a symlinked txt was moved away")
			}
			if st, err := os.Lstat(link); err != nil || st.Mode()&os.ModeSymlink == 0 {
				t.Errorf("song txt is no longer a symlink (err=%v)", err)
			}
			if got := testutil.ReadFile(t, target); got != txt {
				t.Errorf("link target was changed: %q", got)
			}
			report := testutil.ReadFile(t, cfg.ReportPath(missingFilesReport))
			if got := strings.Contains(report, "Song txt is a symlink and needs changes, replace it by the file it points to: "+link+"\n"); got != tc.wantNote {
				t.Errorf("symlink noted = %v, want %v:\n%s", got, tc.wantNote, report)
			}
			if st := ops.Stats(); st.Written != 0 || st.Moved != 0 || st.Failed != 0 {
				t.Errorf("Stats() = %+v, want nothing written, moved or failed", st)
			}
		})
	}
}

// Regression: file tags with a leading blank or a blank before the colon were not seen, so a
// second tag was inserted above them and the broken old value still won.
func TestRunFixesFileTagWithBlanks(t *testing.T) {
	media := map[string]string{"song.mp3": "audio", "clip.mp4": "video"}
	for _, tc := range []struct {
		line string
		keys []string
		want string
	}{
		{" #MP3:gone.mp3", []string{"MP3", "AUDIO"}, "song.mp3"},
		{"#MP3 :gone.mp3", []string{"MP3", "AUDIO"}, "song.mp3"},
		{"\t#audio\t: gone.ogg", []string{"MP3", "AUDIO"}, "song.mp3"},
		{" #VIDEO :gone.mp4", []string{"VIDEO"}, "clip.mp4"},
		{"  #Cover:gone.png", []string{"COVER"}, "sq.png"},
		{"#BACKGROUND  :gone.png", []string{"BACKGROUND"}, "wide.png"},
	} {
		t.Run(tc.line, func(t *testing.T) {
			cfg := newLibrary(t, false)
			files := map[string]string{"Artist - Song/song.txt": songTxt(tc.line)}
			for name, content := range media {
				files["Artist - Song/"+name] = content
			}
			testutil.WriteFiles(t, cfg.SongDir, files)
			testutil.WritePNG(t, filepath.Join(cfg.SongDir, "Artist - Song", "sq.png"), 10, 10)
			testutil.WritePNG(t, filepath.Join(cfg.SongDir, "Artist - Song", "wide.png"), 64, 36)
			run(t, cfg)
			got := testutil.ReadFile(t, filepath.Join(cfg.SongDir, "Artist - Song", "song.txt"))
			if v := tagValues(t, got, tc.keys...); !slices.Equal(v, []string{tc.want}) {
				t.Errorf("%v tags = %q, want exactly [%s]; txt:\n%s", tc.keys, v, tc.want, got)
			}
		})
	}
}

// Blank-separated repetitions of a tag are duplicates, too (the last one wins).
func TestRemoveDuplicateTagsWithBlanks(t *testing.T) {
	in := []string{"#TITLE:A\n", " #MP3:a.mp3\n", "#mp3 :b.mp3\n", "#:x\n", "#:y\n", "#BPM:1\n"}
	want := []string{"#TITLE:A\n", "#mp3 :b.mp3\n", "#:x\n", "#:y\n", "#BPM:1\n"}
	if got := removeDuplicateTags(in); !slices.Equal(got, want) {
		t.Errorf("removeDuplicateTags = %q, want %q", got, want)
	}
}

func TestParseTagLine(t *testing.T) {
	tests := []struct {
		line, key, value string
		ok               bool
	}{
		{"#MP3:a.mp3\n", "MP3", "a.mp3", true},
		{" \t#mp3 : a b.mp3 \n", "mp3", "a b.mp3", true},
		{"#VIDEOGAP:1\n", "VIDEOGAP", "1", true},
		{"#URL:http://x\n", "URL", "http://x", true},
		{"#MP3\n", "", "", false},
		{"# :x\n", "", "", false},
		{": 0 4 0 Hey:\n", "", "", false},
		{"x#MP3:a\n", "", "", false},
	}
	for _, tc := range tests {
		key, value, ok := parseTagLine(tc.line)
		if key != tc.key || value != tc.value || ok != tc.ok {
			t.Errorf("parseTagLine(%q) = %q, %q, %v, want %q, %q, %v", tc.line, key, value, ok, tc.key, tc.value, tc.ok)
		}
	}
	if !hasTag(" #Video :x\n", "#VIDEO") || hasTag("#VIDEOGAP :x\n", "#VIDEO") {
		t.Error("hasTag does not match the whole key ignoring blanks")
	}
}

// Regression: the cover/background notes in missing_files.txt followed the detected images,
// not the tags the txt has after the run.
func TestMissingReportFollowsWrittenImageTags(t *testing.T) {
	tests := []struct {
		name, header, png string // png "" = no valid image
		w, h              int
		wantCover, wantBG bool // whether the report should list the gap
	}{
		{"kept background, no cover", "#BACKGROUND:a cover.png", "a cover.png", 64, 36, true, false},
		{"kept cover, no background", "#COVER:a bg.png", "a bg.png", 10, 10, false, true},
		// The only image stays the (wrong-shaped) cover, so there is no background.
		{"tagged cover of the wrong shape", "#COVER:wide.png", "wide.png", 64, 36, false, true},
		// Tags naming a corrupt image and a non-image are no cover or background.
		{"corrupt cover, non-image background", "#COVER:broken.png\n#BACKGROUND:song.mp3", "", 0, 0, true, true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			cfg := newLibrary(t, false)
			testutil.WriteFiles(t, cfg.SongDir, map[string]string{
				"Song/song.txt":   songTxt("#MP3:song.mp3", tc.header),
				"Song/song.mp3":   "a",
				"Song/broken.png": "not an image",
			})
			if tc.png != "" {
				testutil.WritePNG(t, filepath.Join(cfg.SongDir, "Song", tc.png), tc.w, tc.h)
			}
			run(t, cfg)
			txt := testutil.ReadFile(t, filepath.Join(cfg.SongDir, "Song", "song.txt"))
			report := testutil.ReadFile(t, cfg.ReportPath(missingFilesReport))
			if got := strings.Contains(report, "Missing cover file in "); got != tc.wantCover {
				t.Errorf("Missing cover listed = %v, want %v\ntxt:\n%s\nreport:\n%s", got, tc.wantCover, txt, report)
			}
			if got := strings.Contains(report, "No video or background file in "); got != tc.wantBG {
				t.Errorf("No background listed = %v, want %v\ntxt:\n%s\nreport:\n%s", got, tc.wantBG, txt, report)
			}
		})
	}
}

// A complete song (audio, video, cover) has nothing in missing_files.txt; one without
// images gets both notes, also when its txt cannot be decoded (detection fallback).
func TestMissingReportCompleteAndUndecodable(t *testing.T) {
	cfg := newLibrary(t, false)
	testutil.WriteFiles(t, cfg.SongDir, map[string]string{
		"Song/song.txt": songTxt("#MP3:song.mp3", "#VIDEO:clip.mp4", "#COVER:cover.png"),
		"Song/song.mp3": "a",
		"Song/clip.mp4": "v",
	})
	testutil.WritePNG(t, filepath.Join(cfg.SongDir, "Song", "cover.png"), 10, 10)
	run(t, cfg)
	if got := testutil.ReadFile(t, cfg.ReportPath(missingFilesReport)); got != "" {
		t.Errorf("%s = %q, want empty", missingFilesReport, got)
	}

	cfg = newLibrary(t, false)
	testutil.WriteFiles(t, cfg.SongDir, map[string]string{
		"Bad/song.txt": "#TITLE:\xe4\x81\n#ARTIST:A\n#MP3:song.mp3\n#COVER:c.png\n: 0 1 0 x\nE\n",
		"Bad/song.mp3": "audio",
	})
	testutil.WritePNG(t, filepath.Join(cfg.SongDir, "Bad", "c.png"), 10, 10)
	run(t, cfg)
	bad := filepath.Join(cfg.SongDir, "Bad")
	want := "No video or background file in " + bad + "\n"
	if got := testutil.ReadFile(t, cfg.ReportPath(missingFilesReport)); got != want {
		t.Errorf("%s = %q, want %q", missingFilesReport, got, want)
	}
}

// Regression: with several audio files, a stale #AUDIO next to a valid #MP3 (and the other
// way round) was left alone and reported as ambiguous.
func TestRunUsesValidSiblingAudioTag(t *testing.T) {
	files := map[string]string{"a.mp3": "a", "b.mp3": "a"}
	got, report, _ := fixSong(t, "Song", "song.txt", files, []string{"#VERSION:1.1.0", "#MP3:b.mp3", "#AUDIO:gone.mp3"})
	if !strings.Contains(got, "#MP3:b.mp3\n#AUDIO:b.mp3\n") || strings.Contains(report, "cannot choose #MP3") {
		t.Errorf("stale #AUDIO not fixed:\n%s\nreport:\n%s", got, report)
	}
	got, report, _ = fixSong(t, "Song", "song.txt", files, []string{"#VERSION:1.1.0", "#MP3:gone.mp3", "#AUDIO:B.MP3"})
	if !strings.Contains(got, "#MP3:b.mp3\n#AUDIO:b.mp3\n") || strings.Contains(report, "cannot choose #MP3") {
		t.Errorf("stale #MP3 not fixed:\n%s\nreport:\n%s", got, report)
	}
}

// An unresolved #MP3 stays ambiguous even if a later #AUDIO names an existing file that is
// no candidate (a stem): the flags of both lines accumulate.
func TestRunReportsUnresolvedAudioBeforeResolvedOne(t *testing.T) {
	got, report, _ := fixSong(t, "Song", "song.txt",
		map[string]string{"a.mp3": "a", "b.mp3": "a", "v.mp3": "v"},
		[]string{"#VOCALS:v.mp3", "#MP3:gone.mp3", "#AUDIO:v.mp3"})
	if !strings.Contains(report, "Several audio files, cannot choose #MP3 in ") || !strings.Contains(got, "#MP3:gone.mp3\n") {
		t.Errorf("ambiguity not reported:\n%s\nreport:\n%s", got, report)
	}
}

// Two candidates matching one preference are no choice; the next preference decides, or
// the audio stays ambiguous.
func TestRunChoosesAudioOnlyOnUniqueMatch(t *testing.T) {
	got, report, _ := fixSong(t, "X", "song.txt", map[string]string{"song.mp3": "a", "song.ogg": "a"}, []string{"#MP3:gone.mp3"})
	if !strings.Contains(got, "#MP3:gone.mp3\n") || !strings.Contains(report, "cannot choose #MP3 in ") {
		t.Errorf("same-stem candidates were not ambiguous:\n%s\nreport:\n%s", got, report)
	}
	got, _, _ = fixSong(t, "Song", "song.txt", map[string]string{"song.mp3": "a", "song.ogg": "a", "artist - song.ogg": "a"}, []string{"#MP3:gone.mp3"})
	if !strings.Contains(got, "#MP3:artist - song.ogg\n") {
		t.Errorf("the artist - title preference did not decide:\n%s", got)
	}
}

// Without #ARTIST the "<#ARTIST> - <#TITLE>" preference is skipped (it would be " - Song").
func TestRunSkipsArtistTitlePreferenceWithoutArtist(t *testing.T) {
	cfg := newLibrary(t, false)
	testutil.WriteFiles(t, cfg.SongDir, map[string]string{
		"X/x.txt":       "#TITLE:Song\n" + notesBody,
		"X/a.mp3":       "a",
		"X/ - Song.mp3": "a",
	})
	run(t, cfg)
	got := testutil.ReadFile(t, filepath.Join(cfg.SongDir, "X", "x.txt"))
	if strings.Contains(got, "#MP3:") || !strings.Contains(testutil.ReadFile(t, cfg.ReportPath(missingFilesReport)), "cannot choose #MP3") {
		t.Errorf("audio chosen by an incomplete artist - title:\n%s", got)
	}
}

// With only stems as audio and no video, there is nothing to put into #MP3.
func TestRunStemsOnlyNoVideo(t *testing.T) {
	got, _, ops := fixSong(t, "Song", "song.txt", map[string]string{"v.mp3": "v"}, []string{"#VOCALS:v.mp3"})
	if strings.Contains(got, "#MP3:") || ops.Stats().Failed != 0 {
		t.Errorf("unexpected #MP3 or failure:\n%s", got)
	}
}

// Regression: of several videos the first by name was used for a missing or stale #VIDEO,
// without a report, so "Song (lyrics).mp4" won over "Song.mp4" and cleanup then moved
// Song.mp4 away. The video is chosen like the audio (txt, folder, "Artist - Title" name);
// without a preferred one nothing is inserted (also no #MP3 of a video-only song) and the
// folder is listed in missing_files.txt.
func TestRunChoosesVideoLikeAudio(t *testing.T) {
	videos := map[string]string{"Song (lyrics).mp4": "v", "Song.mp4": "v", "song.mp3": "a"}
	got, report, _ := fixSong(t, "X", "Song.txt", videos, nil)
	if !strings.Contains(got, "#VIDEO:Song.mp4\n") || strings.Contains(report, "#VIDEO") {
		t.Errorf("want #VIDEO:Song.mp4 (named like the txt):\n%s\nreport:\n%s", got, report)
	}
	got, _, _ = fixSong(t, "X", "x.txt", map[string]string{"Artist - Song (lyrics).mp4": "v", "Artist - Song.mp4": "v"},
		[]string{"#VIDEO:gone.mp4"})
	if !strings.Contains(got, "#VIDEO:Artist - Song.mp4\n") {
		t.Errorf("stale #VIDEO not replaced by the artist - title video:\n%s", got)
	}

	got, report, _ = fixSong(t, "X", "x.txt", map[string]string{"b.mp4": "v", "a.mp4": "v"}, nil)
	if strings.Contains(got, "#VIDEO:") || strings.Contains(got, "#MP3:") ||
		!strings.Contains(report, "Several video files, cannot choose #VIDEO in ") || !strings.Contains(report, "a.mp4, b.mp4") {
		t.Errorf("ambiguous videos: want no tag and a report:\n%s\nreport:\n%s", got, report)
	}
	got, report, _ = fixSong(t, "X", "x.txt", map[string]string{"b.mp4": "v", "a.mp4": "v"}, []string{"#VIDEO:gone.mp4"})
	if !strings.Contains(got, "#VIDEO:gone.mp4\n") || !strings.Contains(report, "cannot choose #VIDEO") {
		t.Errorf("ambiguous stale #VIDEO: want it kept and reported:\n%s\nreport:\n%s", got, report)
	}
	// An existing #VIDEO decides the #MP3 of a video-only song, and an #MP3 naming a video
	// decides #VIDEO; neither is reported.
	got, report, _ = fixSong(t, "X", "x.txt", map[string]string{"b.mp4": "v", "a.mp4": "v"}, []string{"#VIDEO:b.mp4"})
	if !strings.Contains(got, "#MP3:b.mp4\n") || strings.Contains(report, "#VIDEO") {
		t.Errorf("#MP3 should follow #VIDEO:\n%s\nreport:\n%s", got, report)
	}
	got, report, _ = fixSong(t, "X", "x.txt", map[string]string{"b.mp4": "v", "a.mp4": "v"}, []string{"#MP3:b.mp4"})
	if !strings.Contains(got, "#VIDEO:b.mp4\n") || strings.Contains(report, "#VIDEO") {
		t.Errorf("#VIDEO should follow #MP3:\n%s\nreport:\n%s", got, report)
	}
	got, report, _ = fixSong(t, "X", "x.txt", map[string]string{"b.mp4": "v", "a.mp4": "v"}, []string{"#AUDIO:b.mp4"})
	if !strings.Contains(got, "#VIDEO:b.mp4\n") || strings.Contains(report, "#VIDEO") {
		t.Errorf("#VIDEO should follow #AUDIO:\n%s\nreport:\n%s", got, report)
	}
}

// A single video is used for #VIDEO and for the #MP3 of a video-only song.
func TestRunChoosesOnlyVideo(t *testing.T) {
	got, _, _ := fixSong(t, "Song", "song.txt", map[string]string{"b.mp4": "v"}, nil)
	if !strings.Contains(got, "#MP3:b.mp4\n#VIDEO:b.mp4\n") {
		t.Errorf("want b.mp4 for #MP3 and #VIDEO:\n%s", got)
	}
}

// Missing file tags are inserted as line 2, in the order MP3, VIDEO, COVER, BACKGROUND.
func TestRunInsertsMissingTagsAsLine2(t *testing.T) {
	cfg := newLibrary(t, false)
	testutil.WriteFiles(t, cfg.SongDir, map[string]string{
		"Artist - Song/song.txt": songTxt("#VIDEOGAP:2"),
		"Artist - Song/song.mp3": "a",
		"Artist - Song/clip.mp4": "v",
	})
	testutil.WritePNG(t, filepath.Join(cfg.SongDir, "Artist - Song", "sq.png"), 10, 10)
	testutil.WritePNG(t, filepath.Join(cfg.SongDir, "Artist - Song", "wide.png"), 64, 36)
	run(t, cfg)
	want := "#TITLE:Song\n#MP3:song.mp3\n#VIDEO:clip.mp4\n#COVER:sq.png\n#BACKGROUND:wide.png\n#ARTIST:Artist\n#VIDEOGAP:2\n" + notesBody
	if got := testutil.ReadFile(t, filepath.Join(cfg.SongDir, "Artist - Song", "song.txt")); got != want {
		t.Errorf("txt = %q, want %q", got, want)
	}
}

// A tagged image that does not exist or cannot be read is replaced by the detected one.
func TestRunReplacesUnreadableTaggedImages(t *testing.T) {
	cfg := newLibrary(t, false)
	testutil.WriteFiles(t, cfg.SongDir, map[string]string{
		"Song/song.txt":   songTxt("#MP3:song.mp3", "#COVER:gone.png", "#BACKGROUND:broken.png"),
		"Song/song.mp3":   "a",
		"Song/broken.png": "not an image",
	})
	testutil.WritePNG(t, filepath.Join(cfg.SongDir, "Song", "sq.png"), 10, 10)
	testutil.WritePNG(t, filepath.Join(cfg.SongDir, "Song", "wide.png"), 64, 36)
	run(t, cfg)
	got := testutil.ReadFile(t, filepath.Join(cfg.SongDir, "Song", "song.txt"))
	if !strings.Contains(got, "#COVER:sq.png\n#BACKGROUND:wide.png\n") {
		t.Errorf("unreadable tagged images not replaced:\n%s", got)
	}
}

func TestDropEncodingTag(t *testing.T) {
	in := []string{"#ENCODING:UTF-8\n", "#encoding:utf8 \n", "#Encoding:cp1252\n", "#ENCODING:auto\n", " #ENCODING :latin1\n", "#ENCODINGX:1\n", "#TITLE:x\n"}
	want := []string{"#ENCODING:UTF-8\n", "#encoding:utf8 \n", "#ENCODINGX:1\n", "#TITLE:x\n"}
	if got := dropEncodingTag(in); !slices.Equal(got, want) {
		t.Errorf("dropEncodingTag = %q, want %q", got, want)
	}
}

func TestResolveFileTag(t *testing.T) {
	dir := t.TempDir()
	testutil.WriteFiles(t, dir, map[string]string{"a.mp3": "a", "\xe4.mp3": "a"})
	choice := fileChoice{name: "a.mp3", candidates: 2}
	tests := []struct {
		value, want             string
		rewrite, wantUnresolved bool
		choice                  fileChoice
	}{
		{"gone.mp3", "a.mp3", true, false, choice},
		{"A.MP3", "a.mp3", true, false, choice},
		{"a.mp3", "a.mp3", false, false, choice},
		// Not on disk, but already the choice: nothing to rewrite.
		{"x.mp3", "x.mp3", false, false, fileChoice{name: "x.mp3", candidates: 1}},
		{"gone.mp3", "gone.mp3", false, true, fileChoice{candidates: 2}},
		// An on-disk name that is not valid UTF-8 is never written into the txt.
		{"\xc4.mp3", "\xc4.mp3", false, false, choice},
	}
	for _, tc := range tests {
		v, rewrite, unresolved := resolveFileTag(dir, tc.value, tc.choice, func(string) bool { return true }, nil)
		if v != tc.want || rewrite != tc.rewrite || unresolved != tc.wantUnresolved {
			t.Errorf("resolveFileTag(%q) = %q, %v, %v, want %q, %v, %v", tc.value, v, rewrite, unresolved, tc.want, tc.rewrite, tc.wantUnresolved)
		}
	}
}

// filled_tags.txt that cannot be read is not overwritten, and that counts as a failure.
func TestWriteFilledReportReadError(t *testing.T) {
	cfg := newLibrary(t, false)
	path := cfg.ReportPath(filledTagsReport)
	if err := os.MkdirAll(filepath.Join(path, "keep"), 0o755); err != nil {
		t.Fatal(err)
	}
	ops := fsops.New(false)
	writeFilledReport(cfg, ops, []string{"/a.txt: #TITLE:A" + filledSuffix})
	if f := ops.Stats().Failed; f != 1 {
		t.Errorf("Failed = %d, want 1", f)
	}
	if !testutil.Exists(filepath.Join(path, "keep")) {
		t.Error("unreadable filled_tags.txt was replaced")
	}
}

// Of two song txts with the same mtime, the first by name is kept.
func TestRunDuplicateSongTxtsSameMtime(t *testing.T) {
	cfg := newLibrary(t, false)
	testutil.WriteFiles(t, cfg.SongDir, map[string]string{
		"Song/b.txt":    songTxt("#MP3:song.mp3"),
		"Song/a.txt":    songTxt("#MP3:song.mp3"),
		"Song/song.mp3": "a",
	})
	ts := time.Now().Add(-time.Hour)
	for _, n := range []string{"a.txt", "b.txt"} {
		if err := os.Chtimes(filepath.Join(cfg.SongDir, "Song", n), ts, ts); err != nil {
			t.Fatal(err)
		}
	}
	run(t, cfg)
	if !testutil.Exists(filepath.Join(cfg.SongDir, "Song", "a.txt")) || !testutil.Exists(filepath.Join(cfg.ExtraFilesDir(), "Song", "b.txt")) {
		t.Error("want a.txt kept and b.txt moved to extra-files/")
	}
}

// A duplicate song txt that cannot be moved stays; the kept txt is still fixed.
func TestRunDuplicateSongTxtMoveFails(t *testing.T) {
	hook := testutil.CaptureLogsAt(t, log.InfoLevel)
	cfg := newLibrary(t, false)
	testutil.WriteFiles(t, cfg.SongDir, map[string]string{
		"Song/old.txt":  songTxt("#MP3:song.mp3"),
		"Song/new.txt":  songTxt("#MP3:gone.mp3"),
		"Song/song.mp3": "a",
	})
	testutil.WriteFiles(t, filepath.Dir(cfg.ExtraFilesDir()), map[string]string{filepath.Base(cfg.ExtraFilesDir()): "a file"})
	past := time.Now().Add(-time.Hour)
	if err := os.Chtimes(filepath.Join(cfg.SongDir, "Song", "old.txt"), past, past); err != nil {
		t.Fatal(err)
	}
	ops := run(t, cfg)
	if f := ops.Stats().Failed; f != 1 {
		t.Errorf("Failed = %d, want 1", f)
	}
	if !testutil.Exists(filepath.Join(cfg.SongDir, "Song", "old.txt")) {
		t.Error("duplicate song txt is gone")
	}
	if got := testutil.ReadFile(t, filepath.Join(cfg.SongDir, "Song", "new.txt")); !strings.Contains(got, "#MP3:song.mp3\n") {
		t.Errorf("kept txt not fixed:\n%s", got)
	}
	if len(messages(hook, "Could not move duplicate song txt")) != 1 {
		t.Error("move failure not logged")
	}
}

// A broken folder that cannot be moved to invalid/ stays where it is, with an error.
func TestRunMoveToInvalidFails(t *testing.T) {
	hook := testutil.CaptureLogsAt(t, log.InfoLevel)
	cfg := newLibrary(t, false)
	testutil.WriteFiles(t, cfg.SongDir, map[string]string{"NoMedia/song.txt": songTxt()})
	testutil.WriteFiles(t, filepath.Dir(cfg.InvalidDir()), map[string]string{filepath.Base(cfg.InvalidDir()): "a file"})
	ops := run(t, cfg)
	if f := ops.Stats().Failed; f != 1 {
		t.Errorf("Failed = %d, want 1", f)
	}
	if !testutil.Exists(filepath.Join(cfg.SongDir, "NoMedia", "song.txt")) {
		t.Error("folder was moved")
	}
	if len(messages(hook, "Could not move "+filepath.Join(cfg.SongDir, "NoMedia")+" to invalid")) != 1 {
		t.Error("move failure not logged")
	}
}

// A report that cannot be written is an error; the others are still written.
func TestRunReportWriteFails(t *testing.T) {
	hook := testutil.CaptureLogsAt(t, log.InfoLevel)
	cfg := newLibrary(t, false)
	testutil.WriteFiles(t, cfg.SongDir, map[string]string{"Song/song.txt": songTxt("#MP3:song.mp3"), "Song/song.mp3": "a"})
	if err := os.MkdirAll(filepath.Join(cfg.ReportPath(missingFilesReport), "x"), 0o755); err != nil {
		t.Fatal(err)
	}
	ops := run(t, cfg)
	if f := ops.Stats().Failed; f != 1 {
		t.Errorf("Failed = %d, want 1", f)
	}
	if got := messages(hook, "Could not write report"); len(got) != 1 || !strings.Contains(got[0], missingFilesReport) {
		t.Errorf("report errors = %q, want one for %s", got, missingFilesReport)
	}
	if len(messages(hook, "Wrote "+cfg.ReportPath(encodingIssuesReport))) != 1 || len(messages(hook, "Wrote "+cfg.ReportPath(missingFilesReport))) != 0 {
		t.Errorf("Wrote messages wrong: %q", messages(hook, "Wrote "))
	}
}

// Without songs left unchanged there is no encoding warning.
func TestRunNoUnchangedWarning(t *testing.T) {
	hook := testutil.CaptureLogsAt(t, log.InfoLevel)
	cfg := newLibrary(t, false)
	testutil.WriteFiles(t, cfg.SongDir, map[string]string{"Song/song.txt": songTxt("#MP3:song.mp3"), "Song/song.mp3": "a"})
	run(t, cfg)
	if got := messages(hook, "encoding issues and were not changed"); len(got) != 0 {
		t.Errorf("unexpected warning %q", got)
	}
}

// A txt that cannot be written is left as it is, with an error, and its fills are not
// reported as written.
func TestRunTxtWriteFails(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root can write everything")
	}
	hook := testutil.CaptureLogsAt(t, log.InfoLevel)
	cfg := newLibrary(t, false)
	raw := "#MP3:song.mp3\n" + notesBody
	testutil.WriteFiles(t, cfg.SongDir, map[string]string{"Artist - Song/song.txt": raw, "Artist - Song/song.mp3": "a"})
	folder := filepath.Join(cfg.SongDir, "Artist - Song")
	if err := os.Chmod(folder, 0o555); err != nil {
		t.Fatal(err)
	}
	defer os.Chmod(folder, 0o755)
	ops := run(t, cfg)
	if f := ops.Stats().Failed; f != 1 {
		t.Errorf("Failed = %d, want 1", f)
	}
	if got := testutil.ReadFile(t, filepath.Join(folder, "song.txt")); got != raw {
		t.Errorf("txt changed: %q", got)
	}
	if got := testutil.ReadFile(t, cfg.ReportPath(filledTagsReport)); got != "" {
		t.Errorf("%s = %q, want empty", filledTagsReport, got)
	}
	if len(messages(hook, "Could not write "+filepath.Join(folder, "song.txt"))) != 1 {
		t.Error("write failure not logged")
	}
}

// The detected encoding is logged at debug level for txts that are not UTF-8, and the
// inserted lines for txts that get some.
func TestRunDebugLogs(t *testing.T) {
	hook := testutil.CaptureLogsAt(t, log.DebugLevel)
	cfg := newLibrary(t, false)
	testutil.WriteFiles(t, cfg.SongDir, map[string]string{
		"Latin/song.txt": "#TITLE:M\xe4dchen\n#ARTIST:A\n#MP3:song.mp3\n" + notesBody,
		"Latin/song.mp3": "a",
		"UTF8/song.txt":  songTxt("#MP3:song.mp3"),
		"UTF8/song.mp3":  "a",
		"Ins/song.txt":   songTxt(),
		"Ins/song.mp3":   "a",
	})
	run(t, cfg)
	if got := messages(hook, "Decoded "); len(got) != 1 || !strings.Contains(got[0], filepath.Join(cfg.SongDir, "Latin", "song.txt")) {
		t.Errorf("Decoded messages = %q, want one for Latin/song.txt", got)
	}
	if got := messages(hook, "Inserting into "); len(got) != 1 || !strings.Contains(got[0], filepath.Join(cfg.SongDir, "Ins")) {
		t.Errorf("Inserting messages = %q, want one for Ins", got)
	}
}

// Regression: headerLen ended the header at any non-"#" line, so a junk line inside the
// header made fixtxt insert a second #MP3 above it while the stale #MP3 below it stayed
// the last one songtxt (and check) read. The header ends where songtxt's body starts.
func TestFixTxtUnknownLineInHeader(t *testing.T) {
	cfg := newLibrary(t, false)
	testutil.WriteFiles(t, cfg.SongDir, map[string]string{
		"Song/song.txt": "#TITLE:Song\n#ARTIST:Artist\nCreated with editor\n#MP3:gone.mp3\n#BPM:300\n: 0 4 0 La\nE\n",
		"Song/a.mp3":    "a",
	})
	run(t, cfg)
	got := testutil.ReadFile(t, filepath.Join(cfg.SongDir, "Song", "song.txt"))
	if mp3 := tagValues(t, got, "MP3"); !slices.Equal(mp3, []string{"a.mp3"}) {
		t.Errorf("#MP3 values = %q, want [a.mp3]:\n%s", mp3, got)
	}
	if bpm := tagValues(t, got, "BPM"); !slices.Equal(bpm, []string{"300"}) {
		t.Errorf("#BPM values = %q, want [300]:\n%s", bpm, got)
	}
	if !strings.Contains(got, "Created with editor\n") {
		t.Errorf("junk line not kept:\n%s", got)
	}
}

// A header-less txt (notes from the first line on) with nothing to insert is returned
// unchanged. Run never passes one (utils.IsSongTxt wants a header tag), but fixTxtLines
// must not index the empty header. The missing #ARTIST/#TITLE are noted.
func TestFixTxtLinesHeaderlessNothingToInsert(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "no obvious name")
	if err := os.Mkdir(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	in := []string{": 0 1 1 a\n", "- 2\n", "E\n"}
	got, notes, filled := fixTxtLines(slices.Clone(in), dir, songFiles{}, DefaultOptions())
	if !slices.Equal(got, in) || filled != nil {
		t.Errorf("fixTxtLines = %q, filled %q, want the lines unchanged", got, filled)
	}
	if len(notes) != 1 || !strings.Contains(notes[0], "#ARTIST and #TITLE") {
		t.Errorf("notes = %q, want one note on the missing #ARTIST and #TITLE", notes)
	}
}
