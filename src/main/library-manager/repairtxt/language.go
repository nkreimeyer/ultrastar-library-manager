package repairtxt

import (
	"math"
	"sort"
	"strings"
	"unicode"

	"golang.org/x/text/unicode/norm"
)

// languagePrefixes maps the start of a folded, letters-only language name (English,
// German or native spelling) to a code. The first matching prefix wins, so a prefix is
// listed before every shorter one it starts with (TestLanguagePrefixOrder).
var languagePrefixes = []struct{ prefix, code string }{
	{"deu", "de"}, {"ger", "de"}, {"dt", "de"},
	{"eng", "en"},
	{"fr", "fr"},
	{"spa", "es"}, {"esp", "es"}, {"cast", "es"},
	{"schwed", "sv"}, {"swe", "sv"}, {"sve", "sv"},
	{"pol", "pl"},
	{"ita", "it"},
	{"dut", "nl"}, {"ned", "nl"}, {"nied", "nl"}, {"holl", "nl"}, {"flam", "nl"}, {"flem", "nl"},
	{"nor", "no"},
	{"dan", "da"},
	{"fin", "fi"}, {"suom", "fi"},
	{"por", "pt"},
	{"tur", "tr"},
	{"tsch", "cs"}, {"cze", "cs"}, {"ces", "cs"},
	{"slovak", "sk"}, {"slowak", "sk"}, {"slovenc", "sk"}, {"slove", "sl"}, {"slowe", "sl"},
	{"hin", "hi"}, {"chin", "zh"},
	{"hun", "hu"}, {"ung", "hu"}, {"magy", "hu"},
	{"kor", "ko"}, {"jap", "ja"}, {"rus", "ru"},
	{"latv", "lv"}, {"lett", "lv"}, {"lat", "la"},
	{"ice", "is"}, {"isl", "is"},
	{"kroat", "hr"}, {"croat", "hr"}, {"hrv", "hr"},
	{"rum", "ro"}, {"rom", "ro"},
}

// foldLetters lower-cases s, drops diacritics and keeps only ASCII letters.
func foldLetters(s string) string {
	var b strings.Builder
	for _, r := range norm.NFD.String(s) {
		r = unicode.ToLower(r)
		if r >= 'a' && r <= 'z' {
			b.WriteRune(r)
		}
	}
	return b.String()
}

// LanguageCodes returns the language codes a #LANGUAGE value names, in order and without
// repetitions ("German/English" = de, en). Names it does not know are left out. A U+FFFD
// in the name does not matter ("Espa�ol" = es).
func LanguageCodes(value string) []string {
	parts := strings.FieldsFunc(value, func(r rune) bool {
		return r == '/' || r == ',' || r == '&' || r == '+' || r == ';' || r == '|' || r == '(' || r == ')' || unicode.IsSpace(r)
	})
	var codes []string
	seen := map[string]bool{}
	for _, p := range parts {
		f := foldLetters(p)
		if f == "" || f == "and" || f == "und" {
			continue
		}
		for _, lp := range languagePrefixes {
			if strings.HasPrefix(f, lp.prefix) {
				if !seen[lp.code] {
					seen[lp.code] = true
					codes = append(codes, lp.code)
				}
				break
			}
		}
	}
	return codes
}

// jmLambda is the weight of the language's own distribution in detectLanguage.
const jmLambda = 0.5

// minDetectTokens is the corpus size a language needs before detectLanguage considers it.
const minDetectTokens = 5000

// detectLanguage guesses the language of a damaged song from its undamaged words with a
// multinomial naive Bayes over the corpus languages (Jelinek-Mercer smoothed, see score). It returns "" if there are too few
// words or no language corpus is big enough. If tagged languages are given, a different
// language is only returned if it scores clearly better (by 0.5 nats per word) than the
// best of them, so a wrong tag is corrected but a right one is not diluted.
func (lx *Lexicon) detectLanguage(words []string, tagged ...string) string {
	if len(words) < 10 {
		return ""
	}
	// Jelinek-Mercer smoothing with the whole library: P(w|L) = λ·c_L(w)/N_L + (1-λ)·P(w).
	// A word a language has never seen costs the same in every language, so only words a
	// language has seen count for it. (With add-one or Dirichlet smoothing a missing word
	// costs a big corpus more than a small one, and songs with many rare words drifted
	// to the small languages.)
	g := lx.global()
	gDenom := float64(g.total + len(g.words))
	score := func(ll *langLex) float64 {
		v := 0.0
		for _, w := range words {
			v += math.Log(jmLambda*float64(ll.count(w))/float64(ll.total) + (1-jmLambda)*float64(g.count(w)+1)/gDenom)
		}
		return v
	}
	type scored struct {
		code string
		v    float64
	}
	var scores []scored
	tagBest := math.Inf(-1)
	for code, ll := range lx.langs {
		if code == "" || ll.total < minDetectTokens {
			continue
		}
		v := score(ll)
		scores = append(scores, scored{code, v})
		for _, t := range tagged {
			if t == code {
				tagBest = max(tagBest, v)
			}
		}
	}
	if len(scores) == 0 {
		return ""
	}
	sort.Slice(scores, func(i, j int) bool {
		if scores[i].v != scores[j].v {
			return scores[i].v > scores[j].v
		}
		return scores[i].code < scores[j].code
	})
	best := scores[0]
	if len(tagged) > 0 && (best.v-tagBest)/float64(len(words)) < 0.5 {
		return ""
	}
	return best.code
}
