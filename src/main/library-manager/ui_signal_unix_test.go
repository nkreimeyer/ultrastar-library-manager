//go:build unix

package main

import (
	"bufio"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"os/signal"
	"strings"
	"syscall"
	"testing"
	"time"

	log "github.com/sirupsen/logrus"

	"github.com/nkreimeyer/library-manager/mediatools"
	"github.com/nkreimeyer/library-manager/webui"
)

// The browser opener starts a session of its own: a browser it starts is no member of the
// UI's process group, so Ctrl-C in the UI's terminal or its hangup does not quit it.
func TestBrowserCommandNewSession(t *testing.T) {
	for _, goos := range []string{"linux", "darwin", "freebsd"} {
		cmd := browserCommand(goos, "http://127.0.0.1:1/")
		if cmd.SysProcAttr == nil || !cmd.SysProcAttr.Setsid {
			t.Errorf("%s: %v in the UI's session", goos, cmd.Args)
		}
	}
}

// While the UI runs, the media tools start in a process group of their own: a Ctrl-C in the
// terminal reaches only the UI, which stops the job, and a running encode finishes instead
// of aborting with exit 255. Afterwards (the CLI) they stay in the program's group.
func TestUIDetachesMediaTools(t *testing.T) {
	t.Cleanup(func() { log.StandardLogger().ReplaceHooks(make(log.LevelHooks)) })
	detached := func() bool {
		cmd := mediatools.Detach(exec.Command("ffmpeg"))
		return cmd.SysProcAttr != nil && cmd.SysProcAttr.Setpgid
	}
	inUI := make(chan bool, 1)
	oldOpen, oldStop := openBrowser, uiStop
	t.Cleanup(func() { openBrowser, uiStop = oldOpen, oldStop })
	openBrowser = func(string) error { inUI <- detached(); return nil }
	uiStop = make(chan struct{})
	done := make(chan int, 1)
	go func() { done <- runWithArgs(t, "ui", "-keep-running") }()
	select {
	case got := <-inUI:
		if !got {
			t.Error("the UI starts the media tools in its own process group")
		}
	case <-time.After(10 * time.Second):
		t.Fatal("the UI did not start")
	}
	close(uiStop)
	<-done
	if detached() {
		t.Error("the tools are still detached after the UI ended")
	}
}

// secondSignalChild marks the test binary run by secondSignal; its value is the test.
const secondSignalChild = "LM_TEST_SECOND_SIGNAL_CHILD"

// secondSignal runs the UI in a child test binary (test is the calling test) whose job takes
// a minute to end, sends it sig twice (the second once the stop waits for the job) and
// returns how the child ended within wait (nil = still running).
func secondSignal(t *testing.T, test string, sig syscall.Signal, wait time.Duration) error {
	if os.Getenv(secondSignalChild) == test {
		closeServer = func(*webui.Server) { // a job that takes its time to end
			fmt.Println("closing")
			time.Sleep(time.Minute)
		}
		signalIgnored = func(os.Signal) bool { return false }
		os.Exit(runUI([]string{"-no-browser", "-keep-running"}, os.Stdout, false))
	}
	cmd := exec.Command(os.Args[0], "-test.run=^"+test+"$")
	cmd.Env = append(os.Environ(), secondSignalChild+"="+test)
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	cmd.Stdout, cmd.Stderr = w, w
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	w.Close()
	t.Cleanup(func() { cmd.Process.Kill() })
	lines := make(chan string, 100)
	go func() {
		sc := bufio.NewScanner(r)
		for sc.Scan() {
			lines <- sc.Text()
		}
		close(lines)
	}()
	waitFor := func(what string) {
		t.Helper()
		timeout := time.After(15 * time.Second)
		for {
			select {
			case l, ok := <-lines:
				if !ok {
					t.Fatalf("the child ended before %q", what)
				}
				if strings.Contains(l, what) {
					return
				}
			case <-timeout:
				t.Fatalf("no %q from the child", what)
			}
		}
	}
	waitFor("the UI runs at")
	if err := cmd.Process.Signal(sig); err != nil {
		t.Fatal(err)
	}
	waitFor("closing")
	if err := cmd.Process.Signal(sig); err != nil {
		t.Fatal(err)
	}
	exited := make(chan error, 1)
	go func() { exited <- cmd.Wait() }()
	select {
	case err := <-exited:
		if err == nil {
			err = errors.New("exit 0")
		}
		return err
	case <-time.After(wait):
		return nil
	}
}

