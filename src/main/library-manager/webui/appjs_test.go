package webui

import (
	"net/http"
	"os/exec"
	"testing"
)

// TestAppJS runs testdata/appjs_check.js (node built-ins only) against the embedded
// static/app.js: the pure logic of the page (requests, ETA, byte sizes, saved form,
// recent libraries). It skips without node on PATH.
func TestAppJS(t *testing.T) {
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("node is not on PATH")
	}
	out, err := exec.Command(node, "testdata/appjs_check.js", "static/app.js").CombinedOutput()
	if err != nil {
		t.Fatalf("app.js checks failed: %v\n%s", err, out)
	}
}

// app.js reads these JSON names of the job object and of the history list. A rename on the
// Go side would break the page silently, so they are pinned here.
func TestAppJSFieldNames(t *testing.T) {
	s := newTestServer(t)
	lib, out := dedupeLibrary(t)
	v := analyseOrganize(t, s, organizeRequest{Mode: "dedupe", Library: lib, Out: out})
	if got := s.do("POST", "/api/organize/apply", map[string]any{"analysis": v.ID}, &Job{}); got != http.StatusAccepted {
		t.Fatalf("apply: %d", got)
	}
	s.wait()

	var job map[string]any
	s.do("GET", "/api/job", nil, &job)
	for _, k := range []string{"id", "kind", "state", "done", "total", "stats", "dryRun", "result", "logs", "started"} {
		if _, ok := job[k]; !ok {
			t.Errorf("job JSON has no %q: %v", k, job)
		}
	}
	if job["state"] != "done" {
		t.Errorf("state = %v", job["state"])
	}
	stats, _ := job["stats"].(map[string]any)
	for _, k := range []string{"moved", "written", "replaced", "failed", "removed"} {
		if _, ok := stats[k]; !ok {
			t.Errorf("stats JSON has no %q: %v", k, stats)
		}
	}

	var hist struct {
		OutDir string           `json:"outDir"`
		Runs   []map[string]any `json:"runs"`
	}
	if got := s.do("POST", "/api/organize/history", map[string]any{"library": lib, "out": out}, &hist); got != http.StatusOK {
		t.Fatalf("history: %d", got)
	}
	if hist.OutDir == "" || len(hist.Runs) != 1 {
		t.Fatalf("history = %+v", hist)
	}
	run := hist.Runs[0]
	for _, k := range []string{"id", "time", "moves", "undone", "next"} {
		if _, ok := run[k]; !ok {
			t.Errorf("history run has no %q: %v", k, run)
		}
	}
	if run["moves"] != 1.0 || run["next"] != true || run["undone"] != false {
		t.Errorf("run = %v", run)
	}
}
