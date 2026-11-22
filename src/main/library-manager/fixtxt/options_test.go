package fixtxt

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/nkreimeyer/library-manager/config"
	"github.com/nkreimeyer/library-manager/fsops"
	"github.com/nkreimeyer/library-manager/internal/testutil"
)

// runWith runs RunWith on cfg and fails the test on an error.
func runWith(t *testing.T, cfg config.Config, opts Options) *fsops.Ops {
	t.Helper()
	ops := fsops.New(cfg.DryRun)
	if err := RunWith(cfg, ops, opts); err != nil {
		t.Fatalf("RunWith: %v", err)
	}
	return ops
}

// without returns DefaultOptions with the step off turned off.
func without(off func(*Options)) Options {
	opts := DefaultOptions()
	off(&opts)
	return opts
}

func readReport(t *testing.T, cfg config.Config, name string) string {
	t.Helper()
	b, err := os.ReadFile(cfg.ReportPath(name))
	if err != nil && !os.IsNotExist(err) {
		t.Fatal(err)
	}
	return string(b)
}

func TestDefaultOptions(t *testing.T) {
	want := Options{Encoding: true, FileTags: true, FillTags: true, DuplicateTxts: true, MoveBroken: true}
	if got := DefaultOptions(); fmt.Sprint(got) != fmt.Sprint(want) {
		t.Errorf("DefaultOptions() = %+v, want %+v", got, want)
	}
}

func TestRunWithoutEncoding(t *testing.T) {
	hook := testutil.CaptureLogs(t)
	cfg := newLibrary(t, false)
	files := map[string]string{
		// CP1252 with a wrong #MP3: fixing it would convert the txt.
		"Cp1252/song.txt": "#TITLE:M\xe4dchen\n#ARTIST:A\n#MP3:old.mp3\n: 0 1 0 x\nE\n",
		"Cp1252/song.mp3": "audio",
		// CP1252 that needs nothing else: not reported.
		"Clean/song.txt": "#TITLE:M\xe4dchen\n#ARTIST:A\n#MP3:song.mp3\n: 0 1 0 x\nE\n",
		"Clean/song.mp3": "audio",
		// UTF-8 with CRLF and nothing else to fix: line endings are the encoding step.
		"Crlf/song.txt": "#TITLE:T\r\n#ARTIST:A\r\n#MP3:song.mp3\r\n: 0 1 0 x\r\nE\r\n",
		"Crlf/song.mp3": "audio",
		// UTF-8 with a wrong #MP3 and a stale #ENCODING: the tag is fixed, #ENCODING stays.
		"Utf8/song.txt": "#TITLE:T\n#ENCODING:CP1252\n#ARTIST:A\n#MP3:old.mp3\n: 0 1 0 x\nE\n",
		"Utf8/song.mp3": "audio",
		// UTF-16 with a wrong #MP3 and no #ARTIST: neither the tag fix nor the fill is written.
		"Artist - Utf16/song.txt": utf16LE("#TITLE:Utf16\n#MP3:old.mp3\n: 0 1 0 x\nE\n"),
		"Artist - Utf16/song.mp3": "audio",
	}
	testutil.WriteFiles(t, cfg.SongDir, files)
	runWith(t, cfg, without(func(o *Options) { o.Encoding = false }))

	for _, rel := range []string{"Cp1252/song.txt", "Clean/song.txt", "Crlf/song.txt", "Artist - Utf16/song.txt"} {
		if got := testutil.ReadFile(t, filepath.Join(cfg.SongDir, rel)); got != files[rel] {
			t.Errorf("%s = %q, want it unchanged", rel, got)
		}
	}
	want := "#TITLE:T\n#ENCODING:CP1252\n#ARTIST:A\n#MP3:song.mp3\n: 0 1 0 x\nE\n"
	if got := testutil.ReadFile(t, filepath.Join(cfg.SongDir, "Utf8/song.txt")); got != want {
		t.Errorf("Utf8/song.txt = %q, want %q", got, want)
	}
	report := readReport(t, cfg, encodingIssuesReport)
	wantLine := filepath.Join(cfg.SongDir, "Artist - Utf16/song.txt") + ": not UTF-8 (read as UTF-16LE); fixing it needs the UTF-8 conversion\n" +
		filepath.Join(cfg.SongDir, "Cp1252/song.txt") + ": not UTF-8 (read as CP1252); fixing it needs the UTF-8 conversion\n"
	if report != wantLine {
		t.Errorf("encoding_issues.txt = %q, want %q", report, wantLine)
	}
	// It counts as a song left unchanged because of its encoding.
	var warned []string
	for _, e := range hook.AllEntries() {
		if strings.Contains(e.Message, "encoding issues and were not changed") {
			warned = append(warned, e.Message)
		}
	}
	if len(warned) != 1 || !strings.HasPrefix(warned[0], "2 song(s) ") {
		t.Errorf("warning = %q, want one warning counting 2 songs", warned)
	}
	if got := readReport(t, cfg, filledTagsReport); got != "" {
		t.Errorf("filled_tags.txt = %q, want empty (nothing was written)", got)
	}
}

