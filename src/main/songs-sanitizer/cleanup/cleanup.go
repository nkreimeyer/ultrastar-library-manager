package cleanup

import (
	"os"
	"path/filepath"
	"strings"
	"sync"

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
	extraFilesTargetDir := filepath.Join(filepath.Dir(songDir), "extra-files")
	// Create the extra files directory if it doesn't exist
	if _, err := os.Stat(extraFilesTargetDir); os.IsNotExist(err) {
		err := os.Mkdir(extraFilesTargetDir, 0755)
		if err != nil {
			log.Errorf("creating cleanup directory %q: %v\n", extraFilesTargetDir, err)
			return
		}
	}

	entries, err := utils.GetAllSongFolders(songDir)
	if err != nil {
		log.Fatalf("reading song directory: %v\n", err)
		return
	}
	// Loop over all directories, build batches of songs and process them concurrently
	batchSize := 100

	validEntries := filterEntries(entries, songDir, invalidDir)

	batches := utils.SplitIntoBatches(validEntries, batchSize)
	for _, batch := range batches { // now run cleanup on each batch concurrently
		var wg sync.WaitGroup
		handleBatch(batch, songDir, &wg)
		wg.Wait()
	}
}

func filterEntries(entries []os.DirEntry, songDir, invalidDir string) []os.DirEntry {
	validEntries := make([]os.DirEntry, 0, len(entries))
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		// get all files in the directory. check if folder is empty or only contains one file, then delete it from list
		files, err := os.ReadDir(filepath.Join(songDir, entry.Name()))
		if err != nil {
			log.Errorf("reading directory %q: %v\n", entry.Name(), err)
			continue
		}
		if len(files) <= 1 {
			// move the folder to invalid directory
			err = os.Rename(filepath.Join(songDir, entry.Name()), filepath.Join(invalidDir, entry.Name()))
			if err != nil {
				log.Errorf("moving broken song: %v\n", err)
			}
		} else {
			validEntries = append(validEntries, entry)
		}
	}
	return validEntries
}

func handleBatch(batch []os.DirEntry, songDir string, wg *sync.WaitGroup) {
	for _, entry := range batch {
		wg.Add(1)
		go func(entry os.DirEntry) {
			defer wg.Done()
			cleanupDirectory(filepath.Join(songDir, entry.Name()))
		}(entry)
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

	for _, file := range files {
		handleFileCleanup(file, dirPath, extraFilesTargetDir)
	}
}

func handleFileCleanup(file os.DirEntry, dirPath, extraFilesTargetDir string) {
	if file.IsDir() {
		return
	}
	// skip usdb files by checking if the file name ends with .usdb
	if strings.HasSuffix(file.Name(), ".usdb") {
		return
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
				return
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
