package webui

import (
	"context"
	"encoding/json"
	"net/http"
	"path/filepath"
	"strings"
	"testing"

	"github.com/nkreimeyer/library-manager/config"
	"github.com/nkreimeyer/library-manager/fsops"
	"github.com/nkreimeyer/library-manager/organize"
)

// folderRoute is a POST route that reads a library (and output folder) from the body, with
// the other fields its handler accepts, so a refusal can only come from the folders.
type folderRoute struct {
	path  string
	extra map[string]any
	// needsLibrary: the library must exist (the dedupe configuration). The undo
	// configuration of history, undo, originals, reports and the set-aside browser also
	// takes a library that is gone: an undo may have removed the root a run created.
	needsLibrary bool
}

func folderRoutes() []folderRoute {
	return []folderRoute{
		{"/api/organize/validate", map[string]any{"mode": "dedupe"}, true},
		{"/api/organize/analyse", map[string]any{"mode": "dedupe"}, true},
		{"/api/overview", nil, true},
		{"/api/check/validate", nil, true},
		{"/api/check/analyse", nil, true},
		{"/api/remove/validate", map[string]any{"patterns": "ABBA"}, true},
		{"/api/remove/analyse", map[string]any{"patterns": "ABBA"}, true},
		{"/api/organize/history", nil, false},
		{"/api/organize/undo", nil, false},
		{"/api/originals/list", nil, false},
		{"/api/originals/delete", nil, false},
		{"/api/reports/list", nil, false},
		{"/api/reports/read", map[string]any{"name": "check_report.txt"}, false},
		{"/api/setaside/validate", nil, false},
		{"/api/setaside/list", nil, false},
		{"/api/setaside/restore", map[string]any{"items": []organize.RestoreRef{{Section: organize.SectionRemoved, Rel: "A"}}}, false},
	}
}

// Every POST route that takes folders refuses an empty library and an output folder inside
// the library (and, where the library must exist, a relative one that is not there) with
// 400 and an error message, and starts no job.
func TestPostRoutesRefuseBadFolders(t *testing.T) {
	s := newTestServer(t)
	lib, _ := removeLibrary(t)
	for _, rt := range folderRoutes() {
		bad := map[string]map[string]any{
			"no library":     {},
			"out in library": {"library": lib, "out": filepath.Join(lib, "out")},
		}
		if rt.needsLibrary {
			bad["relative library that is not there"] = map[string]any{"library": "relative/library"}
			bad["missing library"] = map[string]any{"library": filepath.Join(lib, "missing")}
		}
		for name, folders := range bad {
			body := map[string]any{}
			for k, v := range rt.extra {
				body[k] = v
			}
			for k, v := range folders {
				body[k] = v
			}
			var e apiError
			if got := s.do("POST", rt.path, body, &e); got != http.StatusBadRequest || e.Error == "" {
				t.Errorf("POST %s (%s): %d %q, want 400 with an error", rt.path, name, got, e.Error)
			}
		}
		// A body with a field the route does not know is a bad request, too.
		var e apiError
		if got := s.do("POST", rt.path, map[string]any{"library": lib, "bogus": 1}, &e); got != http.StatusBadRequest || e.Error == "" {
			t.Errorf("POST %s with an unknown field: %d %q", rt.path, got, e.Error)
		}
	}
	if _, _, _, ok := s.srv.jobs.current(0); ok {
		t.Error("a refused request started a job")
	}
}

// Routes that name an analysis answer 404 for an id that does not exist (or is empty).
func TestUnknownAnalysisIs404(t *testing.T) {
	s := newTestServer(t)
	for _, p := range []string{"/api/check/analysis", "/api/remove/analysis", "/api/organize/analysis"} {
		for _, q := range []string{"?id=nope", "?id=", ""} {
			var e apiError
			if got := s.do("GET", p+q, nil, &e); got != http.StatusNotFound || e.Error == "" {
				t.Errorf("GET %s%s: %d %q, want 404 with an error", p, q, got, e.Error)
			}
		}
	}
	for _, p := range []string{"/api/check/apply", "/api/remove/apply", "/api/organize/apply", "/api/organize/exclude", "/api/organize/join"} {
		var e apiError
		if got := s.do("POST", p, map[string]any{"analysis": "nope"}, &e); got != http.StatusNotFound || e.Error == "" {
			t.Errorf("POST %s: %d %q, want 404 with an error", p, got, e.Error)
		}
	}
	if _, _, _, ok := s.srv.jobs.current(0); ok {
		t.Error("a refused request started a job")
	}
}

