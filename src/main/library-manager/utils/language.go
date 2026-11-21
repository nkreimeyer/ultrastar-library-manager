package utils

import (
	"strings"
	"unicode"

	"golang.org/x/text/unicode/norm"
)

// languagePrefixes maps the start of a folded, letters-only language name (English,
// German or native spelling) to a code. The first matching prefix wins, so a prefix is
// listed before every shorter one it starts with (TestLanguagePrefixOrder). An empty code
// stops the search: the name starts like a known language but is another one (Friulian,
// not French) or is ambiguous, and a wrong code is worse than none.
var languagePrefixes = []struct{ prefix, code string }{
	{"deu", "de"}, {"ger", "de"}, {"dt", "de"},
	{"eng", "en"},
	{"frie", "fy"}, {"fris", "fy"}, {"frys", "fy"}, {"friu", ""}, {"fria", ""}, {"frank", ""}, {"fr", "fr"},
	{"spa", "es"}, {"esper", "eo"}, {"esp", "es"}, {"cast", "es"},
	{"schwed", "sv"}, {"swe", "sv"}, {"sve", "sv"},
	{"poly", ""}, {"pol", "pl"},
	{"ita", "it"},
	{"dut", "nl"}, {"ned", "nl"}, {"niederl", "nl"}, {"holl", "nl"}, {"flam", "nl"}, {"flem", "nl"},
	{"nor", "no"},
	{"dan", "da"},
	{"fin", "fi"}, {"suom", "fi"},
	{"por", "pt"},
	{"turkm", ""}, {"tur", "tr"},
	{"tschech", "cs"}, {"cze", "cs"}, {"ces", "cs"},
	{"slovak", "sk"}, {"slowak", "sk"}, {"slovenc", "sk"}, {"slove", "sl"}, {"slowe", "sl"},
	{"hin", "hi"}, {"chin", "zh"},
	{"hun", "hu"}, {"ung", "hu"}, {"magy", "hu"},
	{"korea", "ko"}, {"jap", "ja"}, {"rusyn", ""}, {"rusin", ""}, {"rus", "ru"},
	// "Latino" is Latin in Italian but Latin American Spanish in Spanish.
	{"latv", "lv"}, {"lett", "lv"}, {"latino", ""}, {"latg", ""}, {"lat", "la"},
	{"ice", "is"}, {"isl", "is"},
	{"kroat", "hr"}, {"croat", "hr"}, {"hrv", "hr"},
	{"rumantsch", ""}, {"rum", "ro"},
	// Romani (Romany, Romanes), Romansh (Romanche, Romancio) and Romagnol are no Romanian.
	{"romanian", "ro"}, {"romani", ""}, {"romany", ""}, {"romanes", ""}, {"romansh", ""}, {"romanc", ""}, {"romag", ""}, {"rom", "ro"},
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
// repetitions ("German/English" = de, en). Names it does not know are left out, also those
// that only start like a known one ("Esperanto" is eo, not es). A U+FFFD
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
				if lp.code != "" && !seen[lp.code] {
					seen[lp.code] = true
					codes = append(codes, lp.code)
				}
				break
			}
		}
	}
	return codes
}

// KnownLanguageCode reports whether LanguageCodes can return code.
func KnownLanguageCode(code string) bool {
	for _, lp := range languagePrefixes {
		if lp.code == code {
			return true
		}
	}
	return false
}
