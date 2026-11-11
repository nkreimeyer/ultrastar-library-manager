// Package songtxt parses the UltraStar song txt format into a Song: header tags, notes,
// line breaks, old-format BPM changes and duet player markers.
//
// The package is pure: it does no file system access and no logging. Input is text that
// has already been decoded to UTF-8 (utils.DecodeTxt). Syntax problems are returned as
// Issues; Parse never fails and never panics.
//
// Format summary:
//
//	#TAG:value            header tag (split at the first ':')
//	: 0 4 5 la            note: <type> <start> <length> <pitch> <syllable>
//	                      types ':' normal, '*' golden, 'F' freestyle, 'R' rap, 'G' rap golden
//	- 12                  line break at beat 12
//	- 12 14               line break; the second number only matters in RELATIVE mode
//	B 100 240,5           old-format BPM change at beat 100
//	P1 / P2 / P3 / P 1    duet player marker (P3 = both singers)
//	E                     end of song; everything after it is ignored
package songtxt

import (
	"errors"
	"fmt"
	"math"
	"sort"
	"strconv"
	"strings"
)

// Tag is one "#KEY:value" header line.
type Tag struct {
	Key   string // as written, without '#', original case, surrounding whitespace trimmed
	Value string // surrounding whitespace trimmed
	Line  int    // 1-based
}

// NoteType is the first character of a note line.
type NoteType byte

// Note types.
const (
	Normal    NoteType = ':'
	Golden    NoteType = '*'
	Freestyle NoteType = 'F'
	Rap       NoteType = 'R'
	RapGolden NoteType = 'G'
)

// Note is one note line.
type Note struct {
	Type                 NoteType
	Start, Length, Pitch int    // Start is the absolute beat (RELATIVE mode already resolved); it may be negative
	Text                 string // syllable exactly as written (may contain/start with spaces, may be empty)
	Player               int    // 0 = no duet markers seen yet, else 1, 2 or 3 (3 = both)
	Line                 int
}

// LineBreak is one "- <beat>" line. Beat is absolute and may be negative.
type LineBreak struct {
	Beat, Player, Line int
}

// BPMChange is an old-format "B <beat> <bpm>" line. Beat is absolute.
type BPMChange struct {
	Beat int
	BPM  float64
	Line int
}

// Issue is a syntax-level problem found while parsing.
type Issue struct {
	Line int
	Code string
	Msg  string
}

// Issue codes.
const (
	IssueTagAfterNotes  = "tag-after-notes"
	IssueMalformedTag   = "malformed-tag"
	IssueMalformedNote  = "malformed-note"
	IssueMalformedBreak = "malformed-break"
	IssueMalformedBPM   = "malformed-bpm"
	IssueBadPlayer      = "bad-player"
	IssueUnknownLine    = "unknown-line"
)

// Song is a parsed song txt.
type Song struct {
	Tags       []Tag
	Notes      []Note      // in file order
	Breaks     []LineBreak // in file order
	BPMChanges []BPMChange
	Players    []int // player markers in file order
	// PlayerLines holds the 1-based line of each entry of Players.
	PlayerLines []int
	HasEnd      bool // an "E" line was found; lines after it are ignored
	EndLine     int
	Relative    bool // #RELATIVE:yes (case-insensitive), as set in the header
}

