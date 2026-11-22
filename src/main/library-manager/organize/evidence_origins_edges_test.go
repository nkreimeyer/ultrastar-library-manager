package organize

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/nkreimeyer/library-manager/fsops"
	"github.com/nkreimeyer/library-manager/internal/testutil"
	"github.com/nkreimeyer/library-manager/songtxt"
)

func TestWeakEvidence(t *testing.T) {
	if got := (similarPair{}).weakEvidence(); !strings.Contains(got, "no lyrics in common") {
		t.Errorf("Jaccard 0: %q", got)
	}
	// Floored, never rounded up: 0.999 is 99 %, not 100 %.
	if got := (similarPair{Jaccard: 0.999}).weakEvidence(); !strings.Contains(got, "only 99 %") {
		t.Errorf("Jaccard 0.999: %q", got)
	}
	if got := (similarPair{Jaccard: 0.5}).weakEvidence(); !strings.Contains(got, "only 50 %") {
		t.Errorf("Jaccard 0.5: %q", got)
	}
}

// A possible-duplicates pair where a song has no profile is listed without evidence.
func TestReviewSimilarNilProfilePossible(t *testing.T) {
	cands := []Candidate{{Artist: "A", Title: "T"}, {Artist: "A", Title: "T [Live]"}}
	p := Plan{PossibleDuplicates: [][]int{{0, 1}}}
	got := reviewSimilar(cands, p, nil, func(int) bool { return true })
	want := []Similar{{A: 0, B: 1, Possible: true}}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got %+v, want %+v", got, want)
	}
}

func minorHash(t *testing.T, header, body string) string {
	t.Helper()
	s, _ := songtxt.Parse(header + body + "E\n")
	return ContentHash(s)
}

func TestContentHashIgnoresNoteOrderAndBPMChangeOrder(t *testing.T) {
	var lines []string
	for i := range 12 {
		lines = append(lines, ": "+itoa(i*4)+" 2 "+itoa(i)+" la\n")
	}
	var rev []string
	for i := len(lines) - 1; i >= 0; i-- {
		rev = append(rev, lines[i])
	}
	h := "#BPM:300\n#GAP:0\n"
	a := minorHash(t, h, strings.Join(lines, ""))
	b := minorHash(t, h, strings.Join(rev, ""))
	if a == "" || a != b {
		t.Errorf("note order changed the hash: %q vs %q", a, b)
	}
	// Two BPM changes at the same beat: their file order does not matter (tie on beat
	// is broken by BPM).
	body := strings.Join(lines, "")
	x := minorHash(t, h, "B 8 200\nB 8 100\n"+body)
	y := minorHash(t, h, "B 8 100\nB 8 200\n"+body)
	if x == "" || x != y {
		t.Errorf("BPM change order changed the hash: %q vs %q", x, y)
	}
	if x == a {
		t.Error("BPM changes were ignored")
	}
}

func itoa(i int) string { b, _ := json.Marshal(i); return string(b) }

func TestCutBytesNonPositive(t *testing.T) {
	for _, n := range []int{0, -1, -100} {
		if got := cutBytes("abc", n); got != "" {
			t.Errorf("cutBytes(abc, %d) = %q", n, got)
		}
	}
	if got := cutBytes("aé", 2); got != "a" {
		t.Errorf("rune split: %q", got)
	}
}

// A loser that shares the keeper's... no: shares a SongKey with a content-joined version
// but has no hash itself is joined only through the group.
func TestJoinedViaGroupDefaultReason(t *testing.T) {
	mk := func(rel, artist, hash string, marked bool) Candidate {
		return Candidate{Label: "s", Rel: rel, Folder: "/x/" + rel, Artist: artist, Title: "Song",
			ContentHash: hash, MarkedCopy: marked}
	}
	cands := []Candidate{
		mk("x1", "Alpha", "H", true),
		mk("x2", "Alpha", "", true), // same key as x1, no hash of its own
		mk("y", "Beta", "H", false),
	}
	p := MakePlan(cands, PlanOptions{})
	if p.Decisions[2].Action == ActDuplicate {
		t.Fatalf("y should keep: %+v", p.Decisions)
	}
	if r := p.Decisions[0].Reason; !strings.HasSuffix(r, joinedByContent) {
		t.Errorf("x1 reason = %q", r)
	}
	r := p.Decisions[1].Reason
	if p.Decisions[1].Action != ActDuplicate || !strings.HasSuffix(r, joinedViaGroup) {
		t.Errorf("x2 = %+v, want reason ending in joinedViaGroup", p.Decisions[1])
	}
}

