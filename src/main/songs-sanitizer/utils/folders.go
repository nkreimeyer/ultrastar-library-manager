package utils

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/nkreimeyer/ultrastar-songs-sanitizer/config"
	"github.com/nkreimeyer/ultrastar-songs-sanitizer/fsops"
	log "github.com/sirupsen/logrus"
)

// GetAllSongFolders returns all song folders below songDir (songDir itself is never
// returned):
//
//   - a folder that directly contains a song txt (see IsSongTxt) is a song folder;
//   - a folder without one is a category folder if a song folder exists below it;
//   - otherwise, if it directly contains a .txt or a media file, or has no subfolders, it
//     is returned itself as a (probably broken) song folder, so a broken song is handled as
//     a whole together with its subfolders;
//   - a folder with only subfolders and no song below is a category of broken songs, and
//     those are returned individually (a category is never returned as one song).
//
// Hidden folders, Synology "@eaDir" folders and sanitizer output folders (containing
// config.OutputMarker) are skipped. In dry-run mode, folders that an earlier module would
// have moved away are skipped too. Unreadable folders are logged, counted as failures in
// ops (which may be nil) and skipped; only an unreadable songDir is an error.
func GetAllSongFolders(ops *fsops.Ops, songDir string) ([]string, error) {
	entries, err := os.ReadDir(songDir)
	if err != nil {
		return nil, fmt.Errorf("reading song dir %q: %w", songDir, err)
	}
	var songFolders []string
	for _, entry := range entries {
		if !entry.IsDir() || skipFolder(ops, songDir, entry.Name()) {
			continue
		}
		folders, _ := collectSongFolders(ops, filepath.Join(songDir, entry.Name()))
		songFolders = append(songFolders, folders...)
	}
	return songFolders, nil
}

// collectSongFolders returns the song folders at or below dir and whether at least one of
// them is a real song folder (contains a song txt).
func collectSongFolders(ops *fsops.Ops, dir string) ([]string, bool) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		log.Errorf("Skipping unreadable folder %s: %v", dir, err)
		if ops != nil {
			ops.Fail()
		}
		return nil, false
	}

	var subdirs []string
	hasTxt, hasMedia := false, false
	for _, e := range entries {
		name := e.Name()
		switch {
		case name == config.OutputMarker:
			log.Debugf("Skipping sanitizer output folder %s", dir)
			return nil, false
		case e.IsDir():
			if !skipFolder(ops, dir, name) {
				subdirs = append(subdirs, filepath.Join(dir, name))
			}
		case IsTxtFile(name) && IsRegularFile(dir, e):
			hasTxt = true
			if IsSongTxt(filepath.Join(dir, name)) {
				return []string{dir}, true
			}
		case !IsHidden(name) && (IsAudioFile(name) || IsVideoFile(name)):
			hasMedia = true
		}
	}

	var below []string
	hasRealSong := false
	for _, sub := range subdirs {
		folders, real := collectSongFolders(ops, sub)
		below = append(below, folders...)
		hasRealSong = hasRealSong || real
	}
	switch {
	case hasRealSong:
		return below, true // category folder
	case hasTxt || hasMedia || len(subdirs) == 0:
		return []string{dir}, false // broken song folder (or empty folder)
	default:
		return below, false // category containing only broken songs
	}
}

// skipFolder reports whether the subfolder name of parent must not be scanned.
func skipFolder(ops *fsops.Ops, parent, name string) bool {
	if IsHidden(name) || name == "@eaDir" {
		return true
	}
	return ops != nil && ops.WouldBeMoved(filepath.Join(parent, name))
}
