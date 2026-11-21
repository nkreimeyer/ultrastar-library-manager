package organize

import (
	"fmt"
	"hash/fnv"
	"math"
	"sort"
	"strings"
	"unicode"

	"golang.org/x/text/unicode/norm"

	"github.com/nkreimeyer/library-manager/songtxt"
)

// The similarity check finds versions of a song whose txts are not identical (ContentHash)
// but clearly the same chart: lyrics with typo fixes, the whole song moved by octaves, a
// doubled #BPM with doubled beats, all beats shifted. Such versions are joined into one
// song when all of these hold (see similarJoin):
//
//   - both are duets or both are solo versions;
//   - at least joinLyrics of their lyrics match (Jaccard similarity of 4-word sequences);
//   - the charts match (chartMatch): same number of notes, and every note has the same
//     singer, the same pitch relative to the first note (within an octave), and a start
//     (from the first note) and length in milliseconds within chartToleranceMs;
//   - the artists are similar (artistSimilar);
//   - the titles name the same versions (variantWords: a "Live" or "Mix" version with the
//     chart of the studio version is listed for review, not joined).
//
// Weaker evidence never joins anything; it is listed for review (Plan.Similar).
const (
	joinLyrics       = 0.9
	reviewLyrics     = 0.6 // lyrics this alike are listed for review
	reviewContained  = 0.8 // or the shorter lyrics this much contained in the longer ones
	chartToleranceMs = 30
	shingleWords     = 4
	minShingles      = 20  // fewer 4-word sequences: too little text to compare
	commonShingle    = 50  // a sequence in more songs ("na na na na") says nothing
	chartGridMs      = 50  // grid of the chart index (only used to find candidates)
	shareForCompare  = 0.5 // a pair is compared if it shares this much of the shorter lyrics
)

// Profile is what the similarity check compares of a song.
type Profile struct {
	// Shingles are the hashes of all 4-word sequences of the lyrics, sorted, unique.
	Shingles []uint64
	// Chart holds the notes sorted by (singer, start): start and length in ms from the
	// first note, pitch relative to the first note modulo 12.
	Chart []chartNote
	// chartKey is a hash of the chart on a coarse grid, to find same-chart candidates.
	chartKey uint64
}

type chartNote struct {
	Start, Len int32
	Pitch      int8
	Player     int8
}

// NewProfile returns the profile of a song; nil if it has fewer than minContentNotes
// notes or no valid #BPM (the same songs that have no ContentHash).
func NewProfile(s *songtxt.Song) *Profile {
	if s == nil || len(s.Notes) < minContentNotes {
		return nil
	}
	bpm, err := songtxt.ParseNumber(tagValue(s, "BPM"))
	if err != nil || bpm <= 0 {
		return nil
	}
	notes := append([]songtxt.Note(nil), s.Notes...)
	if !s.IsDuet() {
		for i := range notes {
			notes[i].Player = 0
		}
	}
	sort.SliceStable(notes, func(a, b int) bool {
		if notes[a].Player != notes[b].Player {
			return notes[a].Player < notes[b].Player
		}
		return notes[a].Start < notes[b].Start
	})
	p := &Profile{Shingles: shingles(lyricWords(notes, s.Breaks))}

	// Times from the first note: #GAP drops out, and a doubled #BPM with doubled beats
	// gives exactly the same values.
	first := notes[0]
	for _, n := range notes {
		if n.Start < first.Start {
			first = n
		}
	}
	ms := func(beat int) float64 { return float64(beat-first.Start) * 15000 / bpm }
	if len(s.BPMChanges) > 0 {
		base, _ := s.BeatToMs(float64(first.Start))
		ms = func(beat int) float64 { v, _ := s.BeatToMs(float64(beat)); return v - base }
	}
	h := fnv.New64a()
	for _, n := range notes {
		start, end := ms(n.Start), ms(n.Start+n.Length)
		c := chartNote{
			Start: int32(math.Round(start)), Len: int32(math.Round(end - start)),
			Pitch: int8(((n.Pitch-first.Pitch)%12 + 12) % 12), Player: int8(n.Player),
		}
		p.Chart = append(p.Chart, c)
		fmt.Fprintf(h, "%d %d %d %d|", c.Player, c.Pitch, int(math.Round(start/chartGridMs)), int(math.Round((end-start)/chartGridMs)))
	}
	p.chartKey = h.Sum64()
	return p
}

