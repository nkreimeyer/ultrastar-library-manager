package fixtxt

import (
	"fmt"
	"io/fs"
	"log"
	"os"
	"path/filepath"
	"strings"
	"time"
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
