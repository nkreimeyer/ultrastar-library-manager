package webui

import (
	"context"
	"fmt"
	"net/http"
	"runtime"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/nkreimeyer/library-manager/fsops"
)

// TestJobStartAfterCloseRefused: once the server is closed (the program stops), a request
// still being handled starts no job: it gets 503 and nothing runs that nobody waits for.
func TestJobStartAfterCloseRefused(t *testing.T) {
	s := newTestServer(t)
	lib, out := removeLibrary(t)
	s.srv.Close()
	var e apiError
	if got := s.do("POST", "/api/overview", folderRequest{Library: lib, Out: out}, &e); got != http.StatusServiceUnavailable || e.Error != errClosed.Error() {
		t.Errorf("start after close: %d %q, want 503 %q", got, e.Error, errClosed)
	}
	if _, _, _, ok := s.srv.jobs.current(0); ok {
		t.Error("a job was started after close")
	}
}

// TestJobStartRacingClose: a start racing close either gets a job that close cancels and
// waits for, or is refused; once close has returned, every start is refused and no
// accepted job is still running.
func TestJobStartRacingClose(t *testing.T) {
	r := newRunner(t)
	release := make(chan struct{}) // ends jobs close did not cancel (only without the fix)
	defer close(release)
	var accepted, ended atomic.Int64
	fn := func(ctx context.Context, c jobControl) (any, error) {
		defer ended.Add(1)
		select {
		case <-ctx.Done():
		case <-release:
		}
		return nil, ctx.Err()
	}
	closed := make(chan struct{})
	var wg sync.WaitGroup
	var afterClose atomic.Int64 // starts after close returned that were not refused
	for i := range 8 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for n := 0; ; n++ {
				select {
				case <-closed:
					if _, err := r.start(fmt.Sprintf("late%d", i), "test", fsops.New(true), fn); err != errClosed {
						afterClose.Add(1)
						if err == nil {
							accepted.Add(1)
						}
					}
					return
				default:
				}
				if _, err := r.start(fmt.Sprintf("j%d-%d", i, n), "test", fsops.New(true), fn); err == nil {
					accepted.Add(1)
				} else if err != errBusy && err != errClosed {
					t.Errorf("start: %v", err)
				}
			}
		}()
	}
	// Close while the starters run (they keep trying while a job runs).
	for accepted.Load() == 0 {
		runtime.Gosched()
	}
	r.close()
	endedAtClose := ended.Load()
	close(closed)
	wg.Wait()
	if n := afterClose.Load(); n != 0 {
		t.Errorf("%d starts after close were not refused", n)
	}
	if a := accepted.Load(); a != endedAtClose {
		t.Errorf("%d jobs accepted, %d ended when close returned", a, endedAtClose)
	}
}
