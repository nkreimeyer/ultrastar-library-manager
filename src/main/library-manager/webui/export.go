package webui

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"path/filepath"

	log "github.com/sirupsen/logrus"

	"github.com/nkreimeyer/library-manager/export"
	"github.com/nkreimeyer/library-manager/fsops"
	"github.com/nkreimeyer/library-manager/organize"
)

// exportRequest is what the UI sends for the song list: the library and the output folder.
// (The folders are validated with /api/check/validate, which resolves them the same way.)
type exportRequest struct {
	Library string `json:"library"`
	Out     string `json:"out"`
}

// exportResult is the result of an export job.
type exportResult struct {
	ID      string `json:"id"`
	Library string `json:"library"`
	OutDir  string `json:"outDir"`
	File    string `json:"file"`
	Songs   int    `json:"songs"`
}

// exportFile is the newest exported CSV, which handleExportDownload serves.
type exportFile struct {
	id, path string
}

// handleExport writes the song list of the library as CSV into the output folder. The
// library is only read.
func (s *Server) handleExport(w http.ResponseWriter, r *http.Request) {
	var q exportRequest
	if !readJSON(w, r, &q) {
		return
	}
	cfg, err := organize.NewConfig(organize.ModeDedupe, nil, q.Library, q.Out, true) // the library must exist
	if err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	ops := fsops.New(true) // only the CSV, a report, is written
	id := s.newID("x")
	s.startJob(w, id, "export", ops, func(ctx context.Context, c jobControl) (any, error) {
		if err := cfg.PrepareOutDir(); err != nil {
			return nil, err
		}
		log.Infof("Listing the songs of %s", cfg.Library)
		path := cfg.ReportPath(export.FileName)
		n, err := export.Run(ops, cfg.Library, path, true, export.Options{Ctx: ctx,
			Progress: func(done, total int) { c.Progress("export", done, total) }})
		if err != nil {
			return nil, err
		}
		log.Infof("Wrote %d songs to %s", n, path)
		s.mu.Lock()
		s.exportRes = &exportFile{id: id, path: path}
		s.mu.Unlock()
		return exportResult{ID: id, Library: cfg.Library, OutDir: cfg.OutDir, File: path, Songs: n}, nil
	})
}

// handleExportDownload sends the CSV of the export job id as a download. Only the file the
// newest export job wrote is served, never a path from the request.
func (s *Server) handleExportDownload(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	f := s.exportRes
	s.mu.Unlock()
	if f == nil || f.id != r.URL.Query().Get("id") {
		writeError(w, http.StatusNotFound, fmt.Errorf("no such song list %q; export again", r.URL.Query().Get("id")))
		return
	}
	data, err := os.ReadFile(f.path)
	if err != nil {
		writeError(w, http.StatusNotFound, fmt.Errorf("the song list is gone: %w", err))
		return
	}
	w.Header().Set("Content-Type", "text/csv; charset=utf-8")
	w.Header().Set("Content-Disposition", `attachment; filename="`+filepath.Base(f.path)+`"`)
	_, _ = w.Write(data)
}
