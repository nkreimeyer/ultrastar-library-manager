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
// Hidden folders, Synology "@eaDir" folders and output folders (containing
// config.OutputMarker or the legacy marker) are skipped. Symlinks to folders are not
// followed (no loops, no songs from outside the library); they are logged and count as
// content, so their parent is never returned as one (broken) song. In dry-run mode, folders and txts that an earlier
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
		if IsNoiseEntry(ops, songDir, entry.Name()) {
			continue
		}
		if !entry.IsDir() {
			skipSymlinkedDir(songDir, entry)
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
	hasTxt, hasMedia, linkedDir := false, false, false
	for _, e := range entries {
		name := e.Name()
		switch {
		case config.IsOutputMarker(name):
			log.Debugf("Skipping output folder %s", dir)
			// Not content of a song, but never a reason to treat the parent as one song
			// (which would move the output folder along with it).
			return collected{content: true}
		case e.IsDir():
			if !IsNoiseEntry(ops, dir, name) {
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
		case !IsNoiseEntry(ops, dir, name) && skipSymlinkedDir(dir, e):
			linkedDir = true
		}
	}

	var below []string
	contentBelow := linkedDir // unknown content behind the link, like an unreadable folder
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

// skipSymlinkedDir reports whether e, an entry of dir, is a symlink to a folder, and logs
// that it is skipped. Discovery does not follow such links.
func skipSymlinkedDir(dir string, e os.DirEntry) bool {
	if e.Type()&os.ModeSymlink == 0 {
		return false
	}
	path := filepath.Join(dir, e.Name())
	if fi, err := os.Stat(path); err != nil || !fi.IsDir() {
		return false
	}
	log.Infof("Skipping symlinked folder %s (links are not followed)", path)
	return true
}

// skipUnreadable logs an unreadable folder or file and counts it once per run.
func skipUnreadable(ops *fsops.Ops, path string, err error) {
	log.Errorf("Skipping unreadable %s: %v", path, err)
	if ops != nil {
		ops.FailUnreadable(path)
	}
}

// IsNoiseEntry reports whether the entry name of parent is no song content: a hidden file
// or folder (leftovers of interrupted runs, macOS "._*" files), a Synology "@eaDir" folder,
// or, in a dry run, an entry an earlier module would have moved away (ops.WouldBeMoved; ops
// may be nil). Discovery does not scan such folders and cleanup does not count such
// entries.
func IsNoiseEntry(ops *fsops.Ops, parent, name string) bool {
	if IsHidden(name) || name == "@eaDir" {
		return true
	}
	return ops.WouldBeMoved(filepath.Join(parent, name))
}
