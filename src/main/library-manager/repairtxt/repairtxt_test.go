package repairtxt

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/nkreimeyer/library-manager/config"
	"github.com/nkreimeyer/library-manager/fsops"
	"github.com/nkreimeyer/library-manager/songtxt"
)

const R = "\uFFFD"

// songText builds a song txt whose lyrics are the given lines, one note per word.
func songText(header string, lines ...string) string {
	var b strings.Builder
	b.WriteString(header)
	beat := 0
	for i, l := range lines {
		if i > 0 {
			fmt.Fprintf(&b, "- %d\n", beat)
		}
		for j, w := range strings.Fields(l) {
			if j > 0 {
				w = " " + w
			}
			fmt.Fprintf(&b, ": %d 2 0 %s\n", beat, w)
			beat += 4
		}
	}
	b.WriteString("E\n")
	return b.String()
}

// corpus builds a finalized Lexicon from texts of one language.
func corpus(lang string, texts ...string) *Lexicon {
	lx := NewLexicon()
	var langs []string
	if lang != "" {
		langs = []string{lang}
	}
	for _, t := range texts {
		lx.addText(langs, []rune(t))
	}
	lx.finalize()
	return lx
}

func planText(t *testing.T, p *planner, txt string, names ...string) (*filePlan, string) {
	t.Helper()
	song, _ := songtxt.Parse(txt)
	fp := p.plan(songInput{raw: []byte(txt), song: song, names: names})
	return fp, string(fp.apply([]byte(txt)))
}

func TestTokenize(t *testing.T) {
	rs := []rune("I’m 'cause goin’ „Hallo… f" + R + "r\nzwei")
	var got []string
	var outer []string
	var starts []bool
	for _, tk := range tokenize(rs) {
		got = append(got, string(rs[tk.start:tk.end]))
		outer = append(outer, string(rs[tk.outerStart:tk.outerEnd]))
		starts = append(starts, tk.lineStart)
	}
	if want := []string{"I’m", "cause", "goin", "Hallo", "f" + R + "r", "zwei"}; !reflect.DeepEqual(got, want) {
		t.Errorf("tokens = %q, want %q", got, want)
	}
	if want := []string{"I’m", "cause", "goin’", "„Hallo…", "f" + R + "r", "zwei"}; !reflect.DeepEqual(outer, want) {
		t.Errorf("outer forms = %q, want %q", outer, want)
	}
	if want := []bool{true, false, false, false, false, true}; !reflect.DeepEqual(starts, want) {
		t.Errorf("lineStart = %v, want %v", starts, want)
	}
}

func TestLanguageCodes(t *testing.T) {
	for in, want := range map[string][]string{
		"German":           {"de"},
		"Deutsch (german)": {"de"},
		"Englisch":         {"en"},
		"Espa" + R + "ol":  {"es"},
		"Français":         {"fr"},
		"German/English":   {"de", "en"},
		"English, German":  {"en", "de"},
		"Schwedisch":       {"sv"},
		"Other":            nil,
		"":                 nil,
	} {
		if got := LanguageCodes(in); !reflect.DeepEqual(got, want) {
			t.Errorf("LanguageCodes(%q) = %v, want %v", in, got, want)
		}
	}
}

func TestDetectLanguage(t *testing.T) {
	lx := NewLexicon()
	de := []rune(strings.Repeat("ich bin hier und du bist da\n", 3000) + strings.Repeat("hallo welt\n", 50))
	nl := []rune(strings.Repeat("ik ben hier en jij bent daar\n", 1000) + strings.Repeat("ich bin\n", 30))
	lx.addText([]string{"de"}, de)
	lx.addText([]string{"nl"}, nl)
	lx.addText([]string{"tiny"}, []rune("ich bin"))
	lx.finalize()
	words := strings.Fields("ich bin hier und du bist da und ich bin hier")
	if got := lx.detectLanguage(words); got != "de" {
		t.Errorf("detect = %q, want de", got)
	}
	if got := lx.detectLanguage(words, "de"); got != "" {
		t.Errorf("detect with a right tag = %q, want \"\" (nothing to add)", got)
	}
	if got := lx.detectLanguage(words, "nl"); got != "de" {
		t.Errorf("detect with a wrong tag = %q, want de", got)
	}
	// Words neither corpus knows must not favour the smaller one.
	rare := []string{"ich", "bin"}
	for i := range 30 {
		rare = append(rare, fmt.Sprintf("selten%d", i))
	}
	if got := lx.detectLanguage(rare); got != "de" {
		t.Errorf("detect with rare words = %q, want de", got)
	}
	if got := lx.detectLanguage(words[:5]); got != "" {
		t.Errorf("detect on 5 words = %q, want \"\"", got)
	}
}

