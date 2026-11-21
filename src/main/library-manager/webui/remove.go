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
		c.Progress("inspect", 0, 0)
		log.Infof("Checking remove (dry run, nothing is changed); library %s, output %s", cfg.Library, cfg.OutDir)
		if err := organize.Remove(cfg, ops, patterns); err != nil {
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

// handleRemoveApply runs the reviewed remove for real. It plans again first (a dry run,
// like the check); if that would remove other songs than the reviewed plan, nothing is
// moved and the analysis shows the new plan.
func (s *Server) handleRemoveApply(w http.ResponseWriter, r *http.Request) {
	var q struct {
		Analysis string `json:"analysis"`
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
		writeError(w, http.StatusConflict, errors.New("this plan was applied already; check again for a new plan"))
		return
	}
	patterns, err := a.req.parse()
	if err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
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
		c.Progress("inspect", 0, 0)
		// Plan again, without changing anything, and compare with what was reviewed.
		if err := organize.Remove(dryCfg, fsops.New(true), patterns); err != nil {
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
		if !slices.Equal(now, a.removed) {
			var diff []string
			for _, p := range now {
				if !slices.Contains(a.removed, p) {
					diff = append(diff, "now removed: "+p)
				}
			}
			for _, p := range a.removed {
				if !slices.Contains(now, p) {
					diff = append(diff, "no longer removed: "+p)
				}
			}
			n := len(diff)
			if n > maxDiffLines {
				diff = append(diff[:maxDiffLines], fmt.Sprintf("… and %d more", n-maxDiffLines))
			}
			s.mu.Lock()
			a.removed, a.report = now, data
			s.mu.Unlock()
			return nil, &detailedError{
				msg:     fmt.Sprintf("%v: %d song(s) would be handled differently, so nothing was moved; review the new plan", organize.ErrPlanChanged, n),
				details: diff, result: map[string]any{"analysis": a.id, "planChanged": true},
			}
		}
		log.Infof("Applying remove: library %s, output %s", cfg.Library, cfg.OutDir)
		s.mu.Lock()
		a.applied = true
		s.mu.Unlock()
		runErr := organize.Remove(cfg, ops, patterns)
		res := map[string]any{"analysis": a.id, "outDir": cfg.OutDir, "library": cfg.Library}
		if data, err := os.ReadFile(cfg.ReportPath(removeReportFile)); err == nil {
			res["report"] = json.RawMessage(data)
		}
		return res, runErr
	})
}
