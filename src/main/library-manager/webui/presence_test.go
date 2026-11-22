package webui

import (
	"context"
	"net/http"
	"testing"
	"time"
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

const testGrace = 100 * time.Millisecond

// The real-time end-to-end paths of the watch (ticker, event streams, Close); the grace
// logic itself is tested with a fake clock in presence_clock_test.go.
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
	t.Run("Close ends the watch", func(t *testing.T) {
		s := newTestServer(t)
		gone := s.srv.WatchClients(testGrace)
		s.srv.Close()
		if fired(gone, 3*testGrace) {
			t.Error("fired after Close without a page")
		}
	})
}
