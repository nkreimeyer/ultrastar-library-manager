package utils

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"golang.org/x/text/unicode/norm"

	"github.com/nkreimeyer/library-manager/fsops"
	"github.com/nkreimeyer/library-manager/songtxt"
)

// HeaderTags returns the header tags of a decoded song txt: upper-cased tag name without
// "#" mapped to its trimmed value. Lines end in "\n", "\r\n" or "\r". The header ends at
// the first body line (songtxt.IsBodyLine), as in songtxt.Parse and UltraStar: other lines
// that are no tag (junk such as "Created with editor") are skipped. A repeated tag
// resolves like UltraStar does: the last occurrence wins.
func HeaderTags(text string) map[string]string {
	tags := map[string]string{}
	for _, line := range splitLines(strings.TrimPrefix(text, "\ufeff")) {
		if songtxt.IsBodyLine(line) {
			break
		}
		line = strings.TrimSpace(line)
		if !strings.HasPrefix(line, "#") {
			continue
		}
		name, value, ok := strings.Cut(line[1:], ":")
		if !ok {
			continue
		}
		tags[strings.ToUpper(strings.TrimSpace(name))] = strings.TrimSpace(value)
	}
	return tags
}

// startsWithTag reports whether the first non-blank line of text is a tag line.
func startsWithTag(text string) bool {
	for _, line := range splitLines(strings.TrimPrefix(text, "\ufeff")) {
		if line = strings.TrimSpace(line); line != "" {
			return strings.HasPrefix(line, "#")
		}
	}
	return false
}

// splitLines splits s at "\n" and "\r", leaving out empty lines.
func splitLines(s string) []string {
	return strings.FieldsFunc(s, func(r rune) bool { return r == '\n' || r == '\r' })
}

// songHeaderTags are header tags of which at least one must be present for a .txt to be
// treated as an UltraStar song file (as opposed to License.txt, readme.txt, ...).
var songHeaderTags = []string{"TITLE", "ARTIST", "MP3", "AUDIO", "BPM"}

// IsSongTxt reports whether the file looks like an UltraStar song txt: it starts with a
// tag line (blank lines aside), and its header contains one of the known tags
// (case-insensitive, any supported encoding). A txt that starts with prose (License.txt,
// readme.txt) is never a song txt, even with a "#TITLE:" line further down; a junk line
// inside a header that started with a tag does not end it (see HeaderTags). Unreadable
// files are not song files. The file is read with ops.ReadFile (ops may be nil), so in a dry run it
// is judged as earlier modules would have left it.
func IsSongTxt(ops *fsops.Ops, path string) bool {
	ok, _ := isSongTxt(ops, path)
	return ok
}

// isSongTxt is IsSongTxt that also reports read errors.
func isSongTxt(ops *fsops.Ops, path string) (bool, error) {
	data, err := ops.ReadFile(path)
	if err != nil {
		return false, err
	}
	text := DecodeTxt(data).Text
	if !startsWithTag(text) {
		return false, nil
	}
	tags := HeaderTags(text)
	for _, tag := range songHeaderTags {
		if _, ok := tags[tag]; ok {
			return true, nil
		}
	}
	return false, nil
}

// IsTxtFile reports whether name has a .txt extension (case-insensitive).
func IsTxtFile(name string) bool {
	return strings.EqualFold(filepath.Ext(name), ".txt")
}

// ListTxtFiles returns all .txt files directly inside dir (not recursive), sorted by name.
// Like discovery, it accepts regular files and symlinks to regular files.
func ListTxtFiles(dir string) ([]string, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}
	var txtFiles []string
	for _, entry := range entries {
		if IsTxtFile(entry.Name()) && IsRegularFile(dir, entry) {
			txtFiles = append(txtFiles, filepath.Join(dir, entry.Name()))
		}
	}
	return txtFiles, nil
}

// SongTxtFiles returns the .txt files directly inside dir that look like song files. A txt
// that cannot be read is an error: the folder must not be mistaken for a broken song. The
// txts are read with ops.ReadFile (ops may be nil); in a dry run, txts an earlier module
// would have moved away (ops.WouldBeMoved, e.g. a duplicate song txt fixtxt moves to
// extra-files/) are left out.
func SongTxtFiles(ops *fsops.Ops, dir string) ([]string, error) {
	txtFiles, err := ListTxtFiles(dir)
	if err != nil {
		return nil, err
	}
	var songFiles []string
	for _, f := range txtFiles {
		if ops.WouldBeMoved(f) {
			continue
		}
		ok, err := isSongTxt(ops, f)
		if err != nil {
			return nil, fmt.Errorf("reading %s: %w", f, err)
		}
		if ok {
			songFiles = append(songFiles, f)
		}
	}
	return songFiles, nil
}

