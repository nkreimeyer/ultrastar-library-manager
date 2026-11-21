package main

import (
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/nkreimeyer/library-manager/internal/testutil"
	log "github.com/sirupsen/logrus"
	logtest "github.com/sirupsen/logrus/hooks/test"
)

// runCaptured calls run() as if started with args and returns the exit code, what was
// written to the flag output (usage and usage errors) and the messages logged at info level
// or above.
func runCaptured(t *testing.T, args ...string) (code int, output string, logs []string) {
	t.Helper()
	oldArgs, oldFlags, oldLevel := os.Args, flag.CommandLine, log.GetLevel()
	t.Cleanup(func() { os.Args, flag.CommandLine = oldArgs, oldFlags; log.SetLevel(oldLevel) })
	os.Args = append([]string{"library-manager"}, args...)
	flag.CommandLine = flag.NewFlagSet("library-manager", flag.ContinueOnError)
	var buf strings.Builder
	flag.CommandLine.SetOutput(&buf)
	hook := logtest.NewGlobal()
	defer log.StandardLogger().ReplaceHooks(make(log.LevelHooks))
	code = run()
	for _, e := range hook.AllEntries() {
		if e.Level <= log.InfoLevel {
			logs = append(logs, e.Message)
		}
	}
	return code, buf.String(), logs
}

// cliFixture is a library with two songs, a source with one and a song dir for the main
// command.
func cliFixture(t *testing.T) (base, lib, src, rlib string) {
	t.Helper()
	t.Setenv("PATH", t.TempDir())                   // no ffprobe/ffmpeg: placeholder media are enough
	base, err := filepath.EvalSymlinks(t.TempDir()) // the log shows resolved paths
	if err != nil {
		t.Fatal(err)
	}
	lib, src, rlib = filepath.Join(base, "lib"), filepath.Join(base, "src"), filepath.Join(base, "rlib")
	song := func(dir, artist, title, lyric string) {
		txt := "#ARTIST:" + artist + "\n#TITLE:" + title + "\n#MP3:a.mp3\n#BPM:300\n"
		for i := range 10 {
			txt += fmt.Sprintf(": %d 2 0 %s\n", i*4, lyric)
		}
		testutil.WriteFiles(t, dir, map[string]string{"song.txt": txt + "E\n", "a.mp3": "x"})
	}
	song(filepath.Join(lib, "A", "ABBA - SOS"), "ABBA", "SOS", "sos")
	song(filepath.Join(lib, "C", "Cher - Believe"), "Cher", "Believe", "bel")
	song(filepath.Join(src, "ABBA - Waterloo"), "ABBA", "Waterloo", "wat")
	song(filepath.Join(rlib, "A", "Bad"), "A", "Bad", "f�r")
	song(filepath.Join(rlib, "A", "One"), "A", "One", "für")
	return
}

// A leftover argument is an error of every command: the message names the arguments, gives
// the command's hint, and the usage follows.
func TestLeftoverArgumentsPerCommand(t *testing.T) {
	base, lib, src, rlib := cliFixture(t)
	out := filepath.Join(base, "out")
	for _, tt := range []struct {
		args       []string
		hint, head string
	}{
		{[]string{"-s", rlib, "-t", "false"}, "Bool flags must be written as -t=false, not -t false (parsing stops at the first non-flag argument).",
			"Usage: library-manager -s <song-dir> [flags]\n"},
		{[]string{"organize", "-s", src, "-l", lib, "-o", out, "x"}, "Bool flags must be written as -priority=false, not -priority false (parsing stops at the first non-flag argument).",
			"Usage: library-manager organize -s "},
		{[]string{"dedupe", "-l", lib, "-o", out, "x"}, "Bool flags must be written as -priority=false, not -priority false (parsing stops at the first non-flag argument).",
			"Usage: library-manager dedupe -l <library> "},
		{[]string{"add", "-s", src, "-l", lib, "-o", out, "x"}, "Bool flags must be written as -priority=false, not -priority false (parsing stops at the first non-flag argument).",
			"Usage: library-manager add -s "},
		{[]string{"remove", "-l", lib, "-o", out, "-artist", "Elton", "x"}, "Quote names with spaces (-artist \"Elton John\"); bool flags must be written as -n=false.",
			"Usage: library-manager remove -l <library> "},
	} {
		code, output, logs := runCaptured(t, tt.args...)
		want := "Error: unexpected arguments [\"x\"].\n" + tt.hint + "\n\n" + tt.head
		if tt.args[0] == "-s" {
			want = "Error: unexpected arguments [\"false\"].\n" + tt.hint + "\n\n" + tt.head
		}
		if code != exitUsage || !strings.HasPrefix(output, want) || len(logs) != 0 {
			t.Errorf("%q: exit %d, logs %q, output:\n%s\nwant prefix:\n%s", tt.args, code, logs, output, want)
		}
	}
	if _, err := os.Stat(out); !os.IsNotExist(err) {
		t.Errorf("a usage error created the out dir: %v", err)
	}
}

