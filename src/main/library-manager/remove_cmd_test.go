package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// remove -undo with only -o: a dry run plans the moves back, a real run makes them; both
// end with the summary line.
func TestRunRemoveUndoWithOutDir(t *testing.T) {
	base, lib, _, _ := cliFixture(t)
	out := filepath.Join(base, "out")
	code, _, logs := runCaptured(t, "remove", "-l", lib, "-o", out, "-artist", "Cher")
	if code != exitOK {
		t.Fatalf("remove = %d\n%s", code, strings.Join(logs, "\n"))
	}
	containsAll(t, "remove", logs, []string{"Output dir: " + out, "Library: " + lib + "; 1 pattern(s)", "Done: 1 moved, 0 failed operations"})
	moved := filepath.Join(out, "removed", "C", "Cher - Believe", "song.txt")
	if _, err := os.Stat(moved); err != nil {
		t.Fatal(err)
	}
	code, _, logs = runCaptured(t, "remove", "-undo", "-o", out, "-n")
	if code != exitOK {
		t.Fatalf("remove -undo -n = %d", code)
	}
	containsAll(t, "undo dry run", logs, []string{"Dry run: nothing will be moved back (the planned moves are only logged)",
		"Output dir: " + out, "Undo: 1 move(s) planned, 0 failed", "Done (planned): 1 moved, 0 failed operations"})
	for _, l := range logs {
		if strings.Contains(l, "plan report") { // undo writes none: the one there is the remove run's
			t.Errorf("undo dry run logged %q", l)
		}
	}
	if _, err := os.Stat(moved); err != nil {
		t.Errorf("the dry run moved the song back: %v", err)
	}
	code, _, logs = runCaptured(t, "remove", "-undo", "-o", out)
	if code != exitOK {
		t.Fatalf("remove -undo = %d", code)
	}
	containsAll(t, "undo", logs, []string{"Undo: 1 move(s) moved back, 0 failed", "Done: 1 moved, 0 failed operations"})
	if _, err := os.Stat(filepath.Join(lib, "C", "Cher - Believe", "song.txt")); err != nil {
		t.Errorf("not moved back: %v", err)
	}
}
