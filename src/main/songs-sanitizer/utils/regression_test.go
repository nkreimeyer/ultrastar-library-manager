package utils

import (
	"bytes"
	"encoding/hex"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"golang.org/x/text/encoding/charmap"

	"github.com/nkreimeyer/ultrastar-songs-sanitizer/fsops"
)

// Regression tests for bugs found in the 2026-11 audit of the utils package.

// A CR-only txt (classic Mac line endings) is one "line" for a split on "\n": the
// #ENCODING and #LANGUAGE hints must still be found.
func TestDecodeTxtCROnlyHonoursEncodingAndLanguage(t *testing.T) {
	raw, _ := charmap.Windows1251.NewEncoder().String("#TITLE:Привет\r#ENCODING:CP1251\r#MP3:a.mp3\r")
	d := DecodeTxt([]byte(raw))
	if d.Encoding != "CP1251" || d.Problem != "" || d.Text != "#TITLE:Привет\n#ENCODING:CP1251\n#MP3:a.mp3\n" {
		t.Errorf("CP1251 CR-only: got Encoding=%q Problem=%q Text=%q", d.Encoding, d.Problem, d.Text)
	}
	raw2, _ := charmap.Windows1250.NewEncoder().String("#TITLE:Přišel\r#LANGUAGE:Czech\r#MP3:a.mp3\r")
	d2 := DecodeTxt([]byte(raw2))
	if d2.Encoding != EncCP1250 || d2.Problem != "" || d2.Text != "#TITLE:Přišel\n#LANGUAGE:Czech\n#MP3:a.mp3\n" {
		t.Errorf("CP1250 CR-only: got Encoding=%q Problem=%q Text=%q", d2.Encoding, d2.Problem, d2.Text)
	}
	// CRLF works as well.
	raw3, _ := charmap.Windows1251.NewEncoder().String("#TITLE:Привет\r\n#ENCODING:CP1251\r\n")
	if d3 := DecodeTxt([]byte(raw3)); d3.Encoding != "CP1251" || d3.Text != "#TITLE:Привет\n#ENCODING:CP1251\n" {
		t.Errorf("CRLF: %q %q", d3.Encoding, d3.Text)
	}
	if got := HeaderTags("#TITLE:a\r#ARTIST:b\r: 0 1 2 x\r#AFTER:y\r"); got["TITLE"] != "a" || got["ARTIST"] != "b" || got["AFTER"] != "" {
		t.Errorf("HeaderTags on CR-only text = %v", got)
	}
}

// #LANGUAGE values in a CP1250 file are CP1250 bytes: native names with diacritics
// ("Čeština", "Română") must match too.
func TestDecodeTxtNativeCentralEuropeanLanguage(t *testing.T) {
	cases := []struct{ lang, title, want string }{
		{"Čeština", "Přišel jsem", "Přišel"},
		{"Română", "Ţară şi ţară", "Ţară"},
		{"Slovenčina", "Čo už", "Čo už"},
		{"Slovenščina", "Čas teče", "Čas teče"},
		{"Rumänisch", "Ţară şi ţară", "Ţară"},
		{"ČEŠTINA", "Přišel jsem", "Přišel"},
	}
	for _, c := range cases {
		raw, err := charmap.Windows1250.NewEncoder().String("#LANGUAGE:" + c.lang + "\n#TITLE:" + c.title + "\n")
		if err != nil {
			t.Fatal(err)
		}
		d := DecodeTxt([]byte(raw))
		if d.Problem != "" || d.Encoding != EncCP1250 || !strings.Contains(d.Text, c.want) || !strings.Contains(d.Text, c.lang) {
			t.Errorf("%s: decoded as %s (problem %q): %q", c.lang, d.Encoding, d.Problem, d.Text)
		}
	}
	// A UTF-8 language name in an otherwise CP1250 file still counts.
	raw, _ := charmap.Windows1250.NewEncoder().String("\n#TITLE:Přišel jsem\n")
	raw = "#LANGUAGE:Čeština" + raw
	if d := DecodeTxt([]byte(raw)); d.Encoding != EncCP1250 || !strings.Contains(d.Text, "Přišel") {
		t.Errorf("UTF-8 language name: %s %q", d.Encoding, d.Text)
	}
	// Control: an English name and a non-Central-European native name.
	raw, _ = charmap.Windows1250.NewEncoder().String("#LANGUAGE:Czech\n#TITLE:Přišel jsem\n")
	if d := DecodeTxt([]byte(raw)); d.Encoding != EncCP1250 || !strings.Contains(d.Text, "Přišel") {
		t.Errorf("control failed: %s %q", d.Encoding, d.Text)
	}
	if d := DecodeTxt([]byte("#LANGUAGE:Fran\xe7ais\n#TITLE:\xe9t\xe9\n")); d.Encoding != EncCP1252 || d.Text != "#LANGUAGE:Français\n#TITLE:été\n" {
		t.Errorf("French: %s %q", d.Encoding, d.Text)
	}
}

