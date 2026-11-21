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

// verse is a lyric of n distinct words "w0 w1 ...", with the given word replaced at the
// given positions (typo fixes).
func verse(n int, prefix string, repl map[int]string) []string {
	var w []string
	for i := range n {
		if r, ok := repl[i]; ok {
			w = append(w, r)
		} else {
			w = append(w, fmt.Sprintf("%s%d", prefix, i))
		}
	}
	return w
}

// sung returns a chart for words: one note per word, 4 beats apart, a line break every
// 5 words without a space before the first word of a line. bpm, the beat scale and the
// pitch shift change the txt, not the chart in ms.
func sung(bpm string, scale, shift int, words []string) string {
	var lines []string
	for i, w := range words {
		if i > 0 && i%5 == 0 {
			lines = append(lines, fmt.Sprintf("- %d", (i*4-1)*scale))
		}
		sep := " "
		if i%5 == 0 {
			sep = "" // a line starts without a space
		}
		lines = append(lines, fmt.Sprintf(": %d %d %d %s%s", i*4*scale, 2*scale, 5+shift+i%3, sep, w))
	}
	return chart("#BPM:"+bpm+"\n#GAP:100\n", lines...)
}

func profileOf(t *testing.T, txt string) *Profile {
	t.Helper()
	s, _ := songtxt.Parse(txt)
	return NewProfile(s)
}

func TestNewProfile(t *testing.T) {
	words := verse(40, "w", nil)
	p := profileOf(t, sung("300", 1, 0, words))
	if p == nil || len(p.Chart) != 40 || len(p.Shingles) != 37 {
		t.Fatalf("profile = %+v", p)
	}
	if got := p.Chart[1]; got != (chartNote{Start: 200, Len: 100, Pitch: 1}) {
		t.Errorf("note 1 = %+v, want 200 ms after the first, 100 ms long, pitch +1", got)
	}
	// Line breaks end words, also without a space: w4 and w5 are two words.
	s, _ := songtxt.Parse(sung("300", 1, 0, words))
	if got := lyricWords(s.Notes, s.Breaks); !reflect.DeepEqual(got, words) {
		t.Errorf("words = %q", got)
	}
	same := map[string]string{
		"#BPM doubled, beats doubled": sung("600", 2, 0, words),
		"moved by two octaves":        sung("300", 1, 24, words),
		"moved by a fifth":            sung("300", 1, 7, words),
	}
	for name, txt := range same {
		q := profileOf(t, txt)
		if !chartMatch(p, q) || q.chartKey != p.chartKey {
			t.Errorf("%s: chart differs", name)
		}
		if j, c := lyricsSimilarity(p, q); j != 1 || c != 1 {
			t.Errorf("%s: lyrics %v/%v", name, j, c)
		}
	}
	for name, txt := range map[string]string{
		"9 notes":   chart("#BPM:300\n", tenNotes(": %d 2 5 la%d")[:9]...),
		"no #BPM":   chart("#TITLE:x\n", tenNotes(": %d 2 5 la%d")...),
		"#BPM zero": chart("#BPM:0\n", tenNotes(": %d 2 5 la%d")...),
	} {
		if q := profileOf(t, txt); q != nil {
			t.Errorf("%s: profile %+v", name, q)
		}
	}
	// One note moved by an octave is still the same chart (pitch within an octave).
	octave := strings.Replace(sung("300", 1, 0, words), ": 8 2 7 ", ": 8 2 19 ", 1)
	if q := profileOf(t, octave); !chartMatch(p, q) {
		t.Error("a note moved by an octave changes the chart")
	}
	// A BPM change is applied: the notes after it are further apart.
	withChange := profileOf(t, chart("#BPM:300\n", append([]string{"B 8 150"}, tenNotes(": %d 2 5 la%d")...)...))
	if got := withChange.Chart[3].Start; got != 8*50+4*100 { // beats 0-8 at 50 ms, 8-12 at 100 ms
		t.Errorf("start after the BPM change = %d", got)
	}
	// A single player marker counts as none, also for the line breaks.
	single := profileOf(t, strings.Replace(sung("300", 1, 0, words), "#GAP:100\n", "#GAP:100\nP1\n", 1))
	if !chartMatch(p, single) || !reflect.DeepEqual(single.Shingles, p.Shingles) {
		t.Error("a single P1 marker changes the profile")
	}
}

