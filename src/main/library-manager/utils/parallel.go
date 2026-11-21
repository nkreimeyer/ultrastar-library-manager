package utils

import (
	"context"
	"runtime/debug"
	"sync"
	"sync/atomic"

	log "github.com/sirupsen/logrus"
)

// Parallel configures ForEachParallel.
type Parallel struct {
	// Workers is the most items processed at the same time (<= 0 means 1). No more
	// goroutines than items are started.
	Workers int
	// Progress, if set, is called after each item that ran (one that panicked too) with
	// the number of items done so far and len(items). The calls are serialized and done
	// grows by one with each call, so Progress need not be safe for concurrent use.
	Progress func(done, total int)
	// OnPanic, if set, is called after a panic in fn was recovered and logged, with the
	// item's index and the panic value, on the worker that ran it. Modules count it as a
	// failure there (ops.Fail) and fill in what the item would have produced.
	OnPanic func(i int, p any)
}

// ForEachParallel calls fn(i, items[i]) for every item, with at most opt.Workers
// goroutines, and returns when every started call has returned.
//
// Once ctx is done no further item is started (a nil ctx never is); items already running
// finish. It returns ctx.Err() if it left items out that way, else nil: nil means fn ran
// for every item.
//
// A panic in fn is recovered: it is logged (logrus Error, with the item's index and the
// stack), reported to opt.OnPanic, and the other items still run. So a bug that one song
// triggers costs only that song, as the modules' "log and continue with the next song"
// rule wants, instead of the whole run (or, in the web UI, the server). fn must release
// its locks with defer, or a recovered panic leaves them held. Fatal runtime errors
// (concurrent map writes, out of memory) cannot be recovered.
func ForEachParallel[T any](ctx context.Context, items []T, opt Parallel, fn func(i int, item T)) error {
	if ctx == nil {
		ctx = context.Background()
	}
	n := len(items)
	if n == 0 {
		return nil
	}
	var skipped atomic.Bool
	var progressMu sync.Mutex
	done := 0
	progress := func() {
		if opt.Progress == nil {
			return
		}
		progressMu.Lock()
		defer progressMu.Unlock()
		done++
		opt.Progress(done, n)
	}
	run := func(i int) {
		defer func() {
			if p := recover(); p != nil {
				log.Errorf("panic while processing item %d of %d: %v\n%s", i, n, p, debug.Stack())
				if opt.OnPanic != nil {
					opt.OnPanic(i, p)
				}
			}
		}()
		fn(i, items[i])
	}

	next := make(chan int)
	workers := max(1, min(opt.Workers, n))
	var wg sync.WaitGroup
	wg.Add(workers)
	for range workers {
		go func() {
			defer wg.Done()
			for i := range next {
				// Checked here, right before the item starts: no item starts after the
				// cancel. The remaining items are still handed out, and left out here.
				if ctx.Err() != nil {
					skipped.Store(true)
					continue
				}
				run(i)
				progress()
			}
		}()
	}
	for i := range n {
		next <- i
	}
	close(next)
	wg.Wait()
	if skipped.Load() {
		return ctx.Err()
	}
	return nil
}