// FindSongTxt returns the song txt of a song folder. If there are several, the most
// recently modified one is returned (the same one fixtxt keeps). It returns
// os.ErrNotExist if the folder contains no song txt. See SongTxtFiles for ops.
func FindSongTxt(ops *fsops.Ops, dir string) (string, error) {
	songFiles, err := SongTxtFiles(ops, dir)
	if err != nil {
		return "", err
	}
	return NewestFile(songFiles)
}

// ReadSongTags finds the song txt of dir and returns its header tags (see HeaderTags).
// The txt is found and read through ops (which may be nil), see FindSongTxt.
func ReadSongTags(ops *fsops.Ops, dir string) (map[string]string, error) {
	txt, err := FindSongTxt(ops, dir)
	if err != nil {
		return nil, err
	}
	data, err := ops.ReadFile(txt)
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
// mistake. The txt is found and read through ops (which may be nil), see FindSongTxt.
func IsFileReferenced(ops *fsops.Ops, dirpath, checkFilename string) bool {
	filename, err := FindSongTxt(ops, dirpath)
	if err != nil {
		return true
	}
	data, err := ops.ReadFile(filename)
	if err != nil {
		return true
	}
	return IsNameReferencedIn([][]byte{data}, checkFilename)
}

// IsNameReferencedIn reports whether name appears in one of txts, raw song txt contents,
// the way IsFileReferenced matches: byte-exactly, or case-insensitively and
// Unicode-normalized (NFC) in the decoded text.
func IsNameReferencedIn(txts [][]byte, name string) bool {
	want := normalizeName(name)
	for _, data := range txts {
		if bytes.Contains(data, []byte(name)) || strings.Contains(normalizeName(DecodeTxt(data).Text), want) {
			return true
		}
	}
	return false
}

func normalizeName(s string) string {
	return strings.ToLower(norm.NFC.String(s))
}

// ResolveFileName looks for name in dir, ignoring hidden files. exact reports a byte-identical match. Otherwise a
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
	// Hidden files (leftovers of interrupted runs, "._x" AppleDouble files) are never valid
	// targets, even if an old txt references one.
	want := norm.NFC.String(name)
	for _, e := range entries {
		if e.Name() == name && !IsHidden(name) {
			return name, true, true
		}
	}
	for _, e := range entries {
		if !IsHidden(e.Name()) && strings.EqualFold(norm.NFC.String(e.Name()), want) {
			return e.Name(), false, true
		}
	}
	return "", false, false
}

// fileTags are the header tags (upper-case keys) that reference files of the song folder,
// in check's report order. ReplaceFileReference rewrites them and check observes them.
var fileTags = []string{"AUDIO", "MP3", "VOCALS", "INSTRUMENTAL", "VIDEO", "COVER", "BACKGROUND"}

// FileTags returns the header tags (upper-case keys) that reference files of the song
// folder: #AUDIO, #MP3, #VOCALS, #INSTRUMENTAL, #VIDEO, #COVER and #BACKGROUND. The slice
// is a copy.
func FileTags() []string { return slices.Clone(fileTags) }

// IsFileTag reports whether the upper-case tag key is one of FileTags.
func IsFileTag(key string) bool { return slices.Contains(fileTags, key) }

// ErrTxtNotUTF8 is returned by ReplaceFileReference for song txts that are not UTF-8 yet.
var ErrTxtNotUTF8 = errors.New("song txt is not UTF-8 (run the fixtxt module first)")

// ErrTxtProblem is returned by ReplaceFileReference for song txts with a decode Problem.
var ErrTxtProblem = errors.New("song txt has an encoding problem and is never rewritten (fixtxt lists such txts in encoding_issues.txt)")

