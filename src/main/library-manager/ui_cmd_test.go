package main

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/cookiejar"
	"os"
	"regexp"
	"slices"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	log "github.com/sirupsen/logrus"
)

// No test opens a browser (run() without arguments starts the UI).
func init() {
	openBrowser = func(string) error { return nil }
	launchTerminal = func() bool { return false } // nor a terminal window
	browserGrace = 300 * time.Millisecond
}

// SIGTERM always stops the UI (kill), even if the program was started with it ignored;
// Ctrl-C and SIGHUP only if they were not ignored (nohup).
func TestStopSignals(t *testing.T) {
	old := signalIgnored
	t.Cleanup(func() { signalIgnored = old })
	signalIgnored = func(os.Signal) bool { return true }
	if got := stopSignals(); !slices.Equal(got, []os.Signal{syscall.SIGTERM}) {
		t.Errorf("all ignored: %v, want only SIGTERM", got)
	}
	signalIgnored = func(os.Signal) bool { return false }
	if got := stopSignals(); !slices.Equal(got, []os.Signal{os.Interrupt, syscall.SIGHUP, syscall.SIGTERM}) {
		t.Errorf("none ignored: %v", got)
	}
}

func TestUIUsage(t *testing.T) {
	t.Cleanup(func() { log.StandardLogger().ReplaceHooks(make(log.LevelHooks)) })
	for _, args := range [][]string{
		{"ui", "-bogus"},
		{"ui", "extra"},
		{"ui", "-v", "loud"},
		{"ui", "-addr", "0.0.0.0:0", "-no-browser"},
		{"ui", "-addr", "127.0.0.1:99999", "-no-browser"},
	} {
		if got := runWithArgs(t, args...); got != exitUsage {
			t.Errorf("%q = %d, want %d", args, got, exitUsage)
		}
	}
	// A port in use is no usage error.
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	if got := runWithArgs(t, "ui", "-addr", ln.Addr().String(), "-no-browser"); got != exitFailures {
		t.Errorf("port in use = %d, want %d", got, exitFailures)
	}
	if got := runWithArgs(t, "ui", "-h"); got != exitOK {
		t.Errorf("-h = %d", got)
	}
}

