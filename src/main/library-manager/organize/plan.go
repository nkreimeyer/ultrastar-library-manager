package organize

import (
	"fmt"
	"path"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
)

// Candidate is one song folder found in a source.
type Candidate struct {
	Source int    // flattened index of the source (config.Source.Index)
	Tier   int    // priority tier of the source (config.Source.Tier)
	Label  string // label of the source
	Folder string // absolute path of the song folder
	Rel    string // path relative to the source, with "/"
	// InLibrary: the source is the library itself, so Rel is relative to the library.
	InLibrary bool

	Artist, Title string // #ARTIST / #TITLE as the player reads them (last occurrence)
	// Duet: the txt has duet player markers (P1/P2) or the title a duet qualifier.
	Duet bool
	// MarkedCopy: the folder, the song txt, a media file or #ARTIST/#TITLE ends in
	// "_dupl<n>" (StripCopySuffix), so it is a copy of another version; it loses against
	// every version without the mark. Artist and Title are without the suffix.
	MarkedCopy bool
	// ContentHash fingerprints the notes, timing and lyrics (ContentHash); "" if the song
	// has too few notes. Working songs with the same hash are one song, whatever the tags.
	ContentHash string
	// Profile is what the similarity check compares (lyrics, chart in ms); nil if the song
	// has too few notes.
	Profile *Profile

	// Unreadable: the folder or its txt cannot be read (already logged and counted).
	Unreadable bool
	// EncodingProblem: the txt cannot be decoded unambiguously.
	EncodingProblem bool
	// Broken: check found at least one error; Errors lists their codes (sorted, unique).
	Broken bool
	Errors []string

	Quality Quality
}

// Ref is how a candidate is named in reports and logs: "<label>:<rel>".
func (c *Candidate) Ref() string { return c.Label + ":" + c.Rel }

// Song is the "Artist - Title" display name of the candidate.
func (c *Candidate) Song() string {
	if c.Artist == "" && c.Title == "" {
		return ""
	}
	return c.Artist + " - " + c.Title
}

// Action is what happens to a candidate.
type Action string

const (
	// ActMove moves the keeper of a song into the library (Decision.Target).
	ActMove Action = "library"
	// ActInPlace: the keeper already is at its target in the library.
	ActInPlace Action = "in-place"
	// ActDuplicate moves a losing version to <out>/duplicates/<label>/<rel>.
	ActDuplicate Action = "duplicate"
	// ActInvalid moves a broken song to <out>/invalid/<label>/<rel>.
	ActInvalid Action = "invalid"
	// ActLeft leaves the song where it is (Decision.Reason says why).
	ActLeft Action = "left"
)

// Reasons for ActLeft.
const (
	reasonUnreadable     = "the folder or its txt cannot be read"
	reasonEncoding       = "the txt has an encoding problem (its errors may only come from the guessed codepage); fix the txt first, e.g. with fixtxt"
	reasonTargetExists   = "target exists in the library; add the library as a source (-s) to deduplicate against it"
	reasonTargetOccupied = "target is occupied by %s, which stays in place"
	reasonBucketOccupied = "bucket folder %s is the song folder %s, which stays in place"
	reasonKeeperLeft     = "the better version %s cannot be moved into the library, so this copy stays"
	reasonNotASong       = "target %s exists in the library and is not a song folder (an empty or category folder, or a file); it is never overwritten"
	reasonBrokenStays    = "broken (errors: %s), but no working version replaces it, so it stays; check -k -move-broken moves broken songs"
)

// PlanOptions configure Plan.
type PlanOptions struct {
	// Priority: the tier of the source decides first (see chooseKeeper).
	Priority bool
	// IgnoreThe: bucket by the word after a leading "The ".
	IgnoreThe bool
	// LibraryInPlace (dedupe, add): the library is not reorganized. A keeper from the
	// library stays where it is (ActInPlace at its own path, whatever the bucket layout),
	// and its name is taken for the numbering of the other targets; only keepers from
	// other sources move to <Bucket>/<FolderName>. A broken library song stays unless a
	// working version of the same song replaces it (then it goes to invalid/).
	LibraryInPlace bool
	// Occupied reports whether the library-relative path rel ("A/Artist - Title") is
	// taken in the library as it is before the run: by an entry with the same NameKey in
	// the bucket folder, or by a bucket that cannot take songs (not a folder, a song
	// folder itself, unreadable). occupant is the library-relative path (with "/") of what
	// takes it; note, if not empty, says why the bucket cannot take songs. nil = the
	// library is empty.
	Occupied func(rel string) (occupant, note string, ok bool)
}

