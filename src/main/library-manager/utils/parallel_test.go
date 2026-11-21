package utils

import (
	"context"
	"errors"
	"fmt"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	log "github.com/sirupsen/logrus"
	logtest "github.com/sirupsen/logrus/hooks/test"
)

// Every item is processed exactly once, with its own index, whatever the worker count.
func TestForEachParallelEachItemOnce(t *testing.T) {
	for _, workers := range []int{-1, 0, 1, 3, 100} {
		for _, n := range []int{0, 1, 7, 50} {
			items := make([]string, n)
			for i := range items {
				items[i] = fmt.Sprint("item", i)
			}
			var mu sync.Mutex
			seen := map[int]int{}
			err := ForEachParallel(context.Background(), items, Parallel{Workers: workers}, func(i int, item string) {
				if item != items[i] {
					t.Errorf("workers %d: fn(%d, %q), want item %q", workers, i, item, items[i])
				}
				mu.Lock()
				seen[i]++
				mu.Unlock()
			})
			if err != nil {
				t.Errorf("workers %d, %d items: err = %v", workers, n, err)
			}
			if len(seen) != n {
				t.Errorf("workers %d: %d of %d items processed", workers, len(seen), n)
			}
			for i, c := range seen {
				if c != 1 {
					t.Errorf("workers %d: item %d processed %d times", workers, i, c)
				}
			}
		}
	}
}

// At most Workers items run at the same time, and Workers of them do (also with fewer
// items than a batch of the old code: small libraries run in parallel too).
func TestForEachParallelConcurrency(t *testing.T) {
	for _, workers := range []int{1, 2, 4} {
		var active, peak atomic.Int32
		var started sync.WaitGroup
		started.Add(workers)
		allStarted := make(chan struct{})
		go func() { started.Wait(); close(allStarted) }()
		items := make([]int, 20)
		reached := false
		err := ForEachParallel(context.Background(), items, Parallel{Workers: workers}, func(i int, _ int) {
			a := active.Add(1)
			defer active.Add(-1)
			for {
				p := peak.Load()
				if a <= p || peak.CompareAndSwap(p, a) {
					break
				}
			}
			if i < workers {
				// The first items wait for each other: they must run at the same time.
				started.Done()
				select {
				case <-allStarted:
					if i == 0 {
						reached = true
					}
				case <-time.After(5 * time.Second):
				}
			}
			time.Sleep(time.Millisecond)
		})
		if err != nil {
			t.Fatal(err)
		}
		if got := peak.Load(); got > int32(workers) {
			t.Errorf("workers %d: %d items ran at the same time", workers, got)
		}
		if !reached {
			t.Errorf("workers %d: the first %d items did not run at the same time", workers, workers)
		}
	}
}

// No more goroutines than items are started, however many workers are allowed.
func TestForEachParallelGoroutinesBoundedByItems(t *testing.T) {
	before := runtime.NumGoroutine()
	var during int
	var once sync.Once
	_ = ForEachParallel(context.Background(), []int{1, 2}, Parallel{Workers: 1000}, func(int, int) {
		once.Do(func() { during = runtime.NumGoroutine() })
	})
	// 2 workers at most; a margin for goroutines of the runtime and earlier tests.
	if during-before > 20 {
		t.Errorf("%d goroutines while running 2 items (%d before)", during, before)
	}
}

