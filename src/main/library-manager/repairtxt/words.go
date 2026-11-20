package repairtxt

import (
	"slices"
	"sort"
	"strings"
	"unicode"

	"github.com/nkreimeyer/library-manager/songtxt"
)

// More level names, as they appear in the report.
const (
	srcContext  = "context"  // the neighbouring words (corpus bigrams)
	srcSpelling = "spelling" // character 5-/4-grams of the language (a word no list has)
)

// outcome is the repair of one damaged word: one rune per U+FFFD, in order.
type outcome struct {
	repl   []rune
	form   []rune // the repaired word in lower case (nil for punctuation-only repairs)
	source string
	weak   bool
	// suggest: too little evidence to apply it; the report lists it as a suggestion.
	suggest bool
	cands   []Candidate
}

// wordResolver resolves the damaged words of one file.
type wordResolver struct {
	p         *planner
	levels    []level
	langLexes []*langLex // the song's languages (may be empty)
	local     *langLex
	cache     map[string]resolution
	spanish   bool // one of the song's languages is Spanish (¿ ¡)
}

func (wr *wordResolver) ctxLexes() []*langLex {
	if len(wr.langLexes) > 0 {
		return wr.langLexes
	}
	return []*langLex{wr.p.lex.global()}
}

// word is resolveWord with a cache; a corpus-wide answer for a song with known languages
// is weak (a word its languages have never seen).
func (wr *wordResolver) word(lw []rune, class byte) resolution {
	key := string(lw) + "\x00" + string(rune(class))
	if r, ok := wr.cache[key]; ok {
		return r
	}
	r := resolveWord(lw, class, wr.levels)
	if r.form != nil && !r.weak && r.source == srcCorpus && len(wr.langLexes) > 0 {
		r.weak = true
	}
	wr.cache[key] = r
	return r
}

// count returns how often the repl-free lower-case word occurs in the song, the corpus or
// a word list.
func (wr *wordResolver) count(w []rune) int {
	f := string(lowerRunes(w))
	n := wr.local.count(f) + wr.p.lex.global().count(f)
	for _, l := range wr.p.wordlists {
		n += l.count(f)
	}
	return n
}

// withCase turns a lower-case form for w into an outcome.
func withCase(cr caseRule, w, form []rune, lineStart bool, source string, weak bool) outcome {
	o := outcome{form: form, source: source, weak: weak}
	for i, r := range w {
		if r == replChar {
			o.repl = append(o.repl, cr.restoreCase(w, form, i, lineStart))
		}
	}
	return o
}

// resolveToken repairs the damaged word w:
//  1. as a whole word (the levels; the corpus also knows words with their edge
//     apostrophes and punctuation, "goin’", "¡oh"), weak or not;
//  2. ambiguous candidates: "¿" vs "¡" by the end of the line, else the neighbouring
//     words (prev, next: lower case);
//  3. without any candidate: its parts at an apostrophe ("d'�tre") or the spelling model
//     (a weak spelling is only a suggestion); else, if no letter forms a known 4-gram
//     there, punctuation at its edge.
//
// rest is the text of the line after the word.
func (wr *wordResolver) resolveToken(w []rune, cr caseRule, lineStart bool, class byte, prev, next, rest string) outcome {
	lw := lowerRunes(w)
	res := wr.word(lw, class)
	switch {
	case res.form != nil && !res.weak:
		return withCase(cr, w, res.form, lineStart, res.source, false)
	case res.form != nil:
		return withCase(cr, w, res.form, lineStart, res.source, true)
	case len(res.cands) > 1:
		if f, ok := invertedChoice(res.cands, rest); ok {
			return withCase(cr, w, []rune(f), lineStart, srcContext, false)
		}
		if f, ok := wr.byContext(res.cands, prev, next); ok {
			return withCase(cr, w, []rune(f), lineStart, srcContext, false)
		}
		return outcome{cands: res.cands}
	}
	// No candidate at all: the word's parts or its spelling (a weak spelling is only a
	// suggestion), else punctuation at its edge if no letter fits there.
	if o, ok := wr.core(w, cr, lineStart); ok {
		return o
	}
	if !wr.letterEvidence(w) {
		if po, ok := wr.edgePunct(w, cr, lineStart, rest); ok {
			return po
		}
	}
	return outcome{cands: res.cands}
}

