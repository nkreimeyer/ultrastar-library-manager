//go:build !unix

package fsops

import (
	"errors"
	"testing"
)

// sameDevice cannot tell file systems apart here: it reports the same device, so the
// cross-device tests that need two file systems skip.
func sameDevice(t *testing.T, a, b string) bool {
	t.Helper()
	return true
}

// mkfifo fails: there are no named pipes in the file system, so the FIFO tests skip.
func mkfifo(path string, mode uint32) error {
	return errors.New("named pipes are not supported on this OS")
}
