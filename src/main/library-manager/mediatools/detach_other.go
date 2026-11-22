//go:build !unix && !windows

package mediatools

import "os/exec"

func newProcessGroup(*exec.Cmd) {}
