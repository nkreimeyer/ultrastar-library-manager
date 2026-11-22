package main

import (
	"fmt"
	"os"
	"os/exec"
	"runtime"
	"strings"
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
	cmd := exec.Command(argv[0], argv[1:]...)
	if err := cmd.Start(); err != nil {
		return false
	}
	go cmd.Wait() //nolint:errcheck // reap the process
	return true
}
