package organize

import (
	"context"
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

	"github.com/nkreimeyer/library-manager/check"
	"github.com/nkreimeyer/library-manager/config"
	"github.com/nkreimeyer/library-manager/fsops"
	"github.com/nkreimeyer/library-manager/songtxt"
	"github.com/nkreimeyer/library-manager/utils"
)

// Mode is what Run does with the library.
type Mode string

const (
	// ModeOrganize (the organize subcommand) moves every keeper to <Bucket>/<FolderName>,
	// the library's own songs only if the library is given as a source.
	ModeOrganize Mode = "organize"
	// ModeDedupe (dedupe) removes the duplicates of the library in place: the library is
	// the only source, keepers stay where they are, the other versions go to duplicates/.
	ModeDedupe Mode = "dedupe"
	// ModeAdd (add) adds the sources to the library: the library's songs compete with the
	// sources, library keepers stay where they are, new keepers go to their bucket.
	ModeAdd Mode = "add"
)

// Options configure Run.
type Options struct {
	// Mode; "" is ModeOrganize. ModeDedupe and ModeAdd plan with LibraryInPlace and need
	// the library among cfg.Sources (if it exists).
	Mode Mode
	// Priority: the tier of the source (the -s flag it was given with) decides first
	// between working versions of a song.
	Priority bool
	// IgnoreThe: bucket "The Beatles" under B (the folder name is unchanged).
	IgnoreThe bool
	// Deep: fully decode the media files while checking (check -deep).
	Deep bool

	// Ctx cancels the run (nil = never). It is checked before each song folder is
	// inspected (nothing has moved then) and before each move; a run stopped during the
	// moves is journaled like a complete one, so Undo reverts what it did. Run then
	// returns Ctx.Err().
	Ctx context.Context
	// Progress, if set, is called with the phase (PhaseInspect, PhaseMove), the number of
	// items done and their total. It may be called from several goroutines at once.
	Progress func(phase string, done, total int)
	// Exclude lists candidates (by Ref) that are left where they are ("excluded in
	// review"); see PlanOptions.Exclude.
	Exclude map[string]bool
	// Review, if set, is called with the plan before any report is written or anything is
	// moved. An error it returns stops Run with that error.
	Review func(*Review) error
}

// Phases reported to Options.Progress.
const (
	PhaseInspect = "inspect"
	PhaseMove    = "move"
	PhaseUndo    = "undo" // UndoOptions.Progress: the journal entries of the run
)

// UndoOptions configure Undo.
type UndoOptions struct {
	// Ctx stops the undo (nil = never). It is checked before each journal entry, except
	// while a song is parked under a temporary name (a cycle half undone): the undo goes on
	// until it is out. A stopped undo is not recorded as undone, so the next one finishes
	// it; Undo then returns Ctx.Err().
	Ctx context.Context
	// Progress, if set, is called with PhaseUndo, the number of entries done and their total.
	Progress func(phase string, done, total int)
}

// Review is the plan of a run, as Options.Review sees it.
type Review struct {
	Cands []Candidate
	Plan  Plan
	// Notes are the report notes (e.g. ffprobe missing).
	Notes []string
	// Replan plans the same candidates again with other exclusions. It may be called
	// after Run returned. It is not pure: the library's bucket folders are listed once,
	// when a plan first needs them, so the buckets Plan looked at are seen as they were
	// then, but a bucket only the new plan reaches (another keeper's artist) is read from
	// the disk now, and its problems are logged and counted on Run's ops.
	Replan func(exclude map[string]bool) Plan
}

// ctx is the Ctx, with nil as context.Background().
func (o Options) ctx() context.Context {
	if o.Ctx == nil {
		return context.Background()
	}
	return o.Ctx
}

func (o Options) progress(phase string, done, total int) {
	if o.Progress != nil {
		o.Progress(phase, done, total)
	}
}

// workers is the number of song folders inspected at the same time. It is a variable so
// tests can force concurrency on machines with a single CPU.
var workers = min(16, runtime.NumCPU())

// inspectFolder inspects one song folder; a variable so tests can make it panic.
var inspectFolder = (*check.Inspector).Inspect