// Discovery accepts a txt that is a symlink to a regular file, so the txt lookups must
// accept it too.
func TestFindSongTxtAcceptsSymlinkedTxtLikeDiscovery(t *testing.T) {
	lib := t.TempDir()
	store := t.TempDir()
	real := filepath.Join(store, "real.txt")
	writeFile(t, real, songTxt)
	dir := filepath.Join(lib, "A - B")
	writeFile(t, filepath.Join(dir, "song.mp3"), "x")
	if err := os.Symlink(real, filepath.Join(dir, "song.txt")); err != nil {
		t.Skipf("symlinks not supported: %v", err)
	}
	// Neither a dangling link nor a link to a directory is a txt.
	if err := os.Symlink(filepath.Join(store, "missing.txt"), filepath.Join(dir, "dangling.txt")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(store, filepath.Join(dir, "dir.txt")); err != nil {
		t.Fatal(err)
	}
	folders, err := GetAllSongFolders(nil, lib)
	if err != nil || len(folders) != 1 || folders[0] != dir {
		t.Fatalf("GetAllSongFolders = %v, %v; want [%s]", folders, err, dir)
	}
	if got, err := ListTxtFiles(dir); err != nil || len(got) != 1 || got[0] != filepath.Join(dir, "song.txt") {
		t.Errorf("ListTxtFiles = %q, %v", got, err)
	}
	got, err := FindSongTxt(nil, dir)
	if err != nil || got != filepath.Join(dir, "song.txt") {
		t.Errorf("FindSongTxt = %q, %v; want %q", got, err, filepath.Join(dir, "song.txt"))
	}
}

// The BOM must be stripped in the unsupported-#ENCODING branch, or the header is lost.
func TestDecodeTxtUnknownEncodingStripsUTF8BOM(t *testing.T) {
	raw := []byte("\xef\xbb\xbf#ENCODING:EBCDIC\n#TITLE:x\n#MP3:a.mp3\n")
	d := DecodeTxt(raw)
	if d.Problem == "" {
		t.Errorf("expected a Problem for an unsupported codec")
	}
	if d.Text != "#ENCODING:EBCDIC\n#TITLE:x\n#MP3:a.mp3\n" {
		t.Errorf("Text = %q", d.Text)
	}
	path := filepath.Join(t.TempDir(), "song.txt")
	if err := os.WriteFile(path, raw, 0o644); err != nil {
		t.Fatal(err)
	}
	if !IsSongTxt(nil, path) {
		t.Errorf("IsSongTxt = false for a BOM-prefixed txt with an unsupported #ENCODING")
	}
}

// Repeated tags resolve like UltraStar, check and fixtxt do: the last one wins.
func TestHeaderTagsRepeatedTagLastWins(t *testing.T) {
	tags := HeaderTags("#MP3:song.mp3\n#mp3:clip.mp4\n#TITLE:x\n: 0 1 2 la\n#MP3:after.mp3\n")
	if tags["MP3"] != "clip.mp4" {
		t.Errorf("HeaderTags MP3 = %q, want %q (last occurrence in the header)", tags["MP3"], "clip.mp4")
	}
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "song.txt"), "#TITLE:a\n#ARTIST:x\n#TITLE:b\n")
	if tags, err := ReadSongTags(nil, dir); err != nil || tags["TITLE"] != "b" {
		t.Errorf("ReadSongTags = %v, %v; want TITLE b", tags, err)
	}
}

