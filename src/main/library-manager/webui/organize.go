package webui

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"slices"
	"sort"

	log "github.com/sirupsen/logrus"

	"github.com/nkreimeyer/library-manager/config"
	"github.com/nkreimeyer/library-manager/fsops"
	"github.com/nkreimeyer/library-manager/organize"
)

// organizeRequest is what the UI sends for an organize, dedupe or add run.
type organizeRequest struct {
	Mode      string     `json:"mode"` // organize, dedupe, add
	Tiers     [][]string `json:"tiers"`
	Library   string     `json:"library"`
	Out       string     `json:"out"`
	Priority  bool       `json:"priority"`
	IgnoreThe bool       `json:"ignoreThe"`
	Deep      bool       `json:"deep"`
	// Tidy, for a dedupe, are the options of the tidy-up screen (nil: only the dedupe).
	Tidy *tidyOptions `json:"tidy,omitempty"`
}

func (q organizeRequest) mode() (organize.Mode, error) {
	switch m := organize.Mode(q.Mode); m {
	case organize.ModeOrganize, organize.ModeDedupe, organize.ModeAdd:
		return m, nil
	}
	return "", fmt.Errorf("unknown mode %q", q.Mode)
}

// config builds the configuration like the organize, dedupe and add subcommands do
// (organize.NewConfig); empty folder fields of the form are dropped.
func (q organizeRequest) config(dry bool) (config.OrganizeConfig, error) {
	m, err := q.mode()
	if err != nil {
		return config.OrganizeConfig{}, err
	}
	if err := q.Tidy.check(m); err != nil {
		return config.OrganizeConfig{}, err
	}
	var tiers [][]string
	for _, t := range q.Tiers {
		var dirs []string
		for _, d := range t {
			if d != "" {
				dirs = append(dirs, d)
			}
		}
		if len(dirs) > 0 {
			tiers = append(tiers, dirs)
		}
	}
	return organize.NewConfig(m, tiers, q.Library, q.Out, dry)
}

// jobKind is the kind of the job that does action (analyse, apply): "tidy-…" for a
// tidy-up, so a page that reconnects to it knows what runs, else "organize-…".
func (q organizeRequest) jobKind(action string) string {
	if q.Tidy != nil {
		return "tidy-" + action
	}
	return "organize-" + action
}

func (q organizeRequest) options(m organize.Mode) organize.Options {
	return organize.NewOptions(m, q.Priority, q.IgnoreThe, q.Deep)
}

type sourceView struct {
	Label string `json:"label"`
	Path  string `json:"path"`
	Tier  int    `json:"tier"` // 1-based
}

type configView struct {
	Library string       `json:"library"`
	OutDir  string       `json:"outDir"`
	Sources []sourceView `json:"sources"`
}

func viewConfig(cfg config.OrganizeConfig) configView {
	v := configView{Library: cfg.Library, OutDir: cfg.OutDir, Sources: []sourceView{}}
	for _, s := range cfg.Sources {
		v.Sources = append(v.Sources, sourceView{Label: s.Label, Path: s.Path, Tier: s.Tier + 1})
	}
	return v
}

// handleOrganizeValidate checks a request and returns the resolved folders (the default
// output folder, the source labels), or the error the CLI would give.
func (s *Server) handleOrganizeValidate(w http.ResponseWriter, r *http.Request) {
	var q organizeRequest
	if !readJSON(w, r, &q) {
		return
	}
	cfg, err := q.config(true)
	if err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	writeJSON(w, http.StatusOK, viewConfig(cfg))
}

// organizeAnalysis is a dry run kept for review and apply.
type organizeAnalysis struct {
	id      string
	req     organizeRequest
	cfg     configView
	review  *organize.Review
	exclude map[string]bool
	join    [][2]string   // pairs of refs the reviewer treats as one song
	plan    organize.Plan // review.Plan with exclude applied; review is nil if no song step ran
	applied bool
	// tidy is what a tidy-up's text and cleanup steps would change (nil for other runs);
	// tidyDone: an apply ran them already (it then stopped at a changed song plan).
	tidy     *tidyPlan
	tidyDone bool
	steps    []stepResult // what the check's steps planned
}

