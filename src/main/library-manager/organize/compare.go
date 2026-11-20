package organize

import (
	"errors"
	"fmt"
	"sort"
)

// ErrPlanChanged is returned (wrapped) by a Review hook that finds the plan different
// from the one the user reviewed.
var ErrPlanChanged = errors.New("the plan changed since it was reviewed")

// PlanDiff lists how plan b differs from plan a in what is moved where, one line per
// song folder (by Ref), sorted. Duplicate reasons and the review lists (possible
// duplicates, similar songs) are not compared: they move nothing. Empty = same moves.
func PlanDiff(aCands []Candidate, a Plan, bCands []Candidate, b Plan) []string {
	type entry struct {
		action Action
		target string
		other  string // Ref of the keeper or replacement
	}
	index := func(cands []Candidate, p Plan) map[string]entry {
		m := make(map[string]entry, len(cands))
		for i, d := range p.Decisions {
			e := entry{action: d.Action, target: d.Target}
			if d.Other >= 0 {
				e.other = cands[d.Other].Ref()
			}
			m[cands[i].Ref()] = e
		}
		return m
	}
	describe := func(e entry) string {
		s := string(e.action)
		if e.target != "" {
			s += " " + e.target
		}
		if e.other != "" {
			s += " (instead: " + e.other + ")"
		}
		return s
	}
	ea, eb := index(aCands, a), index(bCands, b)
	var diff []string
	for ref, x := range ea {
		y, ok := eb[ref]
		switch {
		case !ok:
			diff = append(diff, fmt.Sprintf("%s: gone (was: %s)", ref, describe(x)))
		case x != y:
			diff = append(diff, fmt.Sprintf("%s: %s, was: %s", ref, describe(y), describe(x)))
		}
	}
	for ref, y := range eb {
		if _, ok := ea[ref]; !ok {
			diff = append(diff, fmt.Sprintf("%s: new (%s)", ref, describe(y)))
		}
	}
	sort.Strings(diff)
	return diff
}
