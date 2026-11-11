package utils

import (
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/nkreimeyer/ultrastar-songs-sanitizer/config"
	"github.com/nkreimeyer/ultrastar-songs-sanitizer/fsops"
)

// Tests for behaviours the 2026-11 audit found unpinned.

// TestDecodeTxtEdgeCases extends TestDecodeTxt: #ENCODING spellings and placement, the
// unknown-codec precedence, the CP1250 heuristics and the UTF-8 sequence classes.
func TestDecodeTxtEdgeCases(t *testing.T) {
	tests := []struct {
		name     string
		raw      string
		want     string
		encoding string
		problem  bool
	}{
		// #ENCODING values that name no single-byte codec.
		{"utf-8 tag", "#ENCODING:UTF-8\n#TITLE:Mädchen\n", "#ENCODING:UTF-8\n#TITLE:Mädchen\n", EncUTF8, false},
		{"utf8 tag on cp1252 bytes", "#ENCODING:utf8\n#TITLE:M\xe4dchen\n", "#ENCODING:utf8\n#TITLE:Mädchen\n", EncCP1252, false},
		{"auto tag", "#ENCODING:auto\n#TITLE:M\xe4dchen\n", "#ENCODING:auto\n#TITLE:Mädchen\n", EncCP1252, false},
		{"locale tag", "#ENCODING:Locale\n#TITLE:M\xe4dchen\n", "#ENCODING:Locale\n#TITLE:Mädchen\n", EncCP1252, false},
		{"empty tag", "#ENCODING:\n#TITLE:M\xe4dchen\n", "#ENCODING:\n#TITLE:Mädchen\n", EncCP1252, false},
		// Spellings of the tag.
		{"lower-case tag and value", "#encoding:cp1250\n#TITLE:\xb9\n", "#encoding:cp1250\n#TITLE:ą\n", EncCP1250, false},
		{"blanks around the key", "# ENCODING : windows-1250 \n#TITLE:\xb9\n", "# ENCODING : windows-1250 \n#TITLE:ą\n", EncCP1250, false},
		{"bom before the tag", "\xef\xbb\xbf#ENCODING:CP1251\n#TITLE:\xca\xe0\n", "#ENCODING:CP1251\n#TITLE:Ка\n", "CP1251", false},
		{"line without colon before the tag", "#NOCOLON\n\n#ENCODING:CP1251\n#TITLE:\xca\xe0\n", "#NOCOLON\n\n#ENCODING:CP1251\n#TITLE:Ка\n", "CP1251", false},
		{"tag after the first note line is ignored", ": 0 1 2 x\n#ENCODING:CP1251\n\xca\xe0\n", ": 0 1 2 x\n#ENCODING:CP1251\nÊà\n", EncCP1252, false},
		{"only the first tag counts", "#ENCODING:CP1251\n#ENCODING:CP1250\n#TITLE:\xca\xe0\n", "#ENCODING:CP1251\n#ENCODING:CP1250\n#TITLE:Ка\n", "CP1251", false},
		// An unknown codec is a Problem even for valid UTF-8.
		{"unknown codec on valid utf8", "#ENCODING:EBCDIC\n#TITLE:Mädchen\n", "#ENCODING:EBCDIC\n#TITLE:MÃ¤dchen\n", EncLatin1, true},
		// Valid UTF-8 with accidental and real sequences, but no Syriac/Thaana/NKo.
		{"isolated cjk next to real utf8 is utf8", "#TITLE:aim\xe9\xa0\xbb M\xc3\xa4dchen\n", "#TITLE:aim頻 Mädchen\n", EncUTF8, false},
		// The CP1250 artefact check needs no tag and letters on both sides.
		{"cp1252 tag disables the cp1250 check", "#ENCODING:CP1252\n#TITLE:Za\xbf\xf3\xb3\xe6\n", "#ENCODING:CP1252\n#TITLE:Za¿ó³æ\n", EncCP1252, false},
		{"spanish question mark", "#TITLE:Hola \xbfQu\xe9?\n", "#TITLE:Hola ¿Qué?\n", EncCP1252, false},
		{"cubic metre", "#TITLE:5 m\xb3 \xe4\n", "#TITLE:5 m³ ä\n", EncCP1252, false},
		{"artefact at the very end", "#TITLE:ab\xbf", "#TITLE:ab¿", EncCP1252, false},
		{"artefact before a digit", "#TITLE:a\xb31\n", "#TITLE:a³1\n", EncCP1252, false},
		{"artefact between letters", "#TITLE:a\xb3b", "#TITLE:a³b", EncCP1252, true},
		// #LANGUAGE lists.
		{"language list with comma", "#LANGUAGE:English, Polish\n#TITLE:\xb9\n", "#LANGUAGE:English, Polish\n#TITLE:ą\n", EncCP1250, false},
		{"language list with slash", "#LANGUAGE:German/Czech\n#TITLE:\xb9\n", "#LANGUAGE:German/Czech\n#TITLE:ą\n", EncCP1250, false},
		{"language list with semicolon", "#LANGUAGE:English;Hungarian\n#TITLE:\xb9\n", "#LANGUAGE:English;Hungarian\n#TITLE:ą\n", EncCP1250, false},
		{"language with blanks", "#LANGUAGE:  slovak  \n#TITLE:\xb9\n", "#LANGUAGE:  slovak  \n#TITLE:ą\n", EncCP1250, false},
		{"western language list", "#LANGUAGE:English, German\n#TITLE:\xb9\n", "#LANGUAGE:English, German\n#TITLE:¹\n", EncCP1252, false},
		// UTF-8 sequence classes.
		{"emoji in cp1252 file is mixed", "#TITLE:\xe4 \xf0\x9f\x98\x80\n", "#TITLE:ä ðŸ˜€\n", EncCP1252, true},
		{"isolated c6 pair is accidental", "#TITLE:a \xc6\x85\n", "#TITLE:a ƅ\n", EncUTF8, true},
		{"isolated c5 pair is real utf8", "#TITLE:a \xc5\x82\n", "#TITLE:a ł\n", EncUTF8, false},
		{"isolated e2 sequence is real utf8", "#TITLE:a \xe2\x80\x99s\n", "#TITLE:a ’s\n", EncUTF8, false},
		{"isolated emoji is real utf8", "#TITLE:a \xf0\x9f\x98\x80\n", "#TITLE:a 😀\n", EncUTF8, false},
		{"pair at the very start is isolated", "\xc6\x85 a", "ƅ a", EncUTF8, true},
		{"pair at the very end is isolated", "a \xc6\x85", "a ƅ", EncUTF8, true},
		{"adjacent pairs are a run", "#TITLE:\xc6\x85\xc6\x85\n", "#TITLE:ƅƅ\n", EncUTF8, false},
		{"pair after a non-ascii byte is no run", "#TITLE:\xc6\x85\xe4\n", "#TITLE:Æ…ä\n", EncCP1252, true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := DecodeTxt([]byte(tc.raw))
			if got.Text != tc.want || got.Encoding != tc.encoding || (got.Problem != "") != tc.problem {
				t.Errorf("DecodeTxt(%q) = %q, %s, problem=%q; want %q, %s, problem=%v",
					tc.raw, got.Text, got.Encoding, got.Problem, tc.want, tc.encoding, tc.problem)
			}
		})
	}
}

