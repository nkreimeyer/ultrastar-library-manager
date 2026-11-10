package utils

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/nkreimeyer/ultrastar-songs-sanitizer/fsops"
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
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := DecodeTxt([]byte(tc.raw))
			if got.Text != tc.want || got.Encoding != tc.encoding || (got.Problem != "") != tc.problem || got.Damaged != tc.damaged {
				t.Errorf("DecodeTxt(%q) = %q, %s, problem=%q, damaged=%v; want %q, %s, problem=%v, damaged=%v",
					tc.raw, got.Text, got.Encoding, got.Problem, got.Damaged, tc.want, tc.encoding, tc.problem, tc.damaged)
			}
			if !tc.damaged && strings.ContainsRune(got.Text, '�') {
				t.Errorf("DecodeTxt(%q) introduced U+FFFD: %q", tc.raw, got.Text)
			}
		})
	}
}

func TestHeaderTags(t *testing.T) {
	tags := HeaderTags("#title: A \n#mp3:a.mp3\n#MP3:b.mp3\n\n#NOCOLON\n: 0 1 2 la\n#AFTER:x\n")
	if tags["TITLE"] != "A" || tags["MP3"] != "a.mp3" {
		t.Errorf("unexpected tags %v", tags)
	}
	if _, ok := tags["AFTER"]; ok {
		t.Errorf("tags after the first note line must be ignored: %v", tags)
	}
}

func TestResolveFileName(t *testing.T) {
	dir := t.TempDir()
	nfd := "Mädchen.mp3"
	for _, n := range []string{"Song.mp3", nfd} {
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
