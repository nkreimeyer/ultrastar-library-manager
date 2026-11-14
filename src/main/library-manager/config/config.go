// Package config holds the run configuration shared by all modules.
package config

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// Config is passed explicitly to every module. All paths are absolute and cleaned.
type Config struct {
	// SongDir is the library root given with -s.
	SongDir string
	// OutDir receives invalid/, extra-files/ and reports. Never inside SongDir.
	OutDir string
	// DryRun disables every modification of the library (reports are still written to OutDir).
	DryRun bool
}

// DefaultOutDirName is the name of the default output folder, created next to the song dir.
const DefaultOutDirName = "sanitizer-output"

// OutputMarker is a file created in every output folder. Song folder discovery skips
// folders containing it, so an output folder is never scanned as part of a library.
const OutputMarker = ".library-manager-output"

// LegacyOutputMarker is the marker of output folders written under the old program name
// (songs-sanitizer). Discovery skips folders holding it as well.
const LegacyOutputMarker = ".songs-sanitizer-output"

// New validates and normalizes the given directories. An empty outDir defaults to
// DefaultOutDirName next to songDir. Symlinks are resolved before checking that the output
// dir is not inside the song dir.
func New(songDir, outDir string, dryRun bool) (Config, error) {
	absSong, err := filepath.Abs(songDir)
	if err != nil {
		return Config{}, fmt.Errorf("invalid song dir %q: %w", songDir, err)
	}
	if st, err := os.Stat(absSong); err != nil || !st.IsDir() {
		return Config{}, fmt.Errorf("song dir %q must be an existing directory", absSong)
	}
	if absSong, err = filepath.EvalSymlinks(absSong); err != nil {
		return Config{}, fmt.Errorf("resolving song dir %q: %w", songDir, err)
	}
	if outDir == "" {
		outDir = filepath.Join(filepath.Dir(absSong), DefaultOutDirName)
	}
	absOut, err := filepath.Abs(outDir)
	if err != nil {
		return Config{}, fmt.Errorf("invalid output dir %q: %w", outDir, err)
	}
	if absOut, err = resolveExisting(absOut); err != nil {
		return Config{}, fmt.Errorf("resolving output dir %q: %w", outDir, err)
	}
	if IsWithin(absOut, absSong) {
		return Config{}, fmt.Errorf("output dir %q must not be inside the song dir %q", absOut, absSong)
	}
	cfg := Config{SongDir: absSong, OutDir: absOut, DryRun: dryRun}
	// Reprocessing e.g. <out>/invalid with the same output dir would move songs onto themselves.
	for _, d := range []string{cfg.InvalidDir(), cfg.ExtraFilesDir(), cfg.OriginalsDir()} {
		if IsWithin(absSong, d) {
			return Config{}, fmt.Errorf("song dir %q must not be inside %q of the output dir; use a different -o", absSong, d)
		}
	}
	return cfg, nil
}

// resolveExisting resolves symlinks in the longest existing prefix of the absolute path p
// and appends the remaining (not yet existing) elements.
func resolveExisting(p string) (string, error) {
	rest := ""
	for cur := p; ; cur = filepath.Dir(cur) {
		if _, err := os.Lstat(cur); err == nil {
			resolved, err := filepath.EvalSymlinks(cur)
			if err != nil {
				return "", err
			}
			return filepath.Join(resolved, rest), nil
		}
		if parent := filepath.Dir(cur); parent == cur {
			return p, nil
		}
		rest = filepath.Join(filepath.Base(cur), rest)
	}
}

// PrepareOutDir creates the output dir and its OutputMarker file. This happens in dry-run
// mode too, because reports are written there.
func (c Config) PrepareOutDir() error {
	if err := os.MkdirAll(c.OutDir, 0o755); err != nil {
		return fmt.Errorf("creating output dir: %w", err)
	}
	// Remove temp files of reports/journal left behind by an interrupted run.
	if stale, err := filepath.Glob(filepath.Join(c.OutDir, ".*.tmp")); err == nil {
		for _, f := range stale {
			os.Remove(f)
		}
	}
	marker := filepath.Join(c.OutDir, OutputMarker)
	if err := os.WriteFile(marker, []byte("Output folder of library-manager. Song folder discovery skips folders containing this file.\n"), 0o644); err != nil {
		return fmt.Errorf("creating %s: %w", marker, err)
	}
	return nil
}

// InvalidDir is where broken song folders are moved to.
func (c Config) InvalidDir() string { return filepath.Join(c.OutDir, "invalid") }

// ExtraFilesDir is where files removed from song folders are moved to.
func (c Config) ExtraFilesDir() string { return filepath.Join(c.OutDir, "extra-files") }

// OriginalsDir is where the originals of re-encoded audio files are kept.
func (c Config) OriginalsDir() string { return filepath.Join(c.OutDir, "originals") }

// JournalPath is the media journal (see package journal).
func (c Config) JournalPath() string { return filepath.Join(c.OutDir, "media-journal.txt") }

// ReportPath returns the path of a report file in the output directory.
func (c Config) ReportPath(name string) string { return filepath.Join(c.OutDir, name) }

// RelPath returns path relative to SongDir. Paths outside SongDir fall back to their base name.
func (c Config) RelPath(path string) string {
	rel, err := filepath.Rel(c.SongDir, path)
	if err != nil || rel == "." || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return filepath.Base(path)
	}
	return rel
}

// IsWithin reports whether path equals root or lies below it.
func IsWithin(path, root string) bool {
	rel, err := filepath.Rel(root, path)
	if err != nil {
		return false
	}
	return rel == "." || (rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)))
}
