package songtxt

import (
	"fmt"
	"math"
	"reflect"
	"strings"
	"testing"
)

func issueCodes(issues []Issue) []string {
	var codes []string
	for _, is := range issues {
		codes = append(codes, is.Code)
	}
	return codes
}

func mustParse(t *testing.T, text string) *Song {
	t.Helper()
	s, issues := Parse(text)
	if len(issues) != 0 {
		t.Fatalf("unexpected issues: %+v", issues)
	}
	return s
}

func TestParseHeaderAndBody(t *testing.T) {
	text := "#TITLE:Mädchen\n#artist: ABBA \n#Mp3:a - b: c.mp3\n#BPM:300,5\n: 0 4 5 la\n- 6\n* 8 2 -3 di\nE\n"
	s := mustParse(t, text)
	wantTags := []Tag{
		{Key: "TITLE", Value: "Mädchen", Line: 1},
		{Key: "artist", Value: "ABBA", Line: 2},
		{Key: "Mp3", Value: "a - b: c.mp3", Line: 3},
		{Key: "BPM", Value: "300,5", Line: 4},
	}
	if !reflect.DeepEqual(s.Tags, wantTags) {
		t.Errorf("tags = %+v, want %+v", s.Tags, wantTags)
	}
	wantNotes := []Note{
		{Type: Normal, Start: 0, Length: 4, Pitch: 5, Text: "la", Line: 5},
		{Type: Golden, Start: 8, Length: 2, Pitch: -3, Text: "di", Line: 7},
	}
	if !reflect.DeepEqual(s.Notes, wantNotes) {
		t.Errorf("notes = %+v, want %+v", s.Notes, wantNotes)
	}
	if want := []LineBreak{{Beat: 6, Line: 6}}; !reflect.DeepEqual(s.Breaks, want) {
		t.Errorf("breaks = %+v, want %+v", s.Breaks, want)
	}
	if !s.HasEnd || s.EndLine != 8 {
		t.Errorf("HasEnd=%v EndLine=%d, want true 8", s.HasEnd, s.EndLine)
	}
	if v, ok := s.Tag("ARTIST"); !ok || v != "ABBA" {
		t.Errorf("Tag(ARTIST) = %q %v", v, ok)
	}
	if v, ok := s.Tag("#mp3"); !ok || v != "a - b: c.mp3" {
		t.Errorf("Tag(#mp3) = %q %v", v, ok)
	}
	if _, ok := s.Tag("VIDEO"); ok {
		t.Error("Tag(VIDEO) found")
	}
	if bpm, err := s.BPM(); err != nil || bpm != 300.5 {
		t.Errorf("BPM = %v %v", bpm, err)
	}
}

func TestParseLineEndings(t *testing.T) {
	for name, sep := range map[string]string{"LF": "\n", "CRLF": "\r\n", "CR": "\r"} {
		t.Run(name, func(t *testing.T) {
			text := "\uFEFF" + strings.Join([]string{"#TITLE:x", "#BPM:100", ": 0 1 2 la ", "- 3", "E", ""}, sep)
			s := mustParse(t, text)
			if len(s.Tags) != 2 || s.Tags[0].Key != "TITLE" || s.Tags[1].Line != 2 {
				t.Errorf("tags = %+v", s.Tags)
			}
			if len(s.Notes) != 1 || s.Notes[0].Text != "la " || s.Notes[0].Line != 3 {
				t.Errorf("notes = %+v", s.Notes)
			}
			if s.EndLine != 5 {
				t.Errorf("EndLine = %d", s.EndLine)
			}
		})
	}
}