func TestResolveWord(t *testing.T) {
	de := newLangLex()
	de.add([]occurrence{{form: "für"}, {form: "für"}, {form: "für"}, {form: "fór"}}, nil)
	de.finalize()
	single := newLangLex()
	single.add([]occurrence{{form: "glück"}}, nil)
	single.finalize()
	tie := newLangLex()
	tie.add([]occurrence{{form: "för"}, {form: "för"}, {form: "får"}, {form: "får"}}, nil)
	tie.finalize()
	breaker := newLangLex()
	breaker.add([]occurrence{{form: "för"}, {form: "för"}, {form: "för"}, {form: "för"}, {form: "för"}}, nil)
	breaker.finalize()
	lv := func(name string, l *langLex, min int, ratio float64) level {
		return level{name: name, lexes: []*langLex{l}, min: min, ratio: ratio}
	}
	tests := []struct {
		name   string
		tok    string
		levels []level
		form   string
		source string
		weak   bool
	}{
		{"decided", "f" + R + "r", []level{lv("de", de, 2, 2)}, "für", "de", false},
		{"ratio not reached", "f" + R + "r", []level{lv("de", de, 2, 5)}, "", "", false},
		{"single weak candidate", "gl" + R + "ck", []level{lv("c", single, 2, 5)}, "glück", "weak", true},
		{"tie broken by a later level", "f" + R + "r", []level{lv("sv", tie, 2, 5), lv("all", breaker, 2, 5)}, "för", "all", false},
		{"ascii is never a candidate", "f" + R + "r", []level{lv("de", newLangLex(), 1, 1)}, "", "", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tt.levels[0].lexes[0].finalize()
			r := resolveWord(lowerRunes([]rune(tt.tok)), 0, tt.levels)
			if string(r.form) != tt.form || r.source != tt.source || r.weak != tt.weak {
				t.Errorf("resolveWord(%q) = %q from %q weak=%v, want %q from %q weak=%v", tt.tok, string(r.form), r.source, r.weak, tt.form, tt.source, tt.weak)
			}
		})
	}
	// Only characters a CP1252/CP1250 byte stands for can be the lost one (ē is neither).
	baltic := newLangLex()
	baltic.add([]occurrence{{form: "fēr"}, {form: "fēr"}, {form: "fēr"}}, nil)
	baltic.finalize()
	if r := resolveWord(lowerRunes([]rune("f"+R+"r")), 0, []level{lv("lv", baltic, 1, 1)}); r.form != nil || len(r.cands) != 0 {
		t.Errorf("a character outside the codepages was accepted: %q %v", string(r.form), r.cands)
	}
	// A pool keeps a later level from bringing in a candidate the earlier one ruled out.
	other := newLangLex()
	other.add([]occurrence{{form: "fúr"}, {form: "fúr"}, {form: "fúr"}}, nil)
	other.finalize()
	r := resolveWord(lowerRunes([]rune("f"+R+"r")), 0, []level{lv("sv", tie, 2, 5), lv("x", other, 2, 5)})
	if r.form != nil || len(r.cands) != 2 {
		t.Errorf("pool: got %q, cands %v; want unresolved with för/får", string(r.form), r.cands)
	}
}

func TestRestoreCase(t *testing.T) {
	g := newLangLex()
	g.add([]occurrence{{form: "ärger", upper: true}, {form: "ärger", upper: true}, {form: "über"}}, nil)
	cr := caseRule{startUpper: true, global: g}
	for _, tt := range []struct {
		tok, form string
		lineStart bool
		want      string
	}{
		{"F" + R + "R", "für", false, "Ü"},    // all caps
		{R + "ber", "über", true, "Ü"},        // line start, upper-case convention
		{R + "ber", "über", false, "ü"},       // mid-line, written lower case
		{R + "rger", "ärger", false, "Ä"},     // mid-line noun
		{"sch" + R + "n", "schön", true, "ö"}, // not the first letter
		{"I" + R + "m", "i’m", false, "’"},    // not a letter
		{R + "sland", "ísland", false, "Í"},   // mid-line upper case in the song's lyrics
		{R + "rger", "ärger", false, "ä"},     // the song's own case wins over the corpus
	} {
		cr := cr
		if tt.want == "Í" || tt.want == "ä" {
			l := newLangLex()
			l.add([]occurrence{{form: "ísland", upper: true}, {form: "ärger"}}, nil)
			cr.local = l
		}
		tok := []rune(tt.tok)
		i := strings.Index(string(tok), R)
		i = len([]rune(string(tok)[:i]))
		if got := string(cr.restoreCase(tok, []rune(tt.form), i, tt.lineStart)); got != tt.want {
			t.Errorf("restoreCase(%q, %q) = %q, want %q", tt.tok, tt.form, got, tt.want)
		}
	}
}

func TestSegmentsJoinSyllables(t *testing.T) {
	txt := "\uFEFF#TITLE:Gl" + R + "ck\r\n#MP3:a.mp3\r\n: 0 2 0 f\r\n: 2 2 0 " + R + "r\r\n: 4 2 0 ~\r\n: 6 2 0  dich\r\n- 8\r\n: 10 2 0 zwei\r\nE\r\n"
	song, _ := songtxt.Parse(txt)
	segs := segments([]byte(txt), song)
	if len(segs) != 3 {
		t.Fatalf("got %d segments, want 3 (TITLE, MP3, lyrics)", len(segs))
	}
	if got := string(segs[0].runes); segs[0].tag != "TITLE" || got != "Gl"+R+"ck" {
		t.Errorf("tag segment = %s %q", segs[0].tag, got)
	}
	ly := segs[2]
	if got := string(ly.runes); got != "f"+R+"r dich\nzwei" {
		t.Errorf("lyrics = %q", got)
	}
	// The offsets point at the bytes in the raw text.
	for i, r := range ly.runes {
		if ly.offs[i] < 0 {
			continue
		}
		if got := []rune(txt[ly.offs[i]:])[0]; got != r {
			t.Errorf("rune %d %q: offset points at %q", i, r, got)
		}
	}
	if ly.lines[1] != 4 {
		t.Errorf("line of the U+FFFD = %d, want 4", ly.lines[1])
	}
}

