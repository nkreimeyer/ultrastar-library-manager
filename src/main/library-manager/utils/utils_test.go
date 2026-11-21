package utils

import (
	"errors"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"github.com/nkreimeyer/library-manager/config"
	"github.com/nkreimeyer/library-manager/fsops"
	log "github.com/sirupsen/logrus"
)

func TestMain(m *testing.M) {
	log.SetOutput(io.Discard)
	os.Exit(m.Run())
}

// writeFile creates path (and its parents) with the given content.
func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func setMtime(t *testing.T, path string, mt time.Time) {
	t.Helper()
	if err := os.Chtimes(path, mt, mt); err != nil {
		t.Fatal(err)
	}
}

const songTxt = "#TITLE:Song\n#ARTIST:Artist\n#MP3:song.mp3\n#BPM:300\n: 0 4 0 Hel\n: 4 4 0 lo\nE\n"

func TestIsTxtFile(t *testing.T) {
	for name, want := range map[string]bool{
		"song.txt": true, "SONG.TXT": true, "Song.Txt": true, "song.txt.bak": false, "txt": false, "song.mp3": false,
	} {
		if got := IsTxtFile(name); got != want {
			t.Errorf("IsTxtFile(%q) = %v, want %v", name, got, want)
		}
	}
}

func TestIsSongTxt(t *testing.T) {
	tests := []struct {
		name, content string
		want          bool
	}{
		{"plain", songTxt, true},
		{"BOM", "\ufeff#TITLE:Song\n: 0 1 0 a\n", true},
		{"lowercase tags", "#title:Song\n#artist:x\n", true},
		{"mixed case MP3 only", "#Mp3:song.mp3\n", true},
		{"AUDIO tag", "#VERSION:1.1.0\n#AUDIO:song.ogg\n", true},
		{"CRLF", "#VERSION:1.0.0\r\n#BPM:120\r\n", true},
		{"CR only", "#ENCODING:CP1252\r#ARTIST:x\r", true},
		{"leading blank lines", "\n\n  #TITLE:x\n", true},
		{"tag after other header tags", "#VERSION:1.0.0\n#LANGUAGE:German\n#TITLE:x\n", true},
		{"license", "MIT License\n\nPermission is hereby granted...\n#TITLE: not a header\n", false},
		{"junk line inside the header", "#VERSION:1.0.0\nCreated with editor\n#TITLE:x\n: 0 1 0 a\n", true},
		{"tag only after the notes", "#VERSION:1.0.0\n: 0 1 0 a\n#TITLE:x\n", false},
		{"empty", "", false},
		{"only unknown tags", "#VERSION:1.0.0\n#LANGUAGE:German\n", false},
		{"prefix is not enough", "#TITLEX:Song\n", false},
		{"notes only", ": 0 4 0 Hel\nE\n", false},
	}
	dir := t.TempDir()
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			path := filepath.Join(dir, tt.name+".txt")
			writeFile(t, path, tt.content)
			if got := IsSongTxt(nil, path); got != tt.want {
				t.Errorf("IsSongTxt(%q) = %v, want %v", tt.content, got, tt.want)
			}
		})
	}
	if IsSongTxt(nil, filepath.Join(dir, "missing.txt")) {
		t.Error("missing file reported as song txt")
	}
}

