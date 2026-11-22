package main

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	log "github.com/sirupsen/logrus"
)

// inTerminalEnv marks a process the program started inside a terminal window, so it never
// starts another one.
const inTerminalEnv = "LIBRARY_MANAGER_IN_TERMINAL"

// isTerminal reports whether f is a terminal: a character device that is not /dev/null (a
// double-click gives a program /dev/null or a pipe, not a terminal).
func isTerminal(f *os.File) bool {
	st, err := f.Stat()
	if err != nil || st.Mode()&os.ModeCharDevice == 0 {
		return false
	}
	null, err := os.Stat(os.DevNull)
	return err != nil || !os.SameFile(st, null)
}

// needsTerminal reports whether a program started without arguments should open a terminal
// window for itself: it was not started from a terminal (a double-click in a file manager),
// and there is a desktop to open one on. Windows is never asked: a console program started
// by a double-click gets its console window from the system.
func needsTerminal(goos string, stdinTTY, stdoutTTY bool, getenv func(string) string) bool {
	switch {
	case goos == "windows", getenv(inTerminalEnv) != "", stdinTTY, stdoutTTY:
		return false
	case goos == "darwin":
		return true
	}
	return getenv("DISPLAY") != "" || getenv("WAYLAND_DISPLAY") != ""
}

// terminals are the terminal emulators tried on Linux and the BSDs, with the arguments that
// make them run the rest of the command line.
var terminals = []struct {
	name string
	args []string
}{
	{"x-terminal-emulator", []string{"-e"}},
	{"gnome-terminal", []string{"--"}},
	{"konsole", []string{"-e"}},
	{"xfce4-terminal", []string{"-x"}},
	{"mate-terminal", []string{"-x"}},
	{"kitty", nil},
	{"alacritty", []string{"-e"}},
	{"wezterm", []string{"start", "--"}},
	{"xterm", []string{"-e"}},
}

// shellQuote quotes s for a POSIX shell.
func shellQuote(s string) string { return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'" }

// terminalCommand returns the command line that runs exe in a new terminal window, or false
// if no terminal is known. The program is started through env, which sets inTerminalEnv in
// the new process (terminal servers do not pass on the environment of their client).
func terminalCommand(goos, exe string, lookPath func(string) (string, error)) ([]string, bool) {
	run := []string{"env", inTerminalEnv + "=1", exe}
	if goos == "darwin" {
		script := strings.ReplaceAll(strings.ReplaceAll(shellQuote(inTerminalEnv+"=1")+" "+shellQuote(exe), `\`, `\\`), `"`, `\"`)
		return []string{"osascript",
			"-e", `tell application "Terminal"`,
			"-e", "activate",
			"-e", fmt.Sprintf(`do script "exec env %s"`, script),
			"-e", "end tell"}, true
	}
	for _, t := range terminals {
		if path, err := lookPath(t.name); err == nil {
			return append(append([]string{path}, t.args...), run...), true
		}
	}
	return nil, false
}

// launchTerminal opens a terminal window that runs this program again and reports whether
// it did, in which case this process has nothing more to do. A variable so tests do not open
// windows.
var launchTerminal = func() bool {
	if !needsTerminal(runtime.GOOS, isTerminal(os.Stdin), isTerminal(os.Stdout), os.Getenv) {
		return false
	}
	exe, err := os.Executable()
	if err != nil {
		return false
	}
	argv, ok := terminalCommand(runtime.GOOS, exe, exec.LookPath)
	if !ok {
		return false
	}
	return startLauncher(exec.Command(argv[0], argv[1:]...), launcherGrace)
}

// launcherGrace is how long startLauncher waits for a terminal launcher to fail.
// gnome-terminal, konsole and osascript hand the window off and exit within it; xterm,
// kitty and alacritty keep running as the window.
const launcherGrace = 2 * time.Second

// startLauncher starts the terminal launcher cmd and reports whether it opened a window: it
// is still running after grace, or ended without an error by then. A launcher that fails
// (osascript without the permission to control Terminal, a broken x-terminal-emulator)
// is logged with its output, and the caller starts the UI in this process instead: a
// launcher that failed started no second UI.
func startLauncher(cmd *exec.Cmd, grace time.Duration) bool {
	// The output goes to a temp file, not a pipe: a terminal server the launcher starts may
	// keep it open, and would get SIGPIPE on its next write once this process has exited.
	var output func() string
	if f, err := os.CreateTemp("", "library-manager-terminal-*.log"); err == nil {
		defer os.Remove(f.Name())
		defer f.Close()
		cmd.Stdout, cmd.Stderr = f, f
		output = func() string {
			data, _ := os.ReadFile(f.Name())
			return strings.TrimSpace(string(data))
		}
	} else {
		output = func() string { return "no output captured" }
	}
	if err := cmd.Start(); err != nil {
		log.Warnf("Could not open a terminal window (%v); starting the UI here", err)
		return false
	}
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	select {
	case err := <-done:
		if err != nil {
			msg := output()
			if len(msg) > 2000 {
				msg = msg[:2000] + " …"
			}
			log.Warnf("Could not open a terminal window (%s: %v: %q); starting the UI here",
				filepath.Base(cmd.Path), err, msg)
			return false
		}
	case <-time.After(grace): // still running: the window is open (the goroutine reaps it)
	}
	return true
}
