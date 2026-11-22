package organize

import (
	"fmt"
	"reflect"
	"strings"
	"testing"
)

// The BPM and the BPM changes go into the hash with all their digits (not rounded to one).
func TestContentHashKeepsBPMPrecision(t *testing.T) {
	notes := tenNotes(": %d 2 5 la%d")
	if hashOf(t, chart("#BPM:300\n", notes...)) == hashOf(t, chart("#BPM:310\n", notes...)) {
		t.Error("BPM 300 and 310 give the same hash")
	}
	withB := func(bpm string) string {
		return chart("#BPM:300\n", append([]string{"B 100 " + bpm}, notes...)...)
	}
	if hashOf(t, withB("120")) == hashOf(t, withB("130")) {
		t.Error("BPM changes to 120 and 130 give the same hash")
	}
	if hashOf(t, withB("120")) != hashOf(t, withB("120")) {
		t.Error("the same chart gives two hashes")
	}
}

// Changes of the same beat are sorted by their BPM: their order in the file is no content.
func TestContentHashBPMChangesSameBeat(t *testing.T) {
	notes := tenNotes(": %d 2 5 la%d")
	a := chart("#BPM:300\n", append([]string{"B 100 200", "B 100 250"}, notes...)...)
	b := chart("#BPM:300\n", append([]string{"B 100 250", "B 100 200"}, notes...)...)
	if hashOf(t, a) == "" || hashOf(t, a) != hashOf(t, b) {
		t.Error("the order of two BPM changes at one beat changes the hash")
	}
}

// A chart as a duet: P1 sings late, P2 early, both start the same beat with other pitches.
func duetChart(bpm string) string {
	var lines []string
	lines = append(lines, "P1")
	for i := range 5 {
		lines = append(lines, fmt.Sprintf(": %d 2 %d a%d", 100+i*4, 5, i)) // 100.. late
	}
	lines = append(lines, "P2")
	for i := range 5 {
		lines = append(lines, fmt.Sprintf(": %d 2 %d b%d", 100+i*4, 8, i))
	}
	return chart("#BPM:"+bpm+"\n", lines...)
}

func TestNewProfileDuetOrderAndFirstNote(t *testing.T) {
	p := profileOf(t, duetChart("300"))
	if p == nil || len(p.Chart) != 10 {
		t.Fatalf("profile = %+v", p)
	}
	var players []int8
	for _, c := range p.Chart {
		players = append(players, c.Player)
	}
	if want := []int8{1, 1, 1, 1, 1, 2, 2, 2, 2, 2}; !reflect.DeepEqual(players, want) {
		t.Errorf("players = %v, want %v", players, want)
	}
	// The first note (P1's, the first with the smallest beat) is the base of the pitches.
	if p.Chart[0].Pitch != 0 || p.Chart[5].Pitch != 3 || p.Chart[0].Start != 0 || p.Chart[5].Start != 0 {
		t.Errorf("chart = %+v", p.Chart)
	}
}

// Notes of one singer with the same start keep their order in the file.
func TestNewProfileSameStartKeepsOrder(t *testing.T) {
	lines := []string{": 40 2 5 a", ": 40 4 7 b"}
	for i := 1; i < 9; i++ {
		lines = append(lines, fmt.Sprintf(": %d 2 5 c%d", 40+i*8, i))
	}
	p := profileOf(t, chart("#BPM:300\n", lines...))
	if p == nil || p.Chart[0].Len != 100 || p.Chart[1].Len != 200 || p.Chart[1].Pitch != 2 || p.Chart[0].Start != 0 {
		t.Fatalf("chart = %+v", p.Chart)
	}
}

// The start is relative to the first note (a first note off beat 0), also with BPM changes.
func TestNewProfileRelativeToFirstNote(t *testing.T) {
	var lines []string
	for i := range 10 {
		lines = append(lines, fmt.Sprintf(": %d 2 5 la%d", 100+i*4, i))
	}
	p := profileOf(t, chart("#BPM:300\n", lines...))
	if p == nil || p.Chart[0].Start != 0 || p.Chart[1].Start != 200 || p.Chart[9].Start != 1800 {
		t.Fatalf("chart = %+v", p.Chart)
	}
	q := profileOf(t, chart("#BPM:300\n#GAP:777\n", append([]string{"B 108 600"}, lines...)...))
	// Beats 100..108 at 300, then 600 BPM: 25 ms a beat.
	if q == nil || q.Chart[0].Start != 0 || q.Chart[2].Start != 400 || q.Chart[3].Start != 500 {
		t.Fatalf("chart with a change = %+v", q.Chart)
	}
	if q.Chart[4].Start != 600 {
		t.Errorf("note 4 at %d ms, want 600", q.Chart[4].Start)
	}
}

