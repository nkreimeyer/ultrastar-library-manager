package export

import (
	"bytes"
	"context"
	"encoding/csv"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/nkreimeyer/library-manager/fsops"
	"github.com/nkreimeyer/library-manager/internal/testutil"
	"github.com/nkreimeyer/library-manager/songtxt"
)

func parse(t *testing.T, txt string) *songtxt.Song {
	t.Helper()
	s, _ := songtxt.Parse(txt)
	return s
}

func TestFromTxt(t *testing.T) {
	// 300 BPM = 1200 beats/min: beat 600 is at 30 s; GAP 2000 ms is added.
	s := parse(t, "#ARTIST: Queen \n#TITLE:Bohemian Rhapsody\n#YEAR:1975\n#GENRE:Rock\n#BPM:300,5\n#GAP:2000\n#LANGUAGE:English\n: 0 4 0 a\n: 596 4 0 b\nE\n")
	got := FromTxt("/lib/Q/Queen - Bohemian Rhapsody", s)
	want := Song{Folder: got.Folder, Artist: "Queen", Title: "Bohemian Rhapsody", Year: "1975", Genre: "Rock",
		BPM: "300.5", Language: "English", LengthSec: 2 + 30, HasLength: true}
	// 600 beats at 300.5 BPM = 600*60/(300.5*4) = 29.95 s, +2 s = 31.95 s -> 32 s.
	if got != want {
		t.Errorf("FromTxt = %+v, want %+v", got, want)
	}
}

func TestFromTxtMissingData(t *testing.T) {
	// No notes, no BPM, no tags besides the title: empty cells, the artist from the folder.
	got := FromTxt("/lib/ABBA - Waterloo", parse(t, "#TITLE:Waterloo\n"))
	if got.Artist != "ABBA" || got.Title != "Waterloo" || got.HasLength || got.BPM != "" || got.Duet {
		t.Errorf("FromTxt = %+v", got)
	}
	// A BPM that is no number stays as written; a BPM of 0 gives no length.
	got = FromTxt("/lib/x", parse(t, "#ARTIST:A\n#TITLE:T\n#BPM:fast\n: 0 4 0 a\nE\n"))
	if got.BPM != "fast" || got.HasLength {
		t.Errorf("FromTxt(bpm fast) = %+v", got)
	}
}

func TestFromTxtDuet(t *testing.T) {
	duet := parse(t, "#ARTIST:A\n#TITLE:T\n#BPM:300\nP1\n: 0 4 0 a\nP2\n: 8 4 0 b\nE\n")
	if !FromTxt("/lib/x", duet).Duet {
		t.Error("two singers: want Duet")
	}
	solo := parse(t, "#ARTIST:A\n#TITLE:T\n#BPM:300\nP1\n: 0 4 0 a\n: 8 4 0 b\nE\n")
	if FromTxt("/lib/x", solo).Duet {
		t.Error("one singer: want no Duet")
	}
}

// A field with a comma, a double quote or a line break is quoted, quotes are doubled, and a
// reader gets the original values back.
func TestWriteCSVQuoting(t *testing.T) {
	songs := []Song{
		{Artist: "Crosby, Stills & Nash", Title: `He said "Hi"`, Year: "1969", BPM: "120", LengthSec: 185, HasLength: true, Duet: true},
		{Artist: "AC/DC", Title: "Back In Black", Genre: "Hard Rock, Blues"},
	}
	var buf bytes.Buffer
	if err := WriteCSV(&buf, songs, false); err != nil {
		t.Fatal(err)
	}
	want := "Artist,Title,Year,Genre,BPM,Language,Length (s),Duet\n" +
		`"Crosby, Stills & Nash","He said ""Hi""",1969,,120,,185,yes` + "\n" +
		`AC/DC,Back In Black,,"Hard Rock, Blues",,,,no` + "\n"
	if buf.String() != want {
		t.Errorf("CSV =\n%s\nwant\n%s", buf.String(), want)
	}
	rows, err := csv.NewReader(&buf).ReadAll()
	if err != nil {
		t.Fatal(err)
	}
	if rows[1][0] != "Crosby, Stills & Nash" || rows[1][1] != `He said "Hi"` || rows[2][3] != "Hard Rock, Blues" {
		t.Errorf("a reader got %q", rows)
	}
}

