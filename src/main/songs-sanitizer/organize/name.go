// Package organize builds the final library layout (one folder per starting character)
// from any number of source folders, deduplicating songs across them.
package organize

import (
	"strings"
	"unicode"
	"unicode/utf8"

	"golang.org/x/text/cases"
	"golang.org/x/text/unicode/norm"
)

// Bucket names besides the letters "A".."Z".
const (
	BucketDigits = "0-9"
	BucketOther  = "#"
)

// MaxFolderNameBytes is the maximum length of a name built by FolderName, in bytes. It
// leaves room below the 255-byte limit of common file systems for collision suffixes such
// as " (2)" and for the temporary names used while moving.
const MaxFolderNameBytes = 180

// letterFolds maps letters that do not decompose under Unicode normalization to the Latin
// base letter used for bucketing. Upper and lower case are both listed so the map can be
// applied before upper-casing.
var letterFolds = map[rune]rune{
	'Ø': 'O', 'ø': 'O',
	'Æ': 'A', 'æ': 'A',
	'Œ': 'O', 'œ': 'O',
	'Ł': 'L', 'ł': 'L',
	'Đ': 'D', 'đ': 'D',
	'Ð': 'D', 'ð': 'D',
	'Þ': 'T', 'þ': 'T',
	'ß': 'S', 'ẞ': 'S',
	'ı': 'I',
	'Ħ': 'H', 'ħ': 'H',
	'Ŧ': 'T', 'ŧ': 'T',
	'Ŋ': 'N', 'ŋ': 'N',
	'ĸ': 'K',
	'Ŀ': 'L', 'ŀ': 'L',
	'Ɨ': 'I', 'ɨ': 'I',
	'Ƶ': 'Z', 'ƶ': 'Z',
	'Ɓ': 'B', 'ɓ': 'B',
	'Ƈ': 'C', 'ƈ': 'C',
	'Ɗ': 'D', 'ɗ': 'D',
	'Ƒ': 'F', 'ƒ': 'F',
	'Ɠ': 'G', 'ɠ': 'G',
	'Ƙ': 'K', 'ƙ': 'K',
	'Ƥ': 'P', 'ƥ': 'P',
	'Ƭ': 'T', 'ƭ': 'T',
	'Ʋ': 'V', 'ʋ': 'V',
	'Ȥ': 'Z', 'ȥ': 'Z',
}

// Bucket returns the top-level library folder for a song by its #ARTIST: "A".."Z",
// BucketDigits ("0-9") or BucketOther ("#").
//
//   - Leading characters that are neither letters nor digits (whitespace, punctuation,
//     symbols) are skipped: "'Til Tuesday" -> "T", ".38 Special" -> "0-9", "(hed) p.e." -> "H".
//   - With ignoreThe, a leading "The " (case-insensitive) is skipped when a letter or digit
//     follows it: "The Beatles" -> "B", "The 1975" -> "0-9". "The" alone, "Theory" and
//     "The !!!" keep their "T".
//   - The first letter is folded to its Latin base letter: compatibility decomposition with
//     combining marks dropped ("Ä" -> "A", "Ｆ" -> "F", "ﬁ" -> "F") plus letterFolds for
//     letters that do not decompose ("Ø" -> "O", "ß" -> "S", "Ł" -> "L", ...), then
//     upper-cased.
//   - A decimal digit of any script (unicode.IsDigit) -> "0-9".
//   - Anything else (non-Latin scripts, no letter or digit at all, empty) -> "#".
func Bucket(artist string, ignoreThe bool) string {
	s := skipToAlnum(norm.NFC.String(strings.ToValidUTF8(artist, "")))
	if ignoreThe {
		if rest, ok := cutLeadingThe(s); ok {
			s = rest
		}
	}
	r, _ := utf8.DecodeRuneInString(s)
	if s == "" {
		return BucketOther
	}
	if unicode.IsDigit(r) {
		return BucketDigits
	}
	if l := foldLetter(r); l >= 'A' && l <= 'Z' {
		return string(l)
	}
	return BucketOther
}

// isAlnum reports whether r is a letter or a decimal digit.
func isAlnum(r rune) bool { return unicode.IsLetter(r) || unicode.IsDigit(r) }

// skipToAlnum drops everything before the first letter or digit ("" if there is none).
func skipToAlnum(s string) string {
	if i := strings.IndexFunc(s, isAlnum); i >= 0 {
		return s[i:]
	}
	return ""
}

// cutLeadingThe removes a leading "The" followed by whitespace and returns the rest,
// starting at its first letter or digit. ok is false if s does not start with "The " or
// nothing alphanumeric follows.
func cutLeadingThe(s string) (string, bool) {
	if len(s) < 4 || !strings.EqualFold(s[:3], "the") {
		return s, false
	}
	r, _ := utf8.DecodeRuneInString(s[3:])
	if !unicode.IsSpace(r) {
		return s, false
	}
	rest := skipToAlnum(s[3:])
	if rest == "" {
		return s, false
	}
	return rest, true
}

// foldLetter maps a letter to an upper-case Latin base letter if it has one; other runes
// come back upper-cased (and are then not in A-Z).
func foldLetter(r rune) rune {
	for _, d := range norm.NFKD.String(string(r)) {
		if unicode.Is(unicode.Mn, d) {
			continue
		}
		if f, ok := letterFolds[d]; ok {
			return f
		}
		return unicode.ToUpper(d)
	}
	return unicode.ToUpper(r)
}

// Placeholders used by FolderName for an artist or title that is empty after sanitizing.
const (
	unknownArtist = "Unknown Artist"
	unknownTitle  = "Unknown Title"
)