// The first stop signal stops the job and waits for it; a second one quits at once instead
// of being swallowed while the job takes its time (an encode that runs to its end).
func TestUISecondSignalQuits(t *testing.T) {
	err := secondSignal(t, "TestUISecondSignalQuits", syscall.SIGTERM, 10*time.Second)
	if err == nil {
		t.Fatal("the second signal was swallowed while the job ends")
	}
	var ee *exec.ExitError
	if !errors.As(err, &ee) {
		t.Fatalf("exit: %v", err)
	}
	if ws, ok := ee.Sys().(syscall.WaitStatus); !ok || !ws.Signaled() || ws.Signal() != syscall.SIGTERM {
		t.Errorf("exit: %v, want the end by SIGTERM", err)
	}
}

// Closing a terminal sends two SIGHUPs within a millisecond: the second must not kill the
// UI while the stop the first started waits for the job.
func TestUISecondHangupWaits(t *testing.T) {
	if err := secondSignal(t, "TestUISecondHangupWaits", syscall.SIGHUP, 2*time.Second); err != nil {
		t.Errorf("the second SIGHUP ended the UI while the job ends: %v", err)
	}
}

// Closing the terminal (SIGHUP) stops the UI like Ctrl-C, so a running job ends cleanly
// instead of the process being killed mid-move.
func TestUIStopsOnSIGHUP(t *testing.T) {
	t.Cleanup(func() { log.StandardLogger().ReplaceHooks(make(log.LevelHooks)) })
	started := make(chan struct{}, 1)
	oldOpen := openBrowser
	t.Cleanup(func() { openBrowser = oldOpen })
	openBrowser = func(string) error { started <- struct{}{}; return nil }
	// The process was not started with SIGHUP ignored; an earlier test's signal.Ignore
	// would still show in signal.Ignored.
	oldIgnored := signalIgnored
	t.Cleanup(func() { signalIgnored = oldIgnored })
	signalIgnored = func(os.Signal) bool { return false }

	done := make(chan int, 1)
	go func() { done <- runWithArgs(t, "ui") }()
	select {
	case <-started:
	case <-time.After(10 * time.Second):
		t.Fatal("the UI did not start")
	}
	if err := syscall.Kill(syscall.Getpid(), syscall.SIGHUP); err != nil {
		t.Fatal(err)
	}
	select {
	case code := <-done:
		if code != exitOK {
			t.Errorf("exit code %d", code)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("the UI did not stop on SIGHUP")
	}
}

// Started with SIGHUP ignored (nohup), the UI keeps running when the terminal closes.
func TestUIKeepsIgnoredSIGHUP(t *testing.T) {
	t.Cleanup(func() { log.StandardLogger().ReplaceHooks(make(log.LevelHooks)) })
	signal.Ignore(syscall.SIGHUP)
	t.Cleanup(func() { signal.Reset(syscall.SIGHUP) })
	started := make(chan struct{}, 1)
	oldOpen, oldStop := openBrowser, uiStop
	t.Cleanup(func() { openBrowser, uiStop = oldOpen, oldStop })
	openBrowser = func(string) error { started <- struct{}{}; return nil }
	uiStop = make(chan struct{})

	done := make(chan int, 1)
	go func() { done <- runWithArgs(t, "ui") }()
	select {
	case <-started:
	case <-time.After(10 * time.Second):
		t.Fatal("the UI did not start")
	}
	if err := syscall.Kill(syscall.Getpid(), syscall.SIGHUP); err != nil {
		t.Fatal(err)
	}
	select {
	case code := <-done:
		t.Fatalf("the UI stopped on an ignored SIGHUP (exit %d)", code)
	case <-time.After(300 * time.Millisecond):
	}
	close(uiStop)
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("the UI did not stop")
	}
}
