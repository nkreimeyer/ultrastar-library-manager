package cleanup

import (
	"os"
	"path/filepath"
	"strings"

	"github.com/nkreimeyer/ultrastar-songs-sanitizer/utils"

	log "github.com/sirupsen/logrus"
)

// Run performs the cleanup operations on the given song directory.
func Run(songDir, logLevel string) {
	// Set log level
	level, _ := log.ParseLevel(logLevel)
	log.SetLevel(level)

	// Create invalid directory
	invalidDir := filepath.Join(filepath.Dir(songDir), "invalid")
	if _, err := os.Stat(invalidDir); os.IsNotExist(err) {
		err := os.Mkdir(invalidDir, 0755)
		if err != nil {
			log.Errorf("creating invalid directory: %v\n", err)
			return
		}
	}

	// Read all directories in songDir
	entries, err := os.ReadDir(songDir)
	if err != nil {
		log.Errorf("reading song directory: %v\n", err)
		return
	}

	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}

		dirPath := filepath.Join(songDir, entry.Name())
		files, err := os.ReadDir(dirPath)
		if err != nil {
			log.Errorf("reading directory %q: %v\n", dirPath, err)
			continue
		}

		if len(files) == 0 {
			log.Infof("Removing empty folder: %s\n", dirPath)
			err = os.Rename(dirPath, filepath.Join(invalidDir, entry.Name()))
			if err != nil {
				log.Errorf("moving empty folder: %v\n", err)
			}
			continue
		}

		if len(files) == 1 {
			log.Infof("Removing folder with only one file: %s\n", dirPath)
			err = os.Rename(dirPath, filepath.Join(invalidDir, entry.Name()))
			if err != nil {
				log.Errorf("moving single-file folder: %v\n", err)
			}
			continue
		}

		// Run utils.CleanupFolder on valid folders
		cleanupDirectory(dirPath)
	}
}

func cleanupDirectory(dirPath string) {
	dirPath = filepath.Clean(dirPath)
	// set the target directory for extra files to be moved to
	// this is one level up from the song directory, thus 2 levels up from dirpath
	parentPath := filepath.Dir(filepath.Dir(dirPath))
	// set the target directory for extra files to be moved to
	extraFilesTargetDir := filepath.Join(parentPath, "extra-files")
	// Get all files in the directory
	files, err := os.ReadDir(filepath.Clean(dirPath))
	if err != nil {
		log.Errorf("reading directory %q: %v\n", dirPath, err)
		return
	}

	// Create the cleanup directory if it doesn't exist
	if _, err := os.Stat(extraFilesTargetDir); os.IsNotExist(err) {
		err := os.Mkdir(extraFilesTargetDir, 0755)
		if err != nil {
			log.Errorf("creating cleanup directory %q: %v\n", extraFilesTargetDir, err)
			return
		}
	}

	for _, file := range files {
		if file.IsDir() {
			continue
		}
		// Move files that are not referenced in the txt file.
		if !utils.IsFileReferenced(dirPath, file.Name()) && !strings.Contains(file.Name(), ".txt") {
			// Create a directory that matches the song directory name
			songDirName := filepath.Base(dirPath)
			cleanupSongDir := filepath.Join(extraFilesTargetDir, songDirName)
			if _, err := os.Stat(cleanupSongDir); os.IsNotExist(err) {
				err := os.MkdirAll(cleanupSongDir, 0755)
				if err != nil {
					log.Errorf("creating cleanup directory %q: %v\n", cleanupSongDir, err)
					continue
				}
			}
			// Move the file to the cleanup directory
			srcPath := filepath.Join(dirPath, file.Name())
			dstPath := filepath.Join(cleanupSongDir, file.Name())
			err := os.Rename(srcPath, dstPath)
			if err != nil {
				log.Errorf("moving file %q to cleanup directory: %v\n", file.Name(), err)
			}
		}
	}
}
