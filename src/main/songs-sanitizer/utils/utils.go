// Package utils contains helpers shared by the sanitizer modules: song txt decoding and
// lookup (txt.go), song folder discovery (folders.go) and media file helpers (this file).
package utils

import (
	"fmt"
	"image"
	_ "image/gif" // register decoders for image.DecodeConfig
	_ "image/jpeg"
	_ "image/png"
	"os"
	"path/filepath"
	"strings"

	log "github.com/sirupsen/logrus"
)

// IsHidden reports whether name is a hidden file. Hidden files are never treated as song
// media: they are leftovers of interrupted runs (".song.sanitizer-123.mp3"), macOS
// AppleDouble files ("._song.mp3") or similar.
func IsHidden(name string) bool {
	return strings.HasPrefix(name, ".")
}

// IsRegularFile reports whether the directory entry is a regular file or a symlink to one.
func IsRegularFile(dir string, entry os.DirEntry) bool {
	if entry.Type().IsRegular() {
		return true
	}
	if entry.Type()&os.ModeSymlink == 0 {
		return false
	}
	st, err := os.Stat(filepath.Join(dir, entry.Name()))
	return err == nil && st.Mode().IsRegular()
}

// mediaEntries returns the names of the visible regular files in dir accepted by keep.
func mediaEntries(dir string, keep func(string) bool) []string {
	entries, err := os.ReadDir(dir)
	if err != nil {
		log.Debugf("Could not read directory %s: %v", dir, err)
		return nil
	}
	var names []string
	for _, e := range entries {
		if !IsHidden(e.Name()) && keep(e.Name()) && IsRegularFile(dir, e) {
			names = append(names, e.Name())
		}
	}
	return names
}

// AudioFiles returns the visible audio files directly inside dir, sorted by name.
func AudioFiles(dir string) []string { return mediaEntries(dir, IsAudioFile) }

// VideoFiles returns the visible video files directly inside dir, sorted by name.
func VideoFiles(dir string) []string { return mediaEntries(dir, IsVideoFile) }

// AudioFileExists reports whether dir contains a visible audio file.
func AudioFileExists(dir string) bool { return len(AudioFiles(dir)) > 0 }

// IsAudioFile reports whether filename has an audio file extension.
func IsAudioFile(filename string) bool {
	switch strings.ToLower(filepath.Ext(filename)) {
	case ".mp3", ".ogg", ".wav", ".flac", ".m4a", ".aac", ".opus", ".wma":
		return true
	}
	return false
}

// IsVideoFile reports whether filename has a video file extension.
func IsVideoFile(filename string) bool {
	switch strings.ToLower(filepath.Ext(filename)) {
	case ".mp4", ".mpeg", ".mpg", ".m4v", ".m2v", ".avi", ".mov", ".mkv", ".flv", ".wmv", ".webm":
		return true
	}
	return false
}

// IsImageFile reports whether filename has an image file extension.
func IsImageFile(filename string) bool {
	switch strings.ToLower(filepath.Ext(filename)) {
	case ".jpg", ".jpeg", ".png", ".gif", ".bmp", ".tiff":
		return true
	}
	return false
}

// SplitIntoBatches splits items into consecutive batches of at most batchSize items.
func SplitIntoBatches[T any](items []T, batchSize int) [][]T {
	var batches [][]T
	for batchSize < len(items) {
		items, batches = items[batchSize:], append(batches, items[0:batchSize:batchSize])
	}
	batches = append(batches, items)
	return batches
}

// FindCoverImage returns the first square image in rootDir, or "" if there is none.
func FindCoverImage(rootDir string) string {
	return findImage(rootDir, "cover", func(x, y int) bool { return x == y })
}

// FindBackgroundImage returns the first non-square image in rootDir, or "" if there is none.
func FindBackgroundImage(rootDir string) string {
	return findImage(rootDir, "background", func(x, y int) bool { return x != y })
}

func findImage(rootDir, kind string, match func(x, y int) bool) string {
	for _, name := range mediaEntries(rootDir, IsImageFile) {
		x, y, err := GetImageDimensions(filepath.Join(rootDir, name))
		if err != nil {
			log.Debugf("Could not get image dimensions of %s in %s: %v", name, rootDir, err)
			continue
		}
		if match(x, y) {
			log.Debugf("Found %s image %s in %s", kind, name, rootDir)
			return name
		}
	}
	log.Debugf("No %s image found in %s", kind, rootDir)
	return ""
}

// GetImageDimensions returns width and height of a JPEG, PNG or GIF image. Only the image
// header is decoded.
func GetImageDimensions(filePath string) (int, int, error) {
	file, err := os.Open(filePath)
	if err != nil {
		return 0, 0, err
	}
	defer file.Close()
	cfg, _, err := image.DecodeConfig(file)
	if err != nil {
		return 0, 0, fmt.Errorf("decoding image header: %w", err)
	}
	return cfg.Width, cfg.Height, nil
}
