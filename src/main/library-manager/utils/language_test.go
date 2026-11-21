package utils

import (
	"reflect"
	"strings"
	"testing"
)

func TestLanguageCodes(t *testing.T) {
	for in, want := range map[string][]string{
		"German":           {"de"},
		"Deutsch (german)": {"de"},
		"Englisch":         {"en"},
		"Espa\uFFFDol":     {"es"},
		"Français":         {"fr"},
		"German/English":   {"de", "en"},
		"English, German":  {"en", "de"},
		"Schwedisch":       {"sv"},
		"Deutsch":          {"de"},
		"Latvian":          {"lv"},
		"Latviešu":         {"lv"},
		"Lettisch":         {"lv"},
		"Latin":            {"la"},
		"Latein":           {"la"},
		"Slovenčina":       {"sk"},
		"Slovak":           {"sk"},
		"Slovenian":        {"sl"},
		"Čeština":          {"cs"},
		"Slovenščina":      {"sl"},
		"Slowenisch":       {"sl"},
		// Names that start like another language get their own code, or none.
		"Esperanto":        {"eo"},
		"Español":          {"es"},
		"Espanhol":         {"es"},
		"Frisian":          {"fy"},
		"Friesisch":        {"fy"},
		"Frysk":            {"fy"},
		"French":           {"fr"},
		"Französisch":      {"fr"},
		"Friulian":         nil,
		"Friaulisch":       nil,
		"Latino":           nil,
		"Español (Latino)": {"es"},
		"Latina":           {"la"},
		"Polynesian":       nil,
		"Polyglot":         nil,
		"Polaco":           {"pl"},
		"Polonais":         {"pl"},
		"Polski":           {"pl"},
		"Turkmen":          nil,
		"Türkçe":           {"tr"},
		"Romanian":         {"ro"},
		"Română":           {"ro"},
		"Rumänisch":        {"ro"},
		"Romani":           nil,
		"Romansh":          nil,
		"Romanche":         nil,
		"Romancio":         nil,
		"Romany":           nil,
		"Romagnolo":        nil,
		"Romeno":           {"ro"},
		"Rumano":           {"ro"},
		"Rumantsch":        nil,
		"German/Esperanto": {"de", "eo"},
		"Niederländisch":   {"nl"},
		"Niederdeutsch":    nil,
		"Niedersächsisch":  nil,
		"Tschechisch":      {"cs"},
		"Tschetschenisch":  nil,
		"Koreanisch":       {"ko"},
		"Korean":           {"ko"},
		"Kornisch":         nil,
		"Russisch":         {"ru"},
		"Ruso":             {"ru"},
		"Rusyn":            nil,
		"Latgalian":        nil,
		"Fränkisch":        nil,
		"Other":            nil,
		"":                 nil,
	} {
		if got := LanguageCodes(in); !reflect.DeepEqual(got, want) {
			t.Errorf("LanguageCodes(%q) = %v, want %v", in, got, want)
		}
	}
}

// TestLanguagePrefixOrder: the first matching prefix wins, so no prefix may follow a
// shorter one it starts with (it could never match), and none may be listed twice.
func TestLanguagePrefixOrder(t *testing.T) {
	for i, a := range languagePrefixes {
		for _, b := range languagePrefixes[i+1:] {
			if strings.HasPrefix(b.prefix, a.prefix) {
				t.Errorf("prefix %q (%s) comes after %q (%s) and never matches", b.prefix, b.code, a.prefix, a.code)
			}
		}
	}
}