func TestJournalOriginsSkipsUndoneAndReverted(t *testing.T) {
	cfg, _, out := restoreSetup(t)
	mv := func(run, src, dst string) string {
		b, _ := json.Marshal(journalEntry{Op: opMove, Run: run, Src: src, Dst: dst})
		return string(b) + "\n"
	}
	var sb strings.Builder
	sb.WriteString(`{"op":"run","run":"r1"}` + "\n")
	sb.WriteString(mv("r1", "/lib/a", filepath.Join(out, "x", "a")))
	sb.WriteString(`{"op":"undone","run":"r1"}` + "\n")
	sb.WriteString(`{"op":"run","run":"r2"}` + "\n")
	sb.WriteString(mv("r2", "/lib/b", filepath.Join(out, "x", "b"))) // entry 1: reverted
	sb.WriteString(mv("r2", "/lib/c", filepath.Join(out, "x", "c"))) // entry 2: kept
	sb.WriteString(`{"op":"reverted","run":"r2","entry":1}` + "\n")
	testutil.WriteFile(t, cfg.JournalPath(), sb.String())
	got, err := journalOrigins(cfg)
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]string{filepath.Join(out, "x", "c"): "/lib/c"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("journalOrigins = %v, want %v", got, want)
	}
}

func TestMissingDirs(t *testing.T) {
	root := t.TempDir()
	if got := missingDirs(root); got != nil {
		t.Errorf("existing dir: %v", got)
	}
	got := missingDirs(filepath.Join(root, "a", "b", "c"))
	want := []string{filepath.Join(root, "a"), filepath.Join(root, "a", "b"), filepath.Join(root, "a", "b", "c")}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got %v, want %v", got, want)
	}
	// Below a file the parent is ENOTDIR, not ENOENT: the walk stops there.
	f := filepath.Join(root, "file")
	testutil.WriteFile(t, f, "x")
	if got := missingDirs(filepath.Join(f, "sub")); got != nil {
		t.Errorf("below a file: %v", got)
	}
}

func TestListSetAsideExtraFilesWalk(t *testing.T) {
	testutil.SkipIfRoot(t)
	cfg, _, out := restoreSetup(t)
	ef := filepath.Join(out, "extra-files")
	testutil.WriteFiles(t, ef, map[string]string{
		"A/Song/note.txt":     "n",
		".hidden/skip.txt":    "h",
		"A/Song/.DS_Store":    "h",
		"Locked/secret.txt":   "s",
		"Locked/sub/deep.txt": "d",
	})
	locked := filepath.Join(ef, "Locked")
	if err := os.Chmod(locked, 0o000); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(locked, 0o755) })

	ops := fsops.New(true)
	items, err := ListSetAside(cfg, ops)
	if err != nil {
		t.Fatal(err)
	}
	var rels []string
	for _, it := range items {
		if it.Section == SectionExtraFiles {
			rels = append(rels, it.Rel)
		}
	}
	if want := []string{"A/Song/note.txt"}; !reflect.DeepEqual(rels, want) {
		t.Errorf("extra-files items = %v, want %v", rels, want)
	}
	if ops.Stats().Failed == 0 {
		t.Error("the unreadable folder was not counted")
	}
}

// A section that cannot be listed as song folders is an error naming the folder.
func TestListSetAsideSongFoldersError(t *testing.T) {
	testutil.SkipIfRoot(t)
	cfg, _, out := restoreSetup(t)
	inv := filepath.Join(out, SectionInvalid)
	testutil.WriteFile(t, filepath.Join(inv, "x", "song.txt"), "#ARTIST:a\n")
	if err := os.Chmod(inv, 0o000); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(inv, 0o755) })
	items, err := ListSetAside(cfg, fsops.New(true))
	if err == nil {
		// Unreadable folders may be skipped and counted instead; then nothing is listed.
		for _, it := range items {
			if it.Section == SectionInvalid {
				t.Errorf("listed %+v from an unreadable folder", it)
			}
		}
		return
	}
	if !strings.Contains(err.Error(), "listing") {
		t.Errorf("err = %v", err)
	}
}