func TestWriteCSVBOM(t *testing.T) {
	var with, without bytes.Buffer
	_ = WriteCSV(&with, nil, true)
	_ = WriteCSV(&without, nil, false)
	if !strings.HasPrefix(with.String(), "\xEF\xBB\xBFArtist,") || strings.HasPrefix(without.String(), "\xEF") {
		t.Errorf("BOM: with %q, without %q", with.String(), without.String())
	}
}

func writeSong(t *testing.T, lib, rel, artist, title string) string {
	t.Helper()
	dir := filepath.Join(lib, rel)
	txt := "#ARTIST:" + artist + "\n#TITLE:" + title + "\n#YEAR:2000\n" + testutil.NotesTxt(title)
	testutil.WriteFile(t, filepath.Join(dir, "song.txt"), txt)
	return dir
}

func TestRunSortsAndSkipsNonSongs(t *testing.T) {
	lib := t.TempDir()
	writeSong(t, lib, "Z/z", "Zed", "Last")
	writeSong(t, lib, "A/b", "ähm", "Second")
	writeSong(t, lib, "A/a", "Ähm", "First")
	// A folder with only media (broken song) is no row; it is not an unreadable folder either.
	testutil.WriteFile(t, filepath.Join(lib, "broken", "x.mp3"), "x")
	out := filepath.Join(t.TempDir(), "sub", FileName)
	ops := fsops.New(true)
	n, err := Run(ops, lib, out, true, Options{})
	if err != nil || n != 3 {
		t.Fatalf("Run = %d, %v", n, err)
	}
	if ops.Stats().Failed != 0 {
		t.Errorf("failed operations: %d", ops.Stats().Failed)
	}
	data, err := os.ReadFile(out)
	if err != nil {
		t.Fatal(err)
	}
	rows, err := csv.NewReader(bytes.NewReader(bytes.TrimPrefix(data, []byte(bom)))).ReadAll()
	if err != nil {
		t.Fatal(err)
	}
	// Case and accents are ignored when sorting; "Ähm"/"First" comes before "ähm"/"Second".
	var order []string
	for _, r := range rows[1:] {
		order = append(order, r[1])
	}
	if got := strings.Join(order, ","); got != "First,Second,Last" {
		t.Errorf("order = %s", got)
	}
	// 10 notes of 2 beats at 300 BPM, the last ends at beat 38 -> 38*60/(300*4) = 1.9 s.
	if r := rows[1]; r[2] != "2000" || r[4] != "300" || r[6] != "2" || r[7] != "no" {
		t.Errorf("row = %q", r)
	}
}

func TestRunLeavesLibraryAlone(t *testing.T) {
	lib := t.TempDir()
	writeSong(t, lib, "A/a", "A", "One")
	before := testutil.Snapshot(t, lib)
	if _, err := Run(fsops.New(true), lib, filepath.Join(t.TempDir(), FileName), false, Options{}); err != nil {
		t.Fatal(err)
	}
	if testutil.Snapshot(t, lib) != before {
		t.Error("the library changed")
	}
}

func TestRunStopped(t *testing.T) {
	lib := t.TempDir()
	writeSong(t, lib, "A/a", "A", "One")
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	out := filepath.Join(t.TempDir(), FileName)
	if _, err := Run(fsops.New(true), lib, out, false, Options{Ctx: ctx}); err == nil {
		t.Error("a stopped run returned no error")
	}
	if _, err := os.Stat(out); err == nil {
		t.Error("a stopped run wrote the file")
	}
}
