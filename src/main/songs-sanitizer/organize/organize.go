package organize

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"sync"

	log "github.com/sirupsen/logrus"

	"github.com/nkreimeyer/ultrastar-songs-sanitizer/check"
	"github.com/nkreimeyer/ultrastar-songs-sanitizer/config"
	"github.com/nkreimeyer/ultrastar-songs-sanitizer/fsops"
	"github.com/nkreimeyer/ultrastar-songs-sanitizer/songtxt"
	"github.com/nkreimeyer/ultrastar-songs-sanitizer/utils"
)

// Options configure Run.
type Options struct {
	// Priority: the tier of the source (the -s flag it was given with) decides first
	// between working versions of a song.
	Priority bool
	// IgnoreThe: bucket "The Beatles" under B (the folder name is unchanged).
	IgnoreThe bool
	// Deep: fully decode the media files while checking (check -deep).
	Deep bool
}

// workers is the number of song folders inspected at the same time. It is a variable so
// tests can force concurrency on machines with a single CPU.
var workers = min(16, runtime.NumCPU())

// noteNoMediaQuality is added to the report when ffprobe is missing.
const noteNoMediaQuality = "without ffprobe the video and audio bitrate of duplicates are unknown; the keeper is chosen by priority, txt warnings, cover, background and #VERSION only"

// tempPrefix starts the hidden temporary names used to break move cycles in the library.
// Hidden folders are skipped by the song discovery; -undo moves such a song back.
const tempPrefix = ".organize-tmp-"

// Run builds the library from the sources: it discovers and inspects every song folder,
// plans (MakePlan) and executes the moves, and writes organize_plan.json/.txt. Every
// completed move is journaled (not in dry runs), so Undo can revert the run. It returns
// an error only if a source cannot be listed (before anything is moved), the journal
// cannot be written or a report cannot be written; failed moves are counted in ops.
func Run(cfg config.OrganizeConfig, ops *fsops.Ops, opts Options) error {
	if len(cfg.Sources) == 0 || cfg.Library == "" {
		return errors.New("organize needs at least one source and a library")
	}
	type item struct {
		src    config.Source
		folder string
	}
	var items []item
	var empty []string
	for _, src := range cfg.Sources {
		folders, err := utils.GetAllSongFolders(ops, src.Path)
		if err != nil {
			// Without all sources the deduplication would pick the wrong keepers.
			return fmt.Errorf("finding song folders in %q: %w", src.Path, err)
		}
		for _, f := range folders {
			if isEmptyTree(f) {
				empty = append(empty, src.Label+":"+filepath.ToSlash(cfg.RelPath(src, f)))
				continue
			}
			items = append(items, item{src, f})
		}
	}

	insp, notes := check.NewInspector(ops, opts.Deep)
	if !insp.MediaChecked() {
		notes = append(notes, noteNoMediaQuality)
	}
	cands := make([]Candidate, len(items))
	forEachParallel(len(items), workers, func(i int) {
		in, err := insp.Inspect(items[i].folder) // an error is logged and counted by Inspect
		cands[i] = newCandidate(cfg, items[i].src, items[i].folder, in, err)
	})
	log.Infof("Organize: %d song folder(s) in %d source(s), %d empty folder(s) ignored", len(cands), len(cfg.Sources), len(empty))

	plan := MakePlan(cands, PlanOptions{Priority: opts.Priority, IgnoreThe: opts.IgnoreThe, Occupied: libraryOccupant(ops, cfg.Library)})
	logPlan(cands, plan)

	in := reportInput{cfg: cfg, opts: opts, cands: cands, plan: plan, outcomes: map[int]moveOutcome{}, empty: empty, notes: notes}
	if !cfg.DryRun {
		// A crash during the moves leaves the plan behind (the journal has the moves).
		for i, d := range plan.Decisions {
			if movesSong(d.Action) {
				in.outcomes[i] = moveOutcome{Status: outcomePending}
			}
		}
		if err := writeReports(cfg, ops, buildReport(in)); err != nil {
			return err
		}
	}

	a := newApplier(cfg, ops, cands, in.outcomes)
	a.apply(plan)
	if !cfg.DryRun {
		in.emptyLeft = emptiedCategories(cfg, cands, in.outcomes)
	}
	rep := buildReport(in)
	if err := writeReports(cfg, ops, rep); err != nil {
		return err
	}
	verb := "moved"
	if cfg.DryRun {
		verb = "planned"
	}
	log.Infof("Organize: %d to the library, %d already in place, %d duplicate(s), %d invalid, %d left in place, %d possible duplicate(s); %d move(s) %s, %d failed; see %s",
		rep.Summary.ToLibrary, rep.Summary.AlreadyInPlace, rep.Summary.Duplicates, rep.Summary.Invalid, rep.Summary.LeftInPlace,
		rep.Summary.PossibleDuplicates, rep.Summary.Moved, verb, rep.Summary.Failed, cfg.ReportPath(reportTxt))
	if a.j.failed {
		return fmt.Errorf("the journal %s cannot be written; the remaining moves were not attempted", cfg.JournalPath())
	}
	return nil
}

