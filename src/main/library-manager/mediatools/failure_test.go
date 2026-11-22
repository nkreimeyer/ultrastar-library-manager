package mediatools

import (
	"errors"
	"os/exec"
	"runtime"
	"testing"
)

func shellErr(t *testing.T, script string) error {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("needs sh")
	}
	if _, err := exec.LookPath("sh"); err != nil {
		t.Skip("no sh")
	}
	err := exec.Command("sh", "-c", script).Run()
	if err == nil {
		t.Fatalf("%q succeeded", script)
	}
	return err
}

func TestFileFailure(t *testing.T) {
	exit1 := shellErr(t, "exit 1")
	killed := shellErr(t, "kill -9 $$")
	tests := []struct {
		name   string
		err    error
		output string
		want   bool
	}{
		{"exit status, file error", exit1, "x.mp4: Invalid data found when processing input", true},
		{"exit status, no output", exit1, "", true},
		{"wrapped in a ProbeError", &ProbeError{Err: exit1}, "moov atom not found", true},
		{"killed by a signal", killed, "", false},
		{"killed, wrapped", &ProbeError{Err: killed}, "Invalid data found", false},
		{"I/O error", exit1, "/nas/a.mp3: Input/output error", false},
		{"I/O error, other case", exit1, "INPUT/OUTPUT ERROR", false},
		{"permission", exit1, "a.mp3: Permission denied", false},
		{"stale NFS handle", exit1, "Stale file handle", false},
		{"out of memory", exit1, "Cannot allocate memory", false},
		{"not an exit error", errors.New("exit status 1"), "", false},
		{"nil", nil, "", false},
		{"start error", exec.Command("/nonexistent/tool").Run(), "", false},
	}
	for _, tt := range tests {
		if got := FileFailure(tt.err, tt.output); got != tt.want {
			t.Errorf("%s: FileFailure(%v, %q) = %v, want %v", tt.name, tt.err, tt.output, got, tt.want)
		}
	}
}

func TestSystemError(t *testing.T) {
	for out, want := range map[string]bool{
		"": false,
		"Estimating duration from bitrate, this may be inaccurate": false,
		"[mp3] Header missing":                   false,
		"Error reading file: Input/output error": true,
		"Connection reset by peer":               true,
		"Transport endpoint is not connected":    true,
		"Too many open files":                    true,
	} {
		if got := SystemError(out); got != want {
			t.Errorf("SystemError(%q) = %v, want %v", out, got, want)
		}
	}
}

// The file's own path and ffmpeg's indented dump of streams and metadata name no system
// error, however they are spelled: a folder "Timed Out", a title tag "Broken Pipe".
func TestSystemErrorIgnoresPathsAndMetadata(t *testing.T) {
	const path = "/lib/Artist - Timed Out/Permission Denied.mp3"
	for _, tt := range []struct {
		name, out string
		want      bool
	}{
		{"path in a message", "Input #0, mp3, from '" + path + "':", false},
		{"path with an error", path + ": Invalid data found when processing input", false},
		{"metadata", "  Metadata:\n    title           : Broken Pipe\n    artist          : Timed Out", false},
		{"tab-indented", "\ttitle: Input/output error", false},
		{"path, then a real error", "Input #0, mp3, from '" + path + "':\n" + path + ": Input/output error", true},
		{"uncleaned path given", "Input #0, mp3, from '/lib/Artist - Timed Out/Permission Denied.mp3':", false},
	} {
		p := path
		if tt.name == "uncleaned path given" {
			p = "/lib/Artist - Timed Out/./Permission Denied.mp3"
		}
		if got := SystemError(tt.out, p); got != tt.want {
			t.Errorf("%s: SystemError(%q) = %v, want %v", tt.name, tt.out, got, tt.want)
		}
	}
	// Without the path, the folder name in it counts (callers pass it).
	if !SystemError("Input #0, mp3, from '" + path + "':") {
		t.Error("SystemError without paths ignored the path's words")
	}
	exit1 := shellErr(t, "exit 1")
	if !FileFailure(exit1, path+": Invalid data found when processing input", path) {
		t.Error("FileFailure took the path for a system error")
	}
}

func TestUnreadableFile(t *testing.T) {
	exit1 := shellErr(t, "exit 1")
	killed := shellErr(t, "kill -9 $$")
	const path = "/lib/a.mp4"
	for _, tt := range []struct {
		name string
		err  error
		want bool
	}{
		{"nil", nil, false},
		{"file failure", &ProbeError{Err: exit1, Stderr: path + ": Invalid data found when processing input"}, true},
		{"from the cache", &ProbeError{Err: errors.New("exit status 1"), Stderr: "moov atom not found"}, true},
		{"read error", &ProbeError{Err: exit1, Stderr: path + ": Input/output error"}, false},
		{"killed", &ProbeError{Err: killed}, false},
		{"no Err", &ProbeError{}, false},
		{"not run", ErrNotRun, false},
		{"parse error", errors.New("parsing ffprobe output: unexpected end of JSON input"), false},
	} {
		if got := UnreadableFile(tt.err, path); got != tt.want {
			t.Errorf("%s: UnreadableFile = %v, want %v", tt.name, got, tt.want)
		}
	}
}

func TestFirstVideoSkipsAttachedPictures(t *testing.T) {
	probe := ProbeInfo{Streams: []Stream{
		{Index: 0, CodecType: "audio"},
		{Index: 1, CodecType: "video", CodecName: "mjpeg", Disposition: Disposition{AttachedPic: 1}},
		{Index: 2, CodecType: "video", CodecName: "h264"},
	}}
	if s, ok := probe.FirstVideo(); !ok || s.Index != 2 {
		t.Errorf("FirstVideo = %+v, %v; want stream 2", s, ok)
	}
	coverOnly := ProbeInfo{Streams: probe.Streams[:2]}
	if s, ok := coverOnly.FirstVideo(); ok {
		t.Errorf("FirstVideo of a cover-only file = %+v, want none", s)
	}
}

// ReadError: the read, I/O and network errors only. A damaged file makes ffmpeg print
// ENOMEM (a bogus size) or EPERM (-1, a decoder's generic error) and exit 0; those keep a
// result out of the cache (SystemError) but are no read error.
func TestReadError(t *testing.T) {
	const path = "/lib/Timed Out/a.m4a"
	for _, tt := range []struct {
		out       string
		read, sys bool
	}{
		{"[in#0/mov,mp4,m4a,3gp,3g2,mj2 @ 0x1] Error during demuxing: Input/output error", true, true},
		{"[in#0/mov,mp4,m4a,3gp,3g2,mj2 @ 0x1] Error during demuxing: Cannot allocate memory", false, true},
		{"[aist#0:0/aac @ 0x1] [dec:aac @ 0x2] Error submitting packet to decoder: Operation not permitted", false, true},
		{"Error while decoding stream #0:0: Operation not permitted", false, true},
		{"av_interleaved_write_frame(): No space left on device", true, true},
		{"Input #0, mov, from '" + path + "':", false, false},
		{"", false, false},
	} {
		if got := ReadError(tt.out, path); got != tt.read {
			t.Errorf("ReadError(%q) = %v, want %v", tt.out, got, tt.read)
		}
		if got := SystemError(tt.out, path); got != tt.sys {
			t.Errorf("SystemError(%q) = %v, want %v", tt.out, got, tt.sys)
		}
	}
}
