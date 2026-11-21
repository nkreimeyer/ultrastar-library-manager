package main

import (
	"io"
	"net"
	"net/http"
	"net/http/cookiejar"
	"strings"
	"testing"
	"time"

	log "github.com/sirupsen/logrus"
)

// No test opens a browser (run() without arguments starts the UI).
func init() { openBrowser = func(string) error { return nil } }

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