func movesSong(a Action) bool { return a == ActMove || a == ActDuplicate || a == ActInvalid }

func writeReports(cfg config.OrganizeConfig, ops *fsops.Ops, rep planReport) error {
	data, err := rep.json()
	if err != nil {
		return fmt.Errorf("encoding organize report: %w", err)
	}
	if err := ops.WriteReport(cfg.ReportPath(reportJSON), data); err != nil {
		return err
	}
	return ops.WriteReport(cfg.ReportPath(reportTxt), rep.text())
}

// newCandidate builds the candidate of one song folder from its inspection (err: the
// folder or txt could not be read).
func newCandidate(cfg config.OrganizeConfig, src config.Source, folder string, in check.Inspection, err error) Candidate {
	c := Candidate{
		Source: src.Index, Tier: src.Tier, Label: src.Label, Folder: folder,
		Rel:       filepath.ToSlash(cfg.RelPath(src, folder)),
		InLibrary: src.Path == cfg.Library,
	}
	if err != nil {
		c.Unreadable = true
		return c
	}
	c.EncodingProblem, c.Broken = in.EncodingProblem, in.Broken
	seen := map[string]bool{}
	for _, f := range in.Findings {
		if f.Severity == check.SevError && !seen[f.Code] {
			seen[f.Code] = true
			c.Errors = append(c.Errors, f.Code)
		}
	}
	sort.Strings(c.Errors)
	if in.Song != nil {
		c.Artist, c.Title = lastTag(in.Song, "ARTIST"), lastTag(in.Song, "TITLE")
		c.Duet = len(in.Song.Players) > 0 || IsDuetTitle(c.Title)
		c.Quality.HasVersion = lastTag(in.Song, "VERSION") != ""
	}
	m := in.Media
	c.Quality.SongWarnings = check.SongWarnings(in.Findings)
	c.Quality.HasVideo, c.Quality.VideoHeight = m.HasVideo, m.VideoHeight
	c.Quality.AudioBitrate = m.AudioBitrate
	c.Quality.HasCover, c.Quality.HasBackground = m.HasCover, m.HasBackground
	return c
}

// lastTag returns the value of the last occurrence of the tag, the one players use.
func lastTag(s *songtxt.Song, key string) string {
	v := ""
	for _, t := range s.Tags {
		if strings.EqualFold(t.Key, key) {
			v = t.Value
		}
	}
	return v
}

// isEmptyTree reports whether dir contains no visible file at any depth (only folders
// and hidden entries): a leftover category or an empty song folder. Errors count as
// "not empty", so the folder is inspected (and reported) instead.
func isEmptyTree(dir string) bool {
	empty := true
	err := filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if p == dir {
			return nil
		}
		if utils.IsHidden(d.Name()) {
			if d.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		if !d.IsDir() {
			empty = false
			return filepath.SkipAll
		}
		return nil
	})
	return err == nil && empty
}

