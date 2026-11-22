package webui

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/nkreimeyer/library-manager/fsops"
	"github.com/nkreimeyer/library-manager/internal/testutil"
)

// Before any job ran, GET /api/job answers 200 with null, and a cancel reports that
// nothing was cancelled (and starts nothing).
func TestJobWithoutAnyJob(t *testing.T) {
	s := newTestServer(t)
	var j *Job
	if got := s.do("GET", "/api/job", nil, &j); got != http.StatusOK || j != nil {
		t.Errorf("GET /api/job = %d %+v, want 200 null", got, j)
	}
	var res map[string]bool
	if got := s.do("POST", "/api/job/cancel", map[string]any{}, &res); got != http.StatusOK || res["cancelled"] {
		t.Errorf("cancel without a job = %d %v", got, res)
	}
	if _, _, _, ok := s.srv.jobs.current(0); ok {
		t.Error("a cancel created a job")
	}
}

// A cancel after the job has ended cancels nothing and leaves its state alone.
func TestJobCancelAfterEnd(t *testing.T) {
	s := newTestServer(t)
	if _, err := s.srv.jobs.start("d1", "test", fsops.New(true), func(context.Context, jobControl) (any, error) { return "ok", nil }); err != nil {
		t.Fatal(err)
	}
	if j := s.wait(); j.State != stateDone {
		t.Fatalf("state %s", j.State)
	}
	var res map[string]bool
	if got := s.do("POST", "/api/job/cancel", map[string]any{}, &res); got != http.StatusOK || res["cancelled"] {
		t.Errorf("cancel of an ended job = %d %v", got, res)
	}
	var j Job
	s.do("GET", "/api/job", nil, &j)
	if j.State != stateDone {
		t.Errorf("state after the cancel: %s", j.State)
	}
}

// noFlush is a ResponseWriter that cannot stream.
type noFlush struct {
	h    http.Header
	code int
	body []byte
}

func (n *noFlush) Header() http.Header         { return n.h }
func (n *noFlush) WriteHeader(c int)           { n.code = c }
func (n *noFlush) Write(p []byte) (int, error) { n.body = append(n.body, p...); return len(p), nil }

// Without http.Flusher the event stream answers 500 and never counts as an open page.
func TestJobEventsNeedsFlusher(t *testing.T) {
	s := newTestServer(t)
	w := &noFlush{h: http.Header{}}
	s.srv.handleJobEvents(w, httptest.NewRequest("GET", "/api/job/events", nil))
	if w.code != http.StatusInternalServerError || len(w.body) == 0 {
		t.Errorf("code %d body %q, want 500 with a message", w.code, w.body)
	}
	if open, seen := s.srv.presence.snapshot(); seen || open != 0 {
		t.Error("a refused stream counted as a page")
	}
}

func (p *presence) snapshot() (open int, seen bool) {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.open, p.seen
}

// An event stream is an open page; a client that disconnects mid-stream releases it.
func TestJobEventsDisconnectReleasesPresence(t *testing.T) {
	s := newTestServer(t)
	ctx, cancel := context.WithCancel(context.Background())
	req, _ := http.NewRequestWithContext(ctx, "GET", s.base+"/api/job/events", nil)
	resp, err := s.client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK || resp.Header.Get("Content-Type") != "text/event-stream" {
		t.Fatalf("%d %q", resp.StatusCode, resp.Header.Get("Content-Type"))
	}
	if open, seen := s.srv.presence.snapshot(); open != 1 || !seen {
		t.Fatalf("open %d seen %v with the stream open", open, seen)
	}
	cancel()
	deadline := time.Now().Add(5 * time.Second)
	for {
		if open, _ := s.srv.presence.snapshot(); open == 0 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("the page stayed open after the client left")
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// A journal that cannot be read (here a folder in its place) is a 500, not an empty list.
func TestOrganizeHistoryUnreadableJournal(t *testing.T) {
	s := newTestServer(t)
	lib, out := removeLibrary(t)
	if err := os.MkdirAll(filepath.Join(out, "organize-journal.jsonl"), 0o755); err != nil {
		t.Fatal(err)
	}
	var e apiError
	if got := s.do("POST", "/api/organize/history", folderRequest{Library: lib, Out: out}, &e); got != http.StatusInternalServerError || e.Error == "" {
		t.Errorf("history = %d %q, want 500 with an error", got, e.Error)
	}
}

// The check analysis of a library whose output folder cannot be created ends the job in
// error; nothing is written and no analysis is kept.
func TestCheckAnalysePrepareOutDirFails(t *testing.T) {
	testutil.SkipIfRoot(t)
	s := newTestServer(t)
	lib, _ := removeLibrary(t)
	parent := t.TempDir()
	if err := os.Chmod(parent, 0o500); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.Chmod(parent, 0o755) })
	out := filepath.Join(parent, "out")
	var j Job
	if got := s.do("POST", "/api/check/analyse", checkRequest{Library: lib, Out: out}, &j); got != http.StatusAccepted {
		t.Fatalf("analyse: %d", got)
	}
	if j = s.wait(); j.State != stateFailed || j.Error == "" {
		t.Fatalf("job %+v, want an error", j)
	}
	if s.srv.checkRes != nil {
		t.Error("a failed check was kept")
	}
}

// An apply whose stored request no longer resolves (the library is gone) is a 400 and
// starts no job; the analysis stays unapplied.
func TestCheckApplyBadStoredRequest(t *testing.T) {
	s := newTestServer(t)
	lib, out := removeLibrary(t)
	var j Job
	s.do("POST", "/api/check/analyse", checkRequest{Library: lib, Out: out}, &j)
	if j = s.wait(); j.State != stateDone {
		t.Fatalf("analyse: %+v", j)
	}
	id := j.Result.(map[string]any)["analysis"].(string)
	before, _, _, _ := s.srv.jobs.current(0)
	if err := os.RemoveAll(lib); err != nil {
		t.Fatal(err)
	}
	var e apiError
	if got := s.do("POST", "/api/check/apply", map[string]any{"analysis": id}, &e); got != http.StatusBadRequest || e.Error == "" {
		t.Errorf("apply = %d %q, want 400", got, e.Error)
	}
	if after, _, _, _ := s.srv.jobs.current(0); after.ID != before.ID {
		t.Error("a refused apply started a job")
	}
	var v struct {
		Applied bool `json:"applied"`
	}
	s.do("GET", "/api/check/analysis?id="+id, nil, &v)
	if v.Applied {
		t.Error("a refused apply marked the analysis applied")
	}
}
