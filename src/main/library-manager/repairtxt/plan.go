package repairtxt

import (
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"unicode"

	"golang.org/x/text/unicode/norm"

	"github.com/nkreimeyer/library-manager/songtxt"
	"github.com/nkreimeyer/library-manager/utils"
)

// Level names, as they appear in the report.
const (
	srcFileName     = "file name" // a file name in the song folder
	srcSong         = "song"      // words of the song itself: file names, fetched lyrics, undamaged words
	srcWordlist     = "wordlist"
	srcReviewed     = "reviewed"      // a spelling from a -words file
	srcReviewedSong = "reviewed song" // a spelling from the song's section of a -words file
	srcCorpus       = "corpus"        // the undamaged txts of the library (all languages)
	srcPunct        = "punctuation"
)

// Fix is one kind of repair in a file: every occurrence of Before (a damaged word or tag
// value) that became After.
type Fix struct {
	Before string `json:"before"`
	After  string `json:"after"`
	Source string `json:"source"`
	Weak   bool   `json:"weak,omitempty"`
	Lines  []int  `json:"lines"`
}

// Left is a damaged word that was not repaired.
type Left struct {
	Line       int         `json:"line"`
	Word       string      `json:"word"`
	Context    string      `json:"context"`
	Candidates []Candidate `json:"candidates,omitempty"`
}

// filePlan is the repair of one txt.
type filePlan struct {
	repl      map[int]rune // byte offset of a U+FFFD -> replacement
	fixes     []Fix
	left      []Left
	total     int // U+FFFD characters in the file
	outside   int // of those, the ones outside any tag value or note
	langs     []string
	langHow   string // "tag", "detected" or ""
	weakCount int
}

// songInput is what the planner knows about one damaged txt.
type songInput struct {
	raw    []byte // valid UTF-8
	song   *songtxt.Song
	folder string   // name of the song folder
	names  []string // names of the other files in the song folder
	dirs   []string // names of the folders in the song folder (no extension to cut)
	refs   []string // extra reference texts (fetched lyrics)
	// own is the txt before an earlier repair (its oldest copy in originals/, else nil):
	// the song's own undamaged words are taken from it, so that run's repairs are no
	// evidence for this one.
	own []byte
	// songWords are the reviewed spellings for this song only ([Artist - Title] sections of
	// the -words files), the first level; may be nil.
	songWords *langLex
}

// planner repairs damaged txts with the corpus and the word lists.
type planner struct {
	lex       *Lexicon
	wordlists map[string]*langLex // language code ("" = any language) -> words
	strict    bool                // weak repairs are left (listed with their suggestion)
	reviewed  *langLex            // reviewed spellings (-words), the first level; may be nil
}

// languages returns the language codes of a song: the ones #LANGUAGE names plus the one
// detected from its undamaged words (tags are sometimes wrong, and often missing).
func (p *planner) languages(song *songtxt.Song, segs []segment) ([]string, string) {
	var codes []string
	how := ""
	if v, ok := song.Tag("LANGUAGE"); ok {
		codes = utils.LanguageCodes(v)
		if len(codes) > 0 {
			how = "tag"
		}
	}
	var words []string
	for _, s := range segs {
		if s.tag != "" {
			continue
		}
		ws, _ := textStats(s.runes)
		for _, w := range ws {
			words = append(words, w.form)
		}
	}
	if c := p.lex.detectLanguage(words, codes...); c != "" && !slices.Contains(codes, c) {
		codes = append(codes, c)
		if how == "" {
			how = "detected"
		} else {
			how = "tag+detected"
		}
	}
	return codes, how
}