// libraryOccupant returns the PlanOptions.Occupied function for the library as it is on
// disk: a target is taken by an entry of its bucket folder with the same NameKey (as a
// case-insensitive file system sees it), or by a bucket that cannot take songs (see
// listBucket).
func libraryOccupant(ops *fsops.Ops, lib string) func(rel string) (string, string, bool) {
	type listing struct {
		names map[string]string // NameKey(name) -> name; nil = the bucket is taken
		note  string
	}
	var mu sync.Mutex
	listings := map[string]listing{}
	return func(rel string) (string, string, bool) {
		mu.Lock()
		defer mu.Unlock()
		bucket, name := path.Split(rel)
		bucket = strings.TrimSuffix(bucket, "/")
		l, ok := listings[bucket]
		if !ok {
			l.names, l.note = listBucket(ops, filepath.Join(lib, filepath.FromSlash(bucket)), bucket)
			listings[bucket] = l
		}
		if l.names == nil {
			return bucket, l.note, true
		}
		if n, ok := l.names[NameKey(name)]; ok {
			return bucket + "/" + n, "", true
		}
		return "", "", false
	}
}

// listBucket returns the entries of a bucket folder by NameKey; an empty map if it does
// not exist. It returns nil (nothing may go there) if the bucket is not a folder, is a song
// folder or holds media itself (bucketProblem), or cannot be read; note then says why
// (empty for a file). An unreadable bucket is logged and counted (ops.FailOnce).
func listBucket(ops *fsops.Ops, dir, bucket string) (map[string]string, string) {
	st, err := os.Lstat(dir)
	if errors.Is(err, fs.ErrNotExist) {
		return map[string]string{}, ""
	}
	if err == nil && !st.IsDir() {
		return nil, ""
	}
	var entries []os.DirEntry
	if err == nil {
		entries, err = os.ReadDir(dir)
	}
	if err != nil {
		log.Errorf("Reading library folder %s: %v; no song is moved into it", dir, err)
		ops.FailOnce("unreadable:" + dir)
		return nil, fmt.Sprintf("the bucket folder %s cannot be read", bucket)
	}
	if why := bucketProblem(ops, dir, entries); why != "" {
		log.Warnf("Library folder %s %s; no song is moved into it", dir, why)
		return nil, fmt.Sprintf("the bucket folder %s %s; move it away or add the library as a source (-s)", bucket, why)
	}
	names := map[string]string{}
	for _, e := range entries {
		if k := NameKey(e.Name()); names[k] == "" {
			names[k] = e.Name()
		}
	}
	return names, ""
}

// bucketProblem reports why the bucket folder dir (with its entries) cannot take songs: it
// is a song folder itself (it contains a song txt), or it holds audio or video files (a
// broken song folder). A song moved into it would be hidden inside that song. Hidden
// entries and, in a dry run, entries planned to move away are ignored. "" = it is fine.
func bucketProblem(ops *fsops.Ops, dir string, entries []os.DirEntry) string {
	media := ""
	for _, e := range entries {
		name, p := e.Name(), filepath.Join(dir, e.Name())
		if utils.IsHidden(name) || e.IsDir() || ops.WouldBeMoved(p) {
			continue
		}
		switch {
		case utils.IsTxtFile(name) && utils.IsSongTxt(ops, p):
			return fmt.Sprintf("is a song folder (it contains the song txt %s)", name)
		case media == "" && (utils.IsAudioFile(name) || utils.IsVideoFile(name)):
			media = name
		}
	}
	if media != "" {
		return fmt.Sprintf("holds the media file %s", media)
	}
	return ""
}

// bucketTaken checks the bucket folder of a library move right before it runs: an earlier
// step that should have moved a song folder named like the bucket away may have failed.
// A bucket found fine stays fine (the run only moves folders into it), so it is checked
// once.
func (a *applier) bucketTaken(dst string) string {
	dir := filepath.Dir(dst)
	if a.freeBuckets[dir] {
		return ""
	}
	entries, err := os.ReadDir(dir)
	if errors.Is(err, fs.ErrNotExist) {
		return ""
	}
	if err != nil {
		return fmt.Sprintf("the bucket folder %s cannot be read: %v", dir, err)
	}
	if why := bucketProblem(a.ops, dir, entries); why != "" {
		return fmt.Sprintf("the bucket folder %s %s", dir, why)
	}
	a.freeBuckets[dir] = true
	return ""
}

