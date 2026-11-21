package main

import (
	"flag"
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
	fs := flag.NewFlagSet(name, flag.ContinueOnError)
	fs.SetOutput(output)
	var artists, songs, files stringList
	fs.Var(&artists, "artist", "Remove every song of this artist, also collaborations with it (\"A & B\"); repeatable")
	fs.Var(&songs, "song", "Remove this song, written \"Artist - Title\" (solo and duet version); repeatable")
	fs.Var(&files, "from", "Read patterns from a file: one per line, \"Artist - Title\" or an artist; \"artist:\"/\"song:\" prefixes force the kind; # comments; repeatable")
	library := fs.String("l", "", "Library folder to remove the songs from (required unless -undo -o)")
	outDir := fs.String("o", "", "Output folder for removed/, the journal and the reports (default: "+config.DefaultOrganizeOutDirName+"/ next to -l)")
	dryRun := fs.Bool("n", false, "Dry run: write the plan report only; nothing is moved")
	undo := fs.Bool("undo", false, "Undo the most recent organize, dedupe, add or remove run recorded in the journal (needs -o or -l)")
	logLevel := fs.String("v", "info", "Log level (debug, info, warn, error)")
	fs.Usage = func() {
		fmt.Fprintf(output, "Usage: %s -l <library> [-artist <name>]... [-song \"Artist - Title\"]... [-from <file>]... [-o <out>] [-n] [-v level]\n"+
			"       %s -undo [-o <out> | -l <library>] [-n] [-v level]\n\n"+
			"Moves the songs that match to <out>/removed/<path in the library>; nothing is deleted, and\n"+
			"-undo puts them back. Tags are compared like the duplicate detection does (case, accents,\n"+
			"spaces, punctuation, a leading \"The\" and feat. parts do not matter). Review the plan with -n\n"+
			"first: <out>/remove_plan.txt also lists the patterns that matched no song.\n\nFlags:\n", name, name)
		fs.PrintDefaults()
	}
	usage := func(format string, a ...any) int {
		fmt.Fprintf(output, "Error: "+format+"\n\n", a...)
		fs.Usage()
		return exitUsage
	}
	if err := fs.Parse(args); err != nil {
		if err == flag.ErrHelp {
			return exitOK
		}
		return exitUsage
	}
	if fs.NArg() > 0 {
		return usage("unexpected arguments %q.\nQuote names with spaces (-artist \"Elton John\"); bool flags must be written as -n=false.", fs.Args())
	}
	hasPatterns := len(artists)+len(songs)+len(files) > 0
	if *undo {
		if hasPatterns {
			return usage("-artist, -song and -from cannot be combined with -undo.")
		}
		if *library == "" && *outDir == "" {
			return usage("-undo needs the output folder: give -o <out> or -l <library>.")
		}
	} else {
		if *library == "" {
			return usage("-l <library> is required.")
		}
		if !hasPatterns {
			return usage("give at least one -artist, -song or -from.")
		}
	}
	level, err := log.ParseLevel(*logLevel)
	if err != nil {
		fmt.Fprintf(output, "Error: invalid log level %q (use debug, info, warn or error).\n", *logLevel)
		return exitUsage
	}
	log.SetLevel(level)

	var patterns []organize.Pattern
	for _, a := range artists {
		p, err := organize.ParsePattern(organize.PatternArtist, a, "-artist")
		if err != nil {
			return usage("%v", err)
		}
		patterns = append(patterns, p)
	}
	for _, s := range songs {
		p, err := organize.ParsePattern(organize.PatternSong, s, "-song")
		if err != nil {
			return usage("%v", err)
		}
		patterns = append(patterns, p)
	}
	for _, f := range files {
		fh, err := os.Open(f)
		if err != nil {
			return usage("-from: %v", err)
		}
		ps, err := organize.ParsePatternFile(fh, f)
		fh.Close()
		if err != nil {
			return usage("%v", err)
		}
		if len(ps) == 0 {
			return usage("-from %s: the file holds no patterns.", f)
		}
		patterns = append(patterns, ps...)
	}

	var cfg config.OrganizeConfig
	if *undo {
		cfg, err = config.NewOrganize(nil, *library, *outDir, *dryRun)
	} else {
		cfg, err = config.NewDedupe(*library, *outDir, *dryRun) // the library must exist
	}
	if err != nil {
		log.Error(err)
		return exitUsage
	}
	ops := fsops.New(cfg.DryRun)
	if cfg.DryRun {
		log.Infof("Dry run: nothing will be moved (the plan report is written to the output dir)")
	}
	log.Infof("Output dir: %s", cfg.OutDir)
	var runErr error
	if *undo {
		runErr = organize.Undo(cfg, ops, organize.UndoOptions{})
	} else {
		if err := cfg.PrepareOutDir(); err != nil {
			log.Error(err)
			return exitFailures
		}
		log.Infof("Library: %s; %d pattern(s)", cfg.Library, len(patterns))
		runErr = organize.Remove(cfg, ops, patterns)
	}
	if runErr != nil {
		log.Errorf("remove failed: %v", runErr)
	}
	st := ops.Stats()
	verb := ""
	if cfg.DryRun {
		verb = " (planned)"
	}
	log.Infof("Done%s: %d moved, %d failed operations", verb, st.Moved, st.Failed)
	if st.Failed > 0 || runErr != nil {
		return exitFailures
	}
	return exitOK
}
