package repairtxt

import (
	"bufio"
	"bytes"
	"fmt"
	"io"
	"os"
	"strings"
	"unicode"
	"unicode/utf8"

	"golang.org/x/text/encoding/charmap"
	"golang.org/x/text/unicode/norm"
)

// WordList is a word list file for one language ("" = any language).
type WordList struct {
	Lang string
	Path string
}

// ParseWordListFlag parses a -wordlist value: "path" or "lang=path" where lang is a code
// ("de") or a language name ("German").
func ParseWordListFlag(v string) (WordList, error) {
	if i := strings.IndexByte(v, '='); i > 0 && !strings.ContainsAny(v[:i], `/\`) {
		lang := v[:i]
		code := lang
		if len(lang) != 2 {
			codes := LanguageCodes(lang)
			if len(codes) != 1 {
				return WordList{}, fmt.Errorf("-wordlist %s: unknown language %q (use a code such as de)", v, lang)
			}
			code = codes[0]
		}
		return WordList{Lang: strings.ToLower(code), Path: v[i+1:]}, nil
	}
	return WordList{Path: v}, nil
}

// readWordList reads a word list: one word per line, as plain lists, hunspell .dic files
// ("word/FLAGS", a word count on the first line) and frequency lists ("word<TAB>count" or
// "rank<TAB>word<TAB>count") have them. The first field with a letter is the word. Lines
// starting with '#' are comments. A file that is not valid UTF-8 is read as CP1252 (older
// hunspell dictionaries are ISO-8859-1).
func readWordList(r io.Reader) ([]string, error) {
	data, err := io.ReadAll(r)
	if err != nil {
		return nil, err
	}
	if !utf8.Valid(data) {
		if data, err = charmap.Windows1252.NewDecoder().Bytes(data); err != nil {
			return nil, err
		}
	}
	data = bytes.TrimPrefix(data, []byte("\uFEFF"))
	var words []string
	sc := bufio.NewScanner(bytes.NewReader(data))
	sc.Buffer(make([]byte, 64*1024), 1024*1024)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		for _, f := range strings.Fields(line) {
			if i := strings.IndexByte(f, '/'); i >= 0 {
				f = f[:i]
			}
			if strings.IndexFunc(f, unicode.IsLetter) >= 0 {
				words = append(words, norm.NFC.String(f))
				break
			}
		}
	}
	return words, sc.Err()
}

// loadWordLists reads the word lists into one langLex per language. Every word counts
// once: a word list only tells which words exist.
func loadWordLists(lists []WordList) (map[string]*langLex, error) {
	out := map[string]*langLex{}
	for _, wl := range lists {
		f, err := os.Open(wl.Path)
		if err != nil {
			return nil, fmt.Errorf("word list: %w", err)
		}
		words, err := readWordList(f)
		f.Close()
		if err != nil {
			return nil, fmt.Errorf("word list %s: %w", wl.Path, err)
		}
		l := out[wl.Lang]
		if l == nil {
			l = newLangLex()
			out[wl.Lang] = l
		}
		for _, w := range words {
			form := string(lowerRunes([]rune(w)))
			if l.words[form] == nil {
				l.words[form] = &entry{n: 1}
				l.total++
			}
		}
	}
	for _, l := range out {
		l.finalize()
	}
	return out, nil
}
