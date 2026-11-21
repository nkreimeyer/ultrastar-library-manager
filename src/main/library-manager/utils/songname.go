package utils

import (
	"fmt"
	"slices"
	"strings"
	"unicode"
	"unicode/utf8"

	"golang.org/x/text/cases"
	"golang.org/x/text/unicode/norm"
)

// folderNameSep separates artist and title in a song folder name ("Artist - Title").
const folderNameSep = " - "

// NameFill is the result of TagsFromFolderName: the #ARTIST and #TITLE values that can be
// filled in from the song folder name.
type NameFill struct {
	Artist, Title         string // values to fill; only meaningful if FillArtist/FillTitle
	FillArtist, FillTitle bool
	// Reason explains why a missing tag cannot be filled (for reports). It is empty if
	// nothing was missing or everything missing can be filled.
	Reason string
}

// TagsFromFolderName decides whether a missing #ARTIST and/or #TITLE can be taken from the
// base name of the song folder. artist and title are the current tag values ("" or only
// whitespace = missing). A tag is only filled when the folder name makes it obvious; when
// in doubt nothing is filled and Reason says why:
//
//   - A non-empty tag is never changed. With both tags present there is nothing to do.
//   - The folder name must be valid UTF-8 without control or invisible format characters
//     (BOM, zero-width space, ...). It is NFC-normalized and trimmed. Artist and title are
//     separated by " - " (space, hyphen-minus, space) only: "AC-DC" or an en dash
//     "Artist – Title" do not separate anything.
//   - Both missing: the name must contain exactly one " - ". Left is the artist, right the
//     title.
//   - An artist part of only digits ("01 - Song", "01. - Song") looks like a track number
//     and is never filled (also when the title is present).
//   - Title present, artist missing: the right part of the FIRST " - " must match the
//     title; the left part is the artist. A filled artist never contains " - ": in
//     "SingStar - Artist - Song" with title "Song" the left part is a prefix (pack name,
//     category, track number), so nothing is filled. "Title - Artist" naming (the title
//     matches a left part), no match or several matches fill nothing.
//   - Artist present, title missing: the left part of a " - " must match the artist; the
//     title is the whole remainder after it ("Artist - Song - Live" gives "Song - Live").
//   - A self-titled song ("Black Sabbath - Black Sabbath") is filled although the known tag
//     matches both parts: with one " - " both sides give the same value.
//   - Matching ignores case (Unicode case folding), Unicode normalization, runs of
//     whitespace, trailing dots (Windows strips them from folder names), typographic
//     apostrophes and quotes (’ ‘ ´ ` “ ” „ count as ' and ") and the characters
//     < > : " / \ | ? * that file names cannot contain on Windows or macOS: the tag side
//     matches with them removed or replaced by "-", "_" or a space (title "What's Up?"
//     matches folder "Artist - What's Up", artist "AC/DC" matches "AC-DC" and "AC_DC").
//     The folder part may additionally end in qualifiers the tag lacks: one or more
//     " [..]" or " (..)" groups ("Artist - Song [DUET]" matches title "Song").
//   - Filled values are the folder parts as written (NFC, trimmed), including any
//     qualifiers. Collision suffixes such as " (2)" are not removed.
func TagsFromFolderName(folder, artist, title string) NameFill {
	needArtist, needTitle := strings.TrimSpace(artist) == "", strings.TrimSpace(title) == ""
	if !needArtist && !needTitle {
		return NameFill{}
	}
	fail := func(format string, args ...any) NameFill {
		return NameFill{Reason: fmt.Sprintf(format, args...)}
	}
	if !utf8.ValidString(folder) {
		return fail("the folder name is not valid UTF-8")
	}
	if strings.IndexFunc(folder, unicode.IsControl) >= 0 {
		return fail("the folder name contains control characters")
	}
	if strings.IndexFunc(folder, func(r rune) bool { return unicode.Is(unicode.Cf, r) }) >= 0 {
		return fail("the folder name contains invisible format characters (BOM, zero-width space, ...)")
	}
	name := strings.TrimSpace(norm.NFC.String(folder))
	splits := splitFolderName(name)
	if len(splits) == 0 {
		return fail("the folder name %q is not \"Artist - Title\" (no %q)", name, folderNameSep)
	}

	switch {
	case needArtist && needTitle:
		if len(splits) > 1 {
			return fail("the folder name %q contains %q %d times: artist and title are ambiguous", name, folderNameSep, len(splits))
		}
		// Both parts are non-empty: name is trimmed, so a " - " is never at either end.
		s := splits[0]
		if looksLikeTrackNumber(s.left) {
			return fail("the artist part %q of the folder name %q looks like a track number", s.left, name)
		}
		return NameFill{Artist: s.left, Title: s.right, FillArtist: true, FillTitle: true}
	case needArtist:
		value, reason := fillOther(name, splits, title, "title", "artist", true)
		if reason != "" {
			return NameFill{Reason: reason}
		}
		return NameFill{Artist: value, FillArtist: true}
	default:
		value, reason := fillOther(name, splits, artist, "artist", "title", false)
		if reason != "" {
			return NameFill{Reason: reason}
		}
		return NameFill{Title: value, FillTitle: true}
	}
}

// folderSplit is the folder name split at one occurrence of folderNameSep.
type folderSplit struct{ left, right string }

