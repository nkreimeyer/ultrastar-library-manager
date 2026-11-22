//go:build windows

package mediatools

import (
	"os/exec"
	"syscall"
)

// A console Ctrl-C reaches every process attached to the console, except those of a new
// process group.
func newProcessGroup(cmd *exec.Cmd) {
	if cmd.SysProcAttr == nil {
		cmd.SysProcAttr = &syscall.SysProcAttr{}
	}
	cmd.SysProcAttr.CreationFlags |= syscall.CREATE_NEW_PROCESS_GROUP
}
