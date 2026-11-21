// Package utils contains helpers shared by the sanitizer modules: song txt decoding and
// lookup (txt.go), song folder discovery (folders.go), image headers and cover/background
// choice (image.go) and media file helpers (this file).
package utils

import (
	"os"
	"path/filepath"
	"strings"
	"unicode/utf8"

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

// NonUTF8MediaNames returns the names of the visible audio, video and image files among
// entries that are not valid UTF-8 (e.g. Latin-1 names from old archives). fixtxt leaves
// folders containing such files untouched.
func NonUTF8MediaNames(entries []os.DirEntry) []string {
	var bad []string
	for _, e := range entries {
		n := e.Name()
		if utf8.ValidString(n) || IsHidden(n) || e.IsDir() {
			continue
		}
		if IsAudioFile(n) || IsVideoFile(n) || IsImageFile(n) {
			bad = append(bad, n)
		}
	}
	return bad
}

// AudioFiles returns the visible audio files directly inside dir, sorted by name.
func AudioFiles(dir string) []string { return mediaEntries(dir, IsAudioFile) }

// VideoFiles returns the visible video files directly inside dir, sorted by name.
func VideoFiles(dir string) []string { return mediaEntries(dir, IsVideoFile) }

// IsAudioFile reports whether filename has an audio file extension (".mka" is Matroska
// audio; ".mkv" is a video).
func IsAudioFile(filename string) bool {
	switch strings.ToLower(filepath.Ext(filename)) {
	case ".mp3", ".ogg", ".wav", ".flac", ".m4a", ".aac", ".opus", ".wma", ".mka":
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

// IsImageFile reports whether filename has an image file extension (".jfif" is JPEG).
// ReadImageHeader can measure all of them except TIFF.
func IsImageFile(filename string) bool {
	switch strings.ToLower(filepath.Ext(filename)) {
	case ".jpg", ".jpeg", ".jfif", ".png", ".gif", ".bmp", ".webp", ".tif", ".tiff":
		return true
	}
	return false
}