// utf16LE encodes ASCII s as UTF-16LE with a BOM.
func utf16LE(s string) string {
	b := []byte{0xff, 0xfe}
	for i := range len(s) {
		b = append(b, s[i], 0)
	}
	return string(b)
}

func TestRunEncodingOnly(t *testing.T) {
	cfg := newLibrary(t, false)
	folder := "Artist - Song"
	testutil.WriteFiles(t, cfg.SongDir, map[string]string{
		// CP1252, a stale #ENCODING, a repeated tag, a wrong #MP3, no #ARTIST.
		filepath.Join(folder, "song.txt"): "#TITLE:M\xe4dchen\r\n#ENCODING:CP1252\r\n#BPM:200\r\n#MP3:old.mp3\r\n#BPM:300\r\n: 0 1 0 x\r\nE",
		filepath.Join(folder, "song.mp3"): "audio",
	})
	runWith(t, cfg, Options{Encoding: true, DuplicateTxts: true, MoveBroken: true})
	want := "#TITLE:Mädchen\n#BPM:200\n#MP3:old.mp3\n#BPM:300\n: 0 1 0 x\nE\n"
	if got := testutil.ReadFile(t, filepath.Join(cfg.SongDir, folder, "song.txt")); got != want {
		t.Errorf("txt = %q, want %q", got, want)
	}
	if got := readReport(t, cfg, filledTagsReport); got != "" {
		t.Errorf("filled_tags.txt = %q, want empty", got)
	}
}

func TestRunWithoutFileTags(t *testing.T) {
	cfg := newLibrary(t, false)
	testutil.WriteFiles(t, cfg.SongDir, map[string]string{
		// Wrong #MP3, a repeated tag, no #ARTIST, CP1252.
		"Artist - Song/song.txt": "#TITLE:Song\n#EDITION:M\xe4dchen\n#BPM:200\n#MP3:old.mp3\n#BPM:300\n: 0 1 0 x\nE\n",
		"Artist - Song/song.mp3": "audio",
		// Several audio files and no #MP3: no ambiguity note without the file tag step.
		"Two/song.txt":  songTxt(),
		"Two/a.mp3":     "audio",
		"Two/b.mp3":     "audio",
		"Two/cover.jpg": testutil.PNG(10, 10),
	})
	runWith(t, cfg, without(func(o *Options) { o.FileTags = false }))
	want := "#ARTIST:Artist\n#TITLE:Song\n#EDITION:Mädchen\n#BPM:200\n#MP3:old.mp3\n#BPM:300\n: 0 1 0 x\nE\n"
	if got := testutil.ReadFile(t, filepath.Join(cfg.SongDir, "Artist - Song/song.txt")); got != want {
		t.Errorf("txt = %q, want %q", got, want)
	}
	if got := testutil.ReadFile(t, filepath.Join(cfg.SongDir, "Two/song.txt")); got != songTxt() {
		t.Errorf("Two/song.txt = %q, want it unchanged (no #COVER inserted)", got)
	}
	missing := readReport(t, cfg, missingFilesReport)
	if strings.Contains(missing, "Several audio files") {
		t.Errorf("missing_files.txt has an ambiguity note:\n%s", missing)
	}
	// The cover note only reports, so it stays: Two has an image, but no #COVER.
	if !strings.Contains(missing, "Missing cover file in "+filepath.Join(cfg.SongDir, "Two")) {
		t.Errorf("missing_files.txt lacks the cover note:\n%s", missing)
	}
	if got := readReport(t, cfg, filledTagsReport); !strings.Contains(got, "#ARTIST:Artist") {
		t.Errorf("filled_tags.txt = %q, want the filled artist", got)
	}
}

