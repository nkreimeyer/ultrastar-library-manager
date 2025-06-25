package fixtxt

import (
	"bufio"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"golang.org/x/text/encoding/charmap"
	"golang.org/x/text/encoding/unicode"
	"golang.org/x/text/transform"

	"github.com/nkreimeyer/ultrastar-songs-sanitizer/utils"
	log "github.com/sirupsen/logrus"
)

var songDir string
var missingFiles []string
var configOnce sync.Once
var missingFilesRWM sync.RWMutex

// Run scans all subfolders in songDir and applies the text sanitization rules:
//   - If no .txt files → move to ../notxt/
//   - If exactly one .txt file → do nothing
//   - If more than one .txt file → keep only the least recently modified file
//
// After that it checks folders for issues like wrong tags or missing files and handles them accordingly.
// This is done concurrently for all folders (in batches)
func Run(targetDir, logLevel string) error {
	configOnce.Do(func() {
		songDir = targetDir
	})

	missingFiles = []string{}

	// Set log level
	level, _ := log.ParseLevel(logLevel)
	log.SetLevel(level)

	entries, err := os.ReadDir(songDir)
	if err != nil {
		return fmt.Errorf("reading songDir %q: %w", songDir, err)
	}

	// Loop over all directories, build batches of songs and process them concurrently
	batchSize := 100
	validEntries := make([]os.DirEntry, 0, len(entries))
	for _, entry := range entries {
		if !entry.IsDir() {
			// We only care about directories. Remove the entry from the list.
			continue
		} else {
			validEntries = append(validEntries, entry)
		}
	}
	entries = validEntries

	batches := utils.SplitIntoBatches(entries, batchSize)
	for _, batch := range batches {
		var wg sync.WaitGroup
		for _, entry := range batch {
			wg.Add(1)
			go func(entry os.DirEntry) {
				defer wg.Done()
				err := checkFolder(filepath.Join(songDir, entry.Name()))
				if err != nil {
					log.Warnf(" Could not check folder %s: %v", entry.Name(), err)
				}
			}(entry)
		}
		wg.Wait()
	}

	// Print missing files to one level up from targetDir in a file called missing_files.txt
	missingFilesPath := filepath.Join(filepath.Dir(songDir), "missing_files.txt")
	if err := os.WriteFile(missingFilesPath, []byte(strings.Join(missingFiles, "\n")), 0644); err != nil {
		log.Warnf(" Could not write missing files to %s: %v", missingFilesPath, err)
	} else {
		log.Infof("Wrote missing files to %s\n", missingFilesPath)
	}
	return nil
}

func checkFolder(folderPath string) error {
	txtFiles, err := getTxtFiles(folderPath)
	if err != nil {
		log.Warnf(" Could not read folder %s: %v", folderPath, err)
		return err
	}

	switch len(txtFiles) {
	case 0:
		// Move entire folder to ../notxt/
		if err := moveToNoTxt(folderPath); err != nil {
			log.Warnf(" Could not move folder %s to ../notxt/: %v", folderPath, err)
		} else {
			log.Infof("Moved folder %s -> ../notxt/\n", folderPath)
		}
		return nil
	case 1:
		// Exactly one .txt file, do nothing
		//log.Infof("Folder %s has one TXT file. Nothing to do.\n", folderPath)

	default:
		// More than 1 .txt file; keep only the least recently modified
		keepMostRecentlyModified(txtFiles)
		log.Infof("Folder %s had %d .txt files. Kept the least recently modified.\n",
			folderPath, len(txtFiles))

	}
	// Now we only have one .txt file, call updateFilenamesInTxt on the folder
	fixFileTags(folderPath)
	return nil
}

// getTxtFiles returns a list of .txt file paths within a given folder.
func getTxtFiles(folderPath string) ([]string, error) {
	var txtFiles []string
	err := filepath.WalkDir(folderPath, func(path string, d fs.DirEntry, wErr error) error {
		if wErr != nil {
			return wErr
		}
		if !d.IsDir() && strings.ToLower(filepath.Ext(d.Name())) == ".txt" {
			txtFiles = append(txtFiles, path)
		}
		return nil
	})
	return txtFiles, err
}

