package organize

import (
	"fmt"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/nkreimeyer/library-manager/config"
	"github.com/nkreimeyer/library-manager/internal/testutil"
	"github.com/nkreimeyer/library-manager/songtxt"
)

// chart builds a song txt: header, then the body lines.
func chart(header string, body ...string) string {
	return header + strings.Join(body, "\n") + "\nE\n"
}

// tenNotes are the note lines of a minimal chart: 10 notes "la0".."la9".
func tenNotes(format string) []string {
	var lines []string
	for i := range 10 {
		lines = append(lines, fmt.Sprintf(format, i*4, i))
	}
	return lines
}

func hashOf(t *testing.T, txt string) string {
	t.Helper()
	s, _ := songtxt.Parse(txt)
	return ContentHash(s)
}

func TestContentHash(t *testing.T) {
	base := chart("#ARTIST:ABBA\n#TITLE:SOS\n#BPM:300\n#GAP:1000\n", tenNotes(": %d 2 5 la%d")...)
	want := hashOf(t, base)
	if len(want) != 64 {
		t.Fatalf("hash = %q, want a sha256 hex digest", want)
	}

	shuffled := tenNotes(": %d 2 5 la%d")
	shuffled[0], shuffled[9] = shuffled[9], shuffled[0]
	withBreaks := tenNotes(": %d 2 5 la%d")
	withBreaks = append(withBreaks[:5], append([]string{"- 18"}, withBreaks[5:]...)...)
	var relative []string // the same beats in RELATIVE mode: a break at 20 moves the offset
	for i := range 10 {
		beat := i * 4
		if i == 5 {
			relative = append(relative, "- 20 20")
		}
		if i >= 5 {
			beat -= 20
		}
		relative = append(relative, fmt.Sprintf(": %d 2 5 la%d", beat, i))
	}
	same := map[string]string{
		"other tags, #GAP and comma BPM": chart("#ARTIST:Abba\n#TITLE:S.O.S.\n#BPM:300,0\n#GAP:250\n#VIDEO:x.mp4\n", tenNotes(": %d 2 5 la%d")...),
		"notes in another order":         chart("#BPM:300\n", shuffled...),
		"line breaks":                    chart("#BPM:300\n", withBreaks...),
		"RELATIVE mode":                  chart("#BPM:300\n#RELATIVE:yes\n", relative...),
		"a single P1 marker":             chart("#BPM:300\n", append([]string{"P1"}, tenNotes(": %d 2 5 la%d")...)...),
		"leading blanks, CRLF":           strings.ReplaceAll(chart("#BPM:300\n", tenNotes("  : %d 2 5 la%d")...), "\n", "\r\n"),
		"the last #BPM counts":           chart("#BPM:120\n#BPM:300\n", tenNotes(": %d 2 5 la%d")...),
	}
	for name, txt := range same {
		if got := hashOf(t, txt); got != want {
			t.Errorf("%s: hash differs", name)
		}
	}

	duet := append([]string{"P1"}, tenNotes(": %d 2 5 la%d")...)
	duet = append(duet, "P2", ": 40 2 5 x")
	different := map[string]string{
		"a syllable":        chart("#BPM:300\n", tenNotes(": %d 2 5 lo%d")...),
		"syllable spacing":  chart("#BPM:300\n", tenNotes(": %d 2 5  la%d")...),
		"the pitch":         chart("#BPM:300\n", tenNotes(": %d 2 6 la%d")...),
		"the length":        chart("#BPM:300\n", tenNotes(": %d 3 5 la%d")...),
		"the start beats":   chart("#BPM:300\n", tenNotes(": %d1 2 5 la%d")...),
		"the note type":     chart("#BPM:300\n", tenNotes("* %d 2 5 la%d")...),
		"the BPM":           chart("#BPM:301\n", tenNotes(": %d 2 5 la%d")...),
		"a BPM change":      chart("#BPM:300\n", append([]string{"B 8 200"}, tenNotes(": %d 2 5 la%d")...)...),
		"a second singer":   chart("#BPM:300\n", duet...),
		"an additional one": chart("#BPM:300\n", append(tenNotes(": %d 2 5 la%d"), ": 40 2 5 x")...),
	}
	for name, txt := range different {
		if got := hashOf(t, txt); got == want || got == "" {
			t.Errorf("%s: hash = %q, want another one", name, got)
		}
	}

	none := map[string]string{
		"9 notes":     chart("#BPM:300\n", tenNotes(": %d 2 5 la%d")[:9]...),
		"no #BPM":     chart("#ARTIST:A\n", tenNotes(": %d 2 5 la%d")...),
		"#BPM 0":      chart("#BPM:0\n", tenNotes(": %d 2 5 la%d")...),
		"invalid BPM": chart("#BPM:fast\n", tenNotes(": %d 2 5 la%d")...),
	}
	for name, txt := range none {
		if got := hashOf(t, txt); got != "" {
			t.Errorf("%s: hash = %q, want none", name, got)
		}
	}
	if ContentHash(nil) != "" {
		t.Error("nil song has a hash")
	}
}