func TestRunWithoutFillTags(t *testing.T) {
	cfg := newLibrary(t, false)
	testutil.WriteFiles(t, cfg.SongDir, map[string]string{
		"Artist - Song/song.txt": "#TITLE:Song\n#MP3:old.mp3\n" + notesBody,
		"Artist - Song/song.mp3": "audio",
		// The audio choice uses the unfilled tags: no "Electric Callboy - Hypa Hypa".
		"Electric Callboy - Hypa Hypa [DUET]/song.txt":                         "#TITLE:Hypa Hypa\n" + notesBody,
		"Electric Callboy - Hypa Hypa [DUET]/Electric Callboy - Hypa Hypa.mp3": "audio",
		"Electric Callboy - Hypa Hypa [DUET]/other.mp3":                        "audio",
	})
	runWith(t, cfg, without(func(o *Options) { o.FillTags = false }))
	want := "#TITLE:Song\n#MP3:song.mp3\n" + notesBody
	if got := testutil.ReadFile(t, filepath.Join(cfg.SongDir, "Artist - Song/song.txt")); got != want {
		t.Errorf("txt = %q, want %q", got, want)
	}
	duet := "#TITLE:Hypa Hypa\n" + notesBody
	if got := testutil.ReadFile(t, filepath.Join(cfg.SongDir, "Electric Callboy - Hypa Hypa [DUET]/song.txt")); got != duet {
		t.Errorf("duet txt = %q, want %q", got, duet)
	}
	if got := readReport(t, cfg, filledTagsReport); got != "" {
		t.Errorf("filled_tags.txt = %q, want empty", got)
	}
	if missing := readReport(t, cfg, missingFilesReport); strings.Contains(missing, "cannot fill") {
		t.Errorf("missing_files.txt has a fill note:\n%s", missing)
	}
}

func TestRunWithoutDuplicateTxts(t *testing.T) {
	cfg := newLibrary(t, false)
	testutil.WriteFiles(t, cfg.SongDir, map[string]string{
		"Song/old.txt":  songTxt("#MP3:old.mp3"),
		"Song/new.txt":  songTxt("#MP3:old.mp3"),
		"Song/song.mp3": "audio",
	})
	song := filepath.Join(cfg.SongDir, "Song")
	past := time.Now().Add(-time.Hour)
	if err := os.Chtimes(filepath.Join(song, "old.txt"), past, past); err != nil {
		t.Fatal(err)
	}
	runWith(t, cfg, without(func(o *Options) { o.DuplicateTxts = false }))
	if got := testutil.ReadFile(t, filepath.Join(song, "old.txt")); got != songTxt("#MP3:old.mp3") {
		t.Errorf("old.txt = %q, want it unchanged in place", got)
	}
	if got := testutil.ReadFile(t, filepath.Join(song, "new.txt")); got != songTxt("#MP3:song.mp3") {
		t.Errorf("new.txt = %q, want the fixed #MP3", got)
	}
	if testutil.Exists(cfg.ExtraFilesDir()) {
		t.Error("extra-files/ was created")
	}
}

func TestRunWithoutMoveBroken(t *testing.T) {
	cfg := newLibrary(t, false)
	testutil.WriteFiles(t, cfg.SongDir, map[string]string{
		"Cat/Good/song.txt":        songTxt("#MP3:old.mp3"),
		"Cat/Good/song.mp3":        "audio",
		"Cat/NoTxt/song.mp3":       "audio",
		"Cat/NoMedia/song.txt":     songTxt("#MP3:song.mp3"),
		"Cat/NoSongTxt/readme.txt": "just text\n",
		"Cat/NoSongTxt/song.mp3":   "audio",
	})
	ops := runWith(t, cfg, without(func(o *Options) { o.MoveBroken = false }))
	for _, rel := range []string{"Cat/NoTxt/song.mp3", "Cat/NoMedia/song.txt", "Cat/NoSongTxt/readme.txt"} {
		if !testutil.Exists(filepath.Join(cfg.SongDir, rel)) {
			t.Errorf("%s was moved", rel)
		}
	}
	if testutil.Exists(cfg.InvalidDir()) {
		t.Error("invalid/ was created")
	}
	if got := testutil.ReadFile(t, filepath.Join(cfg.SongDir, "Cat/Good/song.txt")); got != songTxt("#MP3:song.mp3") {
		t.Errorf("Good/song.txt = %q, want the fixed #MP3", got)
	}
	if s := ops.Stats(); s.Moved != 0 || s.Failed != 0 {
		t.Errorf("Stats() = %+v, want no moves and no failures", s)
	}
}

