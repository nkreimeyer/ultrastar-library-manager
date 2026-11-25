//go:build !unix

package main

import "os/exec"

// newSession does nothing: on Windows rundll32 hands the address to the browser and exits.
func newSession(*exec.Cmd) {
	// Nothing to set: rundll32 exits at once and the browser is no child of the UI.
}