func TestLyricWords(t *testing.T) {
	for name, c := range map[string]struct {
		body string
		want []string
	}{
		"a break at the start of the next note ends the word, but only once": {
			": 0 2 0 a\n- 4\n: 4 2 0 b\n: 8 2 0 c\n: 12 2 0 d\n- 16\n", []string{"a", "bcd"}},
		"a break before the next note":   {": 0 2 0 a\n- 2\n: 4 2 0 b\n", []string{"a", "b"}},
		"no break: one word":             {": 0 2 0 a\n: 4 2 0 b\n", []string{"ab"}},
		"a held syllable joins its word": {": 0 2 0 wor\n: 4 2 0 ~\n: 8 2 0 ld\n", []string{"world"}},
		"case, punctuation, NFC":         {": 0 2 0 Ma\u0308d,\n: 4 2 0  CHEN!\n", []string{"mäd", "chen"}},
		"each singer starts a new word":  {"P1\n: 0 2 0 a\nP2\n: 0 2 0 b\n", []string{"a", "b"}},
		"a break of the other singer":    {"P1\n: 0 2 0 a\n: 8 2 0 b\nP2\n- 4\n: 0 2 0 c\n", []string{"ab", "c"}},
	} {
		s, _ := songtxt.Parse("#BPM:300\n" + c.body + "E\n")
		if got := lyricWords(s.Notes, s.Breaks); !reflect.DeepEqual(got, c.want) {
			t.Errorf("%s: %q, want %q", name, got, c.want)
		}
	}
	if got := shingles([]string{"a", "b", "c", "d", "a", "b", "c", "d"}); len(got) != 4 {
		t.Errorf("%d shingles of a repeated line, want 4 unique", len(got))
	}
}

func TestLyricsSimilarity(t *testing.T) {
	a := profileOf(t, sung("300", 1, 0, verse(40, "w", nil)))
	b := profileOf(t, sung("300", 1, 0, verse(40, "w", map[int]string{20: "typo"}))) // 4 of 37 sequences differ
	if j, c := lyricsSimilarity(a, b); fmt.Sprintf("%.3f %.3f", j, c) != "0.805 0.892" {
		t.Errorf("one typo: %v %v", j, c)
	}
	short := profileOf(t, sung("300", 1, 0, verse(20, "w", nil))) // the first half
	if j, c := lyricsSimilarity(a, short); fmt.Sprintf("%.3f %.3f", j, c) != "0.459 1.000" {
		t.Errorf("first half: %v %v", j, c)
	}
	if j, c := lyricsSimilarity(a, &Profile{}); j != 0 || c != 0 {
		t.Errorf("no lyrics: %v %v", j, c)
	}
}

func TestChartMatch(t *testing.T) {
	base := &Profile{Chart: []chartNote{{0, 100, 0, 0}, {500, 200, 3, 0}}}
	for name, c := range map[string]struct {
		notes []chartNote
		want  bool
	}{
		"equal":           {[]chartNote{{0, 100, 0, 0}, {500, 200, 3, 0}}, true},
		"start +30 ms":    {[]chartNote{{0, 100, 0, 0}, {530, 200, 3, 0}}, true},
		"start -31 ms":    {[]chartNote{{0, 100, 0, 0}, {469, 200, 3, 0}}, false},
		"length +30 ms":   {[]chartNote{{0, 130, 0, 0}, {500, 200, 3, 0}}, true},
		"length -31 ms":   {[]chartNote{{0, 69, 0, 0}, {500, 200, 3, 0}}, false},
		"other pitch":     {[]chartNote{{0, 100, 0, 0}, {500, 200, 4, 0}}, false},
		"other singer":    {[]chartNote{{0, 100, 0, 0}, {500, 200, 3, 1}}, false},
		"one note more":   {[]chartNote{{0, 100, 0, 0}, {500, 200, 3, 0}, {900, 1, 0, 0}}, false},
		"no notes at all": {nil, false},
	} {
		if got := chartMatch(base, &Profile{Chart: c.notes}); got != c.want {
			t.Errorf("%s: %v", name, got)
		}
	}
	if chartMatch(&Profile{}, &Profile{}) {
		t.Error("two empty charts match")
	}
}

