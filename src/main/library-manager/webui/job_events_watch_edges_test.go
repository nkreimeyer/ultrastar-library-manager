package webui

import (
	"bufio"
	"context"
	"encoding/json"
	"net/http"
	"testing"
	"time"

	log "github.com/sirupsen/logrus"

	"github.com/nkreimeyer/library-manager/fsops"
)

type sseConn struct {
	sc   *bufio.Scanner
	stop func()
}

func openEvents(t *testing.T, s *testServer, query, lastID string) *sseConn {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	req, _ := http.NewRequestWithContext(ctx, "GET", s.base+"/api/job/events"+query, nil)
	if lastID != "" {
		req.Header.Set("Last-Event-ID", lastID)
	}
	resp, err := s.client.Do(req)
	if err != nil {
		cancel()
		t.Fatal(err)
	}
	t.Cleanup(func() { cancel(); resp.Body.Close() })
	return &sseConn{sc: bufio.NewScanner(resp.Body), stop: cancel}
}

func fastEvents(t *testing.T, interval, keepAlive time.Duration) {
	t.Helper()
	oldEv, oldKA := eventInterval, keepAliveInterval
	eventInterval, keepAliveInterval = interval, keepAlive
	t.Cleanup(func() { eventInterval, keepAliveInterval = oldEv, oldKA })
}

// A Last-Event-ID that counts from line 0 resumes at line 0, whatever ?log= says.
func TestEventsLastEventIDZero(t *testing.T) {
	fastEvents(t, 5*time.Millisecond, time.Hour)
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
	c := openEvents(t, s, "?log=2", "j1/0")
	evs := scanEvents(t, c.sc, func(evs []event) bool {
		for _, e := range evs {
			if e.name == "log" {
				return true
			}
		}
		return false
	})
	var ls []LogLine
	for _, e := range evs {
		if e.name == "log" {
			json.Unmarshal([]byte(e.data), &ls)
		}
	}
	if got := msgs(ls); got != "0:line 0 1:line 1 2:line 2" {
		t.Errorf("lines = %s, want all three from line 0", got)
	}
}

// An idle job is sent once, not on every tick.
func TestEventsNoRepeatWhenNothingChanges(t *testing.T) {
	fastEvents(t, 5*time.Millisecond, time.Hour)
	s := newTestServer(t)
	s.srv.jobs.start("j1", "test", fsops.New(true), func(context.Context, jobControl) (any, error) {
		return "done", nil
	})
	s.wait()
	c := openEvents(t, s, "", "")
	go func() { time.Sleep(400 * time.Millisecond); c.stop() }()
	jobEvents := 0
	for c.sc.Scan() {
		if c.sc.Text() == "event: job" {
			jobEvents++
		}
	}
	if jobEvents != 1 {
		t.Errorf("%d job events in 400 ms of nothing changing, want 1", jobEvents)
	}
}

// The keep-alive comes again and again, not only once.
func TestEventsKeepAliveRepeats(t *testing.T) {
	fastEvents(t, 5*time.Millisecond, 30*time.Millisecond)
	s := newTestServer(t)
	c := openEvents(t, s, "", "")
	go func() { time.Sleep(3 * time.Second); c.stop() }()
	n := 0
	for c.sc.Scan() {
		if c.sc.Text() == ": keep-alive" {
			if n++; n == 3 {
				return
			}
		}
	}
	t.Fatalf("%d keep-alives, want 3", n)
}

// The watch looks often enough: a page gone for the grace period is noticed within a small
// fraction of it, and even a tiny grace period does not break the ticker.
func TestWatchClientsTicksOftenEnough(t *testing.T) {
	s := newTestServer(t)
	grace := 200 * time.Millisecond
	gone := s.srv.WatchClients(grace)
	closePage := openPage(t, s)
	closePage()
	if !fired(gone, 5*grace) {
		t.Errorf("not noticed within %v of a %v grace period", 5*grace, grace)
	}

	s2 := newTestServer(t)
	gone = s2.srv.WatchClients(time.Nanosecond)
	closePage = openPage(t, s2)
	closePage()
	if !fired(gone, 3*time.Second) {
		t.Error("a 1 ns grace period never fired")
	}
}
