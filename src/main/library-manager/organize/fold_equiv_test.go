package organize

import (
	"math/rand/v2"
	"slices"
	"strings"
	"testing"
	"unicode"

	"golang.org/x/text/cases"
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

// oldKeyLetterFolds and oldNormWords are the key normalization before it moved to
// utils.FoldString, kept to prove which keys change.
var oldKeyLetterFolds = map[rune]string{
	'ø': "o", 'æ': "ae", 'œ': "oe", 'ł': "l", 'đ': "d", 'ð': "d", 'þ': "th",
	'ı': "i", 'ħ': "h", 'ŧ': "t", 'ŋ': "n", 'ĸ': "k", 'ɨ': "i", 'ƶ': "z",
}

func oldIsApostrophe(r rune) bool {
	switch r {
	case '\'', '’', '‘', '`', '´', 'ʼ', 'ʹ', '′':
		return true
	}
	return false
}

func oldNormWords(s string) []string {
	s = norm.NFC.String(strings.ToValidUTF8(s, ""))
	s = strings.Map(func(r rune) rune {
		if oldIsApostrophe(r) {
			return -1
		}
		return r
	}, s)
	s = norm.NFKD.String(cases.Fold().String(s))
	var b strings.Builder
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
		case r == '.' || r == '·' || oldIsApostrophe(r):
		case unicode.IsSpace(r):
			b.WriteByte(' ')
			inWord = false
		case unicode.IsControl(r) || unicode.Is(unicode.Cf, r):
		case r == '&' || r == '+':
			b.WriteString(" and ")
			inWord = false
		case oldKeyLetterFolds[r] != "":
			b.WriteString(oldKeyLetterFolds[r])
			inWord = true
		case unicode.IsLetter(r) || unicode.IsDigit(r):
			b.WriteRune(r)
			inWord = true
		default:
			b.WriteByte(' ')
			inWord = false
		}
	}
	return strings.Fields(norm.NFC.String(b.String()))
}

// keyFoldChanged are the runes whose key changed deliberately when the keys took the
// shared folding (also when a decomposition yields them): the letters with a hook (the
// buckets folded them already) and the upper-case table letters a decomposition yields
// after the case folding (modifier letter 'ᴭ' -> 'Æ').
const keyFoldChanged = "ƁɓƇƈƊɗƑƒƓɠƘƙƤƥƬƭƲʋȤȥÆĦ"

// Every rune alone, and between letters, and followed by every mark of a sample, gives
// the same words as before, except keyFoldChanged. (Upper-case Æ and Ħ only survive the
// case folding as the decomposition of a modifier letter.)
func TestNormWordsUnchanged(t *testing.T) {
	marks := []rune{0x301, 0x306, 0x308, 0x309, 0x93C, 0x3099, 0x20DD}
	check := func(s string) {
		t.Helper()
		if strings.ContainsAny(norm.NFKD.String(cases.Fold().String(s)), keyFoldChanged) {
			return
		}
		if o, n := oldNormWords(s), normWords(s); !slices.Equal(o, n) {
			t.Errorf("normWords(%+q) = %q, was %q", s, n, o)
		}
	}
	for r := rune(0); r <= unicode.MaxRune; r++ {
		if r >= 0xD800 && r <= 0xDFFF {
			continue
		}
		if !unicode.IsPrint(r) && !unicode.Is(unicode.Cf, r) && !unicode.IsSpace(r) && r > 0x7F {
			continue
		}
		c := string(r)
		check(c)
		check("a" + c + "b")
		check("и" + c + "\u0306")
		if r < 0x3400 || unicode.Is(unicode.M, r) {
			for _, m := range marks {
				check(c + string(m))
				check("ж" + c + string(m) + "x")
			}
		}
	}
	rng := rand.New(rand.NewPCG(1, 2))
	pool := []rune("aAéÉøØæÆßẞłŁŀĿ' ’ʼʹ′´`.·&+-/ йИゲケガ한글ʰᴮ\u0301\u0306\u0308\u3099\u00ad\u200b\ufeffΆΣﬁＦ1٣ŉİı")
	for i := 0; i < 200000; i++ {
		n := 1 + rng.IntN(8)
		rs := make([]rune, n)
		for j := range rs {
			rs[j] = pool[rng.IntN(len(pool))]
		}
		check(string(rs))
	}
}

// The deliberately changed keys: a letter with a hook is its base letter, as in Bucket.
func TestNormWordsHookedLetters(t *testing.T) {
	for _, c := range []struct{ in, want string }{
		{"ƒunk", "funk"}, {"Ƒunk", "funk"}, {"ɓa", "ba"}, {"Ʋa", "va"}, {"ȥa", "za"}, {"ᴭ", "ae"},
	} {
		if got := normWords(c.in); !slices.Equal(got, []string{c.want}) {
			t.Errorf("normWords(%q) = %q, want %q", c.in, got, c.want)
		}
	}
	if SongKey("ƒ(x)", "Ƒunk", false) != SongKey("f(x)", "Funk", false) {
		t.Errorf("SongKey does not fold ƒ: %q vs %q", SongKey("ƒ(x)", "Ƒunk", false), SongKey("f(x)", "Funk", false))
	}
}
