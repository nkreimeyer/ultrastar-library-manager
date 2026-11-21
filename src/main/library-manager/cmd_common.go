package main

import (
	"flag"
	"fmt"
	"io"

	log "github.com/sirupsen/logrus"

	"github.com/nkreimeyer/library-manager/config"
	"github.com/nkreimeyer/library-manager/fsops"
	"github.com/nkreimeyer/library-manager/organize"
)

// The steps every command shares: usage errors, the leftover-argument check, -v, -undo and
// the summary with the exit code.

// boolFlagHint is the leftover-argument hint of the commands whose typical mistake is a
// bool flag written as "-<name> false".
func boolFlagHint(name string) string {
	return fmt.Sprintf("Bool flags must be written as -%s=false, not -%s false (parsing stops at the first non-flag argument).", name, name)
}

// usageError prints "Error: <message>", a blank line and the usage, and returns exitUsage.
func usageError(output io.Writer, usage func(), format string, a ...any) int {
	fmt.Fprintf(output, "Error: "+format+"\n\n", a...)
	usage()
	return exitUsage
}

// leftoverError is the usage error for arguments after the flags: they usually mean a bool
// flag was written as "-t false", which stops flag parsing and silently ignores every
// following flag.
func leftoverError(output io.Writer, usage func(), args []string, hint string) int {
	return usageError(output, usage, "unexpected arguments %q.\n%s", args, hint)
}

// setLogLevel applies -v. An unknown level prints one line (no usage) and returns false.
func setLogLevel(output io.Writer, level string) bool {
	l, err := log.ParseLevel(level)
	if err != nil {
		fmt.Fprintf(output, "Error: invalid log level %q (use debug, info, warn or error).\n", level)
		return false
	}
	log.SetLevel(l)
	return true
}

// plannedSuffix marks the summary of a dry run.
func plannedSuffix(dryRun bool) string {
	if dryRun {
		return " (planned)"
	}
	return ""
}

// exitStatus is exitFailures if anything failed, else exitOK.
func exitStatus(failed bool) int {
	if failed {
		return exitFailures
	}
	return exitOK
}

// subcommand is the flag set of a subcommand, which reports its errors to output and has
// -v.
type subcommand struct {
	*flag.FlagSet
	output   io.Writer
	logLevel *string
}

func newSubcommand(name string, output io.Writer) *subcommand {
	fs := flag.NewFlagSet(name, flag.ContinueOnError)
	fs.SetOutput(output)
	return &subcommand{FlagSet: fs, output: output, logLevel: fs.String("v", "info", "Log level (debug, info, warn, error)")}
}

// undoFlag defines -undo of the commands that write the organize journal.
func (c *subcommand) undoFlag() *bool {
	return c.Bool("undo", false, "Undo the most recent organize, dedupe, add or remove run recorded in the journal (needs -o or -l)")
}

// usageError prints the error and the usage of the subcommand and returns exitUsage.
func (c *subcommand) usageError(format string, a ...any) int {
	return usageError(c.output, c.Usage, format, a...)
}

// parse parses args. ok is false if the command is done: -h (exitOK), a flag error (the
// flag package printed it and the usage) or leftover arguments (a usage error with hint).
func (c *subcommand) parse(args []string, hint string) (code int, ok bool) {
	if err := c.Parse(args); err != nil {
		if err == flag.ErrHelp {
			return exitOK, false
		}
		return exitUsage, false
	}
	if c.NArg() > 0 {
		return leftoverError(c.output, c.Usage, c.Args(), hint), false
	}
	return 0, true
}

// setLogLevel applies -v (see the function setLogLevel).
func (c *subcommand) setLogLevel() bool { return setLogLevel(c.output, *c.logLevel) }

// checkUndoFolder is the usage error of -undo without a folder to find the journal in, or
// ok.
func (c *subcommand) checkUndoFolder(library, outDir string) (code int, ok bool) {
	if library == "" && outDir == "" {
		return c.usageError("-undo needs the output folder: give -o <out> or -l <library>."), false
	}
	return 0, true
}

// journaledRun is what organize, dedupe, add and remove share once their flags are checked:
// the configuration (newConfig, or for -undo the journal's folder from -o or -l), the start
// log, then either organize.Undo or (after preparing the output folder) start, and the
// summary. name is the command in the failure message. It returns the exit code.
func journaledRun(name string, undo bool, library, outDir string, dryRun bool,
	newConfig func() (config.OrganizeConfig, error), start func(config.OrganizeConfig, *fsops.Ops) error) int {
	var cfg config.OrganizeConfig
	var err error
	if undo {
		cfg, err = config.NewOrganize(nil, library, outDir, dryRun)
	} else {
		cfg, err = newConfig()
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
	if undo {
		runErr = organize.Undo(cfg, ops, organize.UndoOptions{})
	} else {
		if err := cfg.PrepareOutDir(); err != nil {
			log.Error(err)
			return exitFailures
		}
		runErr = start(cfg, ops)
	}
	if runErr != nil {
		log.Errorf("%s failed: %v", name, runErr)
	}
	st := ops.Stats()
	log.Infof("Done%s: %d moved, %d failed operations", plannedSuffix(cfg.DryRun), st.Moved, st.Failed)
	return exitStatus(st.Failed > 0 || runErr != nil)
}