// errInspectPanic marks a candidate whose inspection panicked: it is left in place like an
// unreadable folder.
var errInspectPanic = errors.New("inspection panicked")

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
	ctx := opts.ctx()
	cands := make([]Candidate, len(items))
	opts.progress(PhaseInspect, 0, len(items))
	_ = utils.ForEachParallel(ctx, items, utils.Parallel{
		Workers:  workers,
		Progress: func(done, total int) { opts.progress(PhaseInspect, done, total) },
		OnPanic: func(i int, _ any) {
			// Like an unreadable folder, the song stays where it is.
			log.Errorf("Organize: %s not inspected: internal error (logged above); it stays in place", items[i].folder)
			ops.Fail()
			cands[i] = newCandidate(cfg, items[i].src, items[i].folder, check.Inspection{}, errInspectPanic)
		},
	}, func(i int, it item) {
		in, err := inspectFolder(insp, it.folder) // an error is logged and counted by Inspect
		cands[i] = newCandidate(cfg, it.src, it.folder, in, err)
	})
	// Also after a cancel during the last inspections: nothing is moved.
	if err := ctx.Err(); err != nil {
		log.Warnf("Organize: stopped while inspecting the songs; nothing was moved")
		return err
	}
	log.Infof("Organize: %d song folder(s) in %d source(s), %d empty folder(s) ignored", len(cands), len(cfg.Sources), len(empty))

	planOpts := PlanOptions{
		Priority: opts.Priority, IgnoreThe: opts.IgnoreThe, Occupied: libraryOccupant(ops, cfg.Library),
		LibraryInPlace: opts.mode() != ModeOrganize, Exclude: opts.Exclude,
	}
	for _, src := range cfg.Sources {
		if src.Path == cfg.Library {
			planOpts.LibrarySource = true
		}
	}
	plan := MakePlan(cands, planOpts)
	logPlan(cands, plan)
	if opts.Review != nil {
		replan := func(exclude map[string]bool) Plan {
			po := planOpts
			po.Exclude = exclude
			return MakePlan(cands, po)
		}
		if err := opts.Review(&Review{Cands: cands, Plan: plan, Notes: notes, Replan: replan}); err != nil {
			return err
		}
	}

	in := reportInput{cfg: cfg, opts: opts, cands: cands, plan: plan, outcomes: map[int]moveOutcome{}, empty: empty, notes: notes}
	if !cfg.DryRun {
		// A crash during the moves leaves the plan behind (the journal has the moves).
		for i, d := range plan.Decisions {
			if movesSong(d.Action) {
				in.outcomes[i] = moveOutcome{Status: outcomePending}
			}
		}
		if err := writeReportFiles(cfg, ops, reportJSON, reportTxt, buildReport(in)); err != nil {
			return err
		}
	}

	a := newApplier(cfg, ops, cands, in.outcomes)
	a.ctx, a.progress, a.libraryInPlace = ctx, opts.progress, planOpts.LibraryInPlace
	a.apply(plan)
	if !cfg.DryRun {
		in.emptyLeft = emptiedCategories(cfg, cands, in.outcomes)
	}
	rep := buildReport(in)
	if err := writeReportFiles(cfg, ops, reportJSON, reportTxt, rep); err != nil {
		return err
	}
	verb := "moved"
	if cfg.DryRun {
		verb = "planned"
	}
	log.Infof("%s: %d to the library, %d already in place, %d duplicate(s), %d invalid, %d left in place, %d possible duplicate(s), %d similar song pair(s); %d move(s) %s, %d failed; see %s",
		strings.ToUpper(string(rep.Mode[:1]))+string(rep.Mode[1:]), rep.Summary.ToLibrary, rep.Summary.AlreadyInPlace, rep.Summary.Duplicates, rep.Summary.Invalid, rep.Summary.LeftInPlace,
		rep.Summary.PossibleDuplicates, rep.Summary.Similar, rep.Summary.Moved, verb, rep.Summary.Failed, cfg.ReportPath(reportTxt))
	if a.j.failed {
		return fmt.Errorf("the journal %s cannot be written; the remaining moves were not attempted", cfg.JournalPath())
	}
	if err := ctx.Err(); err != nil && a.stopped {
		log.Warnf("Organize: stopped during the moves; the moves done so far are journaled (undo reverts them)")
		return err
	}
	return nil
}

// mode is the Mode, with "" as ModeOrganize.
func (o Options) mode() Mode {
	if o.Mode == "" {
		return ModeOrganize
	}
	return o.Mode
}