// A newer analysis of a kind replaces the older one: the old id is gone for the view and
// the apply.
func TestOlderCheckAndRemoveAnalysesAreGone(t *testing.T) {
	s := newTestServer(t)
	lib, out := removeLibrary(t)
	analyse := func(path string, body any) string {
		t.Helper()
		var j Job
		if got := s.do("POST", path, body, &j); got != http.StatusAccepted {
			t.Fatalf("%s: %d", path, got)
		}
		if j = s.wait(); j.State != stateDone {
			t.Fatalf("%s: %+v", path, j)
		}
		return j.Result.(map[string]any)["analysis"].(string)
	}
	for _, c := range []struct {
		name, analyse, view, apply string
		body                       any
	}{
		{"check", "/api/check/analyse", "/api/check/analysis", "/api/check/apply", checkRequest{Library: lib, Out: out}},
		{"remove", "/api/remove/analyse", "/api/remove/analysis", "/api/remove/apply", removeRequest{Library: lib, Out: out, Patterns: "ABBA"}},
	} {
		first := analyse(c.analyse, c.body)
		second := analyse(c.analyse, c.body)
		var e apiError
		if got := s.do("GET", c.view+"?id="+first, nil, &e); got != http.StatusNotFound {
			t.Errorf("%s: the older analysis: %d", c.name, got)
		}
		if got := s.do("POST", c.apply, map[string]any{"analysis": first}, &e); got != http.StatusNotFound {
			t.Errorf("%s: apply of the older analysis: %d", c.name, got)
		}
		if got := s.do("GET", c.view+"?id="+second, nil, nil); got != http.StatusOK {
			t.Errorf("%s: the newest analysis: %d", c.name, got)
		}
	}
}

// jobRoute is a request that starts a job.
type jobRoute struct {
	name string
	path string
	body any
}

// jobRoutes returns one request for every route that starts a job, with analyses made
// first for the routes that apply one. Output folders carry their marker (the analyses made it).
func jobRoutes(t *testing.T, s *testServer) []jobRoute {
	t.Helper()
	lib, out := removeLibrary(t)
	var j Job
	run := func(path string, body any) string {
		t.Helper()
		if got := s.do("POST", path, body, &j); got != http.StatusAccepted {
			t.Fatalf("%s: %d", path, got)
		}
		if j = s.wait(); j.State != stateDone {
			t.Fatalf("%s: %+v", path, j)
		}
		return j.Result.(map[string]any)["analysis"].(string)
	}
	checkID := run("/api/check/analyse", checkRequest{Library: lib, Out: out})
	removeID := run("/api/remove/analyse", removeRequest{Library: lib, Out: out, Patterns: "ABBA"})
	orgID := run("/api/organize/analyse", organizeRequest{Mode: "dedupe", Library: lib, Out: out})
	folders := folderRequest{Library: lib, Out: out}
	return []jobRoute{
		{"overview", "/api/overview", folders},
		{"originals delete", "/api/originals/delete", folders},
		{"setaside restore", "/api/setaside/restore", map[string]any{"library": lib, "out": out,
			"items": []organize.RestoreRef{{Section: organize.SectionRemoved, Rel: "A"}}}},
		{"check analyse", "/api/check/analyse", checkRequest{Library: lib, Out: out}},
		{"check apply", "/api/check/apply", map[string]any{"analysis": checkID}},
		{"remove analyse", "/api/remove/analyse", removeRequest{Library: lib, Out: out, Patterns: "ABBA"}},
		{"remove apply", "/api/remove/apply", map[string]any{"analysis": removeID}},
		{"organize analyse", "/api/organize/analyse", organizeRequest{Mode: "dedupe", Library: lib, Out: out}},
		{"organize apply", "/api/organize/apply", map[string]any{"analysis": orgID}},
		{"organize undo", "/api/organize/undo", folders},
	}
}

