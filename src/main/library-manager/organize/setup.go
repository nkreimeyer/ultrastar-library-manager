package organize

import (
	"fmt"

	"github.com/nkreimeyer/library-manager/config"
)

// NewConfig returns the configuration of a run in mode m, the one constructor the
// subcommands and the UI share: dedupe and remove work on an existing library alone
// (config.NewDedupe), add puts the library in front of the first tier (config.NewAdd),
// organize needs at least one source and a library (config.NewOrganize). The CLI checks
// its flags before (with their names in the message); the checks here give the UI the same
// rules. An undo run takes NewUndoConfig instead.
func NewConfig(m Mode, tiers [][]string, library, outDir string, dryRun bool) (config.OrganizeConfig, error) {
	switch m {
	case ModeDedupe, ModeRemove:
		return config.NewDedupe(library, outDir, dryRun)
	case ModeAdd:
		return config.NewAdd(tiers, library, outDir, dryRun)
	}
	if len(tiers) == 0 {
		return config.OrganizeConfig{}, fmt.Errorf("at least one source folder is required")
	}
	if library == "" {
		return config.OrganizeConfig{}, fmt.Errorf("a library folder is required")
	}
	return config.NewOrganize(tiers, library, outDir, dryRun)
}

// NewUndoConfig returns the configuration of -undo and the UI's history and undo: no
// sources; the journal is found in outDir, or by default in organize-output/ next to
// library.
func NewUndoConfig(library, outDir string, dryRun bool) (config.OrganizeConfig, error) {
	return config.NewOrganize(nil, library, outDir, dryRun)
}

// NewOptions returns the options of an organize, dedupe or add run. dedupe has a single
// source and keeps every song where it is, so -priority and -ignore-the (which its CLI does
// not offer) are dropped there: a UI form that still holds them from organize or add would
// otherwise report "priority: on" for the dedupe run.
func NewOptions(m Mode, priority, ignoreThe, deep bool) Options {
	if m == ModeDedupe {
		priority, ignoreThe = false, false
	}
	return Options{Mode: m, Priority: priority, IgnoreThe: ignoreThe, Deep: deep}
}
