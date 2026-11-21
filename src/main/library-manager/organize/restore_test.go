package organize

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/nkreimeyer/library-manager/config"
	"github.com/nkreimeyer/library-manager/fsops"
)

// addWithBetterSource sets up `add` of a source whose copy of the library's song is better
// (it has a video): the library copy loses and goes to duplicates/, the source copy is to
// take its place.
func addWithBetterSource(t *testing.T, dry bool) (cfg config.OrganizeConfig, lib, src string, before []string) {
	t.Helper()
	noFFprobe(t)
	base := t.TempDir()
	lib, src = filepath.Join(base, "lib"), filepath.Join(base, "usb", "songs")
	writeFiles(t, lib, songFiles("A/ABBA - SOS", "ABBA", "SOS"))
	writeFiles(t, src, songFiles("ABBA - SOS", "ABBA", "SOS", "video"))
	cfg = prepared(t)(config.NewAdd([][]string{{src}}, lib, filepath.Join(base, "out"), dry))
	return cfg, lib, src, tree(t, lib)
}

func duplicateOutcome(rep planReport, song string) string {
	for _, d := range rep.Duplicates {
		if d.Song == song {
			return d.Outcome
		}
	}
	return "<not a duplicate>"
}

// The library's copy goes to duplicates/ before the better copy's move into the library,
// which then fails (here: the source cannot be written). The library copy is moved back,
// so the library still has the song, and -undo still reverts the whole run.
func TestApplyRestoresLibrarySongWhenKeeperMoveFails(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root ignores permissions")
	}
	cfg, lib, src, before := addWithBetterSource(t, false)
	if err := os.Chmod(src, 0o555); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.Chmod(src, 0o755) })

	st, rep := runOrganize(t, cfg, Options{Mode: ModeAdd})
	if got := tree(t, lib); !reflect.DeepEqual(got, before) {
		t.Errorf("library:\n%s\nwant:\n%s", strings.Join(got, "\n"), strings.Join(before, "\n"))
	}
	if o := duplicateOutcome(rep, "lib:A/ABBA - SOS"); o != outcomeRestored {
		t.Errorf("library copy outcome = %q, want %q; report %+v", o, outcomeRestored, rep.Duplicates)
	}
	if len(rep.ToLibrary) != 1 || rep.ToLibrary[0].Outcome != outcomeFailed {
		t.Errorf("to library = %+v, want the failed keeper", rep.ToLibrary)
	}
	if st.Failed != 1 || st.Moved != 2 {
		t.Errorf("stats = %+v, want the failed keeper move, the move out and the move back", st)
	}
	if text := string(rep.text()); !strings.Contains(text, "[moved back]") {
		t.Errorf("text report does not show the move back:\n%s", text)
	}
	if err := os.Chmod(src, 0o755); err != nil {
		t.Fatal(err)
	}
	if st := runUndo(t, cfg); st.Failed != 0 || st.Moved != 2 {
		t.Errorf("undo: %+v, want both moves reverted", st)
	}
	if got := tree(t, lib); !reflect.DeepEqual(got, before) {
		t.Errorf("library after undo:\n%s", strings.Join(got, "\n"))
	}
	if got := files(t, cfg.DuplicatesDir()); len(got) != 0 {
		t.Errorf("files in duplicates/ after undo: %v", got)
	}
}

// A run stopped between the move out and the keeper's move moves the library copy back
// too, in a dry run as in a real one.
func TestApplyRestoresLibrarySongAfterStop(t *testing.T) {
	for _, dry := range []bool{false, true} {
		t.Run(fmt.Sprintf("dry=%v", dry), func(t *testing.T) {
			cfg, lib, src, before := addWithBetterSource(t, dry)
			srcBefore := tree(t, src)
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			progress := func(phase string, done, _ int) {
				if phase == PhaseMove && done == 1 {
					cancel() // the move out runs, the keeper's move is not attempted
				}
			}
			ops := fsops.New(dry)
			if err := Run(cfg, ops, Options{Mode: ModeAdd, Ctx: ctx, Progress: progress}); !errors.Is(err, context.Canceled) {
				t.Fatalf("Run = %v, want context.Canceled", err)
			}
			if got := tree(t, lib); !reflect.DeepEqual(got, before) {
				t.Errorf("library:\n%s\nwant:\n%s", strings.Join(got, "\n"), strings.Join(before, "\n"))
			}
			if got := tree(t, src); !reflect.DeepEqual(got, srcBefore) {
				t.Errorf("source changed:\n%s", strings.Join(got, "\n"))
			}
			if st := ops.Stats(); st.Moved != 2 || st.Failed != 0 {
				t.Errorf("stats = %+v, want the move out and back", st)
			}
			data, err := os.ReadFile(cfg.ReportPath(reportJSON))
			if err != nil {
				t.Fatal(err)
			}
			if !strings.Contains(string(data), `"outcome": "`+outcomeRestored+`"`) {
				t.Errorf("report does not show the move back:\n%s", data)
			}
		})
	}
}

