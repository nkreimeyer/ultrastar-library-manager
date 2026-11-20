package repairtxt

import (
	"bytes"
	"sort"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/nkreimeyer/library-manager/songtxt"
)

// fileTags name files; their values are repaired by matching the folder's file names.
var fileTags = map[string]bool{
	"MP3": true, "AUDIO": true, "VIDEO": true, "COVER": true, "BACKGROUND": true,
	"VOCALS": true, "INSTRUMENTAL": true,
}

// segment is a text the repair works on: one header tag value, or the lyrics of one run
// of notes (the syllables joined, '\n' at each line break and player marker).
type segment struct {
	tag   string // upper-case tag key; "" for lyrics
	runes []rune
	offs  []int // byte offset of each rune in the source; -1 for the inserted '\n'
	lines []int // 1-based line of each rune
}

// lineSpans returns the [start, end) byte range of every line of src, numbered like
// songtxt.Parse does (a leading BOM skipped; "\r\n", "\r" and "\n" end a line).
func lineSpans(src []byte) [][2]int {
	i := 0
	if bytes.HasPrefix(src, []byte("\uFEFF")) {
		i = 3
	}
	var spans [][2]int
	start := i
	for i < len(src) {
		switch src[i] {
		case '\n':
			spans = append(spans, [2]int{start, i})
			i++
			start = i
		case '\r':
			spans = append(spans, [2]int{start, i})
			i++
			if i < len(src) && src[i] == '\n' {
				i++
			}
			start = i
		default:
			i++
		}
	}
	return append(spans, [2]int{start, len(src)})
}

// appendRunes decodes src[from:to] into seg. '~' (a held syllable) is left out, so the
// syllables around it join into one word.
func (seg *segment) appendRunes(src []byte, from, to, line int) {
	for i := from; i < to; {
		r, size := utf8.DecodeRune(src[i:to])
		if r != '~' {
			seg.runes = append(seg.runes, r)
			seg.offs = append(seg.offs, i)
			seg.lines = append(seg.lines, line)
		}
		i += size
	}
}

func (seg *segment) appendBreak() {
	if n := len(seg.runes); n == 0 || seg.runes[n-1] == '\n' {
		return
	}
	seg.runes = append(seg.runes, '\n')
	seg.offs = append(seg.offs, -1)
	seg.lines = append(seg.lines, 0)
}

// segments returns the tag values and the lyrics of song, which was parsed from src
// (valid UTF-8, the line numbers of song refer to it). A tag or note whose text cannot be
// found in its line (which Parse never produces) is left out.
func segments(src []byte, song *songtxt.Song) []segment {
	spans := lineSpans(src)
	lineText := func(n int) (int, int, bool) {
		if n < 1 || n > len(spans) {
			return 0, 0, false
		}
		return spans[n-1][0], spans[n-1][1], true
	}
	var segs []segment
	for _, t := range song.Tags {
		s, e, ok := lineText(t.Line)
		if !ok || t.Value == "" {
			continue
		}
		line := string(src[s:e])
		colon := strings.IndexByte(line, ':')
		if colon < 0 {
			continue
		}
		vs := colon + 1
		for vs < len(line) && (line[vs] == ' ' || line[vs] == '\t') {
			vs++
		}
		if !strings.HasPrefix(line[vs:], t.Value) {
			continue
		}
		seg := segment{tag: strings.ToUpper(t.Key)}
		seg.appendRunes(src, s+vs, s+vs+len(t.Value), t.Line)
		segs = append(segs, seg)
	}

	// Line breaks and player markers separate the lyric lines.
	var seps []int
	for _, b := range song.Breaks {
		seps = append(seps, b.Line)
	}
	seps = append(seps, song.PlayerLines...)
	sort.Ints(seps)
	lyrics := segment{}
	prevLine, si := 0, 0
	for _, n := range song.Notes {
		for si < len(seps) && seps[si] < n.Line {
			if seps[si] > prevLine {
				lyrics.appendBreak()
			}
			si++
		}
		prevLine = n.Line
		s, e, ok := lineText(n.Line)
		if !ok || !strings.HasSuffix(string(src[s:e]), n.Text) {
			continue
		}
		lyrics.appendRunes(src, e-len(n.Text), e, n.Line)
	}
	if len(lyrics.runes) > 0 {
		segs = append(segs, lyrics)
	}
	return segs
}

// corpusText returns the parts of the segments the corpus learns from: the lyrics and
// the text tags (not the file names, not numbers).
func corpusText(segs []segment) [][]rune {
	var out [][]rune
	for _, s := range segs {
		if fileTags[s.tag] || !hasLetter(s.runes) {
			continue
		}
		out = append(out, s.runes)
	}
	return out
}

func hasLetter(rs []rune) bool {
	for _, r := range rs {
		if unicode.IsLetter(r) {
			return true
		}
	}
	return false
}

// lyricLine returns the lyric line (or tag value) around rune index i of seg.
func (seg *segment) lyricLine(i int) (from, to int) {
	from, to = i, i
	for from > 0 && seg.runes[from-1] != '\n' {
		from--
	}
	for to < len(seg.runes) && seg.runes[to] != '\n' {
		to++
	}
	return from, to
}
