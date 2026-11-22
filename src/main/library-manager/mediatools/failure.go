package mediatools

import (
	"errors"
	"os/exec"
	"path/filepath"
	"strings"
)

// readMessages are (lower-case) parts of ffmpeg/ffprobe messages that name an error of
// reading or writing through the file system or the network, not of the file: a NAS
// connection that dropped for a moment, file handles running out, missing permissions.
// Such a failure can heal without the file changing (a permission change does not touch
// the mtime either).
var readMessages = []string{
	"input/output error",
	"connection reset",
	"connection refused",
	"connection timed out",
	"timed out",
	"broken pipe",
	"stale file handle",
	"resource temporarily unavailable",
	"too many open files",
	"no space left",
	"host is down",
	"network is unreachable",
	"network is down",
	"transport endpoint is not connected",
	"permission denied",
	"interrupted system call",
	"device or resource busy",
}

// otherSystemMessages are system errors that ffmpeg also prints for a damaged file: a
// demuxer that reads a bogus size fails with ENOMEM ("Error during demuxing: Cannot
// allocate memory"), and AVERROR(EPERM) is -1, the generic error code of many decoders
// ("Error submitting packet to decoder: Operation not permitted"). They keep a result
// out of the cache (SystemError), but after an exit status 0 they say nothing about a
// read error (ReadError): a real lack of memory kills the tool, a real EPERM fails the open.
var otherSystemMessages = []string{
	"cannot allocate memory",
	"out of memory",
	"operation not permitted",
}

// SystemError reports whether ffmpeg/ffprobe output names an error of the system rather
// than of the file (readMessages and otherSystemMessages). A result obtained with such an
// error, failed or not (ffprobe may exit 0 after a read error, with partial results), may
// say nothing lasting about the file and must not be cached.
//
// paths are the files the tool was given: they are cut out of every line before matching,
// so a folder named "Timed Out" or a file "Broken Pipe.mp3" names no error. Indented lines
// are skipped: at info level ffmpeg dumps the streams and metadata of its input and output
// indented (a title tag "Permission Denied"), while its messages start at the line start.
func SystemError(output string, paths ...string) bool {
	return matchMessages(output, paths, readMessages, otherSystemMessages)
}

// ReadError reports whether the output of an ffmpeg/ffprobe run names a read (or write)
// error of the file system or the network (readMessages; paths and indented lines as in
// SystemError). ffmpeg and ffprobe exit 0 after such an error with what they read so far,
// so a result whose output names one is incomplete. Unlike SystemError it ignores the
// messages that damaged files cause as well (otherSystemMessages).
func ReadError(output string, paths ...string) bool {
	return matchMessages(output, paths, readMessages)
}

// matchMessages reports whether a line of output (indented lines skipped, paths cut out)
// contains one of the messages, ignoring case.
func matchMessages(output string, paths []string, messages ...[]string) bool {
	for _, line := range strings.Split(output, "\n") {
		if line == "" || line[0] == ' ' || line[0] == '\t' {
			continue
		}
		for _, p := range paths {
			if p != "" {
				// The tools print the path as given; Probe passes it cleaned.
				line = strings.ReplaceAll(strings.ReplaceAll(line, p, ""), filepath.Clean(p), "")
			}
		}
		line = strings.ToLower(line)
		for _, list := range messages {
			for _, m := range list {
				if strings.Contains(line, m) {
					return true
				}
			}
		}
	}
	return false
}

// FileFailure reports whether err, the error of an ffmpeg/ffprobe run with the output
// output, is a property of the file, so that it stays until the file changes: the tool ran
// and exited normally with an error status (an *exec.ExitError, possibly wrapped, e.g. in a
// *ProbeError), and its output names no system error (SystemError, with the same paths). A
// tool that could not start, a process killed by a signal (the OOM killer, a user) and a
// read error of the file system are no file failures. A nil err is none either.
func FileFailure(err error, output string, paths ...string) bool {
	var exitErr *exec.ExitError
	if !errors.As(err, &exitErr) || exitErr.ProcessState == nil || !exitErr.Exited() {
		return false
	}
	return !SystemError(output, paths...)
}

// UnreadableFile reports whether err, an error of Probe or mediacache.Probe for path, says
// that ffprobe cannot read the file itself: a *ProbeError that is a FileFailure, or one
// from the media cache (which stores only such failures). Every other error says nothing
// about the file: ffprobe could not run (ErrNotRun), was killed by a signal, hit a read
// error of the file system (a NAS dropping out for a moment), or printed no usable JSON.
// A nil err is no unreadable file either.
func UnreadableFile(err error, path string) bool {
	var pe *ProbeError
	if !errors.As(err, &pe) || pe.Err == nil {
		return false
	}
	var exitErr *exec.ExitError
	if !errors.As(pe.Err, &exitErr) {
		return true // a cached failure (see ProbeError.Err)
	}
	return FileFailure(pe.Err, pe.Stderr, path)
}
