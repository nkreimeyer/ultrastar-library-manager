package webui

import (
	"cmp"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"

	"github.com/nkreimeyer/library-manager/mediatools"
)

type systemInfo struct {
	Version     string            `json:"version"`
	OS          string            `json:"os"`
	Arch        string            `json:"arch"`
	Home        string            `json:"home,omitempty"`
	Tools       []mediatools.Tool `json:"tools"`
	ToolsOK     bool              `json:"toolsOK"`
	InstallHint string            `json:"installHint"`
	MinVersion  string            `json:"minVersion"`
}

func (s *Server) handleSystem(w http.ResponseWriter, r *http.Request) {
	tools := mediatools.Detect(r.Context())
	home, _ := os.UserHomeDir()
	writeJSON(w, http.StatusOK, systemInfo{
		Version: s.version, OS: runtime.GOOS, Arch: runtime.GOARCH, Home: home,
		Tools: tools, ToolsOK: mediatools.AllOK(tools), InstallHint: mediatools.InstallHint(runtime.GOOS),
		MinVersion: "5.1",
	})
}

type dirEntry struct {
	Name string `json:"name"`
	Path string `json:"path"`
}

type dirListing struct {
	Path   string     `json:"path"`
	Parent string     `json:"parent,omitempty"`
	Dirs   []dirEntry `json:"dirs"`
	// Roots are the starting points: the home folder and the drives (Windows) or "/".
	Roots []dirEntry `json:"roots"`
	Error string     `json:"error,omitempty"`
}

// handleFSList lists the folders (not files) of ?path= (default: the home folder).
// Hidden folders are left out unless ?hidden=1.
func (s *Server) handleFSList(w http.ResponseWriter, r *http.Request) {
	p := r.URL.Query().Get("path")
	if p == "" {
		p, _ = os.UserHomeDir()
	}
	if p == "" {
		p = string(filepath.Separator)
	}
	l := dirListing{Path: filepath.Clean(p), Dirs: []dirEntry{}, Roots: roots()}
	if abs, err := filepath.Abs(l.Path); err == nil {
		l.Path = abs
	}
	if parent := filepath.Dir(l.Path); parent != l.Path {
		l.Parent = parent
	}
	entries, err := os.ReadDir(l.Path)
	if err != nil {
		l.Error = err.Error()
	}
	hidden := r.URL.Query().Get("hidden") == "1"
	for _, e := range entries {
		if !hidden && strings.HasPrefix(e.Name(), ".") {
			continue
		}
		full := filepath.Join(l.Path, e.Name())
		isDir := e.IsDir()
		if e.Type()&os.ModeSymlink != 0 {
			st, err := os.Stat(full)
			isDir = err == nil && st.IsDir()
		}
		if isDir {
			l.Dirs = append(l.Dirs, dirEntry{Name: e.Name(), Path: full})
		}
	}
	slices.SortFunc(l.Dirs, func(a, b dirEntry) int {
		return cmp.Compare(strings.ToLower(a.Name), strings.ToLower(b.Name))
	})
	writeJSON(w, http.StatusOK, l)
}

func roots() []dirEntry {
	var rs []dirEntry
	if home, err := os.UserHomeDir(); err == nil {
		rs = append(rs, dirEntry{Name: "Home", Path: home})
	}
	if runtime.GOOS == "windows" {
		for c := 'A'; c <= 'Z'; c++ {
			d := string(c) + `:\`
			if _, err := os.Stat(d); err == nil {
				rs = append(rs, dirEntry{Name: string(c) + ":", Path: d})
			}
		}
		return rs
	}
	rs = append(rs, dirEntry{Name: "/", Path: "/"})
	for _, m := range []string{"/media", "/mnt", "/Volumes", "/run/media"} {
		if st, err := os.Stat(m); err == nil && st.IsDir() {
			rs = append(rs, dirEntry{Name: m, Path: m})
		}
	}
	return rs
}
