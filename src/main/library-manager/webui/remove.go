package webui

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"slices"
	"strings"

	log "github.com/sirupsen/logrus"

	"github.com/nkreimeyer/library-manager/config"
	"github.com/nkreimeyer/library-manager/fsops"
	"github.com/nkreimeyer/library-manager/organize"
)

// removeReportFile is the JSON report of the remove subcommand in the output folder.
const removeReportFile = "remove_plan.json"

// removeRequest is what the UI sends for a remove run: the patterns are the text of a
// patterns file (one per line: "Artist - Title", an artist, or an "artist:"/"song:" prefix).
type removeRequest struct {
	Library  string `json:"library"`
	Out      string `json:"out"`
	Patterns string `json:"patterns"`
}

// parse returns the patterns (at least one) of the request.
func (q removeRequest) parse() ([]organize.Pattern, error) {
	ps, err := organize.ParsePatternFile(strings.NewReader(q.Patterns), "patterns")
	if err != nil {
		return nil, err
	}
	if len(ps) == 0 {
		return nil, errors.New("give at least one artist or song to remove")
	}
	return ps, nil
}

func (q removeRequest) config(dry bool) (config.OrganizeConfig, error) {
	return organize.NewConfig(organize.ModeRemove, nil, q.Library, q.Out, dry)
}

// removeAnalysis is a dry run of a remove kept for review and apply.
type removeAnalysis struct {
	id      string
	req     removeRequest
	cfg     configView
	report  json.RawMessage // remove_plan.json of the dry run
	removed []string        // the songs it would remove (paths in the library), sorted
	applied bool
}

// removeView is the review of a remove check: the report with the analysis' id.
type removeView struct {
	ID      string          `json:"id"`
	Config  configView      `json:"config"`
	Report  json.RawMessage `json:"report"`
	Applied bool            `json:"applied"`
}

// removedSongs reads the songs a remove report removes (or would remove).
func removedSongs(report []byte) ([]string, error) {
	var r struct {
		Removed []struct {
			Song string `json:"song"`
		} `json:"removed"`
	}
	if err := json.Unmarshal(report, &r); err != nil {
		return nil, err
	}
	songs := make([]string, 0, len(r.Removed))
	for _, e := range r.Removed {
		songs = append(songs, e.Song)
	}
	slices.Sort(songs)
	return songs, nil
}

// handleRemoveValidate checks a remove request (folders and patterns) and returns the
// resolved folders like the organize validation does.
func (s *Server) handleRemoveValidate(w http.ResponseWriter, r *http.Request) {
	var q removeRequest
	if !readJSON(w, r, &q) {
		return
	}
	cfg, err := q.config(true)
	if err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	if strings.TrimSpace(q.Patterns) != "" {
		if _, err := q.parse(); err != nil {
			writeError(w, http.StatusBadRequest, err)
			return
		}
	}
	writeJSON(w, http.StatusOK, viewConfig(cfg))
}

func (s *Server) handleRemoveAnalyse(w http.ResponseWriter, r *http.Request) {
	var q removeRequest
	if !readJSON(w, r, &q) {
		return
	}
	patterns, err := q.parse()
	if err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	cfg, err := q.config(true)
	if err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	a := &removeAnalysis{id: s.newID("r"), req: q, cfg: viewConfig(cfg)}
	ops := fsops.New(true)
	s.startJob(w, a.id, "remove-analyse", ops, func(ctx context.Context, c jobControl) (any, error) {
		if err := cfg.PrepareOutDir(); err != nil {
			return nil, err
		}
		log.Infof("Checking remove (dry run, nothing is changed); library %s, output %s", cfg.Library, cfg.OutDir)
		if err := organize.RemoveWith(cfg, ops, patterns, organize.RemoveOptions{Ctx: ctx, Progress: c.Progress}); err != nil {
			return nil, err
		}
		data, err := os.ReadFile(cfg.ReportPath(removeReportFile))
		if err != nil {
			return nil, fmt.Errorf("the run made no plan: %w", err)
		}
		a.removed, err = removedSongs(data)
		if err != nil {
			return nil, fmt.Errorf("reading the plan: %w", err)
		}
		a.report = data
		s.mu.Lock()
		s.removal = a // only the newest analysis is kept
		s.mu.Unlock()
		return map[string]any{"analysis": a.id, "removed": len(a.removed)}, nil
	})
}

func (s *Server) removeAnalysis(id string) (*removeAnalysis, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if a := s.removal; a != nil && a.id == id {
		return a, nil
	}
	return nil, fmt.Errorf("no such analysis %q; check again", id)
}

func (s *Server) handleRemoveAnalysis(w http.ResponseWriter, r *http.Request) {
	a, err := s.removeAnalysis(r.URL.Query().Get("id"))
	if err != nil {
		writeError(w, http.StatusNotFound, err)
		return
	}
	s.mu.Lock()
	v := removeView{ID: a.id, Config: a.cfg, Report: a.report, Applied: a.applied}
	s.mu.Unlock()
	writeJSON(w, http.StatusOK, v)
}

// removeProgressHook, if set (tests), is called after each progress report of the real
// remove run of an apply job.
var removeProgressHook func(phase string, done int)

var errRemoveApplied = errors.New("this plan was applied already; check again for a new plan")

