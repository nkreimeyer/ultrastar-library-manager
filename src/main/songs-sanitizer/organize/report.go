package organize

import (
	"bytes"
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	"github.com/nkreimeyer/ultrastar-songs-sanitizer/config"
)

// Report file names in the output dir.
const (
	reportJSON = "organize_plan.json"
	reportTxt  = "organize_plan.txt"
)

// Outcomes of a planned move, as reported.
const (
	outcomePending = "pending" // real run: written before the moves start
	outcomePlanned = "planned" // dry run
	outcomeMoved   = "moved"
	outcomeFailed  = "failed"
	outcomeSkipped = "skipped" // not attempted: the journal could not be written
)

// planReport is the content of organize_plan.json. It is deterministic: no timestamps,
// every list sorted.
type planReport struct {
	DryRun    bool           `json:"dryRun"`
	Priority  bool           `json:"priority"`
	IgnoreThe bool           `json:"ignoreThe"`
	Library   string         `json:"library"`
	Sources   []sourceReport `json:"sources"`
	Notes     []string       `json:"notes,omitempty"`
	Summary   summaryReport  `json:"summary"`
	// ToLibrary are the moves into (or within) the library.
	ToLibrary          []libraryEntry   `json:"toLibrary"`
	AlreadyInPlace     []libraryEntry   `json:"alreadyInPlace"`
	Duplicates         []duplicateEntry `json:"duplicates"`
	Invalid            []invalidEntry   `json:"invalid"`
	LeftInPlace        []leftEntry      `json:"leftInPlace"`
	PossibleDuplicates [][]string       `json:"possibleDuplicates"`
	EmptyFolders       []string         `json:"emptyFolders"` // ignored, "<label>:<rel>"
}

type sourceReport struct {
	Label string `json:"label"`
	Path  string `json:"path"`
	Tier  int    `json:"tier"` // 1 = highest priority (first -s)
}

type summaryReport struct {
	Songs              int `json:"songs"`
	ToLibrary          int `json:"toLibrary"`
	AlreadyInPlace     int `json:"alreadyInPlace"`
	Duplicates         int `json:"duplicates"`
	Invalid            int `json:"invalid"`
	LeftInPlace        int `json:"leftInPlace"`
	PossibleDuplicates int `json:"possibleDuplicates"`
	EmptyFolders       int `json:"emptyFolders"`
	// Moved and Failed count the executed (dry run: planned) moves.
	Moved  int `json:"moved"`
	Failed int `json:"failed"`
	// EmptyCategoriesLeft: category folders a real run emptied in the sources; they are
	// left alone.
	EmptyCategoriesLeft int `json:"emptyCategoriesLeft"`
}

type libraryEntry struct {
	Target  string `json:"target"` // relative to the library, with "/"
	Song    string `json:"song"`   // "<label>:<rel>"
	Name    string `json:"name"`   // "Artist - Title"
	Outcome string `json:"outcome,omitempty"`
}

type duplicateEntry struct {
	Song    string `json:"song"`
	Name    string `json:"name"`
	Keeper  string `json:"keeper"`
	Reason  string `json:"reason"`
	Dest    string `json:"dest"` // relative to the output dir, with "/"
	Outcome string `json:"outcome"`
}

type invalidEntry struct {
	Song        string   `json:"song"`
	Name        string   `json:"name,omitempty"`
	Errors      []string `json:"errors"`
	Replacement string   `json:"replacement,omitempty"` // the working version used instead
	Dest        string   `json:"dest"`
	Outcome     string   `json:"outcome"`
}

type leftEntry struct {
	Song   string `json:"song"`
	Name   string `json:"name,omitempty"`
	Reason string `json:"reason"`
}

// moveOutcome is the result of one planned move.
type moveOutcome struct {
	Status string
	Dst    string // actual target (absolute), if moved
}

// reportInput is everything the report is built from.
type reportInput struct {
	cfg       config.OrganizeConfig
	opts      Options
	cands     []Candidate
	plan      Plan
	outcomes  map[int]moveOutcome // by candidate
	empty     []string            // "<label>:<rel>" of ignored empty folders
	notes     []string
	emptyLeft int
}

