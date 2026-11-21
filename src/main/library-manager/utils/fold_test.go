package utils

import (
	"testing"
	"unicode/utf8"
)

// TestFoldString covers the mappings of every folding the shared one replaced: organize's
// bucket table (letterFolds, both cases, first letter), organize's key table
// (keyLetterFolds), repairtxt's lrclib matching (baseRune: the NFD base letter) and the
// language names (foldLetters: NFD, lower case). A dropped mapping fails here.
func TestFoldString(t *testing.T) {
	for _, c := range []struct{ in, want string }{
		// organize letterFolds / keyLetterFolds: letters that do not decompose.
		{"Ø", "o"}, {"ø", "o"}, {"Æ", "ae"}, {"æ", "ae"}, {"Œ", "oe"}, {"œ", "oe"},
		{"Ł", "l"}, {"ł", "l"}, {"Đ", "d"}, {"đ", "d"}, {"Ð", "d"}, {"ð", "d"},
		{"Þ", "th"}, {"þ", "th"}, {"ß", "ss"}, {"ẞ", "ss"}, {"ı", "i"},
		{"Ħ", "h"}, {"ħ", "h"}, {"Ŧ", "t"}, {"ŧ", "t"}, {"Ŋ", "n"}, {"ŋ", "n"}, {"ĸ", "k"},
		{"Ɨ", "i"}, {"ɨ", "i"}, {"Ƶ", "z"}, {"ƶ", "z"}, {"Ɓ", "b"}, {"ɓ", "b"},
		{"Ƈ", "c"}, {"ƈ", "c"}, {"Ɗ", "d"}, {"ɗ", "d"}, {"Ƒ", "f"}, {"ƒ", "f"},
		{"Ɠ", "g"}, {"ɠ", "g"}, {"Ƙ", "k"}, {"ƙ", "k"}, {"Ƥ", "p"}, {"ƥ", "p"},
		{"Ƭ", "t"}, {"ƭ", "t"}, {"Ʋ", "v"}, {"ʋ", "v"}, {"Ȥ", "z"}, {"ȥ", "z"},
		{"Ŀa", "l·a"}, {"ᴭ", "ae"}, {"ꟸ", "h"},
		// Decomposing letters (baseRune, foldLetters, the bucket's NFKD).
		{"Désolé", "desole"}, {"Ä", "a"}, {"ç", "c"}, {"İ", "i"}, {"ǅemal", "dzemal"},
		{"Ĳssel", "ijssel"}, {"Ｆ", "f"}, {"ﬁ", "fi"}, {"Español", "espanol"},
		{"Ελληνικά", "ελληνικα"}, {"ΆΣ", "ασ"},
		// Marks of other scripts make different letters.
		{"й", "й"}, {"Й", "й"}, {"ゲ", "ゲ"}, {"한국어", "한국어"},
		// A mark after a non-letter is dropped; everything else is kept.
		{"á", "a"}, {"1́", "1"}, {"Mø & X", "mo & x"}, {"don’t", "don’t"},
	} {
		if got := FoldString(c.in); got != c.want {
			t.Errorf("FoldString(%q) = %q, want %q", c.in, got, c.want)
		}
	}
	for r, f := range letterFolds {
		if got := FoldString(string(r)); got != f {
			t.Errorf("FoldString(%q) = %q, want %q", r, got, f)
		}
		if !utf8.ValidString(f) || f == "" {
			t.Errorf("letterFolds[%q] = %q", r, f)
		}
	}
}

func TestIsApostrophe(t *testing.T) {
	for _, r := range "'`’‘´ʼ" {
		if !IsApostrophe(r) {
			t.Errorf("IsApostrophe(%q) = false", r)
		}
	}
	for _, r := range "\"“”„ʹ′-.a" {
		if IsApostrophe(r) {
			t.Errorf("IsApostrophe(%q) = true", r)
		}
	}
}
