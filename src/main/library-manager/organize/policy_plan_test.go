package organize

import (
	"fmt"
	"reflect"
	"strings"
	"testing"
)

// reasonf fills the %s of a reason format.
func reasonf(format string, args ...any) string { return fmt.Sprintf(format, args...) }

// Each BrokenRule in dedupe (LibraryInPlace) and organize, for a broken song with a
// working version and one without.
func TestMakePlanBrokenRules(t *testing.T) {
	poor := Quality{AudioBitrate: 128000}
	dedupe := []Candidate{
		brokenLib("A/ABBA - SOS (bad)", "ABBA", "SOS", "no-notes"),
		libFirst("A/ABBA - SOS", "ABBA", "SOS", poor),
		brokenLib("Q/Queen - Song", "Queen", "Song", "bad-bpm", "missing-file"),
	}
	organize := []Candidate{
		song(hq, "ABBA - SOS", "ABBA", "SOS", poor),
		brokenSong(low1, "SOS bad", "ABBA", "SOS", "no-notes"),
		brokenSong(low1, "Queen", "Queen", "Song", "bad-bpm"),
	}
	tests := []struct {
		name    string
		inPlace bool
		rule    BrokenRule
		cands   []Candidate
		want    []string
	}{
		{"dedupe, by mode", true, BrokenByMode, dedupe, []string{
			"library:A/ABBA - SOS (bad): invalid <library:A/ABBA - SOS> (errors: no-notes)",
			"library:A/ABBA - SOS: in-place A/ABBA - SOS",
			"library:Q/Queen - Song: left (" + reasonf(reasonBrokenStays, "bad-bpm, missing-file") + ")",
		}},
		{"dedupe, keep", true, BrokenKeep, dedupe, []string{
			"library:A/ABBA - SOS (bad): duplicate <library:A/ABBA - SOS> (" + reasonf(reasonBrokenDuplicate, "no-notes") + ")",
			"library:A/ABBA - SOS: in-place A/ABBA - SOS",
			"library:Q/Queen - Song: left (" + reasonf(reasonBrokenKept, "bad-bpm, missing-file") + ")",
		}},
		{"dedupe, set aside", true, BrokenSetAside, dedupe, []string{
			"library:A/ABBA - SOS (bad): invalid <library:A/ABBA - SOS> (errors: no-notes)",
			"library:A/ABBA - SOS: in-place A/ABBA - SOS",
			"library:Q/Queen - Song: invalid (errors: bad-bpm, missing-file)",
		}},
		{"organize, by mode", false, BrokenByMode, organize, []string{
			"hq:ABBA - SOS: library A/ABBA - SOS",
			"low1:SOS bad: invalid <hq:ABBA - SOS> (errors: no-notes)",
			"low1:Queen: invalid (errors: bad-bpm)",
		}},
		{"organize, keep", false, BrokenKeep, organize, []string{
			"hq:ABBA - SOS: library A/ABBA - SOS",
			"low1:SOS bad: duplicate <hq:ABBA - SOS> (" + reasonf(reasonBrokenDuplicate, "no-notes") + ")",
			"low1:Queen: left (" + reasonf(reasonBrokenKept, "bad-bpm") + ")",
		}},
		{"organize, set aside", false, BrokenSetAside, organize, []string{
			"hq:ABBA - SOS: library A/ABBA - SOS",
			"low1:SOS bad: invalid <hq:ABBA - SOS> (errors: no-notes)",
			"low1:Queen: invalid (errors: bad-bpm)",
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := summary(tt.cands, MakePlan(tt.cands, PlanOptions{LibraryInPlace: tt.inPlace, Broken: tt.rule}))
			if !reflect.DeepEqual(got, tt.want) {
				t.Errorf("plan:\n%s\nwant:\n%s", strings.Join(got, "\n"), strings.Join(tt.want, "\n"))
			}
		})
	}
}