func TestArtistSimilar(t *testing.T) {
	for _, c := range []struct {
		a, b string
		want bool
	}{
		{"ABBA", "Abba", true},
		{"Mis-Teeq", "Misteeq", true},
		{"Glee", "Glee Cast", true},
		{"Disney’s Aladdin", "Aladdin", true},
		{"Elvis Presly", "Elvis Presley", true},
		{"LeAnn Rhimes", "LeAnn Rimes", true},
		{"Beyoncé", "Beyonce", true},
		{"Tom Petty & The Heartbreakers", "Tom Petty", true},
		{"AB", "ABC", true},             // one edit is always allowed
		{"AB", "XABY", false},           // contained, but too short; 2 edits
		{"Cats", "Elaine Paige", false}, // other artists
		{"Stefan Raab", "Unbekannter Interpret", false},
		{"30 Seconds To Mars", "Thirty Seconds to Mars", false},
		{"", "ABBA", false},
		{"", "A", false},
		{"...", "...", false},
	} {
		if got := artistSimilar(c.a, c.b); got != c.want {
			t.Errorf("artistSimilar(%q, %q) = %v", c.a, c.b, got)
		}
		if got := artistSimilar(c.b, c.a); got != c.want {
			t.Errorf("artistSimilar(%q, %q) = %v (reversed)", c.b, c.a, got)
		}
	}
	for _, c := range []struct {
		a, b string
		want int
	}{{"", "abc", 3}, {"abc", "", 3}, {"kitten", "sitting", 3}, {"ä", "a", 1}, {"same", "same", 0}} {
		if got := levenshtein(c.a, c.b); got != c.want {
			t.Errorf("levenshtein(%q, %q) = %d", c.a, c.b, got)
		}
	}
}

func TestVariantWords(t *testing.T) {
	for title, want := range map[string]string{
		"Song":                          "",
		"Song (Album Version)":          "",
		"Song (Radio Edit)":             "",
		"Song (Live)":                   "live",
		"Song [Rough Mix]":              "mix",
		"Song - Live Acoustic (Live)":   "acoustic live",
		"Song (Unplugged Version) Live": "live unplugged",
	} {
		if got := variantWords(title); got != want {
			t.Errorf("variantWords(%q) = %q, want %q", title, got, want)
		}
	}
}

// withProfile gives a candidate the profile of a chart.
func withProfile(t *testing.T, c Candidate, txt string) Candidate {
	t.Helper()
	c.Profile = profileOf(t, txt)
	return c
}

