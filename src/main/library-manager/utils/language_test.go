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
		"Bosnian":          {"bs"},
		"Bosanski":         {"bs"},
		"Bosnisch":         {"bs"},
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

// TestIsCentralEuropean: every name the old exact-name map (folded: lower case, no
// diacritics) accepted is still a CP1250 language, in its folded and its written form;
// lists and qualifiers LanguageCodes splits are recognized too; names that only start
// like a CP1250 language are not.
func TestIsCentralEuropean(t *testing.T) {
	for _, name := range []string{
		// The old centralEuropean map, entry by entry.
		"polish", "polski", "polnisch",
		"czech", "cestina", "tschechisch",
		"slovak", "slovencina", "slowakisch",
		"hungarian", "magyar", "ungarisch",
		"croatian", "hrvatski", "kroatisch",
		"slovenian", "slovene", "slovenscina", "slowenisch",
		"romanian", "romana", "rumanisch",
		"bosnian", "bosanski", "bosnisch",
		// Their written forms.
		"Polish", "Polski", "Čeština", "Slovenčina", "Slovenščina", "Magyar", "Hrvatski",
		"Română", "Rumänisch", "Bosanski", " Slovak ", "English, Polish", "German/Czech",
		"English;Hungarian",
		// Lists and qualifiers the old ",", "/", ";" split did not separate.
		"Polish & English", "Polski (PL)", "Polish English", "Polnisch (Polen)",
		"English + Croatian", "Czech | English", "Bosnian and English",
	} {
		if !isCentralEuropean(name) {
			t.Errorf("isCentralEuropean(%q) = false, want true", name)
		}
	}
	for _, name := range []string{
		"", "English", "German, English", "Esperanto", "Polynesian", "Polyglot",
		"Romani", "Romany", "Romanes", "Romansh", "Rumantsch", "Romanche", "Romancio",
		"Romagnolo", "Niederdeutsch", "Niedersächsisch", "Tschetschenisch", "Kornisch",
		"Rusyn", "Latgalian", "Turkmen", "Serbo-Croatian", "Other",
	} {
		if isCentralEuropean(name) {
			t.Errorf("isCentralEuropean(%q) = true, want false", name)
		}
	}
}

// TestDecodeTxtCP1250ByLanguage: a txt that is not UTF-8 is read as CP1250 when its
// #LANGUAGE names a CP1250 language, also in a list with blanks, "&" or a "(…)" qualifier,
// and when the name itself is written in CP1250.
func TestDecodeTxtCP1250ByLanguage(t *testing.T) {
	for language, want := range map[string]string{
		"Polish & English": EncCP1250,
		"Polski (PL)":      EncCP1250,
		"Polish English":   EncCP1250,
		"Polnisch (Polen)": EncCP1250,
		"Bosanski":         EncCP1250,
		"\xc8e\x9atina":    EncCP1250, // "Čeština" in CP1250
		"Sloven\xe8ina":    EncCP1250, // "Slovenčina" in CP1250
		"Rum\xe4nisch":     EncCP1250, // "Rumänisch" in CP1250 and CP1252
		"Rom\xe2n\xe3":     EncCP1250, // "Română" in CP1250
		"English & German": EncCP1252,
		"Romanche":         EncCP1252,
		"Romany":           EncCP1252,
		"Niederdeutsch":    EncCP1252,
		"Tschetschenisch":  EncCP1252,
		"Kornisch":         EncCP1252,
		// Read as it is, "Románi" would be "Romni" (Romanian); only its decoded form counts.
		"Rom\xe1ni": EncCP1252,
	} {
		raw := "#LANGUAGE:" + language + "\n#TITLE:\xb9\n"
		if got := DecodeTxt([]byte(raw)); got.Encoding != want || got.Problem != "" {
			t.Errorf("DecodeTxt(%q) = %s, problem=%q; want %s", raw, got.Encoding, got.Problem, want)
		}
	}
}