// Duet singers are kept apart: P1/P2 swapped is another chart, while the order of the
// sections does not matter.
func TestContentHashDuetSections(t *testing.T) {
	p1, p2 := tenNotes(": %d 2 5 la%d"), tenNotes(": %d 2 7 ba%d")
	sec := func(a, b string, x, y []string) string {
		body := append(append([]string{a}, x...), b)
		return chart("#BPM:300\n", append(body, y...)...)
	}
	h := hashOf(t, sec("P1", "P2", p1, p2))
	if got := hashOf(t, sec("P2", "P1", p2, p1)); got != h {
		t.Error("section order changes the hash")
	}
	if got := hashOf(t, sec("P1", "P2", p2, p1)); got == h {
		t.Error("swapped singers give the same hash")
	}
}

// The hash does not depend on the order of notes that differ in a single field, nor on
// the order of BPM changes, nor on the Unicode normalization of the lyrics.
func TestContentHashOrderAndNFC(t *testing.T) {
	notes := tenNotes(": %d 2 5 la%d")
	pairs := map[string][2]string{
		"start":  {": 50 2 5 x", ": 60 2 5 x"},
		"length": {": 50 2 5 x", ": 50 3 5 x"},
		"pitch":  {": 50 2 5 x", ": 50 2 6 x"},
		"type":   {": 50 2 5 x", "* 50 2 5 x"},
		"text":   {": 50 2 5 x", ": 50 2 5 y"},
		"bpm":    {"B 8 200", "B 16 250"},
	}
	for name, p := range pairs {
		a := hashOf(t, chart("#BPM:300\n", append(append([]string{}, notes...), p[0], p[1])...))
		b := hashOf(t, chart("#BPM:300\n", append(append([]string{}, notes...), p[1], p[0])...))
		if a != b || a == "" {
			t.Errorf("%s: the order changes the hash", name)
		}
	}
	// Both singers sing the same notes: only the player tells them apart.
	duet := func(first, second string) string {
		body := append(append([]string{first}, notes...), second)
		return chart("#BPM:300\n", append(body, notes...)...)
	}
	if hashOf(t, duet("P1", "P2")) != hashOf(t, duet("P2", "P1")) {
		t.Error("player: the order changes the hash")
	}
	nfc := chart("#BPM:300\n", tenNotes(": %d 2 5 M\u00e4d%d")...)
	nfd := chart("#BPM:300\n", tenNotes(": %d 2 5 Ma\u0308d%d")...)
	if hashOf(t, nfc) != hashOf(t, nfd) {
		t.Error("NFD lyrics change the hash")
	}
}

func withHash(c Candidate, h string) Candidate { c.ContentHash = h; return c }