// -v sets the log level of every command; an unknown level is a usage error with one
// line and no usage.
func TestLogLevelPerCommand(t *testing.T) {
	base, lib, src, rlib := cliFixture(t)
	out := filepath.Join(base, "out")
	for _, args := range [][]string{
		{"-s", rlib, "-o", out, "-n"},
		{"organize", "-s", src, "-l", lib, "-o", out, "-n"},
		{"dedupe", "-l", lib, "-o", out, "-n"},
		{"add", "-s", src, "-l", lib, "-o", out, "-n"},
		{"remove", "-l", lib, "-o", out, "-n", "-artist", "Cher"},
		{"organize", "-undo", "-o", out, "-n"},
	} {
		code, output, _ := runCaptured(t, append(args, "-v", "loud")...)
		if want := "Error: invalid log level \"loud\" (use debug, info, warn or error).\n"; code != exitUsage || output != want {
			t.Errorf("%q -v loud: exit %d, output %q, want %q", args, code, output, want)
		}
		log.SetLevel(log.InfoLevel)
		if code, output, _ := runCaptured(t, append(args, "-v", "warn")...); code != exitOK || output != "" || log.GetLevel() != log.WarnLevel {
			t.Errorf("%q -v warn: exit %d, level %s, output %q", args, code, log.GetLevel(), output)
		}
	}
}

func containsAll(t *testing.T, what string, logs, want []string) {
	t.Helper()
	for _, w := range want {
		found := false
		for _, l := range logs {
			found = found || l == w
		}
		if !found {
			t.Errorf("%s: no log line %q in\n%s", what, w, strings.Join(logs, "\n"))
		}
	}
}

// remove -undo with only -o: a dry run plans the moves back, a real run makes them; both
// end with the summary line.
func TestRunRemoveUndoWithOutDir(t *testing.T) {
	base, lib, _, _ := cliFixture(t)
	out := filepath.Join(base, "out")
	code, _, logs := runCaptured(t, "remove", "-l", lib, "-o", out, "-artist", "Cher")
	if code != exitOK {
		t.Fatalf("remove = %d\n%s", code, strings.Join(logs, "\n"))
	}
	containsAll(t, "remove", logs, []string{"Output dir: " + out, "Library: " + lib + "; 1 pattern(s)", "Done: 1 moved, 0 failed operations"})
	moved := filepath.Join(out, "removed", "C", "Cher - Believe", "song.txt")
	if _, err := os.Stat(moved); err != nil {
		t.Fatal(err)
	}
	code, _, logs = runCaptured(t, "remove", "-undo", "-o", out, "-n")
	if code != exitOK {
		t.Fatalf("remove -undo -n = %d", code)
	}
	containsAll(t, "undo dry run", logs, []string{"Dry run: nothing will be moved (the plan report is written to the output dir)",
		"Output dir: " + out, "Undo: 1 move(s) planned, 0 failed", "Done (planned): 1 moved, 0 failed operations"})
	if _, err := os.Stat(moved); err != nil {
		t.Errorf("the dry run moved the song back: %v", err)
	}
	code, _, logs = runCaptured(t, "remove", "-undo", "-o", out)
	if code != exitOK {
		t.Fatalf("remove -undo = %d", code)
	}
	containsAll(t, "undo", logs, []string{"Undo: 1 move(s) moved back, 0 failed", "Done: 1 moved, 0 failed operations"})
	if _, err := os.Stat(filepath.Join(lib, "C", "Cher - Believe", "song.txt")); err != nil {
		t.Errorf("not moved back: %v", err)
	}
}

