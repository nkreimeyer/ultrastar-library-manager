package utils

import (
	"strings"
	"unicode"
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
	// Polari is a cant, Polka a dance (a genre in the wrong tag).
	{"poly", ""}, {"polar", ""}, {"polk", ""}, {"pol", "pl"},
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
	// Hunsrik is a German dialect spoken in Brazil.
	{"hunsr", ""}, {"hun", "hu"}, {"ung", "hu"}, {"magy", "hu"},
	{"korea", "ko"}, {"jap", "ja"}, {"rusyn", ""}, {"rusin", ""}, {"rus", "ru"},
	// "Latino" is Latin in Italian but Latin American Spanish in Spanish.
	{"latv", "lv"}, {"lett", "lv"}, {"latino", ""}, {"latg", ""}, {"lat", "la"},
	{"ice", "is"}, {"isl", "is"},
	{"kroat", "hr"}, {"croat", "hr"}, {"hrv", "hr"},
	{"bosn", "bs"}, {"bosan", "bs"},
	{"rumantsch", ""}, {"rumb", ""}, {"rum", "ro"},
	// Romani (Romany, Romanes), Romansh (Romanche, Romancio, Romontsch), Romagnol and
	// romanized Japanese (Romaji, Romanji; also "Romanized", "Romanisch") are no Romanian.
	{"romanian", "ro"}, {"romani", ""}, {"romany", ""}, {"romanes", ""}, {"romansh", ""}, {"romanc", ""}, {"romag", ""},
	{"romaj", ""}, {"romanj", ""}, {"romont", ""}, {"rom", "ro"},
}

// notLanguageNames are folded words that start like a language in languagePrefixes but
// name none, and whose prefix cannot be stopped without stopping a real name ("Roma" and
// "Roman" against "Romana", Romanian in Romanian).
var notLanguageNames = map[string]bool{"roma": true, "roman": true}

// foldLetters folds s (FoldString: lower case, diacritics dropped, "ł" -> "l", "ø" -> "o",
// "ß" -> "ss", ...) and keeps only ASCII letters.
func foldLetters(s string) string {
	var b strings.Builder
	for _, r := range FoldString(s) {
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
		if f == "" || f == "and" || f == "und" || notLanguageNames[f] {
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