func TestMakePlanSimilar(t *testing.T) {
	good := Quality{HasVideo: true, VideoHeight: 1080, AudioBitrate: 320000, HasCover: true}
	poor := Quality{AudioBitrate: 128000}
	v := verse(120, "w", nil)
	typo := sung("300", 1, 12, verse(120, "w", map[int]string{60: "typo"})) // 113 of 121 sequences: 93 %
	other := sung("250", 1, 0, verse(120, "x", nil))
	parody := sung("300", 1, 0, verse(120, "w", map[int]string{10: "a", 30: "b", 50: "c", 70: "d", 90: "e", 110: "f"})) // 93 of 141: 65 %
	short := sung("300", 1, 0, v[:60])
	retimed := sung("280", 1, 0, v)
	joined := func(reason string) string { return reason + joinedBySimilar }
	tests := []struct {
		name    string
		cands   []Candidate
		want    []string
		similar []string
	}{
		{"a typo, an octave and another artist spelling: one song", []Candidate{
			withProfile(t, song(hq, "a", "Mis-Teeq", "Scandalous", good), sung("300", 1, 0, v)),
			withProfile(t, song(low1, "b", "Misteeq", "Scandalouz", poor), typo),
			withProfile(t, song(low1, "c", "Other", "Song", poor), other),
		}, []string{
			"hq:a: library M/Mis-Teeq - Scandalous",
			"low1:b: duplicate <hq:a> (" + joined("no video") + ")",
			"low1:c: library O/Other - Song",
		}, nil},
		{"other artist, other version word, or a duet: listed, not joined", []Candidate{
			withProfile(t, song(hq, "a", "Mis-Teeq", "Scandalous", good), sung("300", 1, 0, v)),
			withProfile(t, song(low1, "b", "Cover Band", "Scandalous", poor), typo),
			withProfile(t, song(low1, "c", "Mis-Teeq", "Scandalous (Live)", poor), typo),
			withProfile(t, song(low1, "d", "Mis-Teeq", "Scandalous [DUET]", poor), typo),
		}, []string{
			"hq:a: library M/Mis-Teeq - Scandalous",
			"low1:b: library C/Cover Band - Scandalous",
			"low1:c: library M/Mis-Teeq - Scandalous (Live)",
			"low1:d: library M/Mis-Teeq - Scandalous [DUET]",
		}, []string{
			"hq:a | low1:b: same chart, lyrics 93 % alike, but other artists (an alias, a placeholder artist, or a cover that reuses the chart)",
			`hq:a | low1:c: same chart, lyrics 93 % alike, but the titles name other versions ("" vs "live") [possible]`,
			`low1:b | low1:c: same chart, lyrics 100 % alike, but other artists (an alias, a placeholder artist, or a cover that reuses the chart)`,
		}},
		{"weaker evidence: listed", []Candidate{
			withProfile(t, song(hq, "a", "Band", "Song", good), sung("300", 1, 0, v)),
			withProfile(t, song(low1, "b", "Band", "Song (Parody)", poor), parody),
			withProfile(t, song(low1, "c", "Band", "Song (TV)", poor), short),
			withProfile(t, song(low1, "d", "Band", "Song (Remaster)", poor), retimed),
			withProfile(t, song(low1, "e", "Unrelated", "Cover", poor), retimed),
			withProfile(t, song(low2, "f", "Band", "Rewrite", poor), sung("300", 1, 0, verse(120, "z", nil))),
		}, []string{
			"hq:a: library B/Band - Song",
			"low1:b: library B/Band - Song (Parody)",
			"low1:c: library B/Band - Song (TV)",
			"low1:d: duplicate <hq:a> (no video)", // a noise qualifier: same SongKey
			"low1:e: library U/Unrelated - Cover",
			"low2:f: library B/Band - Rewrite",
		}, []string{
			"hq:a | low1:b: same chart, lyrics 65 % alike (below the 90 % needed to join them) [possible]",
			"hq:a | low1:c: the shorter lyrics are 100 % contained in the longer ones (a shorter cut: TV size, edit?) [possible]",
			"hq:a | low2:f: same chart, but only 0 % of the lyrics alike (a parody, translation or rewrite?)",
			"low1:b | low1:c: only 34 % of the lyrics alike: probably different versions [possible]",
			"low1:b | low2:f: same chart, but only 0 % of the lyrics alike (a parody, translation or rewrite?)",
		}},
		{"lyrics alike but another chart, or 81 % alike: listed, not joined", []Candidate{
			withProfile(t, song(hq, "a", "Band", "Song", good), sung("300", 1, 0, v)),
			withProfile(t, song(low1, "b", "Band", "Sogn", poor), retimed),
			withProfile(t, song(low1, "c", "Band", "Snog", poor), sung("280", 1, 0, verse(120, "w", map[int]string{20: "x", 60: "y", 100: "z"}))),
		}, []string{
			"hq:a: library B/Band - Song",
			"low1:b: library B/Band - Sogn",
			"low1:c: library B/Band - Snog",
		}, []string{
			"hq:a | low1:b: lyrics 100 % alike, other chart (re-timed or charted anew)",
			"hq:a | low1:c: lyrics 81 % alike, other chart",
			"low1:b | low1:c: same chart, lyrics 81 % alike (below the 90 % needed to join them)",
		}},
		{"exactly 90 % joins; a version joined by content and alike keeps the content reason", []Candidate{
			withProfile(t, song(hq, "a", "Band", "Song", good), sung("300", 1, 0, verse(79, "w", nil))),
			withProfile(t, song(low1, "b", "Band", "Sogn", poor), sung("300", 1, 0, verse(79, "w", map[int]string{40: "x"}))), // 72 of 80
			withHash(withProfile(t, song(low1, "c", "Band", "Snog", poor), sung("300", 1, 0, verse(79, "w", nil))), "h"),
			withHash(song(low2, "d", "Band", "Song", poor), "h"),
		}, []string{
			"hq:a: library B/Band - Song",
			"low1:b: duplicate <hq:a> (" + joined("no video") + ")",
			"low1:c: duplicate <hq:a> (no video" + joinedByContent + ")",
			"low2:d: duplicate <hq:a> (no video)",
		}, nil},
		{"no profile in a possible-duplicates group: no evidence", []Candidate{
			song(hq, "a", "Band", "Song", good),
			withProfile(t, song(low1, "b", "Band", "Song (Live)", poor), short),
		}, []string{
			"hq:a: library B/Band - Song",
			"low1:b: library B/Band - Song (Live)",
		}, []string{"hq:a | low1:b:  [possible]"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			p := MakePlan(tt.cands, PlanOptions{})
			if got := summary(tt.cands, p); !reflect.DeepEqual(got, tt.want) {
				t.Errorf("plan:\n%s\nwant:\n%s", strings.Join(got, "\n"), strings.Join(tt.want, "\n"))
			}
			var got []string
			for _, s := range p.Similar {
				line := tt.cands[s.A].Ref() + " | " + tt.cands[s.B].Ref() + ": " + s.Evidence
				if s.Possible {
					line += " [possible]"
				}
				got = append(got, line)
			}
			if !reflect.DeepEqual(got, tt.similar) {
				t.Errorf("similar:\n%s\nwant:\n%s", strings.Join(got, "\n"), strings.Join(tt.similar, "\n"))
			}
		})
	}
}

