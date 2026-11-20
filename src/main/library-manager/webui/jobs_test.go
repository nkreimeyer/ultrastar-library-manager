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
	next() // 8 lines, the newest 5 are kept
	j, _, lines, _ = r.current(0)
	if j.Logs != 8 || msgs(lines) != "3:line 3 4:line 4 5:line 5 6:line 6 7:line 7" {
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
	resp, err := s.client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	start := time.Now()
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