// localLex builds the song's own word list: its folder and file names, the reference
// texts and its undamaged words. An ASCII apostrophe of a reference text is read as the
// song's own apostrophe (songApostrophe): a lost byte can only have been a non-ASCII one, so
// "j'espère" in the lyrics is the "j’espère" of "j�esp�re".
func localLex(in songInput, segs []segment) *langLex {
	l := newLangLex()
	add := func(s string) {
		w, _ := textStats([]rune(norm.NFC.String(s)))
		l.add(w, nil)
	}
	for _, n := range in.nameTexts(false) {
		add(n)
	}
	apo := string(songApostrophe(segs))
	for _, r := range in.refs {
		add(strings.ReplaceAll(r, "'", apo))
	}
	for _, s := range segs {
		if !fileTags[s.tag] {
			w, _ := textStats(s.runes)
			l.add(w, nil)
		}
	}
	l.finalize()
	return l
}

// nameTexts returns the folder name, the names of its subfolders and the file names, the
// latter without their extension unless whole is set. A folder name is never cut at a dot:
// "Dr. Alban - Männer" has no extension.
func (in songInput) nameTexts(whole bool) []string {
	var texts []string
	if in.folder != "" {
		texts = append(texts, in.folder)
	}
	texts = append(texts, in.dirs...)
	for _, n := range in.names {
		if !whole {
			n = strings.TrimSuffix(n, filepath.Ext(n))
		}
		texts = append(texts, n)
	}
	return texts
}

// songApostrophe is the non-ASCII apostrophe the song's lyrics use most (’ if none).
func songApostrophe(segs []segment) rune {
	counts := map[rune]int{}
	for _, s := range segs {
		if fileTags[s.tag] {
			continue
		}
		for _, r := range s.runes {
			if r >= 0x80 && isApostrophe(r) {
				counts[r]++
			}
		}
	}
	best := '’'
	for _, r := range []rune{'’', '´', '‘', 'ʼ'} {
		if counts[r] > counts[best] {
			best = r
		}
	}
	return best
}

func (p *planner) levels(langs []string, local, songWords *langLex) []level {
	var lv []level
	if songWords != nil {
		lv = append(lv, level{name: srcReviewedSong, lexes: []*langLex{songWords}, min: 1, ratio: 1e9})
	}
	if p.reviewed != nil {
		lv = append(lv, level{name: srcReviewed, lexes: []*langLex{p.reviewed}, min: 1, ratio: 1e9})
	}
	lv = append(lv, level{name: srcSong, lexes: []*langLex{local}, min: 1, ratio: 3})
	var langLexes []*langLex
	for _, c := range langs {
		if l := p.lex.langs[c]; l != nil {
			langLexes = append(langLexes, l)
		}
	}
	if len(langLexes) > 0 {
		lv = append(lv, level{name: srcCorpus + ":" + strings.Join(langs, "+"), lexes: langLexes, min: 2, ratio: 5})
	}
	var wl []*langLex
	if l := p.wordlists[""]; l != nil {
		wl = append(wl, l)
	}
	for _, c := range langs {
		if l := p.wordlists[c]; l != nil {
			wl = append(wl, l)
		}
	}
	if len(langs) == 0 {
		for c, l := range p.wordlists {
			if c != "" {
				wl = append(wl, l)
			}
		}
	}
	if len(wl) > 0 {
		lv = append(lv, level{name: srcWordlist, lexes: wl, min: 1, ratio: 1e9})
	}
	return append(lv, level{name: srcCorpus, lexes: []*langLex{p.lex.global()}, min: 2, ratio: 5})
}

// wildMatch reports whether the name runes s match the damaged pattern p: same length, a
// U+FFFD matches any allowed non-ASCII rune, other runes match regardless of case. The
// name's rune is filled in as it is, so it must be allowed itself (not only its lower
// case: Ƒ is in no codepage, ƒ is).
func wildMatch(p, s []rune) bool {
	if len(p) != len(s) {
		return false
	}
	for i, r := range p {
		if r == replChar {
			if !allowedRune[s[i]] {
				return false
			}
		} else if unicode.ToLower(r) != unicode.ToLower(s[i]) {
			return false
		}
	}
	return true
}