// findSimilar pairs by shared lyrics and by the coarse chart key; very common lyrics and
// duet/solo pairs are never compared.
func TestFindSimilar(t *testing.T) {
	v := verse(120, "w", nil)
	cands := []Candidate{
		withProfile(t, song(hq, "a", "A", "A", Quality{}), sung("300", 1, 0, v)),
		withProfile(t, song(hq, "b", "B", "B", Quality{}), sung("280", 1, 0, v)),                    // same lyrics, other chart
		withProfile(t, song(hq, "c", "C", "C", Quality{}), sung("300", 1, 0, verse(120, "x", nil))), // same chart only
		withProfile(t, song(hq, "d", "D", "D", Quality{}), sung("300", 1, 0, v)),
	}
	cands[3].Duet = true
	cands = append(cands,
		// The first quarter of the lyrics with another chart: compared (the shorter lyrics
		// are all shared).
		withProfile(t, song(hq, "e", "E", "E", Quality{}), sung("250", 1, 0, v[:30])),
		// Too few words to compare (19 of 20 sequences), not even with each other.
		withProfile(t, song(hq, "f", "F", "F", Quality{}), sung("250", 1, 1, v[:22])),
		withProfile(t, song(hq, "g", "G", "G", Quality{}), sung("240", 1, 1, v[:22])),
	)
	got := findSimilar(cands, []int{6, 5, 4, 3, 2, 1, 0}) // the order of idx does not matter
	var keys []string
	for _, p := range got {
		keys = append(keys, fmt.Sprintf("%d-%d chart=%v", p.A, p.B, p.ChartMatch))
	}
	if want := []string{"0-1 chart=false", "0-2 chart=true", "0-4 chart=false", "1-4 chart=false"}; !reflect.DeepEqual(keys, want) {
		t.Errorf("pairs = %v, want %v", keys, want)
	}

	// The same chorus in more than commonShingle songs is not a reason to compare them.
	var many []Candidate
	var idx []int
	chorus := verse(40, "la", nil) // 37 of the 57 sequences of each song
	for i := range commonShingle + 1 {
		words := append(append([]string{}, chorus...), verse(20, fmt.Sprintf("s%dx", i), nil)...)
		c := withProfile(t, song(hq, fmt.Sprint(i), "X", fmt.Sprint(i), Quality{}), sung("300", 1, 0, words))
		c.Profile.chartKey = uint64(i) // keep the chart index out of it
		many = append(many, c)
		idx = append(idx, i)
	}
	if got := findSimilar(many, idx); len(got) != 0 {
		t.Errorf("%d pairs from a common chorus", len(got))
	}
	many = many[:commonShingle]
	if got := findSimilar(many, idx[:commonShingle]); len(got) != commonShingle*(commonShingle-1)/2 {
		t.Errorf("%d pairs, want all pairs of a chorus in %d songs", len(got), commonShingle)
	}
}

