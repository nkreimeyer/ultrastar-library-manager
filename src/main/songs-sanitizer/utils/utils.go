package utils

import (
	"fmt"
	"image"
	"image/jpeg"
	"image/png"
	"os"
	"path/filepath"
	"strings"

	log "github.com/sirupsen/logrus"
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

func FindCoverImage(rootDir string) string {
	entries, err := os.ReadDir(rootDir)
	if err != nil {
		return ""
	}
	for _, entry := range entries {
		name := filepath.Clean(entry.Name())
		path := filepath.Join(rootDir, name)
		if IsImageFile(name) {
			//log.Debugf("Found image file %s", name)
			// check if image is square
			x, y, err := GetImageDimensions(path)
			if err != nil {
				log.Errorf("Error getting image dimensions for %s: %v", name, err)
				continue
			}
			if x == y {
				log.Debugf("Found cover image %s", name)
				return name
			}
		}
	}
	log.Warnf("No cover image found in %s", rootDir)
	return ""
}

func FindBackgroundImage(rootDir string) string {
	entries, err := os.ReadDir(rootDir)
	if err != nil {
		log.Errorf("Error reading directory %s: %v", rootDir, err)
		return ""
	}
	for _, entry := range entries {
		name := filepath.Clean(entry.Name())
		path := filepath.Join(rootDir, name)
		if IsImageFile(name) {
			// check if image is square
			x, y, err := GetImageDimensions(path)
			if err != nil {
				log.Errorf("Error getting image dimensions for %s: %v", name, err)
				continue
			}
			if x != y {
				log.Debugf("Found background image %s", name)
				return name
			}
		}
	}
	log.Warnf("No background image found in %s", rootDir)
	return ""
}

// writeFileAsUTF8 writes the file with UTF-8 encoding
func WriteFileAsUTF8(filePath string, lines []string) error {
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

func DeleteFiles(files []string) {
	for _, file := range files {
		if err := os.Remove(file); err != nil {
			log.Errorf(" could not delete file %s: %v", file, err)
		}
	}
}

func GetImageDimensions(filePath string) (int, int, error) {
	file, err := os.Open(filePath)
	if err != nil {
		return 0, 0, err
	}
	defer file.Close()

	// Detect the image format
	format, err := detectImageFormat(file)
	if err != nil {
		return 0, 0, err
	}

	// Reset the file pointer to the beginning
	_, err = file.Seek(0, 0)
	if err != nil {
		return 0, 0, err
	}

	var img image.Image
	switch format {
	case "jpeg":
		img, err = jpeg.Decode(file)
	case "png":
		img, err = png.Decode(file)
	default:
		return 0, 0, fmt.Errorf("unsupported image format: %s", format)
	}

	if err != nil {
		return 0, 0, err
	}

	bounds := img.Bounds()
	width := bounds.Dx()
	height := bounds.Dy()

	return width, height, nil
}

// detectImageFormat returns the format of the image file.
func detectImageFormat(file *os.File) (string, error) {
	// Read the first few bytes to determine the format
	buffer := make([]byte, 8)
	_, err := file.Read(buffer)
	if err != nil {
		return "", err
	}

	// Check for JPEG format
	if strings.HasPrefix(string(buffer), "\xff\xd8\xff") {
		return "jpeg", nil
	}

	// Check for PNG format
	if strings.HasPrefix(string(buffer), "\x89PNG\r\n\x1a\n") {
		return "png", nil
	}

	return "", fmt.Errorf("unknown image format")
}

func FolderContainsTxtFile(folderPath string) bool {
	entries, err := os.ReadDir(folderPath)
	if err != nil {
		log.Errorf("Error reading folder %s: %v", folderPath, err)
		return false
	}
	for _, entry := range entries {
		if !entry.IsDir() && strings.HasSuffix(entry.Name(), ".txt") {
			return true
		}
	}
	return false
}

func GetAllSongFolders(songDir string) ([]string, error) {
	var songFolders []string
	entries, err := os.ReadDir(songDir)
	if err != nil {
		return nil, fmt.Errorf("reading songDir %q: %w", songDir, err)
	}
	for _, entry := range entries {
		fullPath := filepath.Join(songDir, entry.Name())
		if entry.IsDir() && FolderContainsTxtFile(fullPath) {
			songFolders = append(songFolders, fullPath)
		} else if entry.IsDir() {
			subFolders, err := GetAllSongFolders(fullPath)
			if err != nil {
				return nil, err
			}
			songFolders = append(songFolders, subFolders...)
			// in case of no subfolders, add the current folder, as it might be a broken song folder.
			if len(subFolders) == 0 {
				songFolders = append(songFolders, fullPath)
			}
		}
	}
	return songFolders, nil
}