// TestDecodeTxtUTF16WithoutBOMByteOrder pins how bomlessUTF16 tells LE from BE and from
// stray NUL bytes: more than half of the pairs must have their NUL at the same offset.
func TestDecodeTxtUTF16WithoutBOMByteOrder(t *testing.T) {
	tests := []struct {
		name, raw, text, enc string
	}{
		// 2 pairs, NULs at odd offsets in both: LE.
		{"two LE pairs", "a\x00b\x00", "ab", EncUTF16LE},
		// 4 pairs, 2 odd NULs: exactly half is not enough.
		{"half LE pairs", "a\x00b\x00cdef", "a\x00b\x00cdef", EncUTF8},
		// 3 pairs, 2 odd NULs: more than half.
		{"most LE pairs", "a\x00b\x00cd", "ab" + string(rune(0x6463)), EncUTF16LE},
		// As many NULs at even as at odd offsets: no byte order.
		{"NULs at both offsets", "\x00\x00\x00\x00", "\x00\x00\x00\x00", EncUTF8},
		{"two BE pairs", "\x00a\x00b", "ab", EncUTF16BE},
		{"half BE pairs", "\x00a\x00bcdef", "\x00a\x00bcdef", EncUTF8},
		// 3 even NULs, 2 odd: BE wins.
		{"more even than odd NULs", "\x00\x00\x00\x00\x00a", "\x00\x00a", EncUTF16BE},
	}
	for _, tc := range tests {
		d := DecodeTxt([]byte(tc.raw))
		if d.Problem == "" || d.Text != tc.text || d.Encoding != tc.enc {
			t.Errorf("%s: DecodeTxt(%q) = %q, %s, problem=%q; want %q, %s with a Problem", tc.name, tc.raw, d.Text, d.Encoding, d.Problem, tc.text, tc.enc)
		}
	}
}

