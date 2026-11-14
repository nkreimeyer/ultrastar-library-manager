package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/nkreimeyer/library-manager/check"
)

// runWithArgs calls run() with a fresh flag set, as if started with args.
func runWithArgs(t *testing.T, args ...string) int {
	t.Helper()
	oldArgs, oldFlags := os.Args, flag.CommandLine
	t.Cleanup(func() { os.Args, flag.CommandLine = oldArgs, oldFlags })
	os.Args = append([]string{"library-manager"}, args...)
	flag.CommandLine = flag.NewFlagSet("library-manager", flag.ContinueOnError)
	flag.CommandLine.SetOutput(io.Discard)
	return run()
}

func TestRunUsage(t *testing.T) {
	t.Setenv("PATH", t.TempDir()) // no ffmpeg: check skips the media checks
	lib, out := t.TempDir(), t.TempDir()
	tests := []struct {
		name string
		args []string
		want int
	}{
		{"no song dir", []string{"-k"}, exitUsage},
		{"leftover argument", []string{"-s", lib, "-t", "false"}, exitUsage},
		{"-deep without -k", []string{"-s", lib, "-o", out, "-deep"}, exitUsage},
		{"-move-broken without -k", []string{"-s", lib, "-o", out, "-move-broken"}, exitUsage},
		{"check with its options", []string{"-s", lib, "-o", out, "-k", "-deep", "-move-broken", "-t=false", "-c=false"}, exitOK},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := runWithArgs(t, tt.args...); got != tt.want {
				t.Errorf("run(%q) = %d, want %d", tt.args, got, tt.want)
			}
		})
	}
	if _, err := os.Stat(filepath.Join(out, "check_report.json")); err != nil {
		t.Errorf("check did not run: %v", err)
	}
}

func TestModuleOrder(t *testing.T) {
	var names []string
	for _, m := range moduleList(true, true, true, false, true, check.Options{}) {
		names = append(names, m.name)
		if m.enabled != (m.name != "fixvideos") {
			t.Errorf("%s enabled = %v", m.name, m.enabled)
		}
	}
	if want := []string{"fixtxt", "cleanup", "check", "fixvideos", "fixaudio"}; !slices.Equal(names, want) {
		t.Errorf("module order = %v, want %v", names, want)
	}
}