// Correctly tagged Cyrillic and Greek files have adjacent non-ASCII bytes everywhere;
// accidental UTF-8 sequences among them are no evidence of mixed encodings.
func TestDecodeTxtTaggedCyrillicGreekNotMixed(t *testing.T) {
	tests := []struct {
		name string
		cm   *charmap.Charmap
		enc  string
		text string
	}{
		{"cp1251 yo", charmap.Windows1251, "CP1251", "#ENCODING:CP1251\n#TITLE:Чёрный кот\n"},
		{"cp1251 guillemets", charmap.Windows1251, "CP1251", "#ENCODING:CP1251\n#TITLE:«ПРИВЕТ»\n"},
		{"cp1251 lyric syllable", charmap.Windows1251, "CP1251", "#ENCODING:CP1251\n#TITLE:Песня\n: 0 1 2 Тёмная\nE\n"},
		{"cp1251 isolated pair", charmap.Windows1251, "CP1251", "#ENCODING:CP1251\n#TITLE:Её песня\n"},
		{"cp1253 guillemets", charmap.Windows1253, "CP1253", "#ENCODING:CP1253\n#TITLE:«ΑΓΑΠΗ»\n"},
		{"koi8-r yo", charmap.KOI8R, "KOI8-R", "#ENCODING:KOI8-R\n#TITLE:её ёлка\n"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			raw, err := tc.cm.NewEncoder().String(tc.text)
			if err != nil {
				t.Fatal(err)
			}
			d := DecodeTxt([]byte(raw))
			if d.Text != tc.text || d.Encoding != tc.enc || d.Problem != "" {
				t.Errorf("DecodeTxt = %q, %s, problem=%q; want %q, %s, no problem", d.Text, d.Encoding, d.Problem, tc.text, tc.enc)
			}
		})
	}
	// Real UTF-8 mixed into a tagged file is still a Problem.
	mixed := []struct{ name, raw string }{
		{"utf8 cyrillic word", "#ENCODING:CP1251\n#TITLE:\xca\xe0\xeb \xd0\x9f\xd1\x80\xd0\xb8\n"},
		{"utf8 apostrophe", "#ENCODING:CP1251\n#TITLE:\xca\xe0\xeb \xe2\x80\x99s\n"},
		{"utf8 emoji", "#ENCODING:CP1251\n#TITLE:\xca\xe0\xeb \xf0\x9f\x98\x80\n"},
		{"latin tag keeps isolated mojibake", "#ENCODING:CP1252\n#TITLE:M\xc3\xa4dchen \xe4\n"},
		{"latin tag mojibake next to cp1252 letter", "#ENCODING:CP1250\n#TITLE:\xe4M\xc3\xa4\n"},
	}
	for _, tc := range mixed {
		if d := DecodeTxt([]byte(tc.raw)); d.Problem == "" {
			t.Errorf("%s: DecodeTxt(%q) = %q, %s without a Problem", tc.name, tc.raw, d.Text, d.Encoding)
		}
	}
}

// ReplaceFileReference must accept indented tag lines like HeaderTags does.
func TestReplaceFileReferenceIndentedTag(t *testing.T) {
	dir := t.TempDir()
	txt := filepath.Join(dir, "song.txt")
	content := "\ufeff #TITLE:x\n  #VIDEO:clip.avi\n\t#MP3:clip.avi\n: 0 1 2 x\n"
	writeFile(t, txt, content)
	if got := HeaderTags(DecodeTxt([]byte(content)).Text)["VIDEO"]; got != "clip.avi" {
		t.Fatalf("HeaderTags VIDEO = %q", got)
	}
	replaced, err := ReplaceFileReference(fsops.New(false), dir, "clip.avi", "clip.mp4")
	if err != nil {
		t.Fatal(err)
	}
	got, _ := os.ReadFile(txt)
	want := "\ufeff #TITLE:x\n  #VIDEO:clip.mp4\n\t#MP3:clip.mp4\n: 0 1 2 x\n"
	if !replaced || string(got) != want {
		t.Errorf("replaced=%v, file=%q, want %q", replaced, got, want)
	}
}

