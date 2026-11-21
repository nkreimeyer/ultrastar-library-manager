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