// ReplaceFileReference changes every file tag (#MP3, #AUDIO, #VIDEO, #COVER, ...) of the
// song txt in dir whose value is oldName (also matching case- and NFC-insensitively) to
// newName, written in NFC. Other lines are never touched. The file is written atomically,
// and only if a reference was replaced, which is reported as replaced. A txt that DecodeTxt
// does not read as UTF-8 without a Problem, before or after the change, is left untouched
// (ErrTxtNotUTF8, ErrTxtProblem). The txt is found and read through ops, so in a dry run
// the replacement applies to the content earlier modules planned.
func ReplaceFileReference(ops *fsops.Ops, dir, oldName, newName string) (replaced bool, err error) {
	txt, content, err := FileReferenceReplacement(ops, dir, oldName, newName)
	if err != nil || content == nil {
		return false, err
	}
	_, err = ops.WriteFileAtomic(txt, content)
	return err == nil, err
}

// FileReferenceReplacement returns the song txt in dir and the content
// ReplaceFileReference would write to it, without writing; content is nil if no file tag
// references oldName. The errors are ReplaceFileReference's, so a caller can check before
// expensive work (fixvideos encodes only if the txt can follow the new name).
func FileReferenceReplacement(ops *fsops.Ops, dir, oldName, newName string) (txt string, content []byte, err error) {
	txt, err = FindSongTxt(ops, dir)
	if err != nil {
		return "", nil, err
	}
	raw, err := ops.ReadFile(txt)
	if err != nil {
		return "", nil, err
	}
	// Valid UTF-8 bytes are not enough: DecodeTxt may read them as CP1252 (accidental
	// UTF-8) or with a Problem (stale #ENCODING, NUL bytes), and such a file is never
	// rewritten; fixtxt leaves it alone too.
	if err := rewritableUTF8(txt, raw); err != nil {
		return "", nil, err
	}

	want := normalizeName(oldName)
	// A name read from disk may be NFD (macOS); its combining marks alone are no clean
	// UTF-8 evidence for DecodeTxt, while the NFC letters are.
	newName = norm.NFC.String(newName)
	replaced := false
	lines := splitLinesKeepEnding(string(raw))
	for i, line := range lines {
		body := strings.TrimRight(line, "\r\n")
		ending := line[len(body):]
		// Leading blanks are allowed before the "#", as in HeaderTags.
		trimmed := strings.TrimLeft(strings.TrimPrefix(body, "\ufeff"), " \t")
		if songtxt.IsBodyLine(trimmed) {
			break // end of header
		}
		if !strings.HasPrefix(trimmed, "#") {
			continue // blank or junk line inside the header
		}
		name, value, ok := strings.Cut(trimmed[1:], ":")
		if !ok || !IsFileTag(strings.ToUpper(strings.TrimSpace(name))) {
			continue
		}
		if v := strings.TrimSpace(value); v == oldName || normalizeName(v) == want {
			prefix := body[:len(body)-len(trimmed)] // keeps a BOM on the first line and the indentation
			lines[i] = prefix + "#" + name + ":" + newName + ending
			replaced = true
		}
	}
	if !replaced {
		return txt, nil, nil
	}
	content = []byte(strings.Join(lines, ""))
	// The new name may not fit the file either (a Cyrillic name under a stale #ENCODING).
	if err := rewritableUTF8(txt, content); err != nil {
		return "", nil, err
	}
	return txt, content, nil
}

// splitLinesKeepEnding splits s into lines that end at "\r\n", "\r" or "\n" (a lone "\r"
// is a line break, as in HeaderTags and songtxt), each keeping its own ending, so that
// joining them gives s again. The last piece has no ending (it may be empty).
func splitLinesKeepEnding(s string) []string {
	var lines []string
	start := 0
	for i := 0; i < len(s); i++ {
		switch s[i] {
		case '\r':
			if i+1 < len(s) && s[i+1] == '\n' {
				i++
			}
		case '\n':
		default:
			continue
		}
		lines = append(lines, s[start:i+1])
		start = i + 1
	}
	return append(lines, s[start:])
}

// rewritableUTF8 returns nil if DecodeTxt reads content, the content of txt, as UTF-8
// without a Problem, else ErrTxtProblem or ErrTxtNotUTF8 naming txt.
func rewritableUTF8(txt string, content []byte) error {
	switch d := DecodeTxt(content); {
	case d.Problem != "":
		return fmt.Errorf("%s: %w: %s", txt, ErrTxtProblem, d.Problem)
	case d.Encoding != EncUTF8:
		return fmt.Errorf("%s: %w", txt, ErrTxtNotUTF8)
	}
	return nil
}