// In add, the replacement of a broken library song cannot move in (its target is not a
// song folder). As a duplicate (BrokenKeep) the library song stays like every library
// duplicate of a keeper left outside; set aside, it goes to invalid/ all the same.
func TestMakePlanBrokenRulesKeeperLeft(t *testing.T) {
	nonSong := func(rel string) (string, string, bool) { return rel, "", rel == "A/ABBA - SOS" }
	cands := []Candidate{
		brokenLib("Misc/sos", "ABBA", "SOS", "no-notes"),
		song(low1, "ABBA - SOS", "ABBA", "SOS", Quality{}),
	}
	keeperLeft := "left (" + reasonf(reasonKeeperLeft, "low1:ABBA - SOS") + ")"
	targetLeft := "low1:ABBA - SOS: left (" + reasonf(reasonNotASong, "A/ABBA - SOS") + ")"
	tests := []struct {
		rule BrokenRule
		want []string
	}{
		{BrokenByMode, []string{"library:Misc/sos: " + keeperLeft, targetLeft}},
		{BrokenKeep, []string{"library:Misc/sos: " + keeperLeft, targetLeft}},
		{BrokenSetAside, []string{"library:Misc/sos: invalid <low1:ABBA - SOS> (errors: no-notes)", targetLeft}},
	}
	for _, tt := range tests {
		got := summary(cands, MakePlan(cands, PlanOptions{LibraryInPlace: true, Occupied: nonSong, Broken: tt.rule}))
		if !reflect.DeepEqual(got, tt.want) {
			t.Errorf("rule %d: plan:\n%s\nwant:\n%s", tt.rule, strings.Join(got, "\n"), strings.Join(tt.want, "\n"))
		}
	}
}

// With an excluded working version, a set-aside broken song still goes to invalid/.
func TestMakePlanBrokenSetAsideExcludedReplacement(t *testing.T) {
	cands := []Candidate{
		brokenLib("A/ABBA - SOS (bad)", "ABBA", "SOS", "no-notes"),
		libFirst("A/ABBA - SOS", "ABBA", "SOS", Quality{}),
	}
	opts := PlanOptions{LibraryInPlace: true, Broken: BrokenSetAside, Exclude: map[string]bool{"library:A/ABBA - SOS": true}}
	got := summary(cands, MakePlan(cands, opts))
	want := []string{"library:A/ABBA - SOS (bad): invalid (errors: no-notes)", "library:A/ABBA - SOS: left (excluded in review)"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("plan:\n%s\nwant:\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
}

// KeepDuplicates in dedupe: every working version stays in place, nothing is listed for
// review, broken songs follow the rule.
func TestMakePlanKeepDuplicates(t *testing.T) {
	good := Quality{HasVideo: true, VideoHeight: 1080, AudioBitrate: 320000}
	poor := Quality{AudioBitrate: 128000}
	cands := []Candidate{
		libFirst("Pop/abba sos", "ABBA", "SOS", poor),
		libFirst("Rock/ABBA - SOS", "ABBA", "SOS", good),
		libFirst("Rock/ABBA - SOS (Live)", "ABBA", "SOS (Live)", poor),
		brokenLib("Pop/sos bad", "ABBA", "SOS", "no-notes"),
		brokenLib("Q/Queen - Song", "Queen", "Song", "bad-bpm"),
	}
	// Without it the same songs give a duplicate and a possible duplicate.
	if p := MakePlan(cands, PlanOptions{LibraryInPlace: true}); p.Decisions[0].Action != ActDuplicate || len(p.PossibleDuplicates) == 0 || len(p.Similar) == 0 {
		t.Fatalf("fixture: plan %v, possible %v, similar %v", summary(cands, p), p.PossibleDuplicates, p.Similar)
	}
	kept := []string{
		"library:Pop/abba sos: in-place Pop/abba sos",
		"library:Rock/ABBA - SOS: in-place Rock/ABBA - SOS",
		"library:Rock/ABBA - SOS (Live): in-place Rock/ABBA - SOS (Live)",
	}
	tests := []struct {
		rule BrokenRule
		want []string
	}{
		{BrokenByMode, append(kept[:3:3],
			"library:Pop/sos bad: invalid <library:Rock/ABBA - SOS> (errors: no-notes)",
			"library:Q/Queen - Song: left ("+reasonf(reasonBrokenStays, "bad-bpm")+")")},
		{BrokenKeep, append(kept[:3:3],
			"library:Pop/sos bad: left ("+reasonf(reasonBrokenKept, "no-notes")+")",
			"library:Q/Queen - Song: left ("+reasonf(reasonBrokenKept, "bad-bpm")+")")},
		{BrokenSetAside, append(kept[:3:3],
			"library:Pop/sos bad: invalid <library:Rock/ABBA - SOS> (errors: no-notes)",
			"library:Q/Queen - Song: invalid (errors: bad-bpm)")},
	}
	for _, tt := range tests {
		p := MakePlan(cands, PlanOptions{LibraryInPlace: true, KeepDuplicates: true, Broken: tt.rule})
		if got := summary(cands, p); !reflect.DeepEqual(got, tt.want) {
			t.Errorf("rule %d: plan:\n%s\nwant:\n%s", tt.rule, strings.Join(got, "\n"), strings.Join(tt.want, "\n"))
		}
		if len(p.PossibleDuplicates) != 0 || len(p.Similar) != 0 {
			t.Errorf("rule %d: possible duplicates %v, similar %v, want none", tt.rule, p.PossibleDuplicates, p.Similar)
		}
	}
}