// letterEvidence reports whether some letter at a U+FFFD of w forms a 4-gram the corpus
// knows (at least 3 times): then the U+FFFD may well be a letter of an unknown word, and
// it is not taken for punctuation.
func (wr *wordResolver) letterEvidence(w []rune) bool {
	padded := append(append([]rune{'^'}, lowerRunes(w)...), '$')
	for pi := 1; pi < len(padded)-1; pi++ {
		if padded[pi] != replChar {
			continue
		}
		for _, lexes := range [][]*langLex{wr.ctxLexes(), {wr.p.lex.global()}} {
			if best, _, _, ok := spellAt(padded, pi, 4, lexes); ok && best >= 3 {
				return true
			}
		}
	}
	return false
}

// fill returns w with its U+FFFDs replaced by repl.
func fill(w, repl []rune) string {
	out := []rune(string(w))
	k := 0
	for i, r := range out {
		if r == replChar && k < len(repl) {
			out[i] = repl[k]
			k++
		}
	}
	return string(out)
}

// edgePunct treats a single U+FFFD at the start and/or end of w as punctuation when the
// rest of w is a word that occurs at least 3 times (or is repaired with confidence).
func (wr *wordResolver) edgePunct(w []rune, cr caseRule, lineStart bool, rest string) (outcome, bool) {
	a, b := 0, len(w)
	if w[0] == replChar {
		a = 1
	}
	if b > a && w[b-1] == replChar {
		b--
	}
	if (a == 0 && b == len(w)) || a >= b || w[a] == replChar || w[b-1] == replChar {
		return outcome{}, false
	}
	sets := [][]*langLex{wr.langLexes, {wr.p.lex.global()}}
	o := outcome{source: srcPunct}
	if a == 1 {
		r, ok := wr.invertedMark(rest)
		if !ok {
			r, ok = bestPunct(classLeading, sets)
		}
		if !ok {
			return outcome{}, false
		}
		o.repl = append(o.repl, r)
	}
	core := w[a:b]
	if hasRepl(core) {
		res := wr.word(lowerRunes(core), 0)
		var co outcome
		if res.form != nil && !res.weak {
			co = withCase(cr, core, res.form, lineStart && a == 0, res.source, false)
		} else if c, ok := wr.core(core, cr, lineStart && a == 0); ok && !c.weak {
			co = c
		} else {
			return outcome{}, false
		}
		o.repl = append(o.repl, co.repl...)
	} else if wr.count(core) < 3 {
		return outcome{}, false
	}
	if b < len(w) {
		r, ok := bestPunct(classTrailing, sets)
		if !ok {
			return outcome{}, false
		}
		o.repl = append(o.repl, r)
	}
	return o, true
}

// invertedChoice decides between candidates that differ only in a leading "¿" or "¡"
// by the "?" or "!" that closes the line.
func invertedChoice(cands []Candidate, rest string) (string, bool) {
	r, ok := invertedFor(rest)
	if !ok {
		return "", false
	}
	want := string(r)
	var keep []string
	for _, c := range cands {
		f := []rune(c.Form)
		if f[0] != '¿' && f[0] != '¡' {
			return "", false
		}
		if string(f[0]) == want {
			keep = append(keep, c.Form)
		}
	}
	if len(keep) != 1 {
		return "", false
	}
	return keep[0], true
}

// invertedFor returns "¿" if a "?" comes first in rest, "¡" if a "!" does.
func invertedFor(rest string) (rune, bool) {
	q, e := strings.IndexByte(rest, '?'), strings.IndexByte(rest, '!')
	switch {
	case q >= 0 && (e < 0 || q < e):
		return '¿', true
	case e >= 0:
		return '¡', true
	}
	return 0, false
}

// invertedMark returns "¿" or "¡" for a damaged word that opens a question or an
// exclamation ("�Qué …?") in a Spanish song.
func (wr *wordResolver) invertedMark(rest string) (rune, bool) {
	if !wr.spanish {
		return 0, false
	}
	return invertedFor(rest)
}

