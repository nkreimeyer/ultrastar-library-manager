package webui

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
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
}

func (q organizeRequest) mode() (organize.Mode, error) {
	switch m := organize.Mode(q.Mode); m {
	case organize.ModeOrganize, organize.ModeDedupe, organize.ModeAdd:
		return m, nil
	}
	return "", fmt.Errorf("unknown mode %q", q.Mode)
}

// config builds the configuration like the organize, dedupe and add subcommands do.
func (q organizeRequest) config(dry bool) (config.OrganizeConfig, error) {
	m, err := q.mode()
	if err != nil {
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
	switch m {
	case organize.ModeDedupe:
		return config.NewDedupe(q.Library, q.Out, dry)
	case organize.ModeAdd:
		return config.NewAdd(tiers, q.Library, q.Out, dry)
	}
	if len(tiers) == 0 {
		return config.OrganizeConfig{}, fmt.Errorf("at least one source folder is required")
	}
	if q.Library == "" {
		return config.OrganizeConfig{}, fmt.Errorf("a library folder is required")
	}
	return config.NewOrganize(tiers, q.Library, q.Out, dry)
}

func (q organizeRequest) options(m organize.Mode) organize.Options {
	return organize.Options{Mode: m, Priority: q.Priority, IgnoreThe: q.IgnoreThe, Deep: q.Deep}
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
	plan    organize.Plan // review.Plan with exclude applied
	applied bool
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
	a := &organizeAnalysis{id: s.newID("o"), req: q, cfg: viewConfig(cfg), exclude: map[string]bool{}}
	ops := fsops.New(true)
	job, err := s.jobs.start(a.id, "organize-analyse", ops, func(ctx context.Context, c jobControl) (any, error) {
		if err := cfg.PrepareOutDir(); err != nil {
			return nil, err
		}
		opts := q.options(m)
		opts.Ctx, opts.Progress = ctx, c.Progress
		opts.Review = func(rv *organize.Review) error {
			a.review, a.plan = rv, rv.Plan
			return nil
		}
		log.Infof("Checking %s (dry run, nothing is changed); library %s, output %s", m, cfg.Library, cfg.OutDir)
		if err := organize.Run(cfg, ops, opts); err != nil {
			return nil, err
		}
		if a.review == nil {
			return nil, fmt.Errorf("the run made no plan")
		}
		s.mu.Lock()
		// Only the newest analysis is kept: an older one holds every song of its library and
		// is not shown any more (applying it answers "check again").
		s.organize = map[string]*organizeAnalysis{a.id: a}
		s.mu.Unlock()
		return map[string]any{"analysis": a.id, "summary": summarize(a.review.Cands, a.plan)}, nil
	})
	if err != nil {
		writeError(w, http.StatusConflict, err)
		return
	}
	writeJSON(w, http.StatusAccepted, job)
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
	rv, applied := a.review, a.applied
	s.mu.Unlock()
	if applied {
		writeError(w, http.StatusConflict, fmt.Errorf("this plan was applied already"))
		return
	}
	plan := rv.Replan(ex) // pure, may take a moment for a big library
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

// maxDiffLines limits the plan differences shown.
var maxDiffLines = 200

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
	applied, exclude := a.applied, a.exclude
	s.mu.Unlock()
	if applied {
		writeError(w, http.StatusConflict, fmt.Errorf("this plan was applied already; check again for a new plan"))
		return
	}
	m, _ := a.req.mode()
	cfg, err := a.req.config(false)
	if err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	ops := fsops.New(false)
	job, err := s.jobs.start(s.newID("a"), "organize-apply", ops, func(ctx context.Context, c jobControl) (any, error) {
		if err := cfg.PrepareOutDir(); err != nil {
			return nil, err
		}
		opts := a.req.options(m)
		opts.Ctx, opts.Progress, opts.Exclude = ctx, c.Progress, exclude
		opts.Review = func(rv *organize.Review) error {
			s.mu.Lock()
			defer s.mu.Unlock()
			diff := organize.PlanDiff(a.review.Cands, a.plan, rv.Cands, rv.Plan)
			if len(diff) == 0 {
				a.applied = true
				return nil
			}
			a.review, a.plan = rv, rv.Plan
			n := len(diff)
			if n > maxDiffLines {
				diff = append(diff[:maxDiffLines], fmt.Sprintf("… and %d more", n-maxDiffLines))
			}
			return &detailedError{
				msg:     fmt.Sprintf("%v: %d song folder(s) would be handled differently, so nothing was moved; review the new plan", organize.ErrPlanChanged, n),
				details: diff, result: map[string]any{"analysis": a.id, "planChanged": true},
			}
		}
		log.Infof("Applying %s: library %s, output %s", m, cfg.Library, cfg.OutDir)
		runErr := organize.Run(cfg, ops, opts)
		res := map[string]any{"analysis": a.id, "outDir": cfg.OutDir, "library": cfg.Library}
		if data, err := os.ReadFile(cfg.ReportPath("organize_plan.json")); err == nil && a.applied {
			res["report"] = json.RawMessage(data)
		}
		return res, runErr // a changed plan's detailedError brings its own result
	})
	if err != nil {
		writeError(w, http.StatusConflict, err)
		return
	}
	writeJSON(w, http.StatusAccepted, job)
}

type folderRequest struct {
	Library string `json:"library"`
	Out     string `json:"out"`
	DryRun  bool   `json:"dryRun"`
}

func (q folderRequest) config() (config.OrganizeConfig, error) {
	return config.NewOrganize(nil, q.Library, q.Out, q.DryRun)
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
	job, err := s.jobs.start(s.newID("u"), "organize-undo", ops, func(ctx context.Context, c jobControl) (any, error) {
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
	if err != nil {
		writeError(w, http.StatusConflict, err)
		return
	}
	writeJSON(w, http.StatusAccepted, job)
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
	Applied            bool            `json:"applied"`
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
	cands, p := a.review.Cands, a.plan
	v := organizeView{
		ID: a.id, Mode: a.req.Mode, Config: a.cfg, Notes: a.review.Notes, Summary: summarize(cands, p),
		Songs: make([]songView, len(cands)), PossibleDuplicates: p.PossibleDuplicates, Similar: []similarView{},
		Excluded: []string{}, Applied: a.applied,
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
	return v
}
