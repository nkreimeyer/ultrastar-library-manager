package repairtxt

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/nkreimeyer/library-manager/config"
)

// The checks of the repair inputs, shared by the repair subcommand and the UI. Each names
// the input in its error the way the caller does (InputNames): the CLI with its flags, the
// UI with its fields.

// InputNames name the inputs in error messages.
type InputNames struct {
	WordList, Words, LyricsCache string
}

// ResolveWordFiles parses the "[lang=]path" word list arguments (ParseWordListFlag, whose
// error is returned as it is) and checks that every word list and words file exists (the
// error is "<name>: <stat error>").
func ResolveWordFiles(wordLists, words []string, names InputNames) ([]WordList, []string, error) {
	var lists []WordList
	for _, w := range wordLists {
		wl, err := ParseWordListFlag(w)
		if err != nil {
			return nil, nil, err
		}
		if _, err := os.Stat(wl.Path); err != nil {
			return nil, nil, fmt.Errorf("%s: %w", names.WordList, err)
		}
		lists = append(lists, wl)
	}
	var files []string
	for _, w := range words {
		if _, err := os.Stat(w); err != nil {
			return nil, nil, fmt.Errorf("%s: %w", names.Words, err)
		}
		files = append(files, w)
	}
	return lists, files, nil
}

// LyricsCacheDir returns the folder for the cached lyrics answers: cache, by default
// <out>/lyrics-cache. It is written in dry runs too, so it must not be inside the song dir
// (it would become a song folder); the error is "<name>: <reason>".
func LyricsCacheDir(cfg config.Config, cache string, names InputNames) (string, error) {
	if cache == "" {
		cache = filepath.Join(cfg.OutDir, "lyrics-cache")
	}
	dir, err := cfg.OutsideSongDir(cache)
	if err != nil {
		return "", fmt.Errorf("%s: %w", names.LyricsCache, err)
	}
	return dir, nil
}