// TestReplaceFileReferenceDetails: BOM kept, header end, several tags, NFD values, blanks
// in the tag name, and the error paths.
func TestReplaceFileReferenceDetails(t *testing.T) {
	setup := func(t *testing.T, content string) (string, string) {
		dir := t.TempDir()
		txt := filepath.Join(dir, "song.txt")
		writeFile(t, txt, content)
		return dir, txt
	}
	tests := []struct {
		name, content, old, new, want string
	}{
		{"bom, several tags, header end", "\ufeff#VIDEO:a.avi\n#MP3:A.AVI\n#TITLE:a.avi\n: 0 1 2 x\n#VIDEO:a.avi\n", "a.avi", "a.mp4",
			"\ufeff#VIDEO:a.mp4\n#MP3:a.mp4\n#TITLE:a.avi\n: 0 1 2 x\n#VIDEO:a.avi\n"},
		{"nfd value", "#TITLE:t\n#VIDEO:Mädchen.avi\n", "Mädchen.avi", "Mädchen.mp4", "#TITLE:t\n#VIDEO:Mädchen.mp4\n"},
		{"blank in the tag name", "#TITLE:t\n#VIDEO :x.avi\n#cover: x.avi \n", "x.avi", "x.mp4", "#TITLE:t\n#VIDEO :x.mp4\n#cover:x.mp4\n"},
		{"empty lines in the header", "#TITLE:t\n\n  \n#AUDIO:x.ogg\n", "x.ogg", "x.opus", "#TITLE:t\n\n  \n#AUDIO:x.opus\n"},
		{"all file tags", "#MP3:f\n#AUDIO:f\n#VOCALS:f\n#INSTRUMENTAL:f\n#VIDEO:f\n#COVER:f\n#BACKGROUND:f\n#VIDEOGAP:f\n#NOCOLON f\n",
			"f", "g", "#MP3:g\n#AUDIO:g\n#VOCALS:g\n#INSTRUMENTAL:g\n#VIDEO:g\n#COVER:g\n#BACKGROUND:g\n#VIDEOGAP:f\n#NOCOLON f\n"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			dir, txt := setup(t, tc.content)
			replaced, err := ReplaceFileReference(fsops.New(false), dir, tc.old, tc.new)
			got, _ := os.ReadFile(txt)
			if err != nil || !replaced || string(got) != tc.want {
				t.Errorf("replaced=%v err=%v file=%q; want %q", replaced, err, got, tc.want)
			}
		})
	}

	t.Run("no song txt", func(t *testing.T) {
		replaced, err := ReplaceFileReference(fsops.New(false), t.TempDir(), "a", "b")
		if replaced || !errors.Is(err, os.ErrNotExist) {
			t.Errorf("replaced=%v err=%v; want os.ErrNotExist", replaced, err)
		}
	})
	t.Run("non utf8 txt", func(t *testing.T) {
		dir, txt := setup(t, "#TITLE:M\xe4dchen\n#VIDEO:v.avi\n")
		replaced, err := ReplaceFileReference(fsops.New(false), dir, "v.avi", "v.mp4")
		got, _ := os.ReadFile(txt)
		if replaced || !errors.Is(err, ErrTxtNotUTF8) || string(got) != "#TITLE:M\xe4dchen\n#VIDEO:v.avi\n" {
			t.Errorf("replaced=%v err=%v file=%q", replaced, err, got)
		}
	})
	t.Run("dry run leaves the file and plans the write", func(t *testing.T) {
		dir, txt := setup(t, "#TITLE:t\n#VIDEO:v.avi\n")
		ops := fsops.New(true)
		replaced, err := ReplaceFileReference(ops, dir, "v.avi", "v.mp4")
		got, _ := os.ReadFile(txt)
		planned, _ := ops.ReadFile(txt)
		if err != nil || !replaced || string(got) != "#TITLE:t\n#VIDEO:v.avi\n" || string(planned) != "#TITLE:t\n#VIDEO:v.mp4\n" {
			t.Errorf("replaced=%v err=%v disk=%q planned=%q", replaced, err, got, planned)
		}
	})
	t.Run("write error", func(t *testing.T) {
		if os.Geteuid() == 0 {
			t.Skip("root can write everywhere")
		}
		dir, txt := setup(t, "#TITLE:t\n#VIDEO:v.avi\n")
		if err := os.Chmod(dir, 0o555); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { os.Chmod(dir, 0o755) })
		replaced, err := ReplaceFileReference(fsops.New(false), dir, "v.avi", "v.mp4")
		got, _ := os.ReadFile(txt)
		if replaced || err == nil || string(got) != "#TITLE:t\n#VIDEO:v.avi\n" {
			t.Errorf("replaced=%v err=%v file=%q; want an error and the file unchanged", replaced, err, got)
		}
	})
}

