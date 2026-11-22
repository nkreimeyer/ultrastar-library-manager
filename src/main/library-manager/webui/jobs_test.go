package webui

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"path/filepath"
	"strings"
	"testing"
	"time"

	log "github.com/sirupsen/logrus"

	"github.com/nkreimeyer/library-manager/fsops"
)

// newRunner is a job runner whose log hook is removed after the test.
func newRunner(t *testing.T) *jobRunner {
	t.Helper()
	r := newJobRunner()
	t.Cleanup(func() {
		r.close()
		log.StandardLogger().ReplaceHooks(make(log.LevelHooks))
	})
	return r
}

// step runs a job whose function does each step when told to, so a test can look at the
// job between the steps. next() runs the next step and waits for it.
func step(t *testing.T, r *jobRunner, ops *fsops.Ops, steps ...func(jobControl)) (next func()) {
	t.Helper()
	do, did := make(chan struct{}), make(chan struct{})
	if _, err := r.start(fmt.Sprintf("s%d", time.Now().UnixNano()), "test", ops, func(ctx context.Context, c jobControl) (any, error) {
		for _, s := range steps {
			<-do
			s(c)
			did <- struct{}{}
		}
		<-do
		return "result", nil
	}); err != nil {
		t.Fatal(err)
	}
	return func() {
		do <- struct{}{}
		if len(steps) > 0 {
			<-did
			steps = steps[1:]
		}
	}
}

func version(r *jobRunner) int {
	_, v, _, _ := r.current(0)
	return v
}

func TestJobProgressVersion(t *testing.T) {
	r := newRunner(t)
	tests := []struct {
		name        string
		phase       string
		done, total int
		changed     bool
		wantDone    int
		wantPhase   string
	}{
		{"first", "scan", 1, 5, true, 1, "scan"},
		{"same", "scan", 1, 5, false, 1, "scan"},
		{"more done", "scan", 2, 5, true, 2, "scan"},
		{"late report of an earlier item", "scan", 1, 5, false, 2, "scan"},
		{"other total", "scan", 2, 6, true, 2, "scan"},
		{"same phase starts again at 0", "scan", 0, 6, true, 0, "scan"},
		{"its start reported twice", "scan", 0, 6, false, 0, "scan"},
		{"and counts up again", "scan", 1, 6, true, 1, "scan"},
		{"next phase starts at 0", "plan", 0, 3, true, 0, "plan"},
	}
	var steps []func(jobControl)
	for _, tt := range tests {
		steps = append(steps, func(c jobControl) { c.Progress(tt.phase, tt.done, tt.total) })
	}
	next := step(t, r, fsops.New(true), steps...)
	for _, tt := range tests {
		before := version(r)
		next()
		j, v, _, _ := r.current(0)
		if (v != before) != tt.changed || j.Done != tt.wantDone || j.Phase != tt.wantPhase || (tt.changed && v != before+1) {
			t.Errorf("%s: version %d -> %d, job %s %d/%d", tt.name, before, v, j.Phase, j.Done, j.Total)
		}
	}
	before := version(r)
	next() // the job ends
	r.wg.Wait()
	if j, v, _, _ := r.current(0); j.State != stateDone || v <= before || j.Result != "result" {
		t.Errorf("after the end: version %d -> %d, %+v", before, v, j)
	}
}

func TestJobStatsWhileRunning(t *testing.T) {
	r := newRunner(t)
	ops := fsops.New(true)
	dir := t.TempDir()
	next := step(t, r, ops, func(jobControl) {
		if _, err := ops.WriteFileAtomic(filepath.Join(dir, "a.txt"), []byte("x")); err != nil {
			t.Error(err)
		}
	})
	if j, _, _, _ := r.current(0); j.Stats.Written != 0 {
		t.Errorf("before the write: %+v", j.Stats)
	}
	next()
	if j, _, _, _ := r.current(0); j.State != stateRunning || j.Stats.Written != 1 {
		t.Errorf("while running: %s %+v", j.State, j.Stats)
	}
	next()
	r.wg.Wait()
	if j, _, _, _ := r.current(0); j.Stats.Written != 1 {
		t.Errorf("after the end: %+v", j.Stats)
	}
}

func msgs(lines []LogLine) string {
	var b []string
	for _, l := range lines {
		b = append(b, fmt.Sprintf("%d:%s", l.N, l.Msg))
	}
	return strings.Join(b, " ")
}