func TestListAndSongTxtFiles(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "b.txt"), songTxt)
	writeFile(t, filepath.Join(dir, "A.TXT"), songTxt)
	writeFile(t, filepath.Join(dir, "License.txt"), "MIT\n")
	writeFile(t, filepath.Join(dir, "song.mp3"), "audio")
	writeFile(t, filepath.Join(dir, "sub", "nested.txt"), songTxt) // not top-level
	if err := os.Mkdir(filepath.Join(dir, "dir.txt"), 0o755); err != nil {
		t.Fatal(err)
	}

	all, err := ListTxtFiles(dir)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{filepath.Join(dir, "A.TXT"), filepath.Join(dir, "License.txt"), filepath.Join(dir, "b.txt")}
	if !reflect.DeepEqual(all, want) {
		t.Errorf("ListTxtFiles = %v, want %v", all, want)
	}
	songs, err := SongTxtFiles(nil, dir)
	if err != nil {
		t.Fatal(err)
	}
	want = []string{filepath.Join(dir, "A.TXT"), filepath.Join(dir, "b.txt")}
	if !reflect.DeepEqual(songs, want) {
		t.Errorf("SongTxtFiles = %v, want %v", songs, want)
	}
	if _, err := ListTxtFiles(filepath.Join(dir, "missing")); err == nil {
		t.Error("ListTxtFiles on missing dir succeeded")
	}
}

func TestFindSongTxt(t *testing.T) {
	base := time.Now().Add(-time.Hour)
	t.Run("ignores non-song txt and picks newest", func(t *testing.T) {
		dir := t.TempDir()
		writeFile(t, filepath.Join(dir, "old.txt"), songTxt)
		writeFile(t, filepath.Join(dir, "new.txt"), songTxt)
		writeFile(t, filepath.Join(dir, "License.txt"), "Licensed under CC-BY\n")
		setMtime(t, filepath.Join(dir, "old.txt"), base)
		setMtime(t, filepath.Join(dir, "new.txt"), base.Add(time.Minute))
		setMtime(t, filepath.Join(dir, "License.txt"), base.Add(time.Hour)) // newest, but no song
		got, err := FindSongTxt(nil, dir)
		if err != nil {
			t.Fatal(err)
		}
		if want := filepath.Join(dir, "new.txt"); got != want {
			t.Errorf("FindSongTxt = %q, want %q", got, want)
		}
	})
	t.Run("tie keeps first by name", func(t *testing.T) {
		dir := t.TempDir()
		for _, n := range []string{"b.txt", "a.txt"} {
			writeFile(t, filepath.Join(dir, n), songTxt)
			setMtime(t, filepath.Join(dir, n), base)
		}
		got, err := FindSongTxt(nil, dir)
		if err != nil {
			t.Fatal(err)
		}
		if want := filepath.Join(dir, "a.txt"); got != want {
			t.Errorf("FindSongTxt = %q, want %q", got, want)
		}
	})
	t.Run("no song txt", func(t *testing.T) {
		dir := t.TempDir()
		writeFile(t, filepath.Join(dir, "readme.txt"), "hello\n")
		if _, err := FindSongTxt(nil, dir); !errors.Is(err, os.ErrNotExist) {
			t.Errorf("FindSongTxt err = %v, want os.ErrNotExist", err)
		}
	})
}

func TestNewestFile(t *testing.T) {
	dir := t.TempDir()
	a, b := filepath.Join(dir, "a"), filepath.Join(dir, "b")
	writeFile(t, a, "")
	writeFile(t, b, "")
	setMtime(t, a, time.Now())
	setMtime(t, b, time.Now().Add(-time.Hour))
	got, err := NewestFile([]string{filepath.Join(dir, "missing"), b, a})
	if err != nil || got != a {
		t.Errorf("NewestFile = %q, %v; want %q", got, err, a)
	}
	if _, err := NewestFile(nil); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("NewestFile(nil) err = %v, want os.ErrNotExist", err)
	}
}