// lyricWords returns the sung words: the syllables of each singer in order, a line break
// ending a word (many txts start a new line without a space), folded to lower-case letters
// and digits.
func lyricWords(notes []songtxt.Note, breaks []songtxt.LineBreak) []string {
	var b strings.Builder
	for i, n := range notes {
		if i > 0 && (notes[i-1].Player != n.Player || breakBetween(breaks, n.Player, notes[i-1].Start, n.Start)) {
			b.WriteByte(' ')
		}
		b.WriteString(n.Text)
	}
	text := strings.ToLower(norm.NFC.String(strings.ReplaceAll(b.String(), "~", "")))
	return strings.FieldsFunc(text, func(r rune) bool { return !unicode.IsLetter(r) && !unicode.IsDigit(r) })
}

// breakBetween reports whether a line break of the player lies in (from, to].
func breakBetween(breaks []songtxt.LineBreak, player, from, to int) bool {
	for _, br := range breaks {
		// A file with a single player marker has its notes counted as player 0 (see
		// NewProfile), its breaks keep the marker: accept both.
		if (br.Player == player || player == 0) && br.Beat > from && br.Beat <= to {
			return true
		}
	}
	return false
}

func shingles(words []string) []uint64 {
	var out []uint64
	for i := 0; i+shingleWords <= len(words); i++ {
		h := fnv.New64a()
		h.Write([]byte(strings.Join(words[i:i+shingleWords], " ")))
		out = append(out, h.Sum64())
	}
	sort.Slice(out, func(a, b int) bool { return out[a] < out[b] })
	return uniq(out)
}

func uniq(s []uint64) []uint64 {
	out := s[:0]
	for i, v := range s {
		if i == 0 || v != s[i-1] {
			out = append(out, v)
		}
	}
	return out
}

// lyricsSimilarity returns the Jaccard similarity of the shingle sets and how much of the
// smaller set is contained in the larger one.
func lyricsSimilarity(a, b *Profile) (jaccard, contained float64) {
	x, y := a.Shingles, b.Shingles
	if len(x) == 0 || len(y) == 0 {
		return 0, 0
	}
	common := 0
	for i, j := 0, 0; i < len(x) && j < len(y); {
		switch {
		case x[i] == y[j]:
			common++
			i++
			j++
		case x[i] < y[j]:
			i++
		default:
			j++
		}
	}
	return float64(common) / float64(len(x)+len(y)-common), float64(common) / float64(min(len(x), len(y)))
}

// chartMatch reports whether two charts are the same within chartToleranceMs.
func chartMatch(a, b *Profile) bool {
	if len(a.Chart) != len(b.Chart) || len(a.Chart) == 0 {
		return false
	}
	for i, x := range a.Chart {
		y := b.Chart[i]
		if x.Player != y.Player || x.Pitch != y.Pitch || abs32(x.Start-y.Start) > chartToleranceMs || abs32(x.Len-y.Len) > chartToleranceMs {
			return false
		}
	}
	return true
}

func abs32(v int32) int32 {
	if v < 0 {
		return -v
	}
	return v
}

// artistSimilar reports whether two #ARTIST values plausibly name the same artist:
// equal ignoring case, accents, spaces and punctuation, one contained in the other
// ("Glee" / "Glee Cast", "Disney's Aladdin" / "Aladdin"), or a small edit distance
// (a typo: at most a quarter of the longer name).
func artistSimilar(a, b string) bool {
	x, y := looseName(a), looseName(b)
	if x == "" || y == "" {
		return false
	}
	if x == y {
		return true
	}
	if len([]rune(x)) > len([]rune(y)) {
		x, y = y, x
	}
	if len([]rune(x)) >= 3 && strings.Contains(y, x) {
		return true
	}
	return levenshtein(x, y) <= max(1, len([]rune(y))/4)
}