// The start and summary lines of each command, in a dry run and when the output folder
// cannot be created (exit 1, nothing run).
func TestCommandSummaryLines(t *testing.T) {
	base, lib, src, _ := cliFixture(t)
	out := filepath.Join(base, "out")
	file := filepath.Join(base, "file")
	if err := os.WriteFile(file, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	for _, tt := range []struct {
		args []string
		want []string
	}{
		{[]string{"organize", "-n", "-s", src, "-l", lib, "-o", out}, []string{
			"Dry run: nothing will be moved (the plan report is written to the output dir)", "Output dir: " + out,
			"Library: " + lib, "Source (tier 1): " + src, "Done (planned): 1 moved, 0 failed operations"}},
	} {
		code, _, logs := runCaptured(t, tt.args...)
		if code != exitOK {
			t.Errorf("%q = %d", tt.args, code)
		}
		containsAll(t, tt.args[0], logs, tt.want)
	}
	bad := filepath.Join(file, "out")
	for _, args := range [][]string{
		{"organize", "-s", src, "-l", lib, "-o", bad},
		{"remove", "-l", lib, "-o", bad, "-artist", "Cher"},
	} {
		code, _, logs := runCaptured(t, args...)
		want := "creating output dir: mkdir " + file + ": not a directory"
		if code != exitFailures || len(logs) == 0 || logs[len(logs)-1] != want {
			t.Errorf("%q: exit %d, logs %q, want the last %q", args, code, logs, want)
		}
	}
	// An undo whose journal cannot be read is a failed run with a summary.
	code, _, logs := runCaptured(t, "organize", "-undo", "-o", bad)
	if code != exitFailures || len(logs) == 0 || logs[len(logs)-1] != "Done: 0 moved, 0 failed operations" || !testutil.ContainsPrefix(logs, "organize failed: reading the journal: ") {
		t.Errorf("undo with an unreadable journal: exit %d, logs %q", code, logs)
	}
}

// -undo without -o or -l is a usage error of every journaled command: the message, then
// the command's usage; nothing is logged and no folder is created.
func TestUndoNeedsFolderPerCommand(t *testing.T) {
	cliFixture(t)
	for _, cmd := range []string{"organize", "dedupe", "add", "remove"} {
		code, output, logs := runCaptured(t, cmd, "-undo")
		want := "Error: -undo needs the output folder: give -o <out> or -l <library>.\n\nUsage: library-manager " + cmd + " "
		if code != exitUsage || !strings.HasPrefix(output, want) || len(logs) != 0 {
			t.Errorf("%s -undo: exit %d, logs %q, output:\n%s\nwant prefix:\n%s", cmd, code, logs, output, want)
		}
	}
}

// A failed run names its command: remove logs "remove failed", the organize family
// "organize failed".
func TestFailedRunNamesCommand(t *testing.T) {
	base, _, _, _ := cliFixture(t)
	file := filepath.Join(base, "file")
	if err := os.WriteFile(file, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	bad := filepath.Join(file, "out")
	for cmd, want := range map[string]string{"organize": "organize", "dedupe": "organize", "add": "organize", "remove": "remove"} {
		code, _, logs := runCaptured(t, cmd, "-undo", "-o", bad)
		if code != exitFailures || !testutil.ContainsPrefix(logs, want+" failed: reading the journal: ") {
			t.Errorf("%s -undo with an unreadable journal: exit %d, logs %q, want %q failed", cmd, code, logs, want)
		}
	}
}