func (s *Server) handleOrganizeAnalyse(w http.ResponseWriter, r *http.Request) {
	var q organizeRequest
	if !readJSON(w, r, &q) {
		return
	}
	m, err := q.mode()
	if err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	cfg, err := q.config(true)
	if err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	if err := q.Tidy.checkTools(r.Context()); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	a := &organizeAnalysis{id: s.newID("o"), req: q, cfg: viewConfig(cfg), exclude: map[string]bool{}}
	ops := fsops.New(true)
	s.startJob(w, a.id, q.jobKind("analyse"), ops, func(ctx context.Context, c jobControl) (any, error) {
		if err := cfg.PrepareOutDir(); err != nil {
			return nil, err
		}
		run := &stepRun{tidy: q.Tidy, cfg: cfg, ops: ops, ctx: ctx, c: c, songs: q.options(m)}
		run.songs.Review = func(rv *organize.Review) error {
			a.review, a.plan = rv, rv.Plan
			return nil
		}
		if q.Tidy != nil {
			run.onText = func(p tidyPlan) { a.tidy = &p }
		}
		log.Infof("Checking %s (dry run, nothing is changed); library %s, output %s", m, cfg.Library, cfg.OutDir)
		steps, err := run.run()
		if err != nil {
			return nil, err
		}
		if a.review == nil && slices.Contains(q.Tidy.steps(true), stepSongs) {
			return nil, fmt.Errorf("the run made no plan")
		}
		a.steps = steps
		s.mu.Lock()
		// Only the newest analysis is kept: an older one holds every song of its library and
		// is not shown any more (applying it answers "check again").
		s.organize = map[string]*organizeAnalysis{a.id: a}
		v := buildOrganizeView(a)
		s.mu.Unlock()
		return map[string]any{"analysis": a.id, "summary": v.Summary}, nil
	})
}

func (s *Server) analysis(id string) (*organizeAnalysis, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	a := s.organize[id]
	if a == nil {
		return nil, fmt.Errorf("no such analysis %q; check again", id)
	}
	return a, nil
}

func (s *Server) handleOrganizeAnalysis(w http.ResponseWriter, r *http.Request) {
	a, err := s.analysis(r.URL.Query().Get("id"))
	if err != nil {
		writeError(w, http.StatusNotFound, err)
		return
	}
	s.mu.Lock()
	v := buildOrganizeView(a)
	s.mu.Unlock()
	writeJSON(w, http.StatusOK, v)
}

// handleOrganizeExclude sets the excluded songs of an analysis and plans again.
func (s *Server) handleOrganizeExclude(w http.ResponseWriter, r *http.Request) {
	var q struct {
		Analysis string   `json:"analysis"`
		Exclude  []string `json:"exclude"`
	}
	if !readJSON(w, r, &q) {
		return
	}
	a, err := s.analysis(q.Analysis)
	if err != nil {
		writeError(w, http.StatusNotFound, err)
		return
	}
	ex := map[string]bool{}
	for _, ref := range q.Exclude {
		ex[ref] = true
	}
	s.mu.Lock()
	rv, applied, join := a.review, a.applied, a.join
	s.mu.Unlock()
	if applied {
		writeError(w, http.StatusConflict, fmt.Errorf("this plan was applied already"))
		return
	}
	if rv == nil {
		writeError(w, http.StatusConflict, fmt.Errorf("this check moves no song folders, so there is none to leave alone"))
		return
	}
	plan := rv.ReplanWith(ex, join) // in memory (only buckets no plan needed yet are read), may take a moment for a big library
	s.mu.Lock()
	if a.review != rv {
		s.mu.Unlock()
		writeError(w, http.StatusConflict, fmt.Errorf("the plan changed meanwhile; review it again"))
		return
	}
	a.exclude, a.plan = ex, plan
	v := buildOrganizeView(a)
	s.mu.Unlock()
	writeJSON(w, http.StatusOK, v)
}

