package mediatools

import (
	"os/exec"
	"sync/atomic"
)

// detachChildren is set while a program that handles its stop signals itself (the ui
// subcommand) runs: see SetDetachChildren.
var detachChildren atomic.Bool

// SetDetachChildren makes Detach start the media tools in a process group of their own
// (on Windows: a new console process group) and returns a function that restores the
// previous setting. The ui subcommand sets it: a Ctrl-C in its terminal goes to the whole
// foreground process group, and an ffmpeg encode that gets it aborts (exit 255) instead of
// finishing while the job stops. A program without a signal handler (the CLI) leaves it
// off, so that Ctrl-C still ends the tools with it and none keeps running orphaned.
func SetDetachChildren(on bool) (restore func()) {
	old := detachChildren.Swap(on)
	return func() { detachChildren.Store(old) }
}

// Detach prepares cmd (not started yet) to run outside the terminal's process group if
// SetDetachChildren is on, and returns it. Stopping it still works: exec.CommandContext
// kills the process itself, not its group. The tools read no terminal input (stdin is the
// null device or -nostdin), so a background group gets no SIGTTIN.
func Detach(cmd *exec.Cmd) *exec.Cmd {
	if detachChildren.Load() {
		newProcessGroup(cmd)
	}
	return cmd
}