func TestPlanRepairsOnlyTheDamagedBytes(t *testing.T) {
	lx := corpus("de",
		"Ich danke dir für alles\nfür dich und für mich\nGlück und Glück",
		"Das ist für dich\nso schön\nschön war die Zeit, weißt du",
		"Ärger gibt es immer\nkein Ärger\nweißt du noch")
	p := &planner{lex: lx}
	txt := "\uFEFF#TITLE:Gl" + R + "ck\r\n#ARTIST:Band\r\n#LANGUAGE:Deutsch\r\n#MP3:Gl" + R + "ck.mp3\r\n" +
		": 0 2 0 F" + R + "r\r\n: 2 2 0  dich\r\n- 4\r\n: 6 2 0 Kein\r\n: 8 2 0  " + R + "rger\r\n: 10 2 0  wei\r\n: 12 2 0 " + R + "t\r\n: 14 2 0  du\r\nE\r\n"
	fp, got := planText(t, p, txt, "Glück.mp3", "Band - Glück.txt")
	want := strings.NewReplacer("#TITLE:Gl"+R+"ck", "#TITLE:Glück", "#MP3:Gl"+R+"ck", "#MP3:Glück",
		"F"+R+"r", "Für", R+"rger", "Ärger", "wei\r\n: 12 2 0 "+R+"t", "wei\r\n: 12 2 0 ßt").Replace(txt)
	if got != want {
		t.Errorf("repaired =\n%q\nwant\n%q\nleft: %+v", got, want, fp.left)
	}
	if fp.total != 5 || len(fp.repl) != 5 || len(fp.left) != 0 {
		t.Errorf("total %d, repaired %d, left %v", fp.total, len(fp.repl), fp.left)
	}
}

func TestPlanLeavesAmbiguousAndFileNames(t *testing.T) {
	lx := corpus("sv", "för dig\nför mig\nfår jag\nfår du", "för alltid", "får vi")
	p := &planner{lex: lx}
	txt := "#TITLE:X\n#ARTIST:Y\n#LANGUAGE:Swedish\n#MP3:L" + R + "t.mp3\n: 0 2 0 f" + R + "r\nE\n"
	fp, got := planText(t, p, txt, "Lat.mp3", "Lut.mp3")
	if got != txt {
		t.Errorf("an ambiguous word or file name was changed:\n%q", got)
	}
	if len(fp.left) != 2 {
		t.Fatalf("left = %+v, want the #MP3 value and f%sr", fp.left, R)
	}
	if fp.left[1].Word != "f"+R+"r" || len(fp.left[1].Candidates) != 2 {
		t.Errorf("left word = %+v", fp.left[1])
	}
	// A file really named with U+FFFD keeps its reference.
	if fill, ok := nameFill([]rune("L"+R+"t.mp3"), []string{"L" + R + "t.mp3", "Lät.mp3"}, true); ok {
		t.Errorf("nameFill changed a reference to an existing file: %q", string(fill))
	}
}

func TestPlanContextAndElision(t *testing.T) {
	lx := corpus("sv", "jag gör det\nvad gör du\nvi gör allt", "jag går hem\njag går ut", "gör det nu", "går det")
	p := &planner{lex: lx}
	_, got := planText(t, p, songText("#TITLE:X\n#ARTIST:Y\n#LANGUAGE:Swedish\n", "vad g"+R+"r du"))
	if !strings.Contains(got, "gör") {
		t.Errorf("bigram context did not decide gör/går:\n%s", got)
	}
	fr := corpus("fr", "pour être libre\nêtre ou ne pas être", "être là", "c'est la vie")
	p = &planner{lex: fr}
	_, got = planText(t, p, songText("#TITLE:X\n#ARTIST:Y\n#LANGUAGE:French\n", "d'"+R+"tre libre"))
	if !strings.Contains(got, "d'être") {
		t.Errorf("elision not repaired:\n%s", got)
	}
}

func TestPlanEdgeForms(t *testing.T) {
	lx := corpus("en", "I’m goin’ home\nkeep goin’ on\ngoin’ down", "nothin’ to lose", "the tomb", "the tomb is cold")
	p := &planner{lex: lx}
	_, got := planText(t, p, songText("#TITLE:X\n#ARTIST:Y\n#LANGUAGE:English\n", "I"+R+"m goin"+R+" home"))
	if !strings.Contains(got, "I’m") || !strings.Contains(got, "goin’") {
		t.Errorf("apostrophes not restored:\n%s", got)
	}
}

func TestSpell(t *testing.T) {
	texts := []string{"la revolución\nmi corazón\nla canción", "la razón de la nación", "una lección", "la ilusión y la pasión"}
	small := corpus("es", texts...)
	var many []string
	for range 5 {
		many = append(many, texts...)
	}
	song := songText("#TITLE:X\n#ARTIST:Y\n#LANGUAGE:Spanish\n", "la emoci"+R+"n")
	_, got := planText(t, &planner{lex: corpus("es", many...)}, song)
	if !strings.Contains(got, "emoción") {
		t.Errorf("spelling model did not repair emoci%sn:\n%s", R, got)
	}
	// With little evidence it is only a suggestion.
	fp, got := planText(t, &planner{lex: small}, song)
	if got != song || len(fp.left) != 1 || len(fp.left[0].Candidates) != 1 || !strings.HasPrefix(fp.left[0].Candidates[0].Form, "emoción (suggested") {
		t.Errorf("weak spelling: left %+v\n%s", fp.left, got)
	}
}

func planWithRefs(t *testing.T, p *planner, txt string, refs ...string) string {
	t.Helper()
	song, _ := songtxt.Parse(txt)
	fp := p.plan(songInput{raw: []byte(txt), song: song, refs: refs})
	return string(fp.apply([]byte(txt)))
}

