package check

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/nkreimeyer/library-manager/config"
	"github.com/nkreimeyer/library-manager/fsops"
	"github.com/nkreimeyer/library-manager/internal/testutil"
	"github.com/nkreimeyer/library-manager/songtxt"
)

// A check whose report cannot be written fails: a missing report must not look like a
// clean library.
func TestRunFailsWhenTheReportCannotBeWritten(t *testing.T) {
	withoutFFmpeg(t)
	setup := func(t *testing.T) (config.Config, string) {
		lib := filepath.Join(t.TempDir(), "songs")
		testutil.WriteFiles(t, lib, map[string]string{"Song/song.txt": song("song.mp3", 10), "Song/song.mp3": "x"})
		return newConfig(t, lib, false), lib
	}
	t.Run("read-only output folder", func(t *testing.T) {
		testutil.SkipIfRoot(t)
		cfg, _ := setup(t)
		if err := os.Chmod(cfg.OutDir, 0o555); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { os.Chmod(cfg.OutDir, 0o755) })
		if err := Run(cfg, fsops.New(false), Options{}); err == nil {
			t.Error("Run succeeded although the report cannot be written")
		}
	})
	for _, name := range []string{reportJSON, reportTxt} {
		t.Run(name+" blocked", func(t *testing.T) {
			cfg, lib := setup(t)
			// A non-empty folder at the report's path cannot be replaced by a file.
			testutil.WriteFile(t, filepath.Join(cfg.ReportPath(name), "keep"), "x")
			before := testutil.Tree(t, lib)
			ops := fsops.New(false)
			if err := Run(cfg, ops, Options{}); err == nil {
				t.Error("Run succeeded although the report cannot be written")
			}
			if got := testutil.Tree(t, lib); len(got) != len(before) {
				t.Errorf("the library changed: %v", got)
			}
		})
	}
}

// A song folder that is a file or does not exist is an error of the module, and no report
// is written.
func TestRunFailsWithoutSongDir(t *testing.T) {
	base := t.TempDir()
	file := filepath.Join(base, "file")
	testutil.WriteFile(t, file, "x")
	for name, dir := range map[string]string{"file": file, "missing": filepath.Join(base, "missing")} {
		t.Run(name, func(t *testing.T) {
			out := filepath.Join(base, "out-"+name)
			if err := Run(config.Config{SongDir: dir, OutDir: out}, fsops.New(false), Options{}); err == nil {
				t.Error("Run succeeded")
			}
			if testutil.Exists(out) {
				t.Error("a report folder was created")
			}
		})
	}
}

// A referenced media file of zero bytes is an error (the audio) or a warning (a cover),
// found on disk without ffprobe.
func TestRunReportsZeroSizeMediaAsEmpty(t *testing.T) {
	withoutFFmpeg(t)
	lib := filepath.Join(t.TempDir(), "songs")
	testutil.WriteFiles(t, lib, map[string]string{
		"Empty Audio/song.txt": song("song.mp3", 10), "Empty Audio/song.mp3": "",
		"Empty Cover/song.txt": strings.Replace(song("song.mp3", 10), "#BPM", "#COVER:c.jpg\n#BPM", 1),
		"Empty Cover/song.mp3": "x", "Empty Cover/c.jpg": "",
	})
	_, r := run(t, newConfig(t, lib, true), Options{})
	got := songCodes(r)
	if want := []string{"error:empty-file"}; !equalStrings(got["Empty Audio"], want) {
		t.Errorf("empty audio: %v, want %v", got["Empty Audio"], want)
	}
	if want := []string{"warning:empty-file"}; !equalStrings(got["Empty Cover"], want) {
		t.Errorf("empty cover: %v, want %v", got["Empty Cover"], want)
	}
	if findSong(t, r, "Empty Audio").Status != "broken" {
		t.Errorf("a song with an empty audio file is not broken: %+v", findSong(t, r, "Empty Audio"))
	}
}

func equalStrings(a, b []string) bool { return strings.Join(a, "|") == strings.Join(b, "|") }

// A parser issue code the table does not know is a warning, never an error: a new code
// must not make songs "broken", and it is reported with its own code and line.
func TestAnalyzeUnknownIssueCodeIsAWarning(t *testing.T) {
	in := input(header+notes(10), healthyFiles())
	in.Issues = append(in.Issues, songtxt.Issue{Line: 3, Code: "made-up-code", Msg: "something new"})
	got := analyze(in)
	if len(got) != 1 || got[0].Severity != SevWarning || got[0].Code != "made-up-code" || got[0].Line != 3 || got[0].Message != "something new" {
		t.Errorf("findings = %+v", got)
	}
	if isBroken(got) {
		t.Error("an unknown issue code made the song broken")
	}
}