// FolderName builds the song folder name "Artist - Title" from the tag values (trimmed,
// NFC). The name is safe on Windows, macOS and Linux:
//
//   - '/', '\' and '|' become '-' ("AC/DC" -> "AC-DC");
//   - ':' becomes '-' between two non-space characters ("Re:Zero" -> "Re-Zero") and is
//     dropped otherwise ("Title: Sub" -> "Title Sub"), so it never adds a " - " separator;
//   - '?', '*', '<' and '>' are dropped, '"' becomes an apostrophe;
//   - control and format characters (BOM, zero-width space, ...) are dropped, every other
//     whitespace becomes a space, runs of spaces are collapsed;
//   - leading dots (a hidden folder on Linux/macOS, skipped by discovery) and trailing dots
//     and spaces of the whole name (stripped by Windows) are trimmed;
//   - a Windows reserved device name (CON, PRN, AUX, NUL, COM1-9, LPT1-9, also with an
//     extension such as "CON.x") gets a '_' after the reserved part;
//   - at most MaxFolderNameBytes bytes, cut at a rune boundary: the title is shortened
//     first, the artist only when it alone leaves too little room.
//
// An artist or title that is empty after sanitizing is replaced by "Unknown Artist" or
// "Unknown Title", so the result is never empty, "." or "..". Qualifiers like "[DUET]" or
// "(Live)" are kept as written.
func FolderName(artist, title string) string {
	// Leading dots of the artist would hide the folder (discovery skips hidden folders);
	// trailing dots of the artist are kept ("R.E.M. - Title"), those of the title end the
	// name and are trimmed.
	a := strings.TrimLeft(sanitizePart(artist), ". ")
	t := strings.TrimRight(sanitizePart(title), ". ")
	if a == "" {
		a = unknownArtist
	}
	if t == "" {
		t = unknownTitle
	}
	// The name contains " - ", so it can only be a reserved device name when the artist
	// has a dot ("CON.x - y" is reserved like "CON.txt"). Fixing this before the length
	// cut keeps the result within MaxFolderNameBytes.
	if strings.Contains(a, ".") {
		a = avoidReserved(a)
	}
	const sep = " - "
	if len(a)+len(sep)+len(t) > MaxFolderNameBytes {
		room := MaxFolderNameBytes - len(sep) - len(a)
		if minTitle := min(len(t), 40); room < minTitle {
			// The artist alone is too long: keep 40 bytes of the title.
			room = minTitle
			a = strings.TrimRight(cutBytes(a, MaxFolderNameBytes-len(sep)-room), " ")
		}
		// The artist starts with a character that is neither a space nor a dot, so it
		// cannot become empty above.
		if t = strings.TrimRight(cutBytes(t, room), ". "); t == "" {
			t = "_" // only a title of 40+ leading dots
		}
	}
	return a + sep + t
}

// sanitizePart applies the per-character rules of FolderName to one tag value and
// collapses whitespace. The result is trimmed of spaces (not of dots).
func sanitizePart(s string) string {
	runes := []rune(norm.NFC.String(strings.ToValidUTF8(s, "")))
	var b strings.Builder
	for i, r := range runes {
		switch {
		case r == '/' || r == '\\' || r == '|':
			b.WriteRune('-')
		case r == ':':
			if i > 0 && i < len(runes)-1 && !unicode.IsSpace(runes[i-1]) && !unicode.IsSpace(runes[i+1]) {
				b.WriteRune('-')
			}
		case r == '?' || r == '*' || r == '<' || r == '>':
		case r == '"':
			b.WriteRune('\'')
		case unicode.IsSpace(r):
			b.WriteRune(' ')
		case unicode.IsControl(r) || unicode.Is(unicode.Cf, r):
		default:
			b.WriteRune(r)
		}
	}
	return strings.Join(strings.Fields(b.String()), " ")
}

// cutBytes shortens s to at most n bytes without splitting a rune.
func cutBytes(s string, n int) string {
	if n <= 0 {
		return ""
	}
	if len(s) <= n {
		return s
	}
	for n > 0 && !utf8.RuneStart(s[n]) {
		n--
	}
	return s[:n]
}

// reservedNames are the Windows device names that cannot be used as a file or folder
// name, with or without an extension.
var reservedNames = func() map[string]bool {
	m := map[string]bool{"CON": true, "PRN": true, "AUX": true, "NUL": true, "CONIN$": true, "CONOUT$": true}
	for _, p := range []string{"COM", "LPT"} {
		for _, d := range []string{"1", "2", "3", "4", "5", "6", "7", "8", "9", "¹", "²", "³"} {
			m[p+d] = true
		}
	}
	return m
}()

// avoidReserved inserts '_' after a Windows reserved device name at the start of name
// ("CON" -> "CON_", "nul.x - y" -> "nul_.x - y"). Windows ignores trailing spaces of the
// part before the first dot, so "CON .x" is reserved too.
func avoidReserved(name string) string {
	stem, _, _ := strings.Cut(name, ".")
	trimmed := strings.TrimRight(stem, " ")
	if !reservedNames[strings.ToUpper(trimmed)] {
		return name
	}
	return trimmed + "_" + name[len(trimmed):]
}

// NameKey returns a case-insensitive and normalization-insensitive key of a folder name,
// to detect collisions the way case-insensitive file systems (Windows, macOS) see them.
// It uses full Unicode case folding, which is slightly stricter than those file systems
// ("Straße" and "STRASSE" share a key): a false collision only adds a " (2)" suffix.
func NameKey(name string) string {
	return norm.NFC.String(cases.Fold().String(norm.NFC.String(name)))
}