// Decision is the plan for one candidate.
type Decision struct {
	Action Action
	// Target is the library-relative path (with "/") for ActMove and ActInPlace.
	Target string
	// Reason: why a duplicate lost (the deciding criterion) or a song is left in place;
	// the error codes of a broken song.
	Reason string
	// Other is the keeper of a duplicate, or for a broken song the working version of
	// the same song that is used instead; -1 if none.
	Other int
}

// Plan is the result of planning: one decision per candidate (same index).
type Plan struct {
	Decisions []Decision
	// PossibleDuplicates lists keepers (candidate indices) of different songs that might
	// still be the same song (equal LooseKey, both duets or both solo), for manual review.
	// Sorted.
	PossibleDuplicates [][]int
	// Similar lists pairs of keepers of different songs that the similarity check found
	// alike but did not join, and every pair inside a PossibleDuplicates group, with the
	// evidence, for review. Sorted by (A, B).
	Similar []Similar
}

// Similar is a pair of keepers (candidate indices, A < B) for review.
type Similar struct {
	A, B     int
	Evidence string
	// Possible: both are in the same PossibleDuplicates group.
	Possible bool
}

// group is one song: all working candidates with the same SongKey, joined with every
// group that shares a content hash with it.
type group struct {
	key     string // the smallest SongKey of its members
	members []int  // candidate indices in (source, rel) order
	keeper  int
	bucket  string
	name    string // folder name without collision suffix
	target  string // final library-relative target
}

