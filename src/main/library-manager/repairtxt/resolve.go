package repairtxt

import (
	"sort"
	"strings"
	"unicode"
)

// level is one source of evidence. Levels are asked in order; the first that decides wins.
type level struct {
	name  string
	lexes []*langLex
	// min is the count the best candidate needs, and ratio how many times more often than
	// the second best it must occur, for the level to decide.
	min   int
	ratio float64
}

// Candidate is one possible repair of a damaged word with its count in the deciding (or
// last asked) level.
type Candidate struct {
	Form  string `json:"form"`
	Count int    `json:"count"`
}

// resolution is the outcome for one damaged word.
type resolution struct {
	form   []rune // the repaired lower-case word; nil if unresolved
	source string // name of the deciding level
	weak   bool   // decided without the usual evidence (see resolveWord)
	cands  []Candidate
}

func sortedCandidates(m map[string]int) []Candidate {
	c := make([]Candidate, 0, len(m))
	for f, n := range m {
		c = append(c, Candidate{f, n})
	}
	sort.Slice(c, func(i, j int) bool {
		if c[i].Count != c[j].Count {
			return c[i].Count > c[j].Count
		}
		return c[i].Form < c[j].Form
	})
	return c
}

// apostropheFold maps every apostrophe-like rune to '\”.
func apostropheFold(s string) string {
	return strings.Map(func(r rune) rune {
		if isApostrophe(r) {
			return '\''
		}
		return r
	}, s)
}

// sameButApostrophes reports whether all candidates differ only in the kind of apostrophe
// ("I’m" vs "I´m"): which one the byte was does not matter to a singer.
func sameButApostrophes(c []Candidate) bool {
	for _, x := range c[1:] {
		if apostropheFold(x.Form) != apostropheFold(c[0].Form) {
			return false
		}
	}
	return true
}

// resolveWord finds what the damaged lower-case word tok stood for. Each level is asked
// for the candidates (restricted to the ones still plausible after an undecided earlier
// level). A level decides if its best candidate reaches min and occurs ratio times as
// often as the second. If no level decides, a single remaining candidate, or candidates
// that differ only in the apostrophe, still give a weak result.
func resolveWord(tok []rune, class byte, levels []level) resolution {
	var pool map[string]bool // nil = any form
	var last []Candidate
	for _, lv := range levels {
		m := map[string]int{}
		for _, l := range lv.lexes {
			l.candidates(tok, class, m)
		}
		if pool != nil {
			for f := range m {
				if !pool[f] {
					delete(m, f)
				}
			}
		}
		if len(m) == 0 {
			continue
		}
		c := sortedCandidates(m)
		last = c
		second := 0
		if len(c) > 1 {
			second = c[1].Count
		}
		if c[0].Count >= lv.min && float64(c[0].Count) >= lv.ratio*float64(second) {
			return resolution{form: []rune(c[0].Form), source: lv.name, cands: c}
		}
		pool = map[string]bool{}
		for _, x := range c {
			if float64(x.Count)*lv.ratio >= float64(c[0].Count) {
				pool[x.Form] = true
			}
		}
	}
	if len(last) == 0 {
		return resolution{}
	}
	var plausible []Candidate
	for _, x := range last {
		if pool == nil || pool[x.Form] {
			plausible = append(plausible, x)
		}
	}
	if len(plausible) == 1 || (len(plausible) > 1 && sameButApostrophes(plausible)) {
		return resolution{form: []rune(plausible[0].Form), source: "weak", weak: true, cands: plausible}
	}
	return resolution{cands: plausible}
}

// caseRule holds what is needed to restore the case of a repaired character.
type caseRule struct {
	// startUpper: a word that starts a lyric line (or any word of a title-case tag value)
	// starts with an upper-case letter.
	startUpper bool
	// local is the song's own lexicon (its lyrics, file names and undamaged words); its
	// mid-line case of a word wins over the global corpus's.
	local  *langLex
	global *langLex
}

// restoreCase returns the repaired rune for position i of the damaged word tok (original
// case), given the lower-case repaired form:
//   - in an all-caps word (two or more other letters, all upper case) it is upper case;
//   - as the first letter of a word that starts a line it follows the line convention;
//   - as the first letter of another word it is upper case if the song's own lexicon, or
//     else the corpus, mostly writes the word that way in the middle of a line (German
//     nouns, "of Ísland" in the lyrics);
//   - otherwise it is lower case.
//
// An upper case no codepage byte stands for (µ → U+039C, ƒ → U+0191) is never used.
func (cr caseRule) restoreCase(tok, form []rune, i int, lineStart bool) rune {
	r := form[i]
	if !unicode.IsLetter(r) {
		return r
	}
	letters, upper := 0, 0
	for _, x := range tok {
		if x != replChar && unicode.IsLetter(x) {
			letters++
			if unicode.IsUpper(x) {
				upper++
			}
		}
	}
	if letters >= 2 && upper == letters {
		return upperAllowed(r)
	}
	first := 0
	for first < len(tok) && isApostrophe(tok[first]) {
		first++
	}
	if i != first {
		return r
	}
	if lineStart {
		if cr.startUpper {
			return upperAllowed(r)
		}
		return r
	}
	for _, l := range []*langLex{cr.local, cr.global} {
		if l == nil {
			continue
		}
		if e := l.words[string(form)]; e != nil && e.mid > 0 {
			if 2*e.midUpper > e.mid {
				return upperAllowed(r)
			}
			return r
		}
	}
	return r
}

// upperAllowed returns the upper case of r if a lost byte can stand for it, else r.
func upperAllowed(r rune) rune {
	if u := unicode.ToUpper(r); allowedRune[u] {
		return u
	}
	return r
}

// bestPunct chooses the punctuation character for a U+FFFD at the edge of a word, in
// context class c: the most frequent one in that class of the first lexicon set that has
// at least 3 occurrences and 3 times as many as the second. Apostrophes are left out: a
// word with an edge apostrophe ("goin’") is found as a word if the corpus knows it.
func bestPunct(c byte, sets [][]*langLex) (rune, bool) {
	for _, lexes := range sets {
		m := map[rune]int{}
		for _, l := range lexes {
			for p, n := range l.punct {
				if p.class == c && !isApostrophe(p.r) {
					m[p.r] += n
				}
			}
		}
		var best, second int
		var br rune
		for r, n := range m {
			switch {
			case n > best || (n == best && r < br):
				second = max(second, best)
				best, br = n, r
			case n > second:
				second = n
			}
		}
		if best >= 3 && best >= 3*second {
			return br, true
		}
	}
	return 0, false
}
