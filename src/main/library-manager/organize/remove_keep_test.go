package organize

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/nkreimeyer/library-manager/fsops"
	"github.com/nkreimeyer/library-manager/internal/testutil"
)

// A song in Keep matches but stays, and the report says so.
func TestRemoveWithKeep(t *testing.T) {
	cfg, lib, out := restoreSetup(t)
	opts := RemoveOptions{Keep: map[string]bool{"A/ABBA - SOS": true, "Q/Queen - Rock": true}} // the second does not match
	if err := RemoveWith(cfg, fsops.New(false), removePatterns(t, "ABBA"), opts); err != nil {
		t.Fatal(err)
	}
	for rel, want := range map[string]bool{"A/ABBA - SOS": true, "A/ABBA - Waterloo": false, "Q/Queen - Rock": true} {
		if got := testutil.Exists(filepath.Join(lib, filepath.FromSlash(rel), "song.txt")); got != want {
			t.Errorf("%s in the library = %v, want %v", rel, got, want)
		}
	}
	rep := testutil.ReadFile(t, filepath.Join(out, "remove_plan.json"))
	for _, want := range []string{`"leftInPlace"`, ReasonKept, `"removed": 1`} {
		if !strings.Contains(rep, want) {
			t.Errorf("report lacks %q:\n%s", want, rep)
		}
	}
}
