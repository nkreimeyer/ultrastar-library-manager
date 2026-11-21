//go:build unix

package fsops

import (
	"syscall"
	"testing"
)

// sameDevice reports whether a and b are on the same file system.
func sameDevice(t *testing.T, a, b string) bool {
	t.Helper()
	var sa, sb syscall.Stat_t
	if syscall.Stat(a, &sa) != nil || syscall.Stat(b, &sb) != nil {
		return true
	}
	return sa.Dev == sb.Dev
}

// mkfifo creates a named pipe at path.
func mkfifo(path string, mode uint32) error {
	return syscall.Mkfifo(path, mode)
}
