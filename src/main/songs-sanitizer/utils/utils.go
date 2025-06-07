package utils

import (
	"fmt"
	"image"
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

// Check if file given exists in directory
func DoesFileExist(dirpath, checkFilename string) bool {
	// Read the directory
	files, err := os.ReadDir(dirpath)
	if err != nil {
		return false
	}
	// Check if the file exists
	for _, file := range files {
		if file.Name() == checkFilename {
			return true
		}
	}
	return false
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
		if IsAudioFile(file.Name()) {
			return true
		}
	}
	return false
}

func IsAudioFile(filename string) bool {
	ext := strings.ToLower(filepath.Ext(filename))
	return ext == ".mp3" || ext == ".ogg" || ext == ".wav" || ext == ".flac" || ext == ".m4a" || ext == ".aac"
	// Add more audio file extensions as needed
}

func IsVideoFile(filename string) bool {
	ext := strings.ToLower(filepath.Ext(filename))
	return ext == ".mp4" || ext == ".mpeg" || ext == ".mpg" || ext == ".m4v" || ext == ".m2v" || ext == ".avi" ||
		ext == ".mov" || ext == ".mkv" || ext == ".flv" || ext == ".wmv" || ext == ".webm"
}

func IsImageFile(filename string) bool {
	ext := strings.ToLower(filepath.Ext(filename))
	return ext == ".jpg" || ext == ".jpeg" || ext == ".png" || ext == ".gif" || ext == ".bmp" || ext == ".tiff"
}

func SplitIntoBatches[T any](items []T, batchSize int) [][]T {
	var batches [][]T
	for batchSize < len(items) {
		items, batches = items[batchSize:], append(batches, items[0:batchSize:batchSize])
	}
	batches = append(batches, items)
	return batches
}

func FindCoverImage(rootDir string) (string, error) {
	entries, err := os.ReadDir(rootDir)
	if err != nil {
		return "", err
	}
	for _, entry := range entries {
		if IsImageFile(entry.Name()) {
			imgFile := entry
			// check if image is square
			if reader, err := os.Open(entry.Name()); err == nil {
				defer reader.Close()
				im, _, err := image.DecodeConfig(reader)
				if err != nil {
					fmt.Fprintf(os.Stderr, "%s: %v\n", imgFile, err)
					continue
				}
				if im.Width == im.Height {
					return imgFile.Name(), nil
				}
			}
		}
	}
	return "", nil
}

func FindBackgroundImage(rootDir string) (string, error) {
	entries, err := os.ReadDir(rootDir)
	if err != nil {
		return "", err
	}
	for _, entry := range entries {
		if IsImageFile(entry.Name()) {
			imgFile := entry
			// check if image is square
			if reader, err := os.Open(entry.Name()); err == nil {
				defer reader.Close()
				im, _, err := image.DecodeConfig(reader)
				if err != nil {
					continue
				}
				if im.Width != im.Height {
					return imgFile.Name(), nil
				}
			}
		}
	}
	return "", nil
}
