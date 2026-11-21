package organize

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/nkreimeyer/library-manager/check"
	"github.com/nkreimeyer/library-manager/internal/testutil"
)

// A panic while inspecting one song is counted as a failure; that song stays where it is
// (like an unreadable folder) and the other songs are organized.
func TestRunInspectionPanic(t *testing.T) {
	noFFprobe(t)
	base := t.TempDir()
	src := filepath.Join(base, "src")
	testutil.WriteFiles(t, src, merge(songFiles("Bad", "Bad Artist", "Bad Song"), songFiles("Good", "Good Artist", "Good Song")))
	old := inspectFolder
	t.Cleanup(func() { inspectFolder = old })
	inspectFolder = func(insp *check.Inspector, folder string) (check.Inspection, error) {
		if filepath.Base(folder) == "Bad" {
			panic("inspection bug")
		}
		return old(insp, folder)
	}
	lib := filepath.Join(base, "library")
	stats, rep := runOrganize(t, organizeCfg(t, [][]string{{src}}, lib, filepath.Join(base, "out"), false), Options{})
	if stats.Failed != 1 {
		t.Errorf("Failed = %d, want 1 for the panic", stats.Failed)
	}
	if len(rep.ToLibrary) != 1 || rep.ToLibrary[0].Target != "G/Good Artist - Good Song" {
		t.Errorf("to library: %+v, want only the good song", rep.ToLibrary)
	}
	if len(rep.LeftInPlace) != 1 || !strings.HasSuffix(rep.LeftInPlace[0].Song, ":Bad") || rep.LeftInPlace[0].Reason != reasonUnreadable {
		t.Errorf("left in place: %+v, want the panicked song", rep.LeftInPlace)
	}
	if got := files(t, filepath.Join(src, "Bad")); len(got) != 2 {
		t.Errorf("the panicked song's folder holds %v, want its 2 files", got)
	}
}
