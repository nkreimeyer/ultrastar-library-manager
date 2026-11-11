package main

import (
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	log "github.com/sirupsen/logrus"

	"github.com/nkreimeyer/ultrastar-songs-sanitizer/config"
	"github.com/nkreimeyer/ultrastar-songs-sanitizer/fsops"
	"github.com/nkreimeyer/ultrastar-songs-sanitizer/organize"
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

// runOrganize runs the organize subcommand with its own flag set. It returns the exit
// code (same meaning as run).
func runOrganize(args []string, output io.Writer) int {
	name := filepath.Base(os.Args[0]) + " organize"
	fs := flag.NewFlagSet(name, flag.ContinueOnError)
	fs.SetOutput(output)
	var tiers tierList
	fs.Var(&tiers, "s", fmt.Sprintf("Source folder(s) (required unless -undo). Repeat -s for every priority tier; one -s may list several folders separated by %q", string(filepath.ListSeparator)))
	library := fs.String("l", "", "Library folder to build (required unless -undo -o); may also be given as a source")
	outDir := fs.String("o", "", "Output folder for invalid/, duplicates/, the journal and the reports (default: "+config.DefaultOrganizeOutDirName+"/ next to -l)")
	priority := fs.Bool("priority", false, "The order of the -s flags decides first: a working song of an earlier -s always beats one of a later -s")
	ignoreThe := fs.Bool("ignore-the", false, "Bucket by the word after a leading \"The \" (\"The Beatles\" goes to B; the folder name is unchanged)")
	deep := fs.Bool("deep", false, "Fully decode every referenced audio/video file while checking the songs (slow)")
	dryRun := fs.Bool("n", false, "Dry run: write the plan report only; nothing is moved")
	undo := fs.Bool("undo", false, "Undo the most recent organize run recorded in the journal (needs -o or -l)")
	logLevel := fs.String("v", "info", "Log level (debug, info, warn, error)")
	fs.Usage = func() {
		fmt.Fprintf(output, "Usage: %s -s <dir>[%c<dir>...] [-s ...] -l <library> [-o <out>] [-priority] [-ignore-the] [-deep] [-n] [-v level]\n"+
			"       %s -undo [-o <out> | -l <library>] [-n] [-v level]\n\n"+
			"Moves the songs of the sources into <library>/<A-Z|0-9|#>/<Artist - Title>/, keeping the best\n"+
			"version of every song; other versions go to <out>/duplicates/, broken songs to <out>/invalid/.\n\nFlags:\n",
			name, filepath.ListSeparator, name)
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
		if len(tiers) == 0 {
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

	cfg, err := config.NewOrganize(tiers, *library, *outDir, *dryRun)
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
		runErr = organize.Run(cfg, ops, organize.Options{Priority: *priority, IgnoreThe: *ignoreThe, Deep: *deep})
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
