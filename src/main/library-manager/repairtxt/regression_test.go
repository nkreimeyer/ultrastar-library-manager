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