func logPlan(cands []Candidate, p Plan) {
	for i, d := range p.Decisions {
		c := &cands[i]
		switch d.Action {
		case ActMove:
			log.Debugf("Plan: %s -> library %s", c.Ref(), d.Target)
		case ActInPlace:
			log.Debugf("Plan: %s already in place", c.Ref())
		case ActDuplicate:
			log.Infof("Duplicate: %s loses to %s: %s", c.Ref(), cands[d.Other].Ref(), d.Reason)
		case ActInvalid:
			log.Infof("Broken: %s (%s)", c.Ref(), d.Reason)
		case ActLeft:
			log.Warnf("Leaving %s in place: %s", c.Ref(), d.Reason)
		}
	}
}

// applier executes a plan.
type applier struct {
	cfg      config.OrganizeConfig
	ops      *fsops.Ops
	cands    []Candidate
	j        *journal
	outcomes map[int]moveOutcome
	// moveTo performs the library moves (ops.MoveTo; tests inject failures).
	moveTo func(src, dst string) error
	// freeBuckets are the bucket folders found able to take songs (bucketTaken).
	freeBuckets map[string]bool
}

func newApplier(cfg config.OrganizeConfig, ops *fsops.Ops, cands []Candidate, outcomes map[int]moveOutcome) *applier {
	return &applier{cfg: cfg, ops: ops, cands: cands, j: newJournal(ops, cfg.JournalPath()), outcomes: outcomes, moveTo: ops.MoveTo, freeBuckets: map[string]bool{}}
}

func (a *applier) done(i int, dst string) {
	status := outcomeMoved
	if a.cfg.DryRun {
		status = outcomePlanned
	}
	a.outcomes[i] = moveOutcome{Status: status, Dst: dst}
}

// apply moves the broken songs and duplicates to the output dir first (which frees
// their place in a library that is also a source), then the keepers into the library in
// an order in which every target is free (orderMoves).
func (a *applier) apply(p Plan) {
	var out []int
	var moves []libMove
	for i, d := range p.Decisions {
		switch d.Action {
		case ActInvalid, ActDuplicate:
			out = append(out, i)
		case ActMove:
			moves = append(moves, libMove{Cand: i, Src: a.cands[i].Folder, Dst: filepath.Join(a.cfg.Library, filepath.FromSlash(d.Target))})
		}
	}
	sort.SliceStable(out, func(x, y int) bool {
		dx, dy := p.Decisions[out[x]].Action, p.Decisions[out[y]].Action
		if dx != dy {
			return dx == ActInvalid
		}
		return a.cands[out[x]].Ref() < a.cands[out[y]].Ref()
	})
	for _, i := range out {
		if !a.ready(i) {
			continue
		}
		c := &a.cands[i]
		dir := a.cfg.DuplicatesDir()
		if p.Decisions[i].Action == ActInvalid {
			dir = a.cfg.InvalidDir()
		}
		dst, err := a.ops.MoveInto(c.Folder, dir, filepath.Join(c.Label, filepath.FromSlash(c.Rel)))
		if err != nil {
			log.Errorf("Moving %s: %v", c.Ref(), err) // counted by ops
			a.outcomes[i] = moveOutcome{Status: outcomeFailed}
			continue
		}
		a.done(i, dst)
		a.journalMove(c.Folder, dst)
	}

	steps := orderMoves(moves, func(n int) string {
		return filepath.Join(a.cfg.Library, fmt.Sprintf("%s%s-%d", tempPrefix, a.j.run, n))
	})
	parked := map[int]bool{} // the song is at its temporary name (a ToTemp step ran)
	for _, s := range steps {
		if s.ViaTemp && !parked[s.Cand] {
			continue // its move to the temporary name failed or was undone
		}
		if !s.ToTemp {
			if why := a.bucketTaken(s.Dst); why != "" {
				log.Errorf("Not moving %s into the library: %s", a.cands[s.Cand].Ref(), why)
				a.ops.Fail()
				a.outcomes[s.Cand] = moveOutcome{Status: outcomeFailed}
				if s.ViaTemp {
					a.stayParked(s)
				}
				continue
			}
		}
		if !a.ready(s.Cand) || !a.journalMkdirs(s.Cand, s.Dst) {
			if s.ViaTemp {
				a.unpark(s) // never leave a song hidden under the temporary name
			}
			continue
		}
		if err := a.moveTo(s.Src, s.Dst); err != nil {
			log.Errorf("Moving %s into the library: %v", a.cands[s.Cand].Ref(), err) // counted by ops
			a.outcomes[s.Cand] = moveOutcome{Status: outcomeFailed}
			if s.ViaTemp {
				a.stayParked(s)
			}
			continue
		}
		// A failed journal append stops the following steps; a song parked here is then
		// moved out of its temporary name by its final step (unpark).
		a.journalMove(s.Src, s.Dst)
		if s.ToTemp {
			parked[s.Cand] = true
		} else {
			parked[s.Cand] = false
			a.done(s.Cand, s.Dst)
		}
	}
}

