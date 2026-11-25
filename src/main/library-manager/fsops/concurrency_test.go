package fsops

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"testing"

	"github.com/nkreimeyer/library-manager/internal/testutil"
)

// Many goroutines MoveInto and CopyInto the same target name: every call gets its own
// target (" (2)", " (3)", ...), nothing is overwritten, and a dry run hands out the same
// set of names as a real run (go test -race).
func TestMoveIntoCopyIntoConcurrentSameNameDistinctTargets(t *testing.T) {
	const n = 24
	run := func(dry bool) (names []string, dir string) {
		out := t.TempDir()
		srcs := make([]string, n)
		for i := range srcs {
			srcs[i] = filepath.Join(t.TempDir(), "cover.jpg")
			writeFile(t, srcs[i], fmt.Sprint("content ", i), 0o644)
		}
		ops := New(dry)
		targets := make([]string, n)
		var wg sync.WaitGroup
		for i := range srcs {
			wg.Add(1)
			go func() {
				defer wg.Done()
				var err error
				if i%2 == 0 {
					targets[i], err = ops.MoveInto(srcs[i], out, filepath.Join("S", "cover.jpg"))
				} else {
					targets[i], err = ops.CopyInto(srcs[i], out, filepath.Join("S", "cover.jpg"))
				}
				if err != nil {
					t.Errorf("call %d: %v", i, err)
				}
			}()
		}
		wg.Wait()
		seen := map[string]bool{}
		for _, dst := range targets {
			if seen[dst] {
				t.Errorf("dry=%v: target %q handed out twice", dry, dst)
			}
			seen[dst] = true
			names = append(names, filepath.Base(dst))
		}
		sort.Strings(names)
		if !dry {
			got := testutil.DirNames(t, filepath.Join(out, "S"))
			if len(got) != n {
				t.Fatalf("%d files arrived, want %d: %v", len(got), n, got)
			}
			contents := map[string]bool{}
			for _, name := range got {
				contents[testutil.ReadFile(t, filepath.Join(out, "S", name))] = true
			}
			if len(contents) != n {
				t.Errorf("a file was overwritten: %d distinct contents of %d", len(contents), n)
			}
		} else if _, err := os.Stat(filepath.Join(out, "S")); err == nil {
			t.Error("a dry run created the target folder")
		}
		return names, out
	}
	realStats, _ := run(false)
	dry, _ := run(true)
	want := []string{"cover.jpg", "cover (2).jpg", "cover (3).jpg"}
	for _, w := range want {
		found := false
		for _, g := range realStats {
			found = found || g == w
		}
		if !found {
			t.Errorf("real run names lack %q: %v", w, realStats)
		}
	}
	if fmt.Sprint(realStats) != fmt.Sprint(dry) {
		t.Errorf("dry-run names differ from the real run:\nreal %v\ndry  %v", realStats, dry)
	}
}
