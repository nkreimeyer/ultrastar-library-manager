package organize

import (
	"strings"
	"unicode"

	"golang.org/x/text/cases"
	"golang.org/x/text/unicode/norm"
)

// Song identity keys. Both keys are built from normalized words:
//
//   - NFC, Unicode case folding, compatibility decomposition ("Ｆ" == "F", "ｶﾞ" == "ガ");
//     combining marks on Latin and Greek letters are dropped (diacritics folded:
//     "Beyoncé" == "Beyonce"), plus keyLetterFolds for letters that do not decompose
//     ("ø" -> "o", "æ" -> "ae", "ß" -> "ss", ...). Marks on letters of other scripts
//     make different letters and are kept (Cyrillic "й" != "и", kana "ゲ" != "ケ");
//   - invisible format and control characters (soft hyphen, zero-width space/joiner, BOM)
//     are dropped, as FolderName drops them ("Wonder\u00adwall" == "Wonderwall");
//   - apostrophes (' ’ ‘ ` ´ ʼ) and dots are removed inside words ("Don’t" == "Dont",
//     "R.E.M." == "REM");
//   - "&" and "+" are the word "and" ("Simon & Garfunkel" == "Simon and Garfunkel");
//   - every other punctuation or symbol separates words ("AC/DC" == "AC-DC" == "AC DC",
//     "Jay-Z" == "Jay Z"), whitespace is collapsed.
//
// The keys are internal: they are never shown or used as file names.

// keySep separates the parts of a key. It cannot occur in a normalized word.
const keySep = "|"

// keyLetterFolds maps letters that do not decompose to their Latin spelling, for keys.
// Case folding runs first, so only lower-case letters are listed.
var keyLetterFolds = map[rune]string{
	'ø': "o", 'æ': "ae", 'œ': "oe", 'ł': "l", 'đ': "d", 'ð': "d", 'þ': "th",
	'ı': "i", 'ħ': "h", 'ŧ': "t", 'ŋ': "n", 'ĸ': "k", 'ŀ': "l", 'ɨ': "i", 'ƶ': "z",
}

// isApostrophe reports whether r is an apostrophe-like character that is removed from
// words instead of separating them.
func isApostrophe(r rune) bool {
	switch r {
	case '\'', '’', '‘', '`', '´', 'ʼ', 'ʹ', '′':
		return true
	}
	return false
}

// normWords returns the normalized words of s (see the package comment above).
func normWords(s string) []string {
	s = norm.NFC.String(strings.ToValidUTF8(s, ""))
	// Apostrophes go before the decomposition: NFKD turns '´' into a space plus a
	// combining accent.
	s = strings.Map(func(r rune) rune {
		if isApostrophe(r) {
			return -1
		}
		return r
	}, s)
	s = norm.NFKD.String(cases.Fold().String(s))
	var b strings.Builder
	// foldMarks: combining marks after the last base character are dropped (it is not a
	// letter of a script other than Latin and Greek). inWord: the last character written
	// belongs to a word, so a kept mark has a letter to attach to.
	foldMarks, inWord := true, false
	for _, r := range s {
		if unicode.Is(unicode.M, r) {
			if !foldMarks && inWord {
				b.WriteRune(r)
			}
			continue
		}
		foldMarks = !unicode.IsLetter(r) || unicode.In(r, unicode.Latin, unicode.Greek)
		switch {
		case r == '.' || isApostrophe(r):
		case unicode.IsSpace(r):
			b.WriteByte(' ')
			inWord = false
		case unicode.IsControl(r) || unicode.Is(unicode.Cf, r):
			// Invisible: soft hyphen, zero-width space/joiner, BOM, ...
		case r == '&' || r == '+':
			b.WriteString(" and ")
			inWord = false
		case keyLetterFolds[r] != "":
			b.WriteString(keyLetterFolds[r])
			inWord = true
		case unicode.IsLetter(r) || unicode.IsDigit(r):
			b.WriteRune(r)
			inWord = true
		default:
			b.WriteByte(' ')
			inWord = false
		}
	}
	// Kept marks are composed again ("и" + breve -> "й"), so NFC and NFD input agree.
	return strings.Fields(norm.NFC.String(b.String()))
}

