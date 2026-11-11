package utils

import (
	"bytes"
	"fmt"
	"strings"
	"unicode"
	"unicode/utf8"

	"golang.org/x/text/encoding/charmap"
	xunicode "golang.org/x/text/encoding/unicode"
	"golang.org/x/text/unicode/norm"
)

// Names of the encodings DecodeTxt can detect. Single-byte codecs named by an #ENCODING
// tag are reported under their canonical name (e.g. "CP1251", "ISO-8859-2").
const (
	EncUTF8    = "UTF-8"
	EncUTF16LE = "UTF-16LE"
	EncUTF16BE = "UTF-16BE"
	EncCP1250  = "CP1250"
	EncCP1252  = "CP1252"
	EncLatin1  = "ISO-8859-1"
)

var (
	utf8BOM    = []byte{0xEF, 0xBB, 0xBF}
	utf16LEBOM = []byte{0xFF, 0xFE}
	utf16BEBOM = []byte{0xFE, 0xFF}
)

// DecodedTxt is the result of DecodeTxt.
type DecodedTxt struct {
	// Text is the decoded content without BOM and with "\n" line endings.
	Text string
	// Encoding is the encoding the file was read as.
	Encoding string
	// Problem is non-empty if the file could not be decoded unambiguously. Such a file must
	// not be rewritten: Text is only a best-effort reading for matching purposes.
	Problem string
	// Damaged reports that the (otherwise cleanly decoded) text contains U+FFFD, i.e. the
	// file was damaged before, for example by an earlier version of this tool.
	Damaged bool
}