func TestParseNoteLines(t *testing.T) {
	tests := []struct {
		line string
		want Note
	}{
		{": 0 4 5 la", Note{Type: Normal, Start: 0, Length: 4, Pitch: 5, Text: "la"}},
		{"* 1 2 3 gold", Note{Type: Golden, Start: 1, Length: 2, Pitch: 3, Text: "gold"}},
		{"F 1 2 3 free", Note{Type: Freestyle, Start: 1, Length: 2, Pitch: 3, Text: "free"}},
		{"R 1 2 3 rap", Note{Type: Rap, Start: 1, Length: 2, Pitch: 3, Text: "rap"}},
		{"G 1 2 3 rapgold", Note{Type: RapGolden, Start: 1, Length: 2, Pitch: 3, Text: "rapgold"}},
		{": 0 4 5  la", Note{Type: Normal, Start: 0, Length: 4, Pitch: 5, Text: " la"}},
		{": 0 4 5 la ", Note{Type: Normal, Start: 0, Length: 4, Pitch: 5, Text: "la "}},
		{": 0 4 5 two words", Note{Type: Normal, Start: 0, Length: 4, Pitch: 5, Text: "two words"}},
		{": 0 4 5", Note{Type: Normal, Start: 0, Length: 4, Pitch: 5, Text: ""}},
		{": 0 4 5 ", Note{Type: Normal, Start: 0, Length: 4, Pitch: 5, Text: ""}},
		{": 0 4 5  ", Note{Type: Normal, Start: 0, Length: 4, Pitch: 5, Text: " "}},
		{":\t0\t4\t5\tla", Note{Type: Normal, Start: 0, Length: 4, Pitch: 5, Text: "la"}},
		{":  0   4  5 \tla", Note{Type: Normal, Start: 0, Length: 4, Pitch: 5, Text: "\tla"}},
		{": 10 0 -12 ~", Note{Type: Normal, Start: 10, Length: 0, Pitch: -12, Text: "~"}},
		{": -2 2 0 pre", Note{Type: Normal, Start: -2, Length: 2, Pitch: 0, Text: "pre"}}, // signed beats, as in USDX
		{":0 4 5 la", Note{Type: Normal, Start: 0, Length: 4, Pitch: 5, Text: "la"}},
		{"*0 4 5 la", Note{Type: Golden, Start: 0, Length: 4, Pitch: 5, Text: "la"}},
		{"  : 0 4 5 la", Note{Type: Normal, Start: 0, Length: 4, Pitch: 5, Text: "la"}},
		{": 0 4 5 Mädchen’s", Note{Type: Normal, Start: 0, Length: 4, Pitch: 5, Text: "Mädchen’s"}},
	}
	for _, tt := range tests {
		t.Run(tt.line, func(t *testing.T) {
			s := mustParse(t, "#BPM:100\n"+tt.line+"\nE")
			tt.want.Line = 2
			if len(s.Notes) != 1 || s.Notes[0] != tt.want {
				t.Errorf("notes = %+v, want %+v", s.Notes, tt.want)
			}
		})
	}
}

func TestParseIssues(t *testing.T) {
	tests := []struct {
		name  string
		text  string
		codes []string
		lines []int
	}{
		{"malformed tag", "#TITLE\n#:x\n: 0 1 2 a", []string{"malformed-tag", "malformed-tag"}, []int{1, 2}},
		{"tag after notes", "#BPM:1\n: 0 1 2 a\n#GAP:5", []string{"tag-after-notes"}, []int{3}},
		{"non-integer note", ": a 1 2 x\n: 0 1,5 2 x\n: 0 1 2.0 x", []string{"malformed-note", "malformed-note", "malformed-note"}, []int{1, 2, 3}},
		{"missing note field", ": 0 1\n:\n* 1", []string{"malformed-note", "malformed-note", "malformed-note"}, []int{1, 2, 3}},
		// F, R and G need a separator ("F0" is not a note); alone they are an empty note.
		{"letter note type alone", "F\nR \nG\t", []string{"malformed-note", "malformed-note", "malformed-note"}, []int{1, 2, 3}},
		{"letter note type without space", "F0 1 2 x\nR0 1 2 x\nG1 2 3 x", []string{"unknown-line", "unknown-line", "unknown-line"}, []int{1, 2, 3}},
		{"negative length", ": 0 -1 2 x\n: -1 1 2 x", []string{"malformed-note"}, []int{1}},
		{"malformed break", "-\n- x\n- 1 y\n- 1 2 3", []string{"malformed-break", "malformed-break", "malformed-break", "malformed-break"}, []int{1, 2, 3, 4}},
		{"malformed bpm change", "B\nB x 100\nB 10\nB 10 0\nB 10 abc\nB -5 100\nB 10 -5", []string{"malformed-bpm", "malformed-bpm", "malformed-bpm", "malformed-bpm", "malformed-bpm", "malformed-bpm", "malformed-bpm"}, []int{1, 2, 3, 4, 5, 6, 7}},
		{"B needs a separator", "B10 100\nBPM 10", []string{"unknown-line", "unknown-line"}, []int{1, 2}},
		{"bad player", "P0\nP4\nP 12\nP9", []string{"bad-player", "bad-player", "bad-player", "bad-player"}, []int{1, 2, 3, 4}},
		{"E needs a separator", "#BPM:1\nEnd\nE\t\n: x", []string{"unknown-line"}, []int{2}},
		{"unknown lines", "hello\nFoo 1 2 3\nPx\nEnd\nBla", []string{"unknown-line", "unknown-line", "unknown-line", "unknown-line", "unknown-line"}, []int{1, 2, 3, 4, 5}},
		{"blank lines are silent", "\n   \n\t\n#BPM:1\n\n: 0 1 2 a\n \nE", nil, nil},
		{"after E is ignored", "#BPM:1\n: 0 1 2 a\nE\ngarbage\n#TITLE:x\n: x", nil, nil},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, issues := Parse(tt.text)
			if got := issueCodes(issues); !reflect.DeepEqual(got, tt.codes) {
				t.Fatalf("codes = %v, want %v (%+v)", got, tt.codes, issues)
			}
			for i, is := range issues {
				if is.Line != tt.lines[i] {
					t.Errorf("issue %d line = %d, want %d", i, is.Line, tt.lines[i])
				}
				if is.Msg == "" {
					t.Errorf("issue %d has no message", i)
				}
			}
		})
	}
}

