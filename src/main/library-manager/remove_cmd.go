package main

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	log "github.com/sirupsen/logrus"

	"github.com/nkreimeyer/library-manager/config"
	"github.com/nkreimeyer/library-manager/fsops"
	"github.com/nkreimeyer/library-manager/organize"
)

// stringList is a repeatable string flag.
type stringList []string

func (s *stringList) String() string     { return strings.Join(*s, ", ") }
func (s *stringList) Set(v string) error { *s = append(*s, v); return nil }

// runRemove runs the remove subcommand. It returns the exit code (same meaning as run).
func runRemove(args []string, output io.Writer) int {
	name := filepath.Base(os.Args[0]) + " remove"
	fs := newSubcommand(name, output)
	var artists, songs, files stringList
	fs.Var(&artists, "artist", "Remove every song of this artist, also collaborations with it (\"A & B\"); repeatable")
	fs.Var(&songs, "song", "Remove this song, written \"Artist - Title\" (solo and duet version); repeatable")
	fs.Var(&files, "from", "Read patterns from a file: one per line, \"Artist - Title\" or an artist; \"artist:\"/\"song:\" prefixes force the kind; # comments; repeatable")
	library := fs.String("l", "", "Library folder to remove the songs from (required unless -undo -o)")
	outDir := fs.String("o", "", "Output folder for removed/, the journal and the reports (default: "+config.DefaultOrganizeOutDirName+"/ next to -l)")
	dryRun := fs.Bool("n", false, "Dry run: write the plan report only; nothing is moved")
	undo := fs.undoFlag()
	fs.Usage = func() {
		fmt.Fprintf(output, "Usage: %s -l <library> [-artist <name>]... [-song \"Artist - Title\"]... [-from <file>]... [-o <out>] [-n] [-v level]\n"+
			undoUsageLine+
			"Moves the songs that match to <out>/removed/<path in the library>; nothing is deleted, and\n"+
			"-undo puts them back. Tags are compared like the duplicate detection does (case, accents,\n"+
			"spaces, punctuation, a leading \"The\" and feat. parts do not matter). Review the plan with -n\n"+
			"first: <out>/remove_plan.txt also lists the patterns that matched no song.\n\nFlags:\n", name, name)
		fs.PrintDefaults()
	}
	if code, ok := fs.parse(args, "Quote names with spaces (-artist \"Elton John\"); bool flags must be written as -n=false."); !ok {
		return code
	}
	hasPatterns := len(artists)+len(songs)+len(files) > 0
	if *undo {
		if hasPatterns {
			return fs.usageError("-artist, -song and -from cannot be combined with -undo.")
		}
		if code, ok := fs.checkUndoFolder(*library, *outDir); !ok {
			return code
		}
	} else {
		if *library == "" {
			return fs.usageError("-l <library> is required.")
		}
		if !hasPatterns {
			return fs.usageError("give at least one -artist, -song or -from.")
		}
	}
	if !fs.setLogLevel() {
		return exitUsage
	}

	var patterns []organize.Pattern
	for _, a := range artists {
		p, err := organize.ParsePattern(organize.PatternArtist, a, "-artist")
		if err != nil {
			return fs.usageError("%v", err)
		}
		patterns = append(patterns, p)
	}
	for _, s := range songs {
		p, err := organize.ParsePattern(organize.PatternSong, s, "-song")
		if err != nil {
			return fs.usageError("%v", err)
		}
		patterns = append(patterns, p)
	}
	for _, f := range files {
		fh, err := os.Open(f)
		if err != nil {
			return fs.usageError("-from: %v", err)
		}
		ps, err := organize.ParsePatternFile(fh, f)
		fh.Close()
		if err != nil {
			return fs.usageError("%v", err)
		}
		if len(ps) == 0 {
			return fs.usageError("-from %s: the file holds no patterns.", f)
		}
		patterns = append(patterns, ps...)
	}

	return journaledRun("remove", *undo, *library, *outDir, *dryRun,
		func() (config.OrganizeConfig, error) {
			return organize.NewConfig(organize.ModeRemove, nil, *library, *outDir, *dryRun) // the library must exist
		},
		func(cfg config.OrganizeConfig, ops *fsops.Ops) error {
			log.Infof("Library: %s; %d pattern(s)", cfg.Library, len(patterns))
			return organize.Remove(cfg, ops, patterns)
		})
}