func TestResolveFileNameDetails(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "Song.mp3"), "")
	writeFile(t, filepath.Join(dir, "Mädchen.mp3"), "")
	if _, err := os.Stat(filepath.Join(dir, "song.mp3")); err == nil {
		t.Skip("case-insensitive file system")
	}
	writeFile(t, filepath.Join(dir, "song.mp3"), "")
	for _, name := range []string{"song.mp3", "Song.mp3"} {
		if got, exact, found := ResolveFileName(dir, name); got != name || !exact || !found {
			t.Errorf("ResolveFileName(%q) = %q, %v, %v; want the exact match", name, got, exact, found)
		}
	}
	if got, exact, found := ResolveFileName(dir, "MÄDCHEN.MP3"); got != "Mädchen.mp3" || exact || !found {
		t.Errorf("ResolveFileName(MÄDCHEN.MP3) = %q, %v, %v", got, exact, found)
	}
	if got, exact, found := ResolveFileName(filepath.Join(dir, "missing"), "Song.mp3"); got != "" || exact || found {
		t.Errorf("ResolveFileName(missing dir) = %q, %v, %v", got, exact, found)
	}
}

func TestIsFileReferencedAndReadSongTagsDetails(t *testing.T) {
	dir := t.TempDir()
	// No song txt: every file counts as referenced, and there are no tags.
	if !IsFileReferenced(nil, dir, "anything.mp3") {
		t.Error("IsFileReferenced without a song txt = false, want true")
	}
	if tags, err := ReadSongTags(nil, dir); tags != nil || !errors.Is(err, os.ErrNotExist) {
		t.Errorf("ReadSongTags without a song txt = %v, %v", tags, err)
	}
	// A CP1252 txt referencing a CP1252 file name (raw bytes) and its UTF-8 spelling.
	writeFile(t, filepath.Join(dir, "song.txt"), "#TITLE:t\n#MP3:M\xe4dchen.mp3\n#COVER:Cover.JPG\n")
	for name, want := range map[string]bool{
		"M\xe4dchen.mp3": true, // raw byte match
		"Mädchen.mp3":    true, // decoded match
		"MÄDCHEN.MP3":    true,
		"cover.jpg":      true,
		"Mädchen.mp3":   true, // NFD
		"other.mp3":      false,
	} {
		if got := IsFileReferenced(nil, dir, name); got != want {
			t.Errorf("IsFileReferenced(%q) = %v, want %v", name, got, want)
		}
	}
	if os.Geteuid() != 0 {
		// An unreadable song txt: every file counts as referenced, and ReadSongTags fails.
		dir2 := t.TempDir()
		txt := filepath.Join(dir2, "song.txt")
		writeFile(t, txt, "#TITLE:t\n")
		if err := os.Chmod(txt, 0); err != nil {
			t.Fatal(err)
		}
		if !IsFileReferenced(nil, dir2, "other.mp3") {
			t.Error("IsFileReferenced with an unreadable txt = false, want true")
		}
		if _, err := ReadSongTags(nil, dir2); err == nil {
			t.Error("ReadSongTags with an unreadable txt: no error")
		}
	}
}

