package webui

import (
	"context"
	"encoding/json"
	"maps"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/nkreimeyer/library-manager/fsops"
	"github.com/nkreimeyer/library-manager/internal/testutil"
	"github.com/nkreimeyer/library-manager/organize"
)

// dedupeLibrary is a library with one song in two versions (the one with a cover wins)
// and one other song.
func dedupeLibrary(t *testing.T) (lib, out string) {
	t.Helper()
	base := t.TempDir()
	lib, out = filepath.Join(base, "library"), filepath.Join(base, "out")
	writeSong(t, filepath.Join(lib, "A", "ABBA - SOS"), "ABBA", "SOS", nil)
	writeSong(t, filepath.Join(lib, "Pop", "ABBA - SOS"), "ABBA", "SOS", nil)
	writeSong(t, filepath.Join(lib, "Q", "Queen - Rock"), "Queen", "Rock", nil)
	return lib, out
}

func analyseOrganize(t *testing.T, s *testServer, q organizeRequest) organizeView {
	t.Helper()
	var j Job
	if got := s.do("POST", "/api/organize/analyse", q, &j); got != http.StatusAccepted {
		t.Fatalf("analyse: %d", got)
	}
	j = s.wait()
	if j.State != stateDone || !j.DryRun {
		t.Fatalf("analyse job: %+v", j)
	}
	res := j.Result.(map[string]any)
	var v organizeView
	if got := s.do("GET", "/api/organize/analysis?id="+res["analysis"].(string), nil, &v); got != http.StatusOK {
		t.Fatalf("analysis: %d", got)
	}
	return v
}

func findSong(v organizeView, ref string) (int, songView) {
	for i, sv := range v.Songs {
		if sv.Ref == ref {
			return i, sv
		}
	}
	return -1, songView{}
}

func TestOrganizeValidate(t *testing.T) {
	s := newTestServer(t)
	lib, _ := dedupeLibrary(t)
	var cv configView
	if got := s.do("POST", "/api/organize/validate", organizeRequest{Mode: "dedupe", Library: lib}, &cv); got != http.StatusOK {
		t.Fatalf("validate: %d", got)
	}
	if cv.OutDir != filepath.Join(filepath.Dir(lib), "organize-output") || len(cv.Sources) != 1 || cv.Sources[0].Path != lib {
		t.Errorf("config = %+v", cv)
	}
	var e apiError
	for _, q := range []organizeRequest{
		{Mode: "dedupe", Library: filepath.Join(lib, "missing")},
		{Mode: "organize", Library: lib},
		{Mode: "add", Library: lib, Tiers: [][]string{{lib}}},
		{Mode: "nonsense", Library: lib},
	} {
		if got := s.do("POST", "/api/organize/validate", q, &e); got != http.StatusBadRequest || e.Error == "" {
			t.Errorf("%+v: %d %q", q, got, e.Error)
		}
	}
	if got := s.do("POST", "/api/organize/validate", map[string]any{"mode": "dedupe", "bogus": 1}, &e); got != http.StatusBadRequest {
		t.Errorf("unknown field: %d", got)
	}
}