// MakePlan decides what happens to every candidate. It is pure: the library state comes
// in through opts.Occupied only.
//
//   - Unreadable candidates and those with an encoding problem are left in place.
//   - Broken candidates go to invalid/; the working version of the same song (if any) is
//     named as the replacement.
//   - Working candidates are grouped by SongKey, and groups whose members share a content
//     hash (same notes, timing and lyrics; duets and solo versions apart) are joined. The
//     best one (see chooseKeeper) is the keeper, the others go to duplicates/ with the
//     deciding criterion as reason (and joinedByContent if their SongKey differs).
//   - The keeper's target is <Bucket>/<FolderName>, plus " [DUET]" for a duet whose title
//     has no duet qualifier. Targets of different songs that collide (NameKey) get " (2)",
//     " (3)", ...: keepers already in place at one of those names keep it, the others are
//     numbered in SongKey order.
//   - A keeper already at its target is in place. A target taken by something that is
//     not a candidate, or by a candidate that stays where it is, leaves the keeper in
//     place: nothing is ever overwritten or merged. If such a keeper comes from another
//     source, the library's own copies of the song stay too (instead of going to
//     duplicates/), so the library never loses the song.
func MakePlan(cands []Candidate, opts PlanOptions) Plan {
	p := Plan{Decisions: make([]Decision, len(cands))}
	for i := range p.Decisions {
		p.Decisions[i].Other = -1
	}
	order := make([]int, len(cands))
	for i := range order {
		order[i] = i
	}
	sort.SliceStable(order, func(a, b int) bool {
		ca, cb := &cands[order[a]], &cands[order[b]]
		if ca.Source != cb.Source {
			return ca.Source < cb.Source
		}
		return ca.Rel < cb.Rel
	})

	songKey := make([]string, len(cands)) // working candidates only
	var working, broken []int
	for pos, i := range order {
		c := &cands[i]
		switch {
		case c.Unreadable:
			p.Decisions[i] = Decision{Action: ActLeft, Reason: reasonUnreadable, Other: -1}
		case c.EncodingProblem:
			p.Decisions[i] = Decision{Action: ActLeft, Reason: reasonEncoding, Other: -1}
		case c.Broken:
			broken = append(broken, i)
		default:
			key := SongKey(c.Artist, c.Title, c.Duet)
			if key == "" {
				// No tags to compare by (only the content hash can join it). The key is
				// the (source, rel) position, zero-padded, so the numbering of such songs
				// follows that order and not the order of cands.
				key = fmt.Sprintf("\x00%09d", pos)
			}
			songKey[i] = key
			working = append(working, i)
		}
	}
	// Join the song keys of candidates with the same content.
	uf := unionFind{}
	joinedBy := map[int]string{}  // how a candidate was joined with another song key
	byContent := map[string]int{} // content key -> first working candidate with it
	for _, i := range working {
		ck := contentKey(&cands[i])
		if ck == "" {
			continue
		}
		if first, ok := byContent[ck]; ok {
			uf.union(songKey[first], songKey[i])
			joinedBy[i], joinedBy[first] = joinedByContent, joinedByContent
		} else {
			byContent[ck] = i
		}
	}
	// Join versions that the similarity check finds to be the same chart.
	var profiled []int
	for _, i := range working {
		if cands[i].Profile != nil {
			profiled = append(profiled, i)
		}
	}
	similar := findSimilar(cands, profiled)
	for _, sp := range similar {
		if similarJoin(cands, sp) {
			uf.union(songKey[sp.A], songKey[sp.B])
			for _, i := range []int{sp.A, sp.B} {
				if joinedBy[i] == "" {
					joinedBy[i] = joinedBySimilar
				}
			}
		}
	}
	groups := map[string]*group{}
	var groupKeys []string
	for _, i := range working {
		key := uf.find(songKey[i])
		g := groups[key]
		if g == nil {
			g = &group{key: key}
			groups[key] = g
			groupKeys = append(groupKeys, key)
		}
		g.members = append(g.members, i)
	}
	sort.Strings(groupKeys)

	// Keepers and duplicates.
	for _, k := range groupKeys {
		g := groups[k]
		members := make([]*Candidate, len(g.members))
		for j, m := range g.members {
			members[j] = &cands[m]
		}
		keeper, reasons := chooseKeeper(members, opts.Priority)
		g.keeper = g.members[keeper]
		for j, m := range g.members {
			if j != keeper {
				reason := reasons[j]
				if songKey[m] != songKey[g.keeper] {
					switch {
					case joinedBy[m] != "":
						reason += joinedBy[m]
					default:
						reason += joinedViaGroup
					}
				}
				p.Decisions[m] = Decision{Action: ActDuplicate, Reason: reason, Other: g.keeper}
			}
		}
	}

	// Broken songs, with the working version used instead.
	for _, i := range broken {
		c := &cands[i]
		d := Decision{Action: ActInvalid, Reason: "errors: " + strings.Join(c.Errors, ", "), Other: -1}
		if key := SongKey(c.Artist, c.Title, c.Duet); key != "" && groups[uf.find(key)] != nil {
			d.Other = groups[uf.find(key)].keeper
		} else if first, ok := byContent[contentKey(c)]; ok {
			d.Other = groups[uf.find(songKey[first])].keeper
		}
		if opts.LibraryInPlace && c.InLibrary && d.Other < 0 {
			d = Decision{Action: ActLeft, Reason: fmt.Sprintf(reasonBrokenStays, strings.Join(c.Errors, ", ")), Other: -1}
		}
		p.Decisions[i] = d
	}

	assignTargets(cands, groups, groupKeys, opts.IgnoreThe, opts.LibraryInPlace)
	for _, k := range groupKeys {
		g := groups[k]
		c := &cands[g.keeper]
		if c.InLibrary && NameKey(c.Rel) == NameKey(g.target) {
			p.Decisions[g.keeper] = Decision{Action: ActInPlace, Target: c.Rel, Other: -1}
		} else {
			p.Decisions[g.keeper] = Decision{Action: ActMove, Target: g.target, Other: -1}
		}
	}
	// A keeper left outside the library must not cost the library its own copies: they
	// stay, which may in turn block other targets.
	for resolveOccupied(cands, p.Decisions, opts.Occupied, opts.LibraryInPlace); keepLibraryLosers(cands, p.Decisions, groups, groupKeys, opts.LibraryInPlace); {
		resolveOccupied(cands, p.Decisions, opts.Occupied, opts.LibraryInPlace)
	}
	p.PossibleDuplicates = possibleDuplicates(cands, groups, groupKeys)
	keepers := map[int]bool{}
	for _, k := range groupKeys {
		keepers[groups[k].keeper] = true
	}
	p.Similar = reviewSimilar(cands, p, similar, func(i int) bool { return keepers[i] })
	return p
}