func movesSong(a Action) bool { return a == ActMove || a == ActDuplicate || a == ActInvalid }

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
	_, c.MarkedCopy = StripCopySuffix(filepath.Base(folder), false)
	if _, marked := StripCopySuffix(in.Txt, true); marked {
		c.MarkedCopy = true
	}
	if in.Song != nil {
		var markedArtist, markedTitle bool
		c.Artist, markedArtist = StripCopySuffix(tagValue(in.Song, "ARTIST"), false)
		c.Title, markedTitle = StripCopySuffix(tagValue(in.Song, "TITLE"), false)
		c.MarkedCopy = c.MarkedCopy || markedArtist || markedTitle
		for _, tag := range []string{"AUDIO", "MP3", "VIDEO"} {
			if _, marked := StripCopySuffix(tagValue(in.Song, tag), true); marked {
				c.MarkedCopy = true
			}
		}
		// A duet by its notes (a solo file with one "P1" marker is none, as for
		// ContentHash) or by its title.
		c.Duet = in.Song.IsDuet() || IsDuetTitle(c.Title)
		c.ContentHash = ContentHash(in.Song)
		c.Profile = NewProfile(in.Song)
		c.Quality.HasVersion = tagValue(in.Song, "VERSION") != ""
	}
	m := in.Media
	c.Quality.SongWarnings = check.SongWarnings(in.Findings)
	c.Quality.HasVideo, c.Quality.VideoHeight, c.Quality.VideoBitrate = m.HasVideo, m.VideoHeight, m.VideoBitrate
	c.Quality.AudioBitrate = m.AudioBitrate
	c.Quality.HasCover, c.Quality.HasBackground = m.HasCover, m.HasBackground
	return c
}

// tagValue returns the value of the tag a player uses (the last occurrence), "" if absent.
func tagValue(s *songtxt.Song, key string) string {
	v, _ := s.Tag(key)
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
	// ctx stops the moves (checked before each one); stopped is set when it did.
	ctx     context.Context
	stopped bool
	// progress reports the moves (PhaseMove); never nil.
	progress func(phase string, done, total int)
	// libraryInPlace is PlanOptions.LibraryInPlace (dedupe, add): see restoreLibraryLosers.
	libraryInPlace bool
}

func newApplier(cfg config.OrganizeConfig, ops *fsops.Ops, cands []Candidate, outcomes map[int]moveOutcome) *applier {
	return &applier{cfg: cfg, ops: ops, cands: cands, j: newJournal(ops, cfg.JournalPath()), outcomes: outcomes, moveTo: ops.MoveTo, freeBuckets: map[string]bool{},
		ctx: context.Background(), progress: func(string, int, int) {}}
}

