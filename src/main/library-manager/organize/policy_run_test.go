package organize

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/nkreimeyer/library-manager/config"
	"github.com/nkreimeyer/library-manager/fsops"
	"github.com/nkreimeyer/library-manager/internal/testutil"
)

// KeepDuplicates outside dedupe is refused before anything is read or moved.
func TestRunKeepDuplicatesOnlyInDedupe(t *testing.T) {
	noFFprobe(t)
	for _, m := range []Mode{"", ModeOrganize, ModeAdd} {
		base := t.TempDir()
		lib, src, out := filepath.Join(base, "lib"), filepath.Join(base, "src"), filepath.Join(base, "out")
		testutil.WriteFiles(t, lib, songFiles("A/ABBA - SOS", "ABBA", "SOS"))
		testutil.WriteFiles(t, src, songFiles("ABBA - SOS", "ABBA", "SOS", "video"))
		cfg, err := NewConfig(m, [][]string{{src}}, lib, out, false)
		cfg = prepared(t)(cfg, err)
		before, srcBefore := tree(t, lib), tree(t, src)
		ops := fsops.New(false)
		err = Run(cfg, ops, Options{Mode: m, KeepDuplicates: true})
		if err == nil || !strings.Contains(err.Error(), "only possible in dedupe") {
			t.Errorf("mode %q: Run = %v, want the dedupe-only error", m, err)
		}
		if got := tree(t, lib); !reflect.DeepEqual(got, before) {
			t.Errorf("mode %q: library changed:\n%s", m, strings.Join(got, "\n"))
		}
		if got := tree(t, src); !reflect.DeepEqual(got, srcBefore) {
			t.Errorf("mode %q: source changed:\n%s", m, strings.Join(got, "\n"))
		}
		for _, p := range []string{cfg.ReportPath(reportJSON), cfg.JournalPath()} {
			if _, err := os.Stat(p); err == nil {
				t.Errorf("mode %q: %s was written", m, p)
			}
		}
	}
}

// dedupe with BrokenSetAside moves a broken song without a working version to invalid/,
// and -undo moves it back.
func TestRunDedupeBrokenSetAside(t *testing.T) {
	noFFprobe(t)
	base := t.TempDir()
	lib := filepath.Join(base, "library")
	testutil.WriteFiles(t, lib, merge(songFiles("A/ABBA - SOS", "ABBA", "SOS"), map[string]string{
		"B/Blur - Song 2/song.txt": "#ARTIST:Blur\n#TITLE:Song 2\n#MP3:song.mp3\n: 0 2 0 la\nE\n", // no #BPM
		"B/Blur - Song 2/song.mp3": "x",
	}))
	before := tree(t, lib)
	cfg := prepared(t)(config.NewDedupe(lib, filepath.Join(base, "out"), false))
	st, rep := runOrganize(t, cfg, Options{Mode: ModeDedupe, Broken: BrokenSetAside})
	if st.Moved != 1 || st.Failed != 0 {
		t.Errorf("stats = %+v, want the broken song moved", st)
	}
	if len(rep.Invalid) != 1 || rep.Invalid[0].Song != "library:B/Blur - Song 2" || rep.Invalid[0].Dest != "invalid/library/B/Blur - Song 2" {
		t.Errorf("invalid = %+v", rep.Invalid)
	}
	if len(rep.LeftInPlace) != 0 {
		t.Errorf("left in place = %+v", rep.LeftInPlace)
	}
	if _, err := os.Stat(filepath.Join(cfg.InvalidDir(), "library", "B", "Blur - Song 2", "song.txt")); err != nil {
		t.Error(err)
	}
	if ust := runUndo(t, cfg); ust.Failed != 0 || ust.Moved != 1 {
		t.Errorf("undo: %+v", ust)
	}
	if got := tree(t, lib); !reflect.DeepEqual(got, before) {
		t.Errorf("undo did not restore the library:\n%s\nwant:\n%s", strings.Join(got, "\n"), strings.Join(before, "\n"))
	}
}