func TestParseMalformedLinesAreSkipped(t *testing.T) {
	s, issues := Parse("#BPM:1\n: 0 1 2 a\n: x 1 2 b\n- y\n: 4 1 2 c\n#GAP:3\nE")
	if len(issues) != 3 {
		t.Fatalf("issues = %+v", issues)
	}
	if len(s.Notes) != 2 || s.Notes[1].Text != "c" || len(s.Breaks) != 0 {
		t.Errorf("notes=%+v breaks=%+v", s.Notes, s.Breaks)
	}
	// The late tag is reported, but not recorded: players ignore it.
	if v, ok := s.Tag("GAP"); ok {
		t.Errorf("late tag recorded: %q", v)
	}
}

// UltraStar reads the header only up to the first body line, so a tag after it must not
// override (or add to) the header: check and organize use the last occurrence of a tag.
func TestParseLateTagDoesNotOverrideHeader(t *testing.T) {
	s, issues := Parse("#TITLE:Real\n#BPM:300\n#GAP:100\n: 0 1 0 a\n#GAP:900\n#BPM:30\n#TITLE:Late\n#VIDEO:v.mp4\nE")
	if got, want := issueCodes(issues), []string{"tag-after-notes", "tag-after-notes", "tag-after-notes", "tag-after-notes"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("codes = %v, want %v", got, want)
	}
	for i, is := range issues {
		if is.Line != 5+i {
			t.Errorf("issue %d line = %d, want %d", i, is.Line, 5+i)
		}
	}
	want := []Tag{{Key: "TITLE", Value: "Real", Line: 1}, {Key: "BPM", Value: "300", Line: 2}, {Key: "GAP", Value: "100", Line: 3}}
	if !reflect.DeepEqual(s.Tags, want) {
		t.Errorf("tags = %+v, want %+v", s.Tags, want)
	}
	if d := s.Duplicates(); len(d) != 0 {
		t.Errorf("Duplicates = %v, want none", d)
	}
	if ms, err := s.BeatToMs(10); err != nil || ms != 600 {
		t.Errorf("BeatToMs(10) = %v %v, want 600 (header BPM and GAP)", ms, err)
	}
}

func TestParseAfterEndIgnored(t *testing.T) {
	s := mustParse(t, "#BPM:1\n: 0 1 2 a\nE \n: 5 1 2 b\n#TITLE:late")
	if !s.HasEnd || s.EndLine != 3 || len(s.Notes) != 1 {
		t.Errorf("HasEnd=%v EndLine=%d notes=%+v", s.HasEnd, s.EndLine, s.Notes)
	}
	if _, ok := s.Tag("TITLE"); ok {
		t.Error("tag after E was recorded")
	}

	s = mustParse(t, "#BPM:1\n: 0 1 2 a\n")
	if s.HasEnd || s.EndLine != 0 {
		t.Errorf("no E: HasEnd=%v EndLine=%d", s.HasEnd, s.EndLine)
	}
}

func TestParseBreaksNormalMode(t *testing.T) {
	s := mustParse(t, "#BPM:1\n: -6 2 0 x\n- -3\n: 0 2 0 a\n- 4 6\n: 8 2 0 b\n- 12\n: 14 2 0 c\nE")
	want := []LineBreak{{Beat: -3, Line: 3}, {Beat: 4, Line: 5}, {Beat: 12, Line: 7}}
	if !reflect.DeepEqual(s.Breaks, want) {
		t.Errorf("breaks = %+v, want %+v", s.Breaks, want)
	}
	if s.Notes[0].Start != -6 || s.Notes[2].Start != 8 || s.Notes[3].Start != 14 {
		t.Errorf("normal mode must not shift notes: %+v", s.Notes)
	}
}

