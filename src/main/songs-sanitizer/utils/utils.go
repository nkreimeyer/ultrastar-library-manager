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

// Usage example:
// err := replaceInFile("myfile.txt", "old text", "new text")
