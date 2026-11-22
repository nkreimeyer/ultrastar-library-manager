package main

import (
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"runtime"
	"slices"
	"syscall"
	"time"

	log "github.com/sirupsen/logrus"

	"github.com/nkreimeyer/library-manager/mediatools"
	"github.com/nkreimeyer/library-manager/webui"
)

// browserCommand is the command that opens url in the default browser, in a session of its
// own (newSession).
func browserCommand(goos, url string) *exec.Cmd {
	var cmd *exec.Cmd
	switch goos {
	case "windows":
		cmd = exec.Command("rundll32", "url.dll,FileProtocolHandler", url)
	case "darwin":
		cmd = exec.Command("open", url)
	default:
		cmd = exec.Command("xdg-open", url)
	}
	newSession(cmd)
	return cmd
}

// openBrowser opens url in the default browser; a variable so tests do not open one.
var openBrowser = func(url string) error {
	cmd := browserCommand(runtime.GOOS, url)
	if err := cmd.Start(); err != nil {
		return err
	}
	go cmd.Wait() //nolint:errcheck // reap the process
	return nil
}

// listen opens the UI's listener; a variable so tests can make the server stop by itself.
var listen = net.Listen

// closeServer stops the UI server: it cancels a running job and waits for it. A variable so
// a test can make it wait.
var closeServer = (*webui.Server).Close

// stopSignals are the signals that stop the UI: SIGTERM always, so that kill stops it, and
// Ctrl-C and SIGHUP (the terminal window was closed) unless the program was started with
// them ignored: Notify would install a handler, and "nohup library-manager ui &" would stop
// at the logout.
func stopSignals() []os.Signal {
	var sigs []os.Signal
	for _, sig := range []os.Signal{os.Interrupt, syscall.SIGHUP} {
		if !signalIgnored(sig) {
			sigs = append(sigs, sig)
		}
	}
	return append(sigs, syscall.SIGTERM)
}

// signalIgnored is signal.Ignored. Tests replace it: once a test has ignored SIGHUP,
// signal.Ignored keeps reporting it even after signal.Reset.
var signalIgnored = signal.Ignored

// browserGrace is how long every browser page of the UI may be gone, with no job running,
// before the UI stops by itself: long enough for a reload or a moment without network. A
// variable so tests do not wait for it.
var browserGrace = 15 * time.Second

// uiStop is closed by tests to stop the UI; nil = wait for a signal.
var uiStop chan struct{}

// runUI serves the browser UI until Ctrl-C (or the console window is closed).
// noArgs: started without any argument, e.g. by a double-click.
func runUI(args []string, output io.Writer, noArgs bool) int {
	prog := filepath.Base(os.Args[0])
	fs := newSubcommand(prog+" ui", output)
	addr := fs.String("addr", "127.0.0.1:0", "Address to listen on (loopback only; port 0 = any free port)")
	noBrowser := fs.Bool("no-browser", false, "Do not open the browser, only print the address")
	keepRunning := fs.Bool("keep-running", false, "Do not stop when the last browser tab is closed (stop with Ctrl-C)")
	fs.Usage = func() {
		fmt.Fprintf(output, "Usage: %s ui [-addr 127.0.0.1:0] [-no-browser] [-keep-running] [-v level]\n\n"+
			"Starts the browser UI. Running %s without any argument does the same (started outside a terminal,\n"+
			"for example by a double-click, it opens a terminal window for itself). The UI stops by itself when\n"+
			"the last browser tab is closed, unless a job is running.\n\nFlags:\n", prog, prog)
		fs.PrintDefaults()
	}
	if code, ok := fs.parse(args, boolFlagHint("no-browser")); !ok {
		return code
	}
	if !fs.setLogLevel() {
		return exitUsage
	}

	if err := webui.CheckAddr(*addr); err != nil {
		return fs.usageError("invalid -addr %q: %v", *addr, err)
	}
	ln, err := listen("tcp", *addr) // fails e.g. if the port is in use: not a usage error
	if err != nil {
		log.Errorf("Cannot listen on %s: %v", *addr, err)
		return exitFailures
	}
	srv, err := webui.New(ln.Addr().String(), version)
	if err != nil {
		ln.Close()
		log.Error(err)
		return exitUsage
	}
	hs := &http.Server{Handler: srv, ReadHeaderTimeout: 10 * time.Second}
	// Set before the browser opens. Stopping lets a running job end cleanly (organize gets a
	// song parked under a temporary name out of it and journals the moves).
	ctx, stop := signal.NotifyContext(context.Background(), stopSignals()...)
	defer stop()
	// The media tools run in a process group of their own: a Ctrl-C in the terminal reaches
	// only this process, which stops the job, and a running encode finishes.
	defer mediatools.SetDetachChildren(true)()

	url := srv.StartURL()
	fmt.Fprintf(output, "library-manager %s: the UI runs at\n\n    %s\n\n", version, url)
	if noArgs {
		fmt.Fprintln(output, "Keep this window open while you use it. It stops by itself when you close the browser tab;")
		fmt.Fprintln(output, "you can also close this window (or press Ctrl-C) to stop.")
		fmt.Fprintf(output, "Command-line use: %s -h\n", os.Args[0])
	} else {
		fmt.Fprintln(output, "Press Ctrl-C to stop.")
	}
	if *keepRunning {
		fmt.Fprintln(output, "It keeps running when the browser tab is closed (-keep-running).")
	}
	if !*noBrowser {
		if err := openBrowser(url); err != nil {
			fmt.Fprintf(output, "Could not open the browser (%v); open the address above yourself.\n", err)
		}
	}

	served := make(chan error, 1)
	go func() { served <- hs.Serve(ln) }()
	var gone <-chan struct{} // nil blocks: no watch with -keep-running
	if !*keepRunning {
		gone = srv.WatchClients(browserGrace)
	}
	select {
	case <-ctx.Done():
		log.Infof("Stopping the UI: a running job ends first (press Ctrl-C again to quit at once)")
	case <-gone:
		log.Infof("The browser tab was closed: stopping the UI")
	case <-uiStop:
	case err := <-served:
		log.Errorf("The UI server stopped: %v", err)
		srv.Close()
		return exitFailures
	}
	// From here on Ctrl-C and SIGTERM have their default action again: a second one while
	// the job ends quits at once instead of being swallowed. A hangup stays caught: closing
	// a terminal sends two SIGHUPs (the shell's and the tty's) within a millisecond, and the
	// second must not kill the job the first one stops (a song parked mid-move).
	if slices.Contains(stopSignals(), os.Signal(syscall.SIGHUP)) {
		hup := make(chan os.Signal, 1)
		signal.Notify(hup, syscall.SIGHUP)
		defer signal.Stop(hup)
	}
	stop()
	closeServer(srv) // cancels a running job and waits for it
	// The event streams are closed already; what Shutdown still waits for is a request in
	// flight, and a connection a browser opened ahead of time that Go counts as idle only after
	// five seconds. A second is plenty for the former and keeps a closed tab from lingering.
	shutdown, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := hs.Shutdown(shutdown); err != nil {
		hs.Close()
	}
	return exitOK
}