func TestIsFileReferenced(t *testing.T) {
	const nfc, nfd = "M\u00e4dchen.mp3", "Ma\u0308dchen.mp3"
	tests := []struct {
		name  string
		txt   []byte // nil: no song txt in the folder
		check string
		want  bool
	}{
		{"exact", []byte(songTxt), "song.mp3", true},
		{"case-insensitive", []byte(songTxt), "SONG.MP3", true},
		{"not referenced", []byte(songTxt), "cover.jpg", false},
		{"NFC txt, NFD name", []byte("#TITLE:x\n#MP3:" + nfc + "\n"), nfd, true},
		{"NFD txt, NFC name", []byte("#TITLE:x\n#MP3:" + nfd + "\n"), nfc, true},
		{"NFC txt, uppercase NFD name", []byte("#TITLE:x\n#MP3:" + nfc + "\n"), "MA\u0308DCHEN.MP3", true},
		{"CP1252 txt", []byte("#TITLE:x\n#MP3:M\xe4dchen.mp3\n#COVER:\x93Quote\x94.jpg\n"), nfc, true},
		{"CP1252 txt, special chars", []byte("#TITLE:x\n#COVER:\x93Quote\x94.jpg\n"), "“Quote”.jpg", true},
		{"CP1252 txt, NFD name", []byte("#TITLE:x\n#MP3:M\xe4dchen.mp3\n"), nfd, true},
		{"no txt", nil, "anything.mp3", true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dir := t.TempDir()
			writeFile(t, filepath.Join(dir, "License.txt"), "mentions cover.jpg\n")
			if tt.txt != nil {
				writeFile(t, filepath.Join(dir, "song.txt"), string(tt.txt))
			}
			if got := IsFileReferenced(nil, dir, tt.check); got != tt.want {
				t.Errorf("IsFileReferenced(%q) = %v, want %v", tt.check, got, tt.want)
			}
		})
	}
	if !IsFileReferenced(nil, filepath.Join(t.TempDir(), "missing"), "x.mp3") {
		t.Error("missing folder: want every file to count as referenced")
	}
}