func TestJobLogLines(t *testing.T) {
	old := maxLogLines
	maxLogLines = 5
	t.Cleanup(func() { maxLogLines = old })
	r := newRunner(t)
	log.Infof("before the job") // no job: not collected
	next := step(t, r,
		fsops.New(true),
		func(jobControl) {
			for i := range 3 {
				log.Infof("line %d", i)
			}
		},
		func(jobControl) {
			for i := 3; i < 8; i++ {
				log.Infof("line %d", i)
			}
		})
	next()
	j, _, lines, _ := r.current(0)
	if j.Logs != 3 || msgs(lines) != "0:line 0 1:line 1 2:line 2" {
		t.Errorf("logs = %d, %s", j.Logs, msgs(lines))
	}
	if _, _, lines, _ = r.current(2); msgs(lines) != "2:line 2" {
		t.Errorf("from 2: %s", msgs(lines))
	}
	if _, _, lines, _ = r.current(3); len(lines) != 0 {
		t.Errorf("from 3: %s", msgs(lines))
	}
	next() // 8 lines: the 7th cuts the log back to the newest 5, then the 8th is added
	j, _, lines, _ = r.current(0)
	if j.Logs != 8 || msgs(lines) != "2:line 2 3:line 3 4:line 4 5:line 5 6:line 6 7:line 7" {
		t.Errorf("after trimming: logs = %d, %s", j.Logs, msgs(lines))
	}
	if _, _, lines, _ = r.current(6); msgs(lines) != "6:line 6 7:line 7" {
		t.Errorf("from 6 after trimming: %s", msgs(lines))
	}
	next()
	r.wg.Wait()
	log.Infof("after the job") // the job ended: not collected
	if j, _, _, _ := r.current(0); j.Logs != 8 {
		t.Errorf("after the end: logs = %d", j.Logs)
	}
}

// TestJobLogTrimAmortised: the log is not cut for every line over the cap, only once it is
// 10 % over, and then back to the cap; line numbers stay continuous and a request for a
// dropped line gets the lines from the oldest kept one on.
func TestJobLogTrimAmortised(t *testing.T) {
	old := maxLogLines
	maxLogLines = 100
	t.Cleanup(func() { maxLogLines = old })
	r := newRunner(t)
	write := func(from, to int) func(jobControl) {
		return func(jobControl) {
			for i := from; i < to; i++ {
				log.Infof("line %d", i)
			}
		}
	}
	// window uses Errorf, not Fatalf: the job waits for every next() and would block the
	// cleanup's r.close().
	window := func(wantFirst, wantLogs int) {
		t.Helper()
		j, _, lines, _ := r.current(0)
		if j.Logs != wantLogs || len(lines) != wantLogs-wantFirst {
			t.Errorf("logs = %d, %d lines; want %d, %d", j.Logs, len(lines), wantLogs, wantLogs-wantFirst)
			return
		}
		for k, l := range lines {
			if l.N != wantFirst+k || l.Msg != fmt.Sprintf("line %d", wantFirst+k) {
				t.Errorf("line %d = %d:%s", k, l.N, l.Msg)
				return
			}
		}
	}
	next := step(t, r, fsops.New(true), write(0, 101), write(101, 110), write(110, 111), write(111, 230))
	next()
	window(0, 101) // one over the cap: not cut yet
	next()
	window(0, 110) // at the limit: still not cut
	next()
	window(11, 111) // one over the limit: back to the newest 100
	next()
	window(121, 230) // cut every 11 lines, last at line 220 (to 121..220), then 221..229 added
	if _, _, lines, _ := r.current(5); len(lines) != 109 || lines[0].N != 121 {
		t.Errorf("from a dropped line: %d lines, first %v", len(lines), lines[:min(1, len(lines))])
	}
	if _, _, lines, _ := r.current(228); msgs(lines) != "228:line 228 229:line 229" {
		t.Errorf("from 228: %s", msgs(lines))
	}
	next()
	r.wg.Wait()
}

// TestJobEventsAcrossJobs reads one event stream over two jobs: each log line arrives
// once, the second job's lines from its first, and the end of each job is sent.
func TestJobEventsAcrossJobs(t *testing.T) {
	old := eventInterval
	eventInterval = 5 * time.Millisecond
	t.Cleanup(func() { eventInterval = old })
	s := newTestServer(t)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	req, _ := http.NewRequestWithContext(ctx, "GET", s.base+"/api/job/events", nil)
	resp, err := s.client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	sc := bufio.NewScanner(resp.Body)

	var got []string
	// readUntil reads until the job's state and the log line have both arrived (a job's
	// event comes before the log lines of the same moment: a new job clears the log view).
	readUntil := func(id, state, line string) {
		t.Helper()
		seen := false
		scanEvents(t, sc, func(evs []event) bool {
			e := evs[len(evs)-1]
			switch e.name {
			case "log":
				var lines []LogLine
				json.Unmarshal([]byte(e.data), &lines)
				for _, l := range lines {
					got = append(got, fmt.Sprintf("%d:%s", l.N, l.Msg))
				}
			case "job":
				var j Job
				json.Unmarshal([]byte(e.data), &j)
				seen = seen || j.ID == id && j.State == state
			}
			return seen && strings.HasSuffix(strings.Join(got, " "), line)
		})
	}
	for _, id := range []string{"a", "b"} {
		release := make(chan struct{})
		s.srv.jobs.start(id, "test", fsops.New(true), func(context.Context, jobControl) (any, error) {
			log.Infof("%s first", id)
			<-release
			log.Infof("%s second", id)
			return nil, nil
		})
		readUntil(id, stateRunning, id+" first")
		close(release)
		readUntil(id, stateDone, id+" second")
	}
	// A job that logs nothing: its events come without log lines.
	s.srv.jobs.start("c", "test", fsops.New(true), func(_ context.Context, c jobControl) (any, error) {
		c.Progress("quiet", 1, 1)
		return nil, nil
	})
	readUntil("c", stateDone, "b second")
	if strings.Join(got, " ") != "0:a first 1:a second 0:b first 1:b second" {
		t.Errorf("log lines = %v", got)
	}
}

