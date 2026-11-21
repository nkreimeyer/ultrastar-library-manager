package repairtxt

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/nkreimeyer/library-manager/config"
	"github.com/nkreimeyer/library-manager/fsops"
)

// A dot in the folder name is no extension: "Herbert feat. X - Männer" spells the title
// (nameFill) and the lyrics (localLex) as well as "Herbert - Männer" does. localLex alone
// repairs the title here too; TestNameFillFolderNameWithDot checks the tag fill.
func TestRunFolderNameWithDotIsEvidence(t *testing.T) {
	base := t.TempDir()
	lib := filepath.Join(base, "lib")
	h := "#ARTIST:A\n#MP3:a.mp3\n#BPM:300\n#LANGUAGE:German\n"
	folders := []string{"Herbert - Männer", "Herbert feat. X - Männer", "Dr. Alban - Männer", "P.!.N.K. - Männer"}
	songs := map[string]string{
		"A/A - Eins": songText("#TITLE:Eins\n"+h, "für dich", "das Glück für mich"),
		"A/A - Zwei": songText("#TITLE:Zwei\n"+h, "nur für dich", "kein Glück"),
	}
	damaged := songText("#TITLE:M"+R+"nner\n"+h, "nur f"+R+"r dich", "die M"+R+"nner")
	for _, f := range folders {
		songs["M/"+f] = damaged
	}
	writeLib(t, lib, songs)
	cfg, err := config.New(lib, filepath.Join(base, "out"), false)
	if err != nil {
		t.Fatal(err)
	}
	if err := Run(cfg, fsops.New(false), Options{}); err != nil {
		t.Fatal(err)
	}
	for _, f := range folders {
		got, _ := os.ReadFile(filepath.Join(lib, "M", f, "song.txt"))
		if !strings.Contains(string(got), "#TITLE:Männer\n") || !strings.Contains(string(got), " Männer\n") {
			t.Errorf("%s: not repaired from the folder name:\n%s", f, got)
		}
	}
}

// A subfolder of the song folder is read uncut too: "Dr. Alban - Männer" is no file "Dr"
// with the extension ". Alban - Männer".
func TestRunSubfolderNameIsEvidence(t *testing.T) {
	base := t.TempDir()
	lib := filepath.Join(base, "lib")
	h := "#ARTIST:A\n#MP3:a.mp3\n#BPM:300\n#LANGUAGE:German\n"
	writeLib(t, lib, map[string]string{
		"A/A - Eins": songText("#TITLE:Eins\n"+h, "für dich", "das Glück für mich"),
		"A/A - Zwei": songText("#TITLE:Zwei\n"+h, "nur für dich", "kein Glück"),
		"M/song":     songText("#TITLE:M"+R+"nner\n"+h, "die M"+R+"nner"),
	})
	if err := os.Mkdir(filepath.Join(lib, "M", "song", "Dr. Alban - Männer"), 0o755); err != nil {
		t.Fatal(err)
	}
	cfg, err := config.New(lib, filepath.Join(base, "out"), false)
	if err != nil {
		t.Fatal(err)
	}
	if err := Run(cfg, fsops.New(false), Options{}); err != nil {
		t.Fatal(err)
	}
	got, _ := os.ReadFile(filepath.Join(lib, "M", "song", "song.txt"))
	if !strings.Contains(string(got), "#TITLE:Männer\n") || !strings.Contains(string(got), " Männer\n") {
		t.Errorf("not repaired from the subfolder name:\n%s", got)
	}
}

// A file tag names a whole file: a longer name that only starts with the value
// ("Cafè.mp3.bak") is another file, neither a fill nor a veto against the real one.
func TestNameFillWholeNameOnly(t *testing.T) {
	value := []rune("Caf" + R + ".mp3")
	if fill, ok := nameFill(value, []string{"Cafè.mp3.bak"}, true); ok {
		t.Errorf("filled %q from a longer name", string(fill))
	}
	if fill, ok := nameFill(value, []string{"Café.mp3", "Cafè.mp3.bak"}, true); !ok || string(fill) != "é" {
		t.Errorf("fill = %q, %v; want é", string(fill), ok)
	}
	// Case and NFC are still ignored.
	if fill, ok := nameFill(value, []string{"CAFE\u0301.MP3"}, true); !ok || string(lowerRunes(fill)) != "é" {
		t.Errorf("fill = %q, %v; want É", string(fill), ok)
	}
	// The substring mode of text tags is unchanged.
	if fill, ok := nameFill([]rune("Caf"+R), []string{"Café.mp3.bak"}, false); !ok || string(fill) != "é" {
		t.Errorf("text fill = %q, %v; want é", string(fill), ok)
	}

	txt := "#TITLE:X\n#ARTIST:Y\n#MP3:Caf" + R + ".mp3\n: 0 2 0 la\nE\n"
	if _, got := planText(t, &planner{lex: corpus("")}, txt, "Café.mp3", "Cafè.mp3.bak"); !strings.Contains(got, "#MP3:Café.mp3\n") {
		t.Errorf("#MP3 not repaired from the existing file:\n%s", got)
	}
}

// The tag fill sees the folder name uncut, the file names without their extension (the
// localLex half of TestRunFolderNameWithDotIsEvidence would repair the title on its own).
func TestNameFillFolderNameWithDot(t *testing.T) {
	in := songInput{folder: "Herbert feat. X - Männer", names: []string{"Männer.mp3", "x.y.jpg"}}
	if got, want := in.nameTexts(false), []string{"Herbert feat. X - Männer", "Männer", "x.y"}; !slices.Equal(got, want) {
		t.Errorf("nameTexts(false) = %q, want %q", got, want)
	}
	if got, want := in.nameTexts(true), []string{"Herbert feat. X - Männer", "Männer.mp3", "x.y.jpg"}; !slices.Equal(got, want) {
		t.Errorf("nameTexts(true) = %q, want %q", got, want)
	}
	// A subfolder (stems, a "Dr. Alban - Männer (Video)" folder) has no extension either.
	withDir := songInput{folder: "M", dirs: []string{"Dr. Alban - Männer"}, names: []string{"a.mp3"}}
	if got, want := withDir.nameTexts(false), []string{"M", "Dr. Alban - Männer", "a"}; !slices.Equal(got, want) {
		t.Errorf("nameTexts(false) = %q, want %q", got, want)
	}
	folderOnly := songInput{folder: "Herbert feat. X - Männer"}
	if fill, ok := nameFill([]rune("M"+R+"nner"), folderOnly.nameTexts(false), false); !ok || string(fill) != "ä" {
		t.Errorf("fill = %q, %v; want ä", string(fill), ok)
	}

	// Only the folder decides: the song's own "Mönner" ties the word-level evidence.
	txt := songText("#TITLE:M"+R+"nner\n#ARTIST:A\n#MP3:a.mp3\n#BPM:300\n", "die Mönner", "la la")
	fp := (&planner{lex: corpus("")}).plan(songInput{raw: []byte(txt), song: mustParse(txt), folder: "Dr. Alban - Männer"})
	if got := string(fp.apply([]byte(txt))); !strings.Contains(got, "#TITLE:Männer\n") {
		t.Errorf("#TITLE not repaired from the folder name:\n%s", got)
	}
}
