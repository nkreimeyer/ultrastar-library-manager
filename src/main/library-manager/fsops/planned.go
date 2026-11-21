package fsops

import (
	"slices"
	"sort"
	"strings"
)

// Sub returns s - o, field by field: the work done between two Stats snapshots.
func (s Stats) Sub(o Stats) Stats {
	return Stats{
		Written:  s.Written - o.Written,
		Moved:    s.Moved - o.Moved,
		Removed:  s.Removed - o.Removed,
		Replaced: s.Replaced - o.Replaced,
		Copied:   s.Copied - o.Copied,
		Failed:   s.Failed - o.Failed,
	}
}

// PlannedMove is a move a dry run planned: Src is where the file or folder is, Dst where
// it would go (with a collision suffix such as " (2)" already applied). Both are cleaned
// paths.
type PlannedMove struct{ Src, Dst string }

// PlannedWrites returns the paths of the files a dry run planned to write, sorted. Only
// WriteFileAtomic calls that change a file are planned writes (an unchanged content
// writes nothing, in a real run too); a path written several times is listed once.
// Reports (WriteReport, WriteJSONReport) are not included: they are written to disk in a
// dry run too. Nil in a real run, and nil if nothing was planned.
func (o *Ops) PlannedWrites() []string {
	if !o.DryRun {
		return nil
	}
	o.writesMu.Lock()
	paths := make([]string, 0, len(o.plannedWrites))
	for p := range o.plannedWrites {
		paths = append(paths, p)
	}
	o.writesMu.Unlock()
	if len(paths) == 0 {
		return nil
	}
	sort.Strings(paths)
	return paths
}

// PlannedMoves returns the moves a dry run planned with MoveInto and MoveTo (also the
// single entries a MoveInto merge moves), sorted by Src. Not included: CopyInto, Rename,
// Replace, RemoveEmptyDir and the removal of a merged, emptied folder.
//
// Each planned operation is listed as it was performed, so a chain stays a chain: a
// MoveTo through a temporary name (organize's orderMoves) shows up as A -> tmp and
// tmp -> B, and a target moved on later appears both as a Dst and as a Src. Moves with
// the same Src keep the order they were planned in. Nil in a real run, and nil if
// nothing was planned. The result is a copy.
func (o *Ops) PlannedMoves() []PlannedMove {
	if !o.DryRun {
		return nil
	}
	o.plannedMu.Lock()
	moves := slices.Clone(o.plannedList)
	o.plannedMu.Unlock()
	if len(moves) == 0 {
		return nil
	}
	slices.SortStableFunc(moves, func(a, b PlannedMove) int { return strings.Compare(a.Src, b.Src) })
	return moves
}