// moveToNoTxt moves the entire folder to "../notxt/<folderName>" relative to folderPath.
func moveToNoTxt(folderPath string) error {
	parentDir := filepath.Dir(folderPath)
	folderName := filepath.Base(folderPath)

	notxtPath := filepath.Join(parentDir, "..", "notxt")
	if err := os.MkdirAll(notxtPath, 0755); err != nil {
		return fmt.Errorf("creating ../notxt/ directory: %w", err)
	}

	targetPath := filepath.Join(notxtPath, folderName)
	// Rename will move the directory to the new location.
	if err := os.Rename(folderPath, targetPath); err != nil {
		return fmt.Errorf("moving folder: %w", err)
	}
	return nil
}

// moveToNoTxt moves the entire folder to "../notxt/<folderName>" relative to folderPath.
func moveToInvalid(folderPath string) error {
	parentDir := filepath.Dir(folderPath)
	folderName := filepath.Base(folderPath)

	invalidPath := filepath.Join(parentDir, "..", "notxt")
	if err := os.MkdirAll(invalidPath, 0755); err != nil {
		return fmt.Errorf("creating ../notxt/ directory: %w", err)
	}

	targetPath := filepath.Join(invalidPath, folderName)
	// Rename will move the directory to the new location.
	if err := os.Rename(folderPath, targetPath); err != nil {
		return fmt.Errorf("moving folder: %w", err)
	}
	return nil
}

// keepLeastRecentlyModified finds the least recently modified .txt file
// and deletes all the others.
func keepMostRecentlyModified(txtFiles []string) {
	if len(txtFiles) < 2 {
		return // Nothing to do
	}

	var newestFile string
	var newestTime time.Time

	for i, file := range txtFiles {
		info, err := os.Stat(file)
		if err != nil {
			log.Warnf(" could not stat %s: %v", file, err)
			continue
		}
		content, err := os.ReadFile(file)
		if err != nil {
			log.Warnf(" could not read %s: %v", file, err)
			continue
		}
		if !strings.Contains(string(content), "#MP3") {
			continue
		}

		modTime := info.ModTime()
		if i == 0 || modTime.After(newestTime) {
			newestFile = file
			newestTime = modTime
		}
	}
	// compile a list of files to delete
	var filesToDelete []string
	for _, file := range txtFiles {
		if file != newestFile {
			filesToDelete = append(filesToDelete, file)
		}
	}
	utils.DeleteFiles(filesToDelete)
}

// tryReadWithEncoding attempts to read a file with different encodings and returns the successfully decoded content
func tryReadWithEncoding(filePath string) ([]string, error) {
	encodings := []struct {
		name    string
		decoder transform.Transformer
	}{
		{"utf-8", unicode.UTF8.NewDecoder()},
		{"ISO-8859-1", charmap.ISO8859_1.NewDecoder()},
		{"cp1252", charmap.Windows1252.NewDecoder()},
	}
	for _, enc := range encodings {
		file, err := os.Open(filePath)
		if err != nil {
			return nil, err
		}

		reader := transform.NewReader(file, enc.decoder)
		scanner := bufio.NewScanner(reader)
		var lines []string
		scanErr := false

		for scanner.Scan() {
			lines = append(lines, scanner.Text()+"\n")
		}
		if scanner.Err() != nil {
			scanErr = true
		}
		file.Close()
		if !scanErr {
			return lines, nil
		}
	}

	return nil, fmt.Errorf("unable to decode file %s with any of the supported encodings", filePath)
}

// fixFileTags checks Files in a song folder and updates tag filenames in the txt file.
// Additionally, it handles missing files by logging them or remove if required data is missing.
func fixFileTags(rootDir string) {
	entries, err := os.ReadDir(rootDir)
	if err != nil {
		log.Errorf("reading directory %s: %v\n", rootDir, err)
		return
	}
	var mp3Files, videoFiles []string
	var txtFile string
	// Identify the .mp3 and .txt files
	for _, entry := range entries {
		if utils.IsAudioFile(entry.Name()) {
			mp3Files = append(mp3Files, entry.Name())
		} else if strings.HasSuffix(entry.Name(), ".txt") {
			txtFile = filepath.Join(rootDir, entry.Name())
		} else if utils.IsVideoFile(entry.Name()) {
			videoFiles = append(videoFiles, entry.Name())
		}
	}
	var coverFile, backgroundFile string
	coverFile, err = utils.FindCoverImage(rootDir)
	if err != nil {
		log.Errorf("getting cover file: %v\n", err)
	}

	backgroundFile, err = utils.FindBackgroundImage(rootDir)
	if err != nil {
		log.Errorf("getting background file: %v\n", err)
	}

	if txtFile != "" {
		//log.Debugf("Updating %s with files %s, %s, %s, %s\n", txtFile, mp3Files, videoFiles, coverFile, backgroundFile)
		updateTxtFile(rootDir, txtFile, mp3Files, videoFiles, coverFile, backgroundFile)
	}
}

