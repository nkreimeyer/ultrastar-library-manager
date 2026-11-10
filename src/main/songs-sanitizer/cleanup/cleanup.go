// Package cleanup removes clutter from song folders: files that the song txt does not
// reference are moved to <out>/extra-files/, folders with at most one entry are moved to
// <out>/invalid/. Both targets mirror the folder's path relative to the library root.
package cleanup

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"github.com/nkreimeyer/ultrastar-songs-sanitizer/config"
	"github.com/nkreimeyer/ultrastar-songs-sanitizer/fsops"
	"github.com/nkreimeyer/ultrastar-songs-sanitizer/utils"

	log "github.com/sirupsen/logrus"
)

// batchSize is the number of song folders cleaned up concurrently.
const batchSize = 100

// Run performs the cleanup on every song folder below cfg.SongDir. It only returns an
// error if the library cannot be read at all; per-song problems are logged and counted
// by ops.
func Run(cfg config.Config, ops *fsops.Ops) error {
	entries, err := utils.GetAllSongFolders(ops, cfg.SongDir)
	if err != nil {
		return fmt.Errorf("listing song folders: %w", err)
	}

	validEntries := filterEntries(cfg, ops, entries)

	for _, batch := range utils.SplitIntoBatches(validEntries, batchSize) {
		var wg sync.WaitGroup
		for _, entry := range batch {
			wg.Add(1)
			go func(entry string) {
				defer wg.Done()
				cleanupDirectory(cfg, ops, entry)
			}(entry)
		}
		wg.Wait()
	}
	return nil
}

// filterEntries moves folders with at most one entry to the invalid folder and returns the
// remaining folders. Moved (or, in dry-run mode, to-be-moved) folders and unreadable
// folders are excluded from further cleanup.
func filterEntries(cfg config.Config, ops *fsops.Ops, entries []string) []string {
	validEntries := make([]string, 0, len(entries))
	for _, entry := range entries {
		files, err := os.ReadDir(entry)
		if err != nil {
			log.Errorf("reading song folder %q: %v", entry, err)
			ops.Fail()
			continue
		}
		if len(files) <= 1 {
			log.Infof("Song folder %q has %d entries, moving it to invalid", entry, len(files))
			if _, err := ops.MoveInto(entry, cfg.InvalidDir(), cfg.RelPath(entry)); err != nil {
				log.Errorf("moving broken song folder: %v", err)
			}
			continue
		}
		validEntries = append(validEntries, entry)
	}
	return validEntries
}

// cleanupDirectory moves all files of songFolder that its song txt does not reference to
// extra-files/<relative song path>/.
func cleanupDirectory(cfg config.Config, ops *fsops.Ops, songFolder string) {
	songFolder = filepath.Clean(songFolder)
	files, err := os.ReadDir(songFolder)
	if err != nil {
		log.Errorf("reading song folder %q: %v", songFolder, err)
		ops.Fail()
		return
	}
	for _, file := range files {
		handleFileCleanup(cfg, ops, songFolder, file)
	}
}

// handleFileCleanup moves a single file to the extra-files folder unless it must be kept.
func handleFileCleanup(cfg config.Config, ops *fsops.Ops, songFolder string, file os.DirEntry) {
	name := file.Name()
	if !shouldConsider(file) || utils.IsFileReferenced(songFolder, name) {
		return
	}
	src := filepath.Join(songFolder, name)
	rel := filepath.Join(cfg.RelPath(songFolder), name)
	if _, err := ops.MoveInto(src, cfg.ExtraFilesDir(), rel); err != nil {
		log.Errorf("moving unreferenced file to extra-files: %v", err)
	}
}

// shouldConsider reports whether file is a candidate for removal at all. Folders, usdb
// files and txt files (any case) are always left alone. Temp files left behind by an
// interrupted run are not referenced, so they are moved to extra-files like any other
// clutter; modules run one after another, so no temp file is in use while cleanup runs.
func shouldConsider(file os.DirEntry) bool {
	name := file.Name()
	switch {
	case file.IsDir():
		return false
	case strings.EqualFold(filepath.Ext(name), ".usdb"):
		return false
	case utils.IsTxtFile(name):
		return false
	}
	return true
}
