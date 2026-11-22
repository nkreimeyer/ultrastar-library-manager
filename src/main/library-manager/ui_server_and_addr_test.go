package main

import (
	"errors"
	"net"
	"path/filepath"
	"strings"
	"testing"
	"time"

	log "github.com/sirupsen/logrus"
)

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

func TestTierList(t *testing.T) {
	var tl tierList
	if tl.String() != "" {
		t.Errorf("empty = %q", tl.String())
	}
	sep := string(filepath.ListSeparator)
	for _, v := range []string{"a" + sep + "b", "c"} {
		if err := tl.Set(v); err != nil {
			t.Fatal(err)
		}
	}
	if len(tl) != 2 || len(tl[0]) != 2 || tl[1][0] != "c" {
		t.Errorf("tiers %v", [][]string(tl))
	}
	if got, want := tl.String(), "a"+sep+"b c"; got != want {
		t.Errorf("String = %q, want %q", got, want)
	}
}
