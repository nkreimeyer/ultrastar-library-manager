package repairtxt

import (
	"sort"
	"strings"
	"sync"
	"unicode"

	"golang.org/x/text/encoding/charmap"
	"golang.org/x/text/unicode/norm"
)

// replChar is U+FFFD, the character an earlier conversion put in place of every byte it
// could not decode. Each one stands for exactly one lost single-byte (CP1252/CP1250)
// character, so a repair replaces it by exactly one rune.
const replChar = '\uFFFD'

// allowedRune reports whether r can be the character a lost byte stood for: a character
// that one byte in 0x80–0xFF decodes to in CP1252 or CP1250 (the codepages song txts were
// written in). ASCII never qualifies: an ASCII byte is never lost.
var allowedRune = func() map[rune]bool {
	m := map[rune]bool{}
	for _, cm := range []*charmap.Charmap{charmap.Windows1252, charmap.Windows1250} {
		for b := 0x80; b <= 0xFF; b++ {
			if r := cm.DecodeByte(byte(b)); r != replChar && r >= 0x80 {
				m[r] = true
			}
		}
	}
	return m
}()

// punctRunes are the punctuation characters a lost byte may stand for. Other signs of
// the codepages ("³", "¿" in "Za¿ó³æ") mostly come from mis-decoded text in the corpus.
var punctRunes = func() map[rune]bool {
	m := map[rune]bool{}
	for _, r := range "’‘´“”„«»‹›–—…¡¿·•°‚" {
		m[r] = true
	}
	return m
}()

// edgeRune reports the characters a word's outer form takes in (see token).
func edgeRune(r rune) bool { return r >= 0x80 && (isApostrophe(r) || punctRunes[r]) }

// isApostrophe reports the characters that join the parts of a word like "I’m".
func isApostrophe(r rune) bool {
	switch r {
	case '\'', '`', '’', '‘', '´', 'ʼ':
		return true
	}
	return false
}

// isWordRune reports the runes a word token consists of. U+FFFD counts as a word rune:
// most of them stand for letters (the punctuation case is handled by the planner).
func isWordRune(r rune) bool {
	return r == replChar || unicode.IsLetter(r) || unicode.IsMark(r) || isApostrophe(r)
}

// token is one word in a rune sequence: rs[start:end]. rs[outerStart:outerEnd] adds the
// non-ASCII apostrophes and punctuation at its edges ("goin’", "’cause", "„hallo", "¡oh").
type token struct {
	start, end           int
	outerStart, outerEnd int
	// lineStart reports that no other word precedes it in its lyric line or tag value.
	lineStart bool
}

// tokenize splits rs into words. '\n' separates lyric lines. Apostrophes at the edges of a
// word are not part of it ("'cause" is "cause").
func tokenize(rs []rune) []token {
	var toks []token
	lineStart := true
	for i := 0; i < len(rs); {
		if rs[i] == '\n' {
			lineStart = true
			i++
			continue
		}
		if !isWordRune(rs[i]) {
			i++
			continue
		}
		j := i
		for j < len(rs) && isWordRune(rs[j]) {
			j++
		}
		s, e := i, j
		for s < e && isApostrophe(rs[s]) {
			s++
		}
		for e > s && isApostrophe(rs[e-1]) {
			e--
		}
		if s < e {
			os, oe := s, e
			for os > 0 && edgeRune(rs[os-1]) {
				os--
			}
			for oe < len(rs) && edgeRune(rs[oe]) {
				oe++
			}
			toks = append(toks, token{start: s, end: e, outerStart: os, outerEnd: oe, lineStart: lineStart})
			lineStart = false
		}
		i = j
	}
	return toks
}

// Punctuation context classes: where a non-letter character stands relative to words
// and other characters.
const (
	classAlone      byte = 'S' // between spaces: "a – b"
	classLeading    byte = 'L' // before a word: "„Hallo"
	classTrailing   byte = 'T' // after a word: "Hallo…"
	classMiddle     byte = 'M' // between two words: "A–B"
	classAfterSign  byte = 'P' // after another sign, not a word: "back!”"
	classBeforeSign byte = 'Q' // before another sign: "“(…"
)

