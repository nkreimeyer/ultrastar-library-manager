package main

import (
	"bytes"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/nkreimeyer/library-manager/internal/testutil"
)

// buildBinary builds the program into the test's temp dir (with a version set by ldflags).
func buildBinary(t *testing.T) string {
	t.Helper()
	if testing.Short() {
		t.Skip("builds the program")
	}
	exe := filepath.Join(t.TempDir(), "library-manager")
	if runtime.GOOS == "windows" {
		exe += ".exe"
	}
	cmd := exec.Command("go", "build", "-ldflags", "-X main.version=v9.9.9-test", "-o", exe, ".")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("go build: %v\n%s", err, out)
	}
	return exe
}

// runBinary runs exe with an environment that has no media tools and no desktop, and
// returns the exit code and the combined output.
func runBinary(t *testing.T, exe string, args ...string) (int, string) {
	t.Helper()
	cmd := exec.Command(exe, args...)
	cmd.Env = []string{"PATH=" + t.TempDir(), "HOME=" + t.TempDir()}
	var buf bytes.Buffer
	cmd.Stdout, cmd.Stderr = &buf, &buf
	err := cmd.Run()
	var ee *exec.ExitError
	switch {
	case err == nil:
		return 0, buf.String()
	case errors.As(err, &ee):
		return ee.ExitCode(), buf.String()
	}
	t.Fatal(err)
	return -1, ""
}

func binaryLibrary(t *testing.T) string {
	t.Helper()
	lib := filepath.Join(t.TempDir(), "lib")
	testutil.WriteFiles(t, filepath.Join(lib, "A - B"), map[string]string{
		"song.txt": "#ARTIST:A\n#TITLE:B\n#MP3:a.mp3\n" + validNotes(),
		"a.mp3":    "a",
		"extra.xx": "x",
	})
	return lib
}

func TestBinaryExitCodes(t *testing.T) {
	exe := buildBinary(t)

	t.Run("leftover argument is exit 2", func(t *testing.T) {
		lib := binaryLibrary(t)
		code, out := runBinary(t, exe, "-s", lib, "-o", t.TempDir(), "-t", "false")
		if code != exitUsage || !strings.Contains(out, "-t=false") {
			t.Errorf("exit %d, output %q", code, out)
		}
	})
	t.Run("missing -s is exit 2", func(t *testing.T) {
		if code, _ := runBinary(t, exe, "-n"); code != exitUsage {
			t.Errorf("exit %d, want %d", code, exitUsage)
		}
	})
	t.Run("dry run is exit 0 and leaves the library alone", func(t *testing.T) {
		lib := binaryLibrary(t)
		before := testutil.Snapshot(t, lib)
		code, out := runBinary(t, exe, "-s", lib, "-n", "-o", filepath.Join(t.TempDir(), "out"))
		if code != exitOK {
			t.Fatalf("exit %d\n%s", code, out)
		}
		if after := testutil.Snapshot(t, lib); after != before {
			t.Errorf("the dry run changed the library:\n%s\n---\n%s", before, after)
		}
	})
	t.Run("a failing operation is exit 1", func(t *testing.T) {
		testutil.SkipIfRoot(t)
		lib := binaryLibrary(t)
		txt := filepath.Join(lib, "A - B", "song.txt")
		if err := os.Chmod(txt, 0); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { os.Chmod(txt, 0o644) })
		if code, out := runBinary(t, exe, "-s", lib, "-o", t.TempDir()); code != exitFailures {
			t.Errorf("exit %d, want %d\n%s", code, exitFailures, out)
		}
	})
}

// Started without arguments (not from a terminal window, the marker variable set so no
// terminal is opened), the program serves the UI and opens the browser; the only
// "browser" on PATH here is a script that records the address. SIGTERM stops it, exit 0.
func TestBinaryNoArgsStartsUI(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("needs a shell script as the browser")
	}
	exe := buildBinary(t)
	bin, urlFile := t.TempDir(), filepath.Join(t.TempDir(), "url")
	script := "#!/bin/sh\necho \"$1\" > \"$LM_URL_FILE\"\n"
	for _, name := range []string{"xdg-open", "open"} {
		if err := os.WriteFile(filepath.Join(bin, name), []byte(script), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	cmd := exec.Command(exe)
	cmd.Env = []string{"PATH=" + bin, "HOME=" + t.TempDir(), "LM_URL_FILE=" + urlFile, inTerminalEnv + "=1"}
	var output bytes.Buffer // read only after Wait
	cmd.Stdout, cmd.Stderr = &output, &output
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	exited := make(chan error, 1)
	go func() { exited <- cmd.Wait() }()
	t.Cleanup(func() { cmd.Process.Kill() })

	var url string
	deadline := time.Now().Add(15 * time.Second)
	for url == "" && time.Now().Before(deadline) {
		if data, err := os.ReadFile(urlFile); err == nil && strings.HasSuffix(string(data), "\n") {
			url = strings.TrimSpace(string(data))
		}
		time.Sleep(20 * time.Millisecond)
	}
	if !strings.HasPrefix(url, "http://127.0.0.1:") || !strings.Contains(url, "/?token=") {
		t.Fatalf("the browser was given %q", url)
	}
	if err := cmd.Process.Signal(syscall.SIGTERM); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-exited:
		if err != nil {
			t.Errorf("exit: %v", err)
		}
	case <-time.After(15 * time.Second):
		t.Fatal("the UI did not stop on SIGTERM")
	}
	if !strings.Contains(output.String(), "library-manager v9.9.9-test: the UI runs at") {
		t.Errorf("output %q lacks the version banner", output.String())
	}
}