func TestParseRelative(t *testing.T) {
	tests := []struct {
		name       string
		body       string
		wantStarts []int
		wantBreaks []int
		wantBPM    []int
	}{
		{
			// "- a": break at offset+a, offset += a.
			name:       "single break param",
			body:       ": 0 2 0 a\n: 2 2 0 b\n- 5\n: 1 2 0 c\n- 4\n: 0 2 0 d\nB 2 200",
			wantStarts: []int{0, 2, 6, 9},
			wantBreaks: []int{5, 9},
			wantBPM:    []int{11},
		},
		{
			// "- a b": break at offset+a, offset += b.
			name:       "two break params",
			body:       ": 0 2 0 a\n- 4 6\n: 0 2 0 b\n- 3 5\n: 1 2 0 c",
			wantStarts: []int{0, 6, 12},
			wantBreaks: []int{4, 9},
		},
		{
			// Every player has its own offset.
			name:       "duet offsets per player",
			body:       "P1\n: 0 2 0 a\n- 4\n: 0 2 0 b\nP2\n: 1 2 0 c\n- 3\n: 0 2 0 d",
			wantStarts: []int{0, 4, 1, 3},
			wantBreaks: []int{4, 3},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s := mustParse(t, "#BPM:100\n#relative: YES \n"+tt.body+"\nE")
			if !s.Relative {
				t.Fatal("Relative not set")
			}
			var starts, breaks, bpm []int
			for _, n := range s.Notes {
				starts = append(starts, n.Start)
			}
			for _, b := range s.Breaks {
				breaks = append(breaks, b.Beat)
			}
			for _, c := range s.BPMChanges {
				bpm = append(bpm, c.Beat)
			}
			if !reflect.DeepEqual(starts, tt.wantStarts) || !reflect.DeepEqual(breaks, tt.wantBreaks) || !reflect.DeepEqual(bpm, tt.wantBPM) {
				t.Errorf("starts=%v breaks=%v bpm=%v, want %v %v %v", starts, breaks, bpm, tt.wantStarts, tt.wantBreaks, tt.wantBPM)
			}
		})
	}

	for _, v := range []string{"no", "", "1"} {
		if s := mustParse(t, "#RELATIVE:"+v+"\n: 0 1 0 a\n- 4\n: 1 1 0 b"); s.Relative || s.Notes[1].Start != 1 {
			t.Errorf("#RELATIVE:%q: Relative=%v notes=%+v", v, s.Relative, s.Notes)
		}
	}
	// A #RELATIVE tag after the body started does not switch modes.
	s, _ := Parse(": 0 1 0 a\n#RELATIVE:yes\n- 4\n: 1 1 0 b")
	if s.Relative || s.Notes[1].Start != 1 {
		t.Errorf("late #RELATIVE applied: Relative=%v notes=%+v", s.Relative, s.Notes)
	}
}

func TestParsePlayers(t *testing.T) {
	s := mustParse(t, "#BPM:1\n: 0 1 0 solo\nP1\n: 1 1 0 a\n- 2\nP 2\n: 3 1 0 b\nP3\n: 4 1 0 c\nP\t1\nE")
	if want := []int{1, 2, 3, 1}; !reflect.DeepEqual(s.Players, want) {
		t.Errorf("players = %v, want %v", s.Players, want)
	}
	if want := []int{3, 6, 8, 10}; !reflect.DeepEqual(s.PlayerLines, want) {
		t.Errorf("player lines = %v, want %v", s.PlayerLines, want)
	}
	var got []int
	for _, n := range s.Notes {
		got = append(got, n.Player)
	}
	if want := []int{0, 1, 2, 3}; !reflect.DeepEqual(got, want) {
		t.Errorf("note players = %v, want %v", got, want)
	}
	if s.Breaks[0].Player != 1 {
		t.Errorf("break player = %d", s.Breaks[0].Player)
	}
}

func TestDuplicates(t *testing.T) {
	s := mustParse(t, "#TITLE:a\n#Title:b\n#mp3:x\n#ARTIST:c\n#MP3:y\n#mp3:z\n#BPM:1")
	if got, want := s.Duplicates(), []string{"MP3", "TITLE"}; !reflect.DeepEqual(got, want) {
		t.Errorf("Duplicates = %v, want %v", got, want)
	}
	if v, _ := s.Tag("title"); v != "b" {
		t.Errorf("Tag(title) = %q, want the last occurrence", v)
	}
	if v, _ := s.Tag("#MP3"); v != "z" {
		t.Errorf("Tag(#MP3) = %q, want the last occurrence", v)
	}
	// The accessors read the last occurrence too, as UltraStar does.
	r := mustParse(t, "#BPM:100\n#GAP:10\n#BPM:200\n#GAP:20\n: 0 1 0 a\n")
	if bpm, err := r.BPM(); err != nil || bpm != 200 {
		t.Errorf("BPM = %v, %v; want the last #BPM 200", bpm, err)
	}
	if gap, err := r.GapMs(); err != nil || gap != 20 {
		t.Errorf("GapMs = %v, %v; want the last #GAP 20", gap, err)
	}
	if got := mustParse(t, "#TITLE:a\n#ARTIST:b").Duplicates(); len(got) != 0 {
		t.Errorf("Duplicates = %v, want none", got)
	}
}

