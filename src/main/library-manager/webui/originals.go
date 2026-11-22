package webui

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"net/http"
	"os"
	"path/filepath"
	"sort"

	log "github.com/sirupsen/logrus"

	"github.com/nkreimeyer/library-manager/config"
	"github.com/nkreimeyer/library-manager/fsops"
)

// originalsView is what the originals/ folder of an output folder holds: fixaudio's copies
// of the audio files it re-encoded (a tidy-up with "even out the volume").
type originalsView struct {
	Config  configView      `json:"config"`
	Dir     string          `json:"dir"`
	Files   int             `json:"files"`
	Bytes   int64           `json:"bytes"`
	Largest []originalsFile `json:"largest"` // the biggest files, at most maxOriginalsListed
}

type originalsFile struct {
	Rel   string `json:"rel"`
	Bytes int64  `json:"bytes"`
}

const maxOriginalsListed = 100

// checkOutputDir refuses an output folder without the output marker: originals/ in any
// other folder (the output field is free text in advanced mode) is not this program's, and
// deleting it could not be undone.
func checkOutputDir(outDir string) error {
	ok, err := config.IsOutputDir(outDir)
	if err != nil {
		return fmt.Errorf("checking %s: %w", outDir, err)
	}
	if !ok {
		return fmt.Errorf("%s is not an output folder of library-manager (it has no %s file), so its originals folder is not touched", outDir, config.OutputMarker)
	}
	return nil
}

// walkOriginals calls fn for every file below dir (symlinks are files, never followed).
func walkOriginals(dir string, fn func(path string, size int64)) error {
	err := filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			if p == dir && errors.Is(err, fs.ErrNotExist) {
				return fs.SkipAll
			}
			return err
		}
		if d.IsDir() {
			return nil
		}
		var size int64
		if info, err := d.Info(); err == nil {
			size = info.Size()
		}
		fn(p, size)
		return nil
	})
	return err
}

// handleOriginalsList reports the size of the originals/ folder.
func (s *Server) handleOriginalsList(w http.ResponseWriter, r *http.Request) {
	var q folderRequest
	if !readJSON(w, r, &q) {
		return
	}
	cfg, err := q.config()
	if err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	dir := config.Config{OutDir: cfg.OutDir}.OriginalsDir()
	v := originalsView{Config: viewConfig(cfg), Dir: dir, Largest: []originalsFile{}}
	// Without originals/ there is nothing to list, marker or not (no output folder yet).
	if _, err := os.Lstat(dir); err == nil {
		if err := checkOutputDir(cfg.OutDir); err != nil {
			writeError(w, http.StatusBadRequest, err)
			return
		}
	}
	var all []originalsFile
	if err := walkOriginals(dir, func(p string, size int64) {
		rel, _ := filepath.Rel(dir, p)
		all = append(all, originalsFile{Rel: filepath.ToSlash(rel), Bytes: size})
		v.Files++
		v.Bytes += size
	}); err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	sort.Slice(all, func(i, j int) bool {
		if all[i].Bytes != all[j].Bytes {
			return all[i].Bytes > all[j].Bytes
		}
		return all[i].Rel < all[j].Rel
	})
	v.Largest = append(v.Largest, all[:min(len(all), maxOriginalsListed)]...)
	writeJSON(w, http.StatusOK, v)
}

// handleOriginalsDelete deletes the copies in originals/. This is the one place the UI
// deletes files that are not a conversion's own original: the copies are backups, so it
// only runs for the output folder the user named and after a confirmation in the page.
// What was evened out stays evened out; only the way back to the unprocessed file is gone.
func (s *Server) handleOriginalsDelete(w http.ResponseWriter, r *http.Request) {
	var q folderRequest
	if !readJSON(w, r, &q) {
		return
	}
	cfg, err := q.config()
	if err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	if err := checkOutputDir(cfg.OutDir); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	dir := config.Config{OutDir: cfg.OutDir}.OriginalsDir()
	ops := fsops.New(false)
	s.startJob(w, s.newID("d"), "originals-delete", ops, func(ctx context.Context, c jobControl) (any, error) {
		var files []string
		if err := walkOriginals(dir, func(p string, _ int64) { files = append(files, p) }); err != nil {
			return nil, err
		}
		log.Infof("Deleting %d backup file(s) in %s", len(files), dir)
		for i, p := range files {
			if err := ctx.Err(); err != nil {
				return map[string]any{"library": cfg.Library, "outDir": cfg.OutDir}, err
			}
			c.Progress("delete", i, len(files))
			_ = ops.Remove(p) // failures are counted and logged by ops
		}
		c.Progress("delete", len(files), len(files))
		// The folders left empty, innermost first, then originals/ itself.
		var dirs []string
		_ = filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
			if err == nil && d.IsDir() {
				dirs = append(dirs, p)
			}
			return nil
		})
		sort.Slice(dirs, func(i, j int) bool { return len(dirs[i]) > len(dirs[j]) })
		for _, d := range dirs {
			if _, err := ops.RemoveEmptyDir(d); err != nil {
				break
			}
		}
		if _, err := os.Stat(dir); err == nil {
			log.Infof("%s is not empty: something was left in it", dir)
		}
		return map[string]any{"library": cfg.Library, "outDir": cfg.OutDir}, nil
	})
}
