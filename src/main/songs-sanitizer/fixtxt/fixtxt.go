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

type SongInfo struct {
	Artist       string
	Title        string
	MP3          string
	Cover        string
	Background   string
	Video        string
	Genre        string
	Edition      string
	Year         string
	Language     string
	Gap          string
	BPM          string
	Count        string
	DuetsingerP1 string
}

// Bitmask flags for 4 values, usually audio, video, cover, background
const (
	Flag1 = 1 << iota // 1 << 0 which is 1
	Flag2             // 1 << 1 which is 2
	Flag3             // 1 << 2 which is 4
	Flag4             // 1 << 3 which is 8
)

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

	entries, err := utils.GetAllSongFolders(songDir)
	if err != nil {
		return fmt.Errorf("reading songDir %q: %w", songDir, err)
	}

	// Loop over all directories, build batches of songs and process them concurrently
	batchSize := 100

	batches := utils.SplitIntoBatches(entries, batchSize)
	for _, batch := range batches {
		var wg sync.WaitGroup
		for _, entry := range batch {
			wg.Add(1)
			go func(entry string) {
				defer wg.Done()
				err := checkFolderForDupTxtFiles(entry)
				if err != nil {
					log.Warnf("Could not check folder %s: %v", entry, err)
				}
				handleDirectory(entry)
			}(entry)
		}
		wg.Wait()
	}

	// Print missing files to one level up from targetDir in a file called missing_files.txt
	missingFilesPath := filepath.Join(filepath.Dir(songDir), "missing_files.txt")
	if err := os.WriteFile(missingFilesPath, []byte(strings.Join(missingFiles, "\n")), 0644); err != nil {
		log.Warnf("Could not write missing files to %s: %v", missingFilesPath, err)
	} else {
		log.Infof("Wrote missing files to %s\n", missingFilesPath)
	}
	return nil
}

