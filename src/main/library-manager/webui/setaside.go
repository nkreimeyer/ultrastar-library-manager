package webui

import (
	"context"
	"errors"
	"net/http"

	log "github.com/sirupsen/logrus"

	"github.com/nkreimeyer/library-manager/fsops"
	"github.com/nkreimeyer/library-manager/organize"
)

var errNothingChosen = errors.New("choose at least one item to put back")

// handleSetAsideValidate resolves the folders of the set-aside browser (the library and
// its output folder, as the undo does).
func (s *Server) handleSetAsideValidate(w http.ResponseWriter, r *http.Request) {
	var q folderRequest
	if !readJSON(w, r, &q) {
		return
	}
	cfg, err := q.config()
	if err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	writeJSON(w, http.StatusOK, viewConfig(cfg))
}

// handleSetAsideList lists what is set aside in the output folder. It only reads.
func (s *Server) handleSetAsideList(w http.ResponseWriter, r *http.Request) {
	var q folderRequest
	if !readJSON(w, r, &q) {
		return
	}
	cfg, err := q.config()
	if err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	items, err := organize.ListSetAside(cfg, fsops.New(true))
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"config": viewConfig(cfg), "items": items})
}

// handleSetAsideRestore puts the chosen items back. One journal run, so the undo of the
// output folder reverses it.
func (s *Server) handleSetAsideRestore(w http.ResponseWriter, r *http.Request) {
	var q struct {
		folderRequest
		Items []organize.RestoreRef `json:"items"`
	}
	if !readJSON(w, r, &q) {
		return
	}
	cfg, err := q.config()
	if err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	if len(q.Items) == 0 {
		writeError(w, http.StatusBadRequest, errNothingChosen)
		return
	}
	ops := fsops.New(false)
	s.startJob(w, s.newID("s"), "setaside-restore", ops, func(ctx context.Context, c jobControl) (any, error) {
		if err := cfg.PrepareOutDir(); err != nil {
			return nil, err
		}
		log.Infof("Putting back %d item(s) from %s", len(q.Items), cfg.OutDir)
		err := organize.Restore(cfg, ops, q.Items, organize.RestoreOptions{Ctx: ctx,
			Progress: func(done, total int) { c.Progress("restore", done, total) }})
		return map[string]any{"library": cfg.Library, "outDir": cfg.OutDir}, err
	})
}