// Parse parses decoded song text. Line endings \r\n, \r and \n are all accepted, and a
// leading BOM is ignored.
//
// RELATIVE mode (#RELATIVE:yes in the header, before the first body line): note, break and
// B-line beats are relative to an offset that starts at 0. A break "- a" lies at
// offset+a and then advances the offset by a; a break "- a b" lies at offset+a and
// advances the offset by b. As in UltraStar Deluxe, every player has its own offset (a
// P marker switches to that player's offset). All beats stored in the Song are absolute.
// In normal mode the second break number is ignored.
func Parse(text string) (*Song, []Issue) {
	s := &Song{}
	var issues []Issue
	addIssue := func(line int, code, format string, args ...any) {
		issues = append(issues, Issue{Line: line, Code: code, Msg: fmt.Sprintf(format, args...)})
	}

	inBody := false
	player := 0
	offsets := map[int]int{} // RELATIVE mode: beat offset per player

	for i, raw := range splitLines(text) {
		lineNo := i + 1
		line := strings.TrimLeft(raw, " \t")
		if strings.TrimSpace(line) == "" {
			continue
		}
		c := line[0]
		rest := line[1:]

		switch {
		case c == '#':
			body := rest
			colon := strings.IndexByte(body, ':')
			if colon < 0 {
				addIssue(lineNo, IssueMalformedTag, "tag line without ':': %q", raw)
				continue
			}
			key := strings.TrimSpace(body[:colon])
			if key == "" {
				addIssue(lineNo, IssueMalformedTag, "tag line without a key: %q", raw)
				continue
			}
			s.Tags = append(s.Tags, Tag{Key: key, Value: strings.TrimSpace(body[colon+1:]), Line: lineNo})
			if inBody {
				addIssue(lineNo, IssueTagAfterNotes, "tag #%s after the first note line", key)
			} else if strings.EqualFold(key, "RELATIVE") {
				s.Relative = strings.EqualFold(strings.TrimSpace(body[colon+1:]), "yes")
			}

		case c == 'E' && (rest == "" || isSpace(rest[0])):
			s.HasEnd = true
			s.EndLine = lineNo
			return s, issues

		case isNoteType(c) && (c == ':' || c == '*' || rest == "" || isSpace(rest[0])):
			inBody = true
			f1, r := nextField(rest)
			f2, r := nextField(r)
			f3, r := nextField(r)
			start, err1 := strconv.Atoi(f1)
			length, err2 := strconv.Atoi(f2)
			pitch, err3 := strconv.Atoi(f3)
			if err := errors.Join(err1, err2, err3); err != nil {
				addIssue(lineNo, IssueMalformedNote, "note fields are not integers: %q", raw)
				continue
			}
			// Beats are signed: a note at a negative beat plays before #GAP (editors that
			// shift the beats instead of #GAP produce them), as in UltraStar Deluxe.
			if length < 0 {
				addIssue(lineNo, IssueMalformedNote, "negative length: %q", raw)
				continue
			}
			syllable := ""
			if r != "" {
				syllable = r[1:] // skip the single separator after the pitch
			}
			if s.Relative {
				start += offsets[player]
			}
			s.Notes = append(s.Notes, Note{
				Type: NoteType(c), Start: start, Length: length, Pitch: pitch,
				Text: syllable, Player: player, Line: lineNo,
			})

		case c == '-':
			inBody = true
			f1, r := nextField(rest)
			f2, r := nextField(r)
			a, err := strconv.Atoi(f1) // signed, like note beats
			if err != nil {
				addIssue(lineNo, IssueMalformedBreak, "line break beat is not an integer: %q", raw)
				continue
			}
			b, hasB := 0, f2 != ""
			if hasB {
				b, err = strconv.Atoi(f2)
				if err != nil {
					addIssue(lineNo, IssueMalformedBreak, "second line break beat is not an integer: %q", raw)
					continue
				}
			}
			if strings.TrimSpace(r) != "" {
				addIssue(lineNo, IssueMalformedBreak, "extra fields after line break: %q", raw)
				continue
			}
			beat := a
			if s.Relative {
				beat += offsets[player]
				if hasB {
					offsets[player] += b
				} else {
					offsets[player] += a
				}
			}
			s.Breaks = append(s.Breaks, LineBreak{Beat: beat, Player: player, Line: lineNo})

		case c == 'B' && (rest == "" || isSpace(rest[0])):
			inBody = true
			f1, r := nextField(rest)
			beat, err := strconv.Atoi(f1)
			if err != nil || beat < 0 {
				addIssue(lineNo, IssueMalformedBPM, "BPM change beat is not a non-negative integer: %q", raw)
				continue
			}
			bpm, err := ParseNumber(r)
			if err != nil || bpm <= 0 {
				addIssue(lineNo, IssueMalformedBPM, "BPM change value is not a positive number: %q", raw)
				continue
			}
			if s.Relative {
				beat += offsets[player]
			}
			s.BPMChanges = append(s.BPMChanges, BPMChange{Beat: beat, BPM: bpm, Line: lineNo})

		case c == 'P' && isDigits(strings.TrimSpace(rest)):
			inBody = true
			n, err := strconv.Atoi(strings.TrimSpace(rest))
			if err != nil || n < 1 || n > 3 {
				addIssue(lineNo, IssueBadPlayer, "unknown player marker: %q", raw)
				continue
			}
			player = n
			s.Players = append(s.Players, n)
			s.PlayerLines = append(s.PlayerLines, lineNo)

		default:
			addIssue(lineNo, IssueUnknownLine, "unrecognized line: %q", raw)
		}
	}
	return s, issues
}

