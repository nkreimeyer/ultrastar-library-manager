//go:build !unix && !windows

package mediatools

import "os/exec"

// newProcessGroup does nothing on systems without process groups the UI could use.
func newProcessGroup(*exec.Cmd) {
	// No process groups here: the tools stay in the UI's group.
}
