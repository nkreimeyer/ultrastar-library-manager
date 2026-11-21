package utils

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/nkreimeyer/library-manager/config"
	"github.com/nkreimeyer/library-manager/fsops"
	log "github.com/sirupsen/logrus"
)

// GetAllSongFolders returns all song folders below songDir (songDir itself is never
// returned):
//
//   - a folder that directly contains a song txt (see IsSongTxt) is a song folder;
//   - a folder without one is a category folder if any of its subfolders contains a txt or
//     media file (at any depth); its own loose files (readme.txt, ...) are left alone;
//   - otherwise it is returned itself as a (probably broken) song folder, together with its
//     subfolders (e.g. an empty "covers/" folder).
//
// Hidden folders, Synology "@eaDir" folders and sanitizer output folders (containing
// config.OutputMarker) are skipped. In dry-run mode, folders and txts that an earlier
// module would have moved away are skipped too, and txts are read as an earlier module
// would have written them (ops.ReadFile). Unreadable folders and song txts are logged, counted
// once per run in ops (which may be nil) and skipped; only an unreadable songDir is an error.
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
		r := collectSongFolders(ops, filepath.Join(songDir, entry.Name()))
		songFolders = append(songFolders, r.folders...)
	}
	return songFolders, nil
}

// collected is the result of collectSongFolders for one folder.
type collected struct {
	folders []string // song folders at or below the folder
	content bool     // the folder or a subfolder directly contains a txt or media file
}

func collectSongFolders(ops *fsops.Ops, dir string) collected {
	entries, err := os.ReadDir(dir)
	if err != nil {
		skipUnreadable(ops, dir, err)
		return collected{content: true} // unknown content: never treat the parent as one song
	}

	var subdirs []string
	hasTxt, hasMedia := false, false
	for _, e := range entries {
		name := e.Name()
		switch {
		case name == config.OutputMarker || name == config.LegacyOutputMarker:
			log.Debugf("Skipping sanitizer output folder %s", dir)
			// Not content of a song, but never a reason to treat the parent as one song
			// (which would move the output folder along with it).
			return collected{content: true}
		case e.IsDir():
			if !skipFolder(ops, dir, name) {
				subdirs = append(subdirs, filepath.Join(dir, name))
			}
		case IsTxtFile(name) && IsRegularFile(dir, e):
			// A txt an earlier module would have moved away (dry run) is not there.
			if ops.WouldBeMoved(filepath.Join(dir, name)) {
				continue
			}
			hasTxt = true
			isSong, err := isSongTxt(ops, filepath.Join(dir, name))
			if err != nil {
				skipUnreadable(ops, filepath.Join(dir, name), err)
				return collected{content: true}
			}
			if isSong {
				return collected{folders: []string{dir}, content: true}
			}
		case !IsHidden(name) && (IsAudioFile(name) || IsVideoFile(name)):
			hasMedia = true
		}
	}

	var below []string
	contentBelow := false
	for _, sub := range subdirs {
		r := collectSongFolders(ops, sub)
		below = append(below, r.folders...)
		contentBelow = contentBelow || r.content
	}
	switch {
	case contentBelow:
		return collected{folders: below, content: true} // category folder
	default:
		// No txt or media anywhere below: this folder is a (broken or empty) song.
		return collected{folders: []string{dir}, content: hasTxt || hasMedia}
	}
}

// skipUnreadable logs an unreadable folder or file and counts it once per run.
func skipUnreadable(ops *fsops.Ops, path string, err error) {
	log.Errorf("Skipping unreadable %s: %v", path, err)
	if ops != nil {
		ops.FailUnreadable(path)
	}
}

// skipFolder reports whether the subfolder name of parent must not be scanned.
func skipFolder(ops *fsops.Ops, parent, name string) bool {
	if IsHidden(name) || name == "@eaDir" {
		return true
	}
	return ops.WouldBeMoved(filepath.Join(parent, name))
}