// UTF-16 without BOM (and any other NUL bytes) must be a Problem, and the header of a
// BOM-less UTF-16 file must still be readable.
func TestDecodeTxtBOMlessUTF16IsProblem(t *testing.T) {
	tests := []struct {
		name, raw, text, enc string
	}{
		{"ascii LE", "#\x00T\x00I\x00T\x00L\x00E\x00:\x00x\x00\n\x00", "#TITLE:x\n", EncUTF16LE},
		{"umlaut LE", "#\x00T\x00I\x00T\x00L\x00E\x00:\x00\xe4\x00\n\x00", "#TITLE:ä\n", EncUTF16LE},
		{"ascii BE", "\x00#\x00T\x00I\x00T\x00L\x00E\x00:\x00x\x00\n", "#TITLE:x\n", EncUTF16BE},
		{"cr lf LE", "#\x00A\x00:\x00x\x00\r\x00\n\x00", "#A:x\n", EncUTF16LE},
		// Other NUL bytes are a Problem, but the text is read as without them, so that tag
		// values (stem names, image hints) still match.
		{"single NUL", "#TITLE:x\x00y\n", "#TITLE:x\x00y\n", EncUTF8},
		{"utf-8 padded with NUL", "#MP3:Beyonc\xc3\xa9.mp3\n\x00", "#MP3:Beyoncé.mp3\n\x00", EncUTF8},
		{"utf-8 bom and NUL", "\xef\xbb\xbf#MP3:\xc3\xa9\x00\n", "#MP3:é\x00\n", EncUTF8},
		{"single NUL in cp1252", "#TITLE:\xe4\x92\x00\n", "#TITLE:ä’\x00\n", EncCP1252},
		{"NUL and undefined cp1252 byte", "#TITLE:\xe4\x81\x00\n", "#TITLE:ä\u0081\x00\n", EncLatin1},
	}
	for _, tc := range tests {
		d := DecodeTxt([]byte(tc.raw))
		if d.Problem == "" || d.Text != tc.text || d.Encoding != tc.enc {
			t.Errorf("%s: DecodeTxt = %q, %s, problem=%q; want %q, %s with a Problem", tc.name, d.Text, d.Encoding, d.Problem, tc.text, tc.enc)
		}
	}
	path := filepath.Join(t.TempDir(), "song.txt")
	writeFile(t, path, tests[0].raw)
	if !IsSongTxt(nil, path) {
		t.Error("a BOM-less UTF-16 song txt is not recognised as a song txt")
	}
}

// arithJPEGHex is a 300x300 red JPEG converted with `jpegtran -arithmetic` (SOF9). It is a
// valid image (ImageMagick/libjpeg-turbo read it) that image/jpeg cannot decode.
const arithJPEGHex = "ffd8ffe000104a46494600010100000100010000ffdb004300030202020202030202020303030304060404040404080606050609080a0a090809090a0c0f0c0a0b0e0b09090d110d0e0f101011100a0c12131210130f101010ffdb00430103030304030408040408100b090b1010101010101010101010101010101010101010101010101010101010101010101010101010101010101010101010101010ffc9001108012c012c03011100021101031101ffcc000a0010100501101105ffda000c03010002110311003f00ff00d9491c5412a27feac5d84fffd9"

func TestReadImageHeaderArithmeticJPEGNotInvalid(t *testing.T) {
	b, err := hex.DecodeString(arithJPEGHex)
	if err != nil {
		t.Fatal(err)
	}
	h, err := readImageHeader(bytes.NewReader(b))
	if errors.Is(err, ErrInvalidImage) || !errors.Is(err, ErrUnsupportedImage) || h.Format != "jpeg" {
		t.Fatalf("got %+v, %v; want ErrUnsupportedImage with Format jpeg", h, err)
	}

	// A hinted arithmetic JPEG is still chosen; without a hint (shape unknown) it is not.
	dir := filepath.Join(t.TempDir(), "Song")
	writeFile(t, filepath.Join(dir, "song.txt"), "#ARTIST:A\n#TITLE:T\n")
	writeFile(t, filepath.Join(dir, "cover.jpg"), string(b))
	writeFile(t, filepath.Join(dir, "scan.jpg"), string(b))
	if got := FindCoverImage(nil, dir); got != "cover.jpg" {
		t.Errorf("FindCoverImage = %q, want cover.jpg", got)
	}
	if got := FindBackgroundImage(nil, dir); got != "" {
		t.Errorf("FindBackgroundImage = %q, want none", got)
	}
}
