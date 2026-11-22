//go:build unix

package mediatools

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"syscall"
	"testing"

	"github.com/nkreimeyer/library-manager/internal/testutil"
)

// pgidOf starts cmd, returns the process group it runs in and waits for it.
func pgidOf(t *testing.T, cmd *exec.Cmd) int {
	t.Helper()
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	defer cmd.Wait() //nolint:errcheck
	defer cmd.Process.Kill()
	pgid, err := syscall.Getpgid(cmd.Process.Pid)
	if err != nil {
		t.Fatal(err)
	}
	return pgid
}

// With SetDetachChildren a tool runs in a process group of its own, so the terminal's
// Ctrl-C (sent to the foreground group) does not reach it; without, it stays in ours.
func TestDetach(t *testing.T) {
	if got := pgidOf(t, Detach(exec.Command("sleep", "5"))); got != syscall.Getpgrp() {
		t.Errorf("off: the tool runs in group %d, not ours (%d)", got, syscall.Getpgrp())
	}
	restore := SetDetachChildren(true)
	cmd := Detach(exec.Command("sleep", "5"))
	if got := pgidOf(t, cmd); got == syscall.Getpgrp() || got != cmd.Process.Pid {
		t.Errorf("on: the tool runs in group %d (ours %d, its pid %d)", got, syscall.Getpgrp(), cmd.Process.Pid)
	}
	restore()
	if detachChildren.Load() {
		t.Error("restore left it on")
	}
}

// Probe, the version check and CombinedOutput (the encodes) detach their tool.
func TestToolsRunDetached(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("reads the process group from /proc")
	}
	t.Cleanup(SetDetachChildren(true))
	pgidFile := filepath.Join(t.TempDir(), "pgid")
	// Field 5 of /proc/<pid>/stat is the process group (the name in field 2 has no blanks).
	cut, err := exec.LookPath("cut")
	if err != nil {
		t.Skip("no cut")
	}
	script := "#!/bin/sh\n" + cut + " -d' ' -f5 /proc/$$/stat >> '" + pgidFile + "'\necho '{}'\n"
	testutil.FakeTools(t, map[string]string{"ffprobe": script, "ffmpeg": script})

	Probe(context.Background(), "x.mp3")   //nolint:errcheck
	detect(context.Background(), "ffmpeg") // runs "ffmpeg -version"
	CombinedOutput(exec.Command("ffmpeg")) //nolint:errcheck
	data, err := os.ReadFile(pgidFile)
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Fields(string(data))
	if len(lines) != 3 {
		t.Fatalf("ran %d tools: %q", len(lines), data)
	}
	for i, l := range lines {
		if pgid, err := strconv.Atoi(l); err != nil || pgid == syscall.Getpgrp() {
			t.Errorf("tool %d ran in our process group %s", i+1, l)
		}
	}
}
