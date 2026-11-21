package webui

import (
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/nkreimeyer/library-manager/organize"
)

func TestBuildOrganizeView(t *testing.T) {
	cands := []organize.Candidate{
		{Label: "new", Rel: "a", Tier: 1, Artist: "A", Title: "One", Quality: organize.Quality{VideoBitrate: 2_500_000, AudioBitrate: 320_000, VideoHeight: 720, HasVideo: true}},
		{Label: "new", Rel: "b", Tier: 0, Artist: "A", Title: "One"},
		{Label: "new", Rel: "c", Broken: true, Errors: []string{"no-audio"}},
		{Label: "new", Rel: "d", EncodingProblem: true},
		{Label: "new", Rel: "e"},
		{Label: "lib", Rel: "f", InLibrary: true},
	}
	p := organize.Plan{Decisions: []organize.Decision{
		{Action: organize.ActMove, Target: "A/A - One", Other: -1},
		{Action: organize.ActDuplicate, Reason: "video", Other: 0},
		{Action: organize.ActInvalid, Reason: "no-audio", Other: -1},
		{Action: organize.ActLeft, Reason: "encoding problem", Other: -1},
		{Action: organize.ActLeft, Reason: organize.ReasonExcluded, Other: -1},
		{Action: organize.ActInPlace, Target: "f", Other: -1},
	}}
	a := &organizeAnalysis{id: "o1", review: &organize.Review{Cands: cands, Plan: p}, plan: p, exclude: map[string]bool{"new:e": true}}
	v := buildOrganizeView(a)
	want := organizeSummary{Songs: 6, ToLibrary: 1, InPlace: 1, Duplicates: 1, Invalid: 1, Left: 1, Excluded: 1}
	if v.Summary != want {
		t.Errorf("summary = %+v, want %+v", v.Summary, want)
	}
	if q := v.Songs[0].Quality; v.Songs[0].Tier != 2 || q.VideoKbps != 2500 || q.AudioKbps != 320 || q.VideoHeight != 720 || !q.Video {
		t.Errorf("song 0 = %+v", v.Songs[0])
	}
	if s := v.Songs[1]; s.Tier != 1 || s.Other != 0 || s.Reason != "video" || s.Action != "duplicate" {
		t.Errorf("song 1 = %+v", s)
	}
	if !v.Songs[4].Excluded || v.Songs[3].Excluded {
		t.Error("excluded flags")
	}
	// Empty lists are [] for the browser, never null.
	data, _ := json.Marshal(v)
	for _, key := range []string{`"possibleDuplicates":[]`, `"similar":[]`, `"notes":[]`, `"excluded":["new:e"]`} {
		if !strings.Contains(string(data), key) {
			t.Errorf("%s missing in %s", key, data)
		}
	}
}

func TestOrganizeValidateTiers(t *testing.T) {
	s := newTestServer(t)
	base := t.TempDir()
	a, b, lib := filepath.Join(base, "a"), filepath.Join(base, "b"), filepath.Join(base, "lib")
	writeSong(t, filepath.Join(a, "X - Y"), "X", "Y", nil)
	writeSong(t, filepath.Join(b, "X - Z"), "X", "Z", nil)
	// Empty fields and empty tiers of the form are dropped.
	var cv configView
	q := organizeRequest{Mode: "organize", Library: lib, Tiers: [][]string{{"", a}, {""}, {b, ""}}}
	if got := s.do("POST", "/api/organize/validate", q, &cv); got != http.StatusOK {
		t.Fatalf("validate: %d", got)
	}
	if len(cv.Sources) != 2 || cv.Sources[0].Path != a || cv.Sources[0].Tier != 1 || cv.Sources[1].Path != b || cv.Sources[1].Tier != 2 {
		t.Errorf("sources = %+v", cv.Sources)
	}
	var e apiError
	if got := s.do("POST", "/api/organize/validate", organizeRequest{Mode: "organize", Tiers: [][]string{{a}}}, &e); got != http.StatusBadRequest || !strings.Contains(e.Error, "library") {
		t.Errorf("no library: %d %q", got, e.Error)
	}
	if got := s.do("POST", "/api/organize/validate", organizeRequest{Mode: "organize", Library: lib, Tiers: [][]string{{""}}}, &e); got != http.StatusBadRequest || !strings.Contains(e.Error, "source") {
		t.Errorf("no source: %d %q", got, e.Error)
	}
}

func TestOrganizeUndoJournalUnreadable(t *testing.T) {
	s := newTestServer(t)
	lib, out := dedupeLibrary(t)
	if err := os.MkdirAll(filepath.Join(out, "organize-journal.jsonl"), 0o755); err != nil {
		t.Fatal(err)
	}
	var j Job
	if got := s.do("POST", "/api/organize/undo", folderRequest{Library: lib, Out: out}, &j); got != http.StatusAccepted {
		t.Fatalf("undo: %d", got)
	}
	if j = s.wait(); j.State != stateFailed || !strings.Contains(j.Error, "journal") {
		t.Errorf("undo job = %+v", j)
	}
}