// ready writes the journal's run line before the first move and reports whether moves
// may run: never after a journal write failed (they could not be undone).
func (a *applier) ready(i int) bool {
	if !a.j.failed {
		if err := a.j.begin(); err == nil {
			return true
		}
		log.Errorf("Cannot write the journal %s; no further songs are moved", a.cfg.JournalPath())
	}
	a.outcomes[i] = moveOutcome{Status: outcomeSkipped}
	return false
}

// journalMkdirs records the folders the move to dst will create (MoveTo creates the
// missing parents), so -undo can remove them again. It reports whether the move may run.
func (a *applier) journalMkdirs(i int, dst string) bool {
	if a.cfg.DryRun {
		return true
	}
	for _, d := range missingDirs(filepath.Dir(dst)) {
		if err := a.j.mkdir(d); err != nil {
			log.Errorf("Cannot write the journal %s: %v; no further songs are moved", a.cfg.JournalPath(), err)
			a.outcomes[i] = moveOutcome{Status: outcomeSkipped}
			return false
		}
	}
	return true
}

// missingDirs returns dir and its parents that do not exist, outermost first.
func missingDirs(dir string) []string {
	var out []string
	for d := filepath.Clean(dir); ; d = filepath.Dir(d) {
		if _, err := os.Lstat(d); !errors.Is(err, fs.ErrNotExist) {
			break
		}
		out = append([]string{d}, out...)
		if filepath.Dir(d) == d {
			break
		}
	}
	return out
}

// journalMove records a completed move and reports whether that worked.
func (a *applier) journalMove(src, dst string) bool {
	if err := a.j.moved(src, dst); err != nil {
		log.Errorf("Cannot record the move %s -> %s in the journal %s: %v; no further songs are moved", src, dst, a.cfg.JournalPath(), err)
		return false
	}
	return true
}

// moveBack moves a song parked under a temporary name back to where it came from when
// its final move failed or is not attempted, and reports whether that worked. The park is
// in the journal (otherwise the song was moved back right away), so if this fails, -undo
// moves it back.
func (a *applier) moveBack(s step) bool {
	if err := a.moveTo(s.Src, s.Orig); err != nil {
		log.Errorf("Moving %s back to %s failed: %v", s.Src, s.Orig, err)
		return false
	}
	if !a.journalMove(s.Src, s.Orig) {
		log.Warnf("The move of %s back to %s is not in the journal (if the park is, -undo reports that move as failed; nothing else is affected)", s.Src, s.Orig)
	}
	return true
}

// stayParked moves a parked song back after its final move failed; if that fails too, it
// stays at the temporary name, and the journaled park lets -undo move it back.
func (a *applier) stayParked(s step) {
	if !a.moveBack(s) {
		log.Errorf("%s stays at the temporary name %s; -undo moves it back", a.cands[s.Cand].Ref(), s.Src)
	}
}