// dedupe joins versions with the same chart in ms through real files.
func TestRunDedupeSimilar(t *testing.T) {
	noFFprobe(t)
	base := t.TempDir()
	lib := filepath.Join(base, "library")
	v := verse(120, "w", nil)
	testutil.WriteFiles(t, lib, map[string]string{
		"A/Band - Song/song.txt":     "#ARTIST:Band\n#TITLE:Song\n#MP3:a.mp3\n" + sung("300", 1, 0, v),
		"A/Band - Song/a.mp3":        "audio",
		"Misc/Bnad - Sogn/song.txt":  "#ARTIST:The Band\n#TITLE:Sogn\n#MP3:a.mp3\n#COVER:c.png\n" + sung("600", 2, -12, verse(120, "w", map[int]string{60: "typo"})),
		"Misc/Bnad - Sogn/a.mp3":     "audio",
		"Misc/Bnad - Sogn/c.png":     pngImage(4, 4),
		"Misc/Cover - Song/song.txt": "#ARTIST:Tribute Group\n#TITLE:Song\n#MP3:a.mp3\n" + sung("300", 1, 0, verse(120, "w", map[int]string{90: "other"})),
		"Misc/Cover - Song/a.mp3":    "audio",
	})
	cfg := prepared(t)(config.NewDedupe(lib, filepath.Join(base, "out"), true))
	_, rep := runOrganize(t, cfg, Options{Mode: ModeDedupe})
	if len(rep.Duplicates) != 1 || rep.Duplicates[0].Song != "library:A/Band - Song" || rep.Duplicates[0].Reason != "no cover"+joinedBySimilar {
		t.Errorf("duplicates = %+v", rep.Duplicates)
	}
	if rep.Summary.Similar != 1 || len(rep.Similar) != 1 || rep.Similar[0].Songs != [2]string{"library:Misc/Bnad - Sogn", "library:Misc/Cover - Song"} || !strings.HasPrefix(rep.Similar[0].Evidence, "same chart, lyrics 87 % alike (below the 90 % needed to join them)") {
		t.Errorf("similar = %+v (summary %d)", rep.Similar, rep.Summary.Similar)
	}
	if txt := string(rep.text()); !strings.Contains(txt, "== Similar songs (review manually; nothing was merged) (1) ==\nlibrary:Misc/Bnad - Sogn  |  library:Misc/Cover - Song\n    same chart") {
		t.Errorf("text report:\n%s", txt)
	}
}

// A possible-duplicates group of three shows the evidence per pair.
func TestReportSimilarText(t *testing.T) {
	cands := []Candidate{
		song(hq, "a", "Band", "Song", Quality{}),
		song(hq, "b", "Band", "Song (Live)", Quality{}),
		song(hq, "c", "Band", "Song (TV)", Quality{}),
	}
	plan := MakePlan(cands, PlanOptions{})
	plan.Similar = []Similar{
		{A: 0, B: 1, Evidence: "e01", Possible: true},
		{A: 0, B: 2, Possible: true},
		{A: 1, B: 2, Evidence: "e12", Possible: true},
	}
	txt := string(buildReport(reportInput{cands: cands, plan: plan}).text())
	want := "hq:a  |  hq:b  |  hq:c\n    hq:a  |  hq:b: e01\n    hq:b  |  hq:c: e12\n"
	if !strings.Contains(txt, want) {
		t.Errorf("text:\n%s\nwant:\n%s", txt, want)
	}
}