// handleOrganizeJoin sets the pairs of songs that are treated as the same song and plans
// again. The pairs are the whole list: an empty list keeps every pair apart.
func (s *Server) handleOrganizeJoin(w http.ResponseWriter, r *http.Request) {
	var q struct {
		Analysis string      `json:"analysis"`
		Join     [][2]string `json:"join"`
	}
	if !readJSON(w, r, &q) {
		return
	}
	a, err := s.analysis(q.Analysis)
	if err != nil {
		writeError(w, http.StatusNotFound, err)
		return
	}
	s.mu.Lock()
	rv, applied, ex := a.review, a.applied, a.exclude
	s.mu.Unlock()
	if applied {
		writeError(w, http.StatusConflict, fmt.Errorf("this plan was applied already"))
		return
	}
	if rv == nil {
		writeError(w, http.StatusConflict, fmt.Errorf("this check moves no song folders, so there is nothing to join"))
		return
	}
	known := map[string]bool{}
	for i := range rv.Cands {
		known[rv.Cands[i].Ref()] = true
	}
	for _, p := range q.Join {
		if !known[p[0]] || !known[p[1]] || p[0] == p[1] {
			writeError(w, http.StatusBadRequest, fmt.Errorf("unknown or identical songs %q and %q", p[0], p[1]))
			return
		}
	}
	plan := rv.ReplanWith(ex, q.Join)
	s.mu.Lock()
	if a.review != rv {
		s.mu.Unlock()
		writeError(w, http.StatusConflict, fmt.Errorf("the plan changed meanwhile; review it again"))
		return
	}
	a.join, a.plan = q.Join, plan
	v := buildOrganizeView(a)
	s.mu.Unlock()
	writeJSON(w, http.StatusOK, v)
}

// maxDiffLines limits the plan differences shown.
var maxDiffLines = 200

var errPlanApplied = errors.New("this plan was applied already; check again for a new plan")

// handleOrganizeApply runs the reviewed analysis for real. The run plans again; if its
// plan moves anything differently from the reviewed one (a folder changed since), it
// stops before the first move, and the analysis shows the new plan for another review.
func (s *Server) handleOrganizeApply(w http.ResponseWriter, r *http.Request) {
	var q struct {
		Analysis string `json:"analysis"`
	}
	if !readJSON(w, r, &q) {
		return
	}
	a, err := s.analysis(q.Analysis)
	if err != nil {
		writeError(w, http.StatusNotFound, err)
		return
	}
	s.mu.Lock()
	applied, exclude, join := a.applied, a.exclude, a.join
	s.mu.Unlock()
	if applied {
		writeError(w, http.StatusConflict, errPlanApplied)
		return
	}
	m, _ := a.req.mode()
	cfg, err := a.req.config(false)
	if err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	ops := fsops.New(false)
	s.startJob(w, s.newID("a"), a.req.jobKind("apply"), ops, func(ctx context.Context, c jobControl) (any, error) {
		if err := cfg.PrepareOutDir(); err != nil {
			return nil, err
		}
		run := &stepRun{tidy: a.req.Tidy, cfg: cfg, ops: ops, ctx: ctx, c: c, songs: a.req.options(m)}
		run.songs.Exclude, run.songs.Join = exclude, join
		var changed map[string]any // the result of a changed plan
		run.songs.Review = func(rv *organize.Review) error {
			s.mu.Lock()
			defer s.mu.Unlock()
			diff := organize.PlanDiff(a.review.Cands, a.plan, rv.Cands, rv.Plan)
			if len(diff) == 0 {
				a.applied = true
				return nil
			}
			a.review, a.plan = rv, rv.Plan
			if a.tidy != nil {
				// The text and cleanup steps ran already: the next apply finds nothing left.
				a.tidy, a.tidyDone = &tidyPlan{TextFiles: []string{}, Leftovers: []string{}, BrokenFolders: []string{}}, true
			}
			n := len(diff)
			if n > maxDiffLines {
				diff = append(diff[:maxDiffLines], fmt.Sprintf("… and %d more", n-maxDiffLines))
			}
			changed = map[string]any{"analysis": a.id, "planChanged": true}
			moved := "nothing was moved"
			if a.tidy != nil {
				moved = "no song folder was moved (the song texts and leftover files were handled)"
			}
			return &detailedError{
				msg:     fmt.Sprintf("%v: %d song folder(s) would be handled differently, so %s; review the new plan", organize.ErrPlanChanged, n, moved),
				details: diff, result: changed,
			}
		}
		if err := s.claimApply(&a.applied, errPlanApplied); err != nil {
			return nil, err
		}
		// Without a song step there is no song plan to compare: the check's lists are all
		// there is, and the analysis is applied once the steps changed something or finished.
		noSongs := !slices.Contains(a.req.Tidy.steps(false), stepSongs)
		log.Infof("Applying %s: library %s, output %s", m, cfg.Library, cfg.OutDir)
		if noSongs && applyStartHook != nil {
			applyStartHook()
		}
		steps, runErr := run.run()
		if noSongs {
			s.markApplied(&a.applied, ops, ctx, runErr)
		}
		res := map[string]any{"analysis": a.id, "outDir": cfg.OutDir, "library": cfg.Library, "steps": steps}
		if a.req.Tidy != nil {
			res["tidy"] = true // the page shows the steps (also after a reload)
		}
		s.mu.Lock()
		reported := a.applied && a.review != nil // the song step ran with the reviewed plan
		s.mu.Unlock()
		if data, err := os.ReadFile(cfg.ReportPath("organize_plan.json")); err == nil && reported {
			res["report"] = json.RawMessage(data)
		}
		if changed != nil {
			changed["steps"] = steps // a changed plan's detailedError brings its own result
		}
		var de *detailedError
		if errors.As(runErr, &de) && de.result == nil {
			de.result = res // failed steps: the others' results are still shown
		}
		return res, runErr
	})
}

