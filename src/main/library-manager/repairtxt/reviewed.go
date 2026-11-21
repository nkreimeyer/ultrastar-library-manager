package repairtxt

import (
	"bufio"
	"bytes"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"unicode"
	"unicode/utf8"

	"golang.org/x/text/encoding/charmap"
	"golang.org/x/text/unicode/norm"
)

// reviewedWords are the spellings of the -words files: the ones for every song and the
// ones of the song sections.
type reviewedWords struct {
	global *langLex // nil if there are none
	songs  []*songSection
}

// songSection holds the words of a "[Artist - Title]" section of a -words file.
type songSection struct {
	name    string // as written in the file
	key     []rune // matchKey(name)
	words   []string
	lex     *langLex
	matched bool
}

// readReviewed reads a -words file: one or more words per line, '#' starts a comment
// line. A line "[Artist - Title]" starts a section whose words apply only to that song; "[*]"
// (or "[]") goes back to words for every song. A file that is not valid UTF-8 is read as
// CP1252.
func readReviewed(r io.Reader) (global []string, sections []*songSection, err error) {
	data, err := io.ReadAll(r)
	if err != nil {
		return nil, nil, err
	}
	if !utf8.Valid(data) {
		if data, err = charmap.Windows1252.NewDecoder().Bytes(data); err != nil {
			return nil, nil, err
		}
	}
	data = bytes.TrimPrefix(data, []byte("\uFEFF"))
	var cur *songSection
	sc := bufio.NewScanner(bytes.NewReader(data))
	sc.Buffer(make([]byte, 64*1024), 1024*1024)
	for n := 1; sc.Scan(); n++ {
		line := strings.TrimSpace(sc.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		if strings.HasPrefix(line, "[") && strings.HasSuffix(line, "]") {
			name := strings.TrimSpace(line[1 : len(line)-1])
			if name == "" || name == "*" {
				cur = nil
				continue
			}
			key := matchKey(name)
			if len(key) == 0 {
				return nil, nil, fmt.Errorf("line %d: section %q names no song", n, line)
			}
			cur = &songSection{name: name, key: key}
			sections = append(sections, cur)
			continue
		}
		for _, f := range strings.Fields(line) {
			if strings.IndexFunc(f, unicode.IsLetter) < 0 {
				continue
			}
			f = norm.NFC.String(f)
			if cur != nil {
				cur.words = append(cur.words, f)
			} else {
				global = append(global, f)
			}
		}
	}
	return global, sections, sc.Err()
}

// reviewedLex is a lexicon of the words, each counted once.
func reviewedLex(words []string) *langLex {
	l := newLangLex()
	for _, w := range words {
		form := string(lowerRunes([]rune(w)))
		if l.words[form] == nil {
			l.words[form] = &entry{n: 1}
			l.total++
		}
	}
	l.finalize()
	return l
}

// loadReviewed reads the -words files. Sections naming the same song (compared like
// forSong does) are merged.
func loadReviewed(paths []string) (*reviewedWords, error) {
	rw := &reviewedWords{}
	var global []string
	for _, p := range paths {
		f, err := os.Open(p)
		if err != nil {
			return nil, fmt.Errorf("-words: %w", err)
		}
		g, secs, err := readReviewed(f)
		f.Close()
		if err != nil {
			return nil, fmt.Errorf("-words %s: %w", p, err)
		}
		global = append(global, g...)
	secs:
		for _, s := range secs {
			for _, prev := range rw.songs {
				if looseEqual(prev.key, s.key) {
					prev.words = append(prev.words, s.words...)
					continue secs
				}
			}
			rw.songs = append(rw.songs, s)
		}
	}
	if len(global) > 0 {
		rw.global = reviewedLex(global)
	}
	for _, s := range rw.songs {
		s.lex = reviewedLex(s.words)
	}
	return rw, nil
}

// forSong returns the lexicon of the sections that name the damaged txt rel (relative to
// the song dir) by its folder, its file name or its "#ARTIST - #TITLE" (compared like
// lyrics records: case, accents, punctuation and U+FFFD do not matter), or nil.
// SectionNamesTxt reports whether the -words section name names the txt rel (relative to
// the song dir) by its folder or file name, compared as forSong does (case, accents and
// punctuation ignored). The "#ARTIST - #TITLE" spelling forSong also accepts is not checked.
func SectionNamesTxt(section, rel string) bool {
	key := matchKey(section)
	for _, n := range []string{filepath.Base(filepath.Dir(rel)), strings.TrimSuffix(filepath.Base(rel), filepath.Ext(rel))} {
		if looseEqual(key, matchKey(n)) {
			return true
		}
	}
	return false
}

func (rw *reviewedWords) forSong(rel, artist, title string) *langLex {
	if rw == nil || len(rw.songs) == 0 {
		return nil
	}
	names := []string{filepath.Base(filepath.Dir(rel)), strings.TrimSuffix(filepath.Base(rel), filepath.Ext(rel))}
	if artist != "" || title != "" {
		names = append(names, artist+" - "+title)
	}
	var words []string
	for _, s := range rw.songs {
		for _, n := range names {
			if looseEqual(s.key, matchKey(n)) {
				s.matched = true
				words = append(words, s.words...)
				break
			}
		}
	}
	if len(words) == 0 {
		return nil
	}
	return reviewedLex(words)
}

// unmatched returns the names of the sections that matched no damaged txt.
func (rw *reviewedWords) unmatched() []string {
	var out []string
	if rw == nil {
		return nil
	}
	for _, s := range rw.songs {
		if !s.matched {
			out = append(out, s.name)
		}
	}
	return out
}
