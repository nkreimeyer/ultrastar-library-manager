package fixtxt

import (
	"crypto/sha256"
	"encoding/hex"
	"io/fs"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/nkreimeyer/ultrastar-songs-sanitizer/config"
	"github.com/nkreimeyer/ultrastar-songs-sanitizer/fsops"
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
	in := []string{"#TITLE:A\n", "#MP3:a.mp3\n", "#mp3:b.mp3\n", "#VIDEO:v.mp4\n", "#VIDEOGAP:1\n", ": 0 1 0 x\n"}
	want := []string{"#TITLE:A\n", "#MP3:a.mp3\n", "#VIDEO:v.mp4\n", "#VIDEOGAP:1\n", ": 0 1 0 x\n"}
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

func TestRunDoesNotInsertNonUTF8FileNames(t *testing.T) {
	cfg := newLibrary(t, false)
	latin1Name := "M\xe4dchen.mp3"
	writeFiles(t, cfg.SongDir, map[string]string{
		"Song/song.txt":      songTxt("#VIDEO:video.mp4"),
		"Song/" + latin1Name: "a",
		"Song/video.mp4":     "v",
	})
	run(t, cfg)
	got := readFile(t, filepath.Join(cfg.SongDir, "Song", "song.txt"))
	if !utf8.ValidString(got) || strings.Contains(got, "#MP3:") {
		t.Errorf("a non-UTF-8 file name must not be written into the txt:\n%q", got)
	}
	if report := readFile(t, cfg.ReportPath(encodingIssuesReport)); !strings.Contains(report, "not valid UTF-8") {
		t.Errorf("file name not reported: %q", report)
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
