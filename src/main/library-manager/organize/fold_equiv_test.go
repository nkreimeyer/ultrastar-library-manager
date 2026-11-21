package organize

import (
	"testing"
	"unicode"

	"golang.org/x/text/unicode/norm"
)

// oldLetterFolds and oldFoldLetter are the bucket folding before it moved to
// utils.FoldString, kept to prove that no bucket changes.
var oldLetterFolds = map[rune]rune{
	'Ø': 'O', 'ø': 'O', 'Æ': 'A', 'æ': 'A', 'Œ': 'O', 'œ': 'O', 'Ł': 'L', 'ł': 'L',
	'Đ': 'D', 'đ': 'D', 'Ð': 'D', 'ð': 'D', 'Þ': 'T', 'þ': 'T', 'ß': 'S', 'ẞ': 'S',
	'ı': 'I', 'Ħ': 'H', 'ħ': 'H', 'Ŧ': 'T', 'ŧ': 'T', 'Ŋ': 'N', 'ŋ': 'N', 'ĸ': 'K',
	'Ɨ': 'I', 'ɨ': 'I', 'Ƶ': 'Z', 'ƶ': 'Z', 'Ɓ': 'B', 'ɓ': 'B', 'Ƈ': 'C', 'ƈ': 'C',
	'Ɗ': 'D', 'ɗ': 'D', 'Ƒ': 'F', 'ƒ': 'F', 'Ɠ': 'G', 'ɠ': 'G', 'Ƙ': 'K', 'ƙ': 'K',
	'Ƥ': 'P', 'ƥ': 'P', 'Ƭ': 'T', 'ƭ': 'T', 'Ʋ': 'V', 'ʋ': 'V', 'Ȥ': 'Z', 'ȥ': 'Z',
}

func oldFoldLetter(r rune) rune {
	for _, d := range norm.NFKD.String(string(r)) {
		if unicode.Is(unicode.Mn, d) {
			continue
		}
		if f, ok := oldLetterFolds[d]; ok {
			return f
		}
		return unicode.ToUpper(d)
	}
	return unicode.ToUpper(r)
}

// Every letter Bucket can see first gets the same bucket as before the folding moved to
// utils.FoldString.
func TestFoldLetterBucketUnchanged(t *testing.T) {
	inAZ := func(r rune) bool { return r >= 'A' && r <= 'Z' }
	for r := rune(0); r <= unicode.MaxRune; r++ {
		if !unicode.IsLetter(r) || unicode.IsDigit(r) {
			continue
		}
		o, n := oldFoldLetter(r), foldLetter(r)
		if inAZ(o) != inAZ(n) || (inAZ(o) && o != n) {
			t.Errorf("foldLetter(%U %q) = %q, was %q", r, r, n, o)
		}
	}
}