func TestPlanLyricsEvidence(t *testing.T) {
	sv := corpus("sv", "för dig\nför mig\nfår jag\nfår du", "för alltid", "får vi")
	p := &planner{lex: sv}
	h := "#TITLE:X\n#ARTIST:Y\n#LANGUAGE:Swedish\n"
	// The corpus and the lyrics know för and får alike; the lyrics' neighbours decide, the
	// previous word ("nu får") as well as the next one ("får jag").
	lyr := "nu får\nfår jag\när för dig"
	for _, line := range []string{"nu f" + R + "r", "f" + R + "r jag"} {
		if got := planWithRefs(t, p, songText(h, line), lyr); !strings.Contains(got, "får") {
			t.Errorf("the lyrics' neighbours did not decide får in %q:\n%s", line, got)
		}
	}
	// A pair only the song knows wins over the corpus's many "får jag".
	many := corpus("sv", "får jag\nfår jag\nfår jag\nfår jag\nfår jag\nfår jag", "för dig", "för mig", "får du")
	got := planWithRefs(t, &planner{lex: many}, songText(h, "f"+R+"r jag tror"), "För jag tror\nfår ett slut")
	if !strings.Contains(got, ": 0 2 0 för\n") {
		t.Errorf("the lyrics' \"för jag\" did not win over the corpus:\n%s", got)
	}
	// If the song has both pairs, its own pairs do not decide alone.
	got = planWithRefs(t, &planner{lex: many}, songText(h, "f"+R+"r jag tror"), "för jag\nför jag\nfår jag")
	if !strings.Contains(got, ": 0 2 0 f"+R+"r\n") {
		t.Errorf("decided although the lyrics have both pairs:\n%s", got)
	}
	// An ASCII apostrophe of the lyrics is the song's own apostrophe.
	fr := &planner{lex: corpus("fr", "la vie", "le temps")}
	h = "#TITLE:X\n#ARTIST:Y\n#LANGUAGE:French\n"
	if got := planWithRefs(t, fr, songText(h, "j"+R+"esp"+R+"re"), "j'espère encore"); !strings.Contains(got, "j’espère") {
		t.Errorf("j%sesp%sre with the lyrics' j'espère:\n%s", R, R, got)
	}
	if got := planWithRefs(t, fr, songText(h, "j"+R+"esp"+R+"re", "c´est la vie"), "j'espère encore"); !strings.Contains(got, "j´espère") {
		t.Errorf("the song's own apostrophe ´ was not used:\n%s", got)
	}
}

func TestLocalSpellAt(t *testing.T) {
	l := newLangLex()
	l.add([]occurrence{{form: "aufzuhören"}, {form: "höher"}}, nil)
	l.finalize()
	pad := func(w string) []rune { return []rune("^" + w + "$") }
	at := func(w string) (rune, bool) {
		p := pad(w)
		return localSpellAt(p, strings.Index(string(p), R) /* ASCII before it */, 5, l)
	}
	if r, ok := at("aufzuh" + R + "r'n"); !ok || r != 'ö' {
		t.Errorf("aufzuh%sr'n = %c, %v; want ö from aufzuhören (fzuhö, zuhör)", R, r, ok)
	}
	if _, ok := at("h" + R + "he"); ok {
		t.Errorf("h%she decided by a single 5-gram (^höhe)", R)
	}
	l.add([]occurrence{{form: "aufzuhär"}}, nil)
	l.finalize()
	if _, ok := at("aufzuh" + R + "r'n"); ok {
		t.Errorf("decided although another letter has support")
	}
}

func TestInvertedMarks(t *testing.T) {
	es := corpus("es", "¿qué pasa?\n¡qué bien!\n¿qué?", "hola")
	p := &planner{lex: es}
	_, got := planText(t, p, songText("#TITLE:X\n#ARTIST:Y\n#LANGUAGE:Spanish\n", R+"qu"+R+" haces?"))
	if !strings.Contains(got, "¿qué") {
		t.Errorf("want ¿qué:\n%s", got)
	}
}

func TestReadWordList(t *testing.T) {
	in := "3\nfür/AB\n# comment\nGlück\n1\tschön\t42\n"
	got, err := readWordList(strings.NewReader(in))
	if err != nil {
		t.Fatal(err)
	}
	if want := []string{"für", "Glück", "schön"}; !reflect.DeepEqual(got, want) {
		t.Errorf("words = %q, want %q", got, want)
	}
	// ISO-8859-1 dictionaries are read as CP1252.
	got, _ = readWordList(strings.NewReader("Gl\xfcck\n"))
	if !reflect.DeepEqual(got, []string{"Glück"}) {
		t.Errorf("latin-1 list = %q", got)
	}
	for in, want := range map[string]WordList{
		"/a/de.dic":    {Path: "/a/de.dic"},
		"de=/a/de.dic": {Lang: "de", Path: "/a/de.dic"},
		"German=x.dic": {Lang: "de", Path: "x.dic"},
	} {
		if got, err := ParseWordListFlag(in); err != nil || got != want {
			t.Errorf("ParseWordListFlag(%q) = %+v, %v; want %+v", in, got, err, want)
		}
	}
	if _, err := ParseWordListFlag("Klingon=x.dic"); err == nil {
		t.Error("unknown language accepted")
	}
}