// nameFill returns the runes at the U+FFFD positions of value taken from the names that
// contain it (songInput.nameTexts: the whole name with whole = true, else any substring).
// All matches must agree (case-insensitively); the first match gives the case. A name
// equal to value as it is (a file really named with U+FFFD) means there is nothing to
// repair.
func nameFill(value []rune, names []string, whole bool) ([]rune, bool) {
	var fill []rune
	for _, n := range names {
		rs := []rune(norm.NFC.String(n))
		if whole && string(rs) == string(value) {
			return nil, false
		}
		if whole && len(rs) != len(value) {
			continue // a name that only starts with value is another file ("Cafè.mp3.bak")
		}
		for i := 0; i+len(value) <= len(rs); i++ {
			cand := rs[i : i+len(value)]
			if !wildMatch(value, cand) {
				continue
			}
			var f []rune
			for j, r := range value {
				if r == replChar {
					f = append(f, cand[j])
				}
			}
			if fill == nil {
				fill = f
			} else if string(lowerRunes(fill)) != string(lowerRunes(f)) {
				return nil, false
			}
		}
	}
	return fill, fill != nil
}

// startUpperConvention reports whether the words that start the lines of seg (or, in a
// tag value, all its words) mostly start with an upper-case letter.
func startUpperConvention(seg segment, toks []token) bool {
	up, n := 0, 0
	for _, t := range toks {
		if seg.tag == "" && !t.lineStart {
			continue
		}
		r := seg.runes[t.start]
		if r == replChar || !unicode.IsLetter(r) {
			continue
		}
		n++
		if unicode.IsUpper(r) {
			up++
		}
	}
	if n == 0 {
		return true
	}
	return 2*up >= n
}

// fileFixes collects the fixes of one file, grouped by kind.
type fileFixes struct {
	plan  *filePlan
	index map[fixKey]int
}

type fixKey struct {
	before, after, source string
	weak                  bool
}

// add records a fix. A fix with a rune no lost byte can stand for (see allowedRune) is
// dropped, so a repair never writes one; its U+FFFD count as left.
func (ff *fileFixes) add(seg segment, idx []int, runes []rune, before, after, source string, weak bool) {
	for k := range idx {
		if !allowedRune[runes[k]] {
			return
		}
	}
	for k, i := range idx {
		ff.plan.repl[seg.offs[i]] = runes[k]
	}
	if weak {
		ff.plan.weakCount += len(idx)
	}
	key := fixKey{before, after, source, weak}
	line := seg.lines[idx[0]]
	if j, ok := ff.index[key]; ok {
		ff.plan.fixes[j].Lines = append(ff.plan.fixes[j].Lines, line)
		return
	}
	ff.index[key] = len(ff.plan.fixes)
	ff.plan.fixes = append(ff.plan.fixes, Fix{Before: before, After: after, Source: source, Weak: weak, Lines: []int{line}})
}

func (ff *fileFixes) leave(seg segment, t token, cands []Candidate) {
	from, to := seg.lyricLine(t.start)
	ctx := string(seg.runes[from:t.start]) + "«" + string(seg.runes[t.start:t.end]) + "»" + string(seg.runes[t.end:to])
	if len(cands) > 5 {
		cands = cands[:5]
	}
	ff.plan.left = append(ff.plan.left, Left{
		Line: seg.lines[t.start], Word: string(seg.runes[t.start:t.end]),
		Context: strings.TrimSpace(ctx), Candidates: cands,
	})
}

// apply returns raw with every planned U+FFFD replaced.
func (fp *filePlan) apply(raw []byte) []byte {
	if len(fp.repl) == 0 {
		return raw
	}
	offs := make([]int, 0, len(fp.repl))
	for o := range fp.repl {
		offs = append(offs, o)
	}
	sort.Ints(offs)
	var b strings.Builder
	b.Grow(len(raw))
	prev := 0
	for _, o := range offs {
		b.Write(raw[prev:o])
		b.WriteRune(fp.repl[o])
		prev = o + len(string(replChar))
	}
	b.Write(raw[prev:])
	return []byte(b.String())
}
