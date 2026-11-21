package utils

import (
	"strings"
	"unicode"

	"golang.org/x/text/cases"
	"golang.org/x/text/unicode/norm"
)

// letterFolds maps letters that do not decompose under Unicode normalization to their
// Latin spelling. Only lower-case letters are listed: FoldString looks them up lower-cased
// (cases.Fold turns 'ẞ' into "ss" itself). The first letter of each spelling is the base
// letter organize.Bucket files the letter under ('æ' -> "ae" -> A, 'þ' -> "th" -> T).
var letterFolds = map[rune]string{
	'ø': "o", 'æ': "ae", 'œ': "oe", 'ł': "l", 'đ': "d", 'ð': "d", 'þ': "th",
	'ß': "ss", 'ı': "i", 'ħ': "h", 'ŧ': "t", 'ŋ': "n", 'ĸ': "k", 'ɨ': "i", 'ƶ': "z",
	// Letters with a hook or stroke ("ƒ" in stylized names).
	'ɓ': "b", 'ƈ': "c", 'ɗ': "d", 'ƒ': "f", 'ɠ': "g", 'ƙ': "k", 'ƥ': "p", 'ƭ': "t",
	'ʋ': "v", 'ȥ': "z",
	// 'ŀ' decomposes to "l" + middle dot: no entry needed.
}

// FoldString folds s for comparing names and words loosely:
//
//   - NFC, Unicode case folding and compatibility decomposition ("Ｆ" == "f", "ﬁ" == "fi");
//   - combining marks on Latin and Greek letters (and on non-letters) are dropped
//     ("Beyoncé" == "beyonce"); marks on letters of other scripts make different letters
//     and are kept (Cyrillic "й" != "и", kana "ゲ" != "ケ");
//   - letters that do not decompose get their Latin spelling (letterFolds: "ø" -> "o",
//     "æ" -> "ae", "ß" -> "ss", "ł" -> "l", ...);
//   - the result is NFC again, so kept marks are composed ("и" + breve -> "й").
//
// Every other rune (digits, punctuation, spaces, apostrophes) is kept as the case folding
// and decomposition leave it: NFKD turns '´' into a space plus a combining accent, so
// callers that drop apostrophes do so before folding.
func FoldString(s string) string {
	// A Caser is stateful and must not be shared between goroutines.
	s = norm.NFKD.String(cases.Fold().String(norm.NFC.String(s)))
	var b strings.Builder
	b.Grow(len(s))
	// foldMarks: combining marks after the last base character are dropped (it is not a
	// letter of a script other than Latin and Greek).
	foldMarks := true
	for _, r := range s {
		if unicode.Is(unicode.M, r) {
			if !foldMarks {
				b.WriteRune(r)
			}
			continue
		}
		foldMarks = !unicode.IsLetter(r) || unicode.In(r, unicode.Latin, unicode.Greek)
		// ToLower: NFKD can yield an upper-case letter after the case folding
		// (modifier letter 'ᴭ' -> 'Æ').
		if f, ok := letterFolds[unicode.ToLower(r)]; ok {
			b.WriteString(f)
		} else {
			b.WriteRune(r)
		}
	}
	return norm.NFC.String(b.String())
}

// IsApostrophe reports the apostrophe-like characters names and lyrics are written with:
// ' ` ’ ‘ ´ ʼ. Typographic quotes (“ ” „) are not apostrophes.
func IsApostrophe(r rune) bool {
	switch r {
	case '\'', '`', '’', '‘', '´', 'ʼ':
		return true
	}
	return false
}
