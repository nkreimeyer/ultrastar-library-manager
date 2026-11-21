package organize

import (
	"bytes"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/nkreimeyer/library-manager/config"
	"github.com/nkreimeyer/library-manager/internal/testutil"
	log "github.com/sirupsen/logrus"
	logtest "github.com/sirupsen/logrus/hooks/test"
)

// messages returns the messages of the entries at level or more severe.
func messages(hook *logtest.Hook, level log.Level) []string {
	var out []string
	for _, e := range hook.AllEntries() {
		if e.Level <= level {
			out = append(out, e.Message)
		}
	}
	return out
}

// A crash can leave a long run of NUL bytes (or other garbage) without a newline at the
// end of the journal: such a line is skipped like any other unparsable line, and the moves
// before and after it can still be undone.
func TestReadJournalLongGarbageLine(t *testing.T) {
	path := filepath.Join(t.TempDir(), "organize-journal.jsonl")
	var data bytes.Buffer
	data.WriteString(`{"op":"run","run":"r1","time":"t1"}` + "\n")
	data.WriteString(`{"op":"move","run":"r1","src":"/s/a","dst":"/lib/A/a"}` + "\n")
	data.Write(bytes.Repeat([]byte{0}, 200*1024)) // longer than the initial 64 KiB buffer
	data.WriteString("\n")
	data.WriteString(`{"op":"move","run":"r1","src":"/s/b","dst":"/lib/B/b"}` + "\n")
	if err := os.WriteFile(path, data.Bytes(), 0o644); err != nil {
		t.Fatal(err)
	}
	runs, err := readJournal(path)
	if err != nil {
		t.Fatalf("readJournal: %v", err)
	}
	if len(runs) != 1 || runs[0].moves() != 2 {
		t.Fatalf("runs = %+v, want r1 with 2 moves", runs)
	}
}