// byContext chooses between candidates by the corpus bigrams with the neighbouring words.
func (wr *wordResolver) byContext(cands []Candidate, prev, next string) (string, bool) {
	if prev == "" && next == "" {
		return "", false
	}
	// The song's own neighbours (its undamaged lines, its lyrics) decide first: if only one
	// candidate has them ("För jag tror" in the lyrics), it wins over the corpus's pairs
	// ("får jag" is common). Otherwise they count twice next to the corpus's.
	local := map[string]int{}
	for _, c := range cands {
		if prev != "" {
			local[c.Form] += wr.local.bigrams[bigramKey(prev, c.Form)]
		}
		if next != "" {
			local[c.Form] += wr.local.bigrams[bigramKey(c.Form, next)]
		}
	}
	if s := sortedCandidates(local); s[0].Count > 0 && (len(s) == 1 || s[1].Count == 0) {
		return s[0].Form, true
	}
	m := map[string]int{}
	for _, c := range cands {
		n := 2 * local[c.Form]
		for _, l := range wr.ctxLexes() {
			if prev != "" {
				n += l.bigrams[bigramKey(prev, c.Form)]
			}
			if next != "" {
				n += l.bigrams[bigramKey(c.Form, next)]
			}
		}
		m[c.Form] = n
	}
	s := sortedCandidates(m)
	second := 0
	if len(s) > 1 {
		second = s[1].Count
	}
	if s[0].Count >= 2 && s[0].Count >= 3*second {
		return s[0].Form, true
	}
	return "", false
}

// core repairs a damaged word that no list knows as a whole (step 5 of resolveToken).
func (wr *wordResolver) core(w []rune, cr caseRule, lineStart bool) (outcome, bool) {
	// Parts at an intact apostrophe: "d'�tre" = "d'" + "être", "qu'�a" = "qu'" + "ça".
	if i := strings.IndexFunc(string(w), isApostrophe); i >= 0 {
		var o outcome
		weak := false
		start := 0
		for k := 0; k <= len(w); k++ {
			if k < len(w) && !isApostrophe(w[k]) {
				continue
			}
			part := w[start:k]
			if hasRepl(part) {
				res := wr.word(lowerRunes(part), 0)
				var po outcome
				switch {
				case res.form != nil:
					po = withCase(cr, part, res.form, lineStart && start == 0, res.source, res.weak)
				default:
					sp, ok := wr.spell(part, cr, lineStart && start == 0)
					if !ok {
						return outcome{}, false
					}
					po = sp
				}
				weak = weak || po.weak
				o.suggest = o.suggest || po.suggest
				if o.source == "" {
					o.source = po.source
				}
				o.repl = append(o.repl, po.repl...)
			}
			start = k + 1
		}
		if o.source != "" {
			o.weak = weak
			return o, true
		}
	}
	return wr.spell(w, cr, lineStart)
}

// spellLetters are the candidate runes of the spelling model: lower-case letters (and
// caseless ones) a lost byte can stand for.
var spellLetters = func() []rune {
	var rs []rune
	for r := range allowedRune {
		if unicode.IsLetter(r) && !unicode.IsUpper(r) {
			rs = append(rs, r)
		}
	}
	sort.Slice(rs, func(i, j int) bool { return rs[i] < rs[j] })
	return rs
}()

// spell repairs the U+FFFDs of w one after the other, left to right, with the character
// n-grams of the words: each candidate letter scores the counts of the 5-grams around it
// (with "^"/"$" at the word boundaries; n-grams over another unrepaired U+FFFD are not
// used), or of the 4-grams if no 5-gram is known. The song's languages are asked first,
// then all languages. The best letter needs 3 or more and 10 times the second; below 20,
// or from all languages while the song's languages are known, the repair is weak.
func (wr *wordResolver) spell(w []rune, cr caseRule, lineStart bool) (outcome, bool) {
	lw := lowerRunes(w)
	padded := append(append([]rune{'^'}, lw...), '$')
	weak := false
	// The song's own words first ("höher" in its lyrics spells "H�he"): a letter decides if
	// two different 5-grams around the gap (four letters of context each) support it and
	// none supports another letter (localSpellAt). The song is too small for counts or
	// 4-grams ("très" would spell "dor�s" as "dorès"); otherwise the corpus decides.
	sets := [][]*langLex{{wr.local}, wr.langLexes, {wr.p.lex.global()}}
	for pi := 1; pi < len(padded)-1; pi++ {
		if padded[pi] != replChar {
			continue
		}
		decided := false
	sets:
		for si, lexes := range sets {
			if len(lexes) == 0 || lexes[0] == nil {
				continue
			}
			for n := 5; n >= 4; n-- {
				best, second, br, ok := spellAt(padded, pi, n, lexes)
				if !ok {
					return outcome{}, false
				}
				if si == 0 {
					if n < 5 {
						continue
					}
					if r, ok := localSpellAt(padded, pi, n, wr.local); ok {
						padded[pi] = r
						decided = true
						break sets
					}
					continue
				}
				if best < 3 {
					continue // nothing known at this length
				}
				if best < 10*second {
					return outcome{}, false
				}
				if best < 20 || (si > 1 && len(wr.langLexes) > 0) {
					weak = true
				}
				padded[pi] = br
				decided = true
				break sets
			}
		}
		if !decided {
			return outcome{}, false
		}
	}
	o := withCase(cr, w, padded[1:len(padded)-1], lineStart, srcSpelling, weak)
	o.suggest = weak
	return o, true
}

