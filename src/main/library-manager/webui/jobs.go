package webui

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	log "github.com/sirupsen/logrus"

	"github.com/nkreimeyer/library-manager/fsops"
)

// Job states.
const (
	stateRunning   = "running"
	stateDone      = "done"
	stateFailed    = "failed"
	stateCancelled = "cancelled"
)

// maxLogLines is how many log lines of a job are kept (the newest).
var maxLogLines = 20000

// Job is the state of a job as the UI sees it.
type Job struct {
	ID      string      `json:"id"`
	Kind    string      `json:"kind"`
	State   string      `json:"state"`
	Phase   string      `json:"phase,omitempty"`
	Done    int         `json:"done"`
	Total   int         `json:"total"`
	Stats   fsops.Stats `json:"stats"`
	DryRun  bool        `json:"dryRun"`
	Error   string      `json:"error,omitempty"`
	Details []string    `json:"details,omitempty"`
	// Result is the job's outcome (kind-specific), set when it ends without error and for
	// some errors (a changed plan).
	Result  any       `json:"result,omitempty"`
	Started time.Time `json:"started"`
	Ended   time.Time `json:"ended,omitzero"`
	// Logs is the number of log lines so far (the next line's N).
	Logs int `json:"logs"`
}

// LogLine is one log entry of a job.
type LogLine struct {
	N     int       `json:"n"`
	Time  time.Time `json:"time"`
	Level string    `json:"level"`
	Msg   string    `json:"msg"`
}

// detailedError is an error with extra lines for the UI and, optionally, a result.
type detailedError struct {
	msg     string
	details []string
	result  any
}

func (e *detailedError) Error() string { return e.msg }

type job struct {
	Job
	ops     *fsops.Ops
	cancel  context.CancelFunc
	logs    []LogLine
	logBase int // N of logs[0]
	version int // incremented on every change
}

// jobControl is what a job function gets to report its progress.
type jobControl struct {
	r *jobRunner
	j *job
}

// Progress records the phase and the number of items done.
func (c jobControl) Progress(phase string, done, total int) {
	c.r.mu.Lock()
	defer c.r.mu.Unlock()
	if c.j.Phase != phase || done > c.j.Done || total != c.j.Total {
		c.j.Phase, c.j.Done, c.j.Total = phase, done, total
		c.j.version++
	}
}

type jobRunner struct {
	mu   sync.Mutex
	cur  *job // the running or last finished job
	wg   sync.WaitGroup
	hook *logHook
}

func newJobRunner() *jobRunner {
	r := &jobRunner{}
	r.hook = &logHook{r: r}
	log.AddHook(r.hook)
	return r
}

// start runs fn as the new job, unless one is running (errBusy).
func (r *jobRunner) start(id, kind string, ops *fsops.Ops, fn func(ctx context.Context, c jobControl) (any, error)) (Job, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.cur != nil && r.cur.State == stateRunning {
		return Job{}, errBusy
	}
	ctx, cancel := context.WithCancel(context.Background())
	j := &job{Job: Job{ID: id, Kind: kind, State: stateRunning, DryRun: ops.DryRun, Started: time.Now()}, ops: ops, cancel: cancel}
	r.cur = j
	r.wg.Add(1)
	go func() {
		defer r.wg.Done()
		defer cancel()
		res, err := runSafely(ctx, jobControl{r, j}, fn)
		r.mu.Lock()
		defer r.mu.Unlock()
		j.Ended = time.Now()
		j.Stats = ops.Stats()
		j.Result = res
		var de *detailedError
		switch {
		case err == nil:
			j.State = stateDone
		case errors.Is(err, context.Canceled):
			j.State, j.Error = stateCancelled, "stopped"
		default:
			j.State, j.Error = stateFailed, err.Error()
			if errors.As(err, &de) {
				j.Details = de.details
				if de.result != nil {
					j.Result = de.result
				}
			}
		}
		j.version++
	}()
	return j.snapshot(), nil
}

// startJob starts fn as the new job and answers 202 with the job, or 409 with errBusy's
// message while another job runs. Every handler that starts a job answers this way.
// It reports whether the job started.
func (s *Server) startJob(w http.ResponseWriter, id, kind string, ops *fsops.Ops, fn func(ctx context.Context, c jobControl) (any, error)) bool {
	job, err := s.jobs.start(id, kind, ops, fn)
	if err != nil {
		writeError(w, http.StatusConflict, err)
		return false
	}
	writeJSON(w, http.StatusAccepted, job)
	return true
}

// runSafely turns a panic in the job's own goroutine into a job error, so the UI keeps
// running. The modules' worker pools (utils.ForEachParallel) recover a panic per song
// themselves: it is logged, counted as a failure in the job's stats and the job goes on.
// A panic in any other goroutine is not caught here and still ends the program.
func runSafely(ctx context.Context, c jobControl, fn func(context.Context, jobControl) (any, error)) (res any, err error) {
	defer func() {
		if p := recover(); p != nil {
			log.Errorf("webui: job %s panicked: %v", c.j.ID, p)
			res, err = nil, fmt.Errorf("internal error: %v", p)
		}
	}()
	return fn(ctx, c)
}

