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
