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

// tierList is the -s flag of the organize subcommand: every -s is one priority tier and
// may list several folders separated by the OS path-list separator.
type tierList [][]string

func (t *tierList) String() string {
	var parts []string
	for _, tier := range *t {
		parts = append(parts, strings.Join(tier, string(filepath.ListSeparator)))
	}
	return strings.Join(parts, " ")
}

func (t *tierList) Set(v string) error {
	*t = append(*t, filepath.SplitList(v))
	return nil
}

// runOrganize runs the organize, dedupe or add subcommand (mode) with its own flag set.
// It returns the exit code (same meaning as run).
func runOrganize(mode organize.Mode, args []string, output io.Writer) int {
	name := filepath.Base(os.Args[0]) + " " + string(mode)
	fs := flag.NewFlagSet(name, flag.ContinueOnError)
	fs.SetOutput(output)
	var tiers tierList
	priority, ignoreThe := new(bool), new(bool)
	libHelp := "Library folder to build (required unless -undo -o); may also be given as a source"
	switch mode {
	case organize.ModeDedupe:
		libHelp = "Library folder to deduplicate (required unless -undo -o)"
	case organize.ModeAdd:
		libHelp = "Library folder to add the songs to (required unless -undo -o); its songs are compared with the sources"
	}
	if mode != organize.ModeDedupe {
		fs.Var(&tiers, "s", fmt.Sprintf("Source folder(s) (required unless -undo). Repeat -s for every priority tier; one -s may list several folders separated by %q", string(filepath.ListSeparator)))
		prioHelp := "The order of the -s flags decides first: a working song of an earlier -s always beats one of a later -s"
		if mode == organize.ModeAdd {
			prioHelp += "; the library counts as part of the first -s"
		}
		priority = fs.Bool("priority", false, prioHelp)
		ignoreThe = fs.Bool("ignore-the", false, "Bucket by the word after a leading \"The \" (\"The Beatles\" goes to B; the folder name is unchanged)")
	}
	library := fs.String("l", "", libHelp)
	outDir := fs.String("o", "", "Output folder for invalid/, duplicates/, the journal and the reports (default: "+config.DefaultOrganizeOutDirName+"/ next to -l)")
	deep := fs.Bool("deep", false, "Fully decode every referenced audio/video file while checking the songs (slow)")
	dryRun := fs.Bool("n", false, "Dry run: write the plan report only; nothing is moved")
	undo := fs.Bool("undo", false, "Undo the most recent organize, dedupe, add or remove run recorded in the journal (needs -o or -l)")
	logLevel := fs.String("v", "info", "Log level (debug, info, warn, error)")
	fs.Usage = func() {
		switch mode {
		case organize.ModeDedupe:
			fmt.Fprintf(output, "Usage: %s -l <library> [-o <out>] [-deep] [-n] [-v level]\n"+
				"       %s -undo [-o <out> | -l <library>] [-n] [-v level]\n\n"+
				"Removes the duplicates of the library in place: the best version of every song stays where it\n"+
				"is, other versions (and copies marked _dupl<n>) go to <out>/duplicates/, broken songs that\n"+
				"have a working version to <out>/invalid/. Nothing is renamed or re-bucketed.\n\nFlags:\n",
				name, name)
		case organize.ModeAdd:
			fmt.Fprintf(output, "Usage: %s -s <dir>[%c<dir>...] [-s ...] -l <library> [-o <out>] [-priority] [-ignore-the] [-deep] [-n] [-v level]\n"+
				"       %s -undo [-o <out> | -l <library>] [-n] [-v level]\n\n"+
				"Adds the songs of the sources to the library, deduplicated among themselves and against the\n"+
				"library: new songs go to <library>/<A-Z|0-9|#>/<Artist - Title>/, a better version replaces the\n"+
				"library's copy (on a tie the library's copy stays), other versions go to <out>/duplicates/,\n"+
				"broken songs to <out>/invalid/. Songs already in the library are not moved otherwise.\n\nFlags:\n",
				name, filepath.ListSeparator, name)
		default:
			fmt.Fprintf(output, "Usage: %s -s <dir>[%c<dir>...] [-s ...] -l <library> [-o <out>] [-priority] [-ignore-the] [-deep] [-n] [-v level]\n"+
				"       %s -undo [-o <out> | -l <library>] [-n] [-v level]\n\n"+
				"Moves the songs of the sources into <library>/<A-Z|0-9|#>/<Artist - Title>/, keeping the best\n"+
				"version of every song; other versions go to <out>/duplicates/, broken songs to <out>/invalid/.\n\nFlags:\n",
				name, filepath.ListSeparator, name)
		}
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
		return exitUsage // the flag package already printed the error and the usage
	}
	if fs.NArg() > 0 {
		return usage("unexpected arguments %q.\nBool flags must be written as -priority=false, not -priority false (parsing stops at the first non-flag argument).", fs.Args())
	}
	set := map[string]bool{}
	fs.Visit(func(f *flag.Flag) { set[f.Name] = true })
	if *undo {
		for _, f := range []string{"s", "priority", "ignore-the", "deep"} {
			if set[f] {
				return usage("-%s cannot be combined with -undo.", f)
			}
		}
		if *library == "" && *outDir == "" {
			return usage("-undo needs the output folder: give -o <out> or -l <library>.")
		}
	} else {
		if len(tiers) == 0 && mode != organize.ModeDedupe {
			return usage("-s <source-dir> is required.")
		}
		if *library == "" {
			return usage("-l <library> is required.")
		}
	}

	level, err := log.ParseLevel(*logLevel)
	if err != nil {
		fmt.Fprintf(output, "Error: invalid log level %q (use debug, info, warn or error).\n", *logLevel)
		return exitUsage
	}
	log.SetLevel(level)

	var cfg config.OrganizeConfig
	switch {
	case *undo:
		cfg, err = config.NewOrganize(nil, *library, *outDir, *dryRun)
	case mode == organize.ModeDedupe:
		cfg, err = config.NewDedupe(*library, *outDir, *dryRun)
	case mode == organize.ModeAdd:
		cfg, err = config.NewAdd(tiers, *library, *outDir, *dryRun)
	default:
		cfg, err = config.NewOrganize(tiers, *library, *outDir, *dryRun)
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
		runErr = organize.Undo(cfg, ops)
	} else {
		if err := cfg.PrepareOutDir(); err != nil {
			log.Error(err)
			return exitFailures
		}
		log.Infof("Library: %s", cfg.Library)
		for _, s := range cfg.Sources {
			log.Infof("Source (tier %d): %s", s.Tier+1, s.Path)
		}
		runErr = organize.Run(cfg, ops, organize.Options{Mode: mode, Priority: *priority, IgnoreThe: *ignoreThe, Deep: *deep})
	}
	if runErr != nil {
		log.Errorf("organize failed: %v", runErr)
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
