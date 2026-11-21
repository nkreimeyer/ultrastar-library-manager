package webui

import (
	"errors"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/nkreimeyer/library-manager/utils"
)

// maxReportBytes is how much of a report the UI shows (the rest is cut off).
const maxReportBytes = 2 << 20

// reportFile is one report of the output folder.
type reportFile struct {
	Name     string    `json:"name"`
	Size     int64     `json:"size"`
	Modified time.Time `json:"modified"`
}

// isReportName says whether a top-level file of the output folder is a report the UI
// shows: a .txt or .json file that is no journal (those are records for undoing).
func isReportName(name string) bool {
	if utils.IsHidden(name) || name == "media-journal.txt" {
		return false
	}
	switch strings.ToLower(filepath.Ext(name)) {
	case ".txt", ".json":
		return true
	}
	return false
}

// handleReportsList lists the reports in the output folder, newest first.
func (s *Server) handleReportsList(w http.ResponseWriter, r *http.Request) {
	var q folderRequest
	if !readJSON(w, r, &q) {
		return
	}
	cfg, err := q.config()
	if err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	files := []reportFile{}
	entries, err := os.ReadDir(cfg.OutDir)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	for _, e := range entries {
		if !e.Type().IsRegular() || !isReportName(e.Name()) {
			continue
		}
		if info, err := e.Info(); err == nil {
			files = append(files, reportFile{Name: e.Name(), Size: info.Size(), Modified: info.ModTime()})
		}
	}
	sort.Slice(files, func(i, j int) bool {
		if !files[i].Modified.Equal(files[j].Modified) {
			return files[i].Modified.After(files[j].Modified)
		}
		return files[i].Name < files[j].Name
	})
	writeJSON(w, http.StatusOK, map[string]any{"config": viewConfig(cfg), "files": files})
}

// handleReportsRead returns the text of one report.
func (s *Server) handleReportsRead(w http.ResponseWriter, r *http.Request) {
	var q struct {
		folderRequest
		Name string `json:"name"`
	}
	if !readJSON(w, r, &q) {
		return
	}
	cfg, err := q.config()
	if err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	if q.Name != filepath.Base(q.Name) || !isReportName(q.Name) {
		writeError(w, http.StatusBadRequest, errors.New("not a report of the output folder"))
		return
	}
	path := filepath.Join(cfg.OutDir, q.Name)
	if st, err := os.Lstat(path); err != nil || !st.Mode().IsRegular() {
		writeError(w, http.StatusNotFound, errors.New("no such report"))
		return
	}
	f, err := os.Open(path)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	defer f.Close()
	data, err := io.ReadAll(io.LimitReader(f, maxReportBytes+1))
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	truncated := len(data) > maxReportBytes
	if truncated {
		data = data[:maxReportBytes]
	}
	writeJSON(w, http.StatusOK, map[string]any{"name": q.Name, "text": strings.ToValidUTF8(string(data), "�"), "truncated": truncated})
}
