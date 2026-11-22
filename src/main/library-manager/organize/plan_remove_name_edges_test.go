package organize

import (
	"encoding/json"
	"fmt"
	"github.com/nkreimeyer/library-manager/fsops"
	"github.com/nkreimeyer/library-manager/internal/testutil"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestBitrateCloseUnknownNeverClose(t *testing.T) {
	if bitrateClose(0, 0) {
		t.Error("an unknown bitrate is close to an unknown best")
	}
	if !bitrateClose(90, 100) || bitrateClose(89, 100) {
		t.Error("the tolerance is exactly 10 % of the best")
	}
}

func TestIsFamilyNameEmptyBase(t *testing.T) {
	if !isFamilyName(" (2)", "") {
		t.Error(`" (2)" is not a numbered "" `)
	}
	if isFamilyName(" (1)", "") || isFamilyName(" (02)", "") {
		t.Error("only plain numbers from 2 count")
	}
}

// A decision that moves nothing to another song has no other song.
func TestMakePlanOtherDefault(t *testing.T) {
	cands := []Candidate{
		song(hq, "a", "Abba", "SOS", Quality{}),
		song(hq, "b", "Bee Gees", "Tragedy", Quality{}),
		song(lib, "A/Cher - Believe", "Cher", "Believe", Quality{}),
	}
	for i, d := range MakePlan(cands, PlanOptions{}).Decisions {
		if d.Other != -1 {
			t.Errorf("decision %d = %+v, want no other song", i, d)
		}
	}
}

// The song at index 0 can be the better version a library copy lost to.
func TestKeepLibraryLosersKeeperIndexZero(t *testing.T) {
	cands := []Candidate{
		song(hq, "a", "Abba", "SOS", Quality{}),
		song(lib, "A/Abba - SOS", "Abba", "SOS", Quality{}),
	}
	ds := []Decision{{Action: ActLeft, Other: -1}, {Action: ActDuplicate, Other: 0, Reason: "x"}}
	if !keepLibraryLosers(cands, ds, false) {
		t.Fatal("no change")
	}
	if ds[1].Action != ActLeft || ds[1].Other != -1 || ds[1].Reason != fmt.Sprintf(reasonKeeperLeft, "hq:a") {
		t.Errorf("decision = %+v", ds[1])
	}
}

// Groups of three or more are listed once, in order of their loose key, solo before duet.
func TestPossibleDuplicatesOrderAndGroups(t *testing.T) {
	cands := []Candidate{
		song(hq, "0", "Zed", "Song (Live)", Quality{}),
		song(hq, "1", "Zed", "Song (Acoustic)", Quality{}),
		song(hq, "2", "Zed", "Song (Remix)", Quality{}),
		song(hq, "3", "Abe", "Tune (Live)", Quality{}),
		song(hq, "4", "Abe", "Tune (Remix)", Quality{}),
		song(hq, "5", "Abe", "Tune [DUET]", Quality{}),
		song(hq, "6", "Abe", "Tune (Live) [DUET]", Quality{}),
		song(hq, "7", "Lone", "Single", Quality{}),
	}
	groups := map[string]*group{}
	var keys []string
	for i := range cands {
		k := fmt.Sprint("k", i)
		groups[k] = &group{keeper: i}
		keys = append(keys, k)
	}
	got := possibleDuplicates(cands, groups, keys)
	if want := [][]int{{3, 4}, {5, 6}, {0, 1, 2}}; !reflect.DeepEqual(got, want) {
		t.Errorf("groups = %v, want %v", got, want)
	}
}

func TestCutBytesInvalidUTF8(t *testing.T) {
	if got := cutBytes("\x80\x80\x80\x80", 2); got != "" {
		t.Errorf("cutBytes = %q, want nothing", got)
	}
	if got := cutBytes("abä", 3); got != "ab" {
		t.Errorf("cutBytes = %q, want ab", got)
	}
}

// A pattern file may have long lines (a long comment); only lines over 1 MiB are refused.
func TestParsePatternFileLongLine(t *testing.T) {
	in := "# " + strings.Repeat("x", 200*1024) + "\nABBA - SOS\n"
	ps, err := ParsePatternFile(strings.NewReader(in), "list.txt")
	if err != nil || len(ps) != 1 || ps[0].Source != "list.txt:2" {
		t.Errorf("patterns %+v, error %v", ps, err)
	}
	if _, err := ParsePatternFile(strings.NewReader("# "+strings.Repeat("x", 2*1024*1024)+"\n"), "l"); err == nil {
		t.Error("a line of 2 MiB is accepted")
	}
}

// A matched song whose txt has an encoding problem stays, for every pattern (also the first).
func TestPlanRemoveEncodingProblemFirstPattern(t *testing.T) {
	songs := []removeSong{{Rel: "A/SOS", Artist: "ABBA", Title: "SOS", EncodingProblem: true}, {Rel: "A/W", Artist: "ABBA", Title: "W"}}
	ds, _, _ := planRemove(songs, []Pattern{mustPattern(t, PatternArtist, "ABBA")})
	if !ds[0].Left || ds[0].Reason != reasonRemoveEncoding || ds[1].Left {
		t.Errorf("decisions = %+v", ds)
	}
}

// Before the first move the report already lists every song to move as pending, and the
// progress knows how many moves there are (not the songs that stay).
func TestRemovePendingReportBeforeMoves(t *testing.T) {
	noFFprobe(t)
	cfg, lib, _ := restoreSetup(t)
	testutil.WriteFiles(t, lib, songFiles("C/Cher - Believe", "Cher", "Believe"))
	patterns := []Pattern{mustPattern(t, PatternArtist, "ABBA"), mustPattern(t, PatternArtist, "Queen")}
	var totals []int
	var pending removeReport
	opts := RemoveOptions{Keep: map[string]bool{"A/ABBA - SOS": true}, Progress: func(phase string, done, total int) {
		if phase != PhaseMove {
			return
		}
		totals = append(totals, total)
		if done == 0 {
			data, err := os.ReadFile(filepath.Join(cfg.OutDir, removeReportJSON))
			if err != nil {
				t.Error(err)
				return
			}
			if err := json.Unmarshal(data, &pending); err != nil {
				t.Error(err)
			}
		}
	}}
	if err := RemoveWith(cfg, fsops.New(false), patterns, opts); err != nil {
		t.Fatal(err)
	}
	if len(totals) == 0 || totals[0] != 2 {
		t.Errorf("progress totals = %v, want 2", totals)
	}
	var got []string
	for _, e := range pending.Removed {
		got = append(got, e.Song+" "+e.Outcome)
	}
	if want := []string{"A/ABBA - Waterloo pending", "Q/Queen - Rock pending"}; !reflect.DeepEqual(got, want) {
		t.Errorf("report before the moves: %q, want %q", got, want)
	}
}

func TestSplitBracketsNoEmptyParts(t *testing.T) {
	got := splitBrackets("(a)(b)")
	want := []titlePart{{text: "a", bracket: true, leading: true}, {text: "b", bracket: true}}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("parts = %+v, want %+v", got, want)
	}
	got = splitBrackets("x(a)")
	want = []titlePart{{text: "x"}, {text: "a", bracket: true}}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("parts = %+v, want %+v", got, want)
	}
}