// outDest is where a duplicate or invalid song goes, relative to the output dir.
func outDest(kind string, c *Candidate) string {
	return kind + "/" + c.Label + "/" + c.Rel
}

func buildReport(in reportInput) planReport {
	r := planReport{
		DryRun: in.cfg.DryRun, Priority: in.opts.Priority, IgnoreThe: in.opts.IgnoreThe,
		Library: in.cfg.Library, Notes: in.notes,
		ToLibrary: []libraryEntry{}, AlreadyInPlace: []libraryEntry{}, Duplicates: []duplicateEntry{},
		Invalid: []invalidEntry{}, LeftInPlace: []leftEntry{}, PossibleDuplicates: [][]string{},
		EmptyFolders: append([]string{}, in.empty...),
	}
	for _, s := range in.cfg.Sources {
		r.Sources = append(r.Sources, sourceReport{Label: s.Label, Path: s.Path, Tier: s.Tier + 1})
	}
	outcome := func(i int) string {
		if o, ok := in.outcomes[i]; ok {
			return o.Status
		}
		return ""
	}
	ref := func(i int) string {
		if i < 0 {
			return ""
		}
		return in.cands[i].Ref()
	}
	for i, d := range in.plan.Decisions {
		c := &in.cands[i]
		switch d.Action {
		case ActMove:
			r.ToLibrary = append(r.ToLibrary, libraryEntry{Target: d.Target, Song: c.Ref(), Name: c.Song(), Outcome: outcome(i)})
		case ActInPlace:
			r.AlreadyInPlace = append(r.AlreadyInPlace, libraryEntry{Target: d.Target, Song: c.Ref(), Name: c.Song()})
		case ActDuplicate:
			r.Duplicates = append(r.Duplicates, duplicateEntry{Song: c.Ref(), Name: c.Song(), Keeper: ref(d.Other),
				Reason: d.Reason, Dest: outDest("duplicates", c), Outcome: outcome(i)})
		case ActInvalid:
			r.Invalid = append(r.Invalid, invalidEntry{Song: c.Ref(), Name: c.Song(), Errors: append([]string{}, c.Errors...),
				Replacement: ref(d.Other), Dest: outDest("invalid", c), Outcome: outcome(i)})
		case ActLeft:
			r.LeftInPlace = append(r.LeftInPlace, leftEntry{Song: c.Ref(), Name: c.Song(), Reason: d.Reason})
		}
		if o := outcome(i); o == outcomeMoved || o == outcomePlanned {
			r.Summary.Moved++
		} else if o == outcomeFailed {
			r.Summary.Failed++
		}
	}
	for _, g := range in.plan.PossibleDuplicates {
		var refs []string
		for _, i := range g {
			refs = append(refs, ref(i))
		}
		sort.Strings(refs)
		r.PossibleDuplicates = append(r.PossibleDuplicates, refs)
	}
	sort.Slice(r.ToLibrary, func(a, b int) bool { return r.ToLibrary[a].Target < r.ToLibrary[b].Target })
	sort.Slice(r.AlreadyInPlace, func(a, b int) bool { return r.AlreadyInPlace[a].Target < r.AlreadyInPlace[b].Target })
	sort.Slice(r.Duplicates, func(a, b int) bool { return r.Duplicates[a].Song < r.Duplicates[b].Song })
	sort.Slice(r.Invalid, func(a, b int) bool { return r.Invalid[a].Song < r.Invalid[b].Song })
	sort.Slice(r.LeftInPlace, func(a, b int) bool { return r.LeftInPlace[a].Song < r.LeftInPlace[b].Song })
	sort.Slice(r.PossibleDuplicates, func(a, b int) bool {
		return strings.Join(r.PossibleDuplicates[a], "\x00") < strings.Join(r.PossibleDuplicates[b], "\x00")
	})
	sort.Strings(r.EmptyFolders)
	r.Summary.Songs = len(in.cands)
	r.Summary.ToLibrary = len(r.ToLibrary)
	r.Summary.AlreadyInPlace = len(r.AlreadyInPlace)
	r.Summary.Duplicates = len(r.Duplicates)
	r.Summary.Invalid = len(r.Invalid)
	r.Summary.LeftInPlace = len(r.LeftInPlace)
	r.Summary.PossibleDuplicates = len(r.PossibleDuplicates)
	r.Summary.EmptyFolders = len(r.EmptyFolders)
	r.Summary.EmptyCategoriesLeft = in.emptyLeft
	return r
}