// assignTargets sets the bucket, name and collision-free target of every group. With
// libraryInPlace, a keeper from the library keeps its own path as target, and no other
// group gets a name with the same NameKey.
func assignTargets(cands []Candidate, groups map[string]*group, keys []string, ignoreThe, libraryInPlace bool) {
	families := map[string][]*group{} // NameKey(bucket/name) -> groups, in SongKey order
	var familyKeys []string
	// claimed holds the NameKey of every bucket/name that is taken, across all families:
	// a numbered name must not be another song's plain name ("X (2)" titled so), nor the
	// numbered name of another family, nor the path of a library song that stays.
	claimed := map[string]bool{}
	staying := map[string]bool{} // NameKeys of the library keepers that stay
	for _, k := range keys {
		g := groups[k]
		c := &cands[g.keeper]
		if libraryInPlace && c.InLibrary {
			g.target = c.Rel
			claimed[NameKey(c.Rel)], staying[NameKey(c.Rel)] = true, true
			continue
		}
		g.bucket = Bucket(c.Artist, ignoreThe)
		g.name = FolderName(c.Artist, c.Title)
		if c.Duet && !IsDuetTitle(c.Title) {
			g.name += " [DUET]"
		}
		fk := NameKey(g.bucket + "/" + g.name)
		if families[fk] == nil {
			familyKeys = append(familyKeys, fk)
		}
		families[fk] = append(families[fk], g)
	}
	for _, fk := range familyKeys {
		claimed[fk] = true // the plain name belongs to its family
	}
	for _, fk := range familyKeys {
		fam := families[fk]
		if len(fam) == 1 {
			continue
		}
		// Keepers already in place at one of the family's numbered names keep it, so a
		// re-run renumbers nothing (the plain name is assigned below).
		for _, g := range fam {
			c := &cands[g.keeper]
			if !c.InLibrary || NameKey(path.Dir(c.Rel)) != NameKey(g.bucket) {
				continue
			}
			base := path.Base(c.Rel)
			if k := NameKey(g.bucket + "/" + base); isFamilyName(base, g.name) && k != fk && !claimed[k] {
				claimed[k] = true
				g.target = c.Rel
			}
		}
	}
	for _, fk := range familyKeys {
		fam := families[fk]
		// The plain name goes to the first group without a target (in SongKey order, or
		// the keeper already in place at it).
		plain := -1
		if staying[fk] {
			plain = len(fam) // taken by a library song that stays: every group is numbered
		}
		for j, g := range fam {
			c := &cands[g.keeper]
			if g.target == "" && c.InLibrary && NameKey(c.Rel) == fk {
				plain = j
				break
			}
		}
		for j, g := range fam {
			if plain < 0 && g.target == "" {
				plain = j
			}
		}
		if plain >= 0 && plain < len(fam) {
			fam[plain].target = fam[plain].bucket + "/" + fam[plain].name
		}
		for _, g := range fam {
			if g.target != "" {
				continue
			}
			for n := 2; ; n++ {
				name := fmt.Sprintf("%s (%d)", g.name, n)
				if k := NameKey(g.bucket + "/" + name); !claimed[k] {
					claimed[k] = true
					g.target = g.bucket + "/" + name
					break
				}
			}
		}
	}
}

// isFamilyName reports whether folder is name itself or name with a collision suffix
// " (n)", n >= 2, compared by NameKey.
func isFamilyName(folder, name string) bool {
	if NameKey(folder) == NameKey(name) {
		return true
	}
	open := strings.LastIndex(folder, " (")
	if open < 0 || !strings.HasSuffix(folder, ")") || NameKey(folder[:open]) != NameKey(name) {
		return false
	}
	num := folder[open+2 : len(folder)-1]
	n, err := strconv.Atoi(num)
	return err == nil && n >= 2 && strconv.Itoa(n) == num
}

