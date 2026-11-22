//go:build unix

package main

import (
	"os/exec"
	"syscall"
)

// newSession makes cmd (the browser opener) start a session of its own, as Python's
// webbrowser does: a browser it starts is then no member of the UI's process group, so a
// Ctrl-C in the UI's terminal, or the hangup when that terminal closes, does not quit it.
func newSession(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
}