// cancelled reports whether the moves are to stop, and logs it once.
func (a *applier) cancelled() bool {
	if a.ctx.Err() == nil {
		return false
	}
	if !a.stopped {
		a.stopped = true
		log.Warnf("Stopping: no further songs are moved")
	}
	return true
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
// an order in which every target is free (orderMoves), and finally moves library songs
// whose keeper did not get there back (restoreLibraryLosers).
func (a *applier) apply(p Plan) {
	defer a.restoreLibraryLosers(p)
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
	total, moved := len(out)+len(moves), 0
	a.progress(PhaseMove, 0, total)
	for _, i := range out {
		if a.cancelled() {
			a.outcomes[i] = moveOutcome{Status: outcomeSkipped, Note: noteStopped}
			continue
		}
		if !a.ready(i) {
			continue
		}
		moved++
		a.progress(PhaseMove, moved, total)
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
		if a.cancelled() {
			a.outcomes[s.Cand] = moveOutcome{Status: outcomeSkipped, Note: noteStopped}
			if s.ViaTemp {
				a.unpark(s) // never leave a song hidden under the temporary name
			}
			continue
		}
		if !s.ToTemp {
			moved++
			a.progress(PhaseMove, moved, total)
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

// restoreLibraryLosers moves the library songs that went to duplicates/ back to their
// place when the keeper they lost to, from another source, did not reach the library:
// its move failed, was refused (bucketTaken) or not attempted (stop, journal failure).
// Otherwise the library would be left without the song. In dedupe and add (a library
// that stays in place) the same goes for a broken library song whose replacement did not
// get there. It is the counterpart of keepLibraryLosers for what only the moves show, and
// it also runs after a stop (as unpark does). The moves back are journaled, so -undo
// still reverts the whole run; after a journal failure nothing is moved and the songs are
// named instead. A dry run plans the same moves back.
func (a *applier) restoreLibraryLosers(p Plan) {
	reached := func(k int) bool {
		s := a.outcomes[k].Status
		return s == outcomeMoved || s == outcomePlanned
	}
	for i, d := range p.Decisions {
		c := &a.cands[i]
		k := d.Other
		switch {
		case !c.InLibrary || k < 0 || !reached(i):
			continue
		case d.Action != ActDuplicate && !(d.Action == ActInvalid && a.libraryInPlace):
			continue
		case p.Decisions[k].Action != ActMove || a.cands[k].InLibrary || reached(k):
			continue // the keeper is in the library (a library keeper stays there if its move fails)
		}
		from := a.outcomes[i].Dst
		if a.j.failed {
			log.Errorf("%s did not reach the library, and %s (moved to %s) cannot be moved back: the journal cannot be written; move it back by hand", a.cands[k].Ref(), c.Ref(), from)
			continue
		}
		if out := a.outcomes[i]; !a.journalMkdirs(i, c.Folder) {
			a.outcomes[i] = out // it is still where it was moved to
			continue
		}
		if err := a.moveTo(from, c.Folder); err != nil {
			log.Errorf("%s did not reach the library, and moving %s back from %s failed: %v; -undo moves it back", a.cands[k].Ref(), c.Ref(), from, err) // counted by ops
			continue
		}
		log.Warnf("Moved %s back into the library: %s, which it lost to, did not get there", c.Ref(), a.cands[k].Ref())
		a.journalMove(from, c.Folder)
		a.outcomes[i] = moveOutcome{Status: outcomeRestored}
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
	a.outcomes[i] = moveOutcome{Status: outcomeSkipped, Note: noteJournalFailed}
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
			a.outcomes[i] = moveOutcome{Status: outcomeSkipped, Note: noteJournalFailed}
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
// attempted (the journal cannot be written, or the run was stopped): back to where it
// came from or, if that place is taken by now (a later step of the cycle moved a song
// into it), to its planned target. Either way it stays visible to the discovery. The
// move to the target is journaled unless the journal failed, so -undo can revert it. If
// both moves fail, the song stays at the temporary name: -undo moves it back if the
// journal works (the park is in it), else it must be moved by hand.
func (a *applier) unpark(s step) {
	if a.moveBack(s) {
		return
	}
	journaled := !a.j.failed && a.journalMkdirs(s.Cand, s.Dst)
	if err := a.moveTo(s.Src, s.Dst); err != nil {
		if a.j.failed {
			log.Errorf("Moving %s to %s failed too: %v; the song stays at the temporary name, move it by hand", s.Src, s.Dst, err)
		} else {
			// The journal works, so the park is in it.
			log.Errorf("Moving %s to %s failed too: %v; the song stays at the temporary name, -undo moves it back", s.Src, s.Dst, err)
		}
		return
	}
	if journaled && a.journalMove(s.Src, s.Dst) {
		log.Warnf("Moved %s to its planned target %s instead", s.Src, s.Dst)
	} else {
		log.Warnf("Moved %s to its planned target %s instead; this move is not in the journal", s.Src, s.Dst)
	}
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

// Undo reverts the most recent run of the journal (organize, dedupe, add or remove) that
// was not undone yet: its moves in reverse order, each only if its target still exists and
// its source does not (fsops.MoveTo checks both; a failing move is logged, counted and
// skipped). A move whose target and source are both gone (moved or deleted by hand) is
// counted as failed and skipped. Each move back is journaled ("reverted"). Then it records
// the run as undone, unless a move failed that a retry could do or opts.Ctx stopped it:
// then the next Undo retries this run, skipping the reverted entries, instead of reverting
// an older run. A dry run only logs the planned moves. Undo returns an error if the journal
// cannot be read, if it could not record moves back of a run that is not recorded as
// undone (a retry could repeat them), or opts.Ctx.Err() if it stopped.
func Undo(cfg config.OrganizeConfig, ops *fsops.Ops, opts UndoOptions) error {
	ctx := opts.Ctx
	if ctx == nil {
		ctx = context.Background()
	}
	progress := func(done, total int) {
		if opts.Progress != nil {
			opts.Progress(PhaseUndo, done, total)
		}
	}
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
	reverted, already, gone, failed := 0, 0, 0, 0
	// parked holds the temporary names a song is at: a cycle of the run is half undone.
	parked := map[string]bool{}
	isTemp := func(p string) bool { return strings.HasPrefix(filepath.Base(p), tempPrefix) }
	j := &journal{ops: ops, path: cfg.JournalPath(), run: r.ID}
	unrecorded := 0 // reverted moves the journal could not record
	undoEntry := func(n int, e journalEntry) {
		if e.Op == opMkdir {
			// A folder the run created; something else may have been put there since.
			if removed, err := ops.RemoveEmptyDir(e.Dst); err != nil {
				log.Errorf("Undo: %v", err) // counted by ops
			} else if !removed {
				log.Debugf("Undo: folder %s created by the run is not empty or gone; left alone", e.Dst)
			}
			return
		}
		if r.Reverted[n] {
			log.Debugf("Undo: %s was moved back to %s by an earlier undo", e.Dst, e.Src)
			already++
			return
		}
		if !ops.Exists(e.Dst) {
			if ops.Exists(e.Src) {
				// Moved back by an earlier undo whose record was lost.
				log.Debugf("Undo: %s is back at %s already", e.Dst, e.Src)
				already++
				return
			}
			// Moved or deleted by hand since: retrying cannot help, so it does not keep
			// the run from counting as undone.
			log.Errorf("Undo: cannot move %s back to %s: it is gone", e.Dst, e.Src)
			ops.Fail()
			gone++
			return
		}
		if err := ops.MoveTo(e.Dst, e.Src); err != nil {
			log.Errorf("Undo: cannot move %s back to %s: %v", e.Dst, e.Src, err) // counted by ops
			failed++
			return
		}
		reverted++
		if err := j.append(journalEntry{Op: opReverted, Run: r.ID, Entry: n}); err != nil {
			log.Errorf("Undo: cannot record that %s is back at %s: %v", e.Dst, e.Src, err)
			unrecorded++
		}
	}
	var stopped error
	progress(0, len(r.Entries))
	for i := len(r.Entries) - 1; i >= 0; i-- {
		if err := ctx.Err(); err != nil && len(parked) == 0 {
			stopped = err
			break
		}
		e := r.Entries[i]
		undoEntry(i+1, e)
		if e.Op != opMkdir {
			if isTemp(e.Src) && ops.Exists(e.Src) {
				parked[e.Src] = true
			}
			if isTemp(e.Dst) {
				// The only entry that brings the song out: if it failed, the song stays at
				// the temporary name until the next undo, and the stop applies again.
				if parked[e.Dst] && ops.Exists(e.Dst) {
					log.Warnf("Undo: %s stays at the temporary name %s; run the undo again once %s is free", e.Src, e.Dst, e.Src)
				}
				delete(parked, e.Dst)
			}
		}
		progress(len(r.Entries)-i, len(r.Entries))
	}
	verb := "moved back"
	if cfg.DryRun {
		verb = "planned"
	}
	summary := fmt.Sprintf("Undo: %d move(s) %s, %d failed", reverted, verb, failed+gone)
	if gone > 0 {
		summary += fmt.Sprintf(" (%d gone)", gone)
	}
	if already > 0 {
		summary += fmt.Sprintf(", %d moved back by an earlier undo", already)
	}
	log.Info(summary)
	recorded := false // the run is recorded as undone
	switch {
	case stopped != nil:
		log.Warnf("Undo: stopped; run %s is not completely undone, run the undo again to finish it", r.ID)
	case failed > 0:
		// Not recorded as undone: the next -undo retries this run (the moves done now
		// are skipped then), instead of reverting the run before it.
		log.Warnf("Undo: run %s is not completely undone; fix the cause of the failed move(s) and run the undo again to retry them", r.ID)
	default:
		if err := j.append(journalEntry{Op: opUndone, Run: r.ID}); err != nil {
			log.Errorf("Undo: cannot record run %s as undone: %v", r.ID, err)
		} else {
			recorded = true
		}
	}
	if unrecorded > 0 && !recorded {
		// A retry would take the moves the journal lacks for undone ones (a swapped pair even
		// looks like one) and repeat them.
		return fmt.Errorf("the journal could not record %d move(s) back, so undoing run %s again could repeat them: check the library before you do", unrecorded, r.ID)
	}
	return stopped
}
