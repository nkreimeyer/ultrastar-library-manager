package organize

import (
	"strings"
	"testing"
)

// A joined pair is one song: one keeper, the other version a duplicate whose reason says
// who joined it; the possible-duplicate listing no longer has them.
func TestMakePlanJoin(t *testing.T) {
	cands := []Candidate{
		song(hq, "a", "Nirvana", "Smells Like Teen Spirit", Quality{HasVideo: true}),
		song(hq, "b", "Nirvana", "Smells Like Teen Spirit (Live)", Quality{}),
		song(hq, "c", "Other", "Song", Quality{}),
		brokenSong(hq, "d", "Nirvana", "Smells Like Teen Spirit (Acoustic)", "no-notes"),
	}
	base := MakePlan(cands, PlanOptions{})
	if len(base.Similar) == 0 || base.Decisions[1].Action == ActDuplicate {
		t.Fatalf("the versions should be apart: %+v", base)
	}
	join := [][2]string{{cands[0].Ref(), cands[1].Ref()}}
	p := MakePlan(cands, PlanOptions{Join: join})
	if d := p.Decisions[1]; d.Action != ActDuplicate || d.Other != 0 || !strings.HasSuffix(d.Reason, joinedByUser) {
		t.Errorf("b = %+v, want a duplicate of a, joined by the user", d)
	}
	if d := p.Decisions[0]; d.Action != ActMove {
		t.Errorf("a = %+v, want the keeper", d)
	}
	if len(p.PossibleDuplicates) != 0 || len(p.Similar) != 0 {
		t.Errorf("listed after the join: %v %v", p.PossibleDuplicates, p.Similar)
	}
	// Pairs that name an unknown, broken or excluded song have no effect.
	for name, o := range map[string]PlanOptions{
		"unknown":  {Join: [][2]string{{cands[0].Ref(), "nope"}}},
		"broken":   {Join: [][2]string{{cands[0].Ref(), cands[3].Ref()}}},
		"excluded": {Join: join, Exclude: map[string]bool{cands[1].Ref(): true}},
	} {
		if d := MakePlan(cands, o).Decisions[0]; d.Action != ActMove {
			t.Errorf("%s: a = %+v", name, d)
		}
		if got := MakePlan(cands, o).Decisions[1]; got.Action == ActDuplicate {
			t.Errorf("%s: b = %+v, want no duplicate", name, got)
		}
	}
	// A pair is symmetric.
	q := MakePlan(cands, PlanOptions{Join: [][2]string{{cands[1].Ref(), cands[0].Ref()}}})
	if d := q.Decisions[1]; d.Action != ActDuplicate || d.Other != 0 {
		t.Errorf("reversed pair: %+v", d)
	}
}

// The reason of a joined duplicate names the mechanism that joined it: a copy that only
// shares the tags of a song the reviewer joined is not said to match "by its notes", and
// a content twin with the same tags does not hide the reviewer's join.
func TestMakePlanJoinReasons(t *testing.T) {
	video := Quality{HasVideo: true}
	tests := []struct {
		name      string
		cands     []Candidate
		join      [2]int         // candidates the reviewer joins; -1 = none
		wantEnds  map[int]string // candidate -> suffix of its reason
		wantOther int
	}{
		{"same-tag copy of a joined song", []Candidate{
			song(hq, "x1", "Alpha", "Song", Quality{}),
			song(hq, "x2", "Alpha", "Song", Quality{}),
			song(hq, "y", "Beta", "Song", video),
		}, [2]int{0, 2}, map[int]string{0: joinedByUser, 1: joinedViaGroupUser}, 2},
		{"content twin with the same tags", []Candidate{
			withHash(song(hq, "a1", "Alpha", "Song", Quality{}), "h1"),
			withHash(song(hq, "a2", "Alpha", "Song", Quality{}), "h1"),
			withHash(song(hq, "b", "Beta", "Song", video), "h2"),
		}, [2]int{0, 2}, map[int]string{0: joinedByUser, 1: joinedViaGroupUser}, 2},
		{"same-tag copy of a content match", []Candidate{
			withHash(song(hq, "a1", "Alpha", "Song", Quality{}), "h1"),
			song(hq, "a2", "Alpha", "Song", Quality{}),
			withHash(song(hq, "b", "Beta", "Song", video), "h1"),
		}, [2]int{-1, -1}, map[int]string{0: joinedByContent, 1: joinedViaGroup}, 2},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var opts PlanOptions
			if tt.join[0] >= 0 {
				opts.Join = [][2]string{{tt.cands[tt.join[0]].Ref(), tt.cands[tt.join[1]].Ref()}}
			}
			p := MakePlan(tt.cands, opts)
			for i, want := range tt.wantEnds {
				if d := p.Decisions[i]; d.Action != ActDuplicate || d.Other != tt.wantOther || !strings.HasSuffix(d.Reason, want) {
					t.Errorf("%s = %+v, want a duplicate of %s ending in %q", tt.cands[i].Ref(), d, tt.cands[tt.wantOther].Ref(), want)
				}
			}
		})
	}
}