// isFeatWord reports whether w (normalized) introduces featured artists.
func isFeatWord(w string) bool {
	return w == "feat" || w == "ft" || w == "featuring"
}

// versionWords are words that make a recording differ from the studio song. A "feat."
// part followed by one of them is not dropped: the featured names cannot be told apart
// from the qualifier ("Song feat. X Remix", "(feat. X Live)"), and when in doubt nothing
// is merged.
var versionWords = map[string]bool{
	"live": true, "remix": true, "mix": true, "rmx": true, "acoustic": true, "akustik": true,
	"unplugged": true, "version": true, "edit": true, "demo": true, "instrumental": true,
	"extended": true, "reprise": true, "cover": true, "session": true, "sessions": true,
	"stripped": true, "orchestral": true, "symphonic": true, "piano": true, "dub": true,
	"club": true, "radio": true, "mashup": true, "medley": true, "bootleg": true, "rework": true,
}

func hasVersionWord(words []string) bool {
	for _, w := range words {
		if versionWords[w] {
			return true
		}
	}
	return false
}

// cutFeat drops a "feat. X" part from normalized words. The feat word must follow at least
// one word and be followed by at least one, so "Little Feat" and a title "Feat" stay.
// allowFt: "ft" counts as a feat word too. Titles pass false for the strict key: in a
// title it is often an abbreviation ("Welcome to Ft. Lauderdale"). guard: if a version
// word follows (see versionWords), only the feat word itself is dropped.
func cutFeat(words []string, allowFt, guard bool) []string {
	for i := 1; i < len(words)-1; i++ {
		if !isFeatWord(words[i]) || (!allowFt && words[i] == "ft") {
			continue
		}
		if guard && hasVersionWord(words[i+1:]) {
			return append(append([]string(nil), words[:i]...), words[i+1:]...)
		}
		return words[:i]
	}
	return words
}

// qualifierKind classifies the content of a bracket group or of a " - " suffix.
type qualifierKind int

const (
	qualOther qualifierKind = iota // changes the song: kept (Live, Acoustic, Remix, ...)
	qualNoise                      // remaster, video, karaoke, plain year, ...: stripped
	qualDuet                       // [DUET], (Duet Version): expressed by the duet flag
	qualFeat                       // (feat. X): dropped
)

// Words that make a qualifier noise. A qualifier is noise if, ignoring years, all its
// words are noise words and at least one is a core word. "version" and "edition" are
// allowed next to a core word, and make "album" and "single" core words.
var (
	noiseCore = map[string]bool{
		"remaster": true, "remastered": true, "video": true, "hd": true, "hq": true,
		"4k": true, "karaoke": true, "ultrastar": true, "singstar": true, "explicit": true,
		"lyrics": true, "audio": true,
	}
	noiseModifiers = map[string]bool{
		"official": true, "music": true, "digitally": true, "digital": true, "lyric": true,
	}
)

// isYear reports whether a normalized word is a plain year (1900-2099).
func isYear(w string) bool {
	return len(w) == 4 && (strings.HasPrefix(w, "19") || strings.HasPrefix(w, "20")) &&
		strings.Trim(w, "0123456789") == ""
}

// classifyQualifier classifies the normalized words of a qualifier.
func classifyQualifier(words []string) qualifierKind {
	if len(words) == 0 {
		return qualNoise // "()" or only punctuation
	}
	switch {
	case words[0] == "duet" || words[0] == "duett":
		return qualDuet
	case isFeatWord(words[0]):
		if hasVersionWord(words[1:]) {
			return qualOther // "(feat. X Live)": kept without the feat word
		}
		return qualFeat
	}
	var rest []string
	hasVersion := false
	for _, w := range words {
		switch {
		case isYear(w):
		case w == "version" || w == "edition":
			hasVersion = true
		default:
			rest = append(rest, w)
		}
	}
	if len(rest) == 0 {
		// A plain year is noise; "(Version)" or "(2011 Version)" may be a re-recording.
		if hasVersion {
			return qualOther
		}
		return qualNoise
	}
	core := false
	for _, w := range rest {
		switch {
		case noiseCore[w], hasVersion && (w == "album" || w == "single"):
			core = true
		case noiseModifiers[w]:
		default:
			return qualOther
		}
	}
	if core {
		return qualNoise
	}
	return qualOther
}

