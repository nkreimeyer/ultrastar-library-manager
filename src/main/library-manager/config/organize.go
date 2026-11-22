package config

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"

	"golang.org/x/text/unicode/norm"
)

// DefaultOrganizeOutDirName is the name of the default output folder of the organize
// subcommand, created next to the library. It is not DefaultOutDirName, so organize
// output never mixes with the output of the per-library modules.
const DefaultOrganizeOutDirName = "organize-output"

// Source is one source folder of the organize subcommand.
type Source struct {
	// Path is absolute, cleaned and symlink-free.
	Path string
	// Label names the source in reports and below <out>/invalid and <out>/duplicates:
	// the base name of Path, with " (2)", " (3)", ... appended to names that are already
	// taken (compared case- and normalization-insensitively) by an earlier source.
	Label string
	// Tier is the index of the -s flag the source was given with (0 = highest priority).
	Tier int
	// Index is the position of the source in the flattened list of all sources.
	Index int
}

// OrganizeConfig is the configuration of the organize subcommand. All paths are
// absolute, cleaned and symlink-free (as far as they exist).
type OrganizeConfig struct {
	Sources []Source
	// Library is the target library. It may not exist yet and may equal one of the
	// sources. "" only for an undo run that was given just the output dir.
	Library string
	// OutDir receives invalid/, duplicates/, the journal and the reports. Never inside the
	// library or a source.
	OutDir string
	DryRun bool
}

// NewOrganize validates and normalizes the organize configuration. tiers holds the
// source folders of each -s flag (already split with filepath.SplitList), in priority
// order. An empty outDir defaults to DefaultOrganizeOutDirName next to the library.
//
// Rules: every source is an existing folder; no source is given twice or lies inside
// another one. The library may not exist yet (if it exists it must be a folder); it may
// equal a source but must not lie inside one or contain one. The output dir must not lie
// inside the library or a source, and neither the library nor a source may lie inside
// the output dir.
//
// tiers may be empty (an undo run, which needs no sources), and library may be "" if
// outDir is given (undo with -o only); checking that a regular run has both is up to the
// caller.
//
// Paths are compared like IsWithin compares them: on darwin and windows "~/songs" and
// "~/Songs" are the same folder (SamePath).
func NewOrganize(tiers [][]string, library, outDir string, dryRun bool) (OrganizeConfig, error) {
	return newOrganize(tiers, library, outDir, dryRun, runtime.GOOS)
}

// newOrganize is NewOrganize with the path rules of the given GOOS.
func newOrganize(tiers [][]string, library, outDir string, dryRun bool, goos string) (OrganizeConfig, error) {
	cfg := OrganizeConfig{DryRun: dryRun}
	labels := map[string]bool{} // lower-cased labels taken so far
	for tier, paths := range tiers {
		if len(paths) == 0 {
			return OrganizeConfig{}, fmt.Errorf("source %d is empty", tier+1)
		}
		for _, p := range paths {
			src, err := resolveSource(p)
			if err != nil {
				return OrganizeConfig{}, err
			}
			for _, prev := range cfg.Sources {
				switch {
				case samePath(prev.Path, src, goos):
					return OrganizeConfig{}, fmt.Errorf("source dir %q is given twice", src)
				case isWithin(src, prev.Path, goos):
					return OrganizeConfig{}, fmt.Errorf("source dir %q must not be inside the source dir %q", src, prev.Path)
				case isWithin(prev.Path, src, goos):
					return OrganizeConfig{}, fmt.Errorf("source dir %q must not be inside the source dir %q", prev.Path, src)
				}
			}
			cfg.Sources = append(cfg.Sources, Source{Path: src, Label: uniqueLabel(filepath.Base(src), labels), Tier: tier, Index: len(cfg.Sources)})
		}
	}

	if library == "" && outDir == "" {
		return OrganizeConfig{}, fmt.Errorf("a library dir (or an output dir) is required")
	}
	if library != "" {
		absLib, err := filepath.Abs(library)
		if err != nil {
			return OrganizeConfig{}, fmt.Errorf("invalid library dir %q: %w", library, err)
		}
		if absLib, err = resolveExisting(absLib); err != nil {
			return OrganizeConfig{}, fmt.Errorf("resolving library dir %q: %w", library, err)
		}
		if st, err := os.Stat(absLib); err == nil && !st.IsDir() {
			return OrganizeConfig{}, fmt.Errorf("library dir %q is not a directory", absLib)
		}
		for _, s := range cfg.Sources {
			switch {
			case samePath(s.Path, absLib, goos):
				// The library is also a source: its songs are candidates, too. On a
				// case-insensitive system it may be spelled differently; the source's
				// spelling is kept, so both paths stay equal strings.
				absLib = s.Path
			case isWithin(absLib, s.Path, goos):
				return OrganizeConfig{}, fmt.Errorf("library dir %q must not be inside the source dir %q", absLib, s.Path)
			case isWithin(s.Path, absLib, goos):
				return OrganizeConfig{}, fmt.Errorf("source dir %q must not be inside the library dir %q; give the library itself as a source instead", s.Path, absLib)
			}
		}
		cfg.Library = absLib
	}

	if outDir == "" {
		outDir = filepath.Join(filepath.Dir(cfg.Library), DefaultOrganizeOutDirName)
	}
	absOut, err := filepath.Abs(outDir)
	if err != nil {
		return OrganizeConfig{}, fmt.Errorf("invalid output dir %q: %w", outDir, err)
	}
	if absOut, err = resolveExisting(absOut); err != nil {
		return OrganizeConfig{}, fmt.Errorf("resolving output dir %q: %w", outDir, err)
	}
	if st, err := os.Stat(absOut); err == nil && !st.IsDir() {
		return OrganizeConfig{}, fmt.Errorf("output dir %q is not a directory", absOut)
	}
	dirs := []struct{ kind, path string }{{"library dir", cfg.Library}}
	for _, s := range cfg.Sources {
		dirs = append(dirs, struct{ kind, path string }{"source dir", s.Path})
	}
	for _, d := range dirs {
		switch {
		case d.path == "":
		case isWithin(absOut, d.path, goos):
			return OrganizeConfig{}, fmt.Errorf("output dir %q must not be inside the %s %q", absOut, d.kind, d.path)
		case isWithin(d.path, absOut, goos):
			return OrganizeConfig{}, fmt.Errorf("%s %q must not be inside the output dir %q; use a different -o", d.kind, d.path, absOut)
		}
	}
	cfg.OutDir = absOut
	return cfg, nil
}