func (r planReport) json() ([]byte, error) {
	data, err := json.MarshalIndent(r, "", "  ")
	if err != nil {
		return nil, err
	}
	return append(data, '\n'), nil
}

// text renders the human-readable report.
func (r planReport) text() []byte {
	var b bytes.Buffer
	mode := "real run"
	if r.DryRun {
		mode = "dry run: nothing was moved"
	}
	fmt.Fprintf(&b, "organize plan (%s)\n", mode)
	fmt.Fprintf(&b, "library: %s\n", r.Library)
	prio := "off (quality decides between all sources)"
	if r.Priority {
		prio = "on (a lower tier always wins between working versions)"
	}
	fmt.Fprintf(&b, "priority: %s\n", prio)
	b.WriteString("sources:\n")
	for _, s := range r.Sources {
		fmt.Fprintf(&b, "  tier %d  %s  (%s)\n", s.Tier, s.Label, s.Path)
	}
	for _, n := range r.Notes {
		fmt.Fprintf(&b, "note: %s\n", n)
	}
	s := r.Summary
	moved := "moved"
	if r.DryRun {
		moved = "planned moves"
	}
	fmt.Fprintf(&b, "\nsummary: %d songs: %d to the library, %d already in place, %d duplicates, %d invalid, %d left in place; %d possible duplicates to review; %d empty folders ignored; %d %s, %d failed",
		s.Songs, s.ToLibrary, s.AlreadyInPlace, s.Duplicates, s.Invalid, s.LeftInPlace, s.PossibleDuplicates, s.EmptyFolders, s.Moved, moved, s.Failed)
	if s.EmptyCategoriesLeft > 0 {
		fmt.Fprintf(&b, "; %d emptied category folders left in the sources", s.EmptyCategoriesLeft)
	}
	b.WriteString("\n")

	section := func(title string, n int) {
		fmt.Fprintf(&b, "\n== %s (%d) ==\n", title, n)
	}
	withOutcome := func(o string) string {
		if o == "" || o == outcomePlanned {
			return ""
		}
		return "  [" + o + "]"
	}
	section("Into the library", len(r.ToLibrary))
	for _, e := range r.ToLibrary {
		fmt.Fprintf(&b, "%s  <-  %s%s\n", e.Target, e.Song, withOutcome(e.Outcome))
	}
	section("Already in place", len(r.AlreadyInPlace))
	for _, e := range r.AlreadyInPlace {
		fmt.Fprintf(&b, "%s\n", e.Target)
	}
	section("Duplicates (moved to duplicates/)", len(r.Duplicates))
	for _, e := range r.Duplicates {
		fmt.Fprintf(&b, "%s  (%s)%s\n    kept: %s\n    reason: %s\n", e.Song, e.Name, withOutcome(e.Outcome), e.Keeper, e.Reason)
	}
	section("Invalid (moved to invalid/)", len(r.Invalid))
	for _, e := range r.Invalid {
		fmt.Fprintf(&b, "%s%s\n    errors: %s\n", e.Song, withOutcome(e.Outcome), strings.Join(e.Errors, ", "))
		if e.Replacement != "" {
			fmt.Fprintf(&b, "    used instead: %s\n", e.Replacement)
		}
	}
	section("Left in place", len(r.LeftInPlace))
	for _, e := range r.LeftInPlace {
		fmt.Fprintf(&b, "%s\n    reason: %s\n", e.Song, e.Reason)
	}
	section("Possible duplicates (review manually; nothing was merged)", len(r.PossibleDuplicates))
	for _, g := range r.PossibleDuplicates {
		fmt.Fprintf(&b, "%s\n", strings.Join(g, "  |  "))
	}
	section("Empty folders (ignored)", len(r.EmptyFolders))
	for _, e := range r.EmptyFolders {
		fmt.Fprintf(&b, "%s\n", e)
	}
	return b.Bytes()
}