func TestParseNumber(t *testing.T) {
	good := map[string]float64{
		"300,5": 300.5, "300.5": 300.5, " 120 ": 120, "-1500": -1500, "0": 0,
		"1e3": 1000, "+7": 7, "\t42,25\t": 42.25, ",5": 0.5,
	}
	for in, want := range good {
		if got, err := ParseNumber(in); err != nil || got != want {
			t.Errorf("ParseNumber(%q) = %v, %v; want %v", in, got, err, want)
		}
	}
	for _, in := range []string{"", "  ", "abc", "NaN", "nan", "Inf", "-Infinity", "1e400", "0x10", "1,000.5", "1.2.3", "1_000", "12 bpm"} {
		if got, err := ParseNumber(in); err == nil {
			t.Errorf("ParseNumber(%q) = %v, want error", in, got)
		}
	}
}

func TestTimingTags(t *testing.T) {
	s := mustParse(t, "#BPM:200\n#GAP:1500,5\n#VIDEOGAP:-2,5\n#START:12.5\n#END:90000")
	if v, err := s.GapMs(); err != nil || v != 1500.5 {
		t.Errorf("GapMs = %v %v", v, err)
	}
	if v, err := s.VideoGapSec(); err != nil || v != -2.5 {
		t.Errorf("VideoGapSec = %v %v", v, err)
	}
	if v, ok, err := s.StartSec(); err != nil || !ok || v != 12.5 {
		t.Errorf("StartSec = %v %v %v", v, ok, err)
	}
	if v, ok, err := s.EndMs(); err != nil || !ok || v != 90000 {
		t.Errorf("EndMs = %v %v %v", v, ok, err)
	}

	empty := mustParse(t, "#TITLE:x\n#GAP:")
	if v, err := empty.GapMs(); err != nil || v != 0 {
		t.Errorf("missing GapMs = %v %v", v, err)
	}
	if v, err := empty.VideoGapSec(); err != nil || v != 0 {
		t.Errorf("missing VideoGapSec = %v %v", v, err)
	}
	if _, ok, err := empty.StartSec(); err != nil || ok {
		t.Errorf("missing StartSec = %v %v", ok, err)
	}
	if _, ok, err := empty.EndMs(); err != nil || ok {
		t.Errorf("missing EndMs = %v %v", ok, err)
	}
	if _, err := empty.BPM(); err == nil {
		t.Error("missing BPM: want error")
	}

	bad := mustParse(t, "#BPM:fast\n#GAP:x\n#VIDEOGAP:y\n#START:z\n#END:w")
	if _, err := bad.BPM(); err == nil {
		t.Error("bad BPM: want error")
	}
	if _, err := bad.GapMs(); err == nil {
		t.Error("bad GAP: want error")
	}
	if _, err := bad.VideoGapSec(); err == nil {
		t.Error("bad VIDEOGAP: want error")
	}
	if _, ok, err := bad.StartSec(); err == nil || !ok {
		t.Error("bad START: want error")
	}
	if _, ok, err := bad.EndMs(); err == nil || !ok {
		t.Error("bad END: want error")
	}
	for _, v := range []string{"0", "-120", ""} {
		if _, err := mustParse(t, "#BPM:"+v).BPM(); err == nil {
			t.Errorf("#BPM:%q: want error", v)
		}
	}
}