// resolveSource returns the absolute, symlink-free path of an existing source folder.
func resolveSource(p string) (string, error) {
	if p == "" {
		return "", fmt.Errorf("empty source dir")
	}
	abs, err := filepath.Abs(p)
	if err != nil {
		return "", fmt.Errorf("invalid source dir %q: %w", p, err)
	}
	if st, err := os.Stat(abs); err != nil || !st.IsDir() {
		return "", fmt.Errorf("source dir %q must be an existing directory", abs)
	}
	if abs, err = filepath.EvalSymlinks(abs); err != nil {
		return "", fmt.Errorf("resolving source dir %q: %w", p, err)
	}
	return abs, nil
}

// uniqueLabel returns name, or name with the first free " (n)" suffix, and marks it as
// taken. Labels are compared case- and normalization-insensitively (NFC), since they become
// folder names on possibly case- or normalization-insensitive file systems (macOS).
func uniqueLabel(name string, taken map[string]bool) string {
	key := func(s string) string { return strings.ToLower(norm.NFC.String(s)) }
	label := name
	for i := 2; taken[key(label)]; i++ {
		label = fmt.Sprintf("%s (%d)", name, i)
	}
	taken[key(label)] = true
	return label
}

// InvalidDir is where broken songs are moved to, below their source label.
func (c OrganizeConfig) InvalidDir() string { return filepath.Join(c.OutDir, "invalid") }

// DuplicatesDir is where the losing versions of duplicates are moved to, below their
// source label.
func (c OrganizeConfig) DuplicatesDir() string { return filepath.Join(c.OutDir, "duplicates") }

// RemovedDir is where the remove subcommand moves the songs it removes, below their path
// in the library.
func (c OrganizeConfig) RemovedDir() string { return filepath.Join(c.OutDir, "removed") }

// JournalPath is the move journal of organize (JSON Lines), used by -undo.
func (c OrganizeConfig) JournalPath() string {
	return filepath.Join(c.OutDir, "organize-journal.jsonl")
}

// ReportPath returns the path of a report file in the output directory.
func (c OrganizeConfig) ReportPath(name string) string { return filepath.Join(c.OutDir, name) }

// RelPath returns path relative to the source folder. Paths outside it (or the source
// folder itself) fall back to their base name.
func (c OrganizeConfig) RelPath(src Source, path string) string {
	return Config{SongDir: src.Path}.RelPath(path)
}

// PrepareOutDir creates the output dir and its OutputMarker file (see
// Config.PrepareOutDir). This happens in dry-run mode too, because reports are written
// there.
func (c OrganizeConfig) PrepareOutDir() error { return Config{OutDir: c.OutDir}.PrepareOutDir() }

// NewDedupe returns the configuration of the dedupe subcommand: the library is the only
// source, so it must be an existing folder. outDir as for NewOrganize.
func NewDedupe(library, outDir string, dryRun bool) (OrganizeConfig, error) {
	if library == "" {
		return OrganizeConfig{}, fmt.Errorf("a library dir is required")
	}
	if st, err := os.Stat(library); err != nil || !st.IsDir() {
		return OrganizeConfig{}, fmt.Errorf("library dir %q must be an existing directory", library)
	}
	return NewOrganize([][]string{{library}}, library, outDir, dryRun)
}

// NewAdd returns the configuration of the add subcommand: the sources of tiers (as for
// NewOrganize, at least one) are added to the library. If the library exists, it becomes
// the first source of the first tier, so it is compared with the sources and wins a tie
// (source order). No source may be the library itself.
func NewAdd(tiers [][]string, library, outDir string, dryRun bool) (OrganizeConfig, error) {
	return newAdd(tiers, library, outDir, dryRun, runtime.GOOS)
}

// newAdd is NewAdd with the path rules of the given GOOS.
func newAdd(tiers [][]string, library, outDir string, dryRun bool, goos string) (OrganizeConfig, error) {
	if len(tiers) == 0 {
		return OrganizeConfig{}, fmt.Errorf("at least one source dir is required")
	}
	if library == "" {
		return OrganizeConfig{}, fmt.Errorf("a library dir is required")
	}
	cfg, err := newOrganize(tiers, library, outDir, dryRun, goos)
	if err != nil {
		return OrganizeConfig{}, err
	}
	for _, s := range cfg.Sources {
		if samePath(s.Path, cfg.Library, goos) {
			return OrganizeConfig{}, fmt.Errorf("source dir %q is the library; add always compares the sources with the library, so do not give it as -s", s.Path)
		}
	}
	if st, err := os.Stat(cfg.Library); err != nil || !st.IsDir() {
		return cfg, nil // a new library: nothing to compare with
	}
	withLib := make([][]string, len(tiers))
	copy(withLib, tiers)
	withLib[0] = append([]string{cfg.Library}, tiers[0]...)
	return newOrganize(withLib, cfg.Library, cfg.OutDir, dryRun, goos)
}
