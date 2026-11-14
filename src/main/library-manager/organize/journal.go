package organize

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"sync/atomic"
	"time"

	log "github.com/sirupsen/logrus"

	"github.com/nkreimeyer/library-manager/fsops"
)

// The move journal (<out>/organize-journal.jsonl) is a JSON Lines file. Every run that
// moves something appends
//
//	{"op":"run","run":"<id>","time":"<RFC3339>"}            once, before its first move
//	{"op":"mkdir","run":"<id>","dst":"<abs>"}                 before a move into the library that
//	                                                          creates this folder (outermost first)
//	{"op":"move","run":"<id>","src":"<abs>","dst":"<abs>"}   after every completed move
//
// and an undo of that run appends {"op":"undone","run":"<id>"}. Undo walks the entries
// backwards: it moves every dst back to its src and removes the folders of "mkdir"
// entries again if they are empty, so the library is left exactly as before the run. Each line is fsynced
// before the next move starts, so after a crash the journal lists every completed move.
// A crash can truncate the last line; readers skip lines they cannot parse.

// Journal operations.
const (
	opRun    = "run"
	opMove   = "move"
	opMkdir  = "mkdir"
	opUndone = "undone"
)

type journalEntry struct {
	Op   string `json:"op"`
	Run  string `json:"run"`
	Time string `json:"time,omitempty"`
	Src  string `json:"src,omitempty"`
	Dst  string `json:"dst,omitempty"`
}

// runCounter makes run ids unique within a process even if the clock does not advance.
var runCounter atomic.Int64

// newRunID returns a unique, time-ordered run id.
func newRunID(now time.Time) string {
	return fmt.Sprintf("%s-%d-%d", now.UTC().Format("20060102T150405.000000000Z"), os.Getpid(), runCounter.Add(1))
}

// journal appends the moves of one run. It writes the run line lazily before the first
// move, so runs that move nothing leave no trace. Nothing is written in dry runs
// (fsops.AppendJournal is a no-op there).
type journal struct {
	ops     *fsops.Ops
	path    string
	run     string
	started bool
	failed  bool // an append failed: further moves must not run
	// write appends one line (ops.AppendJournal; tests inject failures).
	write func(path string, line []byte) error
}

func newJournal(ops *fsops.Ops, path string) *journal {
	return &journal{ops: ops, path: path, run: newRunID(time.Now()), write: ops.AppendJournal}
}

// begin writes the run line if it was not written yet. It must succeed before the first
// move, otherwise that move could not be undone.
func (j *journal) begin() error {
	if j.started {
		return nil
	}
	if err := j.append(journalEntry{Op: opRun, Run: j.run, Time: time.Now().UTC().Format(time.RFC3339)}); err != nil {
		return err
	}
	j.started = true
	return nil
}

// moved records a completed move.
func (j *journal) moved(src, dst string) error {
	return j.append(journalEntry{Op: opMove, Run: j.run, Src: src, Dst: dst})
}

func (j *journal) append(e journalEntry) error {
	line, err := json.Marshal(e)
	if err != nil {
		j.ops.Fail()
		j.failed = true
		return err
	}
	write := j.write
	if write == nil {
		write = j.ops.AppendJournal
	}
	if err := write(j.path, line); err != nil {
		j.failed = true // counted by ops
		return err
	}
	return nil
}

// mkdir records that the next move creates the folder dir.
func (j *journal) mkdir(dir string) error {
	return j.append(journalEntry{Op: opMkdir, Run: j.run, Dst: dir})
}

// journalRun is one run of the journal with its move and mkdir entries in the order
// they were written.
type journalRun struct {
	ID      string
	Time    string
	Entries []journalEntry
	Undone  bool
}

// moves counts the move entries of the run.
func (r *journalRun) moves() int {
	n := 0
	for _, e := range r.Entries {
		if e.Op == opMove {
			n++
		}
	}
	return n
}

// readJournal parses the journal. A missing journal is empty. Lines that cannot be
// parsed (a line truncated by a crash) are skipped with a warning; moves of a run whose
// run line is missing are attributed to that run anyway.
func readJournal(path string) ([]*journalRun, error) {
	data, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var runs []*journalRun
	byID := map[string]*journalRun{}
	get := func(id string) *journalRun {
		r := byID[id]
		if r == nil {
			r = &journalRun{ID: id}
			byID[id] = r
			runs = append(runs, r)
		}
		return r
	}
	sc := bufio.NewScanner(bytes.NewReader(data))
	sc.Buffer(make([]byte, 64*1024), 16*1024*1024)
	for n := 1; sc.Scan(); n++ {
		line := bytes.TrimSpace(sc.Bytes())
		if len(line) == 0 {
			continue
		}
		var e journalEntry
		if err := json.Unmarshal(line, &e); err != nil || e.Run == "" {
			log.Warnf("Journal %s line %d cannot be parsed, skipping it: %q", path, n, line)
			continue
		}
		switch e.Op {
		case opRun:
			get(e.Run).Time = e.Time
		case opMove:
			if e.Src == "" || e.Dst == "" {
				log.Warnf("Journal %s line %d: move without src/dst, skipping it", path, n)
				continue
			}
			r := get(e.Run)
			r.Entries = append(r.Entries, e)
		case opMkdir:
			if e.Dst == "" {
				log.Warnf("Journal %s line %d: mkdir without dst, skipping it", path, n)
				continue
			}
			r := get(e.Run)
			r.Entries = append(r.Entries, e)
		case opUndone:
			get(e.Run).Undone = true
		default:
			log.Warnf("Journal %s line %d: unknown op %q, skipping it", path, n, e.Op)
		}
	}
	if err := sc.Err(); err != nil {
		return nil, err
	}
	return runs, nil
}

// lastUndoable returns the most recent run that has moves and was not undone yet.
func lastUndoable(runs []*journalRun) *journalRun {
	for i := len(runs) - 1; i >= 0; i-- {
		if !runs[i].Undone && runs[i].moves() > 0 {
			return runs[i]
		}
	}
	return nil
}