// TestRunFillsNameTagsBeforeCheck: fixtxt fills a missing #ARTIST from the folder name
// before check runs. In a dry run check reads the txt fixtxt would write, so it finds no
// missing tag and -move-broken plans no move, exactly like the real run.
func TestRunFillsNameTagsBeforeCheck(t *testing.T) {
	t.Setenv("PATH", t.TempDir()) // no ffmpeg: check skips the media checks
	lib := t.TempDir()
	dir := filepath.Join(lib, "Electric Callboy - Hypa Hypa")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	txt := filepath.Join(dir, "song.txt")
	notes := "#BPM:300\n"
	for i := range 10 {
		notes += fmt.Sprintf(": %d 2 0 la\n", i*4)
	}
	for name, content := range map[string]string{txt: "#TITLE:Hypa Hypa\n#MP3:song.mp3\n" + notes + "E\n", filepath.Join(dir, "song.mp3"): "x"} {
		if err := os.WriteFile(name, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	missingTag := func(t *testing.T, out string) string {
		t.Helper()
		data, err := os.ReadFile(filepath.Join(out, "check_report.json"))
		if err != nil {
			t.Fatal(err)
		}
		var rep struct {
			Songs []struct {
				Moved    bool
				Findings []struct{ Severity, Code string }
			}
		}
		if err := json.Unmarshal(data, &rep); err != nil {
			t.Fatal(err)
		}
		for _, s := range rep.Songs {
			if s.Moved {
				t.Errorf("song (planned to be) moved: %s", data)
			}
			for _, f := range s.Findings {
				if f.Code == "missing-tag" {
					return f.Severity
				}
			}
		}
		return ""
	}

	dryOut := t.TempDir()
	if got := runWithArgs(t, "-s", lib, "-o", dryOut, "-n", "-k", "-move-broken"); got != exitOK {
		t.Fatalf("dry run exit code %d", got)
	}
	if got := missingTag(t, dryOut); got != "" {
		t.Errorf("dry run: missing-tag finding %q, want none (fixtxt would fill the tag)", got)
	}

	out := t.TempDir()
	if got := runWithArgs(t, "-s", lib, "-o", out, "-k", "-move-broken"); got != exitOK {
		t.Fatalf("real run exit code %d", got)
	}
	if got := missingTag(t, out); got != "" {
		t.Errorf("real run: missing-tag finding %q after fixtxt", got)
	}
	data, err := os.ReadFile(txt)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(string(data), "#ARTIST:Electric Callboy\n#TITLE:Hypa Hypa\n") {
		t.Errorf("txt = %q", data)
	}
}

func TestRunOrganizeUsage(t *testing.T) {
	t.Setenv("PATH", t.TempDir()) // no ffprobe: placeholder media are enough
	base := t.TempDir()
	src, src2, lib, out := filepath.Join(base, "src"), filepath.Join(base, "src2"), filepath.Join(base, "lib"), filepath.Join(base, "out")
	song := filepath.Join(src, "a")
	if err := os.MkdirAll(song, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(src2, 0o755); err != nil {
		t.Fatal(err)
	}
	notes := "#ARTIST:ABBA\n#TITLE:SOS\n#MP3:a.mp3\n#BPM:300\n"
	for i := range 10 {
		notes += fmt.Sprintf(": %d 2 0 la\n", i*4)
	}
	for name, content := range map[string]string{"song.txt": notes + "E\n", "a.mp3": "x"} {
		if err := os.WriteFile(filepath.Join(song, name), []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	list := string(filepath.ListSeparator)
	tests := []struct {
		name string
		args []string
		want int
	}{
		{"no source", []string{"organize", "-l", lib}, exitUsage},
		{"no library", []string{"organize", "-s", src}, exitUsage},
		{"leftover argument", []string{"organize", "-s", src, "-l", lib, "-priority", "false"}, exitUsage},
		{"unknown flag", []string{"organize", "-s", src, "-l", lib, "-k"}, exitUsage},
		{"empty tier", []string{"organize", "-s", "", "-l", lib}, exitUsage},
		{"empty element in a tier list", []string{"organize", "-s", src + list + list + src2, "-l", lib}, exitUsage},
		{"missing source", []string{"organize", "-s", filepath.Join(base, "missing"), "-l", lib}, exitUsage},
		{"source given twice", []string{"organize", "-s", src, "-s", src, "-l", lib}, exitUsage},
		{"out dir inside a source", []string{"organize", "-s", src, "-l", lib, "-o", filepath.Join(src, "out")}, exitUsage},
		{"undo with a source", []string{"organize", "-undo", "-s", src, "-o", out}, exitUsage},
		{"undo with -priority", []string{"organize", "-undo", "-priority", "-o", out}, exitUsage},
		{"undo without -o or -l", []string{"organize", "-undo"}, exitUsage},
		{"bad log level", []string{"organize", "-s", src, "-l", lib, "-v", "loud"}, exitUsage},
		{"dry run", []string{"organize", "-s", src + list + src2, "-l", lib, "-o", out, "-n", "-priority", "-ignore-the"}, exitOK},
		{"undo with nothing to undo", []string{"organize", "-undo", "-o", out}, exitOK},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := runWithArgs(t, tt.args...); got != tt.want {
				t.Errorf("run(%q) = %d, want %d", tt.args, got, tt.want)
			}
		})
	}
	if _, err := os.Stat(filepath.Join(out, "organize_plan.txt")); err != nil {
		t.Errorf("the dry run wrote no plan: %v", err)
	}
	if _, err := os.Stat(lib); !os.IsNotExist(err) {
		t.Errorf("the dry run created the library: %v", err)
	}

	// A real run moves the song into its bucket, -undo with -l (default out dir) moves it back.
	if got := runWithArgs(t, "organize", "-s", src, "-l", lib); got != exitOK {
		t.Fatalf("real run = %d", got)
	}
	if _, err := os.Stat(filepath.Join(lib, "A", "ABBA - SOS", "song.txt")); err != nil {
		t.Errorf("song not in the library: %v", err)
	}
	if _, err := os.Stat(filepath.Join(base, "organize-output", "organize-journal.jsonl")); err != nil {
		t.Errorf("default out dir: %v", err)
	}
	if got := runWithArgs(t, "organize", "-undo", "-l", lib); got != exitOK {
		t.Fatalf("undo = %d", got)
	}
	if _, err := os.Stat(filepath.Join(song, "song.txt")); err != nil {
		t.Errorf("song not moved back: %v", err)
	}
	// The default CLI does not know the subcommand's flags.
	if got := runWithArgs(t, "-s", src, "-l", lib); got != exitUsage {
		t.Errorf("default CLI with -l = %d, want %d", got, exitUsage)
	}
}

// TestRunOrganizeFlags: -priority, -ignore-the and -deep reach organize. hq and low both
// hold ABBA - SOS; the low version is better (#VERSION), so it only loses with -priority.
func TestRunOrganizeFlags(t *testing.T) {
	t.Setenv("PATH", t.TempDir()) // no ffprobe/ffmpeg: placeholder media are enough
	songTxt := func(artist, title, extra string) string {
		s := "#ARTIST:" + artist + "\n#TITLE:" + title + "\n#MP3:a.mp3\n#BPM:300\n" + extra
		for i := range 10 {
			s += fmt.Sprintf(": %d 2 0 la\n", i*4)
		}
		return s + "E\n"
	}
	setup := func(t *testing.T) (hq, low, lib, out string) {
		base := t.TempDir()
		hq, low, lib, out = filepath.Join(base, "hq"), filepath.Join(base, "low"), filepath.Join(base, "lib"), filepath.Join(base, "out")
		for dir, txt := range map[string]string{
			filepath.Join(hq, "ABBA - SOS"):         songTxt("ABBA", "SOS", ""),
			filepath.Join(low, "ABBA - SOS"):        songTxt("ABBA", "SOS", "#VERSION:1.0.0\n"),
			filepath.Join(hq, "The Beatles - Help"): songTxt("The Beatles", "Help!", ""),
		} {
			if err := os.MkdirAll(dir, 0o755); err != nil {
				t.Fatal(err)
			}
			for name, content := range map[string]string{"song.txt": txt, "a.mp3": "x"} {
				if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o644); err != nil {
					t.Fatal(err)
				}
			}
		}
		return
	}
	type report struct {
		Priority  bool     `json:"priority"`
		IgnoreThe bool     `json:"ignoreThe"`
		Notes     []string `json:"notes"`
	}
	tests := []struct {
		name               string
		sameTier           bool // hq and low in one -s
		flags              []string
		abbaHasVersion     bool
		beatles            string
		priority, ignoreTh bool
		deepNote           bool
	}{
		{"defaults", false, nil, true, "T", false, false, false},
		{"all flags", false, []string{"-priority", "-ignore-the", "-deep"}, false, "B", true, true, true},
		// One -s is one tier: -priority does not separate its folders, so #VERSION decides.
		{"one tier with -priority", true, []string{"-priority"}, true, "T", true, false, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			hq, low, lib, out := setup(t)
			sources := []string{"-s", hq, "-s", low}
			if tt.sameTier {
				sources = []string{"-s", hq + string(filepath.ListSeparator) + low}
			}
			args := append(append([]string{"organize"}, sources...), "-l", lib, "-o", out)
			args = append(args, tt.flags...)
			if got := runWithArgs(t, args...); got != exitOK {
				t.Fatalf("run(%q) = %d", args, got)
			}
			data, err := os.ReadFile(filepath.Join(lib, "A", "ABBA - SOS", "song.txt"))
			if err != nil {
				t.Fatal(err)
			}
			if got := strings.Contains(string(data), "#VERSION"); got != tt.abbaHasVersion {
				t.Errorf("kept ABBA has #VERSION = %v, want %v", got, tt.abbaHasVersion)
			}
			if _, err := os.Stat(filepath.Join(lib, tt.beatles, "The Beatles - Help!", "song.txt")); err != nil {
				t.Errorf("The Beatles not in %s: %v", tt.beatles, err)
			}
			raw, err := os.ReadFile(filepath.Join(out, "organize_plan.json"))
			if err != nil {
				t.Fatal(err)
			}
			var rep report
			if err := json.Unmarshal(raw, &rep); err != nil {
				t.Fatal(err)
			}
			deepNote := false
			for _, n := range rep.Notes {
				deepNote = deepNote || strings.Contains(n, "deep decode")
			}
			if rep.Priority != tt.priority || rep.IgnoreThe != tt.ignoreTh || deepNote != tt.deepNote {
				t.Errorf("report: priority %v, ignoreThe %v, deep note %v (notes %q)", rep.Priority, rep.IgnoreThe, deepNote, rep.Notes)
			}
		})
	}
}
