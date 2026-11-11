package main

import (
	"flag"
	"fmt"
	"os"

	"github.com/nkreimeyer/ultrastar-songs-sanitizer/check"
	"github.com/nkreimeyer/ultrastar-songs-sanitizer/cleanup"
	"github.com/nkreimeyer/ultrastar-songs-sanitizer/config"
	"github.com/nkreimeyer/ultrastar-songs-sanitizer/fixaudio"
	"github.com/nkreimeyer/ultrastar-songs-sanitizer/fixtxt"
	"github.com/nkreimeyer/ultrastar-songs-sanitizer/fixvideos"
	"github.com/nkreimeyer/ultrastar-songs-sanitizer/fsops"

	log "github.com/sirupsen/logrus"
)

// Exit codes.
const (
	exitOK       = 0
	exitFailures = 1 // the run completed, but some operations failed
	exitUsage    = 2 // invalid command line
)

type module struct {
	name    string
	enabled bool
	run     func(config.Config, *fsops.Ops) error
}

// moduleList returns the modules in the order they run: fixtxt, cleanup, check,
// fixvideos, fixaudio. check reports on the songs as fixtxt and cleanup left them, and
// with -move-broken removes broken songs before the slow re-encoding modules run.
func moduleList(fixTxt, cleanupOn, checkOn, videos, audio bool, checkOpts check.Options) []module {
	return []module{
		{"fixtxt", fixTxt, fixtxt.Run},
		{"cleanup", cleanupOn, cleanup.Run},
		{"check", checkOn, func(cfg config.Config, ops *fsops.Ops) error { return check.Run(cfg, ops, checkOpts) }},
		{"fixvideos", videos, fixvideos.Run},
		{"fixaudio", audio, fixaudio.Run},
	}
}

func main() {
	os.Exit(run())
}

func run() int {
	songDir := flag.String("s", "", "Path to the folder containing song sub-folders (required)")
	outDir := flag.String("o", "", "Output folder for invalid/, extra-files/ and reports (default: "+config.DefaultOutDirName+"/ next to -s; must not be inside -s)")
	dryRun := flag.Bool("n", false, "Dry run: log planned changes without modifying the library")
	runFixTxt := flag.Bool("t", true, "Run the text-file sanitization module (disable with -t=false)")
	runCleanup := flag.Bool("c", true, "Run the cleanup module (disable with -c=false)")
	runCheck := flag.Bool("k", false, "Run the check module: report broken and suspicious songs (read-only unless -move-broken)")
	deepCheck := flag.Bool("deep", false, "With -k: fully decode every referenced audio/video file with ffmpeg (slow)")
	moveBroken := flag.Bool("move-broken", false, "With -k: move songs with errors to invalid/")
	runFixVideos := flag.Bool("m", false, "Run the video-fixing and re-encoding module")
	runFixAudio := flag.Bool("a", false, "Run the audio-fixing and re-encoding module")
	logLevel := flag.String("v", "info", "Log level (debug, info, warn, error)")
	flag.Usage = func() {
		fmt.Fprintf(flag.CommandLine.Output(), "Usage: %s -s <song-dir> [flags]\n\nModules run in this order: fixtxt (-t), cleanup (-c), check (-k), fixvideos (-m), fixaudio (-a).\n\nFlags:\n", os.Args[0])
		flag.PrintDefaults()
	}
	flag.Parse()

	// Leftover arguments usually mean a bool flag was written as "-t false", which stops
	// flag parsing and silently ignores every following flag (possibly including -s).
	if flag.NArg() > 0 {
		fmt.Fprintf(flag.CommandLine.Output(), "Error: unexpected arguments %q.\nBool flags must be written as -t=false, not -t false (parsing stops at the first non-flag argument).\n\n", flag.Args())
		flag.Usage()
		return exitUsage
	}
	if *songDir == "" {
		fmt.Fprintf(flag.CommandLine.Output(), "Error: -s <song-dir> is required.\n\n")
		flag.Usage()
		return exitUsage
	}

	if (*deepCheck || *moveBroken) && !*runCheck {
		fmt.Fprintf(flag.CommandLine.Output(), "Error: -deep and -move-broken only apply to the check module; add -k to run it.\n\n")
		flag.Usage()
		return exitUsage
	}

	level, err := log.ParseLevel(*logLevel)
	if err != nil {
		fmt.Fprintf(flag.CommandLine.Output(), "Error: invalid log level %q (use debug, info, warn or error).\n", *logLevel)
		return exitUsage
	}
	log.SetLevel(level)

	cfg, err := config.New(*songDir, *outDir, *dryRun)
	if err != nil {
		log.Error(err)
		return exitUsage
	}
	if err := cfg.PrepareOutDir(); err != nil {
		log.Error(err)
		return exitFailures
	}
	ops := fsops.New(cfg.DryRun)

	if cfg.DryRun {
		log.Infof("Dry run: the library will not be modified (reports are written to the output dir)")
		log.Infof("Note: later modules see the library as it is now, without the changes earlier modules would make, so their plan can differ slightly from a real run")
	}
	log.Infof("Song dir: %s", cfg.SongDir)
	log.Infof("Output dir: %s", cfg.OutDir)

	modules := moduleList(*runFixTxt, *runCleanup, *runCheck, *runFixVideos, *runFixAudio,
		check.Options{Deep: *deepCheck, MoveBroken: *moveBroken, FixTxt: *runFixTxt})

	moduleErrors := 0
	for _, m := range modules {
		if !m.enabled {
			continue
		}
		log.Infof("Running %s module...", m.name)
		if err := m.run(cfg, ops); err != nil {
			moduleErrors++
			log.Errorf("%s failed: %v", m.name, err)
		}
	}

	st := ops.Stats()
	verb := ""
	if cfg.DryRun {
		verb = " (planned)"
	}
	log.Infof("Done%s: %d written, %d moved, %d replaced, %d backed up, %d removed, %d failed operations, %d failed modules",
		verb, st.Written, st.Moved, st.Replaced, st.Copied, st.Removed, st.Failed, moduleErrors)

	if st.Failed > 0 || moduleErrors > 0 {
		return exitFailures
	}
	return exitOK
}