func TestWordListDecides(t *testing.T) {
	dir := t.TempDir()
	wl := filepath.Join(dir, "de.dic")
	if err := os.WriteFile(wl, []byte("2\nfröhlich\nfröhlichen/A\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	lists, err := loadWordLists([]WordList{{Lang: "de", Path: wl}})
	if err != nil {
		t.Fatal(err)
	}
	p := &planner{lex: corpus("de", "wir sind hier", "und hier"), wordlists: lists}
	_, got := planText(t, p, songText("#TITLE:X\n#ARTIST:Y\n#LANGUAGE:German\n", "die fr"+R+"hlichen"))
	if !strings.Contains(got, "fröhlichen") {
		t.Errorf("word list not used:\n%s", got)
	}
}

type fakeLyrics map[string]string

func (f fakeLyrics) Lyrics(artist, title string) (string, error) { return f[artist+" - "+title], nil }

// writeLib writes song folders: rel -> txt (each with an a.mp3).
func writeLib(t *testing.T, lib string, songs map[string]string) {
	t.Helper()
	for rel, txt := range songs {
		dir := filepath.Join(lib, rel)
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
		for name, c := range map[string]string{"song.txt": txt, "a.mp3": "x"} {
			if err := os.WriteFile(filepath.Join(dir, name), []byte(c), 0o644); err != nil {
				t.Fatal(err)
			}
		}
	}
}

func TestRun(t *testing.T) {
	base := t.TempDir()
	lib := filepath.Join(base, "lib")
	h := "#ARTIST:A\n#MP3:a.mp3\n#BPM:300\n#LANGUAGE:German\n"
	damaged := songText("#TITLE:Weg\n"+h, "f"+R+"r dich", "das Gl"+R+"ck", "s"+R+"rprise")
	writeLib(t, lib, map[string]string{
		"A/A - Eins": songText("#TITLE:Eins\n"+h, "für dich", "das Glück für mich"),
		"A/A - Zwei": songText("#TITLE:Zwei\n"+h, "nur für dich", "kein Glück"),
		"A/A - Weg":  damaged,
	})
	out := filepath.Join(base, "out")
	path := filepath.Join(lib, "A/A - Weg/song.txt")

	// Dry run: the library stays byte-identical, the report is written.
	cfg, err := config.New(lib, out, true)
	if err != nil {
		t.Fatal(err)
	}
	if err := Run(cfg, fsops.New(true), Options{Lyrics: fakeLyrics{"A - Weg": "Sürprise, sürprise"}}); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(path); string(b) != damaged {
		t.Fatal("the dry run changed the txt")
	}
	var rep Report
	data, err := os.ReadFile(filepath.Join(out, ReportJSON))
	if err != nil || json.Unmarshal(data, &rep) != nil {
		t.Fatalf("report: %v", err)
	}
	if len(rep.Files) != 1 || rep.Files[0].Damaged != 3 || rep.Files[0].Fixed != 3 || !rep.Files[0].LyricsUsed || rep.Corpus != 2 {
		t.Fatalf("report = %+v", rep)
	}
	if txt, err := os.ReadFile(filepath.Join(out, ReportTxt)); err != nil || !strings.Contains(string(txt), "A/A - Weg/song.txt") {
		t.Errorf("text report: %v\n%s", err, txt)
	}

	// Real run without lyrics: "s�rprise" stays, the rest is repaired, the original is kept.
	cfg.DryRun = false
	ops := fsops.New(false)
	if err := Run(cfg, ops, Options{}); err != nil {
		t.Fatal(err)
	}
	got, _ := os.ReadFile(path)
	want := strings.NewReplacer("f"+R+"r", "für", "Gl"+R+"ck", "Glück").Replace(damaged)
	if string(got) != want {
		t.Errorf("repaired =\n%s\nwant\n%s", got, want)
	}
	if orig, err := os.ReadFile(filepath.Join(out, "originals", "A/A - Weg/song.txt")); err != nil || string(orig) != damaged {
		t.Errorf("original not kept: %v", err)
	}
	if st := ops.Stats(); st.Written != 1 || st.Copied != 1 || st.Failed != 0 {
		t.Errorf("stats = %+v", st)
	}

	// With lyrics the last word is repaired; after that the txt is no longer damaged.
	if err := Run(cfg, fsops.New(false), Options{Lyrics: fakeLyrics{"A - Weg": "Sürprise!"}}); err != nil {
		t.Fatal(err)
	}
	got, _ = os.ReadFile(path)
	if strings.Contains(string(got), R) || !strings.Contains(string(got), "sürprise") {
		t.Errorf("lyrics not used:\n%s", got)
	}
	ops = fsops.New(false)
	if err := Run(cfg, ops, Options{}); err != nil {
		t.Fatal(err)
	}
	if st := ops.Stats(); st.Written != 0 || st.Copied != 0 {
		t.Errorf("a repaired library was changed again: %+v", st)
	}
	// The repaired txt is no evidence for later runs.
	data, _ = os.ReadFile(filepath.Join(out, ReportJSON))
	rep = Report{}
	if json.Unmarshal(data, &rep) != nil || rep.Corpus != 2 || rep.RepairedBefore != 1 {
		t.Errorf("corpus %d, repaired before %d; want 2 and 1", rep.Corpus, rep.RepairedBefore)
	}
}

func TestRunStrict(t *testing.T) {
	base := t.TempDir()
	lib := filepath.Join(base, "lib")
	h := "#ARTIST:A\n#MP3:a.mp3\n#BPM:300\n"
	damaged := songText("#TITLE:Weg\n"+h, "das Gl"+R+"ck")
	writeLib(t, lib, map[string]string{
		"A/A - Eins": songText("#TITLE:Eins\n"+h, "das Glück"), // seen once: weak
		"A/A - Weg":  damaged,
	})
	cfg, err := config.New(lib, filepath.Join(base, "out"), false)
	if err != nil {
		t.Fatal(err)
	}
	if err := Run(cfg, fsops.New(false), Options{Strict: true}); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(filepath.Join(lib, "A/A - Weg/song.txt")); string(b) != damaged {
		t.Errorf("-strict applied a weak repair:\n%s", b)
	}
	if err := Run(cfg, fsops.New(false), Options{}); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(filepath.Join(lib, "A/A - Weg/song.txt")); !strings.Contains(string(b), "Glück") {
		t.Errorf("weak repair not applied without -strict:\n%s", b)
	}
}

func TestLRCLib(t *testing.T) {
	var requests atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if requests.Add(1) == 1 {
			w.WriteHeader(http.StatusTooManyRequests) // retried
			return
		}
		if r.URL.Path != "/search" || r.URL.Query().Get("q") != "Dr mhus Vill Ha Dig" {
			t.Errorf("request %s?%s", r.URL.Path, r.URL.RawQuery)
		}
		json.NewEncoder(w).Encode([]lrclibRecord{
			{TrackName: "Something else", ArtistName: "Drömhus", PlainLyrics: "no"},
			{TrackName: "Vill ha dig", ArtistName: "dr" + R + "mhus", PlainLyrics: "jag vill ha dig"},
		})
	}))
	defer srv.Close()
	dir := t.TempDir()
	c := NewLRCLib(filepath.Join(dir, "cache"), fsops.New(false))
	c.BaseURL, c.Delay, c.RetryWait = srv.URL, 0, time.Millisecond
	for range 2 {
		got, err := c.Lyrics("Dr"+R+"mhus", "Vill Ha Dig")
		if err != nil || got != "jag vill ha dig" {
			t.Fatalf("Lyrics = %q, %v", got, err)
		}
	}
	if n := requests.Load(); n != 2 {
		t.Errorf("%d requests, want 2 (one retry; the second answer comes from the cache)", n)
	}
}