// looseName is a name as SongKey compares it (normWords), without spaces.
func looseName(s string) string { return strings.Join(normWords(s), "") }

func levenshtein(a, b string) int {
	x, y := []rune(a), []rune(b)
	prev := make([]int, len(y)+1)
	cur := make([]int, len(y)+1)
	for j := range prev {
		prev[j] = j
	}
	for i := 1; i <= len(x); i++ {
		cur[0] = i
		for j := 1; j <= len(y); j++ {
			cost := 1
			if x[i-1] == y[j-1] {
				cost = 0
			}
			cur[j] = min(prev[j]+1, cur[j-1]+1, prev[j-1]+cost)
		}
		prev, cur = cur, prev
	}
	return prev[len(y)]
}

// similarPair is a pair of candidates (A < B) found by the similarity check.
type similarPair struct {
	A, B       int
	Jaccard    float64
	Contained  float64
	ChartMatch bool
}

// findSimilar returns the candidate pairs among idx (working candidates with a profile)
// worth comparing, compared: pairs sharing at least shareForCompare of the shorter lyrics
// (an inverted index over the shingles, ignoring very common ones), and pairs with the
// same coarse chart key. Duets and solo versions are never paired. Sorted by (A, B).
func findSimilar(cands []Candidate, idx []int) []similarPair {
	posting := map[uint64][]int{}
	byChart := map[uint64][]int{}
	for _, i := range idx {
		p := cands[i].Profile
		if len(p.Shingles) >= minShingles {
			for _, s := range p.Shingles {
				posting[s] = append(posting[s], i)
			}
		}
		byChart[p.chartKey] = append(byChart[p.chartKey], i)
	}
	shared := map[[2]int]int{}
	for _, list := range posting {
		if len(list) < 2 || len(list) > commonShingle {
			continue
		}
		for x := range list {
			for y := x + 1; y < len(list); y++ {
				shared[pairKey(list[x], list[y])]++
			}
		}
	}
	check := map[[2]int]bool{}
	for k, n := range shared {
		a, b := cands[k[0]].Profile, cands[k[1]].Profile
		if float64(n) >= shareForCompare*float64(min(len(a.Shingles), len(b.Shingles))) {
			check[k] = true
		}
	}
	for _, list := range byChart {
		for x := range list {
			for y := x + 1; y < len(list); y++ {
				check[pairKey(list[x], list[y])] = true
			}
		}
	}
	var out []similarPair
	for k := range check {
		a, b := &cands[k[0]], &cands[k[1]]
		if a.Duet != b.Duet {
			continue
		}
		j, c := lyricsSimilarity(a.Profile, b.Profile)
		out = append(out, similarPair{A: k[0], B: k[1], Jaccard: j, Contained: c, ChartMatch: chartMatch(a.Profile, b.Profile)})
	}
	sort.Slice(out, func(x, y int) bool {
		if out[x].A != out[y].A {
			return out[x].A < out[y].A
		}
		return out[x].B < out[y].B
	})
	return out
}

func pairKey(a, b int) [2]int {
	if a > b {
		a, b = b, a
	}
	return [2]int{a, b}
}

// similarJoin reports whether the pair is one song (see the rules at the top).
func similarJoin(cands []Candidate, p similarPair) bool {
	return p.ChartMatch && p.Jaccard >= joinLyrics && artistSimilar(cands[p.A].Artist, cands[p.B].Artist) &&
		variantWords(cands[p.A].Title) == variantWords(cands[p.B].Title)
}

// variantWords returns the version words of a title (versionWords without the generic
// "version", "edit" and "radio"), sorted and joined: "live", "mix", "acoustic live", ...
func variantWords(title string) string {
	seen := map[string]bool{}
	var out []string
	for _, w := range normWords(title) {
		if versionWords[w] && w != "version" && w != "edit" && w != "radio" && !seen[w] {
			seen[w] = true
			out = append(out, w)
		}
	}
	sort.Strings(out)
	return strings.Join(out, " ")
}

