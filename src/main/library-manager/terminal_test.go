package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"
	"time"

	log "github.com/sirupsen/logrus"
	logtest "github.com/sirupsen/logrus/hooks/test"
)

// A terminal launcher that fails at once (osascript without the permission to control
// Terminal, a broken x-terminal-emulator) is no opened window: the caller starts the UI
// itself, and the launcher's output is logged.
func TestStartLauncher(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("the launchers are shell scripts")
	}
	hook := logtest.NewGlobal()
	t.Cleanup(func() { log.StandardLogger().ReplaceHooks(make(log.LevelHooks)) })
	for _, tt := range []struct {
		name, script string
		want         bool
	}{
		{"fails", "echo 'Not authorized to send Apple events to Terminal.' >&2; exit 1", false},
		{"hands off", "exit 0", true},
		{"stays as the window", "sleep 5", true},
	} {
		t.Run(tt.name, func(t *testing.T) {
			hook.Reset()
			start := time.Now()
			if got := startLauncher(exec.Command("sh", "-c", tt.script), 300*time.Millisecond); got != tt.want {
				t.Errorf("startLauncher = %v, want %v", got, tt.want)
			}
			if d := time.Since(start); d > 3*time.Second {
				t.Errorf("took %v", d)
			}
			logged := hook.LastEntry() != nil && strings.Contains(hook.LastEntry().Message, "Not authorized")
			if logged != !tt.want {
				t.Errorf("logged the launcher's output: %v, entries %v", logged, hook.AllEntries())
			}
		})
	}
	if startLauncher(exec.Command(filepath.Join(t.TempDir(), "missing")), time.Second) {
		t.Error("a launcher that cannot start counts as a window")
	}
}

func TestNeedsTerminal(t *testing.T) {
	env := func(kv ...string) func(string) string {
		m := map[string]string{}
		for i := 0; i < len(kv); i += 2 {
			m[kv[i]] = kv[i+1]
		}
		return func(k string) string { return m[k] }
	}
	for _, c := range []struct {
		name               string
		goos               string
		stdinTTY, stdoutTT bool
		env                func(string) string
		want               bool
	}{
		{"double-click on a Linux desktop", "linux", false, false, env("DISPLAY", ":0"), true},
		{"Wayland", "linux", false, false, env("WAYLAND_DISPLAY", "wayland-0"), true},
		{"started from a terminal", "linux", true, true, env("DISPLAY", ":0"), false},
		{"stdin is a terminal, output redirected", "linux", true, false, env("DISPLAY", ":0"), false},
		{"output is a terminal, input is not", "linux", false, true, env("DISPLAY", ":0"), false},
		{"no desktop (ssh, cron, a service)", "linux", false, false, env(), false},
		{"started by this program in a terminal", "linux", false, false, env("DISPLAY", ":0", inTerminalEnv, "1"), false},
		{"macOS needs no DISPLAY", "darwin", false, false, env(), true},
		{"macOS from a terminal", "darwin", true, true, env(), false},
		{"Windows has its console already", "windows", false, false, env("DISPLAY", ":0"), false},
	} {
		if got := needsTerminal(c.goos, c.stdinTTY, c.stdoutTT, c.env); got != c.want {
			t.Errorf("%s: %v, want %v", c.name, got, c.want)
		}
	}
}

func TestTerminalCommand(t *testing.T) {
	only := func(names ...string) func(string) (string, error) {
		return func(n string) (string, error) {
			if slices.Contains(names, n) {
				return "/usr/bin/" + n, nil
			}
			return "", exec.ErrNotFound
		}
	}
	got, ok := terminalCommand("linux", "/opt/lm/library-manager", only("xterm", "gnome-terminal"))
	want := []string{"/usr/bin/gnome-terminal", "--", "env", inTerminalEnv + "=1", "/opt/lm/library-manager"}
	if !ok || !slices.Equal(got, want) {
		t.Errorf("gnome-terminal before xterm: %q %v, want %q", got, ok, want)
	}
	got, ok = terminalCommand("linux", "/opt/lm/library-manager", only("x-terminal-emulator", "gnome-terminal"))
	if !ok || got[0] != "/usr/bin/x-terminal-emulator" || got[1] != "-e" {
		t.Errorf("the system's choice comes first: %q", got)
	}
	got, ok = terminalCommand("linux", "/p", only("kitty"))
	if !ok || !slices.Equal(got, []string{"/usr/bin/kitty", "env", inTerminalEnv + "=1", "/p"}) {
		t.Errorf("kitty takes the command without a flag: %q", got)
	}
	if _, ok := terminalCommand("linux", "/p", only()); ok {
		t.Error("a command without any terminal installed")
	}

	// macOS: AppleScript, with the path quoted for the shell and for AppleScript.
	got, ok = terminalCommand("darwin", `/Users/me/My "Songs"/it's/library-manager`, only())
	if !ok || got[0] != "osascript" {
		t.Fatalf("darwin: %q %v", got, ok)
	}
	script := strings.Join(got, "\n")
	for _, w := range []string{`tell application "Terminal"`, "do script", inTerminalEnv + "=1", `\"Songs\"`, `it'\\''s`} {
		if !strings.Contains(script, w) {
			t.Errorf("darwin script lacks %q:\n%s", w, script)
		}
	}
}

func TestShellQuote(t *testing.T) {
	for in, want := range map[string]string{"a b": `'a b'`, `it's`: `'it'\''s'`, "": "''"} {
		if got := shellQuote(in); got != want {
			t.Errorf("shellQuote(%q) = %s, want %s", in, got, want)
		}
	}
}

func TestIsTerminal(t *testing.T) {
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	defer w.Close()
	if isTerminal(r) || isTerminal(w) {
		t.Error("a pipe is no terminal")
	}
	null, err := os.Open(os.DevNull)
	if err != nil {
		t.Fatal(err)
	}
	defer null.Close()
	if isTerminal(null) {
		t.Error("/dev/null is no terminal")
	}
	f, err := os.CreateTemp(t.TempDir(), "x")
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	if isTerminal(f) {
		t.Error("a file is no terminal")
	}
	closed, _ := os.Open(os.DevNull)
	closed.Close()
	if isTerminal(closed) {
		t.Error("a closed file is no terminal")
	}
}
