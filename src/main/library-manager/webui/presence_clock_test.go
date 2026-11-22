package webui

import (
	"context"
	"testing"
	"time"

	"github.com/nkreimeyer/library-manager/fsops"
)

// fakeClock is a settable clock for presence.
type fakeClock struct{ t time.Time }

func (c *fakeClock) now() time.Time          { return c.t }
func (c *fakeClock) advance(d time.Duration) { c.t = c.t.Add(d) }

func newFakePresence() (*presence, *fakeClock) {
	c := &fakeClock{t: time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)}
	return &presence{now: c.now}, c
}

// The grace logic of the watch, without waiting for real time.
func TestPresenceGone(t *testing.T) {
	const grace = 15 * time.Second
	t.Run("never before a page was open", func(t *testing.T) {
		p, c := newFakePresence()
		c.advance(24 * time.Hour)
		if p.gone(grace, time.Time{}) {
			t.Error("gone without any page ever")
		}
	})
	t.Run("not while a page is open", func(t *testing.T) {
		p, c := newFakePresence()
		p.opened()
		c.advance(24 * time.Hour)
		if p.gone(grace, time.Time{}) {
			t.Error("gone with an open page")
		}
	})
	t.Run("after the grace period, not before", func(t *testing.T) {
		p, c := newFakePresence()
		p.opened()
		p.closed()
		c.advance(grace - time.Nanosecond)
		if p.gone(grace, time.Time{}) {
			t.Error("gone just before the grace period")
		}
		c.advance(time.Nanosecond)
		if !p.gone(grace, time.Time{}) {
			t.Error("not gone after exactly the grace period")
		}
	})
	t.Run("a reload within the grace period", func(t *testing.T) {
		p, c := newFakePresence()
		p.opened()
		p.closed()
		c.advance(grace / 2)
		p.opened() // the reloaded page
		c.advance(grace * 10)
		if p.gone(grace, time.Time{}) {
			t.Error("gone although the page came back")
		}
		p.closed()
		c.advance(grace - time.Second)
		if p.gone(grace, time.Time{}) {
			t.Error("the grace period did not start over after the reload closed")
		}
		c.advance(time.Second)
		if !p.gone(grace, time.Time{}) {
			t.Error("not gone after the last page closed")
		}
	})
	t.Run("two pages: the first closing is no reason", func(t *testing.T) {
		p, c := newFakePresence()
		p.opened()
		p.opened()
		p.closed()
		c.advance(grace * 10)
		if p.gone(grace, time.Time{}) {
			t.Error("gone with a page still open")
		}
		p.closed()
		c.advance(grace)
		if !p.gone(grace, time.Time{}) {
			t.Error("not gone after both closed")
		}
	})
	t.Run("a later since restarts the period, an earlier one does not", func(t *testing.T) {
		p, c := newFakePresence()
		p.opened()
		p.closed() // at t0
		c.advance(grace)
		if !p.gone(grace, c.t.Add(-grace*2)) {
			t.Error("an earlier since must not delay it")
		}
		since := c.t // a job ended just now
		if p.gone(grace, since) {
			t.Error("a job that just ended must restart the period")
		}
		c.advance(grace)
		if !p.gone(grace, since) {
			t.Error("not gone a grace period after the job ended")
		}
	})
}

// clientsGone adds the job: a running job keeps the UI, its end starts the period over.
func TestClientsGoneWithJob(t *testing.T) {
	s := newTestServer(t)
	c := &fakeClock{t: time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)}
	s.srv.presence.now = c.now
	const grace = 10 * time.Minute

	s.srv.presence.opened()
	s.srv.presence.closed()
	c.advance(grace * 2)
	if !s.srv.clientsGone(grace) {
		t.Fatal("not gone with no page and no job")
	}

	release := make(chan struct{})
	if _, err := s.srv.jobs.start("w1", "test", fsops.New(true), func(context.Context, jobControl) (any, error) {
		<-release
		return nil, nil
	}); err != nil {
		t.Fatal(err)
	}
	c.advance(grace * 10)
	if s.srv.clientsGone(grace) {
		t.Fatal("gone while a job runs")
	}
	close(release)
	deadline := time.Now().Add(10 * time.Second)
	for {
		if running, _ := s.srv.jobs.idleSince(); !running {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("the job did not end")
		}
		time.Sleep(time.Millisecond)
	}
	// The job ended (in real time) shortly before "now" of the fake clock plus the 10 grace
	// periods advanced above: move the fake clock back to the job's end for the check.
	_, ended := s.srv.jobs.idleSince()
	c.t = ended.Add(grace / 2)
	if s.srv.clientsGone(grace) {
		t.Error("gone half a grace period after the job ended")
	}
	c.t = ended.Add(grace)
	if !s.srv.clientsGone(grace) {
		t.Error("not gone a grace period after the job ended")
	}
}
