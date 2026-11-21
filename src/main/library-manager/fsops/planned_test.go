package fsops

import (
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"sync"
	"testing"
)

func TestStatsSub(t *testing.T) {
	a := Stats{Written: 10, Moved: 9, Removed: 8, Replaced: 7, Copied: 6, Failed: 5}
	b := Stats{Written: 1, Moved: 2, Removed: 3, Replaced: 4, Copied: 5, Failed: 6}
	want := Stats{Written: 9, Moved: 7, Removed: 5, Replaced: 3, Copied: 1, Failed: -1}
	if got := a.Sub(b); got != want {
		t.Errorf("Sub = %+v, want %+v", got, want)
	}
}

func TestPlannedWrites(t *testing.T) {
	dir := t.TempDir()
	same := filepath.Join(dir, "same.txt")
	changed := filepath.Join(dir, "b", "changed.txt")
	created := filepath.Join(dir, "a.txt")
	if err := os.MkdirAll(filepath.Dir(changed), 0o755); err != nil {
		t.Fatal(err)
	}
	for _, p := range []string{same, changed} {
		if err := os.WriteFile(p, []byte("old"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	ops := New(true)
	if ops.PlannedWrites() != nil {
		t.Errorf("PlannedWrites before any write = %v, want nil", ops.PlannedWrites())
	}
	mustWrite := func(p, data string) {
		t.Helper()
		if _, err := ops.WriteFileAtomic(p, []byte(data)); err != nil {
			t.Fatal(err)
		}
	}
	mustWrite(same, "old") // unchanged: no write
	mustWrite(changed, "new")
	mustWrite(changed, "newer") // listed once
	mustWrite(created, "x")
	if err := ops.WriteReport(filepath.Join(dir, "out", "report.txt"), []byte("r")); err != nil {
		t.Fatal(err)
	}
	if err := ops.WriteJSONReport(filepath.Join(dir, "out", "report.json"), []int{1}); err != nil {
		t.Fatal(err)
	}
	got := ops.PlannedWrites()
	want := []string{created, changed}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("PlannedWrites = %q, want %q", got, want)
	}
	got[0] = "mutated"
	if ops.PlannedWrites()[0] != created {
		t.Error("PlannedWrites returned the internal state")
	}
}

func TestPlannedMoves(t *testing.T) {
	lib, out := t.TempDir(), t.TempDir()
	for _, p := range []string{
		filepath.Join(lib, "A", "Song"),
		filepath.Join(lib, "B", "Song"),
		filepath.Join(out, "dup", "Song"),
	} {
		if err := os.MkdirAll(p, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(p, "song.txt"), []byte("#TITLE:x\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	ops := New(true)
	if ops.PlannedMoves() != nil {
		t.Errorf("PlannedMoves before any move = %v, want nil", ops.PlannedMoves())
	}
	// B first, so the result must be sorted by Src, not in planning order.
	dst, err := ops.MoveInto(filepath.Join(lib, "B", "Song"), filepath.Join(out, "dup"), "Song")
	if err != nil {
		t.Fatal(err)
	}
	if want := filepath.Join(out, "dup", "Song (2)"); dst != want {
		t.Fatalf("MoveInto = %s, want %s", dst, want)
	}
	tmp := filepath.Join(lib, ".organize-tmp-1")
	if err := ops.MoveTo(filepath.Join(lib, "A", "Song"), tmp); err != nil {
		t.Fatal(err)
	}
	if err := ops.MoveTo(tmp, filepath.Join(lib, "C", "Song")); err != nil {
		t.Fatal(err)
	}
	// Not moves: a copy and the removal of an empty folder.
	if _, err := ops.CopyInto(filepath.Join(out, "dup", "Song", "song.txt"), out, "copy.txt"); err != nil {
		t.Fatal(err)
	}
	if _, err := ops.RemoveEmptyDir(filepath.Join(lib, "A")); err != nil {
		t.Fatal(err)
	}
	want := []PlannedMove{
		{Src: tmp, Dst: filepath.Join(lib, "C", "Song")},
		{Src: filepath.Join(lib, "A", "Song"), Dst: tmp},
		{Src: filepath.Join(lib, "B", "Song"), Dst: filepath.Join(out, "dup", "Song (2)")},
	}
	got := ops.PlannedMoves()
	if !reflect.DeepEqual(got, want) {
		t.Errorf("PlannedMoves =\n%q\nwant\n%q", got, want)
	}
	got[0].Dst = "mutated"
	if ops.PlannedMoves()[0] != want[0] {
		t.Error("PlannedMoves returned the internal state")
	}
}

// Moves with the same Src keep their planning order: a path moved away, refilled and
// moved again.
func TestPlannedMovesSameSrcStable(t *testing.T) {
	lib := t.TempDir()
	a, b, c := filepath.Join(lib, "a"), filepath.Join(lib, "b"), filepath.Join(lib, "c")
	if err := os.WriteFile(a, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(b, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	ops := New(true)
	for _, m := range [][2]string{{a, c}, {b, a}, {a, filepath.Join(lib, "d")}} {
		if err := ops.MoveTo(m[0], m[1]); err != nil {
			t.Fatal(err)
		}
	}
	want := []PlannedMove{{a, c}, {a, filepath.Join(lib, "d")}, {b, a}}
	if got := ops.PlannedMoves(); !reflect.DeepEqual(got, want) {
		t.Errorf("PlannedMoves = %q, want %q", got, want)
	}
}

func TestPlannedRealRunNil(t *testing.T) {
	lib := t.TempDir()
	src := filepath.Join(lib, "a.txt")
	if err := os.WriteFile(src, []byte("a"), 0o644); err != nil {
		t.Fatal(err)
	}
	ops := New(false)
	if _, err := ops.WriteFileAtomic(src, []byte("b")); err != nil {
		t.Fatal(err)
	}
	if _, err := ops.MoveInto(src, filepath.Join(lib, "out"), "a.txt"); err != nil {
		t.Fatal(err)
	}
	if w := ops.PlannedWrites(); w != nil {
		t.Errorf("PlannedWrites in a real run = %q, want nil", w)
	}
	if m := ops.PlannedMoves(); m != nil {
		t.Errorf("PlannedMoves in a real run = %q, want nil", m)
	}
}

// A MoveInto that merges a folder lists the moves of its entries, not the folder itself
// (it is removed once emptied, which is no move).
func TestPlannedMovesMerge(t *testing.T) {
	lib, out := t.TempDir(), t.TempDir()
	for _, p := range []string{filepath.Join(lib, "Cat", "Song1"), filepath.Join(lib, "Cat", "Song2")} {
		if err := os.MkdirAll(p, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(lib, "Cat", "notes.txt"), nil, 0o644); err != nil {
		t.Fatal(err)
	}
	ops := New(true)
	if _, err := ops.MoveInto(filepath.Join(lib, "Cat", "Song1"), out, filepath.Join("Cat", "Song1")); err != nil {
		t.Fatal(err)
	}
	// out/Cat received a move of this run: the leftover category is merged into it.
	if _, err := ops.MoveInto(filepath.Join(lib, "Cat"), out, "Cat"); err != nil {
		t.Fatal(err)
	}
	want := []PlannedMove{
		{Src: filepath.Join(lib, "Cat", "Song1"), Dst: filepath.Join(out, "Cat", "Song1")},
		{Src: filepath.Join(lib, "Cat", "Song2"), Dst: filepath.Join(out, "Cat", "Song2")},
		{Src: filepath.Join(lib, "Cat", "notes.txt"), Dst: filepath.Join(out, "Cat", "notes.txt")},
	}
	if got := ops.PlannedMoves(); !reflect.DeepEqual(got, want) {
		t.Errorf("PlannedMoves =\n%q\nwant\n%q", got, want)
	}
}

// The accessors may run while other goroutines plan moves and writes (go test -race).
func TestPlannedConcurrent(t *testing.T) {
	lib, out := t.TempDir(), t.TempDir()
	const n = 20
	ops := New(true)
	var wg sync.WaitGroup
	for i := range n {
		name := fmt.Sprintf("f%02d", i)
		if err := os.WriteFile(filepath.Join(lib, name), nil, 0o644); err != nil {
			t.Fatal(err)
		}
		wg.Add(3)
		go func() {
			defer wg.Done()
			if _, err := ops.MoveInto(filepath.Join(lib, name), out, name); err != nil {
				t.Error(err)
			}
		}()
		go func() {
			defer wg.Done()
			if _, err := ops.WriteFileAtomic(filepath.Join(lib, name+".txt"), []byte("x")); err != nil {
				t.Error(err)
			}
		}()
		go func() {
			defer wg.Done()
			_ = ops.PlannedMoves()
			_ = ops.PlannedWrites()
		}()
	}
	wg.Wait()
	if got := len(ops.PlannedMoves()); got != n {
		t.Errorf("PlannedMoves has %d entries, want %d", got, n)
	}
	if got := len(ops.PlannedWrites()); got != n {
		t.Errorf("PlannedWrites has %d entries, want %d", got, n)
	}
}