func TestBeatToMs(t *testing.T) {
	near := func(a, b float64) bool { return math.Abs(a-b) < 1e-9 }

	// 300 BPM: one beat = 60000 / 1200 = 50 ms.
	s := mustParse(t, "#BPM:300\n#GAP:1000\n: 0 1 0 a")
	for beat, want := range map[float64]float64{0: 1000, 1: 1050, 10: 1500, 2.5: 1125, -2: 900} {
		if got, err := s.BeatToMs(beat); err != nil || !near(got, want) {
			t.Errorf("BeatToMs(%v) = %v %v, want %v", beat, got, err, want)
		}
	}

	// Decimal comma in BPM and GAP.
	s = mustParse(t, "#BPM:150,0\n#GAP:0,5")
	if got, _ := s.BeatToMs(4); !near(got, 400.5) {
		t.Errorf("decimal comma: BeatToMs(4) = %v, want 400.5", got)
	}

	// Piecewise: 300 BPM (50 ms/beat) until beat 10, then 600 BPM (25 ms/beat) from 10,
	// then 150 BPM (100 ms/beat) from 20. The changes are listed out of order.
	s = mustParse(t, "#BPM:300\n#GAP:0\n: 0 1 0 a\nB 20 150\nB 10 600\n: 30 1 0 b\nE")
	if len(s.BPMChanges) != 2 || s.BPMChanges[0].Beat != 20 || s.BPMChanges[1].BPM != 600 {
		t.Fatalf("BPMChanges = %+v", s.BPMChanges)
	}
	for beat, want := range map[float64]float64{0: 0, 5: 250, 10: 500, 14: 600, 20: 750, 22: 950} {
		if got, err := s.BeatToMs(beat); err != nil || !near(got, want) {
			t.Errorf("piecewise BeatToMs(%v) = %v %v, want %v", beat, got, err, want)
		}
	}

	// Change at beat 0 replaces the header BPM from the start; decimal comma in B lines.
	s = mustParse(t, "#BPM:100\nB 0 300,0\n: 0 1 0 a")
	if got, _ := s.BeatToMs(2); !near(got, 100) {
		t.Errorf("change at 0: BeatToMs(2) = %v, want 100", got)
	}

	if _, err := mustParse(t, "#GAP:5").BeatToMs(1); err == nil {
		t.Error("no BPM: want error")
	}
	if _, err := mustParse(t, "#BPM:100\n#GAP:x").BeatToMs(1); err == nil {
		t.Error("bad GAP: want error")
	}
	if _, err := mustParse(t, "#BPM:100").BeatToMs(math.NaN()); err == nil {
		t.Error("NaN beat: want error")
	}
}

func TestNoteBeatRange(t *testing.T) {
	s := mustParse(t, "#BPM:1\n: 10 4 0 a\n: 2 3 0 b\n* 20 1 0 c\n: 5 30 0 d\nE")
	if end, ok := s.LastNoteEndBeat(); !ok || end != 35 {
		t.Errorf("LastNoteEndBeat = %d %v, want 35", end, ok)
	}
	if start, ok := s.FirstNoteStartBeat(); !ok || start != 2 {
		t.Errorf("FirstNoteStartBeat = %d %v, want 2", start, ok)
	}
	// The note that ends last is not the last note line (a long P1 note, then P2).
	if end, ok := mustParse(t, "#BPM:1\nP1\n: 0 100 0 a\nP2\n: 5 2 0 b\nE").LastNoteEndBeat(); !ok || end != 100 {
		t.Errorf("LastNoteEndBeat = %d %v, want 100", end, ok)
	}
	if start, ok := mustParse(t, "#BPM:1\n: 0 1 0 a\n: -4 1 0 b\nE").FirstNoteStartBeat(); !ok || start != -4 {
		t.Errorf("FirstNoteStartBeat = %d %v, want -4", start, ok)
	}
	empty := mustParse(t, "#BPM:1\nE")
	if _, ok := empty.LastNoteEndBeat(); ok {
		t.Error("LastNoteEndBeat on empty song: want false")
	}
	if _, ok := empty.FirstNoteStartBeat(); ok {
		t.Error("FirstNoteStartBeat on empty song: want false")
	}
}

func FuzzParse(f *testing.F) {
	for _, seed := range []string{
		"#TITLE:x\n#BPM:300,5\n#GAP:100\n: 0 4 5 la\n- 6\n* 8 2 -3  di\nE\n",
		"#RELATIVE:yes\r\nP1\r\n: 0 1 0 a\r\n- 4 6\r\nB 2 100\r\nP 2\r\nF 1 1 1\r\nE",
		"#\n#:\n:\n-\nB\nP\nE",
		": 99999999999999999999 1 1 x\n- 9223372036854775807 9223372036854775807",
		"\uFEFF#A:b\rG\t1\t2\t3\t\t",
		"\xff\xfe: 0 1 2 \x00",
	} {
		f.Add(seed)
	}
	f.Fuzz(func(t *testing.T, text string) {
		s, issues := Parse(text)
		n := len(splitLines(text))
		inRange := func(what string, line int) {
			if line < 1 || line > n {
				t.Fatalf("%s line %d out of range 1..%d", what, line, n)
			}
		}
		for _, x := range issues {
			inRange("issue", x.Line)
		}
		for _, x := range s.Tags {
			inRange("tag", x.Line)
		}
		for _, x := range s.Notes {
			inRange("note", x.Line)
		}
		for _, x := range s.Breaks {
			inRange("break", x.Line)
		}
		for _, x := range s.BPMChanges {
			inRange("bpm change", x.Line)
		}
		if s.HasEnd {
			inRange("end", s.EndLine)
		}
		// Derived helpers must not panic either.
		s.Duplicates()
		s.LastNoteEndBeat()
		s.FirstNoteStartBeat()
		_, _ = s.BeatToMs(100)
		_, _ = s.GapMs()
		_, _, _ = s.StartSec()
	})
}

