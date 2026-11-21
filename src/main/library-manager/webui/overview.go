package webui

import (
	"context"
	"io/fs"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"sync"

	log "github.com/sirupsen/logrus"

	"github.com/nkreimeyer/library-manager/fsops"
	"github.com/nkreimeyer/library-manager/organize"
	"github.com/nkreimeyer/library-manager/utils"
)

// overviewResult is the result of an overview job: what the library holds, counted from the
// folders and file names only (no txt is read, no media probed).
type overviewResult struct {
	Library   string        `json:"library"`
	OutDir    string        `json:"outDir"`
	Songs     int           `json:"songs"`
	Bytes     int64         `json:"bytes"`
	WithVideo int           `json:"withVideo"`
	WithImage int           `json:"withImage"` // a cover or background picture (any image file)
	NoAudio   int           `json:"noAudio"`   // no audio and no video file in the folder
	Buckets   []bucketCount `json:"buckets"`
	// SetAside counts what waits in the output folder: song folders in invalid/,
	// duplicates/ and removed/, files in extra-files/.
	SetAside map[string]int `json:"setAside"`
}

type bucketCount struct {
	Name  string `json:"name"`
	Songs int    `json:"songs"`
	Bytes int64  `json:"bytes"`
}

type songSummary struct {
	bucket                      string
	bytes                       int64
	video, image, audio, failed bool
}

// summarizeSong lists one song folder (its own files, not the subfolders).
func summarizeSong(lib, folder string) songSummary {
	s := songSummary{bucket: "(top level)"}
	if rel, err := filepath.Rel(lib, folder); err == nil {
		if first, _, ok := strings.Cut(filepath.ToSlash(rel), "/"); ok {
			s.bucket = first
		}
	}
	entries, err := os.ReadDir(folder)
	if err != nil {
		s.failed = true
		return s
	}
	for _, e := range entries {
		if e.IsDir() || utils.IsHidden(e.Name()) {
			continue
		}
		if info, err := e.Info(); err == nil && info.Mode().IsRegular() {
			s.bytes += info.Size()
		}
		switch {
		case utils.IsVideoFile(e.Name()):
			s.video = true
		case utils.IsAudioFile(e.Name()):
			s.audio = true
		case utils.IsImageFile(e.Name()):
			s.image = true
		}
	}
	return s
}

// handleOverview counts the library as a job (a big library takes a while).
func (s *Server) handleOverview(w http.ResponseWriter, r *http.Request) {
	var q folderRequest
	if !readJSON(w, r, &q) {
		return
	}
	cfg, err := organize.NewConfig(organize.ModeDedupe, nil, q.Library, q.Out, true) // the library must exist
	if err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	ops := fsops.New(true)
	s.startJob(w, s.newID("v"), "overview", ops, func(ctx context.Context, c jobControl) (any, error) {
		log.Infof("Counting the songs of %s", cfg.Library)
		folders, err := utils.GetAllSongFolders(ops, cfg.Library)
		if err != nil {
			return nil, err
		}
		res := overviewResult{Library: cfg.Library, OutDir: cfg.OutDir, Songs: len(folders), Buckets: []bucketCount{}, SetAside: map[string]int{}}
		byBucket := map[string]*bucketCount{}
		var mu sync.Mutex
		err = utils.ForEachParallel(ctx, folders, utils.Parallel{
			Workers:  min(16, runtime.NumCPU()),
			Progress: func(done, total int) { c.Progress("inspect", done, total) },
		}, func(_ int, folder string) {
			sum := summarizeSong(cfg.Library, folder)
			mu.Lock()
			defer mu.Unlock()
			res.Bytes += sum.bytes
			b := byBucket[sum.bucket]
			if b == nil {
				b = &bucketCount{Name: sum.bucket}
				byBucket[sum.bucket] = b
			}
			b.Songs++
			b.Bytes += sum.bytes
			if sum.video {
				res.WithVideo++
			}
			if sum.image {
				res.WithImage++
			}
			if !sum.audio && !sum.video && !sum.failed {
				res.NoAudio++
			}
		})
		if err != nil {
			return nil, err // stopped
		}
		for _, b := range byBucket {
			res.Buckets = append(res.Buckets, *b)
		}
		sort.Slice(res.Buckets, func(i, j int) bool { return res.Buckets[i].Name < res.Buckets[j].Name })
		for _, section := range organize.Sections {
			dir := filepath.Join(cfg.OutDir, section)
			if section == organize.SectionExtraFiles {
				n := 0
				_ = filepath.WalkDir(dir, func(_ string, d fs.DirEntry, err error) error {
					if err == nil && !d.IsDir() && !utils.IsHidden(d.Name()) {
						n++
					}
					return nil
				})
				res.SetAside[section] = n
			} else if sets, err := utils.GetAllSongFolders(ops, dir); err == nil {
				res.SetAside[section] = len(sets)
			}
		}
		return res, nil
	})
}
