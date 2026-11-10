package utils

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
	"unicode/utf8"

	"golang.org/x/text/encoding/charmap"
	"golang.org/x/text/encoding/unicode"
	"golang.org/x/text/unicode/norm"

	"github.com/nkreimeyer/ultrastar-songs-sanitizer/fsops"
)

// Names of the encodings DecodeTxt can detect.
const (
	EncUTF8    = "UTF-8"
	EncUTF16LE = "UTF-16LE"
	EncUTF16BE = "UTF-16BE"
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

// DecodeTxt decodes the raw content of a song txt without losing data. A codec that cannot
// represent the input exactly fails and the next one is tried:
//
//  1. UTF-16 if the file starts with a UTF-16 BOM
//  2. strict UTF-8
//  3. CP1252, unless the input contains one of the five bytes CP1252 leaves undefined
//  4. otherwise the file is read as ISO-8859-1 and Problem is set
//
// A file that is not valid UTF-8 but contains valid UTF-8 multi-byte sequences mixes
// encodings (or contains mojibake such as "Ã¤"); Problem is set for it as well.
func DecodeTxt(raw []byte) DecodedTxt {
	var d DecodedTxt
	switch {
	case bytes.HasPrefix(raw, utf16LEBOM), bytes.HasPrefix(raw, utf16BEBOM):
		order, enc := unicode.LittleEndian, EncUTF16LE
		if bytes.HasPrefix(raw, utf16BEBOM) {
			order, enc = unicode.BigEndian, EncUTF16BE
		}
		out, err := unicode.UTF16(order, unicode.IgnoreBOM).NewDecoder().Bytes(raw[2:])
		d = DecodedTxt{Text: string(out), Encoding: enc}
		if err != nil || bytes.ContainsRune(out, utf8.RuneError) {
			d.Problem = "invalid UTF-16 data"
		}
	case utf8.Valid(raw):
		d = DecodedTxt{Text: string(bytes.TrimPrefix(raw, utf8BOM)), Encoding: EncUTF8}
		d.Damaged = strings.ContainsRune(d.Text, utf8.RuneError)
	default:
		raw = bytes.TrimPrefix(raw, utf8BOM)
		if text, ok := decodeCP1252(raw); ok {
			d = DecodedTxt{Text: text, Encoding: EncCP1252}
		} else {
			d = DecodedTxt{Text: decodeLatin1(raw), Encoding: EncLatin1,
				Problem: "contains bytes that are undefined in CP1252 (0x81, 0x8D, 0x8F, 0x90 or 0x9D)"}
		}
		if d.Problem == "" && hasUTF8MultiByte(raw) {
			d.Problem = "mixes UTF-8 with another encoding (or contains mojibake such as \"Ã¤\")"
		}
	}

	d.Text = strings.TrimPrefix(d.Text, "\ufeff")
	d.Text = strings.ReplaceAll(d.Text, "\r\n", "\n")
	d.Text = strings.ReplaceAll(d.Text, "\r", "\n")
	return d
}

// decodeCP1252 decodes raw as Windows-1252. It fails if raw contains a byte that is
// undefined in CP1252: the x/text decoder silently turns those into U+FFFD.
func decodeCP1252(raw []byte) (string, bool) {
	for _, b := range raw {
		switch b {
		case 0x81, 0x8D, 0x8F, 0x90, 0x9D:
			return "", false
		}
	}
	decoded, err := charmap.Windows1252.NewDecoder().Bytes(raw)
	if err != nil || bytes.ContainsRune(decoded, utf8.RuneError) {
		return "", false
	}
	return string(decoded), true
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

// hasUTF8MultiByte reports whether raw contains at least one valid UTF-8 multi-byte sequence.
func hasUTF8MultiByte(raw []byte) bool {
	for i := 0; i < len(raw); i++ {
		if raw[i] < 0xC2 {
			continue
		}
		if r, size := utf8.DecodeRune(raw[i:]); r != utf8.RuneError && size > 1 {
			return true
		}
	}
	return false
}

// HeaderTags returns the header tags of a decoded song txt: upper-cased tag name without
// "#" mapped to its trimmed value. The header ends at the first non-empty line that does
// not start with "#". The first occurrence of a tag wins.
func HeaderTags(text string) map[string]string {
	tags := map[string]string{}
	for _, line := range strings.Split(text, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		if !strings.HasPrefix(line, "#") {
			break
		}
		name, value, ok := strings.Cut(line[1:], ":")
		if !ok {
			continue
		}
		name = strings.ToUpper(strings.TrimSpace(name))
		if _, seen := tags[name]; !seen {
			tags[name] = strings.TrimSpace(value)
		}
	}
	return tags
}

// songHeaderTags are header tags of which at least one must be present for a .txt to be
// treated as an UltraStar song file (as opposed to License.txt, readme.txt, ...).
var songHeaderTags = []string{"TITLE", "ARTIST", "MP3", "AUDIO", "BPM"}

// IsSongTxt reports whether the file looks like an UltraStar song txt: its header contains
// one of the known tags (case-insensitive, any supported encoding). Unreadable files are
// not song files.
func IsSongTxt(path string) bool {
	data, err := os.ReadFile(path)
	if err != nil {
		return false
	}
	tags := HeaderTags(DecodeTxt(data).Text)
	for _, tag := range songHeaderTags {
		if _, ok := tags[tag]; ok {
			return true
		}
	}
	return false
}

// IsTxtFile reports whether name has a .txt extension (case-insensitive).
func IsTxtFile(name string) bool {
	return strings.EqualFold(filepath.Ext(name), ".txt")
}

// ListTxtFiles returns all .txt files directly inside dir (not recursive), sorted by name.
func ListTxtFiles(dir string) ([]string, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}
	var txtFiles []string
	for _, entry := range entries {
		if entry.Type().IsRegular() && IsTxtFile(entry.Name()) {
			txtFiles = append(txtFiles, filepath.Join(dir, entry.Name()))
		}
	}
	return txtFiles, nil
}

// SongTxtFiles returns the .txt files directly inside dir that look like song files.
func SongTxtFiles(dir string) ([]string, error) {
	txtFiles, err := ListTxtFiles(dir)
	if err != nil {
		return nil, err
	}
	var songFiles []string
	for _, f := range txtFiles {
		if IsSongTxt(f) {
			songFiles = append(songFiles, f)
		}
	}
	return songFiles, nil
}

// FindSongTxt returns the song txt of a song folder. If there are several, the most
// recently modified one is returned (the same one fixtxt keeps). It returns
// os.ErrNotExist if the folder contains no song txt.
func FindSongTxt(dir string) (string, error) {
	songFiles, err := SongTxtFiles(dir)
	if err != nil {
		return "", err
	}
	return NewestFile(songFiles)
}

// ReadSongTags finds the song txt of dir and returns its header tags (see HeaderTags).
func ReadSongTags(dir string) (map[string]string, error) {
	txt, err := FindSongTxt(dir)
	if err != nil {
		return nil, err
	}
	data, err := os.ReadFile(txt)
	if err != nil {
		return nil, err
	}
	return HeaderTags(DecodeTxt(data).Text), nil
}

// NewestFile returns the most recently modified file of files; ties keep the first one.
// Files that cannot be stat'ed are skipped. It returns os.ErrNotExist if none is usable.
func NewestFile(files []string) (string, error) {
	newest := ""
	var newestTime time.Time
	for _, f := range files {
		st, err := os.Stat(f)
		if err != nil {
			continue
		}
		if newest == "" || st.ModTime().After(newestTime) {
			newest, newestTime = f, st.ModTime()
		}
	}
	if newest == "" {
		return "", os.ErrNotExist
	}
	return newest, nil
}

// IsFileReferenced reports whether checkFilename appears in the song txt of dirpath. The
// comparison is case-insensitive and Unicode-normalized (NFC) on the decoded txt; a raw
// byte match also counts, so file names that are not valid UTF-8 are found too. If there
// is no readable song txt, every file counts as referenced so that nothing is moved by
// mistake.
func IsFileReferenced(dirpath, checkFilename string) bool {
	filename, err := FindSongTxt(dirpath)
	if err != nil {
		return true
	}
	data, err := os.ReadFile(filename)
	if err != nil {
		return true
	}
	if bytes.Contains(data, []byte(checkFilename)) {
		return true
	}
	return strings.Contains(normalizeName(DecodeTxt(data).Text), normalizeName(checkFilename))
}

func normalizeName(s string) string {
	return strings.ToLower(norm.NFC.String(s))
}

// ResolveFileName looks for name in dir. exact reports a byte-identical match. Otherwise a
// name that only differs in case or Unicode normalization (NFC/NFD) is returned as onDisk
// with exact == false, so callers can correct the reference. found is false if neither
// exists or name is empty.
func ResolveFileName(dir, name string) (onDisk string, exact, found bool) {
	if name == "" {
		return "", false, false
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return "", false, false
	}
	want := norm.NFC.String(name)
	for _, e := range entries {
		if e.Name() == name {
			return name, true, true
		}
	}
	for _, e := range entries {
		if strings.EqualFold(norm.NFC.String(e.Name()), want) {
			return e.Name(), false, true
		}
	}
	return "", false, false
}

// fileTags are the header tags that reference files of the song folder.
var fileTags = map[string]bool{
	"MP3": true, "AUDIO": true, "VOCALS": true, "INSTRUMENTAL": true,
	"VIDEO": true, "COVER": true, "BACKGROUND": true,
}

// ErrTxtNotUTF8 is returned by ReplaceFileReference for song txts that are not UTF-8 yet.
var ErrTxtNotUTF8 = errors.New("song txt is not UTF-8 (run the fixtxt module first)")

// ReplaceFileReference changes every file tag (#MP3, #AUDIO, #VIDEO, #COVER, ...) of the
// song txt in dir whose value is oldName (also matching case- and NFC-insensitively) to
// newName. Other lines are never touched. The file is written atomically, and only if a
// reference was replaced, which is reported as replaced.
func ReplaceFileReference(ops *fsops.Ops, dir, oldName, newName string) (replaced bool, err error) {
	txt, err := FindSongTxt(dir)
	if err != nil {
		return false, err
	}
	raw, err := os.ReadFile(txt)
	if err != nil {
		return false, err
	}
	if !utf8.Valid(raw) {
		return false, fmt.Errorf("%s: %w", txt, ErrTxtNotUTF8)
	}

	want := normalizeName(oldName)
	lines := strings.SplitAfter(string(raw), "\n")
	for i, line := range lines {
		body := strings.TrimRight(line, "\r\n")
		ending := line[len(body):]
		trimmed := strings.TrimPrefix(body, "\ufeff")
		if !strings.HasPrefix(trimmed, "#") {
			if strings.TrimSpace(trimmed) != "" {
				break // end of header
			}
			continue
		}
		name, value, ok := strings.Cut(trimmed[1:], ":")
		if !ok || !fileTags[strings.ToUpper(strings.TrimSpace(name))] {
			continue
		}
		if v := strings.TrimSpace(value); v == oldName || normalizeName(v) == want {
			prefix := body[:len(body)-len(trimmed)] // keeps a BOM on the first line
			lines[i] = prefix + "#" + name + ":" + newName + ending
			replaced = true
		}
	}
	if !replaced {
		return false, nil
	}
	_, err = ops.WriteFileAtomic(txt, []byte(strings.Join(lines, "")))
	return err == nil, err
}