// resolveOccupied turns the moves whose target (or bucket folder) is taken by something
// that stays in the library into ActLeft. A target taken by a candidate that moves away
// is fine: the moves are ordered so it is free in time. Leaving a keeper in place can
// make the target of another keeper unavailable, so this repeats until nothing changes.
// With libraryInPlace every library song is a candidate, so a target taken by a
// non-candidate is never a song.
func resolveOccupied(cands []Candidate, ds []Decision, occupied func(string) (string, string, bool), libraryInPlace bool) {
	libCand := map[string]int{} // NameKey of the library-relative path -> candidate
	for i := range cands {
		if cands[i].InLibrary {
			libCand[NameKey(cands[i].Rel)] = i
		}
	}
	// stays reports whether the candidate's folder stays where it is.
	stays := func(i int) bool { return ds[i].Action == ActLeft || ds[i].Action == ActInPlace }
	for changed := true; changed; {
		changed = false
		for i := range ds {
			if ds[i].Action != ActMove {
				continue
			}
			bucket := path.Dir(ds[i].Target)
			if j, ok := libCand[NameKey(bucket)]; ok {
				if stays(j) {
					ds[i] = Decision{Action: ActLeft, Reason: fmt.Sprintf(reasonBucketOccupied, bucket, cands[j].Ref()), Other: -1}
					changed = true
				}
				continue // the song folder named like the bucket moves away first
			}
			if occupied == nil {
				continue
			}
			occ, note, ok := occupied(ds[i].Target)
			if !ok {
				continue
			}
			j, isCand := libCand[NameKey(occ)]
			switch {
			case note != "":
				ds[i] = Decision{Action: ActLeft, Reason: fmt.Sprintf("target %s cannot be used: %s", ds[i].Target, note), Other: -1}
				changed = true
			case !isCand && libraryInPlace:
				ds[i] = Decision{Action: ActLeft, Reason: fmt.Sprintf(reasonNotASong, occ), Other: -1}
				changed = true
			case !isCand:
				reason := reasonTargetExists
				if occ != ds[i].Target {
					reason = fmt.Sprintf("target %s exists in the library as %s; add the library as a source (-s) to deduplicate against it", ds[i].Target, occ)
				}
				ds[i] = Decision{Action: ActLeft, Reason: reason, Other: -1}
				changed = true
			case stays(j):
				ds[i] = Decision{Action: ActLeft, Reason: fmt.Sprintf(reasonTargetOccupied, cands[j].Ref()), Other: -1}
				changed = true
			}
		}
	}
}

// keepLibraryLosers leaves the library members of a group in place when the group's
// keeper comes from another source and is left there (its target cannot be used): moving
// them to duplicates/ would leave the library without the song. With libraryInPlace, a
// broken library song whose replacement is left outside stays as well (a library keeper
// is never left in that mode, so the replacement is from another source). It reports whether
// it changed a decision.
func keepLibraryLosers(cands []Candidate, ds []Decision, groups map[string]*group, keys []string, libraryInPlace bool) bool {
	changed := false
	if libraryInPlace {
		for i := range ds {
			if o := ds[i].Other; ds[i].Action == ActInvalid && cands[i].InLibrary && o >= 0 && ds[o].Action == ActLeft {
				ds[i] = Decision{Action: ActLeft, Reason: fmt.Sprintf(reasonKeeperLeft, cands[o].Ref()), Other: -1}
				changed = true
			}
		}
	}
	for _, k := range keys {
		g := groups[k]
		if ds[g.keeper].Action != ActLeft || cands[g.keeper].InLibrary {
			continue
		}
		for _, m := range g.members {
			if m != g.keeper && cands[m].InLibrary && ds[m].Action == ActDuplicate {
				ds[m] = Decision{Action: ActLeft, Reason: fmt.Sprintf(reasonKeeperLeft, cands[g.keeper].Ref()), Other: -1}
				changed = true
			}
		}
	}
	return changed
}