func checkFolderForDupTxtFiles(folderPath string) error {
	txtFiles, err := getTxtFiles(folderPath)
	if err != nil {
		log.Warnf("Could not read folder %s: %v", folderPath, err)
		return err
	}

	switch len(txtFiles) {
	case 0:
		// Move entire folder to ../invalid/
		if err := moveToInvalid(folderPath); err != nil {
			log.Warnf("Could not move folder %s to invalid directory: %v", folderPath, err)
		} else {
			log.Infof("Moved folder %s -> invalid directory", folderPath)
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

// moveToInvalid moves the entire folder to "invalid/<folderName>" in the working directory of the executable.
func moveToInvalid(folderPath string) error {
	folderName := filepath.Base(folderPath)

	// get the working directory of the executable
	exePath, err := os.Executable()
	if err != nil {
		return fmt.Errorf("getting executable path: %w", err)
	}
	invalidPath := filepath.Join(filepath.Dir(exePath), "invalid")

	if err := os.MkdirAll(invalidPath, 0755); err != nil {
		return fmt.Errorf("creating invalid/ directory: %w", err)
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

// handleDirectory checks Files in a song folder and updates tag filenames in the txt file.
// Additionally, it handles missing files by logging them or remove if required data is missing.
func handleDirectory(rootDir string) {
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
	coverFile = utils.FindCoverImage(rootDir)
	backgroundFile = utils.FindBackgroundImage(rootDir)

	if txtFile != "" { //log.Debugf("Updating %s with files %s, %s, %s, %s\n", txtFile, mp3Files, videoFiles, coverFile, backgroundFile)
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

	SongInfoTxt, err := getSongInfoFromTxtFile(lines)
	if err != nil {
		log.Errorf("getSongInfoFromTxtFile: %v\n", err)
	}

	// check if image dimensions are valid, if not, remove the cover and ba
	forceUpdateCover, forceUpdateBackground, SongInfoTxt := checkDimensionsOfImagesTagged(rootDir, *SongInfoTxt)

	// do not change the files if they are already correct in the txt dat, cleanup will take care of it later
	var updateVideoFile = len(videoFiles) > 0 && !utils.DoesFileExist(rootDir, SongInfoTxt.Video)
	var updateAudioFile = len(mp3Files) > 0 && !utils.DoesFileExist(rootDir, SongInfoTxt.MP3)
	var updateCoverFile = coverFile != "" && (!utils.DoesFileExist(rootDir, SongInfoTxt.Cover) || forceUpdateCover)
	var updateBackgroundFile = backgroundFile != "" && (!utils.DoesFileExist(rootDir, SongInfoTxt.Background) || forceUpdateBackground)

	updatedLines := make([]string, len(lines))

	// if there are less than 3 lines, it's not a valid txt file, so return the original lines
	if len(lines) < 3 {
		return lines
	}

	lines = removeDuplicateTags(lines)

	foundTagFlags := updateTagsInLines(lines, updatedLines, mp3Files, videoFiles, coverFile, backgroundFile, updateAudioFile, updateVideoFile, updateCoverFile, updateBackgroundFile)

	linesToInsert := []string{}
	// Check if any of the required lines are missing, add the missing ones inserting them at line 2
	if !isFlagSet(foundTagFlags, Flag1) && len(mp3Files) > 0 {
		linesToInsert = append(linesToInsert, "#MP3:"+mp3Files[0]+"\n")
		//log.Debugf("Added missing mp3 tag with file %s", mp3Files[0])
	}
	if !isFlagSet(foundTagFlags, Flag2) && len(videoFiles) > 0 {
		linesToInsert = append(linesToInsert, "#VIDEO:"+videoFiles[0]+"\n")
		//log.Debugf("Added missing video tag with file %s", videoFiles[0])
	}
	if !isFlagSet(foundTagFlags, Flag3) && coverFile != "" {
		linesToInsert = append(linesToInsert, "#COVER:"+coverFile+"\n")
		//log.Debugf("Added missing cover tag with file %s", coverFile)
	}
	if !isFlagSet(foundTagFlags, Flag4) && backgroundFile != "" {
		linesToInsert = append(linesToInsert, "#BACKGROUND:"+backgroundFile+"\n")
		//log.Debugf("Added missing background tag with file %s", backgroundFile)
	}

	// Create new slices to avoid modifying the original updatedLines
	frontUpdatedLines := make([]string, len(updatedLines[:1]))
	copy(frontUpdatedLines, updatedLines[:1])

	lastUpdatedLines := make([]string, len(updatedLines[1:]))
	copy(lastUpdatedLines, updatedLines[1:])

	if len(linesToInsert) > 0 {
		log.Debugf("Inserting the following lines: %v", linesToInsert)
	}
	updatedLines = append(frontUpdatedLines, append(linesToInsert, lastUpdatedLines...)...)

	return updatedLines
}

func updateTagsInLines(lines, updatedLines, videoFiles, mp3Files []string, coverFile, backgroundFile string, updateAudioFile, updateVideoFile, updateCoverFile, updateBackgroundFile bool) int {
	var foundTagFlags int
	for i, line := range lines {
		switch {
		case strings.HasPrefix(line, "#MP3"):
			if updateAudioFile && len(mp3Files) > 0 {
				updatedLines[i] = "#MP3:" + mp3Files[0] + "\n"
				log.Debugf("Updated mp3 file to %s", mp3Files[0])
			} else {
				updatedLines[i] = line
			}
			foundTagFlags = setFlag(foundTagFlags, Flag1)
		case strings.HasPrefix(line, "#VIDEO"):
			if updateVideoFile && len(videoFiles) > 0 {
				updatedLines[i] = "#VIDEO:" + videoFiles[0] + "\n"
				log.Debugf("Updated video file to %s", videoFiles[0])
			} else {
				updatedLines[i] = line
			}
			foundTagFlags = setFlag(foundTagFlags, Flag2)
		case strings.HasPrefix(line, "#COVER"):
			if updateCoverFile {
				updatedLines[i] = "#COVER:" + coverFile + "\n"
				log.Debugf("Updated cover file to %s", coverFile)
			} else {
				updatedLines[i] = line
			}
			foundTagFlags = setFlag(foundTagFlags, Flag3)
		case strings.HasPrefix(line, "#BACKGROUND"):
			if updateBackgroundFile {
				updatedLines[i] = "#BACKGROUND:" + backgroundFile + "\n"
				log.Debugf("Updated background file to %s", backgroundFile)
			} else {
				updatedLines[i] = line
			}
			foundTagFlags = setFlag(foundTagFlags, Flag4)
		default:
			updatedLines[i] = line
		}
	}
	return foundTagFlags
}

func checkDimensionsOfImagesTagged(rootDir string, SongInfoTxt SongInfo) (bool, bool, *SongInfo) {
	forceUpdateCover := false
	forceUpdateBackground := false
	if SongInfoTxt.Cover != "" {
		xCover, yCover, err := utils.GetImageDimensions(filepath.Join(rootDir, SongInfoTxt.Cover))
		if err != nil {
			log.Errorf("getting image dimensions for %s: %v\n", SongInfoTxt.Cover, err)
			SongInfoTxt.Cover = ""
		} else if xCover != yCover {
			forceUpdateCover = true
		}
	}
	if SongInfoTxt.Background != "" {
		xBackground, yBackground, err := utils.GetImageDimensions(filepath.Join(rootDir, SongInfoTxt.Background))
		if err != nil {
			log.Errorf("getting image dimensions for %s: %v\n", SongInfoTxt.Background, err)
			SongInfoTxt.Background = ""
		} else if xBackground == yBackground {
			forceUpdateBackground = true
		}
	}
	return forceUpdateCover, forceUpdateBackground, &SongInfoTxt
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
			} else {
				log.Debugf("Removing duplicate tag: %s\n", tag)
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

// Function to set a flag
func setFlag(flags int, flag int) int {
	return flags | flag
}

// Function to clear a flag
func clearFlag(flags int, flag int) int {
	return flags &^ flag
}

// Function to check if a flag is set
func isFlagSet(flags int, flag int) bool {
	return flags&flag != 0
}

func getSongInfoFromTxtFile(lines []string) (*SongInfo, error) {
	songInfo := &SongInfo{
		Artist:     getTagValue(lines, "#ARTIST"),
		Title:      getTagValue(lines, "#TITLE"),
		MP3:        getTagValue(lines, "#MP3"),
		Cover:      getTagValue(lines, "#COVER"),
		Video:      getTagValue(lines, "#VIDEO"),
		Background: getTagValue(lines, "#BACKGROUND"),
	}
	return songInfo, nil
}
