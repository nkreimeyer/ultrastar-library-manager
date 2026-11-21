package mediatools

import (
	"errors"
	"os/exec"
	"strings"
)

// systemMessages are (lower-case) parts of ffmpeg/ffprobe messages that name an error of the
// system the file was read from, not of the file: a NAS connection that dropped for a
// moment, memory or file handles running out, missing permissions. Such a failure can heal
// without the file changing (a permission change does not touch the mtime either).
var systemMessages = []string{
	"input/output error",
	"connection reset",
	"connection refused",
	"connection timed out",
	"timed out",
	"broken pipe",
	"stale file handle",
	"resource temporarily unavailable",
	"cannot allocate memory",
	"out of memory",
	"too many open files",
	"no space left",
	"host is down",
	"network is unreachable",
	"network is down",
	"transport endpoint is not connected",
	"permission denied",
	"operation not permitted",
	"interrupted system call",
	"device or resource busy",
}

// SystemError reports whether ffmpeg/ffprobe output names an error of the system rather
// than of the file (see systemMessages). A result obtained with such an error, failed or
// not (ffprobe may exit 0 after a read error, with partial results), says nothing lasting
// about the file and must not be cached.
func SystemError(output string) bool {
	out := strings.ToLower(output)
	for _, m := range systemMessages {
		if strings.Contains(out, m) {
			return true
		}
	}
	return false
}

// FileFailure reports whether err, the error of an ffmpeg/ffprobe run with the output
// output, is a property of the file, so that it stays until the file changes: the tool ran
// and exited normally with an error status (an *exec.ExitError, possibly wrapped, e.g. in a
// *ProbeError), and its output names no system error (SystemError). A tool that could not
// start, a process killed by a signal (the OOM killer, a user) and a read error of the
// file system are no file failures. A nil err is none either.
func FileFailure(err error, output string) bool {
	var exitErr *exec.ExitError
	if !errors.As(err, &exitErr) || exitErr.ProcessState == nil || !exitErr.Exited() {
		return false
	}
	return !SystemError(output)
}