// DecodeTxt decodes the raw content of a song txt without losing data. The guiding rule is
// "when in doubt, set Problem": a file with a Problem is left untouched and reported.
//
//  1. A UTF-16 BOM selects UTF-16.
//  2. Valid UTF-8 is UTF-8, except:
//     - with an #ENCODING tag naming a single-byte codec, every non-ASCII character must be
//     one that codec can produce (a converted file with a stale tag); otherwise Problem;
//     - without a tag, CP1252 text that is valid UTF-8 only by accident is read as CP1252
//     if that is certain (an isolated "ß…" = 0xDF 0x85 reads as an NKo character) and is a
//     Problem if it is merely likely (only isolated non-Latin characters amid ASCII).
//  3. Otherwise the single-byte codec named by #ENCODING is used, else CP1250 if #LANGUAGE
//     names a Central European language, else CP1252. It fails (Problem) on bytes the codec
//     leaves undefined, on real UTF-8 sequences in the file (mixed encodings, mojibake such
//     as "Ã¤"), and when CP1252 text looks like CP1250 ("Za¿ó³æ").
//  4. An unknown #ENCODING codec is always a Problem, and so is a file with NUL bytes.
//     BOM-less UTF-16 is decoded as such, so that its header can still be read; any other
//     file with NUL bytes is decoded by steps 2 and 3 (a valid UTF-8 file padded with zeros
//     keeps "Beyoncé" readable for callers that match tag values despite the Problem).
func DecodeTxt(raw []byte) DecodedTxt {
	var d DecodedTxt
	hint := encodingHint(raw)
	hasNUL := bytes.IndexByte(raw, 0) >= 0
	var utf16Order xunicode.Endianness
	var utf16Enc string
	var bomless bool
	if hasNUL {
		utf16Order, utf16Enc, bomless = bomlessUTF16(raw)
	}
	switch {
	case bytes.HasPrefix(raw, utf16LEBOM), bytes.HasPrefix(raw, utf16BEBOM):
		order, enc := xunicode.LittleEndian, EncUTF16LE
		if bytes.HasPrefix(raw, utf16BEBOM) {
			order, enc = xunicode.BigEndian, EncUTF16BE
		}
		out, err := xunicode.UTF16(order, xunicode.IgnoreBOM).NewDecoder().Bytes(raw[2:])
		d = DecodedTxt{Text: string(out), Encoding: enc}
		if err != nil || bytes.ContainsRune(out, utf8.RuneError) || len(raw)%2 != 0 {
			d.Problem = "invalid UTF-16 data"
		}
	case hasNUL && bomless:
		out, _ := xunicode.UTF16(utf16Order, xunicode.IgnoreBOM).NewDecoder().Bytes(raw)
		d = DecodedTxt{Text: string(out), Encoding: utf16Enc, Problem: "looks like UTF-16 without a BOM; check manually"}
	case hint.unknown != "":
		d = DecodedTxt{Text: decodeLatin1(bytes.TrimPrefix(raw, utf8BOM)), Encoding: EncLatin1,
			Problem: fmt.Sprintf("declares #ENCODING:%s, which is not supported", hint.unknown)}
	case utf8.Valid(raw):
		raw = bytes.TrimPrefix(raw, utf8BOM)
		d = DecodedTxt{Text: string(raw), Encoding: EncUTF8}
		d.Damaged = strings.ContainsRune(d.Text, utf8.RuneError)
		switch {
		case hint.cm != nil:
			if !producibleBy(hint.cm, d.Text) {
				d.Problem = fmt.Sprintf("declares #ENCODING:%s but is valid UTF-8 with characters %s cannot represent; check which one is right", hint.name, hint.name)
			}
		default:
			accidental, evidence := classifyUTF8Sequences(raw)
			switch {
			case accidental == 0:
				// Real UTF-8 (or ASCII).
			case evidence == 0 && hasAccidentalUTF8(d.Text):
				// Syriac/Thaana/NKo amid Latin text: CP1252 "ß…" and similar.
				d = decodeWith(charmap.Windows1252, EncCP1252, raw)
			case evidence == 0:
				d.Problem = "is valid UTF-8, but its only non-ASCII characters are isolated ones that are more likely CP1252 (e.g. \"é »\" read as \"頻\"); check manually"
			default:
				if hasAccidentalUTF8(d.Text) {
					d.Problem = "contains Syriac/Thaana/NKo characters that are probably CP1252 text read as UTF-8, mixed with real UTF-8; check manually"
				}
			}
		}
	default:
		raw = bytes.TrimPrefix(raw, utf8BOM)
		cm, name := charmap.Windows1252, EncCP1252
		switch {
		case hint.cm != nil:
			cm, name = hint.cm, hint.name
		case centralEuropeanFile(raw):
			cm, name = charmap.Windows1250, EncCP1250
		}
		d = decodeWith(cm, name, raw)
		if d.Problem == "" && hasMixedUTF8(raw, hint) {
			d.Problem = "mixes UTF-8 with another encoding (or contains mojibake such as \"Ã¤\")"
		}
		if d.Problem == "" && hint.cm == nil && name == EncCP1252 && looksLikeCP1250(d.Text) {
			d.Problem = "looks like CP1250 (Central European) text read as CP1252; add #ENCODING:CP1250 or #LANGUAGE"
		}
	}
	if hasNUL && d.Problem == "" && d.Encoding != EncUTF16LE && d.Encoding != EncUTF16BE {
		d.Problem = "contains NUL bytes"
	}

	d.Text = strings.TrimPrefix(d.Text, "\ufeff")
	d.Text = strings.ReplaceAll(d.Text, "\r\n", "\n")
	d.Text = strings.ReplaceAll(d.Text, "\r", "\n")
	return d
}

// decodeWith decodes raw with a single-byte codec; bytes the codec leaves undefined (which
// x/text silently turns into U+FFFD) make it a Problem, with a lossless Latin-1 reading.
func decodeWith(cm *charmap.Charmap, name string, raw []byte) DecodedTxt {
	decoded, err := cm.NewDecoder().Bytes(raw)
	if err != nil || bytes.ContainsRune(decoded, utf8.RuneError) {
		return DecodedTxt{Text: decodeLatin1(raw), Encoding: EncLatin1,
			Problem: fmt.Sprintf("contains bytes that are undefined in %s", name)}
	}
	return DecodedTxt{Text: string(decoded), Encoding: name}
}

// bomlessUTF16 reports whether raw (which has no UTF-16 BOM) looks like UTF-16 without
// one: if more than half of the byte pairs have a NUL at the odd (even) offset, and more of
// them than at the other offset, it is UTF-16LE (BE). No song txt in UTF-8 or a single-byte
// codec has NUL bytes, so the caller only asks for files that contain some.
func bomlessUTF16(raw []byte) (xunicode.Endianness, string, bool) {
	var nul [2]int // NUL bytes at even and odd offsets
	for i, b := range raw {
		if b == 0 {
			nul[i%2]++
		}
	}
	pairs := len(raw) / 2
	switch {
	case nul[1]*2 > pairs && nul[1] > nul[0]:
		return xunicode.LittleEndian, EncUTF16LE, true
	case nul[0]*2 > pairs && nul[0] > nul[1]:
		return xunicode.BigEndian, EncUTF16BE, true
	}
	return xunicode.LittleEndian, "", false
}