func TestGetAllSongFolders(t *testing.T) {
	type entry struct {
		path    string // relative to the library; a trailing "/" creates an empty dir
		content string
	}
	tests := []struct {
		name  string
		files []entry
		want  []string
	}{
		{
			name: "flat",
			files: []entry{
				{"A - One/song.txt", songTxt}, {"A - One/song.mp3", "a"},
				{"B - Two/song.txt", songTxt},
				{"loose.txt", songTxt}, // files in the root are ignored
			},
			want: []string{"A - One", "B - Two"},
		},
		{
			name: "nested categories",
			files: []entry{
				{"Rock/A - One/song.txt", songTxt},
				{"Rock/80s/B - Two/song.txt", songTxt},
				{"Pop/C - Three/song.txt", songTxt},
			},
			want: []string{"Pop/C - Three", "Rock/80s/B - Two", "Rock/A - One"},
		},
		{
			name: "broken song with empty covers subfolder",
			files: []entry{
				{"A - Broken/song.mp3", "a"}, {"A - Broken/covers/", ""},
			},
			want: []string{"A - Broken"},
		},
		{
			name: "category with songs and broken leaf",
			files: []entry{
				{"Cat/A - One/song.txt", songTxt},
				{"Cat/B - Two/song.txt", songTxt},
				{"Cat/C - Broken/song.mp3", "a"},
				{"Cat/C - Broken/covers/", ""},
			},
			want: []string{"Cat/A - One", "Cat/B - Two", "Cat/C - Broken"},
		},
		{
			name:  "empty folder",
			files: []entry{{"Empty/", ""}, {"A - One/song.txt", songTxt}},
			want:  []string{"A - One", "Empty"},
		},
		{
			name:  "uppercase TXT",
			files: []entry{{"A - One/SONG.TXT", songTxt}, {"A - One/sub/other.txt", songTxt}},
			want:  []string{"A - One"},
		},
		{
			name: "category with readme.txt is still a category",
			files: []entry{
				{"Cat/readme.txt", "Songs collected by ...\n"},
				{"Cat/A - One/song.txt", songTxt},
			},
			want: []string{"Cat/A - One"},
		},
		{
			name:  "leaf with only a non-song txt is returned",
			files: []entry{{"A - Broken/License.txt", "MIT\n"}, {"A - Broken/song.mp3", "a"}},
			want:  []string{"A - Broken"},
		},
		{
			name: "category of only broken songs is not one song",
			files: []entry{
				{"Cat/A - Broken/song.mp3", "a"},
				{"Cat/B - Broken/song.mp3", "a"},
			},
			want: []string{"Cat/A - Broken", "Cat/B - Broken"},
		},
		{
			name: "category with loose files and only broken songs below",
			files: []entry{
				{"Cat/readme.txt", "about this category\n"},
				{"Cat/intro.mp3", "a"},
				{"Cat/A - Broken/song.mp3", "a"},
				{"Cat/B - Broken/clip.mp4", "v"},
			},
			want: []string{"Cat/A - Broken", "Cat/B - Broken"},
		},
		{
			name: "utf16 song txts are songs",
			files: []entry{
				{"Cat/A - One/song.txt", "\xff\xfe#\x00T\x00I\x00T\x00L\x00E\x00:\x00A\x00\n\x00"},
				{"Cat/A - One/song.mp3", "a"},
			},
			want: []string{"Cat/A - One"},
		},
		{
			name: "hidden, @eaDir and output folders are skipped",
			files: []entry{
				{".Trash/A - One/song.txt", songTxt},
				{"@eaDir/x.jpg", "x"},
				{"sanitizer-output/" + config.OutputMarker, ""},
				{"sanitizer-output/extra-files/A - One/song.txt", songTxt},
				{"B - Two/song.txt", songTxt},
			},
			want: []string{"B - Two"},
		},
		{
			name:  "empty library",
			files: nil,
			want:  nil,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			lib := t.TempDir()
			for _, f := range tt.files {
				p := filepath.Join(lib, filepath.FromSlash(f.path))
				if f.path[len(f.path)-1] == '/' {
					if err := os.MkdirAll(p, 0o755); err != nil {
						t.Fatal(err)
					}
					continue
				}
				writeFile(t, p, f.content)
			}
			got, err := GetAllSongFolders(nil, lib)
			if err != nil {
				t.Fatal(err)
			}
			var want []string
			for _, w := range tt.want {
				want = append(want, filepath.Join(lib, filepath.FromSlash(w)))
			}
			if !reflect.DeepEqual(got, want) {
				t.Errorf("GetAllSongFolders =\n%v\nwant\n%v", got, want)
			}
		})
	}
	if _, err := GetAllSongFolders(nil, filepath.Join(t.TempDir(), "missing")); err == nil {
		t.Error("missing song dir: expected error")
	}
}

func TestGetAllSongFoldersSkipsUnreadableAndPlannedMoves(t *testing.T) {
	lib := t.TempDir()
	for _, f := range []string{"A - One/song.txt", "B - Two/song.txt", "Locked/C - Three/song.txt"} {
		writeFile(t, filepath.Join(lib, filepath.FromSlash(f)), songTxt)
	}

	ops := fsops.New(true)
	if _, err := ops.MoveInto(filepath.Join(lib, "B - Two"), t.TempDir(), "B - Two"); err != nil {
		t.Fatal(err)
	}
	locked := filepath.Join(lib, "Locked")
	if os.Geteuid() != 0 { // root can read anything
		if err := os.Chmod(locked, 0); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { os.Chmod(locked, 0o755) })
	}

	got, err := GetAllSongFolders(ops, lib)
	if err != nil {
		t.Fatalf("an unreadable subfolder must not abort discovery: %v", err)
	}
	want := []string{filepath.Join(lib, "A - One")}
	if os.Geteuid() == 0 {
		want = append(want, filepath.Join(lib, "Locked", "C - Three"))
	} else if ops.Stats().Failed != 1 {
		t.Errorf("Failed = %d, want 1 for the unreadable folder", ops.Stats().Failed)
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got %v, want %v (planned move of B must be skipped)", got, want)
	}
}