// dedupe with KeepDuplicates and BrokenKeep moves nothing; the report gives the reasons,
// and Review's Replan plans with the same options.
func TestRunDedupeKeepDuplicates(t *testing.T) {
	noFFprobe(t)
	base := t.TempDir()
	lib := filepath.Join(base, "library")
	dedupeLibrary(t, lib)
	before := tree(t, lib)
	cfg := prepared(t)(config.NewDedupe(lib, filepath.Join(base, "out"), false))
	var replanned, planned Plan
	review := func(r *Review) error {
		planned, replanned = r.Plan, r.Replan(nil)
		return nil
	}
	st, rep := runOrganize(t, cfg, Options{Mode: ModeDedupe, KeepDuplicates: true, Broken: BrokenKeep, Review: review})
	if st.Moved != 0 || len(rep.Duplicates) != 0 || len(rep.Invalid) != 0 || len(rep.PossibleDuplicates) != 0 {
		t.Errorf("moved %d, duplicates %+v, invalid %+v, possible %v", st.Moved, rep.Duplicates, rep.Invalid, rep.PossibleDuplicates)
	}
	if got := tree(t, lib); !reflect.DeepEqual(got, before) {
		t.Errorf("library changed:\n%s", strings.Join(got, "\n"))
	}
	left := map[string]string{}
	for _, e := range rep.LeftInPlace {
		left[e.Song] = e.Reason
	}
	for _, s := range []string{"library:Pop/ABBA - SOS (broken)", "library:B/Blur - Song 2"} {
		if !strings.HasSuffix(left[s], "; setting broken songs aside is turned off") {
			t.Errorf("%s: reason %q", s, left[s])
		}
	}
	if !strings.Contains(string(rep.text()), "setting broken songs aside is turned off") {
		t.Errorf("text report lacks the reason:\n%s", rep.text())
	}
	if !reflect.DeepEqual(planned, replanned) {
		t.Errorf("Replan differs from the plan:\n%v\nwant:\n%v", replanned, planned)
	}
}

// add with BrokenSetAside: a broken library song stays in invalid/ when its replacement's
// move into the library fails (by mode it would be moved back).
func TestRunAddBrokenSetAsideNotMovedBack(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root ignores permissions")
	}
	noFFprobe(t)
	base := t.TempDir()
	lib, src := filepath.Join(base, "lib"), filepath.Join(base, "usb", "songs")
	testutil.WriteFiles(t, lib, map[string]string{
		"A/ABBA - SOS/song.txt": "#ARTIST:ABBA\n#TITLE:SOS\n#MP3:song.mp3\n: 0 2 0 la\nE\n", // no #BPM
		"A/ABBA - SOS/song.mp3": "x",
	})
	testutil.WriteFiles(t, src, songFiles("ABBA - SOS", "ABBA", "SOS"))
	cfg := prepared(t)(config.NewAdd([][]string{{src}}, lib, filepath.Join(base, "out"), false))
	if err := os.Chmod(src, 0o555); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.Chmod(src, 0o755) })
	_, rep := runOrganize(t, cfg, Options{Mode: ModeAdd, Broken: BrokenSetAside})
	if len(rep.Invalid) != 1 || rep.Invalid[0].Outcome != outcomeMoved {
		t.Errorf("invalid = %+v, want the library song moved and not back", rep.Invalid)
	}
	if _, err := os.Stat(filepath.Join(cfg.InvalidDir(), "lib", "A", "ABBA - SOS", "song.txt")); err != nil {
		t.Error(err)
	}
}

// add with BrokenKeep: a broken library song is a duplicate of the new version, and when
// that version's move into the library fails it is moved back like every library
// duplicate.
func TestRunAddBrokenKeepMovedBack(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root ignores permissions")
	}
	noFFprobe(t)
	base := t.TempDir()
	lib, src := filepath.Join(base, "lib"), filepath.Join(base, "usb", "songs")
	testutil.WriteFiles(t, lib, map[string]string{
		"A/ABBA - SOS/song.txt": "#ARTIST:ABBA\n#TITLE:SOS\n#MP3:song.mp3\n: 0 2 0 la\nE\n", // no #BPM
		"A/ABBA - SOS/song.mp3": "x",
	})
	testutil.WriteFiles(t, src, songFiles("ABBA - SOS", "ABBA", "SOS"))
	before := tree(t, lib)
	cfg := prepared(t)(config.NewAdd([][]string{{src}}, lib, filepath.Join(base, "out"), false))
	if err := os.Chmod(src, 0o555); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.Chmod(src, 0o755) })
	_, rep := runOrganize(t, cfg, Options{Mode: ModeAdd, Broken: BrokenKeep})
	if len(rep.Invalid) != 0 || len(rep.Duplicates) != 1 || rep.Duplicates[0].Outcome != outcomeRestored ||
		!strings.HasSuffix(rep.Duplicates[0].Reason, "; the working version is kept") {
		t.Errorf("invalid = %+v, duplicates = %+v, want the library song moved back", rep.Invalid, rep.Duplicates)
	}
	if got := tree(t, lib); !reflect.DeepEqual(got, before) {
		t.Errorf("library:\n%s\nwant:\n%s", strings.Join(got, "\n"), strings.Join(before, "\n"))
	}
}