// hint is the codec named by an #ENCODING tag.
type hint struct {
	name    string           // canonical name, e.g. "CP1251"
	cm      *charmap.Charmap // nil if there is no tag or it says UTF-8/AUTO/LOCALE
	unknown string           // the tag's value if it names an unsupported codec
}

// singleByteCodecs maps normalized #ENCODING values (upper case, without "-", "_" and
// spaces) to codecs. "LATIN1"/"ISO88591" files almost always are CP1252 (as browsers
// assume): 0x80-0x9F are its typographic quotes and dashes, not C1 control characters.
var singleByteCodecs = map[string]struct {
	name string
	cm   *charmap.Charmap
}{
	"CP1250": {EncCP1250, charmap.Windows1250}, "WINDOWS1250": {EncCP1250, charmap.Windows1250},
	"CP1251": {"CP1251", charmap.Windows1251}, "WINDOWS1251": {"CP1251", charmap.Windows1251},
	"CP1252": {EncCP1252, charmap.Windows1252}, "WINDOWS1252": {EncCP1252, charmap.Windows1252},
	"CP1253": {"CP1253", charmap.Windows1253}, "WINDOWS1253": {"CP1253", charmap.Windows1253},
	"CP1254": {"CP1254", charmap.Windows1254}, "WINDOWS1254": {"CP1254", charmap.Windows1254},
	"CP1255": {"CP1255", charmap.Windows1255}, "WINDOWS1255": {"CP1255", charmap.Windows1255},
	"CP1256": {"CP1256", charmap.Windows1256}, "WINDOWS1256": {"CP1256", charmap.Windows1256},
	"CP1257": {"CP1257", charmap.Windows1257}, "WINDOWS1257": {"CP1257", charmap.Windows1257},
	"CP1258": {"CP1258", charmap.Windows1258}, "WINDOWS1258": {"CP1258", charmap.Windows1258},
	"ISO88591": {EncCP1252, charmap.Windows1252}, "LATIN1": {EncCP1252, charmap.Windows1252},
	"ISO88592": {"ISO-8859-2", charmap.ISO8859_2}, "LATIN2": {"ISO-8859-2", charmap.ISO8859_2},
	"ISO88595": {"ISO-8859-5", charmap.ISO8859_5},
	"ISO88597": {"ISO-8859-7", charmap.ISO8859_7},
	"ISO88599": {"ISO-8859-9", charmap.ISO8859_9}, "LATIN5": {"ISO-8859-9", charmap.ISO8859_9},
	"ISO885915": {"ISO-8859-15", charmap.ISO8859_15}, "LATIN9": {"ISO-8859-15", charmap.ISO8859_15},
	"KOI8R": {"KOI8-R", charmap.KOI8R}, "KOI8U": {"KOI8-U", charmap.KOI8U},
}

// encodingHint reads the #ENCODING header tag. The header is ASCII in every supported
// encoding, so it can be read from the raw bytes. Lines may end in "\n", "\r\n" or "\r".
func encodingHint(raw []byte) hint {
	for _, line := range splitLines(string(raw)) {
		line = strings.TrimSpace(strings.TrimPrefix(line, "\ufeff"))
		if line == "" {
			continue
		}
		if !strings.HasPrefix(line, "#") {
			break
		}
		name, value, ok := strings.Cut(line[1:], ":")
		if !ok || !strings.EqualFold(strings.TrimSpace(name), "ENCODING") {
			continue
		}
		value = strings.TrimSpace(value)
		key := strings.NewReplacer("-", "", "_", "", " ", "").Replace(strings.ToUpper(value))
		switch key {
		case "", "UTF8", "AUTO", "LOCALE":
			return hint{}
		}
		if c, ok := singleByteCodecs[key]; ok {
			return hint{name: c.name, cm: c.cm}
		}
		return hint{unknown: value}
	}
	return hint{}
}