func splitLines(text string) []string {
	text = strings.TrimPrefix(text, "\uFEFF")
	text = strings.ReplaceAll(text, "\r\n", "\n")
	text = strings.ReplaceAll(text, "\r", "\n")
	return strings.Split(text, "\n")
}

func isSpace(c byte) bool { return c == ' ' || c == '\t' }

func isNoteType(c byte) bool {
	switch NoteType(c) {
	case Normal, Golden, Freestyle, Rap, RapGolden:
		return true
	}
	return false
}

func isDigits(s string) bool {
	if s == "" {
		return false
	}
	for i := 0; i < len(s); i++ {
		if s[i] < '0' || s[i] > '9' {
			return false
		}
	}
	return true
}

// nextField skips leading spaces/tabs and returns the next field and everything after it
// (starting with the separator, or "" at the end of the line).
func nextField(s string) (field, rest string) {
	s = strings.TrimLeft(s, " \t")
	i := strings.IndexAny(s, " \t")
	if i < 0 {
		return s, ""
	}
	return s[:i], s[i:]
}

// Tag returns the value of the first tag with the given key (case-insensitive, a leading
// '#' in key is ignored). Note that players use the last occurrence of a repeated tag
// (check.playerView resolves repeated tags that way).
func (s *Song) Tag(key string) (string, bool) {
	key = strings.TrimPrefix(key, "#")
	for _, t := range s.Tags {
		if strings.EqualFold(t.Key, key) {
			return t.Value, true
		}
	}
	return "", false
}

// Duplicates returns the upper-cased tag keys that occur more than once, sorted.
func (s *Song) Duplicates() []string {
	count := map[string]int{}
	for _, t := range s.Tags {
		count[strings.ToUpper(t.Key)]++
	}
	var dups []string
	for k, n := range count {
		if n > 1 {
			dups = append(dups, k)
		}
	}
	sort.Strings(dups)
	return dups
}

// ParseNumber parses a decimal number as used by #BPM, #GAP etc. Both a decimal comma
// ("300,5") and a dot are accepted, as are surrounding spaces and an exponent. Empty
// values, NaN, infinities and hex notation are rejected.
func ParseNumber(v string) (float64, error) {
	t := strings.TrimSpace(v)
	if t == "" {
		return 0, errors.New("empty number")
	}
	for i := 0; i < len(t); i++ {
		if !strings.ContainsRune("+-0123456789.,eE", rune(t[i])) {
			return 0, fmt.Errorf("invalid number %q", v)
		}
	}
	f, err := strconv.ParseFloat(strings.ReplaceAll(t, ",", "."), 64)
	if err != nil || math.IsNaN(f) || math.IsInf(f, 0) {
		return 0, fmt.Errorf("invalid number %q", v)
	}
	return f, nil
}