func isAttached(r rune) bool { return r != '\n' && !unicode.IsSpace(r) }

// punctClass returns the class of the character at rs[i]; inWord reports whether a rune
// belongs to a word.
func punctClass(rs []rune, i int, inWord func(int) bool) byte {
	prevWord := i > 0 && inWord(i-1)
	nextWord := i+1 < len(rs) && inWord(i+1)
	switch {
	case prevWord && nextWord:
		return classMiddle
	case prevWord:
		return classTrailing
	case nextWord:
		return classLeading
	case i > 0 && isAttached(rs[i-1]):
		return classAfterSign
	case i+1 < len(rs) && isAttached(rs[i+1]):
		return classBeforeSign
	}
	return classAlone
}

type punctKey struct {
	class byte
	r     rune
}

// entry counts the occurrences of one lower-case word form.
type entry struct {
	n        int // all occurrences
	mid      int // occurrences that do not start a lyric line or tag value
	midUpper int // of those, the ones written with an upper-case first letter
}

// langLex is the word statistics of one language (or of everything, or of one song).
type langLex struct {
	words map[string]*entry
	total int
	punct map[punctKey]int
	// bySkel maps the skeleton of every form with a non-ASCII rune to those forms.
	bySkel map[string][]string
	// bigrams counts adjacent words of a line ("a\x00b") where one has a non-ASCII rune.
	bigrams map[string]int
	// grams counts the character 4- and 5-grams with a non-ASCII rune of the words, with
	// "^" and "$" marking the word boundaries.
	grams map[string]int
}

func newLangLex() *langLex {
	return &langLex{words: map[string]*entry{}, punct: map[punctKey]int{}, bigrams: map[string]int{}, grams: map[string]int{}}
}

// wordGrams calls f for every character 4- and 5-gram of the padded word that has a
// non-ASCII rune.
func wordGrams(form string, f func(string)) {
	p := []rune("^" + form + "$")
	for n := 4; n <= 5; n++ {
		for i := 0; i+n <= len(p); i++ {
			if hasNonASCII(p[i : i+n]) {
				f(string(p[i : i+n]))
			}
		}
	}
}

func bigramKey(a, b string) string { return a + "\x00" + b }

// skeleton is the lookup key of a lower-case word: every non-ASCII rune (U+FFFD too)
// becomes U+FFFD, so a damaged word and its undamaged forms share it.
func skeleton(lower []rune) string {
	var b strings.Builder
	for _, r := range lower {
		if r >= 0x80 {
			r = replChar
		}
		b.WriteRune(r)
	}
	return b.String()
}

func lowerRunes(rs []rune) []rune {
	out := make([]rune, len(rs))
	for i, r := range rs {
		out[i] = unicode.ToLower(r)
	}
	return out
}

func hasNonASCII(rs []rune) bool {
	for _, r := range rs {
		if r >= 0x80 {
			return true
		}
	}
	return false
}

func hasRepl(rs []rune) bool {
	for _, r := range rs {
		if r == replChar {
			return true
		}
	}
	return false
}

// occurrence is one word or punctuation character found in a text.
type occurrence struct {
	form      string // lower case
	lineStart bool
	upper     bool
	// prev is the form of the previous word of the line ("" at the start of a line or
	// after a damaged word).
	prev string
	// extra marks the second form of a word with edge apostrophes; it does not count as
	// another word of the text.
	extra bool
}

// textStats returns the words (without U+FFFD) and the non-ASCII punctuation of rs.
func textStats(rs []rune) (words []occurrence, punct []punctKey) {
	toks := tokenize(rs)
	inTok := make([]bool, len(rs))
	prev := ""
	for _, t := range toks {
		for i := t.start; i < t.end; i++ {
			inTok[i] = true
		}
		if t.lineStart {
			prev = ""
		}
		w := rs[t.start:t.end]
		if hasRepl(w) {
			prev = ""
			continue
		}
		form := string(lowerRunes(w))
		words = append(words, occurrence{form: form, lineStart: t.lineStart, upper: unicode.IsUpper(w[0]), prev: prev})
		if t.outerStart < t.start || t.outerEnd > t.end {
			// The word with its edge apostrophes and punctuation is a form of its own: a lost
			// byte at the edge of "goin�" is the apostrophe of "goin’", the one of "tomb�"
			// the é of "tombé", the one of "�Oh" the "¡" of "¡Oh".
			words = append(words, occurrence{form: string(lowerRunes(rs[t.outerStart:t.outerEnd])), lineStart: t.lineStart, upper: unicode.IsUpper(w[0]), extra: true})
		}
		prev = form
	}
	for i, r := range rs {
		if inTok[i] || !punctRunes[r] {
			continue
		}
		punct = append(punct, punctKey{punctClass(rs, i, func(j int) bool { return inTok[j] }), r})
	}
	return words, punct
}