// nonLatinCodecs are the canonical names of the single-byte codecs whose letters are all
// non-ASCII (Cyrillic, Greek, Hebrew, Arabic). See hasMixedUTF8.
var nonLatinCodecs = map[string]bool{
	"CP1251": true, "CP1253": true, "CP1255": true, "CP1256": true,
	"ISO-8859-5": true, "ISO-8859-7": true, "KOI8-R": true, "KOI8-U": true,
}

// centralEuropean are #LANGUAGE values (English, native and German names, lower case and
// without diacritics, see foldLanguage) of languages whose legacy Windows encoding is CP1250.
var centralEuropean = map[string]bool{
	"polish": true, "polski": true, "polnisch": true,
	"czech": true, "cestina": true, "tschechisch": true,
	"slovak": true, "slovencina": true, "slowakisch": true,
	"hungarian": true, "magyar": true, "ungarisch": true,
	"croatian": true, "hrvatski": true, "kroatisch": true,
	"slovenian": true, "slovene": true, "slovenscina": true, "slowenisch": true,
	"romanian": true, "romana": true, "rumanisch": true,
	"bosnian": true, "bosanski": true, "bosnisch": true,
}

// centralEuropeanFile reports whether the #LANGUAGE tag of raw (a file that is not valid
// UTF-8) names a Central European language. The value is still in the file's encoding, so
// it is tried as it is (ASCII, or UTF-8) and decoded as CP1250, the codec in question ("ä"
// in "Rumänisch" is the same byte in CP1250 and CP1252).
func centralEuropeanFile(raw []byte) bool {
	language := HeaderTags(string(raw))["LANGUAGE"]
	if isCentralEuropean(language) {
		return true
	}
	decoded, err := charmap.Windows1250.NewDecoder().String(language)
	return err == nil && isCentralEuropean(decoded)
}

// isCentralEuropean reports whether one of the ",", "/" or ";" separated languages names
// a Central European language.
func isCentralEuropean(language string) bool {
	for _, l := range strings.FieldsFunc(language, func(r rune) bool { return r == ',' || r == '/' || r == ';' }) {
		if centralEuropean[foldLanguage(l)] {
			return true
		}
	}
	return false
}

// foldLanguage trims, lower-cases and removes the diacritics of a language name
// ("Čeština" → "cestina").
func foldLanguage(s string) string {
	var b strings.Builder
	for _, r := range norm.NFD.String(strings.ToLower(strings.TrimSpace(s))) {
		if !unicode.Is(unicode.Mn, r) {
			b.WriteRune(r)
		}
	}
	return b.String()
}

// looksLikeCP1250 reports whether CP1252-decoded text contains a letter, then one of
// "¹ ³ ¿ Ÿ ¥" (CP1252 readings of the CP1250 bytes for ą ł ż ź Ą), then a letter, as in
// "Za¿ó³æ" for "Zażółć". Real CP1252 text never has these characters inside words.
func looksLikeCP1250(text string) bool {
	runes := []rune(text)
	for i := 1; i+1 < len(runes); i++ {
		switch runes[i] {
		case '¹', '³', '¿', 'Ÿ', '¥':
			if unicode.IsLetter(runes[i-1]) && unicode.IsLetter(runes[i+1]) {
				return true
			}
		}
	}
	return false
}

// producibleBy reports whether every non-ASCII character of text exists in the codec.
func producibleBy(cm *charmap.Charmap, text string) bool {
	for _, r := range text {
		if r < 0x80 {
			continue
		}
		if _, ok := cm.EncodeRune(r); !ok {
			return false
		}
	}
	return true
}

// hasAccidentalUTF8 reports whether text contains characters from U+0700-U+07FF (Syriac,
// Thaana, NKo). CP1252 bytes "Ü"/"Ý"/"Þ"/"ß" followed by punctuation such as "…" or "“"
// decode to exactly this range when read as UTF-8.
func hasAccidentalUTF8(text string) bool {
	for _, r := range text {
		if r >= 0x700 && r <= 0x7FF {
			return true
		}
	}
	return false
}

// decodeLatin1 decodes raw as ISO-8859-1, where every byte is the code point of the same value.
func decodeLatin1(raw []byte) string {
	var sb strings.Builder
	sb.Grow(len(raw) * 2)
	for _, b := range raw {
		sb.WriteRune(rune(b))
	}
	return sb.String()
}