func TestRunWithCancelledContext(t *testing.T) {
	for _, name := range []string{"songs", "empty library"} {
		t.Run(name, func(t *testing.T) {
			cfg := newLibrary(t, false)
			if name == "songs" {
				testutil.WriteFiles(t, cfg.SongDir, map[string]string{
					"Song/song.txt":  songTxt("#MP3:old.mp3"),
					"Song/song.mp3":  "audio",
					"NoTxt/song.mp3": "audio",
				})
			}
			before := testutil.Snapshot(t, cfg.SongDir)
			ctx, cancel := context.WithCancel(context.Background())
			cancel()
			opts := DefaultOptions()
			opts.Ctx = ctx
			err := RunWith(cfg, fsops.New(false), opts)
			if !errors.Is(err, context.Canceled) {
				t.Fatalf("RunWith = %v, want context.Canceled", err)
			}
			if after := testutil.Snapshot(t, cfg.SongDir); after != before {
				t.Errorf("library changed:\nbefore %s\nafter  %s", before, after)
			}
			if names := testutil.DirNames(t, cfg.OutDir); len(names) != 0 {
				t.Errorf("out dir has %q, want nothing", names)
			}
		})
	}
}

// TestRunStoppedMidRunWritesNoReports: a stop while the folders are processed leaves the
// folders not yet started alone and writes no report. There are more folders than
// workers, so some have not started when the first one is done.
func TestRunStoppedMidRunWritesNoReports(t *testing.T) {
	cfg := newLibrary(t, false)
	files := map[string]string{}
	for i := range 3 * workers {
		files[fmt.Sprintf("Song %03d/song.txt", i)] = songTxt("#MP3:song.mp3")
		files[fmt.Sprintf("Song %03d/song.mp3", i)] = "audio"
	}
	testutil.WriteFiles(t, cfg.SongDir, files)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	opts := DefaultOptions()
	opts.Ctx = ctx
	opts.Progress = func(int, int) { cancel() }
	err := RunWith(cfg, fsops.New(false), opts)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("RunWith = %v, want context.Canceled", err)
	}
	for _, name := range []string{missingFilesReport, encodingIssuesReport, filledTagsReport} {
		if testutil.Exists(cfg.ReportPath(name)) {
			t.Errorf("%s was written", name)
		}
	}
}

func TestRunWithProgress(t *testing.T) {
	cfg := newLibrary(t, false)
	testutil.WriteFiles(t, cfg.SongDir, map[string]string{
		"A/song.txt":     songTxt("#MP3:song.mp3"),
		"A/song.mp3":     "audio",
		"B/song.txt":     songTxt("#MP3:song.mp3"),
		"B/song.mp3":     "audio",
		"NoTxt/song.mp3": "audio",
	})
	var mu sync.Mutex
	var calls [][2]int
	opts := DefaultOptions()
	opts.Progress = func(done, total int) {
		mu.Lock()
		calls = append(calls, [2]int{done, total})
		mu.Unlock()
	}
	runWith(t, cfg, opts)
	want := [][2]int{{1, 3}, {2, 3}, {3, 3}}
	if fmt.Sprint(calls) != fmt.Sprint(want) {
		t.Errorf("Progress calls = %v, want %v", calls, want)
	}
}

// preferTaggedAudio keeps the stems: an #MP3 naming the vocals stem next to an #AUDIO
// naming one of two takes is still of the wrong kind.
func TestPreferTaggedAudioKeepsStems(t *testing.T) {
	dir := t.TempDir()
	testutil.WriteFiles(t, dir, map[string]string{"take1.mp3": "a", "take2.mp3": "b", "vocals.mp3": "v"})
	in := fileChoice{candidates: 2, ambiguous: []string{"take1.mp3", "take2.mp3"}, stems: map[string]bool{"vocals.mp3": true}}
	got := preferTaggedAudio(dir, []string{"#AUDIO:take1.mp3\n", "#MP3:vocals.mp3\n"}, in)
	if got.name != "take1.mp3" || got.isMainAudio("vocals.mp3") || !got.isMainAudio("take2.mp3") {
		t.Errorf("preferTaggedAudio = %+v; want take1.mp3 with vocals.mp3 still a stem", got)
	}
	if v, ok, _ := resolveFileTag(dir, "vocals.mp3", got, got.isMainAudio); !ok || v != "take1.mp3" {
		t.Errorf("resolveFileTag(#MP3:vocals.mp3) = %q, %v; want take1.mp3", v, ok)
	}
}
