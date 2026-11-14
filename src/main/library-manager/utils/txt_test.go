package utils

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/nkreimeyer/library-manager/fsops"
)

func TestDecodeTxt(t *testing.T) {
	tests := []struct {
		name     string
		raw      string
		want     string
		encoding string
		problem  bool
		damaged  bool
	}{
		{"utf8", "#TITLE:Mädchen\n", "#TITLE:Mädchen\n", EncUTF8, false, false},
		{"latin1 umlaut", "#TITLE:M\xe4dchen\n", "#TITLE:Mädchen\n", EncCP1252, false, false},
		{"cp1252 quotes", "#TITLE:\x84Hallo\x93\n", "#TITLE:„Hallo“\n", EncCP1252, false, false},
		{"undefined cp1252 byte is a problem", "#TITLE:M\xe4d\x81\n", "#TITLE:Mäd\u0081\n", EncLatin1, true, false},
		{"mixed utf8 and latin1 is a problem", "#TITLE:M\xc3\xa4dchen \xe4\n", "#TITLE:MÃ¤dchen ä\n", EncCP1252, true, false},
		{"crlf", "#TITLE:A\r\n#ARTIST:B\r\n", "#TITLE:A\n#ARTIST:B\n", EncUTF8, false, false},
		{"cr only", "#TITLE:A\r#ARTIST:B\r", "#TITLE:A\n#ARTIST:B\n", EncUTF8, false, false},
		{"bom stripped", "\xef\xbb\xbf#TITLE:A\n", "#TITLE:A\n", EncUTF8, false, false},
		{"bom with latin1 body", "\xef\xbb\xbf#TITLE:\xe4\n", "#TITLE:ä\n", EncCP1252, false, false},
		{"literal replacement char is damaged", "#TITLE:M�dchen\n", "#TITLE:M�dchen\n", EncUTF8, false, true},
		{"utf16le with bom", "\xff\xfe#\x00A\x00:\x00\xe4\x00\n\x00", "#A:ä\n", EncUTF16LE, false, false},
		{"utf16be with bom", "\xfe\xff\x00#\x00A\x00:\x00\xe4\x00\n", "#A:ä\n", EncUTF16BE, false, false},
		{"utf16 odd length is a problem", "\xff\xfe#\x00A", "#�", EncUTF16LE, true, false},
		{"utf16 unpaired surrogate is a problem", "\xff\xfe#\x00\x00\xd8A\x00", "#�A", EncUTF16LE, true, false},
		{"cp1252 sharp s before ellipsis is not mojibake", "#TITLE:Gru\xdf\x85\n", "#TITLE:Gruß…\n", EncCP1252, false, false},
		{"cp1250 hint", "#ENCODING:CP1250\n#TITLE:\xb9\n", "#ENCODING:CP1250\n#TITLE:ą\n", "CP1250", false, false},
		{"stale cp1252 hint on converted utf8", "#ENCODING:CP1252\n#TITLE:Mädchen „x“\n", "#ENCODING:CP1252\n#TITLE:Mädchen „x“\n", EncUTF8, false, false},
		{"cp1252 hint on non-western utf8 is a problem", "#ENCODING:CP1252\n#TITLE:Привет\n", "#ENCODING:CP1252\n#TITLE:Привет\n", EncUTF8, true, false},
		{"utf8 without hint stays utf8", "#TITLE:Привет\n", "#TITLE:Привет\n", EncUTF8, false, false},
		{"cp1251 hint", "#ENCODING:CP1251\n#TITLE:\xca\xe0\xeb\xe8\xed\xea\xe0\n", "#ENCODING:CP1251\n#TITLE:Калинка\n", "CP1251", false, false},
		{"iso-8859-2 hint", "#ENCODING:ISO-8859-2\n#TITLE:Za\xbf\xf3\xb3\xe6\n", "#ENCODING:ISO-8859-2\n#TITLE:Zażółć\n", "ISO-8859-2", false, false},
		{"unknown codec is a problem", "#ENCODING:EBCDIC\n#TITLE:\xe4\n", "#ENCODING:EBCDIC\n#TITLE:ä\n", EncLatin1, true, false},
		{"cp1252 mixed with utf8 cyrillic is a problem", "#TITLE:Sch\xf6ne Gr\xfc\xdfe \xd0\x9a\xd0\xb0\xd0\xbb\xd0\xb8\n", "#TITLE:Schöne Grüße ÐšÐ°Ð»Ð¸\n", EncCP1252, true, false},
		{"cp1252 mixed with utf8 greek is a problem", "#TITLE:\xe4 \xce\x93\xce\xb5\xce\xb9\xce\xb1\n", "#TITLE:ä Î“ÎµÎ¹Î±\n", EncCP1252, true, false},
		{"accidental pair next to utf8 cyrillic is a problem", "#TITLE:GRO\xdf\x85 \xd0\x9a\xd0\xb0\n", "#TITLE:GRO\u07c5 Ка\n", EncUTF8, true, false},
		{"cp1250 by language tag", "#LANGUAGE:Polish\n#TITLE:Za\xbf\xf3\xb3\xe6 g\xea\x9cl\xb9\n", "#LANGUAGE:Polish\n#TITLE:Zażółć gęślą\n", EncCP1250, false, false},
		{"cp1250 without tags is a problem", "#TITLE:Za\xbf\xf3\xb3\xe6\n", "#TITLE:Za¿ó³æ\n", EncCP1252, true, false},
		{"french guillemets with nbsp are cp1252", "#TITLE:\xe9t\xe9 aim\xe9\xa0\xbb\n", "#TITLE:été aimé\u00a0»\n", EncCP1252, false, false},
		{"only an isolated accidental sequence is a problem", "#TITLE:aim\xe9\xa0\xbb\n", "#TITLE:aim頻\n", EncUTF8, true, false},
		{"stale cp1252 tag with bullet is utf8", "#ENCODING:CP1252\n#TITLE:a \u2022 b \u2030\n", "#ENCODING:CP1252\n#TITLE:a • b ‰\n", EncUTF8, false, false},
		{"cp1252 tag with accidental latin extended is a problem", "#ENCODING:CP1252\n#TITLE:H\xc4\x85\n", "#ENCODING:CP1252\n#TITLE:Hą\n", EncUTF8, true, false},
		{"latin1 hint is read as cp1252", "#ENCODING:ISO-8859-1\n#TITLE:\x84Hi\x93 \x85\n", "#ENCODING:ISO-8859-1\n#TITLE:„Hi“ …\n", EncCP1252, false, false},
		{"latin9 hint with c1 bytes is a problem", "#ENCODING:ISO-8859-15\n#TITLE:\xa4 \x85\n", "#ENCODING:ISO-8859-15\n#TITLE:¤ \u0085\n", EncLatin1, true, false}, // 0x80-0x9F are undefined in ISO-8859-15
		{"latin9 hint", "#ENCODING:ISO-8859-15\n#TITLE:\xa4\n", "#ENCODING:ISO-8859-15\n#TITLE:€\n", "ISO-8859-15", false, false},
		{"utf8 punctuation in cp1252 file is mixed", "#TITLE:M\xe4dchen \xe2\x80\x99s\n", "#TITLE:Mädchen â€™s\n", EncCP1252, true, false},
		{"utf8 latin extended in cp1252 file is mixed", "#TITLE:\xe4 \xc5\x82\n", "#TITLE:ä Å‚\n", EncCP1252, true, false},
		{"accidental pair next to real utf8 is a problem", "#TITLE:Gru\xdf\x85 M\xc3\xa4dchen\n", "#TITLE:Gru\u07c5 Mädchen\n", EncUTF8, true, false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := DecodeTxt([]byte(tc.raw))
			if got.Text != tc.want || got.Encoding != tc.encoding || (got.Problem != "") != tc.problem || got.Damaged != tc.damaged {
				t.Errorf("DecodeTxt(%q) = %q, %s, problem=%q, damaged=%v; want %q, %s, problem=%v, damaged=%v",
					tc.raw, got.Text, got.Encoding, got.Problem, got.Damaged, tc.want, tc.encoding, tc.problem, tc.damaged)
			}
			if !tc.damaged && !tc.problem && strings.ContainsRune(got.Text, '�') {
				t.Errorf("DecodeTxt(%q) introduced U+FFFD: %q", tc.raw, got.Text)
			}
		})
	}
}