// classifyUTF8Sequences counts the valid UTF-8 multi-byte sequences in raw.
//
// accidental are sequences that ordinary CP1252 text produces by chance: an *isolated*
// (ASCII on both sides) 2-byte sequence with lead 0xC6-0xDF (a capital accented letter or
// "ß" followed by punctuation, "ß…") or 3-byte sequence with lead 0xE0-0xEF other than 0xE2
// (a lowercase accented letter followed by two punctuation bytes, French "é »" with NBSP).
// Read as UTF-8 they would be a single Greek/Cyrillic/CJK/... character amid Latin text.
//
// evidence are all other sequences, which are real UTF-8 in practice: runs of adjacent
// sequences (words in Cyrillic, Greek, CJK, ...), leads 0xC2-0xC5 (Latin-1 and Latin
// Extended-A characters, including the "Ã¤" mojibake), 0xE2 sequences (’ “ ” – … €) and
// 4-byte sequences (emoji).
func classifyUTF8Sequences(raw []byte) (accidental, evidence int) {
	for _, s := range utf8Sequences(raw) {
		if s.isolated && s.accidentalIn(false) {
			accidental++
		} else {
			evidence++
		}
	}
	return accidental, evidence
}

// hasMixedUTF8 reports whether raw, a file that is not valid UTF-8 and is read with a
// single-byte codec, contains real UTF-8 (mixed encodings, or mojibake such as "Ã¤").
// Without an #ENCODING tag every evidence sequence of classifyUTF8Sequences counts ("when
// in doubt"). With a tag the codec is known, so a sequence only counts if it is clean (next
// to ASCII or other valid sequences on both sides; one next to a byte that is no UTF-8 is a
// chance pair of the declared codec's text), and in a non-Latin codec, whose letters are all
// non-ASCII, an isolated sequence also has to be one that is never accidental there: a
// 0xE2 or 4-byte sequence ("Её" in CP1251 is C5 B8, a valid 2-byte sequence).
func hasMixedUTF8(raw []byte, h hint) bool {
	if h.cm == nil {
		_, evidence := classifyUTF8Sequences(raw)
		return evidence > 0
	}
	for _, s := range utf8Sequences(raw) {
		if s.clean && !(s.isolated && s.accidentalIn(nonLatinCodecs[h.name])) {
			return true
		}
	}
	return false
}

// utf8Seq is a valid UTF-8 multi-byte sequence found by utf8Sequences.
type utf8Seq struct {
	lead     byte
	size     int
	isolated bool // ASCII (or the start/end of the data) on both sides
	clean    bool // ASCII, the start/end of the data or another valid sequence on both sides
}

// accidentalIn reports whether the sequence is of a kind that text in a single-byte codec
// produces by chance (see classifyUTF8Sequences; nonLatin: see hasMixedUTF8).
func (s utf8Seq) accidentalIn(nonLatin bool) bool {
	switch s.size {
	case 2:
		return nonLatin || s.lead >= 0xC6
	case 3:
		return s.lead != 0xE2
	}
	return false
}

// utf8Sequences returns the valid UTF-8 multi-byte sequences of raw, in order.
func utf8Sequences(raw []byte) []utf8Seq {
	var seqs []utf8Seq
	prevEnd := -1 // end of the previous sequence
	for i := 0; i < len(raw); {
		size := seqSize(raw, i)
		if size == 0 {
			i++
			continue
		}
		end := i + size
		before := i == 0 || raw[i-1] < 0x80
		after := end == len(raw) || raw[end] < 0x80
		seqs = append(seqs, utf8Seq{
			lead:     raw[i],
			size:     size,
			isolated: before && after,
			clean:    (before || prevEnd == i) && (after || seqSize(raw, end) > 0),
		})
		prevEnd = end
		i = end
	}
	return seqs
}

// seqSize returns the size of the valid UTF-8 multi-byte sequence at raw[i], or 0.
func seqSize(raw []byte, i int) int {
	if raw[i] < 0xC2 {
		return 0
	}
	r, size := utf8.DecodeRune(raw[i:])
	if r == utf8.RuneError || size < 2 {
		return 0
	}
	return size
}