func TestLRCLibFallbackQueries(t *testing.T) {
	var queries []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query().Get("q")
		queries = append(queries, q)
		if q != "Desole'" {
			w.Write([]byte("[]"))
			return
		}
		json.NewEncoder(w).Encode([]lrclibRecord{
			{TrackName: "Désolé", ArtistName: "Other", PlainLyrics: "no"},
			{TrackName: "Désolé (Radio Edit)", ArtistName: "Sexion d’Assaut", PlainLyrics: "désolé"},
		})
	}))
	defer srv.Close()
	cache := filepath.Join(t.TempDir(), "cache")
	c := NewLRCLib(cache, fsops.New(false))
	c.BaseURL, c.Delay = srv.URL, 0
	// A "not found" entry of the old lookup is looked up again.
	old, _ := json.Marshal(cacheEntry{Artist: "Sexion d' Assaut feat. X", Title: "Desole' (Live)", Found: false})
	if err := os.MkdirAll(cache, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(c.cachePath("Sexion d' Assaut feat. X", "Desole' (Live)"), old, 0o644); err != nil {
		t.Fatal(err)
	}
	got, err := c.Lyrics("Sexion d' Assaut feat. X", "Desole' (Live)")
	if err != nil || got != "désolé" {
		t.Fatalf("Lyrics = %q, %v", got, err)
	}
	want := []string{"Sexion d' Assaut feat. Desole' (Live)", "Sexion d' Assaut Desole'", "Desole'"}
	if !slices.Equal(queries, want) {
		t.Errorf("queries %q, want %q", queries, want)
	}
	// The new "not found" entry is final.
	queries = nil
	if got, _ := c.Lyrics("Nobody", "Nothing"); got != "" || len(queries) != 2 {
		t.Fatalf("Lyrics = %q after %q", got, queries)
	}
	queries = nil
	if got, _ := c.Lyrics("Nobody", "Nothing"); got != "" || len(queries) != 0 {
		t.Errorf("a new not-found entry was looked up again: %q", queries)
	}
}

func TestSearchHelpers(t *testing.T) {
	for in, want := range map[string]string{
		"Herz an Herz (Duett)": "Herz an Herz", "Sexy - Live [VIDEO]": "Sexy - Live", "Loca": "Loca",
	} {
		if got := cleanTitle(in); got != want {
			t.Errorf("cleanTitle(%q) = %q, want %q", in, got, want)
		}
	}
	for in, want := range map[string]string{
		"Shakira feat. Carlinhos Brown": "Shakira", "Goldman, Fredericks, Jones": "Goldman",
		"Lasse Holm och Monica Törnell": "Lasse Holm", "Eros Ramazzotti & Carlos Santana": "Eros Ramazzotti",
		"Trooper Da Don ft. Vanessa": "Trooper Da Don", "Bad Boys Blue": "Bad Boys Blue",
	} {
		if got := firstArtist(in); got != want {
			t.Errorf("firstArtist(%q) = %q, want %q", in, got, want)
		}
	}
	if !titleMatches("Das Schäferlied (Radio Version)", "Das Schaferlied") || titleMatches("Das Lied", "Das Schaferlied") {
		t.Error("titleMatches")
	}
}

func TestPickRecord(t *testing.T) {
	recs := []lrclibRecord{
		{TrackName: "Kom o ta mig", ArtistName: "Other", PlainLyrics: "x"},
		{TrackName: "Kom o ta mig!", ArtistName: "Brandsta City Släckers feat. Y", PlainLyrics: "ja"},
	}
	if got := pickRecord(recs, "Brandsta City Sl"+R+"ckers", "Kom o ta mig"); got != "ja" {
		t.Errorf("pickRecord = %q", got)
	}
	if got := pickRecord(recs, "Nobody", "Kom o ta mig"); got != "" {
		t.Errorf("pickRecord matched the wrong artist: %q", got)
	}
}