// evidence describes a pair for the review list; "" if it is too weak to list. A pair of
// unrelated artists without the same chart (a cover version) is not listed.
func (p similarPair) evidence(cands []Candidate) string {
	pct := func(f float64) int { return int(math.Floor(f * 100)) }
	similarArtist := artistSimilar(cands[p.A].Artist, cands[p.B].Artist)
	switch {
	case p.ChartMatch && p.Jaccard >= joinLyrics && similarArtist:
		return fmt.Sprintf("same chart, lyrics %d %% alike, but the titles name other versions (%q vs %q)", pct(p.Jaccard),
			variantWords(cands[p.A].Title), variantWords(cands[p.B].Title))
	case p.ChartMatch && p.Jaccard >= joinLyrics:
		return fmt.Sprintf("same chart, lyrics %d %% alike, but other artists (an alias, a placeholder artist, or a cover that reuses the chart)", pct(p.Jaccard))
	case p.ChartMatch && p.Jaccard >= reviewLyrics:
		return fmt.Sprintf("same chart, lyrics %d %% alike (below the %d %% needed to join them)", pct(p.Jaccard), pct(joinLyrics))
	case p.ChartMatch:
		return fmt.Sprintf("same chart, but only %d %% of the lyrics alike (a parody, translation or rewrite?)", pct(p.Jaccard))
	case !similarArtist:
		return ""
	case p.Jaccard >= joinLyrics:
		return fmt.Sprintf("lyrics %d %% alike, other chart (re-timed or charted anew)", pct(p.Jaccard))
	case p.Contained >= reviewContained && p.Jaccard < reviewLyrics:
		return fmt.Sprintf("the shorter lyrics are %d %% contained in the longer ones (a shorter cut: TV size, edit?)", pct(p.Contained))
	case p.Jaccard >= reviewLyrics:
		return fmt.Sprintf("lyrics %d %% alike, other chart", pct(p.Jaccard))
	}
	return ""
}

// weakEvidence describes a pair of a possible-duplicates group that has no content
// evidence, so the reviewer sees at a glance that its versions differ.
func (p similarPair) weakEvidence() string {
	if p.Jaccard == 0 {
		return "no lyrics in common: different versions"
	}
	return fmt.Sprintf("only %d %% of the lyrics alike: probably different versions", int(math.Floor(p.Jaccard*100)))
}

// reviewSimilar builds Plan.Similar: the pairs of keepers of different songs with evidence,
// and every pair inside a possible-duplicates group (with the weak evidence if there is no
// better one, or "" if a song has no profile).
func reviewSimilar(cands []Candidate, p Plan, pairs []similarPair, isKeeper func(int) bool) []Similar {
	possible := map[[2]int]bool{}
	for _, g := range p.PossibleDuplicates {
		for x := range g {
			for y := x + 1; y < len(g); y++ {
				possible[pairKey(g[x], g[y])] = true
			}
		}
	}
	found := map[[2]int]similarPair{}
	for _, sp := range pairs {
		found[[2]int{sp.A, sp.B}] = sp
	}
	var out []Similar
	for _, sp := range pairs {
		k := [2]int{sp.A, sp.B}
		if !isKeeper(sp.A) || !isKeeper(sp.B) || possible[k] {
			continue
		}
		if e := sp.evidence(cands); e != "" {
			out = append(out, Similar{A: sp.A, B: sp.B, Evidence: e})
		}
	}
	for k := range possible {
		sp, ok := found[k]
		if !ok {
			a, b := cands[k[0]].Profile, cands[k[1]].Profile
			if a == nil || b == nil {
				out = append(out, Similar{A: k[0], B: k[1], Possible: true})
				continue
			}
			sp = similarPair{A: k[0], B: k[1], ChartMatch: chartMatch(a, b)}
			sp.Jaccard, sp.Contained = lyricsSimilarity(a, b)
		}
		e := sp.evidence(cands)
		if e == "" {
			e = sp.weakEvidence()
		}
		out = append(out, Similar{A: k[0], B: k[1], Evidence: e, Possible: true})
	}
	sort.Slice(out, func(x, y int) bool {
		if out[x].A != out[y].A {
			return out[x].A < out[y].A
		}
		return out[x].B < out[y].B
	})
	return out
}