func TestOrganizeFlow(t *testing.T) {
	s := newTestServer(t)
	lib, out := dedupeLibrary(t)
	before := testutil.Tree(t, lib)
	q := organizeRequest{Mode: "dedupe", Library: lib, Out: out}

	v := analyseOrganize(t, s, q)
	if !maps.Equal(testutil.Tree(t, lib), before) {
		t.Fatal("the analysis changed the library")
	}
	if v.Summary.Songs != 3 || v.Summary.InPlace != 2 || v.Summary.Duplicates != 1 || len(v.Notes) == 0 || v.Applied {
		t.Errorf("summary = %+v, notes %v", v.Summary, v.Notes)
	}
	ki, keeper := findSong(v, "library:A/ABBA - SOS")
	_, dup := findSong(v, "library:Pop/ABBA - SOS")
	if keeper.Action != "in-place" || dup.Action != "duplicate" || dup.Other != ki || dup.Reason == "" || dup.Artist != "ABBA" {
		t.Errorf("keeper %+v\nduplicate %+v", keeper, dup)
	}

	// Leave the duplicate alone: the plan changes at once, without a new check.
	var v2 organizeView
	s.do("POST", "/api/organize/exclude", map[string]any{"analysis": v.ID, "exclude": []string{dup.Ref}}, &v2)
	if v2.Summary.Duplicates != 0 || v2.Summary.Excluded != 1 || len(v2.Excluded) != 1 {
		t.Errorf("after exclude: %+v %v", v2.Summary, v2.Excluded)
	}
	if _, d := findSong(v2, dup.Ref); d.Action != "left" || !d.Excluded {
		t.Errorf("excluded song: %+v", d)
	}
	// And back: nothing excluded.
	s.do("POST", "/api/organize/exclude", map[string]any{"analysis": v.ID, "exclude": []string{}}, &v2)
	if v2.Summary.Duplicates != 1 {
		t.Errorf("after clearing: %+v", v2.Summary)
	}

	var j Job
	if got := s.do("POST", "/api/organize/apply", map[string]any{"analysis": v.ID}, &j); got != http.StatusAccepted {
		t.Fatalf("apply: %d", got)
	}
	j = s.wait()
	if j.State != stateDone || j.DryRun || j.Stats.Moved != 1 || j.Stats.Failed != 0 {
		t.Fatalf("apply job: %+v", j)
	}
	// The browser reads the JSON names.
	var raw map[string]any
	s.do("GET", "/api/job", nil, &raw)
	if st, _ := raw["stats"].(map[string]any); st["moved"] != 1.0 || st["failed"] != 0.0 {
		t.Errorf("stats JSON = %v", raw["stats"])
	}
	if _, err := os.Stat(filepath.Join(out, "duplicates", "library", "Pop", "ABBA - SOS", "song.txt")); err != nil {
		t.Errorf("duplicate not moved: %v", err)
	}
	res := j.Result.(map[string]any)
	if rep, ok := res["report"].(map[string]any); !ok || rep["dryRun"] != false {
		t.Errorf("result report = %v", res["report"])
	}
	// Applying twice is refused.
	var e apiError
	if got := s.do("POST", "/api/organize/apply", map[string]any{"analysis": v.ID}, &e); got != http.StatusConflict {
		t.Errorf("second apply: %d %v", got, e)
	}

	var hist struct {
		OutDir string `json:"outDir"`
		Runs   []struct {
			Moves  int  `json:"moves"`
			Undone bool `json:"undone"`
			Next   bool `json:"next"`
		} `json:"runs"`
	}
	s.do("POST", "/api/organize/history", folderRequest{Library: lib, Out: out}, &hist)
	if len(hist.Runs) != 1 || hist.Runs[0].Moves != 1 || !hist.Runs[0].Next || hist.OutDir != out {
		t.Errorf("history = %+v", hist)
	}
	if got := s.do("POST", "/api/organize/undo", folderRequest{Library: lib, Out: out}, &j); got != http.StatusAccepted {
		t.Fatalf("undo: %d", got)
	}
	if j = s.wait(); j.State != stateDone || j.Stats.Moved != 1 || j.Phase != organize.PhaseUndo || j.Total == 0 || j.Done != j.Total {
		t.Errorf("undo job: %+v", j)
	}
	// The result names the folders, so the UI can offer to try a partial undo again.
	if res, _ := j.Result.(map[string]any); res["library"] != lib || res["outDir"] != out {
		t.Errorf("undo result = %#v, want library %s and outDir %s", j.Result, lib, out)
	}
	if !maps.Equal(testutil.Tree(t, lib), before) {
		t.Error("undo did not restore the library")
	}
	s.do("POST", "/api/organize/history", folderRequest{Library: lib, Out: out}, &hist)
	if len(hist.Runs) != 1 || !hist.Runs[0].Undone || hist.Runs[0].Next {
		t.Errorf("history after undo = %+v", hist)
	}
	// A second run is listed first.
	v = analyseOrganize(t, s, q)
	s.do("POST", "/api/organize/apply", map[string]any{"analysis": v.ID}, &j)
	if j = s.wait(); j.State != stateDone {
		t.Fatalf("second apply: %+v", j)
	}
	s.do("POST", "/api/organize/history", folderRequest{Library: lib, Out: out}, &hist)
	if len(hist.Runs) != 2 || hist.Runs[0].Undone || !hist.Runs[0].Next || !hist.Runs[1].Undone {
		t.Errorf("history after a second run = %+v", hist)
	}
}