type folderRequest struct {
	Library string `json:"library"`
	Out     string `json:"out"`
	DryRun  bool   `json:"dryRun"`
}

func (q folderRequest) config() (config.OrganizeConfig, error) {
	return organize.NewUndoConfig(q.Library, q.Out, q.DryRun)
}

// handleOrganizeHistory lists the runs of the journal in the output folder.
func (s *Server) handleOrganizeHistory(w http.ResponseWriter, r *http.Request) {
	var q folderRequest
	if !readJSON(w, r, &q) {
		return
	}
	cfg, err := q.config()
	if err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	runs, err := organize.ListRuns(cfg)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	if runs == nil {
		runs = []organize.RunInfo{}
	}
	for i, j := 0, len(runs)-1; i < j; i, j = i+1, j-1 {
		runs[i], runs[j] = runs[j], runs[i] // newest first
	}
	writeJSON(w, http.StatusOK, map[string]any{"outDir": cfg.OutDir, "runs": runs})
}

// undoProgressHook, if set (tests), is called after each journal entry an undo job handled.
var undoProgressHook func(done int)

// handleOrganizeUndo reverts the newest run that is not undone yet.
func (s *Server) handleOrganizeUndo(w http.ResponseWriter, r *http.Request) {
	var q folderRequest
	if !readJSON(w, r, &q) {
		return
	}
	cfg, err := q.config()
	if err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	ops := fsops.New(cfg.DryRun)
	s.startJob(w, s.newID("u"), "organize-undo", ops, func(ctx context.Context, c jobControl) (any, error) {
		progress := c.Progress
		if undoProgressHook != nil {
			progress = func(phase string, done, total int) {
				c.Progress(phase, done, total)
				undoProgressHook(done)
			}
		}
		err := organize.Undo(cfg, ops, organize.UndoOptions{Ctx: ctx, Progress: progress})
		// The folders also with an error: a stopped or partial undo is retried from them.
		return map[string]any{"library": cfg.Library, "outDir": cfg.OutDir}, err
	})
}

// The review view: every song folder with its decision; the UI groups them.

type qualityView struct {
	Warnings    int   `json:"warnings"`
	Video       bool  `json:"video"`
	VideoHeight int   `json:"videoHeight,omitempty"`
	VideoKbps   int64 `json:"videoKbps,omitempty"`
	AudioKbps   int64 `json:"audioKbps,omitempty"`
	Cover       bool  `json:"cover"`
	Background  bool  `json:"background"`
	Version     bool  `json:"version"`
}

type songView struct {
	Ref        string      `json:"ref"`
	Label      string      `json:"label"`
	Rel        string      `json:"rel"`
	Folder     string      `json:"folder"`
	Artist     string      `json:"artist"`
	Title      string      `json:"title"`
	Tier       int         `json:"tier"` // 1-based
	Duet       bool        `json:"duet,omitempty"`
	InLibrary  bool        `json:"inLibrary,omitempty"`
	MarkedCopy bool        `json:"markedCopy,omitempty"`
	Errors     []string    `json:"errors,omitempty"`
	Quality    qualityView `json:"quality"`
	Action     string      `json:"action"`
	Target     string      `json:"target,omitempty"`
	Reason     string      `json:"reason,omitempty"`
	Other      int         `json:"other"` // index of the keeper or replacement; -1
	Excluded   bool        `json:"excluded,omitempty"`
}

type similarView struct {
	A        int    `json:"a"`
	B        int    `json:"b"`
	Evidence string `json:"evidence"`
	Possible bool   `json:"possible,omitempty"`
}

type organizeSummary struct {
	Songs      int `json:"songs"`
	ToLibrary  int `json:"toLibrary"`
	InPlace    int `json:"inPlace"`
	Duplicates int `json:"duplicates"`
	Invalid    int `json:"invalid"`
	Left       int `json:"left"`
	Excluded   int `json:"excluded"`
	Possible   int `json:"possible"`
	Similar    int `json:"similar"`
}

