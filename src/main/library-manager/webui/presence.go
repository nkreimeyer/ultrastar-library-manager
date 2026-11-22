package webui

import (
	"sync"
	"time"
)

// presence counts the open pages: a page keeps one event stream (/api/job/events) open for as
// long as its tab is there, so a closed tab or browser is a stream that ended.
type presence struct {
	mu   sync.Mutex
	open int
	seen bool      // a page was open at some time
	last time.Time // when the last page went away
}

func (p *presence) opened() {
	p.mu.Lock()
	p.open++
	p.seen = true
	p.mu.Unlock()
}

func (p *presence) closed() {
	p.mu.Lock()
	p.open--
	p.last = time.Now()
	p.mu.Unlock()
}

// gone reports whether pages were open once and none is any more for at least grace since
// the later of the last page leaving and since.
func (p *presence) gone(grace time.Duration, since time.Time) bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	if !p.seen || p.open > 0 {
		return false
	}
	at := p.last
	if since.After(at) {
		at = since
	}
	return time.Since(at) >= grace
}

// idleSince reports whether a job runs, and when the last one ended (zero if none did).
func (r *jobRunner) idleSince() (running bool, ended time.Time) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.cur == nil {
		return false, time.Time{}
	}
	return r.cur.State == stateRunning, r.cur.Ended
}

// WatchClients returns a channel that is closed when every page of the UI has been gone for
// grace (a reload or a short network gap is no reason to stop) and no job runs; a job that
// ends after the last page left starts the grace period over. It never fires before a page
// was open once, so a browser that did not open at all leaves the server waiting for the
// address to be used. The watch ends with Close.
func (s *Server) WatchClients(grace time.Duration) <-chan struct{} {
	gone := make(chan struct{})
	go func() {
		tick := time.NewTicker(max(grace/10, 10*time.Millisecond))
		defer tick.Stop()
		for {
			select {
			case <-s.closed:
				return
			case <-tick.C:
				if running, ended := s.jobs.idleSince(); !running && s.presence.gone(grace, ended) {
					close(gone)
					return
				}
			}
		}
	}()
	return gone
}
