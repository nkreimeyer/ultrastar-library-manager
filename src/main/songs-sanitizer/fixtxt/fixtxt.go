package fixtxt

import (
	"bufio"
	"fmt"
	"io/fs"
	"log"
	"os"
	"path/filepath"
	"strings"
	"time"

	"golang.org/x/text/encoding/charmap"
	"golang.org/x/text/encoding/unicode"
	"golang.org/x/text/transform"
)

// Run scans all subfolders in songDir and applies the text sanitization rules:
//   - If no .txt files → move to ../notxt/
//   - If exactly one .txt file → do nothing
//   - If more than one .txt file → keep only the least recently modified file
func Run(songDir string) error {
	entries, err := os.ReadDir(songDir)
	if err != nil {
		return fmt.Errorf("reading songDir %q: %w", songDir, err)
	}

	for _, entry := range entries {
		if !entry.IsDir() {
			// We only care about directories.
			continue
		}

		folderPath := filepath.Join(songDir, entry.Name())

		txtFiles, err := getTxtFiles(folderPath)
		if err != nil {
			log.Printf("[WARN] Could not read folder %s: %v", folderPath, err)
			continue
		}

		switch len(txtFiles) {
		case 0:
			// Move entire folder to ../notxt/
			if err := moveToNoTxt(folderPath); err != nil {
				log.Printf("[WARN] Could not move folder %s to ../notxt/: %v", folderPath, err)
			} else {
				fmt.Printf("Moved folder %s -> ../notxt/\n", folderPath)
			}
		case 1:
			// Exactly one .txt file, do nothing
			//fmt.Printf("Folder %s has one TXT file. Nothing to do.\n", folderPath)
		default:
			// More than 1 .txt file; keep only the least recently modified
			if err := keepMostRecentlyModified(txtFiles); err != nil {
				log.Printf("[WARN] Could not remove extra .txt files in %s: %v", folderPath, err)
			} else {
				fmt.Printf("Folder %s had %d .txt files. Kept the least recently modified.\n",
					folderPath, len(txtFiles))
			}
		}
	}
	// now loop over all folders in songDir and fix encoding and check if the MP3 Tag is correctly referencing the music file
	updateMP3Filenames(songDir)
	return nil
}

// getTxtFiles returns a list of .txt file paths within a given folder.
func getTxtFiles(folderPath string) ([]string, error) {
	var txtFiles []string
	err := filepath.WalkDir(folderPath, func(path string, d fs.DirEntry, wErr error) error {
		if wErr != nil {
			return wErr
		}
		if !d.IsDir() && filepath.Ext(d.Name()) == ".txt" {
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

// keepLeastRecentlyModified finds the least recently modified .txt file
// and deletes all the others.
func keepMostRecentlyModified(txtFiles []string) error {
	if len(txtFiles) < 2 {
		return nil // Nothing to do
	}

	var newestFile string
	var newestTime time.Time

	for i, file := range txtFiles {
		info, err := os.Stat(file)
		if err != nil {
			log.Printf("[WARN] could not stat %s: %v", file, err)
			continue
		}

		// check if #MP3 is in the file's content,
		// else continue so it gets ignored and deleted later
		content, err := os.ReadFile(file)
		if err != nil {
			log.Printf("[WARN] could not read %s: %v", file, err)
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

	// Delete everything except newest file
	for _, file := range txtFiles {
		if file == newestFile {
			continue
		}
		if err := os.Remove(file); err != nil {
			log.Printf("[WARN] could not delete file %s: %v", file, err)
		} else {
			fmt.Printf("Deleted extra .txt file: %s\n", file)
		}
	}
	return nil
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

// writeFileAsUTF8 writes the file with UTF-8 encoding
func writeFileAsUTF8(filePath string, lines []string) error {
	file, err := os.Create(filePath)
	if err != nil {
		return err
	}
	defer file.Close()

	for _, line := range lines {
		_, err := file.WriteString(line)
		if err != nil {
			return err
		}
	}
	return nil
}

// updateMP3Filenames walks through directories and updates MP3 filenames in txt files
func updateMP3Filenames(rootDir string) error {

	return filepath.Walk(rootDir, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}

		if !info.IsDir() {
			return nil
		}

		entries, err := os.ReadDir(path)
		if err != nil {
			return err
		}

		var mp3File, txtFile string

		// Identify the .mp3 and .txt files
		for _, entry := range entries {
			if strings.HasSuffix(entry.Name(), ".mp3") {
				mp3File = entry.Name()
			} else if strings.HasSuffix(entry.Name(), ".txt") {
				txtFile = filepath.Join(path, entry.Name())
			}
		}

		if mp3File != "" && txtFile != "" {
			// Try reading the txt file using different possible encodings
			lines, err := tryReadWithEncoding(txtFile)
			if err != nil {
				fmt.Printf("Error reading %s. Unable to determine file encoding.\n", txtFile)
				return nil
			}

			var updatedLines []string

			for _, line := range lines {
				// If the line contains the #MP3 tag, check for filename mismatch
				if strings.HasPrefix(line, "#MP3:") {
					parts := strings.SplitN(line, ":", 2)
					if len(parts) == 2 {
						currentMP3 := strings.TrimSpace(parts[1])
						if currentMP3 != mp3File {
							fmt.Printf("Updating %s: %s to %s\n", txtFile, currentMP3, mp3File)
							line = fmt.Sprintf("#MP3:%s\n", mp3File)
						}
					}
				}
				updatedLines = append(updatedLines, line)
			}

			// Write the updated list of lines back to the file using UTF-8 encoding
			if err := writeFileAsUTF8(txtFile, updatedLines); err != nil {
				fmt.Printf("Error writing to %s: %v\n", txtFile, err)
			}
		}
		return nil
	})
}