// The chart key is on a 50 ms grid for the start and the length.
func TestNewProfileChartKeyGrid(t *testing.T) {
	// 10 ms a beat (BPM 1500). The second note: start s beats, length l beats.
	key := func(s, l int) uint64 {
		lines := []string{": 0 1 5 a"}
		lines = append(lines, fmt.Sprintf(": %d %d 5 b", s, l))
		for i := range 8 {
			lines = append(lines, fmt.Sprintf(": %d 1 5 c%d", 1000+i*10, i))
		}
		p := profileOf(t, chart("#BPM:1500\n", lines...))
		if p == nil {
			t.Fatal("no profile")
		}
		return p.chartKey
	}
	if key(1, 1) != key(2, 1) {
		t.Error("starts 10 and 20 ms are on the same grid point")
	}
	if key(2, 1) == key(3, 1) {
		t.Error("starts 20 and 30 ms are on other grid points")
	}
	if key(10, 1) != key(10, 2) {
		t.Error("lengths 10 and 20 ms are on the same grid point")
	}
	if key(2, 2) == key(2, 3) {
		t.Error("lengths 20 and 30 ms are on other grid points")
	}
	if key(2, 2) != key(2, 2) {
		t.Error("the key is not stable")
	}
}

func TestArtistSimilarContainedNeedsThreeLetters(t *testing.T) {
	if !artistSimilar("Abc", "Abcdefgh") {
		t.Error("a name of three letters inside another is similar")
	}
	if artistSimilar("Ab", "Abcdefgh") {
		t.Error("a name of two letters inside another is not")
	}
}

// profileWith is a profile with the given shingles, and a chart key that matches nothing.
func profileWith(key uint64, from, to int) *Profile {
	p := &Profile{chartKey: key}
	for i := from; i < to; i++ {
		p.Shingles = append(p.Shingles, uint64(i))
	}
	return p
}

func similarCands(ps ...*Profile) ([]Candidate, []int) {
	var cands []Candidate
	var idx []int
	for i, p := range ps {
		c := song(hq, fmt.Sprint(i), "A", fmt.Sprint(i), Quality{})
		c.Profile = p
		cands = append(cands, c)
		idx = append(idx, i)
	}
	return cands, idx
}

// Exactly minShingles sequences are indexed; exactly half of the shorter lyrics shared is
// enough to compare, a single shared sequence of 20 is not.
func TestFindSimilarThresholds(t *testing.T) {
	cands, idx := similarCands(profileWith(1, 0, 20), profileWith(2, 0, 20))
	if got := findSimilar(cands, idx); len(got) != 1 {
		t.Errorf("two songs with exactly %d identical sequences: %d pairs, want 1", minShingles, len(got))
	}
	cands, idx = similarCands(profileWith(1, 0, 19), profileWith(2, 0, 19))
	if got := findSimilar(cands, idx); len(got) != 0 {
		t.Errorf("19 sequences: %d pairs, want none", len(got))
	}
	cands, idx = similarCands(profileWith(1, 0, 20), profileWith(2, 10, 30)) // 10 shared of 20
	if got := findSimilar(cands, idx); len(got) != 1 {
		t.Errorf("half shared: %d pairs, want 1", len(got))
	}
	cands, idx = similarCands(profileWith(1, 0, 20), profileWith(2, 11, 31)) // 9 of 20
	if got := findSimilar(cands, idx); len(got) != 0 {
		t.Errorf("9 of 20 shared: %d pairs, want none", len(got))
	}
	cands, idx = similarCands(profileWith(1, 0, 20), profileWith(2, 19, 39)) // 1 of 20
	if got := findSimilar(cands, idx); len(got) != 0 {
		t.Errorf("1 of 20 shared: %d pairs, want none", len(got))
	}
	// exactly commonShingle songs sharing a sequence still compare, one more do not
	var ps []*Profile
	for i := range commonShingle {
		ps = append(ps, profileWith(uint64(i), 0, 20))
	}
	cands, idx = similarCands(ps...)
	if got := findSimilar(cands, idx); len(got) != commonShingle*(commonShingle-1)/2 {
		t.Errorf("%d songs: %d pairs", commonShingle, len(got))
	}
}

// A possible-duplicates pair that the search did not find still gets its evidence from
// the profiles; a pair without a profile gets none.
func TestReviewSimilarPossibleWithProfiles(t *testing.T) {
	a, b := profileWith(1, 0, 20), profileWith(2, 100, 120)
	a.Chart = []chartNote{{Start: 0, Len: 10}}
	b.Chart = []chartNote{{Start: 0, Len: 10}}
	cands, _ := similarCands(a, b, nil)
	plan := Plan{PossibleDuplicates: [][]int{{0, 1}}}
	got := reviewSimilar(cands, plan, nil, func(int) bool { return true })
	if len(got) != 1 || !got[0].Possible || !strings.HasPrefix(got[0].Evidence, "same chart, but only 0 % of the lyrics alike") {
		t.Errorf("similar = %+v", got)
	}
	plan = Plan{PossibleDuplicates: [][]int{{0, 2}}}
	got = reviewSimilar(cands, plan, nil, func(int) bool { return true })
	if len(got) != 1 || got[0].Evidence != "" || !got[0].Possible {
		t.Errorf("similar without a profile = %+v", got)
	}
}
