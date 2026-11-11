package check

import (
	"reflect"
	"strings"
	"testing"
)

// The #END-not-after-#START message names both times in m:ss.s.
func TestAnalyzeEndBeforeStartMessage(t *testing.T) {
	fs := analyze(input(header+"#START:10\n#END:5000\n"+notes(10), noMedia()))
	want := Finding{Severity: SevError, Code: codeBadEnd, Line: 6,
		Message: "#END (0:05.0) is not after #START (0:10.0): nothing would be played"}
	if len(fs) != 1 || fs[0] != want {
		t.Errorf("findings = %+v\nwant [%+v]", fs, want)
	}
}

// More line breaks than notes (a song cut down to one note) are checked like any other.
func TestAnalyzeMoreBreaksThanNotes(t *testing.T) {
	txt := header + ": 10 2 0 a\n- 14\n- 16\n- 18\n- 5\nE\n"
	fs := analyze(input(txt, noMedia()))
	want := []string{"warning:" + codeBreakOrder, "warning:" + codeFewNotes}
	if got := codes(fs); !reflect.DeepEqual(got, want) {
		t.Errorf("codes = %v, want %v (%+v)", got, want, fs)
	}
}

// The overlap check compares each note with the note that ends last so far. Notes ending
// on the same beat keep the earlier one, and a note that extends the end becomes the new
// reference.
func TestAnalyzeOverlapReferenceNote(t *testing.T) {
	tests := []struct {
		name string
		txt  string
		want Finding
	}{
		{
			// Time order: line 6 (0-10), line 7 (5-10, overlaps line 6 and ends on the same
			// beat), line 5 (8-10). Line 5 sorts first in the report, and it overlaps the
			// note of line 6, the first of the two that end at beat 10.
			"same end keeps the earlier note",
			header + ": 8 2 0 c\n: 0 10 0 a\n: 5 5 0 b\n" + pad(100),
			Finding{Severity: SevWarning, Code: codeOverlapping, Line: 5,
				Message: "note at beat 8 starts before the note at line 6 ends (beat 10) (2 occurrences)"},
		},
		{
			// 0-5, 3-13 (overlaps, extends the end to 13), 8-10 (overlaps 3-13 only).
			"a longer note becomes the reference",
			header + ": 0 5 0 a\n: 3 10 0 b\n: 8 2 0 c\n" + pad(100),
			Finding{Severity: SevWarning, Code: codeOverlapping, Line: 6,
				Message: "note at beat 3 starts before the note at line 5 ends (beat 5) (2 occurrences)"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var got []Finding
			for _, f := range analyze(input(tt.txt, noMedia())) {
				if f.Code == codeOverlapping {
					got = append(got, f)
				}
			}
			if len(got) != 1 || got[0] != tt.want {
				t.Errorf("overlap findings = %+v\nwant [%+v]", got, tt.want)
			}
		})
	}
}

// An #END exactly at the end of the audio does not cut the song short: an early end is
// measured against the audio, and the message names it.
func TestAnalyzeEndAtAudioEnd(t *testing.T) {
	files := map[string]fileObs{"MP3": audio("a.mp3", 200)}
	fs := analyze(input(header+"#END:200000\n"+notes(10), files))
	want := Finding{Severity: SevWarning, Code: codeNotesEndEarly, Line: 15,
		Message: "the last note ends at 0:01.9, long before the end of the audio (a.mp3) at 3:20.0 (truncated lyrics or wrong #BPM)"}
	if len(fs) != 1 || fs[0] != want {
		t.Errorf("findings = %+v\nwant [%+v]", fs, want)
	}
}

// Findings equal in every sort key keep their order (the sort is stable).
func TestSortFindingsStable(t *testing.T) {
	in := []Finding{
		{Severity: SevWarning, Code: "c", Line: 3, File: "f", Message: "m", Tag: "MP3"},
		{Severity: SevWarning, Code: "c", Line: 3, File: "f", Message: "m", Tag: "VIDEO"},
	}
	list := append([]Finding(nil), in...)
	sortFindings(list)
	if !reflect.DeepEqual(list, in) {
		t.Errorf("sorted = %+v, want %+v", list, in)
	}
}

// Tool output that is not UTF-8 at all (only continuation bytes) is cut without panicking.
func TestFirstLinesOnlyContinuationBytes(t *testing.T) {
	in := strings.Repeat("\x80", maxMessageLen+10)
	if got := firstLines(in, 3); got != "…" {
		t.Errorf("firstLines = %q, want %q", got, "…")
	}
}

// A repeated tag that does not reference a file is about the txt: players use the last
// occurrence, and the finding carries no file tag.
func TestAnalyzeDuplicateNonFileTag(t *testing.T) {
	fs := analyze(input(header+"#TITLE:T2\n"+notes(10), noMedia()))
	want := Finding{Severity: SevWarning, Code: codeDuplicateTag, Line: 5,
		Message: "#TITLE occurs more than once; players use the last one"}
	if len(fs) != 1 || fs[0] != want {
		t.Errorf("findings = %+v\nwant [%+v]", fs, want)
	}
}