// spellAt scores the candidate letters for padded[pi] with the n-grams of lexes. ok is
// false if every n-gram around pi crosses another U+FFFD.
func spellAt(padded []rune, pi, n int, lexes []*langLex) (best, second int, br rune, ok bool) {
	for _, c := range spellLetters {
		padded[pi] = c
		score, used := 0, false
		for s := pi - n + 1; s <= pi; s++ {
			if s < 0 || s+n > len(padded) || hasRepl(padded[s:s+n]) {
				continue
			}
			used = true
			g := string(padded[s : s+n])
			for _, l := range lexes {
				score += l.grams[g]
			}
		}
		if !used {
			padded[pi] = replChar
			return 0, 0, 0, false
		}
		switch {
		case score > best:
			second, best, br = best, score, c
		case score > second:
			second = score
		}
	}
	padded[pi] = replChar
	return best, second, br, true
}

// localSpellAt returns the one letter for padded[pi] that at least two different n-grams
// of l around pi support, if no n-gram there supports another letter.
func localSpellAt(padded []rune, pi, n int, l *langLex) (rune, bool) {
	var found rune
	windows := 0
	for _, c := range spellLetters {
		padded[pi] = c
		k := 0
		for s := pi - n + 1; s <= pi; s++ {
			if s >= 0 && s+n <= len(padded) && !hasRepl(padded[s:s+n]) && l.grams[string(padded[s:s+n])] > 0 {
				k++
			}
		}
		if k > 0 {
			if found != 0 {
				padded[pi] = replChar
				return 0, false
			}
			found, windows = c, k
		}
	}
	padded[pi] = replChar
	return found, windows >= 2
}

// hyphenJoined reports whether rs (the text between two words) is a single "-" with
// optional spaces: a word split into syllables with hyphens ("jüng - ste").
func hyphenJoined(rs []rune) bool {
	n := 0
	for _, r := range rs {
		switch {
		case r == '-':
			n++
		case r != ' ':
			return false
		}
	}
	return n == 1
}

// joinHyphenated repairs words still left that are part of a hyphen chain ("l� - cher -
// lich"): the windows of the chain that hold the word are looked up as one word without
// the hyphens, the longest first. A window is used only if all its damaged words are
// still left and the joined word is known and decided (not weak, not the spelling model).
func (wr *wordResolver) joinHyphenated(seg segment, toks []token, outs []outcome, cr caseRule) {
	left := func(i int) bool {
		return hasRepl(seg.runes[toks[i].start:toks[i].end]) && (outs[i].repl == nil || outs[i].suggest)
	}
	for i := range toks {
		if !left(i) {
			continue
		}
		lo, hi := i, i // the chain around i
		for lo > 0 && !toks[lo].lineStart && hyphenJoined(seg.runes[toks[lo-1].end:toks[lo].start]) {
			lo--
		}
		for hi+1 < len(toks) && !toks[hi+1].lineStart && hyphenJoined(seg.runes[toks[hi].end:toks[hi+1].start]) {
			hi++
		}
	windows:
		for size := min(hi-lo+1, 4); size >= 2; size-- {
			for a := max(lo, i-size+1); a <= i && a+size-1 <= hi; a++ {
				b := a + size - 1
				var joined []rune
				for k := a; k <= b; k++ {
					w := seg.runes[toks[k].start:toks[k].end]
					if hasRepl(w) && !left(k) {
						continue windows
					}
					joined = append(joined, w...)
				}
				// Only a known word counts: letter n-grams across a syllable boundary
				// mislead ("j�ng - ste" would become "jängste" after "Angst").
				res := wr.word(lowerRunes(joined), 0)
				if res.form == nil || res.weak {
					continue
				}
				o := withCase(cr, joined, res.form, toks[a].lineStart, res.source, false)
				// Hand the repaired runes back to the words of the window, in order.
				r := o.repl
				for k := a; k <= b; k++ {
					n := strings.Count(string(seg.runes[toks[k].start:toks[k].end]), string(replChar))
					if n > 0 {
						outs[k] = outcome{repl: r[:n], source: o.source + " (hyphenated)"}
					}
					r = r[n:]
				}
				break windows
			}
		}
	}
}

