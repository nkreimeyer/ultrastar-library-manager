package webui

import (
	"context"
	"net/http"
	"testing"
	"time"

	"github.com/nkreimeyer/library-manager/fsops"
)

// openPage opens the event stream of a page; the returned func closes it (the tab was closed).
func openPage(t *testing.T, s *testServer) (closePage func()) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	req, _ := http.NewRequestWithContext(ctx, "GET", s.base+"/api/job/events", nil)
	resp, err := s.client.Do(req)
	if err != nil {
		cancel()
		t.Fatal(err)
	}
	t.Cleanup(func() { cancel(); resp.Body.Close() })
	return func() { cancel(); resp.Body.Close() }
}

func fired(ch <-chan struct{}, within time.Duration) bool {
	select {
	case <-ch:
		return true
	case <-time.After(within):
		return false
	}
}

const testGrace = 300 * time.Millisecond

func TestWatchClients(t *testing.T) {
	t.Run("never before a page was open", func(t *testing.T) {
		s := newTestServer(t)
		if fired(s.srv.WatchClients(testGrace), 4*testGrace) {
			t.Error("fired without any page")
		}
	})
	t.Run("a closed page stops it after the grace period", func(t *testing.T) {
		s := newTestServer(t)
		gone := s.srv.WatchClients(testGrace)
		closePage := openPage(t, s)
		if fired(gone, 2*testGrace) {
			t.Fatal("fired while the page is open")
		}
		closePage()
		start := time.Now()
		if !fired(gone, 5*time.Second) {
			t.Fatal("did not fire after the page was closed")
		}
		if d := time.Since(start); d < testGrace/2 {
			t.Errorf("fired %v after the page closed, before the grace period", d)
		}
	})
	t.Run("a reload within the grace period is no reason", func(t *testing.T) {
		s := newTestServer(t)
		gone := s.srv.WatchClients(testGrace)
		openPage(t, s)()
		time.Sleep(testGrace / 3)
		closePage := openPage(t, s) // the reloaded page
		if fired(gone, 2*testGrace) {
			t.Fatal("fired although the page came back")
		}
		closePage()
		if !fired(gone, 5*time.Second) {
			t.Error("did not fire after the last page closed")
		}
	})
	t.Run("two pages: the first closing is no reason", func(t *testing.T) {
		s := newTestServer(t)
		gone := s.srv.WatchClients(testGrace)
		closeA, closeB := openPage(t, s), openPage(t, s)
		closeA()
		if fired(gone, 2*testGrace) {
			t.Fatal("fired with a page still open")
		}
		closeB()
		if !fired(gone, 5*time.Second) {
			t.Error("did not fire after both pages closed")
		}
	})
	t.Run("a running job keeps it, its end starts the grace period", func(t *testing.T) {
		s := newTestServer(t)
		gone := s.srv.WatchClients(testGrace)
		release := make(chan struct{})
		if _, err := s.srv.jobs.start("w1", "test", fsops.New(true), func(context.Context, jobControl) (any, error) {
			<-release
			return nil, nil
		}); err != nil {
			t.Fatal(err)
		}
		openPage(t, s)()
		if fired(gone, 3*testGrace) {
			t.Fatal("fired while a job runs")
		}
		close(release)
		start := time.Now()
		if !fired(gone, 5*time.Second) {
			t.Fatal("did not fire after the job ended")
		}
		if d := time.Since(start); d < testGrace/2 {
			t.Errorf("fired %v after the job ended, before the grace period", d)
		}
	})
	t.Run("Close ends the watch", func(t *testing.T) {
		s := newTestServer(t)
		gone := s.srv.WatchClients(testGrace)
		s.srv.Close()
		if fired(gone, 3*testGrace) {
			t.Error("fired after Close without a page")
		}
	})
}
