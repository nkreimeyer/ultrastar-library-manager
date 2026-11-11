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

	"golang.org/x/text/unicode/norm"

	"github.com/nkreimeyer/ultrastar-songs-sanitizer/fsops"
)

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
// not song files. The file is read with ops.ReadFile (ops may be nil), so in a dry run it
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
	tags := HeaderTags(DecodeTxt(data).Text)
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
	if bytes.Contains(data, []byte(checkFilename)) {
		return true
	}
	return strings.Contains(normalizeName(DecodeTxt(data).Text), normalizeName(checkFilename))
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
// reference was replaced, which is reported as replaced. The txt is found and read through
// ops, so in a dry run the replacement applies to the content earlier modules planned.
func ReplaceFileReference(ops *fsops.Ops, dir, oldName, newName string) (replaced bool, err error) {
	txt, err := FindSongTxt(ops, dir)
	if err != nil {
		return false, err
	}
	raw, err := ops.ReadFile(txt)
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