func updateTxtFile(rootDir, txtFile string, mp3Files []string, videoFiles []string, coverFile string, backgroundFile string) {
	lines, err := tryReadWithEncoding(txtFile)
	if err != nil {
		log.Errorf("reading %s. Unable to determine file encoding.\n", txtFile)
		return
	}

	updatedLines := fixTxtLines(lines, mp3Files, videoFiles, rootDir, coverFile, backgroundFile)

	// Write the updated list of lines back to the file using UTF-8 encoding
	if err := utils.WriteFileAsUTF8(txtFile, updatedLines); err != nil {
		log.Errorf("writing to %s: %v\n", txtFile, err)
	}

	// log missing files (cover and if either video or background is missing)
	missingFilesInPath := []string{}
	if len(mp3Files) == 0 && len(videoFiles) == 0 {
		// if mp3 file is missing and video file is missing (may contain audio),
		// move the directory to two levels up from path to "invalid"
		if err := moveToInvalid(rootDir); err != nil {
			log.Errorf("moving %s to invalid: %v\n", rootDir, err)
		}
	}
	if coverFile == "" {
		missingFilesInPath = append(missingFilesInPath, "Missing cover file in "+rootDir)
	}
	if len(videoFiles) == 0 && backgroundFile == "" {
		missingFilesInPath = append(missingFilesInPath, "No video or background file in "+rootDir)
	}
	if len(missingFilesInPath) > 0 {
		logMissingFiles(missingFilesInPath)
	}
}

