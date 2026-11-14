package organize

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestReadJournal(t *testing.T) {
	path := filepath.Join(t.TempDir(), "organize-journal.jsonl")
	data := `{"op":"run","run":"r1","time":"t1"}
{"op":"mkdir","run":"r1","dst":"/lib/A"}
{"op":"move","run":"r1","src":"/s/a","dst":"/lib/A/a"}
{"op":"run","run":"r2","time":"t2"}
{"op":"move","run":"r2","src":"/s/b","dst":"/lib/B/b"}
{"op":"undone","run":"r2"}
not json
{"op":"move","run":"r3"}
{"op":"run","run":"r4","time":"t4"}
{"op":"move","run":"r4","src":"/s/c","dst":"/lib/C/c"}
{"op":"move","run":"r4","src":"/s/d","dst":"/lib/D/d"}
{"op":"move","run":"r4","src":"/s/e","dst":"/l`
	if err := os.WriteFile(path, []byte(data), 0o644); err != nil {
		t.Fatal(err)
	}
	runs, err := readJournal(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(runs) != 3 { // r3 has only an invalid move
		t.Fatalf("runs = %d", len(runs))
	}
	r := lastUndoable(runs)
	if r == nil || r.ID != "r4" || r.moves() != 2 {
		t.Fatalf("last undoable = %+v (the truncated last line must be skipped)", r)
	}
	r.Undone = true
	if r := lastUndoable(runs); r == nil || r.ID != "r1" || len(r.Entries) != 2 || r.Entries[0].Op != opMkdir {
		t.Errorf("after r4: %+v (r2 is undone, r3 has no moves)", r)
	}
	if runs, err := readJournal(filepath.Join(t.TempDir(), "missing")); err != nil || runs != nil {
		t.Errorf("missing journal = %v, %v", runs, err)
	}
}

func TestNewRunIDUnique(t *testing.T) {
	now := time.Now()
	seen := map[string]bool{}
	for range 100 {
		id := newRunID(now)
		if seen[id] {
			t.Fatalf("duplicate run id %s", id)
		}
		seen[id] = true
	}
}