// Every module calls GetAllSongFolders with the same ops; an unreadable song txt or folder
// must be skipped (not mistaken for a broken song) and counted only once per run.
func TestGetAllSongFoldersCountsUnreadableOnce(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root can read everything")
	}
	lib := t.TempDir()
	// Cat2 contains nothing but the unreadable folder: its content is unknown, so Cat2 must
	// not be returned as one (broken) song.
	for _, f := range []string{"A - One/song.txt", "Cat/B - Locked/song.txt", "Cat/C - Fine/song.txt", "Cat2/Locked/D - Hidden/song.txt"} {
		writeFile(t, filepath.Join(lib, filepath.FromSlash(f)), songTxt)
	}
	writeFile(t, filepath.Join(lib, "Cat", "B - Locked", "song.mp3"), "audio")
	lockedTxt := filepath.Join(lib, "Cat", "B - Locked", "song.txt")
	lockedDir := filepath.Join(lib, "Cat2", "Locked")
	for path, mode := range map[string]os.FileMode{lockedTxt: 0o644, lockedDir: 0o755} {
		if err := os.Chmod(path, 0); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { os.Chmod(path, mode) })
	}

	for _, dry := range []bool{false, true} {
		ops := fsops.New(dry)
		for i := range 4 {
			got, err := GetAllSongFolders(ops, lib)
			if err != nil {
				t.Fatal(err)
			}
			want := []string{filepath.Join(lib, "A - One"), filepath.Join(lib, "Cat", "C - Fine")}
			if !reflect.DeepEqual(got, want) {
				t.Errorf("dry=%v call %d: got %v, want %v", dry, i, got, want)
			}
		}
		if s := ops.Stats(); s.Failed != 2 {
			t.Errorf("dry=%v: Failed = %d after 4 scans, want 2 (one txt, one folder)", dry, s.Failed)
		}
	}
}

func TestMediaFileTypes(t *testing.T) {
	for _, tt := range []struct {
		name                 string
		audio, video, imageF bool
	}{
		{"song.mp3", true, false, false},
		{"song.MKA", true, false, false}, // Matroska audio
		{"song.mka.part", false, false, false},
		{"clip.mkv", false, true, false},
		{"song.opus", true, false, false},
		{"cover.webp", false, false, true},
		{"cover.JFIF", false, false, true},
		{"cover.tif", false, false, true},
		{"cover.tiff", false, false, true},
		{"cover.jpg", false, false, true},
		{"cover.bmp", false, false, true},
		{"cover.heic", false, false, false},
		{"notes.txt", false, false, false},
	} {
		if got := IsAudioFile(tt.name); got != tt.audio {
			t.Errorf("IsAudioFile(%q) = %v, want %v", tt.name, got, tt.audio)
		}
		if got := IsVideoFile(tt.name); got != tt.video {
			t.Errorf("IsVideoFile(%q) = %v, want %v", tt.name, got, tt.video)
		}
		if got := IsImageFile(tt.name); got != tt.imageF {
			t.Errorf("IsImageFile(%q) = %v, want %v", tt.name, got, tt.imageF)
		}
	}
}

// A folder whose only audio is Matroska audio is a song, not a broken folder.
func TestGetAllSongFoldersMatroskaAudio(t *testing.T) {
	lib := t.TempDir()
	writeFile(t, filepath.Join(lib, "Cat", "Song", "song.mka"), "a")
	writeFile(t, filepath.Join(lib, "Cat", "Broken", "song.mka"), "a") // no txt: broken song
	writeFile(t, filepath.Join(lib, "Cat", "Song", "song.txt"), songTxt)
	cfg, err := config.New(lib, t.TempDir(), false)
	if err != nil {
		t.Fatal(err)
	}
	got, err := GetAllSongFolders(fsops.New(false), cfg.SongDir)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{filepath.Join(cfg.SongDir, "Cat", "Broken"), filepath.Join(cfg.SongDir, "Cat", "Song")}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("GetAllSongFolders = %q, want %q", got, want)
	}
}