func TestHeaderTags(t *testing.T) {
	tags := HeaderTags("#title: A \n#mp3:a.mp3\n#MP3:b.mp3\n\n#NOCOLON\n: 0 1 2 la\n#AFTER:x\n")
	if tags["TITLE"] != "A" || tags["MP3"] != "b.mp3" { // the last occurrence wins
		t.Errorf("unexpected tags %v", tags)
	}
	if _, ok := tags["AFTER"]; ok {
		t.Errorf("tags after the first note line must be ignored: %v", tags)
	}
}

func TestResolveFileName(t *testing.T) {
	dir := t.TempDir()
	nfd := "Mädchen.mp3"
	for _, n := range []string{"Song.mp3", nfd, "._Song.mp3"} {
		if err := os.WriteFile(filepath.Join(dir, n), nil, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	tests := []struct {
		name, want   string
		exact, found bool
	}{
		{"Song.mp3", "Song.mp3", true, true},
		{"song.MP3", "Song.mp3", false, true},
		{"Mädchen.mp3", nfd, false, true},
		{"missing.mp3", "", false, false},
		{"._Song.mp3", "", false, false}, // hidden files are never resolved
		{"", "", false, false},
	}
	for _, tc := range tests {
		got, exact, found := ResolveFileName(dir, tc.name)
		if got != tc.want || exact != tc.exact || found != tc.found {
			t.Errorf("ResolveFileName(%q) = %q, %v, %v; want %q, %v, %v", tc.name, got, exact, found, tc.want, tc.exact, tc.found)
		}
	}
}

func TestReplaceFileReference(t *testing.T) {
	write := func(t *testing.T, content string) (string, string) {
		dir := t.TempDir()
		txt := filepath.Join(dir, "song.txt")
		if err := os.WriteFile(txt, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
		return dir, txt
	}
	read := func(t *testing.T, path string) string {
		b, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		return string(b)
	}

	t.Run("only file tag values are replaced", func(t *testing.T) {
		dir, txt := write(t, "#TITLE:video.avi live\r\n#VIDEO:VIDEO.AVI\r\n#VIDEOGAP:1\r\n: 0 1 2 video.avi\r\n")
		replaced, err := ReplaceFileReference(fsops.New(false), dir, "video.avi", "video.mp4")
		if err != nil || !replaced {
			t.Fatalf("replaced=%v err=%v", replaced, err)
		}
		want := "#TITLE:video.avi live\r\n#VIDEO:video.mp4\r\n#VIDEOGAP:1\r\n: 0 1 2 video.avi\r\n"
		if got := read(t, txt); got != want {
			t.Errorf("got %q, want %q", got, want)
		}
	})
	t.Run("no reference means no write", func(t *testing.T) {
		dir, txt := write(t, "#TITLE:A\n#VIDEO:other.mp4\n")
		before, _ := os.Stat(txt)
		replaced, err := ReplaceFileReference(fsops.New(false), dir, "video.avi", "video.mp4")
		after, _ := os.Stat(txt)
		if err != nil || replaced || !after.ModTime().Equal(before.ModTime()) {
			t.Errorf("replaced=%v err=%v, file rewritten=%v", replaced, err, !after.ModTime().Equal(before.ModTime()))
		}
	})
	t.Run("non utf8 txt is refused", func(t *testing.T) {
		dir, _ := write(t, "#TITLE:M\xe4dchen\n#VIDEO:video.avi\n")
		if _, err := ReplaceFileReference(fsops.New(false), dir, "video.avi", "video.mp4"); err == nil {
			t.Error("expected an error for a CP1252 txt")
		}
	})
}

func TestSongTxtFilesReportsReadErrors(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root can read everything")
	}
	dir := t.TempDir()
	txt := filepath.Join(dir, "song.txt")
	if err := os.WriteFile(txt, []byte("#TITLE:A\n"), 0); err != nil {
		t.Fatal(err)
	}
	if _, err := SongTxtFiles(nil, dir); err == nil {
		t.Error("an unreadable txt must be an error, not 'no song txt'")
	}
}

// TestTxtLookupsSeeDryRunPlan: with a dry-run Ops, the song txt lookups and reads see the
// library as the earlier modules planned it: planned txt content counts, and a txt planned
// to be moved away is neither a song txt of its folder nor a reason to skip the folder as
// unreadable. Without the plan (nil Ops) the disk content counts.
func TestTxtLookupsSeeDryRunPlan(t *testing.T) {
	lib := t.TempDir()
	dir := filepath.Join(lib, "A - Song")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	keep, dup, readme := filepath.Join(dir, "song.txt"), filepath.Join(dir, "a-copy.txt"), filepath.Join(dir, "readme.txt")
	for path, content := range map[string]string{
		keep:                           "#TITLE:Song\n#MP3:old.mp3\n",
		dup:                            "#TITLE:Song\n#MP3:dup.mp3\n",
		readme:                         "just a readme\n",
		filepath.Join(dir, "song.mp3"): "audio",
	} {
		if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	// a-copy.txt is the newest song txt.
	old := time.Now().Add(-time.Hour)
	if err := os.Chtimes(keep, old, old); err != nil {
		t.Fatal(err)
	}

	ops := fsops.New(true)
	if _, err := ops.MoveInto(dup, filepath.Join(t.TempDir(), "extra"), "a-copy.txt"); err != nil {
		t.Fatal(err)
	}
	if _, err := ops.WriteFileAtomic(keep, []byte("#TITLE:Song\n#MP3:song.mp3\n")); err != nil {
		t.Fatal(err)
	}
	// A readme the plan turns into a song txt (no module does this; it shows the read path).
	if _, err := ops.WriteFileAtomic(readme, []byte("#TITLE:Readme\n")); err != nil {
		t.Fatal(err)
	}

	if got, err := SongTxtFiles(ops, dir); err != nil || !slices.Equal(got, []string{readme, keep}) {
		t.Errorf("SongTxtFiles(plan) = %q, %v; want %q", got, err, []string{readme, keep})
	}
	if got, err := SongTxtFiles(nil, dir); err != nil || !slices.Equal(got, []string{dup, keep}) {
		t.Errorf("SongTxtFiles(disk) = %q, %v", got, err)
	}
	if got, err := FindSongTxt(nil, dir); err != nil || got != dup {
		t.Errorf("FindSongTxt(disk) = %q, %v; want %q", got, err, dup)
	}
	if !IsSongTxt(ops, readme) || IsSongTxt(nil, readme) {
		t.Error("IsSongTxt does not follow the plan")
	}

	// Without the readme rewrite, the lookups pick song.txt with its planned content.
	ops2 := fsops.New(true)
	if _, err := ops2.MoveInto(dup, filepath.Join(t.TempDir(), "extra"), "a-copy.txt"); err != nil {
		t.Fatal(err)
	}
	if _, err := ops2.WriteFileAtomic(keep, []byte("#TITLE:Song\n#MP3:song.mp3\n")); err != nil {
		t.Fatal(err)
	}
	if got, err := FindSongTxt(ops2, dir); err != nil || got != keep {
		t.Errorf("FindSongTxt(plan) = %q, %v; want %q", got, err, keep)
	}
	if tags, err := ReadSongTags(ops2, dir); err != nil || tags["MP3"] != "song.mp3" {
		t.Errorf("ReadSongTags(plan) = %v, %v", tags, err)
	}
	if !IsFileReferenced(ops2, dir, "song.mp3") || IsFileReferenced(nil, dir, "song.mp3") {
		t.Error("IsFileReferenced does not follow the plan")
	}
	if replaced, err := ReplaceFileReference(ops2, dir, "song.mp3", "new.mp3"); err != nil || !replaced {
		t.Errorf("ReplaceFileReference(plan) = %v, %v", replaced, err)
	}
	if data, _ := ops2.ReadFile(keep); string(data) != "#TITLE:Song\n#MP3:new.mp3\n" {
		t.Errorf("planned txt after ReplaceFileReference = %q", data)
	}

	// Discovery still finds the song: the planned-away txt is not "unreadable".
	if got, err := GetAllSongFolders(ops2, lib); err != nil || !slices.Equal(got, []string{dir}) {
		t.Errorf("GetAllSongFolders(plan) = %q, %v; want %q", got, err, []string{dir})
	}
	if st := ops2.Stats(); st.Failed != 0 {
		t.Errorf("%d failures", st.Failed)
	}
	if data, _ := os.ReadFile(keep); string(data) != "#TITLE:Song\n#MP3:old.mp3\n" {
		t.Errorf("dry run changed song.txt: %q", data)
	}
}
