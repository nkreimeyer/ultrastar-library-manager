package webui

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"

	log "github.com/sirupsen/logrus"

	"github.com/nkreimeyer/library-manager/check"
	"github.com/nkreimeyer/library-manager/config"
	"github.com/nkreimeyer/library-manager/fsops"
	"github.com/nkreimeyer/library-manager/organize"
)

// checkReportFile is the JSON report of the check module in the output folder.
const checkReportFile = "check_report.json"

// checkRequest is what the UI sends for a library check.
type checkRequest struct {
	Library string `json:"library"`
	Out     string `json:"out"`
	Deep    bool   `json:"deep"`
}

// config returns the module configuration: the output folder is the one the other tasks of
// the UI use (organize-output next to the library), so a library has one folder to look in.
func (q checkRequest) config(dry bool) (config.Config, configView, error) {
	oc, err := organize.NewConfig(organize.ModeDedupe, nil, q.Library, q.Out, dry)
	if err != nil {
		return config.Config{}, configView{}, err
	}
	cfg, err := config.New(oc.Library, oc.OutDir, dry)
	return cfg, viewConfig(oc), err
}

// checkAnalysis is the check of a library kept for the review and the "move broken" apply.
type checkAnalysis struct {
	id      string
	req     checkRequest
	cfg     configView
	report  json.RawMessage // check_report.json of the check
	applied bool
}

type checkView struct {
	ID      string          `json:"id"`
	Config  configView      `json:"config"`
	Deep    bool            `json:"deep"`
	Report  json.RawMessage `json:"report"`
	Applied bool            `json:"applied"`
}

// handleCheckValidate resolves the folders of a check request.
func (s *Server) handleCheckValidate(w http.ResponseWriter, r *http.Request) {
	var q checkRequest
	if !readJSON(w, r, &q) {
		return
	}
	_, cv, err := q.config(true)
	if err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	writeJSON(w, http.StatusOK, cv)
}

// handleCheckAnalyse checks the library. The check never changes a song folder (a dry
// run: only the reports are written), so there is nothing to confirm before it.
func (s *Server) handleCheckAnalyse(w http.ResponseWriter, r *http.Request) {
	var q checkRequest
	if !readJSON(w, r, &q) {
		return
	}
	cfg, cv, err := q.config(true)
	if err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	a := &checkAnalysis{id: s.newID("c"), req: q, cfg: cv}
	ops := fsops.New(true)
	s.startJob(w, a.id, "check-analyse", ops, func(ctx context.Context, c jobControl) (any, error) {
		if err := cfg.PrepareOutDir(); err != nil {
			return nil, err
		}
		log.Infof("Checking the songs (nothing is changed); library %s, output %s", cfg.SongDir, cfg.OutDir)
		err := check.Run(cfg, ops, check.Options{Deep: q.Deep, Ctx: ctx, Progress: func(done, total int) { c.Progress("inspect", done, total) }})
		if err != nil {
			return nil, err
		}
		data, err := os.ReadFile(cfg.ReportPath(checkReportFile))
		if err != nil {
			return nil, fmt.Errorf("the check made no report: %w", err)
		}
		a.report = data
		s.mu.Lock()
		s.checkRes = a // only the newest check is kept
		s.mu.Unlock()
		return map[string]any{"analysis": a.id}, nil
	})
}

func (s *Server) checkAnalysis(id string) (*checkAnalysis, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if a := s.checkRes; a != nil && a.id == id {
		return a, nil
	}
	return nil, fmt.Errorf("no such check %q; check again", id)
}

func (s *Server) handleCheckAnalysis(w http.ResponseWriter, r *http.Request) {
	a, err := s.checkAnalysis(r.URL.Query().Get("id"))
	if err != nil {
		writeError(w, http.StatusNotFound, err)
		return
	}
	s.mu.Lock()
	v := checkView{ID: a.id, Config: a.cfg, Deep: a.req.Deep, Report: a.report, Applied: a.applied}
	s.mu.Unlock()
	writeJSON(w, http.StatusOK, v)
}

// handleCheckApply checks the library again and moves the songs with errors to invalid/
// (check's MoveBroken; songs with an encoding problem stay). These moves are not
// journaled, so the organize undo does not cover them: the folders are in
// <out>/invalid and can be moved back by hand.
func (s *Server) handleCheckApply(w http.ResponseWriter, r *http.Request) {
	var q struct {
		Analysis string `json:"analysis"`
	}
	if !readJSON(w, r, &q) {
		return
	}
	a, err := s.checkAnalysis(q.Analysis)
	if err != nil {
		writeError(w, http.StatusNotFound, err)
		return
	}
	s.mu.Lock()
	applied := a.applied
	s.mu.Unlock()
	if applied {
		writeError(w, http.StatusConflict, errors.New("the broken songs were moved already; check again"))
		return
	}
	cfg, cv, err := a.req.config(false)
	if err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	ops := fsops.New(false)
	s.startJob(w, s.newID("a"), "check-apply", ops, func(ctx context.Context, c jobControl) (any, error) {
		if err := cfg.PrepareOutDir(); err != nil {
			return nil, err
		}
		log.Infof("Moving broken songs to invalid/: library %s, output %s", cfg.SongDir, cfg.OutDir)
		s.mu.Lock()
		a.applied = true
		s.mu.Unlock()
		runErr := check.Run(cfg, ops, check.Options{Deep: a.req.Deep, MoveBroken: true, Ctx: ctx,
			Progress: func(done, total int) { c.Progress("inspect", done, total) }})
		res := map[string]any{"analysis": a.id, "outDir": cv.OutDir, "library": cv.Library}
		if data, err := os.ReadFile(cfg.ReportPath(checkReportFile)); err == nil && runErr == nil {
			res["report"] = json.RawMessage(data)
		}
		return res, runErr
	})
}