// MakePlan joins songs with the same content hash into one song, whatever their tags.
func TestMakePlanContentHash(t *testing.T) {
	good := Quality{HasVideo: true, VideoHeight: 1080, AudioBitrate: 320000, HasCover: true}
	poor := Quality{AudioBitrate: 128000}
	joined := func(reason string) string { return reason + joinedByContent }
	duetTitle := func(c Candidate) Candidate { c.Duet = true; return c }
	tests := []struct {
		name    string
		inPlace bool
		cands   []Candidate
		want    []string
	}{
		{"other tags, same notes: one song, the keeper's tags name it", false, []Candidate{
			withHash(song(hq, "abba", "Abba Gold", "Track 3", poor), "h1"),
			withHash(song(low1, "ABBA - SOS", "ABBA", "SOS", good), "h1"),
			withHash(song(low1, "Other", "ABBA", "Mamma Mia", poor), "h2"),
		}, []string{
			"hq:abba: duplicate <low1:ABBA - SOS> (" + joined("no video") + ")",
			"low1:ABBA - SOS: library A/ABBA - SOS",
			"low1:Other: library A/ABBA - Mamma Mia",
		}},
		{"joined through a version with the same tags", false, []Candidate{
			withHash(song(hq, "a", "ABBA", "SOS", good), "h1"),
			withHash(song(low1, "b", "ABBA", "SOS", poor), "h2"),
			withHash(song(low2, "c", "Typo", "S0S", poor), "h2"),
		}, []string{
			"hq:a: library A/ABBA - SOS",
			"low1:b: duplicate <hq:a> (no video)",
			"low2:c: duplicate <hq:a> (" + joined("no video") + ")",
		}},
		{"no hash: tags only", false, []Candidate{
			song(hq, "a", "ABBA", "SOS", good),
			song(low1, "b", "Cher", "Believe", poor),
		}, []string{
			"hq:a: library A/ABBA - SOS",
			"low1:b: library C/Cher - Believe",
		}},
		{"a duet title never joins the solo version", false, []Candidate{
			withHash(song(hq, "a", "ABBA", "SOS", good), "h1"),
			duetTitle(withHash(song(low1, "b", "ABBA", "SOS (Duet)", poor), "h1")),
			duetTitle(withHash(song(low1, "c", "ABBA", "SOS [Duet]", poor), "h1")),
		}, []string{
			"hq:a: library A/ABBA - SOS",
			"low1:b: library A/ABBA - SOS (Duet)",
			"low1:c: duplicate <low1:b> (same quality; the version with the first path is kept)",
		}},
		{"a broken song names the working version with the same notes", false, []Candidate{
			withHash(brokenSong(hq, "Broken", "Unknown", "Track 1", "missing-file"), "h1"),
			withHash(song(low1, "ABBA - SOS", "ABBA", "SOS", poor), "h1"),
		}, []string{
			"hq:Broken: invalid <low1:ABBA - SOS> (errors: missing-file)",
			"low1:ABBA - SOS: library A/ABBA - SOS",
		}},
		{"dedupe: library songs with the same notes", true, []Candidate{
			withHash(libFirst("Pop/Abba - S.O.S", "Abba Gold", "Track 3", poor), "h1"),
			withHash(libFirst("Rock/ABBA - SOS", "ABBA", "SOS", good), "h1"),
			withHash(brokenLib("Misc/sos", "?", "?", "missing-file"), "h1"),
		}, []string{
			"library:Pop/Abba - S.O.S: duplicate <library:Rock/ABBA - SOS> (" + joined("no video") + ")",
			"library:Rock/ABBA - SOS: in-place Rock/ABBA - SOS",
			"library:Misc/sos: invalid <library:Rock/ABBA - SOS> (errors: missing-file)",
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := summary(tt.cands, MakePlan(tt.cands, PlanOptions{LibraryInPlace: tt.inPlace}))
			if !reflect.DeepEqual(got, tt.want) {
				t.Errorf("plan:\n%s\nwant:\n%s", strings.Join(got, "\n"), strings.Join(tt.want, "\n"))
			}
		})
	}
}

func TestUnionFind(t *testing.T) {
	u := unionFind{}
	u.union("c", "d")
	u.union("b", "d")
	u.union("x", "y")
	u.union("d", "c") // already joined
	for k, want := range map[string]string{"b": "b", "c": "b", "d": "b", "x": "x", "y": "x", "z": "z"} {
		if got := u.find(k); got != want {
			t.Errorf("find(%q) = %q, want %q", k, got, want)
		}
	}
}

// dedupe finds a copy with other tags by its notes: the copy is CP1252 with CRLF line
// endings, another #GAP and line breaks, the original UTF-8.
func TestRunDedupeByContent(t *testing.T) {
	noFFprobe(t)
	base := t.TempDir()
	lib := filepath.Join(base, "library")
	body := tenNotes(": %d 2 5 M\u00e4dchen%d")
	cp1252 := strings.ReplaceAll(chart("#ARTIST:Unknown Artist\n#TITLE:Track 03\n#MP3:a.mp3\n#BPM:300\n#GAP:480\n",
		append(body[:5:5], append([]string{"- 19"}, body[5:]...)...)...), "\u00e4", "\xe4")
	testutil.WriteFiles(t, lib, merge(
		songFiles("Pop/Original", "Die Band", "Mädchen", "cover"),
		songFiles("C/Cher - Believe", "Cher", "Believe"),
		map[string]string{
			"Pop/Original/song.txt": chart("#ARTIST:Die Band\n#TITLE:Mädchen\n#MP3:song.mp3\n#COVER:cover.png\n#BPM:300\n#GAP:0\n", body...),
			"Misc/Track 03/a.txt":   strings.ReplaceAll(cp1252, "\n", "\r\n"),
			"Misc/Track 03/a.mp3":   "audio",
		},
	))
	cfg := prepared(t)(config.NewDedupe(lib, filepath.Join(base, "out"), false))
	st, rep := runOrganize(t, cfg, Options{Mode: ModeDedupe})
	want := []duplicateEntry{{
		Song: "library:Misc/Track 03", Name: "Unknown Artist - Track 03", Keeper: "library:Pop/Original",
		Reason: "no cover" + joinedByContent, Dest: "duplicates/library/Misc/Track 03", Outcome: outcomeMoved,
	}}
	if !reflect.DeepEqual(rep.Duplicates, want) || st.Moved != 1 {
		t.Errorf("moved %d, duplicates = %+v\nwant %+v", st.Moved, rep.Duplicates, want)
	}
	if len(rep.AlreadyInPlace) != 2 {
		t.Errorf("in place = %+v, want Pop/Original and Cher", rep.AlreadyInPlace)
	}
}