func TestParseMalformedTagMessages(t *testing.T) {
	_, issues := Parse("#NOCOLON\n#:x\n# :y")
	want := []string{"without ':'", "without a key", "without a key"}
	if len(issues) != len(want) {
		t.Fatalf("issues = %+v", issues)
	}
	for i, is := range issues {
		if is.Code != IssueMalformedTag || !strings.Contains(is.Msg, want[i]) {
			t.Errorf("issue %d = %+v, want %s containing %q", i, is, IssueMalformedTag, want[i])
		}
	}
}

func TestParseMalformedBPMChangesAreSkipped(t *testing.T) {
	s, _ := Parse("#BPM:100\n: 0 1 0 a\nB -5 100\nB 10 -5\nB 10 0\nB 12 200\nE")
	if want := []BPMChange{{Beat: 12, BPM: 200, Line: 6}}; !reflect.DeepEqual(s.BPMChanges, want) {
		t.Errorf("BPMChanges = %+v, want %+v", s.BPMChanges, want)
	}
}

// Every body line (note, break, B line, P marker) ends the header: a tag after it is
// reported and has no effect, and a late #RELATIVE does not switch modes.
func TestParseBodyStartsWithAnyBodyLine(t *testing.T) {
	for _, first := range []string{"P1", "- 4", "B 0 100", ": 0 1 0 x", "F 0 1 0 x"} {
		t.Run(first, func(t *testing.T) {
			s, issues := Parse(first + "\n#RELATIVE:yes\n: 0 1 0 a\n- 4\n: 1 1 0 b")
			if got := issueCodes(issues); !reflect.DeepEqual(got, []string{IssueTagAfterNotes}) || issues[0].Line != 2 {
				t.Fatalf("issues = %+v, want tag-after-notes on line 2", issues)
			}
			if s.Relative || len(s.Tags) != 0 {
				t.Errorf("late #RELATIVE applied: Relative=%v tags=%+v", s.Relative, s.Tags)
			}
			last := s.Notes[len(s.Notes)-1]
			if last.Text != "b" || last.Start != 1 {
				t.Errorf("last note = %+v, want b at beat 1", last)
			}
		})
	}
}

func TestParseRelativeLastHeaderTagWins(t *testing.T) {
	for _, tt := range []struct {
		tags     string
		relative bool
		start    int
	}{
		{"#RELATIVE:yes\n#RELATIVE:no", false, 1},
		{"#RELATIVE:no\n#relative:Yes", true, 5},
	} {
		s := mustParse(t, tt.tags+"\n: 0 1 0 a\n- 4\n: 1 1 0 b")
		if s.Relative != tt.relative || s.Notes[1].Start != tt.start {
			t.Errorf("%q: Relative=%v second note at %d, want %v %d", tt.tags, s.Relative, s.Notes[1].Start, tt.relative, tt.start)
		}
	}
}

// RELATIVE duets: P3 has its own offset like P1 and P2, a B line uses the offset of the
// current player, and a negative break moves the offset back.
func TestParseRelativeDuetSections(t *testing.T) {
	s := mustParse(t, "#BPM:100\n#RELATIVE:yes\n"+
		"P1\n: 0 2 0 a\n- 4\n: 0 2 0 b\n"+
		"P2\n: 1 2 0 c\n- 3\nB 2 200\n: 0 2 0 d\n"+
		"P3\n: 2 1 0 e\n- -2\n: 0 1 0 f\n"+
		"P1\n: 1 1 0 g\nB 1 150\nE")
	var starts, breaks []int
	for _, n := range s.Notes {
		starts = append(starts, n.Start)
	}
	for _, b := range s.Breaks {
		breaks = append(breaks, b.Beat)
	}
	if want := []int{0, 4, 1, 3, 2, -2, 5}; !reflect.DeepEqual(starts, want) {
		t.Errorf("starts = %v, want %v", starts, want)
	}
	if want := []int{4, 3, -2}; !reflect.DeepEqual(breaks, want) {
		t.Errorf("breaks = %v, want %v", breaks, want)
	}
	want := []BPMChange{{Beat: 5, BPM: 200, Line: 10}, {Beat: 5, BPM: 150, Line: 18}}
	if !reflect.DeepEqual(s.BPMChanges, want) {
		t.Errorf("BPMChanges = %+v, want %+v", s.BPMChanges, want)
	}
}

