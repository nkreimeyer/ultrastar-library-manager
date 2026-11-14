package organize

import (
	"fmt"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func checkSummary(t *testing.T, cands []Candidate, p Plan, want []string) {
	t.Helper()
	if got := summary(cands, p); !reflect.DeepEqual(got, want) {
		t.Errorf("got\n  %s\nwant\n  %s", strings.Join(got, "\n  "), strings.Join(want, "\n  "))
	}
}

// The move that depends on a later one being left comes first in the decisions, so a
// single pass of resolveOccupied is not enough.
func TestResolveOccupiedFixpoint(t *testing.T) {
	cands := []Candidate{
		song(hq, "s", "ABBA", "SOS", Quality{}),
		song(lib, "A/ABBA - SOS", "B", "C", Quality{}),
	}
	occ := func(rel string) (string, string, bool) {
		switch NameKey(rel) {
		case NameKey("A/ABBA - SOS"), NameKey("B/B - C"):
			return rel, "", true
		}
		return "", "", false
	}
	checkSummary(t, cands, MakePlan(cands, PlanOptions{Occupied: occ}), []string{
		"hq:s: left (target is occupied by library:A/ABBA - SOS, which stays in place)",
		"library:A/ABBA - SOS: left (" + reasonTargetExists + ")",
	})

	// A bucket chain: the library song folder "Z" cannot move (its target is taken), so
	// nothing can go into the bucket Z.
	cands = []Candidate{
		song(hq, "s", "Zed", "Song", Quality{}),
		song(lib, "Z", "Abba", "Waterloo", Quality{}),
	}
	occ = func(rel string) (string, string, bool) {
		if NameKey(rel) == NameKey("A/Abba - Waterloo") {
			return rel, "", true
		}
		return "", "", false
	}
	checkSummary(t, cands, MakePlan(cands, PlanOptions{Occupied: occ}), []string{
		"hq:s: left (bucket folder Z is the song folder library:Z, which stays in place)",
		"library:Z: left (" + reasonTargetExists + ")",
	})
}

// A keeper already at the plain name keeps it, even if another member of its family has
// a smaller SongKey; a numbered keeper outside the bucket is moved into it.
func TestAssignTargetsInPlaceKeepers(t *testing.T) {
	// Rock'n'Roll -> key a|rocknroll; Rock"n"Roll -> key a|rock n roll (sorts first). Both
	// have the folder name "A - Rock'n'Roll".
	cands := []Candidate{
		song(hq, "s", "A", `Rock"n"Roll`, Quality{}),
		song(lib, "A/A - Rock'n'Roll", "A", "Rock'n'Roll", Quality{}),
	}
	checkSummary(t, cands, MakePlan(cands, PlanOptions{}), []string{
		"hq:s: library A/A - Rock'n'Roll (2)",
		"library:A/A - Rock'n'Roll: in-place A/A - Rock'n'Roll",
	})

	cands = []Candidate{
		song(hq, "s", "A", `Rock"n"Roll`, Quality{}),
		song(lib, "old/A - Rock'n'Roll (2)", "A", "Rock'n'Roll", Quality{}),
	}
	checkSummary(t, cands, MakePlan(cands, PlanOptions{}), []string{
		"hq:s: library A/A - Rock'n'Roll",
		"library:old/A - Rock'n'Roll (2): library A/A - Rock'n'Roll (2)",
	})

	// In the right bucket the numbered keeper keeps its number, and the plain name goes
	// to the other one.
	cands = []Candidate{
		song(hq, "s", "A", `Rock"n"Roll`, Quality{}),
		song(lib, "A/A - Rock'n'Roll (3)", "A", "Rock'n'Roll", Quality{}),
	}
	checkSummary(t, cands, MakePlan(cands, PlanOptions{}), []string{
		"hq:s: library A/A - Rock'n'Roll",
		"library:A/A - Rock'n'Roll (3): in-place A/A - Rock'n'Roll (3)",
	})
}

func TestIsFamilyName(t *testing.T) {
	for _, c := range []struct {
		folder, name string
		want         bool
	}{
		{"X", "X", true},
		{"x", "X", true},
		{"x (2)", "X", true},
		{"X (10)", "X", true},
		{"X (1)", "X", false},
		{"X (0)", "X", false},
		{"X (02)", "X", false},
		{"X (a)", "X", false},
		{"X(2)", "X", false},
		{"X (2", "X", false},
		{"Y (2)", "X", false},
		{"Y", "X", false},
	} {
		if got := isFamilyName(c.folder, c.name); got != c.want {
			t.Errorf("isFamilyName(%q, %q) = %v, want %v", c.folder, c.name, got, c.want)
		}
	}
}

func TestBitrateCloseBoundary(t *testing.T) {
	for _, c := range []struct {
		b, hi int64
		want  bool
	}{
		{320000, 320000, true},
		{288000, 320000, true}, // exactly 10 % below
		{287999, 320000, false},
		{0, 320000, false},
	} {
		if got := bitrateClose(c.b, c.hi); got != c.want {
			t.Errorf("bitrateClose(%d, %d) = %v, want %v", c.b, c.hi, got, c.want)
		}
	}
	// 288k with a cover beats 320k without one: the bitrates count as equal.
	cs := []*Candidate{
		{Label: "a", Rel: "a", Quality: Quality{AudioBitrate: 320000}},
		{Label: "b", Rel: "b", Source: 1, Quality: Quality{AudioBitrate: 288000, HasCover: true}},
	}
	if k, reasons := chooseKeeper(cs, false); k != 1 || reasons[0] != "no cover" {
		t.Errorf("keeper = %d, reasons %q", k, reasons)
	}
}

// Possible-duplicate groups come out in LooseKey order, not SongKey order, and keepers
// without a LooseKey form no group.
func TestPossibleDuplicatesOrder(t *testing.T) {
	cands := []Candidate{
		song(hq, "1", "X", "A b", Quality{}),
		song(hq, "2", "X", "A b (Live)", Quality{}),
		song(hq, "3", "X", "A [DUET]", Quality{}),
		song(hq, "4", "X", "A (Live) [DUET]", Quality{}),
		song(hq, "5", "!!!", "Song A", Quality{}),
		song(hq, "6", "!!!", "Song A (Live)", Quality{}),
	}
	p := MakePlan(cands, PlanOptions{})
	// SongKeys: x|a b < x|a b live < x|a live|duet < x|a|duet; LooseKeys: x|a < x|a b.
	if want := [][]int{{3, 2}, {0, 1}}; !reflect.DeepEqual(p.PossibleDuplicates, want) {
		t.Errorf("possible duplicates = %v, want %v", p.PossibleDuplicates, want)
	}
}

// A duet and a solo version are never possible duplicates of each other, even when only
// a qualifier tells them apart; two duets (or two solo versions) still are. A duet from
// P1/P2 without a duet qualifier counts as a duet too. With the same LooseKey, the solo
// group comes before the duet group.
func TestPossibleDuplicatesDuetVsSolo(t *testing.T) {
	cands := []Candidate{
		song(hq, "1", "X", "Song", Quality{}),
		song(hq, "2", "X", "Song (Live) [DUET]", Quality{}),
		song(hq, "3", "X", "Song [DUET]", Quality{}),
		song(hq, "4", "Y", "Other", Quality{}),
		song(hq, "5", "Y", "Other (Acoustic)", Quality{}),
		song(hq, "6", "Z", "Third", Quality{}),
		song(hq, "7", "Z", "Third (Live)", Quality{}),
		song(hq, "8", "X", "Song (Live)", Quality{}),
	}
	cands[3].Duet = true // P1/P2 in the txt
	p := MakePlan(cands, PlanOptions{})
	if want := [][]int{{0, 7}, {1, 2}, {5, 6}}; !reflect.DeepEqual(p.PossibleDuplicates, want) {
		t.Errorf("possible duplicates = %v, want %v", p.PossibleDuplicates, want)
	}
	for i, want := range []string{"X/X - Song", "X/X - Song (Live) [DUET]", "X/X - Song [DUET]", "Y/Y - Other [DUET]"} {
		if d := p.Decisions[i]; d.Action != ActMove || d.Target != want {
			t.Errorf("%d: %+v, want to the library at %s", i+1, d, want)
		}
	}
}

// A broken duet names the working duet as its replacement, never the solo version.
func TestMakePlanBrokenDuetReplacement(t *testing.T) {
	broken := brokenSong(hq, "broken", "Artist", "Song", "no-bpm")
	broken.Duet = true
	solo := song(low1, "solo", "Artist", "Song", Quality{})
	duet := song(low1, "duet", "Artist", "Song", Quality{})
	duet.Duet = true
	cands := []Candidate{broken, solo, duet}
	if d := MakePlan(cands, PlanOptions{}).Decisions[0]; d.Action != ActInvalid || d.Other != 2 {
		t.Errorf("broken duet: %+v, want invalid with the duet (2) as replacement", d)
	}
	cands = []Candidate{broken, solo}
	if d := MakePlan(cands, PlanOptions{}).Decisions[0]; d.Action != ActInvalid || d.Other != -1 || d.Reason != "errors: no-bpm" {
		t.Errorf("broken duet with only a solo version: %+v, want no replacement", d)
	}
}

func TestCandidateSong(t *testing.T) {
	for _, c := range []struct {
		c    Candidate
		want string
	}{
		{Candidate{}, ""},
		{Candidate{Artist: "A"}, "A - "},
		{Candidate{Title: "T"}, " - T"},
		{Candidate{Artist: "A", Title: "T"}, "A - T"},
	} {
		if got := c.c.Song(); got != c.want {
			t.Errorf("%+v.Song() = %q, want %q", c.c, got, c.want)
		}
	}
}

func TestSongKeyLettersAndSeparators(t *testing.T) {
	equal := []struct{ a, b songID }{
		{songID{"Æther", "Song", false}, songID{"Aether", "Song", false}},
		{songID{"Þór", "Song", false}, songID{"Thor", "Song", false}},
		{songID{"Łódź", "Song", false}, songID{"Lodz", "Song", false}},
		{songID{"Œuvre", "Song", false}, songID{"Oeuvre", "Song", false}},
		{songID{"Đorđe", "Song", false}, songID{"Dorde", "Song", false}},
		{songID{"Ðoe", "Song", false}, songID{"Doe", "Song", false}},
		{songID{"Ħal", "Song", false}, songID{"Hal", "Song", false}},
		{songID{"Ŧom", "Song", false}, songID{"Tom", "Song", false}},
		{songID{"Ŋa", "Song", false}, songID{"Na", "Song", false}},
		{songID{"ĸa", "Song", false}, songID{"Ka", "Song", false}},
		{songID{"Ŀa", "Song", false}, songID{"La", "Song", false}},
		{songID{"Col·lecció", "Song", false}, songID{"Colleccio", "Song", false}},
		{songID{"Ɨa", "Song", false}, songID{"Ia", "Song", false}},
		{songID{"Ƶa", "Song", false}, songID{"Za", "Song", false}},
		{songID{"Dıa", "Song", false}, songID{"Dia", "Song", false}},
		{songID{"Artist", "Don‘t", false}, songID{"Artist", "Dont", false}},
		{songID{"Artist", "Don`t", false}, songID{"Artist", "Dont", false}},
		{songID{"Artist", "Donʼt", false}, songID{"Artist", "Dont", false}},
		{songID{"Artist", "Donʹt", false}, songID{"Artist", "Dont", false}},
		{songID{"Artist", "Don′t", false}, songID{"Artist", "Dont", false}},
		{songID{"Artist", "Song — Live", false}, songID{"Artist", "Song (Live)", false}},
		{songID{"Artist", "Song – Live", false}, songID{"Artist", "Song (Live)", false}},
		{songID{"Artist", "Song — Remastered 2011", false}, songID{"Artist", "Song", false}},
		{songID{"Artist", "Song – Remastered 2011", false}, songID{"Artist", "Song", false}},
		{songID{"Artist", "Song ()", false}, songID{"Artist", "Song", false}},
		{songID{"Artist", "Song (!!)", false}, songID{"Artist", "Song", false}},
		{songID{"Artist", "Song (Lyrics)", false}, songID{"Artist", "Song", false}},
		{songID{"Artist", "Song (2099)", false}, songID{"Artist", "Song", false}},
		{songID{"Artist", "Song (1900)", false}, songID{"Artist", "Song", false}},
	}
	for _, c := range equal {
		if ka, kb := SongKey(c.a.artist, c.a.title, c.a.duet), SongKey(c.b.artist, c.b.title, c.b.duet); ka != kb || ka == "" {
			t.Errorf("SongKey(%q, %q) = %q, want %q", c.a.artist, c.a.title, ka, kb)
		}
	}
	distinct := []struct{ a, b songID }{
		{songID{"Artist", "Song (1899)", false}, songID{"Artist", "Song", false}},
		{songID{"Artist", "Song (2100)", false}, songID{"Artist", "Song", false}},
		{songID{"Artist", "Song (19840)", false}, songID{"Artist", "Song", false}},
		{songID{"Artist", "Song (198)", false}, songID{"Artist", "Song", false}},
		{songID{"Artist", "Song (19a4)", false}, songID{"Artist", "Song", false}},
		{songID{"Artist", "Song (feat. X Acoustic)", false}, songID{"Artist", "Song", false}},
		{songID{"Artist", "Song (feat. X Akustik)", false}, songID{"Artist", "Song", false}},
		{songID{"Artist", "Song feat. X Rmx", false}, songID{"Artist", "Song", false}},
		{songID{"Artist feat. X Live", "Song", false}, songID{"Artist", "Song", false}},
	}
	for _, c := range distinct {
		if ka, kb := SongKey(c.a.artist, c.a.title, c.a.duet), SongKey(c.b.artist, c.b.title, c.b.duet); ka == kb {
			t.Errorf("SongKey(%q, %q) == SongKey(%q, %q) = %q", c.a.artist, c.a.title, c.b.artist, c.b.title, ka)
		}
	}
	if got := classifyQualifier(nil); got != qualNoise {
		t.Errorf("classifyQualifier(nil) = %v", got)
	}
	if got, want := SongKey("Artist feat. X Live", "Song", false), "artist x live|song"; got != want {
		t.Errorf("guarded artist feat: %q, want %q", got, want)
	}
}

func TestFolderNameReserved(t *testing.T) {
	for _, c := range []struct{ artist, title, want string }{
		{"CONIN$.x", "T", "CONIN$_.x - T"},
		{"CONOUT$.x", "T", "CONOUT$_.x - T"},
		{"COM¹.x", "T", "COM¹_.x - T"},
		{"LPT³.x", "T", "LPT³_.x - T"},
		{"COM9.x", "T", "COM9_.x - T"},
		{"CON .x", "T", "CON_ .x - T"},
		{"PRN.x", "T", "PRN_.x - T"},
		{"com0.x", "T", "com0.x - T"},
		{"CONX.x", "T", "CONX.x - T"},
	} {
		if got := FolderName(c.artist, c.title); got != c.want {
			t.Errorf("FolderName(%q, %q) = %q, want %q", c.artist, c.title, got, c.want)
		}
	}
}

func TestBucketUndecomposableLetters(t *testing.T) {
	for _, c := range []struct{ artist, want string }{
		{"Ŧom", "T"}, {"ŧom", "T"}, {"Ŋoma", "N"}, {"ŋoma", "N"}, {"ĸa", "K"}, {"Ŀa", "L"}, {"ŀa", "L"},
		{"Ɨa", "I"}, {"ɨa", "I"}, {"Ƶa", "Z"}, {"ƶa", "Z"}, {"Ɓa", "B"}, {"ɓa", "B"}, {"Ƈa", "C"}, {"ƈa", "C"},
		{"Ɗa", "D"}, {"ɗa", "D"}, {"Ƒunk", "F"}, {"ƒunk", "F"}, {"Ɠa", "G"}, {"ɠa", "G"}, {"Ƙa", "K"}, {"ƙa", "K"},
		{"Ƥa", "P"}, {"ƥa", "P"}, {"Ƭa", "T"}, {"ƭa", "T"}, {"Ʋa", "V"}, {"ʋa", "V"}, {"Ȥa", "Z"}, {"ȥa", "Z"},
		{"Ħa", "H"}, {"ħa", "H"}, {"ıa", "I"}, {"ẞa", "S"}, {"ßa", "S"}, {"ǅemal", "D"}, {"Ĳssel", "I"},
	} {
		if got := Bucket(c.artist, false); got != c.want {
			t.Errorf("Bucket(%q) = %q, want %q", c.artist, got, c.want)
		}
	}
	// A rune that decomposes into marks only comes back as it is.
	if got := foldLetter('̈́'); got != '̈́' {
		t.Errorf("foldLetter(U+0344) = %U", got)
	}
}

// orderMoves marks the steps of a cycle: the park, the final step from the temporary
// name and the original source to move back to.
func TestOrderMovesStepFlags(t *testing.T) {
	temp := func(n int) string { return filepath.FromSlash(fmt.Sprintf("/lib/.tmp-%d", n)) }
	p := func(s string) string { return filepath.FromSlash(s) }
	steps := orderMoves([]libMove{{0, p("/lib/A/a"), p("/lib/B/b")}, {1, p("/lib/B/b"), p("/lib/A/a")}}, temp)
	want := []step{
		{Cand: 1, Src: p("/lib/B/b"), Dst: temp(0), ToTemp: true},
		{Cand: 0, Src: p("/lib/A/a"), Dst: p("/lib/B/b")},
		{Cand: 1, Src: temp(0), Dst: p("/lib/A/a"), ViaTemp: true, Orig: p("/lib/B/b")},
	}
	if !reflect.DeepEqual(steps, want) {
		t.Errorf("swap steps:\n%+v\nwant\n%+v", steps, want)
	}
	// A 3-cycle needs exactly one temporary name.
	steps = orderMoves([]libMove{
		{0, p("/lib/A/a"), p("/lib/B/b")}, {1, p("/lib/B/b"), p("/lib/C/c")}, {2, p("/lib/C/c"), p("/lib/A/a")},
	}, temp)
	temps := 0
	for _, s := range steps {
		if s.ToTemp {
			temps++
		}
	}
	if temps != 1 || len(steps) != 4 {
		t.Errorf("3-cycle: %d temporary steps in %+v", temps, steps)
	}
	if got := blockingMove([]libMove{{0, p("/s/a"), p("/lib/A/a")}}); got != 0 {
		t.Errorf("blockingMove without a blocker = %d", got)
	}
}

func TestBuildReportOrderAndText(t *testing.T) {
	cands := []Candidate{
		brokenSong(low1, "z", "Zed", "Song", "no-bpm"),
		brokenSong(hq, "y", "Yes", "Song", "no-notes", "no-bpm"),
		song(hq, "a", "Abba", "SOS", Quality{}),
		song(hq, "b", "Abba", "SOS (Live)", Quality{}),
		song(hq, "c", "Cher", "Believe", Quality{}),
		song(hq, "d", "Cher", "Believe (Live)", Quality{}),
		song(low1, "e", "Abba", "SOS", Quality{}),
		song(low1, "f", "Abba", "SOS", Quality{}),
	}
	cands[1].Errors = []string{"no-bpm", "no-notes"}
	plan := MakePlan(cands, PlanOptions{})
	// Evidence as the similarity check would give it (the candidates have no profiles).
	plan.Similar = []Similar{
		{A: 2, B: 3, Evidence: "lyrics 70 % alike, other chart", Possible: true},
		{A: 4, B: 5, Possible: true},
		{A: 3, B: 4, Evidence: "same chart, but only 10 % of the lyrics alike (a parody, translation or rewrite?)"},
	}
	build := func(perm []int, outcomes map[int]moveOutcome, dry bool) []byte {
		cs := make([]Candidate, len(perm))
		ds := make([]Decision, len(perm))
		inv := map[int]int{}
		for to, from := range perm {
			inv[from] = to
		}
		for to, from := range perm {
			cs[to] = cands[from]
			ds[to] = plan.Decisions[from]
			if ds[to].Other >= 0 {
				ds[to].Other = inv[ds[to].Other]
			}
		}
		var pd [][]int
		for g := len(plan.PossibleDuplicates) - 1; g >= 0; g-- {
			var grp []int
			for k := len(plan.PossibleDuplicates[g]) - 1; k >= 0; k-- {
				grp = append(grp, inv[plan.PossibleDuplicates[g][k]])
			}
			pd = append(pd, grp)
		}
		oc := map[int]moveOutcome{}
		for i, o := range outcomes {
			oc[inv[i]] = o
		}
		var sim []Similar
		for k := len(plan.Similar) - 1; k >= 0; k-- {
			e := plan.Similar[k]
			sim = append(sim, Similar{A: inv[e.B], B: inv[e.A], Evidence: e.Evidence, Possible: e.Possible})
		}
		in := reportInput{cands: cs, plan: Plan{Decisions: ds, PossibleDuplicates: pd, Similar: sim}, outcomes: oc}
		in.cfg.DryRun = dry
		in.cfg.Library = "/lib"
		in.emptyLeft = 3
		r := buildReport(in)
		if !dry && (r.Summary.Failed != 2 || r.Summary.Moved != 1) {
			t.Errorf("summary = %+v, want 2 failed, 1 moved", r.Summary)
		}
		return r.text()
	}
	outcomes := map[int]moveOutcome{0: {Status: outcomeFailed}, 1: {Status: outcomeSkipped}, 2: {Status: outcomeMoved}, 6: {Status: outcomeFailed}}
	ident := []int{0, 1, 2, 3, 4, 5, 6, 7}
	got := string(build(ident, outcomes, false))
	if rev := string(build([]int{7, 6, 5, 4, 3, 2, 1, 0}, outcomes, false)); rev != got {
		t.Errorf("report depends on the input order:\n%s\nvs\n%s", got, rev)
	}
	want := `organize plan (real run)
library: /lib
priority: off (quality decides between all sources)
sources:

summary: 8 songs: 4 to the library, 0 already in place, 2 duplicates, 2 invalid, 0 left in place; 2 possible duplicates and 1 similar songs to review; 0 empty folders ignored; 1 moved, 2 failed; 3 emptied category folders left in the sources

== Into the library (4) ==
A/Abba - SOS  <-  hq:a  [moved]
A/Abba - SOS (Live)  <-  hq:b
C/Cher - Believe  <-  hq:c
C/Cher - Believe (Live)  <-  hq:d

== Already in place (0) ==

== Duplicates (moved to duplicates/) (2) ==
low1:e  (Abba - SOS)  [failed]
    kept: hq:a
    reason: same quality; the version from the earlier source is kept
low1:f  (Abba - SOS)
    kept: hq:a
    reason: same quality; the version from the earlier source is kept

== Invalid (moved to invalid/) (2) ==
hq:y  [skipped]
    errors: no-bpm, no-notes
low1:z  [failed]
    errors: no-bpm

== Left in place (0) ==

== Possible duplicates (review manually; nothing was merged) (2) ==
hq:a  |  hq:b
    lyrics 70 % alike, other chart
hq:c  |  hq:d

== Similar songs (review manually; nothing was merged) (1) ==
hq:b  |  hq:c
    same chart, but only 10 % of the lyrics alike (a parody, translation or rewrite?)

== Empty folders (ignored) (0) ==
`
	if got != want {
		t.Errorf("text:\n%s\nwant:\n%s", got, want)
	}
	dry := string(build(ident, map[int]moveOutcome{2: {Status: outcomePlanned}}, true))
	if !strings.HasPrefix(dry, "organize plan (dry run: nothing was moved)\n") || strings.Contains(dry, "[planned]") ||
		!strings.Contains(dry, "; 1 planned moves, 0 failed;") {
		t.Errorf("dry-run text:\n%s", dry)
	}
	in := reportInput{plan: Plan{}, opts: Options{Priority: true}}
	if txt := string(buildReport(in).text()); !strings.Contains(txt, "priority: on (a lower tier always wins between working versions)\n") ||
		strings.Contains(txt, "emptied category") {
		t.Errorf("priority text:\n%s", txt)
	}
}

// A keeper in the library that stays (its target is taken) still sends the other library
// copies to duplicates/: the library keeps the song.
func TestMakePlanLibraryKeeperLeftLosersMove(t *testing.T) {
	cands := []Candidate{
		song(lib, "old/ABBA - SOS", "ABBA", "SOS", Quality{HasVideo: true}),
		song(lib, "x/ABBA - SOS", "ABBA", "SOS", Quality{}),
	}
	occ := func(rel string) (string, string, bool) {
		if NameKey(rel) == NameKey("A/ABBA - SOS") {
			return rel, "", true
		}
		return "", "", false
	}
	checkSummary(t, cands, MakePlan(cands, PlanOptions{Occupied: occ}), []string{
		"library:old/ABBA - SOS: left (" + reasonTargetExists + ")",
		"library:x/ABBA - SOS: duplicate <library:old/ABBA - SOS> (no video)",
	})
}