// snapshot copies the public state; the caller holds r.mu.
func (j *job) snapshot() Job {
	s := j.Job
	if j.ops != nil && j.State == stateRunning {
		s.Stats = j.ops.Stats()
	}
	s.Logs = j.logBase + len(j.logs)
	return s
}

// current returns the current job's state, its version and the log lines from n on.
func (r *jobRunner) current(n int) (Job, int, []LogLine, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.cur == nil {
		return Job{}, 0, nil, false
	}
	j := r.cur
	var lines []LogLine
	if i := max(0, n-j.logBase); i < len(j.logs) {
		lines = append(lines, j.logs[i:]...)
	}
	return j.snapshot(), j.version, lines, true
}

// cancel stops the running job; false if none runs.
func (r *jobRunner) cancelRunning() bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.cur == nil || r.cur.State != stateRunning {
		return false
	}
	r.cur.cancel()
	return true
}

func (r *jobRunner) close() {
	r.cancelRunning()
	r.wg.Wait()
}

// logHook collects the log entries of the running job.
type logHook struct{ r *jobRunner }

func (h *logHook) Levels() []log.Level { return log.AllLevels }

func (h *logHook) Fire(e *log.Entry) error {
	h.r.mu.Lock()
	defer h.r.mu.Unlock()
	j := h.r.cur
	if j == nil || j.State != stateRunning {
		return nil
	}
	j.logs = append(j.logs, LogLine{N: j.logBase + len(j.logs), Time: e.Time, Level: e.Level.String(), Msg: e.Message})
	if len(j.logs) > maxLogLines {
		drop := len(j.logs) - maxLogLines
		j.logs = append(j.logs[:0:0], j.logs[drop:]...)
		j.logBase += drop
	}
	j.version++
	return nil
}

func (s *Server) handleJob(w http.ResponseWriter, r *http.Request) {
	j, _, _, ok := s.jobs.current(0)
	if !ok {
		writeJSON(w, http.StatusOK, nil)
		return
	}
	writeJSON(w, http.StatusOK, j)
}

func (s *Server) handleJobCancel(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]bool{"cancelled": s.jobs.cancelRunning()})
}

// eventInterval is how often the event stream looks for changes; keepAliveInterval is
// the longest it stays silent.
var (
	eventInterval     = 250 * time.Millisecond
	keepAliveInterval = 15 * time.Second
)

// handleJobEvents streams the current job as Server-Sent Events: "job" (its state, when
// it changed) and "log" (new log lines, from the query parameter "log" on). Each "log" event
// has the id "<job>/<next line>": a browser that reconnects sends it back (Last-Event-ID)
// and gets only the lines it does not have. The stream stays open across jobs until the
// client goes away or the server closes.
func (s *Server) handleJobEvents(w http.ResponseWriter, r *http.Request) {
	fl, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "streaming not supported", http.StatusInternalServerError)
		return
	}
	s.presence.opened() // a page is there as long as this stream is open
	defer s.presence.closed()
	next, _ := strconv.Atoi(r.URL.Query().Get("log"))
	logJob := "" // the job next counts the lines of ("" = the current one)
	if id, n, ok := strings.Cut(r.Header.Get("Last-Event-ID"), "/"); ok {
		if v, err := strconv.Atoi(n); err == nil && v >= 0 {
			logJob, next = id, v
		}
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(http.StatusOK)
	fl.Flush()
	lastID, lastVersion := "", -1 // the job state sent last
	tick := time.NewTicker(eventInterval)
	defer tick.Stop()
	lastSent := time.Now()
	closing := false
	for {
		j, version, lines, ok := s.jobs.current(next)
		if ok && (j.ID != lastID || version != lastVersion) {
			if logJob != "" && j.ID != logJob {
				next = 0 // another job: its log starts at 0
				j, version, lines, _ = s.jobs.current(0)
			}
			logJob, lastID, lastVersion = j.ID, j.ID, version
			if err := sendEvent(w, "job", "", j); err != nil {
				return
			}
			if len(lines) > 0 {
				next = lines[len(lines)-1].N + 1
				if err := sendEvent(w, "log", fmt.Sprintf("%s/%d", j.ID, next), lines); err != nil {
					return
				}
			}
			fl.Flush()
			lastSent = time.Now()
		} else if time.Since(lastSent) >= keepAliveInterval {
			if _, err := fmt.Fprint(w, ": keep-alive\n\n"); err != nil {
				return
			}
			fl.Flush()
			lastSent = time.Now()
		}
		if closing {
			return // the final state went out above
		}
		select {
		case <-r.Context().Done():
			return
		case <-s.closed:
			closing = true // one more pass sends the stopped job's final state
		case <-tick.C:
		}
	}
}

// sendEvent writes one event; id "" sends none (the browser keeps the last one).
func sendEvent(w http.ResponseWriter, event, id string, v any) error {
	data, err := json.Marshal(v)
	if err != nil {
		return err
	}
	if id != "" {
		if _, err := fmt.Fprintf(w, "id: %s\n", id); err != nil {
			return err
		}
	}
	_, err = fmt.Fprintf(w, "event: %s\ndata: %s\n\n", event, data)
	return err
}
