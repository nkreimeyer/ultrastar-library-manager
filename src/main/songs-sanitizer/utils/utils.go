package utils

import (
	"os"
	"path/filepath"
	"strings"
)

func ReplaceInUSTxtFile(parentPath, searchText, replacement string) error {
	// search parentPath directory for .txt file
	filename, err := findFirstTxtFile(parentPath)
	if err != nil {
		return err
	}

	// Read the file
	data, err := os.ReadFile(filename)
	if err != nil {
		return err
	}

	// Replace all instances
	newData := strings.ReplaceAll(string(data), searchText, replacement)

	// Write back to file
	return os.WriteFile(filename, []byte(newData), 0644)
}

func findFirstTxtFile(searchPath string) (string, error) {
	var txtFile string

	err := filepath.Walk(searchPath, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}

		// Check if it's a .txt file
		if !info.IsDir() && strings.ToLower(filepath.Ext(path)) == ".txt" {
			txtFile = path
			return filepath.SkipAll // Stop walking after finding first txt file
		}
		return nil
	})

	if err != nil {
		return "", err
	}

	if txtFile == "" {
		return "", os.ErrNotExist // No txt file found
	}

	return txtFile, nil
}

func IsFileReferenced(dirpath, checkFilename string) bool {
	// Placeholder function to determine if a file is referenced in the txt file.
	// Read the file
	filename, err := findFirstTxtFile(dirpath)
	if err != nil {
		return true
	}

	// Read the file
	data, err := os.ReadFile(filename)
	if err != nil {
		return true
	}

	// Check if the file is referenced in the txt file
	return strings.Contains(string(data), checkFilename)
}

func AudioFileExists(dirpath string) bool {
	// Placeholder function to determine if an audio file exists in the directory.
	// Read the directory
	files, err := os.ReadDir(dirpath)
	if err != nil {
		return false
	}
	// Check if an audio file exists
	for _, file := range files {
		if isAudioFile(file.Name()) {
			return true
		}
	}
	return false
}

func isAudioFile(filename string) bool {
	ext := strings.ToLower(filepath.Ext(filename))
	return ext == ".mp3" || ext == ".ogg" || ext == ".wav" || ext == ".flac" || ext == ".m4a" || ext == ".aac"
	// Add more audio file extensions as needed
}