func TestJobEventsKeepAlive(t *testing.T) {
	oldEv, oldKA := eventInterval, keepAliveInterval
	eventInterval, keepAliveInterval = 5*time.Millisecond, 30*time.Millisecond
	t.Cleanup(func() { eventInterval, keepAliveInterval = oldEv, oldKA })
	s := newTestServer(t)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	req, _ := http.NewRequestWithContext(ctx, "GET", s.base+"/api/job/events", nil)
	// Taken before the request: the server starts its keep-alive clock after flushing the
	// headers, before Do returns, so a start taken after Do measures less than it waited.
	start := time.Now()
	resp, err := s.client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	sc := bufio.NewScanner(resp.Body)
	for sc.Scan() {
		if sc.Text() == ": keep-alive" {
			if d := time.Since(start); d < keepAliveInterval {
				t.Errorf("keep-alive after %v, want >= %v", d, keepAliveInterval)
			}
			return
		}
	}
	t.Fatalf("no keep-alive: %v", sc.Err())
}

func TestNewIDUnique(t *testing.T) {
	s := &Server{}
	if a, b := s.newID("r"), s.newID("r"); a == b || a != "r1" || b != "r2" {
		t.Errorf("ids %q %q", a, b)
	}
}

// A browser that reconnects sends the id of the last log event back and gets only the
// lines it does not have; an id of another job gets the current job's lines from 0.
func TestJobEventsResumeAfterReconnect(t *testing.T) {
	old := eventInterval
	eventInterval = 5 * time.Millisecond
	t.Cleanup(func() { eventInterval = old })
	s := newTestServer(t)
	release := make(chan struct{})
	defer close(release)
	logged := make(chan struct{})
	s.srv.jobs.start("j1", "test", fsops.New(true), func(context.Context, jobControl) (any, error) {
		for i := range 3 {
			log.Infof("line %d", i)
		}
		close(logged)
		<-release
		return nil, nil
	})
	<-logged
	firstLog := func(lastID string) (id string, lines string) {
		t.Helper()
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		req, _ := http.NewRequestWithContext(ctx, "GET", s.base+"/api/job/events", nil)
		if lastID != "" {
			req.Header.Set("Last-Event-ID", lastID)
		}
		resp, err := s.client.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		sc := bufio.NewScanner(resp.Body)
		for sc.Scan() {
			switch line := sc.Text(); {
			case strings.HasPrefix(line, "id: "):
				id = strings.TrimPrefix(line, "id: ")
			case strings.HasPrefix(line, "data: [") && id != "":
				var ls []LogLine
				json.Unmarshal([]byte(strings.TrimPrefix(line, "data: ")), &ls)
				return id, msgs(ls)
			}
		}
		t.Fatalf("no log event: %v", sc.Err())
		return "", ""
	}
	if id, got := firstLog(""); id != "j1/3" || got != "0:line 0 1:line 1 2:line 2" {
		t.Errorf("first connection: id %q, lines %s", id, got)
	}
	if id, got := firstLog("j1/2"); id != "j1/3" || got != "2:line 2" {
		t.Errorf("reconnect: id %q, lines %s", id, got)
	}
	if _, got := firstLog("j0/2"); got != "0:line 0 1:line 1 2:line 2" {
		t.Errorf("reconnect from another job: lines %s", got)
	}
}

// Close ends the open event streams, so the HTTP server's Shutdown does not wait for them,
// but only after the stopped job's final state went out on them.
func TestCloseEndsEventStreams(t *testing.T) {
	s := newTestServer(t)
	s.srv.jobs.start("long", "test", fsops.New(true), func(ctx context.Context, _ jobControl) (any, error) {
		<-ctx.Done()
		time.Sleep(50 * time.Millisecond) // finishing the current step
		return nil, ctx.Err()
	})
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	req, _ := http.NewRequestWithContext(ctx, "GET", s.base+"/api/job/events", nil)
	resp, err := s.client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	sc := bufio.NewScanner(resp.Body)
	scanEvents(t, sc, func(evs []event) bool { return evs[len(evs)-1].name == "job" })
	ended := make(chan string, 1)
	go func() {
		last := ""
		for sc.Scan() {
			if line := sc.Text(); strings.HasPrefix(line, "data: {") {
				last = line
			}
		}
		ended <- last
	}()
	s.srv.Close()
	select {
	case last := <-ended:
		if !strings.Contains(last, `"state":"cancelled"`) {
			t.Errorf("last job event %s, want the cancelled state", last)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("the event stream is still open after Close")
	}
}