// titlePart is a piece of a title: plain text or the content of a bracket group.
type titlePart struct {
	text    string
	bracket bool
	leading bool // a bracket group at the very start of the title, part of the name
}

// bracketClosers maps an opening bracket to its closing one.
var bracketClosers = map[rune]rune{'(': ')', '[': ']', '{': '}'}

// splitBrackets splits s into plain text and bracket groups. Groups are not nested: the
// first matching closer ends a group. An opener without a closer is plain text.
func splitBrackets(s string) []titlePart {
	var parts []titlePart
	var text strings.Builder
	sawText := false
	for i := 0; i < len(s); {
		r := rune(s[i])
		closer, ok := bracketClosers[r]
		if !ok {
			j := i + 1
			for j < len(s) {
				if _, open := bracketClosers[rune(s[j])]; open {
					break
				}
				j++
			}
			text.WriteString(s[i:j])
			if strings.TrimSpace(s[i:j]) != "" {
				sawText = true
			}
			i = j
			continue
		}
		end := strings.IndexRune(s[i+1:], closer)
		if end < 0 {
			text.WriteString(s[i:])
			sawText = true
			break
		}
		if text.Len() > 0 {
			parts = append(parts, titlePart{text: text.String()})
			text.Reset()
		}
		parts = append(parts, titlePart{text: s[i+1 : i+1+end], bracket: true, leading: !sawText})
		sawText = true
		i += end + 2
	}
	if text.Len() > 0 {
		parts = append(parts, titlePart{text: text.String()})
	}
	return parts
}

// dashSeparators separate a qualifier such as "Song - Remastered 2011" from the title.
var dashSeparators = []string{" - ", " – ", " — "}

// splitDashes splits s at every dash separator.
func splitDashes(s string) []string {
	pieces := []string{s}
	for _, sep := range dashSeparators {
		var next []string
		for _, p := range pieces {
			next = append(next, strings.Split(p, sep)...)
		}
		pieces = next
	}
	return pieces
}

// duetExtras returns the words of a duet qualifier ("[Duet - Acoustic]", "(Duet Remix)",
// "(Duet with B, Live)") that are not about the duet itself, so they keep the versions
// apart: the duet word, "version"/"edition" and noise are dropped, and of the names after
// "with"/"mit"/"feat." only version words (see versionWords) are kept.
func duetExtras(words []string) []string {
	var head, names []string
	inNames := false
	for _, w := range words[1:] {
		switch {
		case inNames:
			names = append(names, w)
		case w == "with" || w == "mit" || isFeatWord(w):
			inNames = true
		case w == "duet" || w == "duett" || w == "version" || w == "edition":
		default:
			head = append(head, w)
		}
	}
	var out []string
	if len(head) > 0 && classifyQualifier(head) == qualOther {
		out = append(out, head...)
	}
	for _, w := range names {
		if versionWords[w] {
			out = append(out, w)
		}
	}
	return out
}

// titleWords returns the normalized words of a title and whether it carries a duet
// qualifier. Bracket groups and the parts after a " - " are qualifiers (see
// classifyQualifier): noise and feat parts are dropped, a duet qualifier sets duet and
// keeps only its other words (duetExtras), every other qualifier stays words. With loose,
// every qualifier is dropped except a leading bracket group, which is part of the name.
func titleWords(title string, loose bool) (words []string, duet bool) {
	t := strings.TrimSpace(norm.NFC.String(strings.ToValidUTF8(title, "")))
	qualifier := func(w []string, leading bool) {
		switch classifyQualifier(w) {
		case qualDuet:
			duet = true
			if !loose {
				words = append(words, duetExtras(w)...)
			}
		case qualNoise, qualFeat:
		default:
			if !loose || leading {
				if isFeatWord(w[0]) {
					w = w[1:] // "(feat. X Live)"
				}
				words = append(words, w...)
			}
		}
	}
	for _, p := range splitBrackets(t) {
		if p.bracket {
			qualifier(normWords(p.text), p.leading)
			continue
		}
		for k, piece := range splitDashes(p.text) {
			w := normWords(piece)
			if k == 0 {
				words = append(words, cutFeat(w, loose, !loose)...)
				continue
			}
			qualifier(w, false)
		}
	}
	return words, duet
}

