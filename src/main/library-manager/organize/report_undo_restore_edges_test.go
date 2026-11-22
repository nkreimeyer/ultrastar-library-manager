package organize

import (
	"fmt"
	"github.com/nkreimeyer/library-manager/config"
	"github.com/nkreimeyer/library-manager/fsops"
	"github.com/nkreimeyer/library-manager/internal/testutil"
	log "github.com/sirupsen/logrus"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

// buildReport sorts every list by its own key, numbers the outcomes and counts moves.
func TestBuildReportSortedAndCounted(t *testing.T) {
	var cands []Candidate
	add := func(rel string) int {
		cands = append(cands, song(hq, rel, "Artist", rel, Quality{}))
		return len(cands) - 1
	}
	m1, m2 := add("m1"), add("m2")
	d2, d1 := add("d2"), add("d1")
	i2, i1 := add("i2"), add("i1")
	l2, l1 := add("l2"), add("l1")
	p2, p1 := add("p2"), add("p1")
	k2, k1 := add("k2"), add("k1")
	dec := make([]Decision, len(cands))
	for i := range dec {
		dec[i].Other = -1
	}
	dec[m1] = Decision{Action: ActMove, Target: "B/two", Other: -1}
	dec[m2] = Decision{Action: ActMove, Target: "A/one", Other: -1}
	dec[d2] = Decision{Action: ActDuplicate, Other: m1, Reason: "r"}
	dec[d1] = Decision{Action: ActDuplicate, Other: m1, Reason: "r"}
	dec[i2] = Decision{Action: ActInvalid, Other: -1}
	dec[i1] = Decision{Action: ActInvalid, Other: m2}
	dec[l2] = Decision{Action: ActLeft, Other: -1, Reason: "x"}
	dec[l1] = Decision{Action: ActLeft, Other: -1, Reason: "x"}
	dec[p2] = Decision{Action: ActInPlace, Target: "Z/b", Other: -1}
	dec[p1] = Decision{Action: ActInPlace, Target: "Y/a", Other: -1}
	dec[k2], dec[k1] = Decision{Action: ActInPlace, Target: "Z/c", Other: -1}, Decision{Action: ActInPlace, Target: "Y/c", Other: -1}
	plan := Plan{Decisions: dec,
		PossibleDuplicates: [][]int{{m1, m2}, {d2, d1}, {d1, l1}},
		Similar: []Similar{
			{A: p1, B: p2, Evidence: "e1"},                 // refs p1 < p2
			{A: i1, B: l1, Evidence: "e3"},                 // same first song as e2, listed before it
			{A: i2, B: i1, Evidence: "e2"},                 // swapped: i1 < i2
			{A: m1, B: m2, Evidence: "e4", Possible: true}, // not counted
		},
	}
	out := "/out"
	outcomes := map[int]moveOutcome{
		m1: {Status: outcomePlanned}, m2: {Status: outcomeMoved},
		d2: {Status: outcomeFailed}, d1: {Status: outcomeMoved, Dst: out + "/duplicates/hq/d1 (2)"},
		i2: {Status: outcomeSkipped, Note: noteStopped}, i1: {Status: outcomePending},
	}
	in := reportInput{cands: cands, plan: plan, outcomes: outcomes, empty: []string{"hq:b", "hq:a"}, emptyLeft: 3}
	in.cfg.OutDir = out
	r := buildReport(in)

	targets := func(es []libraryEntry) (o []string) {
		for _, e := range es {
			o = append(o, e.Target)
		}
		return
	}
	if want := []string{"A/one", "B/two"}; !reflect.DeepEqual(targets(r.ToLibrary), want) {
		t.Errorf("to library %v", targets(r.ToLibrary))
	}
	if want := []string{"Y/a", "Y/c", "Z/b", "Z/c"}; !reflect.DeepEqual(targets(r.AlreadyInPlace), want) {
		t.Errorf("in place %v", targets(r.AlreadyInPlace))
	}
	var songs, dests, outs []string
	for _, e := range r.Duplicates {
		songs, dests, outs = append(songs, e.Song), append(dests, e.Dest), append(outs, e.Outcome)
	}
	if want := []string{"hq:d1", "hq:d2"}; !reflect.DeepEqual(songs, want) {
		t.Errorf("duplicates %v", songs)
	}
	if want := []string{"duplicates/hq/d1 (2)", "duplicates/hq/d2"}; !reflect.DeepEqual(dests, want) {
		t.Errorf("duplicate dests %v", dests)
	}
	if want := []string{"moved", "failed"}; !reflect.DeepEqual(outs, want) {
		t.Errorf("duplicate outcomes %v", outs)
	}
	songs, dests, outs = nil, nil, nil
	for _, e := range r.Invalid {
		songs, dests, outs = append(songs, e.Song+">"+e.Replacement), append(dests, e.Dest), append(outs, e.Outcome+"/"+e.OutcomeNote)
	}
	if want := []string{"hq:i1>hq:m2", "hq:i2>"}; !reflect.DeepEqual(songs, want) {
		t.Errorf("invalid %v", songs)
	}
	if want := []string{"invalid/hq/i1", "invalid/hq/i2"}; !reflect.DeepEqual(dests, want) {
		t.Errorf("invalid dests %v", dests)
	}
	if want := []string{"pending/", "skipped/" + noteStopped}; !reflect.DeepEqual(outs, want) {
		t.Errorf("invalid outcomes %v", outs)
	}
	songs = nil
	for _, e := range r.LeftInPlace {
		songs = append(songs, e.Song)
	}
	if want := []string{"hq:l1", "hq:l2"}; !reflect.DeepEqual(songs, want) {
		t.Errorf("left %v", songs)
	}
	if want := [][]string{{"hq:d1", "hq:d2"}, {"hq:d1", "hq:l1"}, {"hq:m1", "hq:m2"}}; !reflect.DeepEqual(r.PossibleDuplicates, want) {
		t.Errorf("possible duplicates %v", r.PossibleDuplicates)
	}
	var sim []string
	for _, e := range r.Similar {
		sim = append(sim, e.Songs[0]+"+"+e.Songs[1]+" "+e.Evidence)
	}
	if want := []string{"hq:i1+hq:i2 e2", "hq:i1+hq:l1 e3", "hq:m1+hq:m2 e4", "hq:p1+hq:p2 e1"}; !reflect.DeepEqual(sim, want) {
		t.Errorf("similar %v", sim)
	}
	if want := []string{"hq:a", "hq:b"}; !reflect.DeepEqual(r.EmptyFolders, want) {
		t.Errorf("empty %v", r.EmptyFolders)
	}
	want := summaryReport{Songs: 12, ToLibrary: 2, AlreadyInPlace: 4, Duplicates: 2, Invalid: 2, LeftInPlace: 2,
		PossibleDuplicates: 3, Similar: 3, EmptyFolders: 2, EmptyCategoriesLeft: 3, Moved: 3, Failed: 1}
	if r.Summary != want {
		t.Errorf("summary = %+v, want %+v", r.Summary, want)
	}
}

// The remove report: where a removed song went (also with a numbered target), what became of
// each move, and the counts.
func TestBuildRemoveReportOutcomes(t *testing.T) {
	out := "/out"
	songs := []removeSong{
		{Rel: "A/a", Artist: "A", Title: "a"}, {Rel: "B/b", Artist: "B", Title: "b"}, {Rel: "C/c", Artist: "C", Title: "c"},
		{Rel: "D/d", Artist: "D", Title: "d", FromFolder: true}, {Rel: "E/e", Artist: "E", Title: "e"},
		{Rel: "F/f", Artist: "F", Title: "f"}, {Rel: "G/g"},
	}
	ds := []removeDecision{{Pattern: 0}, {Pattern: 0}, {Pattern: 0}, {Pattern: 1}, {Pattern: 1}, {Pattern: 1, Left: true, Reason: "why"}, {Pattern: -1}}
	patterns := []Pattern{mustPattern(t, PatternArtist, "A"), mustPattern(t, PatternArtist, "D"), mustPattern(t, PatternArtist, "Nobody")}
	in := removeInput{
		cfg: config.OrganizeConfig{Library: "/lib", OutDir: out, DryRun: true}, patterns: patterns, songs: songs, ds: ds,
		counts: []int{3, 3, 0}, matched: []bool{true, true, false},
		outcomes: map[int]moveOutcome{
			0: {Status: outcomePlanned},
			1: {Status: outcomeMoved, Dst: out + "/removed/B/b (2)"},
			2: {Status: outcomeFailed},
			3: {Status: outcomeSkipped, Note: noteStopped},
			4: {Status: outcomePending},
		},
	}
	r := buildRemoveReport(in)
	var got []string
	for _, e := range r.Removed {
		got = append(got, fmt.Sprintf("%s|%s|%s|%s|%s|%s", e.Song, e.Name, e.Pattern, e.Dest, e.Outcome, e.OutcomeNote))
	}
	want := []string{
		"A/a|A - a|artist: A|removed/A/a|planned|",
		"B/b|B - b|artist: A|removed/B/b (2)|moved|",
		"C/c|C - c|artist: A|removed/C/c|failed|",
		"D/d|D - d|artist: D (tags from the folder name)|removed/D/d|skipped|" + noteStopped,
		"E/e|E - e|artist: D|removed/E/e|pending|",
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("removed:\n%s\nwant:\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
	if want := (removeSummaryReport{Songs: 7, Removed: 5, LeftInPlace: 1, Moved: 2, Failed: 1}); r.Summary != want {
		t.Errorf("summary = %+v, want %+v", r.Summary, want)
	}
	if !reflect.DeepEqual(r.Left, []leftEntry{{Song: "F/f", Name: "F - f", Reason: "why"}}) || !reflect.DeepEqual(r.Unmatched, []string{"artist: Nobody"}) {
		t.Errorf("left %+v unmatched %v", r.Left, r.Unmatched)
	}
}

func undoMessages(t *testing.T, cfg config.OrganizeConfig) []string {
	t.Helper()
	hook := testutil.CaptureLogsAt(t, log.InfoLevel)
	runUndo(t, cfg)
	var msgs []string
	for _, e := range hook.AllEntries() {
		msgs = append(msgs, e.Message)
	}
	return msgs
}

func hasMessage(msgs []string, want string) bool {
	for _, m := range msgs {
		if m == want {
			return true
		}
	}
	return false
}

// An undo counts the moves an earlier undo already did, whether it recorded them or not.
func TestUndoCountsMovesBackAlready(t *testing.T) {
	noFFprobe(t)
	// recorded: the retry of a partly failed undo
	base := t.TempDir()
	src, lib, out := filepath.Join(base, "src"), filepath.Join(base, "lib"), filepath.Join(base, "out")
	testutil.WriteFiles(t, src, merge(songFiles("b", "Bee Gees", "Tragedy"), songFiles("c", "Cher", "Believe")))
	runOrganize(t, organizeCfg(t, [][]string{{src}}, lib, out, false), Options{})
	undoCfg := organizeCfg(t, nil, lib, out, false)
	blocker := filepath.Join(src, "b")
	testutil.WriteFiles(t, blocker, map[string]string{"new.txt": "x"})
	runUndo(t, undoCfg)
	msgs := undoMessages(t, undoCfg)
	if !hasMessage(msgs, "Undo: 0 move(s) moved back, 1 failed, 1 moved back by an earlier undo") {
		t.Errorf("recorded: %q", msgs)
	}

	// lost record: the song was put back by hand
	base = t.TempDir()
	src, lib, out = filepath.Join(base, "src"), filepath.Join(base, "lib"), filepath.Join(base, "out")
	testutil.WriteFiles(t, src, songFiles("b", "Bee Gees", "Tragedy"))
	runOrganize(t, organizeCfg(t, [][]string{{src}}, lib, out, false), Options{})
	if err := os.Rename(filepath.Join(lib, "B", "Bee Gees - Tragedy"), filepath.Join(src, "b")); err != nil {
		t.Fatal(err)
	}
	msgs = undoMessages(t, organizeCfg(t, nil, lib, out, false))
	if !hasMessage(msgs, "Undo: 0 move(s) moved back, 0 failed, 1 moved back by an earlier undo") {
		t.Errorf("lost record: %q", msgs)
	}
}

func TestValidSection(t *testing.T) {
	for _, s := range Sections {
		if !validSection(s) {
			t.Errorf("%q is not valid", s)
		}
	}
	if validSection("bogus") || validSection("") {
		t.Error("an unknown section is valid")
	}
}

// The listing is sorted by path within a section, skips hidden folders of extra-files and
// the files in them.
func TestListSetAsideOrderAndHidden(t *testing.T) {
	cfg, _, out := restoreSetup(t)
	testutil.WriteFile(t, filepath.Join(out, "extra-files", "B", "two.jpg"), "2")
	testutil.WriteFile(t, filepath.Join(out, "extra-files", "A", "one.jpg"), "1")
	testutil.WriteFile(t, filepath.Join(out, "extra-files", "A", "a.jpg"), "1")
	testutil.WriteFile(t, filepath.Join(out, "extra-files", ".hidden", "three.jpg"), "3")
	testutil.WriteFile(t, filepath.Join(out, "extra-files", "A", ".dot.jpg"), "3")
	items, err := ListSetAside(cfg, fsops.New(true))
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, it := range items {
		got = append(got, it.Rel)
	}
	if want := []string{"A/a.jpg", "A/one.jpg", "B/two.jpg"}; !reflect.DeepEqual(got, want) {
		t.Errorf("items = %q, want %q", got, want)
	}
}

// Restore counts what it put back, and tidies the emptied folders of the output folder
// all the way up to the section, but not a folder that still holds something.
func TestRestoreCountAndTidy(t *testing.T) {
	cfg, lib, out := restoreSetup(t)
	testutil.WriteFile(t, filepath.Join(out, "extra-files", "A", "ABBA - SOS", "one.jpg"), "1")
	testutil.WriteFile(t, filepath.Join(out, "extra-files", "Q", "Queen - Rock", "deep", "two.jpg"), "2")
	testutil.WriteFile(t, filepath.Join(out, "extra-files", "Q", "Queen - Rock", "keep.jpg"), "3")
	hook := testutil.CaptureLogsAt(t, log.InfoLevel)
	refs := []RestoreRef{{SectionExtraFiles, "A/ABBA - SOS/one.jpg"}, {SectionExtraFiles, "Q/Queen - Rock/deep/two.jpg"}, {SectionExtraFiles, "Q/Queen - Rock/nothing.jpg"}}
	if err := Restore(cfg, fsops.New(false), refs, RestoreOptions{}); err != nil {
		t.Fatal(err)
	}
	var msgs []string
	for _, e := range hook.AllEntries() {
		msgs = append(msgs, e.Message)
	}
	if !hasMessage(msgs, "Restore: 2 of 3 item(s) put back") {
		t.Errorf("messages = %q", msgs)
	}
	if _, err := os.Stat(filepath.Join(lib, "A", "ABBA - SOS", "one.jpg")); err != nil {
		t.Error(err)
	}
	for _, gone := range []string{"A/ABBA - SOS", "A", "Q/Queen - Rock/deep"} {
		if _, err := os.Stat(filepath.Join(out, "extra-files", filepath.FromSlash(gone))); err == nil {
			t.Errorf("extra-files/%s is still there", gone)
		}
	}
	if _, err := os.Stat(filepath.Join(out, "extra-files", "Q", "Queen - Rock", "keep.jpg")); err != nil {
		t.Errorf("a folder that is not empty was removed: %v", err)
	}
}