// singleClass returns the punctuation class of a word that is a single U+FFFD, or 0.
func singleClass(seg segment, t token) byte {
	if t.end-t.start != 1 || seg.runes[t.start] != replChar {
		return 0
	}
	return punctClass(seg.runes, t.start, func(j int) bool { return isWordRune(seg.runes[j]) && j != t.start })
}

// plan repairs one damaged txt.
func (p *planner) plan(in songInput) *filePlan {
	segs := segments(in.raw, in.song)
	fp := &filePlan{repl: map[int]rune{}}
	fp.total = strings.Count(string(in.raw), string(replChar))
	fp.langs, fp.langHow = p.languages(in.song, segs)
	ownSegs := segs
	if in.own != nil {
		ownSong, _ := songtxt.Parse(string(in.own))
		ownSegs = segments(in.own, ownSong)
	}
	local := localLex(in, ownSegs)
	wr := &wordResolver{p: p, levels: p.levels(fp.langs, local, in.songWords), local: local, cache: map[string]resolution{}, spanish: slices.Contains(fp.langs, "es")}
	for _, c := range fp.langs {
		if l := p.lex.langs[c]; l != nil {
			wr.langLexes = append(wr.langLexes, l)
		}
	}
	ff := &fileFixes{plan: fp, index: map[fixKey]int{}}
	inSegs := 0

	for _, seg := range segs {
		var replIdx []int
		for i, r := range seg.runes {
			if r == replChar {
				replIdx = append(replIdx, i)
			}
		}
		inSegs += len(replIdx)
		if len(replIdx) == 0 {
			continue
		}
		if seg.tag != "" {
			if fill, ok := nameFill(seg.runes, in.names, fileTags[seg.tag]); ok {
				after := []rune(string(seg.runes))
				for k, i := range replIdx {
					after[i] = fill[k]
				}
				ff.add(seg, replIdx, fill, "#"+seg.tag+":"+string(seg.runes), "#"+seg.tag+":"+string(after), srcFileName, false)
				continue
			}
			if fileTags[seg.tag] {
				ff.leave(seg, token{start: 0, end: len(seg.runes)}, nil)
				continue
			}
		}
		toks := tokenize(seg.runes)
		cr := caseRule{startUpper: startUpperConvention(seg, toks), local: local, global: p.lex.global()}
		forms := make([]string, len(toks)) // lower-case word, "" while damaged
		for i, t := range toks {
			if w := seg.runes[t.start:t.end]; !hasRepl(w) {
				forms[i] = string(lowerRunes(w))
			}
		}
		neighbours := func(i int) (prev, next string) {
			if i > 0 && !toks[i].lineStart {
				prev = forms[i-1]
			}
			if i+1 < len(toks) && !toks[i+1].lineStart {
				next = forms[i+1]
			}
			return prev, next
		}
		outs := make([]outcome, len(toks))
		resolveAll := func(first bool) {
			for i, t := range toks {
				w := seg.runes[t.start:t.end]
				if !hasRepl(w) || (!first && (outs[i].repl != nil || len(outs[i].cands) < 2)) {
					continue
				}
				prev, next := neighbours(i)
				_, to := seg.lyricLine(t.end)
				o := wr.resolveToken(w, cr, t.lineStart || seg.tag != "", singleClass(seg, t), prev, next, string(seg.runes[t.end:to]))
				outs[i] = o
				if o.form != nil {
					forms[i] = string(o.form)
				}
			}
		}
		resolveAll(true)
		resolveAll(false) // ambiguous words again, with the neighbours repaired meanwhile
		wr.joinHyphenated(seg, toks, outs, cr)
		for i, t := range toks {
			o := outs[i]
			w := seg.runes[t.start:t.end]
			if !hasRepl(w) {
				continue
			}
			if o.repl == nil || len(o.repl) != strings.Count(string(w), string(replChar)) {
				ff.leave(seg, t, o.cands)
				continue
			}
			var idx []int
			if o.suggest || (o.weak && p.strict) {
				ff.leave(seg, t, []Candidate{{Form: fill(w, o.repl) + " (suggested, " + o.source + ")"}})
				continue
			}
			after := []rune(string(w))
			k := 0
			for j, r := range w {
				if r == replChar {
					idx = append(idx, t.start+j)
					after[j] = o.repl[k]
					k++
				}
			}
			ff.add(seg, idx, o.repl, string(w), string(after), o.source, o.weak)
		}
	}
	fp.outside = fp.total - inSegs
	sort.SliceStable(fp.fixes, func(i, j int) bool { return fp.fixes[i].Lines[0] < fp.fixes[j].Lines[0] })
	return fp
}
