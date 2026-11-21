package repairtxt

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/nkreimeyer/library-manager/config"
	"github.com/nkreimeyer/library-manager/fsops"
)

// A dot in the folder name is no extension: "Herbert feat. X - Männer" spells the title
// (nameFill) and the lyrics (localLex) as well as "Herbert - Männer" does.
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