// splitFolderName returns every split of name at an occurrence of folderNameSep
// (overlapping occurrences included), with both parts trimmed.
func splitFolderName(name string) []folderSplit {
	var splits []folderSplit
	for i := 0; i+len(folderNameSep) <= len(name); i++ {
		if name[i:i+len(folderNameSep)] == folderNameSep {
			splits = append(splits, folderSplit{
				left:  strings.TrimSpace(name[:i]),
				right: strings.TrimSpace(name[i+len(folderNameSep):]),
			})
		}
	}
	return splits
}

// fillOther finds the value of the missing tag when the other tag (known, named
// knownName) is present. With missingIsLeft the known tag must match the right part of a
// split and the missing value is the left part; otherwise the other way round. It returns
// the value, or a reason why there is none. The parts of a split are never empty (see
// TagsFromFolderName).
func fillOther(name string, splits []folderSplit, known, knownName, missingName string, missingIsLeft bool) (string, string) {
	var values []string
	wrongSide := false
	for _, s := range splits {
		near, far := s.right, s.left // near must match the known tag, far is the value
		if !missingIsLeft {
			near, far = s.left, s.right
		}
		if folderPartMatches(near, known) {
			values = append(values, far)
		}
		if folderPartMatches(far, known) {
			wrongSide = true
		}
	}
	// Self-titled ("Black Sabbath - Black Sabbath"): with a single " - " both sides match
	// and give the same value.
	if len(splits) == 1 && len(values) == 1 && wrongSide {
		wrongSide = false
	}
	switch {
	case len(values) == 1 && !wrongSide && missingIsLeft && strings.Contains(values[0], folderNameSep):
		return "", fmt.Sprintf("the folder name %q has more than one %q before the %s %q: the %s part %q may carry a prefix",
			name, folderNameSep, knownName, known, missingName, values[0])
	case len(values) == 1 && !wrongSide && missingIsLeft && looksLikeTrackNumber(values[0]):
		return "", fmt.Sprintf("the artist part %q of the folder name %q looks like a track number", values[0], name)
	case len(values) == 1 && !wrongSide:
		return values[0], ""
	case len(values) > 1 || (len(values) == 1 && wrongSide):
		return "", fmt.Sprintf("the %s %q matches several parts of the folder name %q", knownName, known, name)
	case wrongSide:
		return "", fmt.Sprintf("the %s %q is on the wrong side of the folder name %q (not \"Artist - Title\")", knownName, known, name)
	}
	return "", fmt.Sprintf("the folder name %q does not contain the %s %q as \"Artist - Title\"", name, knownName, known)
}

// looksLikeTrackNumber reports whether s is only digits, optionally followed by a dot
// ("01", "7.").
func looksLikeTrackNumber(s string) bool {
	s = strings.TrimSuffix(s, ".")
	return s != "" && strings.IndexFunc(s, func(r rune) bool { return r < '0' || r > '9' }) < 0
}

// folderPartMatches reports whether a part of the folder name matches a tag value (see
// TagsFromFolderName). The part may carry trailing " [..]" / " (..)" qualifiers that the
// tag lacks.
func folderPartMatches(part, tag string) bool {
	wants := tagMatchKeys(tag)
	if len(wants) == 0 {
		return false
	}
	for {
		if slices.Contains(wants, matchKey(part, "")) {
			return true
		}
		stripped, ok := stripQualifier(part)
		if !ok {
			return false
		}
		part = stripped
	}
}

// tagMatchKeys returns the match keys of a tag value: the characters file names cannot
// contain removed, or replaced by one of the usual substitutes. Empty keys are left out.
func tagMatchKeys(tag string) []string {
	var keys []string
	for _, repl := range []string{"", "-", "_", " "} {
		if k := matchKey(tag, repl); k != "" && !slices.Contains(keys, k) {
			keys = append(keys, k)
		}
	}
	return keys
}

// stripQualifier removes one trailing " [..]" or " (..)" group from s. It reports false if
// s does not end in one or nothing would remain before it.
func stripQualifier(s string) (string, bool) {
	s = strings.TrimSpace(s)
	var open string
	switch {
	case strings.HasSuffix(s, "]"):
		open = " ["
	case strings.HasSuffix(s, ")"):
		open = " ("
	default:
		return s, false
	}
	i := strings.LastIndex(s, open)
	if i <= 0 {
		return s, false
	}
	rest := strings.TrimSpace(s[:i])
	return rest, rest != ""
}

// fileNameUnsafe are the characters file names cannot contain on Windows or macOS.
const fileNameUnsafe = `<>:"/\|?*`

// asciiQuote maps an apostrophe (IsApostrophe) to "'" and a typographic double quote to
// '"'.
func asciiQuote(r rune) rune {
	switch {
	case IsApostrophe(r):
		return '\''
	case r == '“' || r == '”' || r == '„':
		return '"'
	}
	return r
}

// matchKey normalizes s for folderPartMatches: NFC, case folding, typographic apostrophes
// and quotes folded to ASCII, the characters that are invalid in file names replaced by
// repl ("" removes them), whitespace collapsed and trailing dots removed.
func matchKey(s, repl string) string {
	s = strings.Map(asciiQuote, norm.NFC.String(s))
	var b strings.Builder
	for _, r := range s {
		if strings.ContainsRune(fileNameUnsafe, r) {
			b.WriteString(repl)
		} else {
			b.WriteRune(r)
		}
	}
	// A Caser is stateful and must not be shared between goroutines.
	s = strings.Join(strings.Fields(cases.Fold().String(b.String())), " ")
	return strings.TrimRight(s, ". ")
}