// Without arguments the UI starts and the browser opens its start URL, which signs in.
func TestUINoArgsOpensBrowser(t *testing.T) {
	t.Cleanup(func() { log.StandardLogger().ReplaceHooks(make(log.LevelHooks)) })
	opened := make(chan string, 1)
	oldOpen, oldStop := openBrowser, uiStop
	t.Cleanup(func() { openBrowser, uiStop = oldOpen, oldStop })
	openBrowser = func(url string) error { opened <- url; return nil }
	uiStop = make(chan struct{})

	done := make(chan int, 1)
	go func() { done <- runWithArgs(t) }()
	var url string
	select {
	case url = <-opened:
	case <-time.After(10 * time.Second):
		t.Fatal("the browser was not opened")
	}
	if !strings.HasPrefix(url, "http://127.0.0.1:") || !strings.Contains(url, "/?token=") {
		t.Errorf("url = %q", url)
	}
	jar, _ := cookiejar.New(nil)
	c := &http.Client{Jar: jar, Timeout: 10 * time.Second}
	resp, err := c.Get(url)
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK || !strings.Contains(string(body), "<html") {
		t.Errorf("start page: %d", resp.StatusCode)
	}
	close(uiStop)
	select {
	case code := <-done:
		if code != exitOK {
			t.Errorf("exit code %d", code)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("the UI did not stop")
	}
}

// The UI stops by itself when its last browser page is closed, and not with -keep-running.
func TestUIStopsWhenTheBrowserCloses(t *testing.T) {
	for _, keep := range []bool{false, true} {
		t.Run(fmt.Sprintf("keep-running=%v", keep), func(t *testing.T) {
			t.Cleanup(func() { log.StandardLogger().ReplaceHooks(make(log.LevelHooks)) })
			opened := make(chan string, 1)
			oldOpen, oldStop := openBrowser, uiStop
			t.Cleanup(func() { openBrowser, uiStop = oldOpen, oldStop })
			openBrowser = func(url string) error { opened <- url; return nil }
			uiStop = make(chan struct{})
			args := []string{"ui"}
			if keep {
				args = append(args, "-keep-running")
			}
			done := make(chan int, 1)
			go func() { done <- runWithArgs(t, args...) }()
			url := <-opened
			jar, _ := cookiejar.New(nil)
			c := &http.Client{Jar: jar, Timeout: 10 * time.Second}
			resp, err := c.Get(url)
			if err != nil {
				t.Fatal(err)
			}
			resp.Body.Close()
			// The page keeps its event stream open as long as the tab exists.
			ctx, cancel := context.WithCancel(context.Background())
			req, _ := http.NewRequestWithContext(ctx, "GET", strings.SplitN(url, "/?", 2)[0]+"/api/job/events", nil)
			stream, err := c.Do(req)
			if err != nil {
				t.Fatal(err)
			}
			select {
			case <-done:
				t.Fatal("stopped while the page is open")
			case <-time.After(2 * browserGrace):
			}
			cancel() // the tab is closed
			stream.Body.Close()
			select {
			case code := <-done:
				if keep {
					t.Fatalf("stopped although -keep-running (exit %d)", code)
				}
				if code != exitOK {
					t.Errorf("exit code %d", code)
				}
			case <-time.After(4 * time.Second):
				if !keep {
					t.Fatal("still running after the page was closed")
				}
				close(uiStop)
				if code := <-done; code != exitOK {
					t.Errorf("exit code %d", code)
				}
			}
		})
	}
}

// syncBuffer is a writer the test can read while runUI still writes to it.
type syncBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *syncBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *syncBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

// A browser that cannot be opened is no failure: the UI says so, prints the address and
// keeps serving.
func TestUIBrowserOpenFails(t *testing.T) {
	t.Cleanup(func() { log.StandardLogger().ReplaceHooks(make(log.LevelHooks)) })
	oldOpen, oldStop := openBrowser, uiStop
	t.Cleanup(func() { openBrowser, uiStop = oldOpen, oldStop })
	openBrowser = func(string) error { return errors.New("no browser here") }
	uiStop = make(chan struct{})

	out := &syncBuffer{}
	done := make(chan int, 1)
	go func() { done <- runUI([]string{"-keep-running"}, out, false) }()
	deadline := time.Now().Add(10 * time.Second)
	for !strings.Contains(out.String(), "Could not open the browser (no browser here)") {
		if time.Now().After(deadline) {
			t.Fatalf("no message about the browser; output:\n%s", out.String())
		}
		time.Sleep(10 * time.Millisecond)
	}
	m := regexp.MustCompile(`http://127\.0\.0\.1:\d+/\?token=\w+`).FindString(out.String())
	if m == "" {
		t.Fatalf("the address is not printed:\n%s", out.String())
	}
	jar, _ := cookiejar.New(nil)
	c := &http.Client{Jar: jar, Timeout: 10 * time.Second}
	resp, err := c.Get(m)
	if err != nil {
		t.Fatalf("the UI does not run: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Errorf("start page: %d", resp.StatusCode)
	}
	close(uiStop)
	select {
	case code := <-done:
		if code != exitOK {
			t.Errorf("exit code %d, want %d", code, exitOK)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("the UI did not stop")
	}
}

// A connection that starts a request but never finishes its headers is closed by the
// server after the 10 s header timeout, so stalled clients do not pile up.
func TestUIClosesStalledHeaders(t *testing.T) {
	if testing.Short() {
		t.Skip("waits for the 10 s header timeout")
	}
	t.Cleanup(func() { log.StandardLogger().ReplaceHooks(make(log.LevelHooks)) })
	opened := make(chan string, 1)
	oldOpen, oldStop := openBrowser, uiStop
	t.Cleanup(func() { openBrowser, uiStop = oldOpen, oldStop })
	openBrowser = func(url string) error { opened <- url; return nil }
	uiStop = make(chan struct{})
	done := make(chan int, 1)
	go func() { done <- runWithArgs(t, "ui", "-keep-running") }()
	url := <-opened
	addr := strings.TrimPrefix(strings.SplitN(url, "/?", 2)[0], "http://")
	conn, err := net.Dial("tcp", addr)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	start := time.Now()
	if _, err := conn.Write([]byte("GET / HTTP/1.1\r\nHost: " + addr + "\r\n")); err != nil { // no blank line: the headers never end
		t.Fatal(err)
	}
	conn.SetReadDeadline(time.Now().Add(15 * time.Second))
	_, err = io.Copy(io.Discard, conn) // returns when the server closes the connection
	if ne, ok := err.(net.Error); ok && ne.Timeout() {
		t.Fatal("the server kept a connection with unfinished headers open for 15 s")
	}
	if d := time.Since(start); d < 5*time.Second {
		t.Errorf("closed after %v, before the header timeout", d)
	}
	close(uiStop)
	if code := <-done; code != exitOK {
		t.Errorf("exit code %d", code)
	}
}

// failingListener accepts nothing: Serve returns its error at once.
type failingListener struct{ net.Listener }

func (l failingListener) Accept() (net.Conn, error) { return nil, errors.New("accept broke") }

// A server that stops by itself is exit 1, and the UI does not wait for a signal.
func TestUIServerStopsUnexpectedly(t *testing.T) {
	t.Cleanup(func() { log.StandardLogger().ReplaceHooks(make(log.LevelHooks)) })
	oldListen, oldStop := listen, uiStop
	t.Cleanup(func() { listen, uiStop = oldListen, oldStop })
	uiStop = nil // only the server can end this run
	listen = func(network, addr string) (net.Listener, error) {
		ln, err := net.Listen(network, addr)
		return failingListener{ln}, err
	}
	out := &syncBuffer{}
	done := make(chan int, 1)
	go func() { done <- runUI([]string{"-no-browser", "-keep-running"}, out, false) }()
	select {
	case code := <-done:
		if code != exitFailures {
			t.Errorf("exit code %d, want %d", code, exitFailures)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("the UI kept running after its server stopped")
	}
}

// A bad -addr is refused before anything listens.
func TestUIBadAddrNeverListens(t *testing.T) {
	t.Cleanup(func() { log.StandardLogger().ReplaceHooks(make(log.LevelHooks)) })
	oldListen := listen
	t.Cleanup(func() { listen = oldListen })
	listen = func(network, addr string) (net.Listener, error) {
		t.Errorf("listened on %q", addr)
		return net.Listen(network, "127.0.0.1:0")
	}
	for _, addr := range []string{"0.0.0.0:0", "localhost:0", "127.0.0.1", "127.0.0.1:-1", "127.0.0.1:http", "[::]:0", "8080", ""} {
		out := &syncBuffer{}
		if got := runUI([]string{"-addr", addr, "-no-browser"}, out, false); got != exitUsage {
			t.Errorf("-addr %q = %d, want %d", addr, got, exitUsage)
		}
		if !strings.Contains(out.String(), "invalid -addr") {
			t.Errorf("-addr %q: output %q", addr, out.String())
		}
	}
}