// possibleDuplicates lists the keepers of different songs whose LooseKey is equal and
// that are both duets or both solo versions. A duet and a solo version of a song are
// different songs (both go to the library, the duet as "... [DUET]"), so they are never
// listed for review. Groups come out in LooseKey order, the solo group before the duet one.
func possibleDuplicates(cands []Candidate, groups map[string]*group, keys []string) [][]int {
	type looseKey struct {
		key  string
		duet bool
	}
	byLoose := map[looseKey][]int{}
	var looseKeys []looseKey
	for _, k := range keys {
		c := &cands[groups[k].keeper]
		lk := looseKey{LooseKey(c.Artist, c.Title), c.Duet || IsDuetTitle(c.Title)}
		if lk.key == "" {
			continue
		}
		if byLoose[lk] == nil {
			looseKeys = append(looseKeys, lk)
		}
		byLoose[lk] = append(byLoose[lk], groups[k].keeper)
	}
	sort.Slice(looseKeys, func(a, b int) bool {
		if looseKeys[a].key != looseKeys[b].key {
			return looseKeys[a].key < looseKeys[b].key
		}
		return !looseKeys[a].duet && looseKeys[b].duet
	})
	var out [][]int
	for _, lk := range looseKeys {
		if len(byLoose[lk]) > 1 {
			out = append(out, byLoose[lk])
		}
	}
	return out
}

// libMove is a planned move of a keeper into or within the library (absolute paths).
type libMove struct {
	Cand     int
	Src, Dst string
}

// step is one move to execute. A move whose target is blocked in a cycle is split into
// two steps through a temporary name: ToTemp (Src -> temp) and then the final step with
// ViaTemp set (temp -> Dst; Orig is the original source, to move back on failure).
type step struct {
	Cand     int
	Src, Dst string
	ToTemp   bool
	ViaTemp  bool
	Orig     string
}

// orderMoves orders the library moves so that every target is free when its move runs:
// a move waits while another pending move still has its source at the target or above
// it (a song folder named like the bucket). If every pending move waits (a cycle, e.g.
// two songs swapping names, or a song folder whose target lies inside itself), the first
// first one whose source blocks a move is moved to tempName(n) first. Moves are taken in target order, so the result is
// deterministic. It is pure.
func orderMoves(moves []libMove, tempName func(n int) string) []step {
	pending := append([]libMove(nil), moves...)
	sort.SliceStable(pending, func(a, b int) bool { return pending[a].Dst < pending[b].Dst })
	orig := map[int]string{}
	var steps []step
	temps := 0
	for len(pending) > 0 {
		next := -1
		for i := range pending {
			if !moveBlocked(pending[i], pending) {
				next = i
				break
			}
		}
		if next < 0 {
			// Every move waits: move one whose source blocks a move (a member of a
			// cycle) out of the way. A temporary source blocks nothing, so every move
			// goes through a temporary name at most once.
			m := &pending[blockingMove(pending)]
			tmp := tempName(temps)
			temps++
			orig[m.Cand] = m.Src
			steps = append(steps, step{Cand: m.Cand, Src: m.Src, Dst: tmp, ToTemp: true})
			m.Src = tmp
			continue
		}
		m := pending[next]
		o, via := orig[m.Cand]
		steps = append(steps, step{Cand: m.Cand, Src: m.Src, Dst: m.Dst, ViaTemp: via, Orig: o})
		pending = append(pending[:next], pending[next+1:]...)
	}
	return steps
}

// moveBlocked reports whether m's target is still taken by the source of a pending move:
// the same path (another move) or a path above it (any move, including m itself).
func moveBlocked(m libMove, pending []libMove) bool {
	dst := NameKey(filepath.Clean(m.Dst))
	for _, n := range pending {
		src := NameKey(filepath.Clean(n.Src))
		if (src == dst && n.Cand != m.Cand) || strings.HasPrefix(dst, src+string(filepath.Separator)) {
			return true
		}
	}
	return false
}

// blockingMove returns the index of the first pending move whose source blocks a pending
// move. One exists whenever every pending move is blocked.
func blockingMove(pending []libMove) int {
	for i, m := range pending {
		for _, n := range pending {
			if moveBlocked(n, []libMove{m}) {
				return i
			}
		}
	}
	return 0
}