func TestRunReviewedWords(t *testing.T) {
	base := t.TempDir()
	lib := filepath.Join(base, "lib")
	h := "#ARTIST:A\n#MP3:a.mp3\n#BPM:300\n#LANGUAGE:German\n"
	damaged := songText("#TITLE:Weg\n"+h, "eine T"+R+"ter"+R+"t"+R+"t"+R, "alte M"+R+R+R+"nner", "alte M"+R+R+R+"nner")
	writeLib(t, lib, map[string]string{
		// The corpus spells it otherwise; the reviewed word still wins.
		"A/A - Eins": songText("#TITLE:Eins\n"+h, "alte Mäöänner", "alte Mäöänner", "alte Mäöänner"),
		"A/A - Weg":  damaged,
	})
	words := filepath.Join(base, "words.txt")
	cfg, err := config.New(lib, filepath.Join(base, "out"), false)
	if err != nil {
		t.Fatal(err)
	}
	// Without the reviewed word the corpus decides.
	fp := (&planner{lex: corpus("de", "alte Mäöänner", "alte Mäöänner", "Mäöänner")}).plan(songInput{raw: []byte(damaged), song: mustParse(damaged)})
	if got := string(fp.apply([]byte(damaged))); !strings.Contains(got, "Mäöänner") {
		t.Fatalf("corpus did not decide:\n%s", got)
	}
	if err := os.WriteFile(words, []byte("Täterätätä\nMäääänner\nMäänner\nMääänner\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := Run(cfg, fsops.New(false), Options{Words: []string{words}}); err != nil {
		t.Fatal(err)
	}
	got, _ := os.ReadFile(filepath.Join(lib, "A/A - Weg/song.txt"))
	want := strings.NewReplacer("T"+R+"ter"+R+"t"+R+"t"+R, "Täterätätä", "M"+R+R+R+"nner", "Määänner").Replace(damaged)
	if string(got) != want {
		t.Errorf("repaired =\n%s\nwant\n%s", got, want)
	}
}

func TestPlanEarlierRepairsAreNoEvidence(t *testing.T) {
	lx := corpus("sv", "för dig\nför mig\nfår jag\nfår du", "för alltid", "får vi")
	p := &planner{lex: lx}
	h := "#TITLE:X\n#ARTIST:Y\n#LANGUAGE:Swedish\n"
	orig := songText(h, "F"+R+"r jag tror", "f"+R+"r ett slut")
	now := songText(h, "F"+R+"r jag tror", "får ett slut") // an earlier run repaired one
	plan := func(own []byte) string {
		fp := p.plan(songInput{raw: []byte(now), song: mustParse(now), own: own})
		return string(fp.apply([]byte(now)))
	}
	if got := plan(nil); !strings.Contains(got, ": 0 2 0 Får\n") {
		t.Fatalf("test premise: the song's own får decides without the original:\n%s", got)
	}
	if got := plan([]byte(orig)); got != now {
		t.Errorf("the earlier repair counted as evidence:\n%s", got)
	}
	// Run reads the oldest copy in originals/.
	base := t.TempDir()
	cfg, err := config.New(base, filepath.Join(t.TempDir(), "out"), false)
	if err != nil {
		t.Fatal(err)
	}
	if oldestOriginal(cfg, "A/x.txt") != nil {
		t.Error("an original without a copy")
	}
	dir := filepath.Join(cfg.OriginalsDir(), "A")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	os.WriteFile(filepath.Join(dir, "x.txt"), []byte(orig), 0o644)
	os.WriteFile(filepath.Join(dir, "x (2).txt"), []byte(now), 0o644)
	if got := oldestOriginal(cfg, "A/x.txt"); string(got) != orig {
		t.Errorf("oldestOriginal = %q", got)
	}
}

func mustParse(txt string) *songtxt.Song {
	s, _ := songtxt.Parse(txt)
	return s
}

func TestPlanHyphenatedSyllables(t *testing.T) {
	lx := corpus("de", "das ist lächerlich\nso lächerlich", "der jüngste Tag\nder jüngste", "lächerlich und jüngste")
	p := &planner{lex: lx}
	song := "#TITLE:X\n#ARTIST:Y\n#LANGUAGE:German\n: 0 2 0 Der\n: 2 2 0  j" + R + "ng\n: 4 2 0  -\n: 6 2 0  ste\n- 8\n: 10 2 0 l" + R + "\n: 12 2 0  - cher\n: 14 2 0  - lich\nE\n"
	fp, got := planText(t, p, song)
	if !strings.Contains(got, " jüng\n") || !strings.Contains(got, " lä\n") || len(fp.left) != 0 {
		t.Errorf("hyphenated words not repaired (left %+v):\n%s", fp.left, got)
	}
	// The spelling model is not asked across hyphens: "Angst" makes "j�ngste" look like "ä".
	angst := corpus("de", strings.Repeat("die angst ängste ängstlich\n", 30))
	if _, got := planText(t, &planner{lex: angst}, song); strings.Contains(got, "jäng") {
		t.Errorf("spelling model used across a hyphen:\n%s", got)
	}
	// A hyphen that joins two words ("lo-intent�") finds no joined word and changes nothing.
	same := "#TITLE:X\n#ARTIST:Y\n#LANGUAGE:German\n: 0 2 0 und-sch" + R + "nf\nE\n"
	if _, got := planText(t, p, same); got != same {
		t.Errorf("changed:\n%s", got)
	}
}

// A chain that starts with a word repaired on its own still joins its later syllables:
// skipping a window that holds "Über" must not skip the other windows of that size.
func TestPlanHyphenatedAfterRepairedWord(t *testing.T) {
	lx := corpus("de", strings.Repeat("über alles\n", 5), strings.Repeat("so lächerlich\n", 3))
	song := "#TITLE:X\n#ARTIST:Y\n#LANGUAGE:German\n: 0 2 0 " + R + "ber\n: 2 2 0  - l" + R + "\n: 4 2 0  - cher\n: 6 2 0  - lich\nE\n"
	fp, got := planText(t, &planner{lex: lx}, song)
	if !strings.Contains(got, " Über\n") || !strings.Contains(got, " - lä\n") || len(fp.left) != 0 {
		t.Errorf("hyphen chain after a repaired word not joined (left %+v):\n%s", fp.left, got)
	}
}

func TestReadReviewed(t *testing.T) {
	in := "\uFEFF# comment\nTäterätätä Mäh\n\n[Patrick Lindner - Mandeln und Zimt]\nfährt\n[ Party Animals - Der Betze Ruft ]\nfährt schwört\n[*]\nSäcken 12\n"
	global, secs, err := readReviewed(strings.NewReader(in))
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(global, []string{"Täterätätä", "Mäh", "Säcken"}) {
		t.Errorf("global = %q", global)
	}
	if len(secs) != 2 || secs[0].name != "Patrick Lindner - Mandeln und Zimt" || !slices.Equal(secs[1].words, []string{"fährt", "schwört"}) {
		t.Errorf("sections = %+v", secs)
	}
	if _, _, err := readReviewed(strings.NewReader("[ - ]\nx\n")); err == nil {
		t.Error("a section naming no song was accepted")
	}
}

func TestRunSongWords(t *testing.T) {
	base := t.TempDir()
	lib := filepath.Join(base, "lib")
	h := "#MP3:a.mp3\n#BPM:300\n#LANGUAGE:German\n"
	writeLib(t, lib, map[string]string{
		"B/BVB - Leuchte auf":         songText("#ARTIST:BVB\n#TITLE:Leuchte auf\n"+h, "wohin er uns auch f"+R+"hrt"),
		"P/Patrick Lindner - Mandeln": songText("#ARTIST:Patrick Lindner\n#TITLE:Mandeln und Zimt\n"+h, "der Zug f"+R+"hrt ein"),
		"E/Eisregen - Pest":           songText("#ARTIST:Eisregen\n#TITLE:Pest\n"+h, "f"+R+"hrt niemand mehr"),
		// The corpus knows both, so without a reviewed word all three stay.
		"A/A - Eins": songText("#ARTIST:A\n#TITLE:Eins\n"+h, "er führt uns", "er fährt heim", "sie führt", "sie fährt"),
	})
	words := filepath.Join(base, "words.txt")
	// BVB is named by its folder, Patrick Lindner by its tags (its folder name is cut short).
	content := "führt\n[BVB - Leuchte auf]\nführt\n[Patrick Lindner - Mandeln und Zimt]\nfährt\n[Nobody - Nothing]\nx\n"
	if err := os.WriteFile(words, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	out := filepath.Join(base, "out")
	cfg, err := config.New(lib, out, false)
	if err != nil {
		t.Fatal(err)
	}
	if err := Run(cfg, fsops.New(false), Options{Words: []string{words}}); err != nil {
		t.Fatal(err)
	}
	read := func(rel string) string {
		b, _ := os.ReadFile(filepath.Join(lib, rel, "song.txt"))
		return string(b)
	}
	if got := read("P/Patrick Lindner - Mandeln"); !strings.Contains(got, "fährt") {
		t.Errorf("the song's section did not win over the global führt:\n%s", got)
	}
	if got := read("B/BVB - Leuchte auf"); !strings.Contains(got, "führt") {
		t.Errorf("BVB:\n%s", got)
	}
	if got := read("E/Eisregen - Pest"); !strings.Contains(got, "führt") {
		t.Errorf("the global word did not apply to a song without a section:\n%s", got)
	}
	var rep Report
	data, _ := os.ReadFile(cfg.ReportPath(ReportJSON))
	if err := json.Unmarshal(data, &rep); err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(rep.UnmatchedSections, []string{"Nobody - Nothing"}) {
		t.Errorf("unmatched = %q", rep.UnmatchedSections)
	}
	for _, f := range rep.Files {
		if strings.HasPrefix(f.Path, "P/") && (len(f.Fixes) != 1 || f.Fixes[0].Source != srcReviewedSong) {
			t.Errorf("fixes of %s = %+v", f.Path, f.Fixes)
		}
	}
}

func TestSongWordsMatchFolderWithFFFD(t *testing.T) {
	rw := &reviewedWords{songs: []*songSection{{name: "Ted Gärdestad - Sol, vind och vatten", key: matchKey("Ted Gärdestad - Sol, vind och vatten"), words: []string{"för"}}}}
	if rw.forSong("T/Ted G"+R+"rdestad - Sol, vind och vatten/x.txt", "", "") == nil {
		t.Error("folder name with U+FFFD not matched")
	}
	if rw.forSong("T/Ted Gardestad - Sol/x.txt", "Ted Gardestad", "Sol, vind och vatten") == nil {
		t.Error("tags without accents not matched")
	}
	if rw.forSong("T/Other/x.txt", "Ted", "Sol") != nil {
		t.Error("another song matched")
	}
	if u := rw.unmatched(); len(u) != 0 {
		t.Errorf("unmatched = %q", u)
	}
}

func TestLoadReviewedMergesSections(t *testing.T) {
	dir := t.TempDir()
	a, b := filepath.Join(dir, "a.txt"), filepath.Join(dir, "b.txt")
	os.WriteFile(a, []byte("Mäh\n[Ted Gärdestad - Sol]\nför\n"), 0o644)
	os.WriteFile(b, []byte("[ted gardestad - sol]\nfår\n"), 0o644)
	rw, err := loadReviewed([]string{a, b})
	if err != nil {
		t.Fatal(err)
	}
	if len(rw.songs) != 1 || !slices.Equal(rw.songs[0].words, []string{"för", "får"}) || rw.global.count("mäh") != 1 {
		t.Errorf("songs = %+v", rw.songs)
	}
	if l := rw.forSong("T/x/y.txt", "Ted Gärdestad", "Sol"); l == nil || l.count("får") != 1 || l.count("för") != 1 {
		t.Error("merged section not used")
	}
}

// A repeated #LANGUAGE resolves to its last occurrence, the one UltraStar uses.
func TestLanguagesRepeatedTag(t *testing.T) {
	p := &planner{lex: corpus("de", "ich bin hier")}
	song, _ := songtxt.Parse("#TITLE:X\n#ARTIST:Y\n#LANGUAGE:English\n#LANGUAGE:German\n: 0 1 0 a\n")
	if codes, how := p.languages(song, nil); !slices.Equal(codes, []string{"de"}) || how != "tag" {
		t.Errorf("languages = %v (%s), want [de] from the last #LANGUAGE", codes, how)
	}
}