type organizeView struct {
	ID                 string          `json:"id"`
	Mode               string          `json:"mode"`
	Config             configView      `json:"config"`
	Notes              []string        `json:"notes"`
	Summary            organizeSummary `json:"summary"`
	Songs              []songView      `json:"songs"`
	PossibleDuplicates [][]int         `json:"possibleDuplicates"`
	Similar            []similarView   `json:"similar"`
	Excluded           []string        `json:"excluded"`
	Joined             [][2]string     `json:"joined"` // pairs of song refs treated as one song
	Applied            bool            `json:"applied"`
	Tidy               *tidyView       `json:"tidy,omitempty"`
}

// tidyView is the tidy-up part of a review: the options, what the text and cleanup steps
// would change, and whether the song step ran (Songs: the song lists are its plan).
type tidyView struct {
	Options tidyOptions  `json:"options"`
	Steps   []stepResult `json:"steps"`
	tidyPlan
	Done  bool `json:"done"`
	Songs bool `json:"songs"`
}

func summarize(cands []organize.Candidate, p organize.Plan) organizeSummary {
	sum := organizeSummary{Songs: len(cands), Possible: len(p.PossibleDuplicates), Similar: len(p.Similar)}
	for _, d := range p.Decisions {
		switch d.Action {
		case organize.ActMove:
			sum.ToLibrary++
		case organize.ActInPlace:
			sum.InPlace++
		case organize.ActDuplicate:
			sum.Duplicates++
		case organize.ActInvalid:
			sum.Invalid++
		case organize.ActLeft:
			if d.Reason == organize.ReasonExcluded {
				sum.Excluded++
			} else {
				sum.Left++
			}
		}
	}
	return sum
}

// buildOrganizeView renders an analysis; the caller holds s.mu.
func buildOrganizeView(a *organizeAnalysis) organizeView {
	var cands []organize.Candidate
	var notes []string
	if a.review != nil {
		cands, notes = a.review.Cands, a.review.Notes
	}
	p := a.plan
	v := organizeView{
		ID: a.id, Mode: a.req.Mode, Config: a.cfg, Notes: notes, Summary: summarize(cands, p),
		Songs: make([]songView, len(cands)), PossibleDuplicates: p.PossibleDuplicates, Similar: []similarView{},
		Excluded: []string{}, Joined: [][2]string{}, Applied: a.applied,
	}
	if a.join != nil {
		v.Joined = a.join
	}
	if v.Notes == nil {
		v.Notes = []string{}
	}
	if v.PossibleDuplicates == nil {
		v.PossibleDuplicates = [][]int{}
	}
	for i := range cands {
		c, d := &cands[i], p.Decisions[i]
		q := c.Quality
		v.Songs[i] = songView{
			Ref: c.Ref(), Label: c.Label, Rel: c.Rel, Folder: c.Folder, Artist: c.Artist, Title: c.Title,
			Tier: c.Tier + 1, Duet: c.Duet, InLibrary: c.InLibrary, MarkedCopy: c.MarkedCopy, Errors: c.Errors,
			Quality: qualityView{
				Warnings: q.SongWarnings, Video: q.HasVideo, VideoHeight: q.VideoHeight, VideoKbps: q.VideoBitrate / 1000,
				AudioKbps: q.AudioBitrate / 1000, Cover: q.HasCover, Background: q.HasBackground, Version: q.HasVersion,
			},
			Action: string(d.Action), Target: d.Target, Reason: d.Reason, Other: d.Other,
			Excluded: a.exclude[c.Ref()],
		}
	}
	for _, sp := range p.Similar {
		v.Similar = append(v.Similar, similarView{A: sp.A, B: sp.B, Evidence: sp.Evidence, Possible: sp.Possible})
	}
	for ref := range a.exclude {
		v.Excluded = append(v.Excluded, ref)
	}
	sort.Strings(v.Excluded)
	if t := a.req.Tidy; t != nil {
		tv := &tidyView{Options: *t, Steps: a.steps, Done: a.tidyDone, Songs: a.review != nil}
		if tv.Steps == nil {
			tv.Steps = []stepResult{}
		}
		if a.tidy != nil {
			tv.tidyPlan = *a.tidy
		} else {
			tv.tidyPlan = tidyPlan{TextFiles: []string{}, Leftovers: []string{}, BrokenFolders: []string{}}
		}
		v.Tidy = tv
	}
	return v
}