// planChanged analyses a library, adds two songs that change the plan and applies it.
func planChanged(t *testing.T, s *testServer) (v organizeView, j Job, lib string, before map[string]string) {
	t.Helper()
	lib, out := dedupeLibrary(t)
	v = analyseOrganize(t, s, organizeRequest{Mode: "dedupe", Library: lib, Out: out})
	// A better version appears after the review: it would become the keeper.
	writeSong(t, filepath.Join(lib, "New", "ABBA - SOS"), "ABBA", "SOS", map[string]string{"cover.png": "x"})
	writeSong(t, filepath.Join(lib, "R", "Roxette - Joy"), "Roxette", "Joy", nil)
	before = testutil.Tree(t, lib)
	s.do("POST", "/api/organize/apply", map[string]any{"analysis": v.ID}, &j)
	return v, s.wait(), lib, before
}

func setMaxDiffLines(t *testing.T, n int) {
	old := maxDiffLines
	maxDiffLines = n
	t.Cleanup(func() { maxDiffLines = old })
}

func TestOrganizeApplyPlanChangedTruncated(t *testing.T) {
	setMaxDiffLines(t, 1)
	_, j, _, _ := planChanged(t, newTestServer(t))
	if len(j.Details) != 2 || j.Details[1] != "… and 1 more" || !strings.Contains(j.Error, "2 song folder(s)") {
		t.Errorf("details = %q, error %q", j.Details, j.Error)
	}
}

func TestOrganizeApplyPlanChanged(t *testing.T) {
	setMaxDiffLines(t, 2) // exactly at the limit: nothing cut
	s := newTestServer(t)
	v, j, lib, before := planChanged(t, s)
	if j.State != stateFailed || !strings.Contains(j.Error, "nothing was moved") || j.Stats.Moved != 0 {
		t.Fatalf("job = %+v", j)
	}
	if strings.Join(j.Details, "\n") != strings.Join([]string{
		"library:New/ABBA - SOS: new (duplicate (instead: library:A/ABBA - SOS))",
		"library:R/Roxette - Joy: new (in-place R/Roxette - Joy)",
	}, "\n") {
		t.Errorf("details:\n%s", strings.Join(j.Details, "\n"))
	}
	if res, _ := j.Result.(map[string]any); res["planChanged"] != true || res["analysis"] != v.ID {
		t.Errorf("result = %v", j.Result)
	}
	if !maps.Equal(testutil.Tree(t, lib), before) {
		t.Error("a changed plan moved something")
	}
	// The analysis now holds the new plan; applying it works.
	var v2 organizeView
	s.do("GET", "/api/organize/analysis?id="+v.ID, nil, &v2)
	if v2.Summary.Songs != 5 || v2.Summary.Duplicates != 2 {
		t.Errorf("new plan: %+v", v2.Summary)
	}
	s.do("POST", "/api/organize/apply", map[string]any{"analysis": v.ID}, &j)
	if j = s.wait(); j.State != stateDone || j.Stats.Moved != 2 {
		t.Errorf("second apply: %+v", j)
	}
}

func TestOrganizeBusyAndUnknown(t *testing.T) {
	s := newTestServer(t)
	var e apiError
	if got := s.do("GET", "/api/organize/analysis?id=nope", nil, &e); got != http.StatusNotFound {
		t.Errorf("unknown analysis: %d", got)
	}
	if got := s.do("POST", "/api/organize/apply", map[string]any{"analysis": "nope"}, &e); got != http.StatusNotFound {
		t.Errorf("apply unknown: %d", got)
	}
	block := make(chan struct{})
	defer close(block)
	s.srv.jobs.start("b", "test", fsops.New(true), func(context.Context, jobControl) (any, error) { <-block; return nil, nil })
	lib, out := dedupeLibrary(t)
	if got := s.do("POST", "/api/organize/analyse", organizeRequest{Mode: "dedupe", Library: lib, Out: out}, &e); got != http.StatusConflict || e.Error != errBusy.Error() {
		t.Errorf("busy: %d %q", got, e.Error)
	}
	// The other handlers that start a job answer the same way (Server.startJob).
	if got := s.do("POST", "/api/organize/undo", folderRequest{Library: lib, Out: out}, &e); got != http.StatusConflict || e.Error != errBusy.Error() {
		t.Errorf("undo busy: %d %q", got, e.Error)
	}
}

