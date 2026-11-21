//go:build unix

package main

import (
	"os/signal"
	"syscall"
	"testing"
	"time"

	log "github.com/sirupsen/logrus"
)

// Closing the terminal (SIGHUP) stops the UI like Ctrl-C, so a running job ends cleanly
// instead of the process being killed mid-move.
func TestUIStopsOnSIGHUP(t *testing.T) {
	t.Cleanup(func() { log.StandardLogger().ReplaceHooks(make(log.LevelHooks)) })
	started := make(chan struct{}, 1)
	oldOpen := openBrowser
	t.Cleanup(func() { openBrowser = oldOpen })
	openBrowser = func(string) error { started <- struct{}{}; return nil }

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