// TestMediaFileHelpers covers IsRegularFile, mediaEntries (AudioFiles, VideoFiles,
// AudioFileExists) and NonUTF8MediaNames.
func TestMediaFileHelpers(t *testing.T) {
	dir := t.TempDir()
	for _, n := range []string{"b.mp3", "a.ogg", "._b.mp3", ".hidden.mp4", "v.mp4", "notes.txt", "M\xe4dchen.mp3", "C\xf6ver.jpg", "R\xe9adme.txt"} {
		writeFile(t, filepath.Join(dir, n), "x")
	}
	if err := os.Mkdir(filepath.Join(dir, "c.mp3"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(dir, "D\xfcr.mp4"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("b.mp3", filepath.Join(dir, "d.mp3")); err != nil {
		t.Skipf("symlinks not supported: %v", err)
	}
	if err := os.Symlink("missing.mp3", filepath.Join(dir, "e.mp3")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("c.mp3", filepath.Join(dir, "f.mp3")); err != nil {
		t.Fatal(err)
	}

	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	regular := map[string]bool{}
	for _, e := range entries {
		regular[e.Name()] = IsRegularFile(dir, e)
	}
	for name, want := range map[string]bool{"b.mp3": true, "c.mp3": false, "d.mp3": true, "e.mp3": false, "f.mp3": false} {
		if regular[name] != want {
			t.Errorf("IsRegularFile(%q) = %v, want %v", name, regular[name], want)
		}
	}

	if got, want := AudioFiles(dir), []string{"M\xe4dchen.mp3", "a.ogg", "b.mp3", "d.mp3"}; !slices.Equal(got, want) {
		t.Errorf("AudioFiles = %q, want %q", got, want)
	}
	if got, want := VideoFiles(dir), []string{"v.mp4"}; !slices.Equal(got, want) {
		t.Errorf("VideoFiles = %q, want %q", got, want)
	}
	if !AudioFileExists(dir) {
		t.Error("AudioFileExists = false")
	}
	empty := t.TempDir()
	writeFile(t, filepath.Join(empty, "._a.mp3"), "x")
	writeFile(t, filepath.Join(empty, "v.mp4"), "x")
	if AudioFileExists(empty) || AudioFiles(filepath.Join(empty, "missing")) != nil {
		t.Error("AudioFileExists with only a hidden audio file = true")
	}

	if got, want := NonUTF8MediaNames(entries), []string{"C\xf6ver.jpg", "M\xe4dchen.mp3"}; !slices.Equal(got, want) {
		t.Errorf("NonUTF8MediaNames = %q, want %q", got, want)
	}
}

func TestSplitIntoBatchesEdges(t *testing.T) {
	if got := SplitIntoBatches([]int{}, 2); !reflect.DeepEqual(got, [][]int{{}}) {
		t.Errorf("empty input: %v", got)
	}
	if got := SplitIntoBatches([]int{1, 2}, 2); !reflect.DeepEqual(got, [][]int{{1, 2}}) {
		t.Errorf("len == batchSize: %v", got)
	}
	b := SplitIntoBatches([]int{1, 2, 3}, 2)
	if len(b) != 2 || cap(b[0]) != 2 {
		t.Fatalf("batches %v, cap %d", b, cap(b[0]))
	}
	_ = append(b[0], 9)
	if b[1][0] != 3 {
		t.Errorf("appending to a batch overwrote the next one: %v", b)
	}
}

// TestGetAllSongFoldersEdges: hidden media is no content, and output folders below the
// song dir are skipped without making their parent a song.
func TestGetAllSongFoldersEdges(t *testing.T) {
	t.Run("hidden media is no content", func(t *testing.T) {
		lib := t.TempDir()
		writeFile(t, filepath.Join(lib, "Cat", "X", "._a.mp3"), "x")
		if err := os.MkdirAll(filepath.Join(lib, "Cat", "Y"), 0o755); err != nil {
			t.Fatal(err)
		}
		got, err := GetAllSongFolders(nil, lib)
		if want := []string{filepath.Join(lib, "Cat")}; err != nil || !slices.Equal(got, want) {
			t.Errorf("GetAllSongFolders = %q, %v; want %q", got, err, want)
		}
	})
	t.Run("nested output folder", func(t *testing.T) {
		lib := t.TempDir()
		writeFile(t, filepath.Join(lib, "Cat", "out", config.OutputMarker), "")
		writeFile(t, filepath.Join(lib, "Cat", "out", "invalid", "B", "song.txt"), songTxt)
		writeFile(t, filepath.Join(lib, "Cat", "A", "song.txt"), songTxt)
		got, err := GetAllSongFolders(nil, lib)
		if want := []string{filepath.Join(lib, "Cat", "A")}; err != nil || !slices.Equal(got, want) {
			t.Errorf("GetAllSongFolders = %q, %v; want %q", got, err, want)
		}
	})
	t.Run("category with only an output folder", func(t *testing.T) {
		lib := t.TempDir()
		writeFile(t, filepath.Join(lib, "Cat", "out", config.OutputMarker), "")
		writeFile(t, filepath.Join(lib, "Cat", "out", "invalid", "B", "song.txt"), songTxt)
		got, err := GetAllSongFolders(nil, lib)
		if err != nil || len(got) != 0 {
			t.Errorf("GetAllSongFolders = %q, %v; want no songs (the output folder must not make Cat a broken song)", got, err)
		}
	})
}

// TestTagsFromFolderNameEmptyKeys: tags whose match keys are empty and qualifiers that
// are the whole folder part fill nothing.
func TestTagsFromFolderNameEmptyKeys(t *testing.T) {
	tests := []struct{ folder, artist, title string }{
		{"Artist - ...", "", "..."},
		{"Artist - ?", "", "?"},
		{"... - Title", "...", ""},
		{"Artist - (Live)", "", "Song"},
		{"Artist - [DUET]", "", "Song"},
	}
	for _, tc := range tests {
		got := TagsFromFolderName(tc.folder, tc.artist, tc.title)
		if got.FillArtist || got.FillTitle || got.Reason == "" {
			t.Errorf("TagsFromFolderName(%q, %q, %q) = %+v; want no fill and a reason", tc.folder, tc.artist, tc.title, got)
		}
	}
}

// TestImageHintsUseCombinedNameAndNFC: a hint word in both the artist and the title is
// allowed as often as "Artist Title" has it, and the words are compared in NFC.
func TestImageHintsUseCombinedNameAndNFC(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "Song")
	writeFile(t, filepath.Join(dir, "song.txt"), "#ARTIST:Cover\n#TITLE:Take Cover\n")
	writeFile(t, filepath.Join(dir, "Cover - Take Cover.png"), string(pngBytes(t, 40, 20)))
	writeFile(t, filepath.Join(dir, "a.png"), string(pngBytes(t, 20, 20)))
	if got := FindCoverImage(nil, dir); got != "a.png" {
		t.Errorf("FindCoverImage = %q, want a.png", got)
	}
	if got := FindBackgroundImage(nil, dir); got != "Cover - Take Cover.png" {
		t.Errorf("FindBackgroundImage = %q, want the wide image", got)
	}

	// NFC title "Découverte Cover", NFD file name with the title and one more "cover".
	names := []string{"Song", "", "Découverte Cover", " Découverte Cover"}
	if kind, _, _ := nameHint("Découverte Cover cover.jpg", names); kind != kindCover {
		t.Errorf("nameHint(NFD title + cover) = %v, want kindCover", kind)
	}
	if kind, _, _ := nameHint("Découverte Cover.jpg", names); kind != kindNone {
		t.Errorf("nameHint(NFD title) = %v, want kindNone", kind)
	}
	// "Covér" is one word only if the combining mark is composed first.
	if got := wordCounts("Covér"); !reflect.DeepEqual(got, map[string]int{"covér": 1}) {
		t.Errorf("wordCounts(NFD) = %v", got)
	}
}

// TestFindImagesReadThePlannedTxt: songNames reads the txt through ops, so a title that
// only the dry-run plan has keeps "Take Cover.png" from counting as a cover hint.
func TestFindImagesReadThePlannedTxt(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "Song")
	txt := filepath.Join(dir, "song.txt")
	writeFile(t, txt, "#ARTIST:Artist\n#MP3:a.mp3\n")
	writeFile(t, filepath.Join(dir, "Artist - Take Cover.png"), string(pngBytes(t, 40, 20)))
	writeFile(t, filepath.Join(dir, "a.png"), string(pngBytes(t, 20, 20)))
	if got := FindCoverImage(nil, dir); got != "Artist - Take Cover.png" {
		t.Fatalf("FindCoverImage(disk) = %q, want the hinted image", got)
	}
	ops := fsops.New(true)
	if _, err := ops.WriteFileAtomic(txt, []byte("#ARTIST:Artist\n#TITLE:Take Cover\n#MP3:a.mp3\n")); err != nil {
		t.Fatal(err)
	}
	if got := FindCoverImage(ops, dir); got != "a.png" {
		t.Errorf("FindCoverImage(plan) = %q, want a.png", got)
	}
	if got := FindBackgroundImage(ops, dir); got != "Artist - Take Cover.png" {
		t.Errorf("FindBackgroundImage(plan) = %q, want the wide image", got)
	}
}

// TestReadImageHeaderBoundaries pins the length guards of the BMP, WebP and ftyp readers
// (one byte short is invalid, never a panic) and the remaining unsupported signatures.
func TestReadImageHeaderBoundaries(t *testing.T) {
	tests := []struct {
		name   string
		data   []byte
		format string
		want   error
	}{
		{"bmp 17 bytes", bmpInfo(40, 5, 5)[:17], "", ErrInvalidImage},
		{"bmp core 21 bytes", bmpCore(3, 3)[:21], "", ErrInvalidImage},
		{"bmp info 25 bytes", bmpInfo(40, 5, 5)[:25], "", ErrInvalidImage},
		{"bmp header size 13", cat(bmpInfo(40, 5, 5)[:14], le32(13), make([]byte, 20)), "", ErrInvalidImage},
		{"webp vp8x 29 bytes", webpVP8X(5, 5)[:29], "", ErrInvalidImage},
		{"webp vp8 29 bytes", webpVP8(5, 5)[:29], "", ErrInvalidImage},
		{"webp vp8l 24 bytes", webpVP8L(5, 5)[:24], "", ErrInvalidImage},
		{"riff 11 bytes", []byte("RIFF\x00\x00\x00\x00WEB"), "", ErrInvalidImage},
		{"webp 19 bytes", []byte("RIFF\x00\x00\x00\x00WEBPVP8 \x00\x00\x00"), "", ErrInvalidImage},
		{"ftyp 11 bytes", []byte("\x00\x00\x00\x18ftyphei"), "", ErrInvalidImage},
		{"ftyp 12 bytes", []byte("\x00\x00\x00\x18ftypheic"), "heif", ErrUnsupportedImage},
		{"no ftyp", []byte("\x00\x00\x00\x18ftypheic"[:4] + "xxxxheic"), "", ErrInvalidImage},
		{"jxl container", []byte("\x00\x00\x00\x0cJXL \r\n\x87\n"), "jxl", ErrUnsupportedImage},
		{"jpeg2000", []byte("\x00\x00\x00\x0cjP  \r\n\x87\n"), "jpeg2000", ErrUnsupportedImage},
		{"qoi", []byte("qoif\x00\x00\x00\x01"), "qoi", ErrUnsupportedImage},
		{"avis", []byte("\x00\x00\x00\x18ftypavis"), "avif", ErrUnsupportedImage},
	}
	for _, brand := range []string{"heix", "hevc", "hevx", "heim", "heis", "mif1", "msf1"} {
		tests = append(tests, struct {
			name   string
			data   []byte
			format string
			want   error
		}{"heif " + brand, []byte("\x00\x00\x00\x18ftyp" + brand), "heif", ErrUnsupportedImage})
	}
	for _, tt := range tests {
		h, err := readImageHeader(strings.NewReader(string(tt.data)))
		if !errors.Is(err, tt.want) || h != (ImageHeader{Format: tt.format}) {
			t.Errorf("%s: readImageHeader = %+v, %v; want format %q and %v", tt.name, h, err, tt.format, tt.want)
		}
	}
}

// failingReader fails with a read error that is not about the content.
type failingReader struct{}

var errRead = errors.New("read failed")

func (failingReader) Read([]byte) (int, error) { return 0, errRead }

func TestReadImageHeaderReadError(t *testing.T) {
	for name, err := range map[string]error{
		"reader": func() error { _, err := readImageHeader(failingReader{}); return err }(),
		"dir":    func() error { _, err := ReadImageHeader(t.TempDir()); return err }(),
	} {
		if err == nil || errors.Is(err, ErrInvalidImage) || errors.Is(err, ErrUnsupportedImage) {
			t.Errorf("%s: err = %v, want a plain read error", name, err)
		}
	}
	if _, err := readImageHeader(failingReader{}); !errors.Is(err, errRead) {
		t.Errorf("err = %v, want errRead", err)
	}
}