// A new analysis frees the older one of its kind (it holds every song of its library).
func TestOnlyNewestAnalysisKept(t *testing.T) {
	s := newTestServer(t)
	lib, out := dedupeLibrary(t)
	q := organizeRequest{Mode: "dedupe", Library: lib, Out: out}
	first := analyseOrganize(t, s, q)
	second := analyseOrganize(t, s, q)
	var e apiError
	if got := s.do("GET", "/api/organize/analysis?id="+first.ID, nil, &e); got != http.StatusNotFound {
		t.Errorf("older organize analysis: %d", got)
	}
	if got := s.do("GET", "/api/organize/analysis?id="+second.ID, nil, nil); got != http.StatusOK {
		t.Errorf("newest organize analysis: %d", got)
	}
}

// An undo stopped through the UI ends "cancelled" with its folders in the result (the page
// offers to undo again), the run stays the next one to undo, and a second undo finishes it.
func TestOrganizeUndoStopped(t *testing.T) {
	s := newTestServer(t)
	base := t.TempDir()
	lib, src, out := filepath.Join(base, "library"), filepath.Join(base, "src"), filepath.Join(base, "out")
	for _, name := range []string{"Abba", "Bee", "Cee", "Dee"} {
		writeSong(t, filepath.Join(src, name+" - Song"), name, "Song", nil)
	}
	before := testutil.Tree(t, src)
	v := analyseOrganize(t, s, organizeRequest{Mode: "organize", Tiers: [][]string{{src}}, Library: lib, Out: out})
	var j Job
	s.do("POST", "/api/organize/apply", map[string]any{"analysis": v.ID}, &j)
	if j = s.wait(); j.State != stateDone || j.Stats.Moved != 4 {
		t.Fatalf("apply: %+v", j)
	}

	undoProgressHook = func(done int) {
		if done == 1 {
			s.srv.jobs.cancelRunning()
		}
	}
	t.Cleanup(func() { undoProgressHook = nil })
	s.do("POST", "/api/organize/undo", folderRequest{Library: lib, Out: out}, &j)
	if j = s.wait(); j.State != stateCancelled || j.Stats.Moved != 1 {
		t.Fatalf("stopped undo: %+v", j)
	}
	if res, _ := j.Result.(map[string]any); res["library"] != lib || res["outDir"] != out {
		t.Errorf("stopped undo result = %#v, want the folders", j.Result)
	}
	var hist struct {
		Runs []organize.RunInfo `json:"runs"`
	}
	s.do("POST", "/api/organize/history", folderRequest{Library: lib, Out: out}, &hist)
	if len(hist.Runs) != 1 || hist.Runs[0].Undone || !hist.Runs[0].Next {
		t.Errorf("history after the stop = %+v", hist)
	}

	undoProgressHook = nil
	s.do("POST", "/api/organize/undo", folderRequest{Library: lib, Out: out}, &j)
	if j = s.wait(); j.State != stateDone || j.Stats.Moved != 3 {
		t.Errorf("second undo: %+v", j)
	}
	if !maps.Equal(testutil.Tree(t, src), before) {
		t.Error("the two undos did not restore the source")
	}
	s.do("POST", "/api/organize/history", folderRequest{Library: lib, Out: out}, &hist)
	if len(hist.Runs) != 1 || !hist.Runs[0].Undone {
		t.Errorf("history after the second undo = %+v", hist)
	}
}

// dedupe has no -priority and no -ignore-the (one source, nothing moves into a bucket):
// the UI keeps the checkboxes of an earlier organize or add form, and they must not reach
// the run, or its plan report says "priority: on" for a run the CLI cannot even start.
func TestDedupeIgnoresPriorityAndIgnoreThe(t *testing.T) {
	s := newTestServer(t)
	lib, out := dedupeLibrary(t)
	analyseOrganize(t, s, organizeRequest{Mode: "dedupe", Library: lib, Out: out, Priority: true, IgnoreThe: true})
	data, err := os.ReadFile(filepath.Join(out, "organize_plan.json"))
	if err != nil {
		t.Fatal(err)
	}
	var rep struct {
		Priority  bool `json:"priority"`
		IgnoreThe bool `json:"ignoreThe"`
	}
	if err := json.Unmarshal(data, &rep); err != nil {
		t.Fatal(err)
	}
	if rep.Priority || rep.IgnoreThe {
		t.Errorf("dedupe report: priority %v, ignoreThe %v, want both off", rep.Priority, rep.IgnoreThe)
	}
}
