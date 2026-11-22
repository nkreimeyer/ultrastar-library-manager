// Package config holds the run configuration shared by all modules.
package config

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/nkreimeyer/library-manager/fsops"
	"golang.org/x/text/unicode/norm"
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

// IsOutputMarker reports whether a file name is OutputMarker or LegacyOutputMarker.
func IsOutputMarker(name string) bool {
	return name == OutputMarker || name == LegacyOutputMarker
}

// IsOutputDir reports whether dir holds an output marker (OutputMarker or
// LegacyOutputMarker), i.e. whether it is an output folder of this program. A missing dir
// is no output folder; other errors are returned.
func IsOutputDir(dir string) (bool, error) {
	for _, name := range []string{OutputMarker, LegacyOutputMarker} {
		_, err := os.Lstat(filepath.Join(dir, name))
		if err == nil {
			return true, nil
		}
		if !errors.Is(err, fs.ErrNotExist) {
			return false, err
		}
	}
	return false, nil
}

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

// OutsideSongDir returns p made absolute, with the symlinks of its existing part resolved
// (as for the output dir), and refuses a path inside the song dir: the tool's own files
// there (a cache) would change the library, also in dry runs.
func (c Config) OutsideSongDir(p string) (string, error) {
	abs, err := filepath.Abs(p)
	if err != nil {
		return "", fmt.Errorf("invalid path %q: %w", p, err)
	}
	if abs, err = resolveExisting(abs); err != nil {
		return "", fmt.Errorf("resolving %q: %w", p, err)
	}
	if IsWithin(abs, c.SongDir) {
		return "", fmt.Errorf("%q must not be inside the song dir %q", abs, c.SongDir)
	}
	return abs, nil
}

// PrepareOutDir creates the output dir and its OutputMarker file. This happens in dry-run
// mode too, because reports are written there.
func (c Config) PrepareOutDir() error {
	if err := os.MkdirAll(c.OutDir, 0o755); err != nil {
		return fmt.Errorf("creating output dir: %w", err)
	}
	// Remove temp files of reports/the journal left behind by an interrupted run.
	fsops.RemoveStaleReportTemps(c.OutDir)
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

// IsWithin reports whether path equals root or lies below it. On darwin and windows, whose
// file systems are case-insensitive by default (APFS, NTFS), the case of the paths is ignored:
// there "-s ~/Karaoke/Songs -o ~/karaoke/songs/out" puts the output dir inside the song dir.
// On darwin the Unicode normalization is ignored too, as APFS and HFS+ do: "Ärzte" typed as NFD
// (A + U+0308) is the same folder as NFC "Ärzte".
// On windows filepath.Rel already compares path elements case-insensitively; the lowering
// keeps the rule explicit and testable on other hosts.
func IsWithin(path, root string) bool {
	return isWithin(path, root, runtime.GOOS)
}

// isWithin is IsWithin for the given GOOS, so the case-insensitive rule is tested on Linux.
func isWithin(path, root, goos string) bool {
	if goos == "darwin" {
		path, root = norm.NFC.String(path), norm.NFC.String(root)
	}
	if goos == "darwin" || goos == "windows" {
		path, root = strings.ToLower(path), strings.ToLower(root)
	}
	rel, err := filepath.Rel(root, path)
	if err != nil {
		return false
	}
	return rel == "." || (rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)))
}