// Which losers are moved back when their keeper's move fails: library duplicates always,
// broken library songs only when the library stays in place (dedupe, add; organize sends
// broken songs to invalid/ whatever happens to their replacement), never songs from
// other sources, and nothing whose keeper is a library song (it is still in the library).
func TestRestoreLibraryLosersSelection(t *testing.T) {
	for _, inPlace := range []bool{false, true} {
		t.Run(fmt.Sprintf("inPlace=%v", inPlace), func(t *testing.T) {
			noFFprobe(t)
			base := t.TempDir()
			lib, src, out := filepath.Join(base, "lib"), filepath.Join(base, "src"), filepath.Join(base, "out")
			writeFiles(t, lib, merge(songFiles("A/dup", "A", "S"), songFiles("A/broken", "A", "T"),
				songFiles("B/kept", "B", "K"), songFiles("B/lost", "B", "K")))
			writeFiles(t, src, merge(songFiles("keeper1", "A", "S"), songFiles("keeper2", "A", "T"), songFiles("other", "A", "S")))
			cfg := organizeCfg(t, [][]string{{lib}, {src}}, lib, out, false)
			c := func(label, root, rel string, inLib bool) Candidate {
				return Candidate{Label: label, Rel: rel, Folder: filepath.Join(root, filepath.FromSlash(rel)), InLibrary: inLib}
			}
			cands := []Candidate{
				c("lib", lib, "A/dup", true),    // 0: loses to 4
				c("lib", lib, "A/broken", true), // 1: broken, replaced by 5
				c("lib", lib, "B/kept", true),   // 2: library keeper, its move fails
				c("lib", lib, "B/lost", true),   // 3: loses to 2
				c("src", src, "keeper1", false), // 4
				c("src", src, "keeper2", false), // 5
				c("src", src, "other", false),   // 6: loses to 4
			}
			plan := Plan{Decisions: []Decision{
				{Action: ActDuplicate, Other: 4},
				{Action: ActInvalid, Other: 5},
				{Action: ActMove, Target: "K/kept", Other: -1},
				{Action: ActDuplicate, Other: 2},
				{Action: ActMove, Target: "A/keeper1", Other: -1},
				{Action: ActMove, Target: "A/keeper2", Other: -1},
				{Action: ActDuplicate, Other: 4},
			}}
			ops := fsops.New(false)
			a := newApplier(cfg, ops, cands, map[int]moveOutcome{})
			a.libraryInPlace = inPlace
			a.moveTo = func(s, d string) error {
				if strings.HasPrefix(d, lib) && (s == cands[2].Folder || s == cands[4].Folder || s == cands[5].Folder) {
					ops.Fail()
					return errors.New("injected failure")
				}
				return ops.MoveTo(s, d)
			}
			a.apply(plan)
			want := map[int]string{0: outcomeRestored, 1: outcomeMoved, 3: outcomeMoved, 6: outcomeMoved}
			if inPlace {
				want[1] = outcomeRestored
			}
			for i, w := range want {
				if got := a.outcomes[i].Status; got != w {
					t.Errorf("candidate %d (%s): outcome %q, want %q", i, cands[i].Rel, got, w)
				}
				if _, err := os.Stat(cands[i].Folder); (err == nil) != (w == outcomeRestored) {
					t.Errorf("candidate %d (%s) in its place: %v, want %v", i, cands[i].Rel, err == nil, w == outcomeRestored)
				}
			}
		})
	}
}