func TestBeatToMsEdgeCases(t *testing.T) {
	near := func(a, b float64) bool { return math.Abs(a-b) < 1e-9 }

	// Invalid changes in a hand-built Song are skipped (300 BPM = 50 ms per beat).
	s := &Song{Tags: []Tag{{Key: "BPM", Value: "300"}}, BPMChanges: []BPMChange{
		{Beat: 0, BPM: 0}, {Beat: 2, BPM: -100}, {Beat: 4, BPM: math.NaN()}, {Beat: 6, BPM: math.Inf(1)},
	}}
	if got, err := s.BeatToMs(10); err != nil || !near(got, 500) {
		t.Errorf("invalid changes: BeatToMs(10) = %v %v, want 500", got, err)
	}

	for _, beat := range []float64{math.Inf(1), math.Inf(-1)} {
		if got, err := mustParse(t, "#BPM:100").BeatToMs(beat); err == nil {
			t.Errorf("BeatToMs(%v) = %v, want error", beat, got)
		}
	}

	// Two changes at the same beat: the later line wins (stable order), so 600 BPM until
	// beat 10 is replaced by 150 BPM (100 ms per beat).
	s = mustParse(t, "#BPM:300\nB 10 600\nB 10 150\n: 0 1 0 a\nE")
	if got, err := s.BeatToMs(12); err != nil || !near(got, 700) {
		t.Errorf("same-beat changes: BeatToMs(12) = %v %v, want 700", got, err)
	}
	// The same with many changes in reverse beat order (more than the insertion-sort
	// cutoff of sort.Slice, which is not stable): at every beat 1..40 the second line
	// (200 BPM = 75 ms per beat) must win over the first (100 BPM).
	var b strings.Builder
	b.WriteString("#BPM:300\n: 0 1 0 a\n")
	for beat := 40; beat >= 1; beat-- {
		fmt.Fprintf(&b, "B %d 100\nB %d 200\n", beat, beat)
	}
	s = mustParse(t, b.String()+"E")
	if got, err := s.BeatToMs(41); err != nil || !near(got, 50+40*75) {
		t.Errorf("many same-beat changes: BeatToMs(41) = %v %v, want %v", got, err, 50+40*75)
	}
	// A change at exactly the requested beat does not matter yet.
	s = mustParse(t, "#BPM:300\nB 10 600\n: 0 1 0 a\nE")
	if got, err := s.BeatToMs(10); err != nil || !near(got, 500) {
		t.Errorf("change at the beat: BeatToMs(10) = %v %v, want 500", got, err)
	}
}

func TestParseNumberUpperExponent(t *testing.T) {
	if got, err := ParseNumber("1E3"); err != nil || got != 1000 {
		t.Errorf("ParseNumber(1E3) = %v %v, want 1000", got, err)
	}
}

// Regression: a BPM change at a negative beat (RELATIVE: a negative line break moves the
// next "B 0" before beat 0) was ignored for beats at or before it, so BeatToMs jumped:
// beat -4 came out at -3000 ms and beat -2 at 0 ms, 3 s for 2 beats of 500 ms. A change
// applies from its beat on, on both sides of beat 0.
func TestBeatToMsNegativeBPMChange(t *testing.T) {
	// 15 BPM: 1000 ms per beat; 30 BPM from beat -4 on: 500 ms per beat.
	s := mustParse(t, "#BPM:15\n#GAP:1000\n#RELATIVE:yes\n: 0 1 1 a\n- -4\nB 0 30\n: 2 1 1 b\nE")
	if len(s.BPMChanges) != 1 || s.BPMChanges[0].Beat != -4 {
		t.Fatalf("BPMChanges = %+v, want one change at beat -4", s.BPMChanges)
	}
	for _, tc := range []struct{ beat, want float64 }{
		{-8, -5000}, // 4 beats at 1000 ms before the change, 4 at 500 ms after it
		{-6, -3000},
		{-4, -1000},
		{-2, 0},
		{0, 1000},
		{4, 3000},
	} {
		if got, err := s.BeatToMs(tc.beat); err != nil || math.Abs(got-tc.want) > 1e-9 {
			t.Errorf("BeatToMs(%v) = %v %v, want %v", tc.beat, got, err, tc.want)
		}
	}
	// A change at a positive beat does not affect negative beats.
	s = mustParse(t, "#BPM:15\n#GAP:1000\n: 0 1 1 a\nB 4 30\n: 8 1 1 b\nE")
	for _, tc := range []struct{ beat, want float64 }{{-2, -1000}, {6, 6000}} {
		if got, err := s.BeatToMs(tc.beat); err != nil || math.Abs(got-tc.want) > 1e-9 {
			t.Errorf("change at 4: BeatToMs(%v) = %v %v, want %v", tc.beat, got, err, tc.want)
		}
	}
}
