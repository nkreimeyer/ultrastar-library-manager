package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"os/signal"
	"runtime"
	"syscall"
	"time"

	log "github.com/sirupsen/logrus"

	"github.com/nkreimeyer/library-manager/webui"
)

// openBrowser opens url in the default browser; a variable so tests do not open one.
var openBrowser = func(url string) error {
	var cmd *exec.Cmd
	switch runtime.GOOS {
	case "windows":
		cmd = exec.Command("rundll32", "url.dll,FileProtocolHandler", url)
	case "darwin":
		cmd = exec.Command("open", url)
	default:
		cmd = exec.Command("xdg-open", url)
	}
	if err := cmd.Start(); err != nil {
		return err
	}
	go cmd.Wait() //nolint:errcheck // reap the process
	return nil
}

// uiStop is closed by tests to stop the UI; nil = wait for a signal.
var uiStop chan struct{}

// runUI serves the browser UI until Ctrl-C (or the console window is closed).
// noArgs: started without any argument, e.g. by a double-click.
func runUI(args []string, output io.Writer, noArgs bool) int {
	fs := flag.NewFlagSet(os.Args[0]+" ui", flag.ContinueOnError)
	fs.SetOutput(output)
	addr := fs.String("addr", "127.0.0.1:0", "Address to listen on (loopback only; port 0 = any free port)")
	noBrowser := fs.Bool("no-browser", false, "Do not open the browser, only print the address")
	logLevel := fs.String("v", "info", "Log level (debug, info, warn, error)")
	fs.Usage = func() {
		fmt.Fprintf(fs.Output(), "Usage: %s ui [-addr 127.0.0.1:0] [-no-browser] [-v level]\n\n"+
			"Starts the browser UI. Running %s without any argument does the same.\n\n", os.Args[0], os.Args[0])
		fs.PrintDefaults()
	}
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return exitOK
		}
		return exitUsage
	}
	if fs.NArg() > 0 {
		fmt.Fprintf(output, "unexpected arguments: %q\n", fs.Args())
		return exitUsage
	}
	level, err := log.ParseLevel(*logLevel)
	if err != nil {
		fmt.Fprintf(output, "invalid -v %q: %v\n", *logLevel, err)
		return exitUsage
	}
	log.SetLevel(level)

	ln, err := net.Listen("tcp", *addr)
	if err != nil {
		log.Errorf("Cannot listen on %s: %v", *addr, err)
		return exitUsage
	}
	srv, err := webui.New(ln.Addr().String(), version)
	if err != nil {
		ln.Close()
		log.Error(err)
		return exitUsage
	}
	hs := &http.Server{Handler: srv, ReadHeaderTimeout: 10 * time.Second}

	url := srv.StartURL()
	fmt.Fprintf(output, "library-manager %s: the UI runs at\n\n    %s\n\n", version, url)
	if noArgs {
		fmt.Fprintln(output, "Keep this window open while you use it; close it (or press Ctrl-C) to stop.")
		fmt.Fprintf(output, "Command-line use: %s -h\n", os.Args[0])
	} else {
		fmt.Fprintln(output, "Press Ctrl-C to stop.")
	}
	if !*noBrowser {
		if err := openBrowser(url); err != nil {
			fmt.Fprintf(output, "Could not open the browser (%v); open the address above yourself.\n", err)
		}
	}

	// SIGHUP: the terminal window was closed. Stopping lets a running job end cleanly
	// (organize gets a song parked under a temporary name out of it and journals the moves).
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM, syscall.SIGHUP)
	defer stop()
	served := make(chan error, 1)
	go func() { served <- hs.Serve(ln) }()
	select {
	case <-ctx.Done():
		log.Infof("Stopping the UI")
	case <-uiStop:
	case err := <-served:
		log.Errorf("The UI server stopped: %v", err)
		srv.Close()
		return exitFailures
	}
	srv.Close() // cancels a running job and waits for it
	shutdown, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := hs.Shutdown(shutdown); err != nil {
		hs.Close()
	}
	return exitOK
}
