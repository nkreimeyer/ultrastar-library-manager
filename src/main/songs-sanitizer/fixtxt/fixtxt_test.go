package fixtxt

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"image"
	"image/png"
	"io/fs"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/nkreimeyer/ultrastar-songs-sanitizer/config"
	"github.com/nkreimeyer/ultrastar-songs-sanitizer/fsops"
	log "github.com/sirupsen/logrus"
	logtest "github.com/sirupsen/logrus/hooks/test"
)

// songTxt is a minimal valid song txt with the given extra header lines.
func songTxt(header ...string) string {
	return "#TITLE:Song\n#ARTIST:Artist\n" + strings.Join(header, "\n") + "\n#BPM:300\n: 0 4 0 La\nE\n"
}

// newLibrary returns a config for a fresh library and a separate output dir.
func newLibrary(t *testing.T, dryRun bool) config.Config {
	t.Helper()
	cfg, err := config.New(t.TempDir(), t.TempDir(), dryRun)
	if err != nil {
		t.Fatal(err)
	}
	return cfg
}

// writeFiles creates the given files (path relative to root -> content).
func writeFiles(t *testing.T, root string, files map[string]string) {
	t.Helper()
	for rel, content := range files {
		p := filepath.Join(root, rel)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
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

func run(t *testing.T, cfg config.Config) *fsops.Ops {
	t.Helper()
	ops := fsops.New(cfg.DryRun)
	if err := Run(cfg, ops); err != nil {
		t.Fatalf("Run: %v", err)
	}
	return ops
}

func exists(path string) bool {
	_, err := os.Lstat(path)
	return err == nil
}

// hashTree returns a fingerprint of every entry below root: names, types and file contents.
func hashTree(t *testing.T, root string) map[string]string {
	t.Helper()
	tree := map[string]string{}
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(root, path)
		if d.IsDir() {
			tree[rel] = "dir"
			return nil
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		sum := sha256.Sum256(data)
		tree[rel] = hex.EncodeToString(sum[:])
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return tree
}

func TestSplitLines(t *testing.T) {
	tests := []struct {
		in   string
		want []string
	}{
		{"", nil},
		{"a\nb\n", []string{"a\n", "b\n"}},
		{"a\nb", []string{"a\n", "b\n"}},
		{"a\n\nb\n", []string{"a\n", "\n", "b\n"}},
	}
	for _, tc := range tests {
		if got := splitLines(tc.in); !slices.Equal(got, tc.want) {
			t.Errorf("splitLines(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

func TestHasTag(t *testing.T) {
	tests := []struct {
		line, tag string
		want      bool
	}{
		{"#VIDEO:a.mp4\n", "#VIDEO", true},
		{"#VIDEOGAP:1.5\n", "#VIDEO", false},
		{"#mp3:a.mp3\n", "#MP3", true},
		{"#MP3\n", "#MP3", false},
		{"#M", "#MP3", false},
	}
	for _, tc := range tests {
		if got := hasTag(tc.line, tc.tag); got != tc.want {
			t.Errorf("hasTag(%q, %q) = %v, want %v", tc.line, tc.tag, got, tc.want)
		}
	}
}

func TestRemoveDuplicateTags(t *testing.T) {
	// The last occurrence wins, as in UltraStar Deluxe: #BPM:300 is the tempo that plays.
	in := []string{"#TITLE:A\n", "#BPM:150\n", "#MP3:a.mp3\n", "#mp3:b.mp3\n", "#VIDEO:v.mp4\n", "#VIDEOGAP:1\n", "#BPM:300\n", ": 0 1 0 x\n"}
	want := []string{"#TITLE:A\n", "#mp3:b.mp3\n", "#VIDEO:v.mp4\n", "#VIDEOGAP:1\n", "#BPM:300\n", ": 0 1 0 x\n"}
	if got := removeDuplicateTags(in); !slices.Equal(got, want) {
		t.Errorf("removeDuplicateTags = %q, want %q", got, want)
	}
}

func TestRunFixesFileTags(t *testing.T) {
	tests := []struct {
		name   string
		header []string
		want   []string // lines that must be present
	}{
		{
			// Regression: audio and video lists used to be swapped.
			name:   "stale tags",
			header: []string{"#MP3:old.mp3", "#VIDEO:old.mp4", "#VIDEOGAP:1.5"},
			want:   []string{"#MP3:song.mp3", "#VIDEO:clip.mp4", "#VIDEOGAP:1.5"},
		},
		{
			name:   "lowercase tags",
			header: []string{"#mp3:old.mp3", "#video:old.mp4"},
			want:   []string{"#MP3:song.mp3", "#VIDEO:clip.mp4"},
		},
		{
			name:   "missing tags are inserted",
			header: []string{"#VIDEOGAP:2"},
			want:   []string{"#MP3:song.mp3", "#VIDEO:clip.mp4", "#VIDEOGAP:2"},
		},
		{
			name:   "valid tags are kept",
			header: []string{"#MP3:song.mp3", "#VIDEO:clip.mp4", "#VIDEOGAP:0"},
			want:   []string{"#MP3:song.mp3", "#VIDEO:clip.mp4", "#VIDEOGAP:0"},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			cfg := newLibrary(t, false)
			writeFiles(t, cfg.SongDir, map[string]string{
				"Artist - Song/song.txt": songTxt(tc.header...),
				"Artist - Song/song.mp3": "audio",
				"Artist - Song/clip.mp4": "video",
			})
			run(t, cfg)

			got := readFile(t, filepath.Join(cfg.SongDir, "Artist - Song/song.txt"))
			lines := strings.Split(got, "\n")
			for _, w := range tc.want {
				if !slices.Contains(lines, w) {
					t.Errorf("txt is missing line %q:\n%s", w, got)
				}
			}
			for _, tag := range []string{"#MP3", "#VIDEO"} {
				n := 0
				for _, l := range lines {
					if hasTag(l, tag) {
						n++
					}
				}
				if n != 1 {
					t.Errorf("txt has %d %s tags, want 1:\n%s", n, tag, got)
				}
			}
		})
	}
}

func TestRunDuplicateSongTxts(t *testing.T) {
	cfg := newLibrary(t, false)
	writeFiles(t, cfg.SongDir, map[string]string{
		"Cat/Song/old.txt":     songTxt("#MP3:song.mp3"),
		"Cat/Song/new.txt":     songTxt("#MP3:song.mp3"),
		"Cat/Song/License.txt": "Licensed under CC-BY\n",
		"Cat/Song/song.mp3":    "audio",
	})
	song := filepath.Join(cfg.SongDir, "Cat/Song")
	past := time.Now().Add(-time.Hour)
	if err := os.Chtimes(filepath.Join(song, "old.txt"), past, past); err != nil {
		t.Fatal(err)
	}
	run(t, cfg)

	if !exists(filepath.Join(song, "new.txt")) {
		t.Error("newest song txt was removed")
	}
	if exists(filepath.Join(song, "old.txt")) {
		t.Error("older song txt is still in the song folder")
	}
	if !exists(filepath.Join(cfg.ExtraFilesDir(), "Cat/Song/old.txt")) {
		t.Error("older song txt was not moved to extra-files/Cat/Song/")
	}
	if got := readFile(t, filepath.Join(song, "License.txt")); got != "Licensed under CC-BY\n" {
		t.Errorf("License.txt was modified: %q", got)
	}
}

func TestRunMovesFolderWithoutSongTxtToInvalid(t *testing.T) {
	cfg := newLibrary(t, false)
	writeFiles(t, cfg.SongDir, map[string]string{
		"Song/License.txt": "some license\r\n",
		"Song/readme.txt":  "M\xe4dchen\n",
		"Song/song.mp3":    "audio",
	})
	before := hashTree(t, filepath.Join(cfg.SongDir, "Song"))
	run(t, cfg)
	if _, err := os.Stat(filepath.Join(cfg.SongDir, "Song")); !os.IsNotExist(err) {
		t.Errorf("folder without song txt must be moved away (err=%v)", err)
	}
	// The non-song txts are moved unchanged.
	if after := hashTree(t, filepath.Join(cfg.InvalidDir(), "Song")); !maps.Equal(before, after) {
		t.Errorf("content changed:\nbefore %v\nafter  %v", before, after)
	}
}

func TestRunReportsEncodingIssues(t *testing.T) {
	cfg := newLibrary(t, false)
	writeFiles(t, cfg.SongDir, map[string]string{
		"Bad/song.txt":     "#TITLE:\xe4\x81\n#ARTIST:A\n#MP3:song.mp3\n: 0 1 0 x\nE\n",
		"Bad/song.mp3":     "audio",
		"Damaged/song.txt": "#TITLE:M\ufffddchen\n#ARTIST:A\n#MP3:song.mp3\n: 0 1 0 x\nE\n",
		"Damaged/song.mp3": "audio",
		"Good/song.txt":    "#TITLE:T\n#ARTIST:A\n#MP3:song.mp3\n: 0 1 0 x\nE\n",
		"Good/song.mp3":    "audio",
	})
	run(t, cfg)
	report, err := os.ReadFile(cfg.ReportPath(encodingIssuesReport))
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimSpace(string(report)), "\n")
	if len(lines) != 2 || !strings.Contains(lines[0], filepath.Join("Bad", "song.txt")) || !strings.Contains(lines[1], filepath.Join("Damaged", "song.txt")) {
		t.Errorf("unexpected report:\n%s", report)
	}
}

func TestRunConvertsEncoding(t *testing.T) {
	tests := []struct {
		name string
		raw  string
		want string
	}{
		{"latin1", "#TITLE:M\xe4dchen\n#ARTIST:A\n#MP3:song.mp3\n: 0 1 0 x\nE\n", "#TITLE:Mädchen\n#ARTIST:A\n#MP3:song.mp3\n: 0 1 0 x\nE\n"},
		{"cp1252", "#TITLE:\x84Hi\x93\n#ARTIST:A\n#MP3:song.mp3\n: 0 1 0 x\nE\n", "#TITLE:„Hi“\n#ARTIST:A\n#MP3:song.mp3\n: 0 1 0 x\nE\n"},
		// Not decodable unambiguously: left byte-for-byte untouched (and reported).
		{"undefined byte", "#TITLE:\xe4\x81\n#ARTIST:A\n#MP3:old.mp3\n: 0 1 0 x\nE\n", "#TITLE:\xe4\x81\n#ARTIST:A\n#MP3:old.mp3\n: 0 1 0 x\nE\n"},
		{"mixed encodings", "#TITLE:M\xc3\xa4dchen \xe4\n#ARTIST:A\n#MP3:old.mp3\n: 0 1 0 x\nE\n", "#TITLE:M\xc3\xa4dchen \xe4\n#ARTIST:A\n#MP3:old.mp3\n: 0 1 0 x\nE\n"},
		{"stale encoding tag dropped", "#TITLE:M\xe4dchen\n#ENCODING:CP1252\n#ARTIST:A\n#MP3:song.mp3\n: 0 1 0 x\nE\n", "#TITLE:Mädchen\n#ARTIST:A\n#MP3:song.mp3\n: 0 1 0 x\nE\n"},
		{"crlf", "#TITLE:T\r\n#ARTIST:A\r\n#MP3:song.mp3\r\n: 0 1 0 x\r\nE\r\n", "#TITLE:T\n#ARTIST:A\n#MP3:song.mp3\n: 0 1 0 x\nE\n"},
		{"cr only", "#TITLE:T\r#ARTIST:A\r#MP3:old.mp3\r: 0 1 0 x\rE", "#TITLE:T\n#ARTIST:A\n#MP3:song.mp3\n: 0 1 0 x\nE\n"},
		{"bom", "\xef\xbb\xbf#MP3:old.mp3\n#TITLE:T\n#ARTIST:A\n: 0 1 0 x\nE\n", "#MP3:song.mp3\n#TITLE:T\n#ARTIST:A\n: 0 1 0 x\nE\n"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			cfg := newLibrary(t, false)
			writeFiles(t, cfg.SongDir, map[string]string{
				"Song/song.txt": tc.raw,
				"Song/song.mp3": "audio",
			})
			run(t, cfg)
			if got := readFile(t, filepath.Join(cfg.SongDir, "Song/song.txt")); got != tc.want {
				t.Errorf("txt = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestRunDoesNotRewriteUnchangedFile(t *testing.T) {
	cfg := newLibrary(t, false)
	writeFiles(t, cfg.SongDir, map[string]string{
		"Song/song.txt": songTxt("#MP3:song.mp3", "#VIDEO:clip.mp4", "#VIDEOGAP:0"),
		"Song/song.mp3": "audio",
		"Song/clip.mp4": "video",
	})
	txt := filepath.Join(cfg.SongDir, "Song/song.txt")
	past := time.Date(2020, 1, 1, 0, 0, 0, 0, time.UTC)
	if err := os.Chtimes(txt, past, past); err != nil {
		t.Fatal(err)
	}
	ops := run(t, cfg)
	st, err := os.Stat(txt)
	if err != nil {
		t.Fatal(err)
	}
	if !st.ModTime().Equal(past) {
		t.Errorf("unchanged txt was rewritten (mtime %v)", st.ModTime())
	}
	if w := ops.Stats().Written; w != 0 {
		t.Errorf("Stats().Written = %d, want 0", w)
	}
}

func TestRunMovesBrokenFoldersToInvalid(t *testing.T) {
	cfg := newLibrary(t, false)
	writeFiles(t, cfg.SongDir, map[string]string{
		"Cat/Good/song.txt":    songTxt("#MP3:song.mp3"),
		"Cat/Good/song.mp3":    "audio",
		"Cat/NoTxt/song.mp3":   "audio",
		"Cat/NoMedia/song.txt": songTxt("#MP3:song.mp3"),
	})
	ops := run(t, cfg)

	for _, rel := range []string{"Cat/NoTxt", "Cat/NoMedia"} {
		if exists(filepath.Join(cfg.SongDir, rel)) {
			t.Errorf("%s is still in the library", rel)
		}
		if !exists(filepath.Join(cfg.InvalidDir(), rel)) {
			t.Errorf("%s was not moved to invalid/%s", rel, rel)
		}
	}
	if !exists(filepath.Join(cfg.InvalidDir(), "Cat/NoTxt/song.mp3")) {
		t.Error("contents of the moved folder are missing")
	}
	if !exists(filepath.Join(cfg.SongDir, "Cat/Good/song.txt")) {
		t.Error("valid song was moved")
	}
	if f := ops.Stats().Failed; f != 0 {
		t.Errorf("Stats().Failed = %d, want 0", f)
	}
}

func TestRunWritesSortedMissingFilesReport(t *testing.T) {
	cfg := newLibrary(t, false)
	writeFiles(t, cfg.SongDir, map[string]string{
		"B/song.txt": songTxt("#MP3:song.mp3"),
		"B/song.mp3": "audio",
		"A/song.txt": songTxt("#MP3:song.mp3"),
		"A/song.mp3": "audio",
	})
	run(t, cfg)

	report := readFile(t, cfg.ReportPath(missingFilesReport))
	lines := strings.Split(strings.TrimSuffix(report, "\n"), "\n")
	if len(lines) != 4 {
		t.Fatalf("report has %d lines, want 4:\n%s", len(lines), report)
	}
	if !slices.IsSorted(lines) {
		t.Errorf("report is not sorted:\n%s", report)
	}
	if !strings.Contains(report, "Missing cover file in "+filepath.Join(cfg.SongDir, "A")) {
		t.Errorf("report does not mention song A:\n%s", report)
	}
}

func TestRunDryRunDoesNotModifyLibrary(t *testing.T) {
	cfg := newLibrary(t, true)
	writeFiles(t, cfg.SongDir, map[string]string{
		"Cat/Latin1/song.txt":  "#TITLE:M\xe4dchen\r\n#ARTIST:A\r\n#MP3:old.mp3\r\n: 0 1 0 x\r\nE\r\n",
		"Cat/Latin1/song.mp3":  "audio",
		"Cat/Dup/old.txt":      songTxt("#MP3:song.mp3"),
		"Cat/Dup/new.txt":      songTxt("#MP3:song.mp3"),
		"Cat/Dup/song.mp3":     "audio",
		"Cat/NoTxt/song.mp3":   "audio",
		"Cat/NoMedia/song.txt": songTxt("#MP3:song.mp3"),
	})
	past := time.Now().Add(-time.Hour)
	if err := os.Chtimes(filepath.Join(cfg.SongDir, "Cat/Dup/old.txt"), past, past); err != nil {
		t.Fatal(err)
	}
	before := hashTree(t, cfg.SongDir)
	ops := run(t, cfg)

	if after := hashTree(t, cfg.SongDir); !maps.Equal(before, after) {
		t.Errorf("dry run changed the library:\nbefore %v\nafter  %v", before, after)
	}
	st := ops.Stats()
	if st.Written == 0 || st.Moved == 0 {
		t.Errorf("dry run planned no changes: %+v", st)
	}
	if exists(cfg.InvalidDir()) || exists(cfg.ExtraFilesDir()) {
		t.Error("dry run created invalid/ or extra-files/")
	}
	if !exists(cfg.ReportPath(missingFilesReport)) {
		t.Error("dry run did not write the report to the output dir")
	}
}

func TestRunResolvesFileTags(t *testing.T) {
	nfd := "Ma\u0308dchen.mp3" // NFD on disk (macOS style)
	tests := []struct {
		name   string
		files  map[string]string // besides song.txt
		header []string
		want   []string // expected file tag lines
	}{
		{
			name:   "tags swapped by the old bug are repaired",
			files:  map[string]string{"song.mp3": "a", "video.mp4": "v"},
			header: []string{"#MP3:video.mp4", "#VIDEO:song.mp3"},
			want:   []string{"#MP3:song.mp3", "#VIDEO:video.mp4"},
		},
		{
			name:   "mp3 tag may point to the video if there is no audio file",
			files:  map[string]string{"video.mp4": "v"},
			header: []string{"#MP3:video.mp4", "#VIDEO:video.mp4"},
			want:   []string{"#MP3:video.mp4", "#VIDEO:video.mp4"},
		},
		{
			name:   "spelling is corrected to the on-disk name",
			files:  map[string]string{nfd: "a", "Clip.MP4": "v"},
			header: []string{"#MP3:M\u00e4dchen.mp3", "#VIDEO:clip.mp4"},
			want:   []string{"#MP3:" + nfd, "#VIDEO:Clip.MP4"},
		},
		{
			name:   "audio tag of format 1.1 is used instead of inserting #MP3",
			files:  map[string]string{"song.ogg": "a"},
			header: []string{"#AUDIO:missing.ogg"},
			want:   []string{"#AUDIO:song.ogg"},
		},
		{
			name:   "hidden leftovers are not used as media",
			files:  map[string]string{".song.sanitizer-1.mp3": "tmp", "._song.mp3": "x", "song.mp3": "a"},
			header: []string{"#MP3:gone.mp3"},
			want:   []string{"#MP3:song.mp3"},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			cfg := newLibrary(t, false)
			files := map[string]string{"Song/song.txt": songTxt(tc.header...)}
			for name, content := range tc.files {
				files["Song/"+name] = content
			}
			writeFiles(t, cfg.SongDir, files)
			if f := run(t, cfg).Stats().Failed; f != 0 {
				t.Errorf("%d failed operations", f)
			}
			got := readFile(t, filepath.Join(cfg.SongDir, "Song", "song.txt"))
			for _, line := range tc.want {
				if !strings.Contains(got, line+"\n") {
					t.Errorf("missing %q in\n%s", line, got)
				}
			}
			if strings.Count(got, "#MP3:")+strings.Count(got, "#AUDIO:") != 1 {
				t.Errorf("want exactly one audio tag:\n%s", got)
			}
		})
	}
}

// A Latin-1 media file name is referenced byte-for-byte by a CP1252 txt. Converting the txt
// to UTF-8 would break the reference, so the whole folder must be left alone.
func TestRunSkipsFolderWithNonUTF8MediaNames(t *testing.T) {
	cfg := newLibrary(t, false)
	writeFiles(t, cfg.SongDir, map[string]string{
		// CP1252 txt referencing the Latin-1 name, plus an older duplicate song txt.
		"Latin1/song.txt":       "#TITLE:M\xe4dchen\r\n#ARTIST:A\r\n#MP3:M\xe4dchen.mp3\r\n: 0 1 0 x\r\nE\r\n",
		"Latin1/old.txt":        songTxt("#MP3:gone.mp3"),
		"Latin1/M\xe4dchen.mp3": "a",
		"Latin1/video.mp4":      "v",
		// Only the cover has a Latin-1 name; the txt would be rewritten otherwise.
		"Image/song.txt":       songTxt("#MP3:gone.mp3"),
		"Image/song.mp3":       "a",
		"Image/Cover \xe4.jpg": "not an image",
		// No song txt: must not be moved to invalid/ either.
		"NoSongTxt/License.txt":   "license\n",
		"NoSongTxt/Lied \xfc.mp3": "a",
	})
	past := time.Now().Add(-time.Hour)
	if err := os.Chtimes(filepath.Join(cfg.SongDir, "Latin1/old.txt"), past, past); err != nil {
		t.Fatal(err)
	}
	before := hashTree(t, cfg.SongDir)
	ops := run(t, cfg)

	if after := hashTree(t, cfg.SongDir); !maps.Equal(before, after) {
		t.Errorf("folders with non-UTF-8 media names were changed:\nbefore %v\nafter  %v", before, after)
	}
	if st := ops.Stats(); st.Written != 0 || st.Moved != 0 {
		t.Errorf("Stats() = %+v, want nothing written or moved", st)
	}
	report := readFile(t, cfg.ReportPath(encodingIssuesReport))
	for _, name := range []string{"Latin1/M\xe4dchen.mp3", "Image/Cover \xe4.jpg", "NoSongTxt/Lied \xfc.mp3"} {
		if !strings.Contains(report, filepath.Join(cfg.SongDir, name)+": file name is not valid UTF-8, rename it") {
			t.Errorf("%q not reported:\n%s", name, report)
		}
	}
	if strings.Contains(report, "video.mp4") {
		t.Errorf("valid file name reported:\n%s", report)
	}
}

// The warning counts songs left unchanged: one per skipped folder (however many bad names
// it has) and one per undecodable txt, but not damaged txts, which are still fixed.
func TestRunCountsUnchangedSongs(t *testing.T) {
	hook := logtest.NewGlobal()
	defer log.StandardLogger().ReplaceHooks(make(log.LevelHooks))
	cfg := newLibrary(t, false)
	writeFiles(t, cfg.SongDir, map[string]string{
		"Names/song.txt":   songTxt("#MP3:a.mp3"),
		"Names/\xe41.mp3":  "a",
		"Names/\xe42.mp4":  "v",
		"Bad/song.txt":     "#TITLE:\xe4\x81\n#ARTIST:A\n#MP3:song.mp3\n: 0 1 0 x\nE\n",
		"Bad/song.mp3":     "audio",
		"Damaged/song.txt": "#TITLE:M\ufffddchen\n#ARTIST:A\n#MP3:song.mp3\n: 0 1 0 x\nE\n",
		"Damaged/song.mp3": "audio",
	})
	run(t, cfg)
	var got []string
	for _, e := range hook.AllEntries() {
		if strings.Contains(e.Message, "encoding issues and were not changed") {
			got = append(got, e.Message)
		}
	}
	if len(got) != 1 || !strings.HasPrefix(got[0], "2 song(s) ") {
		t.Errorf("warning = %q, want one warning counting 2 songs", got)
	}
}

func TestRunDoesNotRewriteTxtOfFolderMovedToInvalid(t *testing.T) {
	cfg := newLibrary(t, false)
	original := "#TITLE:M\xe4dchen\r\n#ARTIST:A\r\n: 0 1 0 x\r\nE\r\n"
	writeFiles(t, cfg.SongDir, map[string]string{"NoMedia/song.txt": original})
	run(t, cfg)
	if got := readFile(t, filepath.Join(cfg.InvalidDir(), "NoMedia", "song.txt")); got != original {
		t.Errorf("quarantined txt must be the original, got %q", got)
	}
	if report := readFile(t, cfg.ReportPath(missingFilesReport)); report != "" {
		t.Errorf("moved songs must not be listed as missing files: %q", report)
	}
}

// A broken folder is moved to invalid/ as a whole, before duplicate song txts would be
// split off into extra-files/.
func TestRunMovesBrokenFolderWithDuplicateTxtsAsWhole(t *testing.T) {
	cfg := newLibrary(t, false)
	writeFiles(t, cfg.SongDir, map[string]string{
		"NoMedia/old.txt":   songTxt("#MP3:song.mp3"),
		"NoMedia/new.txt":   songTxt("#MP3:song.mp3"),
		"NoMedia/cover.jpg": "img",
	})
	past := time.Now().Add(-time.Hour)
	if err := os.Chtimes(filepath.Join(cfg.SongDir, "NoMedia/old.txt"), past, past); err != nil {
		t.Fatal(err)
	}
	before := hashTree(t, filepath.Join(cfg.SongDir, "NoMedia"))
	run(t, cfg)
	if exists(filepath.Join(cfg.SongDir, "NoMedia")) {
		t.Error("broken folder is still in the library")
	}
	if after := hashTree(t, filepath.Join(cfg.InvalidDir(), "NoMedia")); !maps.Equal(before, after) {
		t.Errorf("folder was not moved as a whole:\nbefore %v\nafter  %v", before, after)
	}
	if exists(cfg.ExtraFilesDir()) {
		t.Error("a song txt of the broken folder was split off into extra-files/")
	}
}

// fixSong runs fixtxt on a library with one song folder and returns the fixed txt, the
// missing_files report and the ops.
func fixSong(t *testing.T, folder, txtName string, files map[string]string, header []string) (string, string, *fsops.Ops) {
	t.Helper()
	cfg := newLibrary(t, false)
	all := map[string]string{filepath.Join(folder, txtName): songTxt(header...)}
	for name, content := range files {
		all[filepath.Join(folder, name)] = content
	}
	writeFiles(t, cfg.SongDir, all)
	ops := run(t, cfg)
	return readFile(t, filepath.Join(cfg.SongDir, folder, txtName)), readFile(t, cfg.ReportPath(missingFilesReport)), ops
}

func TestRunChoosesAudioFile(t *testing.T) {
	nfd := "Ma\u0308dchen.mp3"
	stems := map[string]string{"a-vocals.mp3": "v", "b-inst.mp3": "i", "c-full.mp3": "a"}
	tests := []struct {
		name          string
		folder, txt   string
		files         map[string]string
		header        []string
		want          string // expected audio tag line, "" for none
		wantAmbiguous bool
	}{
		{
			name:   "video only: missing #MP3 is set to the video",
			files:  map[string]string{"clip.mp4": "v"},
			header: []string{"#VIDEO:clip.mp4"},
			want:   "#MP3:clip.mp4",
		},
		{
			name:   "video only: #MP3 to a missing file is set to the video",
			files:  map[string]string{"clip.mp4": "v"},
			header: []string{"#MP3:gone.mp3"},
			want:   "#MP3:clip.mp4",
		},
		{
			name:   "video only: #AUDIO to a missing file is set to the video",
			files:  map[string]string{"clip.mp4": "v"},
			header: []string{"#AUDIO:gone.ogg"},
			want:   "#AUDIO:clip.mp4",
		},
		{
			name:   "vocals and instrumental are no candidates",
			files:  stems,
			header: []string{"#VOCALS:a-vocals.mp3", "#INSTRUMENTAL:B-Inst.MP3", "#MP3:gone.mp3"},
			want:   "#MP3:c-full.mp3",
		},
		{
			name:   "vocals and instrumental are no candidates for an inserted #MP3",
			files:  stems,
			header: []string{"#VOCALS:a-vocals.mp3", "#INSTRUMENTAL:b-inst.mp3"},
			want:   "#MP3:c-full.mp3",
		},
		{
			name:   "named like the song txt",
			folder: "X", txt: "Song.txt",
			files:  map[string]string{"other.mp3": "a", "song.ogg": "a"},
			header: []string{"#MP3:gone.mp3"},
			want:   "#MP3:song.ogg",
		},
		{
			name:   "named like the song txt, NFC-insensitive",
			folder: "X", txt: "M\u00e4dchen.txt",
			files:  map[string]string{"a.mp3": "a", nfd: "a"},
			header: []string{},
			want:   "#MP3:" + nfd,
		},
		{
			name:   "named like the folder",
			folder: "My Folder", txt: "x.txt",
			files:  map[string]string{"a.mp3": "a", "MY FOLDER.mp3": "a"},
			header: []string{"#MP3:gone.mp3"},
			want:   "#MP3:MY FOLDER.mp3",
		},
		{
			name:   "named like artist - title",
			folder: "X", txt: "x.txt",
			files:  map[string]string{"a.mp3": "a", "artist - song.mp3": "a"},
			header: []string{},
			want:   "#MP3:artist - song.mp3",
		},
		{
			name:          "ambiguous: wrong tag is left alone",
			files:         map[string]string{"a.mp3": "a", "b.mp3": "a"},
			header:        []string{"#MP3:gone.mp3"},
			want:          "#MP3:gone.mp3",
			wantAmbiguous: true,
		},
		{
			name:          "ambiguous: tag pointing to the video is left alone",
			files:         map[string]string{"a.mp3": "a", "b.mp3": "a", "clip.mp4": "v"},
			header:        []string{"#MP3:clip.mp4"},
			want:          "#MP3:clip.mp4",
			wantAmbiguous: true,
		},
		{
			name:          "ambiguous: no #MP3 is inserted",
			files:         map[string]string{"a.mp3": "a", "b.mp3": "a"},
			header:        []string{},
			want:          "",
			wantAmbiguous: true,
		},
		{
			name:   "Matroska audio is audio",
			files:  map[string]string{"song.mka": "a"},
			header: []string{"#MP3:gone.mp3"},
			want:   "#MP3:song.mka",
		},
		{
			name:   "several audio files but a valid tag",
			files:  map[string]string{"a.mp3": "a", "b.mp3": "a"},
			header: []string{"#MP3:b.mp3"},
			want:   "#MP3:b.mp3",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			folder, txt := tc.folder, tc.txt
			if folder == "" {
				folder, txt = "Song", "song.txt"
			}
			got, report, ops := fixSong(t, folder, txt, tc.files, tc.header)
			if f := ops.Stats().Failed; f != 0 {
				t.Errorf("%d failed operations", f)
			}
			n := strings.Count(got, "#MP3:") + strings.Count(got, "#AUDIO:")
			switch {
			case tc.want == "" && n != 0:
				t.Errorf("want no audio tag:\n%s", got)
			case tc.want != "" && (n != 1 || !strings.Contains(got, tc.want+"\n")):
				t.Errorf("want exactly one audio tag %q:\n%s", tc.want, got)
			}
			note := "Several audio files, cannot choose #MP3 in "
			if gotNote := strings.Contains(report, note); gotNote != tc.wantAmbiguous {
				t.Errorf("ambiguity reported = %v, want %v:\n%s", gotNote, tc.wantAmbiguous, report)
			}
			if tc.wantAmbiguous && !strings.Contains(report, "Song: a.mp3, b.mp3\n") {
				t.Errorf("report does not name the folder and candidates:\n%s", report)
			}
		})
	}
}

// An unreadable txt might be the song txt: the folder is counted as failed and left alone,
// never split up or moved to invalid/. Discovery sees the readable a.txt first, so it is
// fixtxt that runs into b.txt.
func TestRunSkipsFolderWithUnreadableTxt(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root can read everything")
	}
	for _, media := range []bool{true, false} {
		t.Run(fmt.Sprintf("media=%v", media), func(t *testing.T) {
			cfg := newLibrary(t, false)
			files := map[string]string{
				"Song/a.txt": "#TITLE:M\xe4dchen\r\n#ARTIST:A\r\n#MP3:gone.mp3\r\n: 0 1 0 x\r\nE\r\n",
				"Song/b.txt": songTxt("#MP3:song.mp3"),
			}
			if media {
				files["Song/song.mp3"] = "a"
			}
			writeFiles(t, cfg.SongDir, files)
			unreadable := filepath.Join(cfg.SongDir, "Song/b.txt")
			if err := os.Chmod(unreadable, 0); err != nil {
				t.Fatal(err)
			}
			defer os.Chmod(unreadable, 0o644)
			before := hashTreeExcept(t, cfg.SongDir, unreadable)
			ops := run(t, cfg)

			if after := hashTreeExcept(t, cfg.SongDir, unreadable); !maps.Equal(before, after) {
				t.Errorf("folder was changed:\nbefore %v\nafter  %v", before, after)
			}
			if !exists(unreadable) {
				t.Error("unreadable txt was moved")
			}
			if st := ops.Stats(); st.Failed != 1 || st.Moved != 0 || st.Written != 0 {
				t.Errorf("Stats() = %+v, want 1 failure and nothing moved or written", st)
			}
		})
	}
}

// hashTreeExcept is hashTree for a tree containing the unreadable file skip, which is only
// recorded by name.
func hashTreeExcept(t *testing.T, root, skip string) map[string]string {
	t.Helper()
	tree := map[string]string{}
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(root, path)
		switch {
		case d.IsDir():
			tree[rel] = "dir"
		case path == skip:
			tree[rel] = "unreadable"
		default:
			sum := sha256.Sum256([]byte(readFile(t, path)))
			tree[rel] = hex.EncodeToString(sum[:])
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return tree
}

func TestRunCorrectsStemSpelling(t *testing.T) {
	cfg := newLibrary(t, false)
	writeFiles(t, cfg.SongDir, map[string]string{
		"Song/song.txt":         songTxt("#MP3:song.mp3", "#VOCALS:VOX.mp3", "#INSTRUMENTAL:missing.mp3"),
		"Song/song.mp3":         "a",
		"Song/vox.mp3":          "v",
		"Song/Instrumental.MP3": "i",
	})
	run(t, cfg)
	got := readFile(t, filepath.Join(cfg.SongDir, "Song", "song.txt"))
	if !strings.Contains(got, "#VOCALS:vox.mp3\n") {
		t.Errorf("#VOCALS not corrected to the on-disk spelling:\n%s", got)
	}
	// A stem tag pointing to a missing file is never replaced by a guess.
	if !strings.Contains(got, "#INSTRUMENTAL:missing.mp3\n") {
		t.Errorf("#INSTRUMENTAL must be left alone:\n%s", got)
	}
}

// fixtxt inserts the cover and background chosen by name hint (over shape) and by
// near-square shape, including WebP and BMP images.
func TestRunChoosesCoverAndBackground(t *testing.T) {
	// Lossless WebP (VP8L) 640x360 and a BITMAPINFOHEADER BMP of 500x-498 (top-down).
	webp := "RIFF\x15\x00\x00\x00WEBPVP8L\x05\x00\x00\x00\x2f\x7f\xc2\x59\x00"
	bmp := "BM\x00\x00\x00\x00\x00\x00\x00\x00\x36\x00\x00\x00\x28\x00\x00\x00\xf4\x01\x00\x00\x0e\xfe\xff\xff\x01\x00\x18\x00"
	tests := []struct {
		name      string
		header    []string // tags besides #MP3
		files     map[string]string
		pngs      map[string][2]int
		cover, bg string // "" = no such tag
	}{
		{
			name:  "hints beat shape",
			pngs:  map[string][2]int{"Song [CO].png": {64, 36}, "a.png": {10, 10}, "b.png": {64, 36}, "Song [BG].png": {10, 10}},
			cover: "Song [CO].png", bg: "Song [BG].png",
		},
		{
			name:  "near square cover, webp background",
			files: map[string]string{"bg.webp": webp},
			pngs:  map[string][2]int{"a.png": {600, 598}},
			cover: "a.png", bg: "bg.webp",
		},
		{
			name:  "bmp cover",
			files: map[string]string{"front.bmp": bmp},
			pngs:  map[string][2]int{"wide.png": {64, 36}},
			cover: "front.bmp", bg: "wide.png",
		},
		{
			// Tagged images are checked with the same near-square rule: the 600x598 tagged
			// background is replaced, too, not made the cover as well.
			name:   "near square tagged background and wide tagged cover are swapped",
			header: []string{"#COVER:wide.png", "#BACKGROUND:pic.png"},
			pngs:   map[string][2]int{"wide.png": {64, 36}, "pic.png": {600, 598}},
			cover:  "pic.png", bg: "wide.png",
		},
		{
			// FindCoverImage picks the cover-hinted image, which the tagged background
			// already uses: no #COVER is inserted for it.
			name:   "the kept background is never also the cover",
			header: []string{"#BACKGROUND:a cover.png"},
			pngs:   map[string][2]int{"a cover.png": {64, 36}},
			cover:  "", bg: "a cover.png",
		},
		{
			name:   "the kept cover is never also the background",
			header: []string{"#COVER:a bg.png"},
			pngs:   map[string][2]int{"a bg.png": {10, 10}},
			cover:  "a bg.png", bg: "",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			cfg := newLibrary(t, false)
			files := map[string]string{"Song/song.txt": songTxt(append([]string{"#MP3:song.mp3"}, tc.header...)...), "Song/song.mp3": "a"}
			for name, content := range tc.files {
				files["Song/"+name] = content
			}
			writeFiles(t, cfg.SongDir, files)
			for name, wh := range tc.pngs {
				writeTestPNG(t, filepath.Join(cfg.SongDir, "Song", name), wh[0], wh[1])
			}
			ops := run(t, cfg)
			if f := ops.Stats().Failed; f != 0 {
				t.Errorf("%d failed operations", f)
			}
			got := readFile(t, filepath.Join(cfg.SongDir, "Song", "song.txt"))
			for tag, want := range map[string]string{"#COVER:": tc.cover, "#BACKGROUND:": tc.bg} {
				if want == "" {
					if strings.Contains(got, tag) {
						t.Errorf("txt has an unexpected %s line:\n%s", tag, got)
					}
				} else if !strings.Contains(got, tag+want+"\n") {
					t.Errorf("txt lacks %q:\n%s", tag+want, got)
				}
			}
		})
	}
}

func writeTestPNG(t *testing.T, path string, w, h int) {
	t.Helper()
	var buf bytes.Buffer
	if err := png.Encode(&buf, image.NewGray(image.Rect(0, 0, w, h))); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, buf.Bytes(), 0o644); err != nil {
		t.Fatal(err)
	}
}

// notesBody is the body of a minimal song txt.
const notesBody = "#BPM:300\n: 0 4 0 La\nE\n"

func TestRunFillsNameTagsFromFolderName(t *testing.T) {
	tests := []struct {
		name       string
		folder     string
		txt        string
		want       string // whole txt after the run
		wantFilled []string
		wantNote   string // substring of a missing_files.txt line, "" for none
	}{
		{
			name:       "both absent are inserted at the top",
			folder:     "Electric Callboy - Hypa Hypa",
			txt:        "#MP3:song.mp3\n" + notesBody,
			want:       "#TITLE:Hypa Hypa\n#ARTIST:Electric Callboy\n#MP3:song.mp3\n" + notesBody,
			wantFilled: []string{"#ARTIST:Electric Callboy", "#TITLE:Hypa Hypa"},
		},
		{
			name:       "absent artist, title present",
			folder:     "Electric Callboy - Hypa Hypa",
			txt:        "#TITLE:Hypa Hypa\n#MP3:song.mp3\n" + notesBody,
			want:       "#ARTIST:Electric Callboy\n#TITLE:Hypa Hypa\n#MP3:song.mp3\n" + notesBody,
			wantFilled: []string{"#ARTIST:Electric Callboy"},
		},
		{
			name:       "empty tag lines are filled in place, keeping the spelling",
			folder:     "Electric Callboy - Hypa Hypa",
			txt:        "#title:\n#Artist: \n#MP3:song.mp3\n" + notesBody,
			want:       "#title:Hypa Hypa\n#Artist:Electric Callboy\n#MP3:song.mp3\n" + notesBody,
			wantFilled: []string{"#ARTIST:Electric Callboy", "#TITLE:Hypa Hypa"},
		},
		{
			name:       "title with a folder qualifier",
			folder:     "Artist - Song [DUET]",
			txt:        "#TITLE:Song\n#ARTIST:\n#MP3:song.mp3\n" + notesBody,
			want:       "#TITLE:Song\n#ARTIST:Artist\n#MP3:song.mp3\n" + notesBody,
			wantFilled: []string{"#ARTIST:Artist"},
		},
		{
			name:       "empty repeated tag: the last (empty) one is filled",
			folder:     "Artist - Song",
			txt:        "#TITLE:Old\n#ARTIST:Artist\n#TITLE:\n#MP3:song.mp3\n" + notesBody,
			want:       "#ARTIST:Artist\n#TITLE:Song\n#MP3:song.mp3\n" + notesBody,
			wantFilled: []string{"#TITLE:Song"},
		},
		{
			name:     "reversed folder name is not used",
			folder:   "Hypa Hypa - Electric Callboy",
			txt:      "#TITLE:Hypa Hypa\n#MP3:song.mp3\n" + notesBody,
			want:     "#TITLE:Hypa Hypa\n#MP3:song.mp3\n" + notesBody,
			wantNote: "Missing #ARTIST in ",
		},
		{
			name:     "ambiguous folder name is not used",
			folder:   "Artist - Song - Live",
			txt:      "#MP3:song.mp3\n" + notesBody,
			want:     "#MP3:song.mp3\n" + notesBody,
			wantNote: "Missing #ARTIST and #TITLE in ",
		},
		{
			name:       "filled after a leading #VERSION",
			folder:     "Electric Callboy - Hypa Hypa",
			txt:        "#VERSION:1.1.0\n#TITLE:Hypa Hypa\n#AUDIO:song.mp3\n" + notesBody,
			want:       "#VERSION:1.1.0\n#ARTIST:Electric Callboy\n#TITLE:Hypa Hypa\n#AUDIO:song.mp3\n" + notesBody,
			wantFilled: []string{"#ARTIST:Electric Callboy"},
		},
		{
			name:   "tag with a blank before the colon counts as present",
			folder: "Electric Callboy - Hypa Hypa",
			txt:    "#TITLE:Hypa Hypa\n#ARTIST :Real Artist\n#MP3:song.mp3\n" + notesBody,
			want:   "#TITLE:Hypa Hypa\n#ARTIST :Real Artist\n#MP3:song.mp3\n" + notesBody,
		},
		{
			name:   "indented tag counts as present",
			folder: "Foo - Hypa Hypa",
			txt:    "#TITLE:Hypa Hypa\n  #ARTIST:Real Artist\n#MP3:song.mp3\n" + notesBody,
			want:   "#TITLE:Hypa Hypa\n  #ARTIST:Real Artist\n#MP3:song.mp3\n" + notesBody,
		},
		{
			name:       "empty tag with a blank before the colon is filled in place",
			folder:     "Electric Callboy - Hypa Hypa",
			txt:        "#TITLE:Hypa Hypa\n#ARTIST :\n#MP3:song.mp3\n" + notesBody,
			want:       "#TITLE:Hypa Hypa\n#ARTIST :Electric Callboy\n#MP3:song.mp3\n" + notesBody,
			wantFilled: []string{"#ARTIST:Electric Callboy"},
		},
		{
			name:       "empty indented tag is filled in place",
			folder:     "Electric Callboy - Hypa Hypa",
			txt:        "#TITLE:Hypa Hypa\n #artist:\n#MP3:song.mp3\n" + notesBody,
			want:       "#TITLE:Hypa Hypa\n #artist:Electric Callboy\n#MP3:song.mp3\n" + notesBody,
			wantFilled: []string{"#ARTIST:Electric Callboy"},
		},
		{
			name:     "prefix before the artist is not used",
			folder:   "SingStar - Electric Callboy - Hypa Hypa",
			txt:      "#TITLE:Hypa Hypa\n#MP3:song.mp3\n" + notesBody,
			want:     "#TITLE:Hypa Hypa\n#MP3:song.mp3\n" + notesBody,
			wantNote: "Missing #ARTIST in ",
		},
		{
			name:   "present tags are never changed",
			folder: "Other - Name",
			txt:    "#TITLE:T\n#ARTIST:A\n#MP3:song.mp3\n" + notesBody,
			want:   "#TITLE:T\n#ARTIST:A\n#MP3:song.mp3\n" + notesBody,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			cfg := newLibrary(t, false)
			writeFiles(t, cfg.SongDir, map[string]string{
				filepath.Join(tc.folder, "song.txt"):  tc.txt,
				filepath.Join(tc.folder, "song.mp3"):  "audio",
				filepath.Join(tc.folder, "cover.jpg"): "x",
			})
			run(t, cfg)
			txt := filepath.Join(cfg.SongDir, tc.folder, "song.txt")
			if got := readFile(t, txt); got != tc.want {
				t.Errorf("txt = %q, want %q", got, tc.want)
			}
			var wantReport string
			for _, f := range tc.wantFilled {
				wantReport += txt + ": " + f + " (from the folder name)\n"
			}
			if got := readFile(t, cfg.ReportPath(filledTagsReport)); got != wantReport {
				t.Errorf("%s = %q, want %q", filledTagsReport, got, wantReport)
			}
			missing := readFile(t, cfg.ReportPath(missingFilesReport))
			if tc.wantNote != "" && !strings.Contains(missing, tc.wantNote+filepath.Join(cfg.SongDir, tc.folder)+", cannot fill it from the folder name: ") {
				t.Errorf("%s has no note %q:\n%s", missingFilesReport, tc.wantNote, missing)
			}
			if tc.wantNote == "" && strings.Contains(missing, "cannot fill") {
				t.Errorf("unexpected note in %s:\n%s", missingFilesReport, missing)
			}
		})
	}
}

// TestRunFillsNameTagsDryRunAndIdempotent: the dry run plans the fill without touching the
// library, the real run fills, and a second run changes nothing.
func TestRunFillsNameTagsDryRunAndIdempotent(t *testing.T) {
	lib, out := t.TempDir(), t.TempDir()
	writeFiles(t, lib, map[string]string{
		"Cat/Electric Callboy - Hypa Hypa/song.txt": "#MP3:song.mp3\n" + notesBody,
		"Cat/Electric Callboy - Hypa Hypa/song.mp3": "audio",
	})
	txt := filepath.Join(lib, "Cat/Electric Callboy - Hypa Hypa/song.txt")
	wantReport := txt + ": #ARTIST:Electric Callboy (from the folder name)\n" + txt + ": #TITLE:Hypa Hypa (from the folder name)\n"
	newCfg := func(dry bool) config.Config {
		cfg, err := config.New(lib, out, dry)
		if err != nil {
			t.Fatal(err)
		}
		return cfg
	}

	dry := newCfg(true)
	before := hashTree(t, lib)
	ops := run(t, dry)
	if after := hashTree(t, lib); !maps.Equal(before, after) {
		t.Errorf("dry run changed the library:\nbefore %v\nafter  %v", before, after)
	}
	if ops.Stats().Written != 1 {
		t.Errorf("dry run planned %d writes, want 1", ops.Stats().Written)
	}
	wantPlanned := strings.ReplaceAll(wantReport, filledSuffix, plannedSuffix)
	if got := readFile(t, dry.ReportPath(filledTagsReport)); got != wantPlanned {
		t.Errorf("dry-run %s = %q, want %q", filledTagsReport, got, wantPlanned)
	}

	cfg := newCfg(false)
	run(t, cfg)
	want := "#TITLE:Hypa Hypa\n#ARTIST:Electric Callboy\n#MP3:song.mp3\n" + notesBody
	if got := readFile(t, txt); got != want {
		t.Errorf("txt = %q, want %q", got, want)
	}
	if got := readFile(t, cfg.ReportPath(filledTagsReport)); got != wantReport {
		t.Errorf("%s = %q, want %q", filledTagsReport, got, wantReport)
	}

	if ops := run(t, cfg); ops.Stats().Written != 0 {
		t.Errorf("second run wrote %d files, want 0", ops.Stats().Written)
	}
	if got := readFile(t, txt); got != want {
		t.Errorf("second run changed the txt to %q", got)
	}
	// The report keeps the fills of earlier runs, also through a later dry run.
	if got := readFile(t, cfg.ReportPath(filledTagsReport)); got != wantReport {
		t.Errorf("second run %s = %q, want %q", filledTagsReport, got, wantReport)
	}
	run(t, dry)
	if got := readFile(t, cfg.ReportPath(filledTagsReport)); got != wantReport {
		t.Errorf("later dry run %s = %q, want %q", filledTagsReport, got, wantReport)
	}
}

// TestFilledReportMerge: filled_tags.txt keeps written fills of earlier runs, drops the
// lines an earlier dry run only planned, and has no duplicates.
func TestFilledReportMerge(t *testing.T) {
	cfg := newLibrary(t, false)
	old := "/a.txt: #ARTIST:A" + filledSuffix + "\n/b.txt: #TITLE:B" + plannedSuffix + "\n"
	if err := os.WriteFile(cfg.ReportPath(filledTagsReport), []byte(old), 0o644); err != nil {
		t.Fatal(err)
	}
	ops := fsops.New(false)
	writeFilledReport(cfg, ops, []string{"/c.txt: #TITLE:C" + filledSuffix, "/a.txt: #ARTIST:A" + filledSuffix})
	want := "/a.txt: #ARTIST:A" + filledSuffix + "\n/c.txt: #TITLE:C" + filledSuffix + "\n"
	if got := readFile(t, cfg.ReportPath(filledTagsReport)); got != want {
		t.Errorf("%s = %q, want %q", filledTagsReport, got, want)
	}
}

// TestRunDoesNotFillNameTagsOfUndecodableTxt: a txt with a decode Problem is never rewritten.
func TestRunDoesNotFillNameTagsOfUndecodableTxt(t *testing.T) {
	cfg := newLibrary(t, false)
	raw := "#TITLE:\xe4\x81\n#MP3:song.mp3\n" + notesBody
	writeFiles(t, cfg.SongDir, map[string]string{
		"Artist - Song/song.txt": raw,
		"Artist - Song/song.mp3": "audio",
	})
	run(t, cfg)
	if got := readFile(t, filepath.Join(cfg.SongDir, "Artist - Song/song.txt")); got != raw {
		t.Errorf("undecodable txt was rewritten: %q", got)
	}
	if got := readFile(t, cfg.ReportPath(filledTagsReport)); got != "" {
		t.Errorf("%s = %q, want empty", filledTagsReport, got)
	}
}

// TestRunChoosesAudioByFilledNameTags: the "<#ARTIST> - <#TITLE>" audio preference uses the
// filled artist. Neither the txt nor the folder name ("[DUET]") matches the audio file.
func TestRunChoosesAudioByFilledNameTags(t *testing.T) {
	cfg := newLibrary(t, false)
	folder := "Electric Callboy - Hypa Hypa [DUET]"
	writeFiles(t, cfg.SongDir, map[string]string{
		filepath.Join(folder, "song.txt"):                         "#TITLE:Hypa Hypa\n" + notesBody,
		filepath.Join(folder, "Electric Callboy - Hypa Hypa.mp3"): "audio",
		filepath.Join(folder, "other.mp3"):                        "audio",
	})
	run(t, cfg)
	got := readFile(t, filepath.Join(cfg.SongDir, folder, "song.txt"))
	if !strings.Contains(got, "#MP3:Electric Callboy - Hypa Hypa.mp3\n") || !strings.Contains(got, "#ARTIST:Electric Callboy\n") {
		t.Errorf("txt = %q, want the filled artist and the matching audio file", got)
	}
}

// TestRunChoosesAudioByFilledTitle: the same for a filled title. The folder's artist part
// carries a qualifier, so the folder name does not match the audio file either.
func TestRunChoosesAudioByFilledTitle(t *testing.T) {
	cfg := newLibrary(t, false)
	folder := "Electric Callboy [DE] - Hypa Hypa"
	writeFiles(t, cfg.SongDir, map[string]string{
		filepath.Join(folder, "song.txt"):                         "#ARTIST:Electric Callboy\n" + notesBody,
		filepath.Join(folder, "Electric Callboy - Hypa Hypa.mp3"): "audio",
		filepath.Join(folder, "other.mp3"):                        "audio",
	})
	run(t, cfg)
	got := readFile(t, filepath.Join(cfg.SongDir, folder, "song.txt"))
	if !strings.Contains(got, "#MP3:Electric Callboy - Hypa Hypa.mp3\n") || !strings.Contains(got, "#TITLE:Hypa Hypa\n") {
		t.Errorf("txt = %q, want the filled title and the matching audio file", got)
	}
}