// handleRemoveApply runs the reviewed remove for real. It plans again first (a dry run,
// like the check); if that would remove other songs than the reviewed plan, nothing is
// moved and the analysis shows the new plan.
func (s *Server) handleRemoveApply(w http.ResponseWriter, r *http.Request) {
	var q struct {
		Analysis string `json:"analysis"`
		// Keep lists songs of the reviewed plan (their path in the library) to leave in place.
		Keep []string `json:"keep"`
	}
	if !readJSON(w, r, &q) {
		return
	}
	a, err := s.removeAnalysis(q.Analysis)
	if err != nil {
		writeError(w, http.StatusNotFound, err)
		return
	}
	s.mu.Lock()
	applied := a.applied
	s.mu.Unlock()
	if applied {
		writeError(w, http.StatusConflict, errRemoveApplied)
		return
	}
	patterns, err := a.req.parse()
	if err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	// What the reviewed plan removes, less the songs to keep.
	s.mu.Lock()
	reviewed := slices.Clone(a.removed)
	s.mu.Unlock()
	keep := map[string]bool{}
	for _, p := range q.Keep {
		if !slices.Contains(reviewed, p) {
			writeError(w, http.StatusBadRequest, fmt.Errorf("%q is not a song of the reviewed plan", p))
			return
		}
		keep[p] = true
	}
	expected := slices.DeleteFunc(reviewed, func(p string) bool { return keep[p] })
	if len(expected) == 0 {
		writeError(w, http.StatusBadRequest, errors.New("every song is kept, so there is nothing to remove"))
		return
	}
	opts := organize.RemoveOptions{Keep: keep}
	cfg, err := a.req.config(false)
	if err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	dryCfg, err := a.req.config(true)
	if err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	ops := fsops.New(false)
	s.startJob(w, s.newID("a"), "remove-apply", ops, func(ctx context.Context, c jobControl) (any, error) {
		if err := cfg.PrepareOutDir(); err != nil {
			return nil, err
		}
		opts.Ctx = ctx
		// Plan again, without changing anything, and compare with what was reviewed. Only its
		// reading of the songs is shown as progress: its planned moves would look like moves.
		dryOpts := opts
		dryOpts.Progress = func(phase string, done, total int) {
			if phase == organize.PhaseInspect {
				c.Progress(phase, done, total)
			}
		}
		if err := organize.RemoveWith(dryCfg, fsops.New(true), patterns, dryOpts); err != nil {
			return nil, err
		}
		data, err := os.ReadFile(cfg.ReportPath(removeReportFile))
		if err != nil {
			return nil, fmt.Errorf("the run made no plan: %w", err)
		}
		now, err := removedSongs(data)
		if err != nil {
			return nil, fmt.Errorf("reading the plan: %w", err)
		}
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		if !slices.Equal(now, expected) {
			var diff []string
			for _, p := range now {
				if !slices.Contains(expected, p) {
					diff = append(diff, "now removed: "+p)
				}
			}
			for _, p := range expected {
				if !slices.Contains(now, p) {
					diff = append(diff, "no longer removed: "+p)
				}
			}
			n := len(diff)
			if n > maxDiffLines {
				diff = append(diff[:maxDiffLines], fmt.Sprintf("… and %d more", n-maxDiffLines))
			}
			// The analysis shows the plan without the songs to keep, as a fresh check would find it:
			// the changed songs matter, the choices of the reviewer are asked for again.
			if err := organize.RemoveWith(dryCfg, fsops.New(true), patterns, organize.RemoveOptions{Ctx: ctx}); err != nil {
				return nil, err
			}
			full, err := os.ReadFile(cfg.ReportPath(removeReportFile))
			if err != nil {
				return nil, fmt.Errorf("the run made no plan: %w", err)
			}
			all, err := removedSongs(full)
			if err != nil {
				return nil, fmt.Errorf("reading the plan: %w", err)
			}
			s.mu.Lock()
			a.removed, a.report = all, full
			s.mu.Unlock()
			return nil, &detailedError{
				msg:     fmt.Sprintf("%v: %d song(s) would be handled differently, so nothing was moved; review the new plan", organize.ErrPlanChanged, n),
				details: diff, result: map[string]any{"analysis": a.id, "planChanged": true},
			}
		}
		if err := s.claimApply(&a.applied, errRemoveApplied); err != nil {
			return nil, err
		}
		log.Infof("Applying remove: library %s, output %s", cfg.Library, cfg.OutDir)
		opts.Progress = c.Progress
		if removeProgressHook != nil {
			opts.Progress = func(phase string, done, total int) {
				c.Progress(phase, done, total)
				removeProgressHook(phase, done)
			}
		}
		// A stop ends the moves after the current one (Ctx); the job then ends as stopped
		// and the shared undo puts back what was moved.
		runErr := organize.RemoveWith(cfg, ops, patterns, opts)
		// Applied once something moved (or the run finished): a stop or a failure before the
		// first move can be applied again.
		s.markApplied(&a.applied, ops, ctx, runErr)
		res := map[string]any{"analysis": a.id, "outDir": cfg.OutDir, "library": cfg.Library}
		if data, err := os.ReadFile(cfg.ReportPath(removeReportFile)); err == nil {
			res["report"] = json.RawMessage(data)
		}
		return res, runErr
	})
}