// A cancel stops handing out items; the item running finishes, and the ctx error is
// returned.
func TestForEachParallelCancel(t *testing.T) {
	// Repeated: the hand-out may race with the cancel, and either way no item may start.
	t.Run("before", func(t *testing.T) {
		for range 20 {
			ctx, cancel := context.WithCancel(context.Background())
			cancel()
			var ran atomic.Int32
			err := ForEachParallel(ctx, make([]int, 5), Parallel{Workers: 2, Progress: func(int, int) { t.Error("progress for an item that did not run") }}, func(int, int) { ran.Add(1) })
			if !errors.Is(err, context.Canceled) || ran.Load() != 0 {
				t.Fatalf("err = %v, %d items ran", err, ran.Load())
			}
		}
	})
	t.Run("one worker", func(t *testing.T) {
		for range 20 {
			ctx, cancel := context.WithCancel(context.Background())
			var ran []int
			err := ForEachParallel(ctx, make([]int, 10), Parallel{Workers: 1}, func(i int, _ int) {
				ran = append(ran, i)
				if i == 3 {
					cancel()
				}
			})
			cancel()
			if !errors.Is(err, context.Canceled) {
				t.Fatalf("err = %v", err)
			}
			if fmt.Sprint(ran) != "[0 1 2 3]" {
				t.Fatalf("ran %v, want [0 1 2 3]", ran)
			}
		}
	})
	t.Run("running items finish", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		var finished, ran atomic.Int32
		slowStarted := make(chan struct{})
		err := ForEachParallel(ctx, make([]int, 50), Parallel{Workers: 2}, func(i int, _ int) {
			ran.Add(1)
			switch i {
			case 0:
				close(slowStarted)
				time.Sleep(50 * time.Millisecond) // still running when the cancel comes
				finished.Add(1)
			case 1:
				<-slowStarted
				cancel()
			}
		})
		if !errors.Is(err, context.Canceled) {
			t.Errorf("err = %v", err)
		}
		if finished.Load() != 1 {
			t.Error("returned before the running item finished")
		}
		if ran.Load() != 2 {
			t.Errorf("%d items ran, want 2", ran.Load())
		}
	})
	t.Run("cancel after the last item", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		err := ForEachParallel(ctx, make([]int, 3), Parallel{Workers: 1}, func(i int, _ int) {
			if i == 2 {
				cancel()
			}
		})
		if err != nil {
			t.Errorf("every item ran, err = %v", err)
		}
	})
	t.Run("nil ctx", func(t *testing.T) {
		ran := 0
		if err := ForEachParallel(nil, make([]int, 3), Parallel{}, func(int, int) { ran++ }); err != nil || ran != 3 {
			t.Errorf("err = %v, ran %d", err, ran)
		}
	})
}

// A panic in one item is recovered, logged with the index and the stack, and reported;
// the other items still run.
func TestForEachParallelPanic(t *testing.T) {
	hook := logtest.NewGlobal()
	t.Cleanup(hook.Reset)
	var mu sync.Mutex
	var panicked []int
	var values []any
	var ran atomic.Int32
	var dones []int
	opt := Parallel{
		Workers: 3,
		OnPanic: func(i int, p any) {
			mu.Lock()
			defer mu.Unlock()
			panicked = append(panicked, i)
			values = append(values, p)
		},
		Progress: func(done, total int) { dones = append(dones, done) },
	}
	err := ForEachParallel(context.Background(), make([]int, 10), opt, func(i int, _ int) {
		if i == 4 {
			panic("boom")
		}
		ran.Add(1)
	})
	if err != nil {
		t.Errorf("err = %v", err)
	}
	if fmt.Sprint(panicked) != "[4]" || fmt.Sprint(values) != "[boom]" {
		t.Errorf("OnPanic calls: %v %v", panicked, values)
	}
	if ran.Load() != 9 {
		t.Errorf("%d other items ran, want 9", ran.Load())
	}
	if len(dones) != 10 {
		t.Errorf("progress calls %v, want one per item, the panicked one too", dones)
	}
	var logged []string
	for _, e := range hook.AllEntries() {
		if e.Level == log.ErrorLevel {
			logged = append(logged, e.Message)
		}
	}
	if len(logged) != 1 || !strings.Contains(logged[0], "item 4 of 10") || !strings.Contains(logged[0], "boom") ||
		!strings.Contains(logged[0], "parallel_test.go") {
		t.Errorf("logged %q, want one error with the index, the value and the stack", logged)
	}

	// Without OnPanic the panic is still recovered and logged.
	hook.Reset()
	if err := ForEachParallel(context.Background(), []int{1}, Parallel{}, func(int, int) { panic(errors.New("bad")) }); err != nil {
		t.Errorf("err = %v", err)
	}
	if len(hook.AllEntries()) != 1 {
		t.Errorf("logged %d entries", len(hook.AllEntries()))
	}
}

// Progress counts from 1 to the total, one serialized call per item.
func TestForEachParallelProgress(t *testing.T) {
	var calls []int
	var inProgress atomic.Int32
	err := ForEachParallel(context.Background(), make([]int, 40), Parallel{Workers: 8, Progress: func(done, total int) {
		if inProgress.Add(1) != 1 {
			t.Error("progress called concurrently")
		}
		defer inProgress.Add(-1)
		if total != 40 {
			t.Errorf("total = %d", total)
		}
		calls = append(calls, done)
		time.Sleep(100 * time.Microsecond)
	}}, func(int, int) {})
	if err != nil {
		t.Fatal(err)
	}
	if len(calls) != 40 {
		t.Fatalf("%d progress calls, want 40", len(calls))
	}
	for i, d := range calls {
		if d != i+1 {
			t.Fatalf("progress calls %v, want 1..40 in order", calls)
		}
	}
}