// artistWords returns the normalized words of an artist: "feat." parts (also in
// brackets) are dropped, a leading "the" is stripped, other bracket groups stay words
// ("(hed) p.e.").
func artistWords(artist string) []string {
	var words []string
	for _, p := range splitBrackets(strings.TrimSpace(norm.NFC.String(strings.ToValidUTF8(artist, "")))) {
		w := normWords(p.text)
		if p.bracket && len(w) > 0 && isFeatWord(w[0]) {
			continue
		}
		if !p.bracket {
			w = cutFeat(w, true, true)
		}
		words = append(words, w...)
	}
	if len(words) > 1 && words[0] == "the" {
		words = words[1:]
	}
	return words
}

// SongKey returns the strict identity key used to group duplicates, or "" if the artist
// or the title is empty after normalization.
//
// Words are normalized as described at the top of this file. In addition:
//   - a leading "the" is stripped from the artist ("The Beatles" == "Beatles");
//   - "feat." / "ft." / "featuring" parts are dropped from artist and title, also inside
//     brackets ("Song (feat. X)" == "Song"). In the plain text of a title "ft." is kept (it
//     may be an abbreviation: "Welcome to Ft. Lauderdale"); LooseKey drops it. If a
//     version word follows the names ("Song feat. X Remix", "(feat. X Live)"), only the
//     feat word is dropped, so a live or remix version does not merge with the studio song;
//   - bracket qualifiers ((), [], {}) and trailing " - X" parts that are known noise are
//     stripped: remaster(ed) [year], official [music] video, video, lyrics, audio, hd, hq,
//     4k, karaoke, ultrastar, singstar, explicit, album/single version, a plain year
//     "(1984)". Every other qualifier is kept as words (Live, Acoustic, Remix,
//     Radio Edit, German Version, (TV), "(2011 Version)", ...): "Song (Live)" ==
//     "Song [Live]" == "Song - Live", but not "Song";
//   - a duet qualifier ("[DUET]", "(Duet)", "(Duet Version)", "- Duet") is removed from the
//     title; duet (from the caller, e.g. P1/P2 in the txt) or such a qualifier adds a duet
//     marker, so a duet and a solo version of the same song have different keys. Other
//     qualifiers inside the duet group stay ("[Duet - Acoustic]" == "(Acoustic) [DUET]").
func SongKey(artist, title string, duet bool) string {
	a := artistWords(artist)
	t, titleDuet := titleWords(title, false)
	if len(a) == 0 || len(t) == 0 {
		return ""
	}
	key := strings.Join(a, " ") + keySep + strings.Join(t, " ")
	if duet || titleDuet {
		key += keySep + "duet"
	}
	return key
}

// LooseKey is SongKey with every title qualifier stripped (except a leading bracket group
// such as "(I Can't Get No) Satisfaction", which is part of the name) and the duet marker
// ignored. It only lists possible duplicates for manual review; nothing is merged by it.
// It returns "" if the artist or the title is empty after normalization.
func LooseKey(artist, title string) string {
	a := artistWords(artist)
	t, _ := titleWords(title, true)
	if len(a) == 0 || len(t) == 0 {
		return ""
	}
	return strings.Join(a, " ") + keySep + strings.Join(t, " ")
}

// IsDuetTitle reports whether the title has a duet qualifier: a bracket group or a
// trailing " - X" part whose first word is "duet" or "duett" ("[DUET]", "(Duet Version)",
// "(Duett mit X)", "Song - Duet").
func IsDuetTitle(title string) bool {
	_, duet := titleWords(title, false)
	return duet
}