// The warnings about skipped journal lines name the right line number (1-based, blank
// lines counted), so the user can find the line in the file.
func TestReadJournalWarningLineNumbers(t *testing.T) {
	hook := testutil.CaptureLogs(t)
	path := filepath.Join(t.TempDir(), "organize-journal.jsonl")
	data := `{"op":"run","run":"r1","time":"t1"}

{"op":"move","run":"r1","src":"/s/a"}
{"op":"move","run":"r1","src":"/s/a","dst":"/lib/A/a"}
{truncated
`
	if err := os.WriteFile(path, []byte(data), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := readJournal(path); err != nil {
		t.Fatal(err)
	}
	got := strings.Join(messages(hook, log.WarnLevel), "\n")
	for _, want := range []string{"line 3: move without src/dst", "line 5 cannot be parsed"} {
		if !strings.Contains(got, want) {
			t.Errorf("warnings %q do not contain %q", got, want)
		}
	}
}

// Undo reports how many moves it reverted and how many failed, and logs no error when
// everything works: the run's moves, its journal entries, the removal of the folders it
// created and the "undone" record all succeed.
func TestUndoLogsResult(t *testing.T) {
	noFFprobe(t)
	base := t.TempDir()
	src, lib, out := filepath.Join(base, "src"), filepath.Join(base, "lib"), filepath.Join(base, "out")
	files := merge(songFiles("a", "Abba", "SOS"), songFiles("b", "Bee Gees", "Tragedy"), songFiles("c", "Cher", "Believe"))
	testutil.WriteFiles(t, src, files)
	cfg := organizeCfg(t, [][]string{{src}}, lib, out, false)

	hook := testutil.CaptureLogs(t)
	if st, _ := runOrganize(t, cfg, Options{}); st.Moved != 3 {
		t.Fatalf("moved %d", st.Moved)
	}
	if errs := messages(hook, log.ErrorLevel); len(errs) != 0 {
		t.Errorf("run logged errors: %q", errs)
	}
	hook.Reset()
	if st := runUndo(t, organizeCfg(t, nil, lib, out, false)); st.Moved != 3 || st.Failed != 0 {
		t.Fatalf("undo: %+v", st)
	}
	if errs := messages(hook, log.ErrorLevel); len(errs) != 0 {
		t.Errorf("undo logged errors: %q", errs)
	}
	if infos := messages(hook, log.InfoLevel); !slicesContain(infos, "Undo: 3 move(s) moved back, 0 failed") {
		t.Errorf("undo log: %q", infos)
	}
	if _, err := os.Stat(filepath.Join(lib, "A")); !os.IsNotExist(err) {
		t.Errorf("bucket A created by the run not removed: %v", err)
	}

	// Again, with one song moved away by hand: one move fails.
	hook.Reset()
	if st, _ := runOrganize(t, cfg, Options{}); st.Moved != 3 {
		t.Fatalf("second run moved %d", st.Moved)
	}
	if err := os.Rename(filepath.Join(lib, "B", "Bee Gees - Tragedy"), filepath.Join(base, "elsewhere")); err != nil {
		t.Fatal(err)
	}
	hook.Reset()
	if st := runUndo(t, organizeCfg(t, nil, lib, out, false)); st.Moved != 2 || st.Failed != 1 {
		t.Fatalf("undo: %+v", st)
	}
	if infos := messages(hook, log.InfoLevel); !slicesContain(infos, "Undo: 2 move(s) moved back, 1 failed (1 gone)") {
		t.Errorf("undo log: %q", infos)
	}
}

func slicesContain(ss []string, s string) bool {
	for _, x := range ss {
		if x == s {
			return true
		}
	}
	return false
}

// SongKey: an apostrophe is removed from a word even when it sits between a letter and
// its combining mark; empty brackets are an empty qualifier in title and artist; an
// artist starting with "FT" (FT Island) is not a featured-artist part.
func TestSongKeyEdgeCases(t *testing.T) {
	for _, tc := range []struct{ a1, t1, a2, t2 string }{
		{"Artist", "и'̆", "Artist", "й"},
		{"ABBA", "SOS () (Remastered)", "ABBA", "SOS"},
		{"ABBA", "SOS [] - Live", "ABBA", "SOS (Live)"},
		{"ABBA ()", "SOS", "ABBA", "SOS"},
	} {
		k1, k2 := SongKey(tc.a1, tc.t1, false), SongKey(tc.a2, tc.t2, false)
		if k1 == "" || k1 != k2 {
			t.Errorf("SongKey(%q, %q) = %q, SongKey(%q, %q) = %q: want equal", tc.a1, tc.t1, k1, tc.a2, tc.t2, k2)
		}
	}
	if got, want := SongKey("FT Island", "Love Love Love", false), "ftisland|lovelovelove"; got != want {
		t.Errorf("SongKey(FT Island) = %q, want %q", got, want)
	}
	if got, want := LooseKey("Ft. Island", "Love"), "ftisland|love"; got != want {
		t.Errorf("LooseKey(Ft. Island) = %q, want %q", got, want)
	}
}

// A name just over MaxFolderNameBytes is shortened to exactly the limit.
func TestFolderNameJustOverLimit(t *testing.T) {
	for extra := 1; extra <= 8; extra++ {
		title := strings.Repeat("x", MaxFolderNameBytes-len("A - ")+extra)
		got := FolderName("A", title)
		if len(got) != MaxFolderNameBytes || !strings.HasPrefix(got, "A - xxx") {
			t.Errorf("+%d bytes: FolderName = %q (%d bytes), want %d bytes", extra, got, len(got), MaxFolderNameBytes)
		}
	}
	exact := strings.Repeat("x", MaxFolderNameBytes-len("A - "))
	if got := FolderName("A", exact); got != "A - "+exact {
		t.Errorf("a name of exactly the limit was changed: %q", got)
	}
}

func TestMovesSong(t *testing.T) {
	for a, want := range map[Action]bool{
		ActMove: true, ActDuplicate: true, ActInvalid: true, ActInPlace: false, ActLeft: false,
	} {
		if got := movesSong(a); got != want {
			t.Errorf("movesSong(%s) = %v, want %v", a, got, want)
		}
	}
}

func TestIsBucketDir(t *testing.T) {
	lib := filepath.Join(string(filepath.Separator), "lib")
	for name, want := range map[string]bool{
		"A": true, "M": true, "Z": true, BucketDigits: true, BucketOther: true,
		"a": false, "@": false, "[": false, "AB": false, "Ä": false, "": false,
	} {
		if got := isBucketDir(lib, filepath.Join(lib, name)); got != want {
			t.Errorf("isBucketDir(%q) = %v, want %v", name, got, want)
		}
	}
	if isBucketDir(lib, filepath.Join(lib, "x", "A")) {
		t.Error("a folder A below a category is not a bucket")
	}
}

// The library's emptied Z bucket is a bucket, not an emptied category.
func TestRunEmptiedZBucketNotCounted(t *testing.T) {
	noFFprobe(t)
	base := t.TempDir()
	lib, out := filepath.Join(base, "lib"), filepath.Join(base, "out")
	testutil.WriteFiles(t, lib, songFiles("Z/Abba", "Abba", "SOS")) // -> A/Abba - SOS, Z emptied
	cfg := organizeCfg(t, [][]string{{lib}}, lib, out, false)
	_, rep := runOrganize(t, cfg, Options{})
	if rep.Summary.ToLibrary != 1 || rep.Summary.EmptyCategoriesLeft != 0 {
		t.Errorf("summary = %+v, want 1 move and no emptied category", rep.Summary)
	}
}

// Songs without a key (an artist of only punctuation) are never merged; their colliding
// folder names are numbered in source order, whatever the order of the candidates.
func TestMakePlanKeylessNumberedBySource(t *testing.T) {
	cands := []Candidate{
		song(low1, "x", "!!!", "!!!", Quality{}),
		song(hq, "y", "!!!", "!!!", Quality{}),
	}
	if k := SongKey("!!!", "!!!", false); k != "" {
		t.Fatalf("SongKey = %q, want none", k)
	}
	p := MakePlan(cands, PlanOptions{})
	want := []string{"low1:x: library #/!!! - !!! (2)", "hq:y: library #/!!! - !!!"}
	if got := summary(cands, p); !reflect.DeepEqual(got, want) {
		t.Errorf("plan:\n%s\nwant:\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
}

// Temporary names are numbered 0, 1, ... in the order the parks happen.
func TestOrderMovesTempNumbering(t *testing.T) {
	p := filepath.FromSlash
	var ns []int
	steps := orderMoves([]libMove{
		{0, p("/lib/A/a"), p("/lib/A/b")}, {1, p("/lib/A/b"), p("/lib/A/a")}, // swap
		{2, p("/lib/C/c"), p("/lib/C/d")}, {3, p("/lib/C/d"), p("/lib/C/c")}, // another swap
	}, func(n int) string {
		ns = append(ns, n)
		return p("/lib/.tmp-") + string(rune('0'+n))
	})
	if !reflect.DeepEqual(ns, []int{0, 1}) {
		t.Errorf("temporary names %v, want [0 1]", ns)
	}
	if len(steps) != 6 {
		t.Errorf("steps = %+v", steps)
	}
}

// The report numbers tiers from 1, and sorts the songs already in place by target and
// the songs left in place by song.
func TestBuildReportTiersAndSorting(t *testing.T) {
	cands := []Candidate{
		song(lib, "C/Cher - Believe", "Cher", "Believe", Quality{}),
		song(lib, "A/Abba - SOS", "Abba", "SOS", Quality{}),
		song(hq, "z", "Zed", "Song", Quality{}),
		song(hq, "b", "Bee Gees", "Tragedy", Quality{}),
	}
	plan := Plan{Decisions: []Decision{
		{Action: ActInPlace, Target: "C/Cher - Believe", Other: -1},
		{Action: ActInPlace, Target: "A/Abba - SOS", Other: -1},
		{Action: ActLeft, Reason: reasonUnreadable, Other: -1},
		{Action: ActLeft, Reason: reasonEncoding, Other: -1},
	}}
	in := reportInput{cands: cands, plan: plan, outcomes: map[int]moveOutcome{}}
	in.cfg.Library = "/lib"
	in.cfg.Sources = []config.Source{{Path: "/src/hq", Label: "hq", Tier: 0, Index: 0}, {Path: "/src/low", Label: "low", Tier: 1, Index: 1}}
	r := buildReport(in)
	if len(r.Sources) != 2 || r.Sources[0].Tier != 1 || r.Sources[1].Tier != 2 {
		t.Errorf("sources = %+v, want tiers 1 and 2", r.Sources)
	}
	if !strings.Contains(string(r.text()), "  tier 1  hq  (/src/hq)\n  tier 2  low  (/src/low)\n") {
		t.Errorf("text:\n%s", r.text())
	}
	var inPlace, left []string
	for _, e := range r.AlreadyInPlace {
		inPlace = append(inPlace, e.Target)
	}
	for _, e := range r.LeftInPlace {
		left = append(left, e.Song)
	}
	if want := []string{"A/Abba - SOS", "C/Cher - Believe"}; !reflect.DeepEqual(inPlace, want) {
		t.Errorf("already in place = %q, want %q", inPlace, want)
	}
	if want := []string{"hq:b", "hq:z"}; !reflect.DeepEqual(left, want) {
		t.Errorf("left in place = %q, want %q", left, want)
	}
}