// unpark gets a parked song out of its temporary name when its final move is not
// attempted because the journal cannot be written: back to where it came from or, if
// that place is taken by now (the completed move into it is what could not be
// journaled), to its planned target. Either way it stays visible to the discovery.
func (a *applier) unpark(s step) {
	if a.moveBack(s) {
		return
	}
	if err := a.moveTo(s.Src, s.Dst); err != nil {
		log.Errorf("Moving %s to %s failed too: %v; the song stays at the temporary name, move it by hand", s.Src, s.Dst, err)
		return
	}
	log.Warnf("Moved %s to its planned target %s instead; this move is not in the journal", s.Src, s.Dst)
	a.done(s.Cand, s.Dst)
}

// emptiedCategories counts the category folders of the sources that contain no visible
// file any more after this run's moves. They are left alone.
func emptiedCategories(cfg config.OrganizeConfig, cands []Candidate, outcomes map[int]moveOutcome) int {
	roots := map[int]string{}
	for _, s := range cfg.Sources {
		roots[s.Index] = s.Path
	}
	seen := map[string]bool{}
	n := 0
	for i, o := range outcomes {
		if o.Status != outcomeMoved {
			continue
		}
		root := roots[cands[i].Source]
		for dir := filepath.Dir(cands[i].Folder); dir != root && config.IsWithin(dir, root) && !seen[dir]; dir = filepath.Dir(dir) {
			seen[dir] = true
			if cfg.Library != "" && root == cfg.Library && isBucketDir(cfg.Library, dir) {
				continue
			}
			if isEmptyTree(dir) {
				n++
			}
		}
	}
	return n
}

// isBucketDir reports whether dir is a bucket folder directly in the library.
func isBucketDir(lib, dir string) bool {
	if filepath.Dir(dir) != lib {
		return false
	}
	b := filepath.Base(dir)
	return b == BucketDigits || b == BucketOther || (len(b) == 1 && b[0] >= 'A' && b[0] <= 'Z')
}

// forEachParallel calls fn(0..n-1) using up to w goroutines.
func forEachParallel(n, w int, fn func(int)) {
	w = max(1, min(w, n))
	queue := make(chan int)
	var wg sync.WaitGroup
	wg.Add(w)
	for range w {
		go func() {
			defer wg.Done()
			for i := range queue {
				fn(i)
			}
		}()
	}
	for i := range n {
		queue <- i
	}
	close(queue)
	wg.Wait()
}

// Undo reverts the most recent organize run of the journal that was not undone yet: its
// moves in reverse order, each only if its target still exists and its source does not
// (fsops.MoveTo checks both; a failing move is logged, counted and skipped). Then it
// records the run as undone. A dry run only logs the planned moves. Undo returns an
// error only if the journal cannot be read.
func Undo(cfg config.OrganizeConfig, ops *fsops.Ops) error {
	runs, err := readJournal(cfg.JournalPath())
	if err != nil {
		return fmt.Errorf("reading the journal: %w", err)
	}
	r := lastUndoable(runs)
	if r == nil {
		log.Infof("Undo: nothing to undo in %s", cfg.JournalPath())
		return nil
	}
	log.Infof("Undo: reverting run %s (%s), %d move(s)", r.ID, r.Time, r.moves())
	failed := 0
	for i := len(r.Entries) - 1; i >= 0; i-- {
		e := r.Entries[i]
		if e.Op == opMkdir {
			// A folder the run created; something else may have been put there since.
			if removed, err := ops.RemoveEmptyDir(e.Dst); err != nil {
				log.Errorf("Undo: %v", err) // counted by ops
			} else if !removed {
				log.Debugf("Undo: folder %s created by the run is not empty or gone; left alone", e.Dst)
			}
			continue
		}
		if err := ops.MoveTo(e.Dst, e.Src); err != nil {
			log.Errorf("Undo: cannot move %s back to %s: %v", e.Dst, e.Src, err) // counted by ops
			failed++
		}
	}
	j := &journal{ops: ops, path: cfg.JournalPath(), run: r.ID}
	if err := j.append(journalEntry{Op: opUndone, Run: r.ID}); err != nil {
		log.Errorf("Undo: cannot record run %s as undone: %v", r.ID, err)
	}
	verb := "moved back"
	if cfg.DryRun {
		verb = "planned"
	}
	log.Infof("Undo: %d move(s) %s, %d failed", r.moves()-failed, verb, failed)
	return nil
}
