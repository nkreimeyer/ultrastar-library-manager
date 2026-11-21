package main

import (
	"flag"
	"fmt"
	"os"
	"path/filepath"

	"github.com/nkreimeyer/library-manager/check"
	"github.com/nkreimeyer/library-manager/cleanup"
	"github.com/nkreimeyer/library-manager/config"
	"github.com/nkreimeyer/library-manager/fixaudio"
	"github.com/nkreimeyer/library-manager/fixtxt"
	"github.com/nkreimeyer/library-manager/fixvideos"
	"github.com/nkreimeyer/library-manager/fsops"
	"github.com/nkreimeyer/library-manager/organize"

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

// version is set at build time (-ldflags "-X main.version=v1.2.3").
var version = "dev"

func run() int {
	if len(os.Args) == 1 {
		// Started without arguments (e.g. by a double-click): open the UI.
		return runUI(nil, flag.CommandLine.Output(), true)
	}
	if os.Args[1] == "ui" {
		return runUI(os.Args[2:], flag.CommandLine.Output(), false)
	}
	switch m := organize.Mode(os.Args[1]); m {
	case organize.ModeOrganize, organize.ModeDedupe, organize.ModeAdd:
		return runOrganize(m, os.Args[2:], flag.CommandLine.Output())
	case organize.ModeRemove:
		return runRemove(os.Args[2:], flag.CommandLine.Output())
	}
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
		fmt.Fprintf(flag.CommandLine.Output(), "Usage: %s -s <song-dir> [flags]\n       %s organize -s <dir>[%c<dir>...] [-s ...] -l <library> [flags]   (see %s organize -h)\n       %s dedupe -l <library> [flags]   (see %s dedupe -h)\n       %s add -s <dir>[%c<dir>...] [-s ...] -l <library> [flags]   (see %s add -h)\n       %s remove -l <library> [-artist <name>] [-song \"Artist - Title\"] [-from <file>] [flags]   (see %s remove -h)\n       %s ui [-addr 127.0.0.1:0] [-no-browser]   (browser UI; also started without any argument)\n\nModules run in this order: fixtxt (-t), cleanup (-c), check (-k), fixvideos (-m), fixaudio (-a).\n\nFlags:\n", os.Args[0], os.Args[0], filepath.ListSeparator, os.Args[0], os.Args[0], os.Args[0], os.Args[0], filepath.ListSeparator, os.Args[0], os.Args[0], os.Args[0], os.Args[0])
		flag.PrintDefaults()
	}
	flag.Parse()

	out := flag.CommandLine.Output()
	if flag.NArg() > 0 {
		return leftoverError(out, flag.Usage, flag.Args(), boolFlagHint("t"))
	}
	if *songDir == "" {
		return usageError(out, flag.Usage, "-s <song-dir> is required.")
	}

	if (*deepCheck || *moveBroken) && !*runCheck {
		return usageError(out, flag.Usage, "-deep and -move-broken only apply to the check module; add -k to run it.")
	}

	if !setLogLevel(out, *logLevel) {
		return exitUsage
	}

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
		log.Infof("Note: later modules see the txt changes and moves earlier modules would make, but not their planned media re-encodes, so their plan can differ slightly from a real run")
	}
	log.Infof("Song dir: %s", cfg.SongDir)
	log.Infof("Output dir: %s", cfg.OutDir)

	modules := moduleList(*runFixTxt, *runCleanup, *runCheck, *runFixVideos, *runFixAudio,
		check.Options{Deep: *deepCheck, MoveBroken: *moveBroken})

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
	log.Infof("Done%s: %d written, %d moved, %d replaced, %d backed up, %d removed, %d failed operations, %d failed modules",
		plannedSuffix(cfg.DryRun), st.Written, st.Moved, st.Replaced, st.Copied, st.Removed, st.Failed, moduleErrors)
	return exitStatus(st.Failed > 0 || moduleErrors > 0)
}