func fixTxtLines(lines, mp3Files, videoFiles []string, rootDir, coverFile, backgroundFile string) []string {
	var videoFileInTxt = getTagValue(lines, "#VIDEO")
	var audioFileInTxt = getTagValue(lines, "#MP3")
	var coverFileInTxt = getTagValue(lines, "#COVER")
	var backgroundFileInTxt = getTagValue(lines, "#BACKGROUND")

	// check if image dimensions are valid, if not, remove the cover and background files from the txt

	forceUpdateCover := false
	forceUpdateBackground := false
	xCover, yCover, err := utils.GetImageDimensions(filepath.Join(rootDir, coverFileInTxt))
	if err != nil {
		log.Errorf("getting image dimensions for %s: %v\n", coverFileInTxt, err)
		coverFileInTxt = ""
	} else if xCover != yCover {
		forceUpdateCover = true
	}

	xBackground, yBackground, err := utils.GetImageDimensions(filepath.Join(rootDir, backgroundFileInTxt))
	if err != nil {
		log.Errorf("getting image dimensions for %s: %v\n", backgroundFileInTxt, err)
		backgroundFileInTxt = ""
	} else if xBackground == yBackground {
		forceUpdateBackground = true
	}

	log.Debugf("forceUpdateCover: %v, forceUpdateBackground: %v\n", forceUpdateCover, forceUpdateBackground)

	// do not change the files if they are already correct in the txt dat, cleanup will take care of it later
	var updateVideoFile = len(videoFiles) > 0 && !utils.DoesFileExist(rootDir, videoFileInTxt)
	var updateAudioFile = len(mp3Files) > 0 && !utils.DoesFileExist(rootDir, audioFileInTxt)
	var updateCoverFile = coverFile != "" && (!utils.DoesFileExist(rootDir, coverFileInTxt) || forceUpdateCover)
	var updateBackgroundFile = backgroundFile != "" && (!utils.DoesFileExist(rootDir, backgroundFileInTxt) || forceUpdateBackground)

	// Update the lines when necessary
	updatedLines := make([]string, len(lines))

	// if there are less than 3 lines, it's not a valid txt file, so return the original lines
	if len(lines) < 3 {
		return lines
	}

	// Remove duplicate Tags
	//updatedLines = removeDuplicateTags(updatedLines)

	foundAudioTag := false
	foundVideoTag := false
	foundCoverTag := false
	foundBackgroundTag := false

	for i, line := range lines {
		switch {
		case strings.HasPrefix(line, "#VIDEO"):
			if updateVideoFile {
				updatedLines[i] = "#VIDEO:" + videoFiles[0] + "\n"
				log.Debugf("Updated video file to %s", videoFiles[0])
			}
			foundVideoTag = true

		case strings.HasPrefix(line, "#MP3"):
			if updateAudioFile {
				updatedLines[i] = "#MP3:" + mp3Files[0] + "\n"
				log.Debugf("Updated mp3 file to %s", mp3Files[0])
			}
			foundAudioTag = true

		case strings.HasPrefix(line, "#COVER"):
			if updateCoverFile {
				updatedLines[i] = "#COVER:" + coverFile + "\n"
				log.Debugf("Updated cover file to %s", coverFile)
			}
			foundCoverTag = true

		case strings.HasPrefix(line, "#BACKGROUND"):
			if updateBackgroundFile {
				updatedLines[i] = "#BACKGROUND:" + backgroundFile + "\n"
				log.Debugf("Updated background file to %s", backgroundFile)
			}
			foundBackgroundTag = true

		default:
			updatedLines[i] = line
		}
	}
	// Check if any of the required lines are missing, add the missing ones inserting them at line 2
	if !foundAudioTag && len(mp3Files) > 0 {
		frontUpdatedLines := updatedLines[:1]
		lastUpdatedLines := updatedLines[1:]
		updatedLines = append(frontUpdatedLines, append([]string{"#MP3:" + mp3Files[0] + "\n"}, lastUpdatedLines...)...)
		log.Debugf("Added missing mp3 tag with file %s", mp3Files[0])
	}
	if !foundVideoTag && len(videoFiles) > 0 {
		frontUpdatedLines := updatedLines[:1]
		lastUpdatedLines := updatedLines[1:]
		updatedLines = append(frontUpdatedLines, append([]string{"#VIDEO:" + videoFiles[0] + "\n"}, lastUpdatedLines...)...)
		log.Debugf("Added missing video tag with file %s", videoFiles[0])
	}
	if !foundCoverTag && coverFile != "" {
		frontUpdatedLines := updatedLines[:1]
		lastUpdatedLines := updatedLines[1:]
		updatedLines = append(frontUpdatedLines, append([]string{"#COVER:" + coverFile + "\n"}, lastUpdatedLines...)...)
		log.Debugf("Added missing cover tag with file %s", coverFile)
	}
	if !foundBackgroundTag && backgroundFile != "" {
		frontUpdatedLines := updatedLines[:1]
		lastUpdatedLines := updatedLines[1:]
		updatedLines = append(frontUpdatedLines, append([]string{"#BACKGROUND:" + backgroundFile + "\n"}, lastUpdatedLines...)...)
		log.Debugf("Added missing background tag with file %s", backgroundFile)
	}
	log.Debugf("Linedump of updatedLines: %v\n", updatedLines)
	return updatedLines
}

func removeDuplicateTags(lines []string) []string {
	tags := make(map[string]bool)
	var result []string
	for _, line := range lines {
		if strings.HasPrefix(line, "#") {
			tag := strings.Split(line, ":")[0]
			if _, exists := tags[tag]; !exists {
				tags[tag] = true
				result = append(result, line)
			}
		} else {
			result = append(result, line)
		}
	}
	return result
}

func getTagValue(lines []string, tag string) string {
	for _, line := range lines {
		if strings.HasPrefix(line, tag) {
			parts := strings.SplitN(line, ":", 2)
			if len(parts) == 2 {
				return strings.TrimSpace(parts[1])
			}
		}
	}
	return ""
}

func logMissingFiles(missingFilesInPath []string) {
	// wait for Write Lock to be available
	missingFilesRWM.Lock()
	missingFiles = append(missingFiles, missingFilesInPath...)
	missingFilesRWM.Unlock()
}