// optionalNumber returns the tag's number; an absent or empty tag is "not present".
func (s *Song) optionalNumber(key string) (float64, bool, error) {
	v, ok := s.Tag(key)
	if !ok || strings.TrimSpace(v) == "" {
		return 0, false, nil
	}
	f, err := ParseNumber(v)
	if err != nil {
		return 0, true, fmt.Errorf("#%s: %w", key, err)
	}
	return f, true, nil
}

// BPM returns #BPM. It is an error if the tag is missing, unparsable or <= 0.
func (s *Song) BPM() (float64, error) {
	v, ok := s.Tag("BPM")
	if !ok {
		return 0, errors.New("#BPM missing")
	}
	f, err := ParseNumber(v)
	if err != nil {
		return 0, fmt.Errorf("#BPM: %w", err)
	}
	if f <= 0 {
		return 0, fmt.Errorf("#BPM must be positive, got %v", f)
	}
	return f, nil
}

// GapMs returns #GAP in milliseconds. A missing or empty tag is 0.
func (s *Song) GapMs() (float64, error) {
	f, _, err := s.optionalNumber("GAP")
	return f, err
}

// VideoGapSec returns #VIDEOGAP in seconds. A missing or empty tag is 0.
func (s *Song) VideoGapSec() (float64, error) {
	f, _, err := s.optionalNumber("VIDEOGAP")
	return f, err
}

// StartSec returns #START in seconds and whether it is present (an empty value counts as
// absent).
func (s *Song) StartSec() (float64, bool, error) { return s.optionalNumber("START") }

// EndMs returns #END in milliseconds and whether it is present (an empty value counts as
// absent).
func (s *Song) EndMs() (float64, bool, error) { return s.optionalNumber("END") }

// BeatToMs converts an absolute beat to song time in milliseconds:
// GAP + beat * 60000 / (BPM * 4). Old-format BPM changes are applied piecewise: a change
// at beat b applies from b on.
func (s *Song) BeatToMs(beat float64) (float64, error) {
	if math.IsNaN(beat) || math.IsInf(beat, 0) {
		return 0, fmt.Errorf("invalid beat %v", beat)
	}
	bpm, err := s.BPM()
	if err != nil {
		return 0, err
	}
	gap, err := s.GapMs()
	if err != nil {
		return 0, err
	}
	changes := append([]BPMChange(nil), s.BPMChanges...)
	sort.SliceStable(changes, func(i, j int) bool { return changes[i].Beat < changes[j].Beat })

	ms, pos, cur := gap, 0.0, bpm
	for _, c := range changes {
		cb := float64(c.Beat)
		if cb >= beat {
			break
		}
		if c.BPM <= 0 || math.IsNaN(c.BPM) || math.IsInf(c.BPM, 0) {
			continue // never produced by Parse; guards hand-built Songs
		}
		if cb > pos {
			ms += (cb - pos) * 60000 / (cur * 4)
			pos = cb
		}
		cur = c.BPM
	}
	return ms + (beat-pos)*60000/(cur*4), nil
}

// LastNoteEndBeat returns max(Start+Length) over all notes; false if there are no notes.
func (s *Song) LastNoteEndBeat() (int, bool) {
	if len(s.Notes) == 0 {
		return 0, false
	}
	end := s.Notes[0].Start + s.Notes[0].Length
	for _, n := range s.Notes[1:] {
		end = max(end, n.Start+n.Length)
	}
	return end, true
}

// FirstNoteStartBeat returns min(Start) over all notes; false if there are no notes.
func (s *Song) FirstNoteStartBeat() (int, bool) {
	if len(s.Notes) == 0 {
		return 0, false
	}
	start := s.Notes[0].Start
	for _, n := range s.Notes[1:] {
		start = min(start, n.Start)
	}
	return start, true
}