func (l *langLex) add(words []occurrence, punct []punctKey) {
	for _, w := range words {
		e := l.words[w.form]
		if e == nil {
			e = &entry{}
			l.words[w.form] = e
		}
		e.n++
		if w.prev != "" && (hasNonASCII([]rune(w.prev)) || hasNonASCII([]rune(w.form))) {
			l.bigrams[bigramKey(w.prev, w.form)]++
		}
		if !w.extra && hasNonASCII([]rune(w.form)) {
			wordGrams(w.form, func(g string) { l.grams[g]++ })
		}
		if !w.lineStart {
			e.mid++
			if w.upper {
				e.midUpper++
			}
		}
		if !w.extra {
			l.total++
		}
	}
	for _, p := range punct {
		l.punct[p]++
	}
}

// finalize builds the skeleton index. It must be called after the last add.
func (l *langLex) finalize() {
	l.bySkel = map[string][]string{}
	for form := range l.words {
		rs := []rune(form)
		if !hasNonASCII(rs) {
			continue
		}
		k := skeleton(rs)
		l.bySkel[k] = append(l.bySkel[k], form)
	}
	for _, forms := range l.bySkel {
		sort.Strings(forms)
	}
}

// compatible reports whether form (lower case, same skeleton) can be what the damaged
// lower-case word tok stood for: every U+FFFD is an allowed rune, and every other
// non-ASCII rune is the same.
func compatible(tok, form []rune) bool {
	if len(tok) != len(form) {
		return false
	}
	for i, r := range tok {
		switch {
		case r == replChar:
			if !allowedRune[form[i]] {
				return false
			}
		case r != form[i]:
			return false
		}
	}
	return true
}

// candidates adds the forms of l that tok (lower case, with U+FFFD) can stand for, with
// their counts, to into. A token that is a single U+FFFD can also be a punctuation
// character of the given class (0 = none).
func (l *langLex) candidates(tok []rune, class byte, into map[string]int) {
	for _, form := range l.bySkel[skeleton(tok)] {
		if compatible(tok, []rune(form)) {
			into[form] += l.words[form].n
		}
	}
	if len(tok) == 1 && tok[0] == replChar {
		for p, n := range l.punct {
			if p.class == class {
				into[string(p.r)] += n
			}
		}
	}
}

// count returns how often the repl-free lower-case word occurs.
func (l *langLex) count(form string) int {
	if e := l.words[form]; e != nil {
		return e.n
	}
	return 0
}

// Lexicon is the corpus built from the undamaged song txts of the library: one langLex per
// language code and one ("") over all songs.
type Lexicon struct {
	mu    sync.Mutex
	langs map[string]*langLex
}

// NewLexicon returns an empty corpus.
func NewLexicon() *Lexicon {
	return &Lexicon{langs: map[string]*langLex{"": newLangLex()}}
}

// addText adds the words of rs to the corpus of every language in langs and to the global
// corpus. It is safe for concurrent use.
func (lx *Lexicon) addText(langs []string, rs []rune) {
	rs = []rune(norm.NFC.String(string(rs)))
	words, punct := textStats(rs)
	lx.mu.Lock()
	defer lx.mu.Unlock()
	lx.langs[""].add(words, punct)
	for _, code := range langs {
		ll := lx.langs[code]
		if ll == nil {
			ll = newLangLex()
			lx.langs[code] = ll
		}
		ll.add(words, punct)
	}
}

func (lx *Lexicon) finalize() {
	for _, ll := range lx.langs {
		ll.finalize()
	}
}

func (lx *Lexicon) global() *langLex { return lx.langs[""] }