// While a job runs, every route that would start another answers 409 with errBusy's
// message, and nothing was started or changed.
func TestJobRoutesAnswer409WhileBusy(t *testing.T) {
	s := newTestServer(t)
	routes := jobRoutes(t, s)
	block := make(chan struct{})
	defer close(block)
	if _, err := s.srv.jobs.start("blocker", "test", fsops.New(true), func(ctx context.Context, _ jobControl) (any, error) {
		select {
		case <-block:
		case <-ctx.Done():
		}
		return nil, nil
	}); err != nil {
		t.Fatal(err)
	}
	for _, rt := range routes {
		var e apiError
		if got := s.do("POST", rt.path, rt.body, &e); got != http.StatusConflict || e.Error != errBusy.Error() {
			t.Errorf("%s: %d %q, want 409 %q", rt.name, got, e.Error, errBusy)
		}
	}
	if j, _, _, _ := s.srv.jobs.current(0); j.ID != "blocker" {
		t.Errorf("the job changed: %+v", j)
	}
}

// Once the server is closed, every route that starts a job answers 503 with errClosed's
// message.
func TestJobRoutesAnswer503AfterClose(t *testing.T) {
	s := newTestServer(t)
	routes := jobRoutes(t, s)
	s.srv.Close()
	for _, rt := range routes {
		var e apiError
		if got := s.do("POST", rt.path, rt.body, &e); got != http.StatusServiceUnavailable || e.Error != errClosed.Error() {
			t.Errorf("%s: %d %q, want 503 %q", rt.name, got, e.Error, errClosed)
		}
	}
}

// An apply refused as busy or closed does not mark its analysis: it can be applied once
// the other job is over.
func TestRefusedApplyLeavesAnalysisOpen(t *testing.T) {
	s := newTestServer(t)
	routes := jobRoutes(t, s)
	block := make(chan struct{})
	s.srv.jobs.start("blocker", "test", fsops.New(true), func(context.Context, jobControl) (any, error) { <-block; return nil, nil })
	for _, rt := range routes {
		s.do("POST", rt.path, rt.body, nil)
	}
	close(block)
	s.wait()
	for _, rt := range routes {
		if !strings.HasSuffix(rt.path, "/apply") {
			continue
		}
		var j Job
		if got := s.do("POST", rt.path, rt.body, &j); got != http.StatusAccepted {
			t.Fatalf("%s after the busy refusal: %d", rt.name, got)
		}
		// The applies run one after the other on one library, so a later one may find a
		// changed plan; what counts is that the refused request did not use the analysis up.
		if j = s.wait(); j.Error != "" && !strings.Contains(j.Error, organize.ErrPlanChanged.Error()) {
			t.Errorf("%s: %+v", rt.name, j)
		}
	}
}

// With no journal the history is an empty list, not null (the page iterates it).
func TestHistoryWithoutJournalIsAnEmptyList(t *testing.T) {
	s := newTestServer(t)
	lib, out := removeLibrary(t)
	for _, o := range []string{out, ""} { // no output folder yet, and the default one
		var h struct {
			Runs json.RawMessage `json:"runs"`
		}
		if got := s.do("POST", "/api/organize/history", folderRequest{Library: lib, Out: o}, &h); got != http.StatusOK || string(h.Runs) != "[]" {
			t.Errorf("out %q: %d runs = %s, want []", o, got, h.Runs)
		}
	}
}

// Lists of the views are [] for the browser, never null: the folders of a config without
// sources and the steps and lists of a tidy-up that has not run a step.
func TestViewsHaveEmptyListsNotNull(t *testing.T) {
	data, _ := json.Marshal(viewConfig(config.OrganizeConfig{}))
	if !strings.Contains(string(data), `"sources":[]`) {
		t.Errorf("config view = %s", data)
	}
	a := &organizeAnalysis{id: "o1", req: organizeRequest{Mode: "dedupe", Tidy: &tidyOptions{UTF8: true}}}
	data, _ = json.Marshal(buildOrganizeView(a)) // no song step: no review
	for _, want := range []string{`"steps":[]`, `"textFiles":[]`, `"leftovers":[]`, `"brokenFolders":[]`, `"songs":[]`, `"notes":[]`} {
		if !strings.Contains(string(data), want) {
			t.Errorf("%s missing in %s", want, data)
		}
	}
	// With a plan from the analysis, the lists it holds are used as they are.
	a.tidy = &tidyPlan{TextFiles: []string{"A/x/song.txt"}, Leftovers: []string{}, BrokenFolders: []string{}}
	a.steps = []stepResult{{Step: stepText}}
	data, _ = json.Marshal(buildOrganizeView(a))
	if !strings.Contains(string(data), `"textFiles":["A/x/song.txt"]`) || !strings.Contains(string(data), `"step":"text"`) {
		t.Errorf("view = %s", data)
	}
}
