package main

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/nkreimeyer/ultrastar-songs-sanitizer/check"
	"github.com/nkreimeyer/ultrastar-songs-sanitizer/cleanup"
	"github.com/nkreimeyer/ultrastar-songs-sanitizer/config"
	"github.com/nkreimeyer/ultrastar-songs-sanitizer/fixtxt"
	"github.com/nkreimeyer/ultrastar-songs-sanitizer/fsops"
)

// library is a small fixture exercising the interplay of fixtxt and cleanup.
var library = map[string]string{
	// Stale #MP3 (fixtxt fixes it) and a junk file (cleanup moves it).
	"Cat/A - Song/song.txt":  "#TITLE:Song\r\n#ARTIST:A\r\n#MP3:old name.mp3\r\n#BPM:300\r\n: 0 4 0 M\xe4d\r\nE\r\n",
	"Cat/A - Song/song.mp3":  "audio",
	"Cat/A - Song/junk.nfo":  "junk",
	"Cat/B - NoTxt/song.mp3": "audio",
	"Cat/B - NoTxt/x.jpg":    "img",
	// Song txt but no media: fixtxt moves it to invalid/, so cleanup must not touch it.
	"Cat/C - NoMedia/song.txt": "#TITLE:C\n#ARTIST:C\n#BPM:300\n: 0 4 0 la\nE\n",
	"Cat/C - NoMedia/junk.jpg": "img",
}

func writeLibrary(t *testing.T) config.Config {
	t.Helper()
	root := t.TempDir()
	songs := filepath.Join(root, "songs")
	for rel, content := range library {
		p := filepath.Join(songs, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return mustConfig(t, songs, false)
}

func mustConfig(t *testing.T, songs string, dryRun bool) config.Config {
	t.Helper()
	cfg, err := config.New(songs, "", dryRun)
	if err != nil {
		t.Fatal(err)
	}
	if err := cfg.PrepareOutDir(); err != nil {
		t.Fatal(err)
	}
	return cfg
}

// runPipeline runs fixtxt and cleanup like main does.
func runPipeline(t *testing.T, cfg config.Config) *fsops.Ops {
	t.Helper()
	ops := fsops.New(cfg.DryRun)
	for _, run := range []func(config.Config, *fsops.Ops) error{fixtxt.Run, cleanup.Run} {
		if err := run(cfg, ops); err != nil {
			t.Fatal(err)
		}
	}
	if f := ops.Stats().Failed; f != 0 {
		t.Errorf("%d operations failed", f)
	}
	return ops
}

func snapshot(t *testing.T, root string) string {
	t.Helper()
	var sb strings.Builder
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		info, err := d.Info()
		if err != nil {
			return err
		}
		fmt.Fprintf(&sb, "%s %v %v", path, info.Mode(), info.ModTime().UnixNano())
		if d.Type().IsRegular() {
			data, err := os.ReadFile(path)
			if err != nil {
				return err
			}
			fmt.Fprintf(&sb, " %x", sha256.Sum256(data))
		}
		sb.WriteString("\n")
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return sb.String()
}

func TestPipeline(t *testing.T) {
	cfg := writeLibrary(t)
	runPipeline(t, cfg)

	song := filepath.Join(cfg.SongDir, "Cat", "A - Song")
	txt, err := os.ReadFile(filepath.Join(song, "song.txt"))
	if err != nil {
		t.Fatal(err)
	}
	if want := "#TITLE:Song\n#ARTIST:A\n#MP3:song.mp3\n#BPM:300\n: 0 4 0 Mäd\nE\n"; string(txt) != want {
		t.Errorf("song.txt = %q, want %q", txt, want)
	}
	for _, p := range []string{
		filepath.Join(song, "song.mp3"), // referenced after fixtxt, so cleanup keeps it
		filepath.Join(cfg.ExtraFilesDir(), "Cat", "A - Song", "junk.nfo"),
		filepath.Join(cfg.InvalidDir(), "Cat", "B - NoTxt", "song.mp3"),
		filepath.Join(cfg.InvalidDir(), "Cat", "C - NoMedia", "junk.jpg"),
	} {
		if _, err := os.Stat(p); err != nil {
			t.Errorf("expected %s: %v", p, err)
		}
	}

	// A second run on the result changes nothing.
	before := snapshot(t, cfg.SongDir)
	st := runPipeline(t, mustConfig(t, cfg.SongDir, false)).Stats()
	if st.Written+st.Moved+st.Removed+st.Replaced != 0 {
		t.Errorf("second run was not idempotent: %+v", st)
	}
	if after := snapshot(t, cfg.SongDir); after != before {
		t.Errorf("second run changed the library")
	}
}

func TestPipelineDryRun(t *testing.T) {
	cfg := writeLibrary(t)
	dry := mustConfig(t, cfg.SongDir, true)
	before := snapshot(t, dry.SongDir)
	ops := runPipeline(t, dry)
	if after := snapshot(t, dry.SongDir); after != before {
		t.Errorf("dry-run modified the library")
	}
	// Planned moves: "B - NoTxt" and "C - NoMedia" to invalid/ (fixtxt) and junk.nfo
	// (cleanup). cleanup reads the txt fixtxt would write, whose #MP3 references song.mp3,
	// so it keeps song.mp3 like the real run. It must skip "C - NoMedia", otherwise it would
	// also plan to move its junk.jpg.
	if got := ops.Stats().Moved; got != 3 {
		t.Errorf("planned moves = %d, want 3", got)
	}
	if ops.WouldBeMoved(filepath.Join(dry.SongDir, "Cat", "A - Song", "song.mp3")) {
		t.Error("dry run plans to move song.mp3, which the fixed txt references")
	}
	if _, err := os.Stat(dry.InvalidDir()); !os.IsNotExist(err) {
		t.Errorf("dry-run created %s", dry.InvalidDir())
	}
}

// A category keeping a readme.txt next to its broken songs: fixtxt moves the songs to
// invalid/Cat/..., cleanup then moves the leftover category (one entry). It must be merged
// into invalid/Cat, not become "invalid/Cat (2)", and the dry-run must plan the same.
func TestPipelineMergesLeftoverCategory(t *testing.T) {
	files := map[string]string{
		"Cat/readme.txt":           "about this category\n",
		"Cat/A - Broken/cover.jpg": "img",
		"Cat/B - Broken/song.txt":  "#TITLE:B\n#ARTIST:B\n#BPM:300\n: 0 4 0 la\nE\n",
		"Good/song.txt":            "#TITLE:G\n#ARTIST:G\n#MP3:song.mp3\n#BPM:300\n: 0 4 0 la\nE\n",
		"Good/song.mp3":            "audio",
	}
	root := t.TempDir()
	songs := filepath.Join(root, "songs")
	for rel, content := range files {
		p := filepath.Join(songs, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	planned := runPipeline(t, mustConfig(t, songs, true)).Stats()
	cfg := mustConfig(t, songs, false)
	real := runPipeline(t, cfg).Stats()
	if planned.Moved != real.Moved {
		t.Errorf("dry-run planned %d moves, real run did %d", planned.Moved, real.Moved)
	}
	for _, rel := range []string{"Cat/readme.txt", "Cat/A - Broken/cover.jpg", "Cat/B - Broken/song.txt"} {
		if _, err := os.Stat(filepath.Join(cfg.InvalidDir(), filepath.FromSlash(rel))); err != nil {
			t.Errorf("expected invalid/%s: %v", rel, err)
		}
	}
	if _, err := os.Stat(filepath.Join(cfg.InvalidDir(), "Cat (2)")); !os.IsNotExist(err) {
		t.Errorf("leftover category must be merged, found invalid/Cat (2)")
	}
	if _, err := os.Stat(filepath.Join(songs, "Cat")); !os.IsNotExist(err) {
		t.Errorf("category must be gone from the library (err=%v)", err)
	}
}

// fixtxt -> cleanup -> check: check runs on the result of the earlier modules, so the
// stale #MP3 fixtxt corrects is not reported, and the songs fixtxt moved to invalid/ are
// not checked. ffprobe is hidden: the fixture's media files are placeholders.
func TestPipelineWithCheck(t *testing.T) {
	t.Setenv("PATH", t.TempDir())
	cfg := writeLibrary(t)
	ops := runPipeline(t, cfg)
	if err := check.Run(cfg, ops, check.Options{MoveBroken: true}); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(cfg.ReportPath("check_report.json"))
	if err != nil {
		t.Fatal(err)
	}
	var rep struct {
		Checked int
		Songs   []struct {
			Path, Status string
			Moved        bool
			Findings     []struct{ Severity, Code string }
		}
	}
	if err := json.Unmarshal(data, &rep); err != nil {
		t.Fatal(err)
	}
	if rep.Checked != 1 || len(rep.Songs) != 1 {
		t.Fatalf("report = %s", data)
	}
	s := rep.Songs[0]
	// The fixture song has a single note: suspicious, not broken, and therefore not moved.
	if s.Path != "Cat/A - Song" || s.Status != "suspicious" || s.Moved || len(s.Findings) != 1 || s.Findings[0].Code != "few-notes" {
		t.Errorf("report = %s", data)
	}
	if _, err := os.Stat(filepath.Join(cfg.SongDir, "Cat", "A - Song", "song.txt")); err != nil {
		t.Errorf("song moved: %v", err)
	}
}

// dryRunLibrary has txts that fixtxt changes in ways the later modules depend on: swapped
// #MP3/#VIDEO tags, a stale #MP3, a missing #ARTIST that the folder name provides, and a
// duplicate song txt that fixtxt moves to extra-files/. "E - NoBPM" stays broken, so check
// moves it in both runs.
func dryRunLibrary(audio, video string) map[string]string {
	var notes strings.Builder
	for i := range 10 {
		fmt.Fprintf(&notes, ": %d 2 0 la\n", i*4)
	}
	body := "#BPM:300\n" + notes.String() + "E\n"
	return map[string]string{
		"Cat/A - Swapped/song.txt":              "#TITLE:Swapped\n#ARTIST:A\n#MP3:video.mp4\n#VIDEO:song.mp3\n" + body,
		"Cat/A - Swapped/song.mp3":              audio,
		"Cat/A - Swapped/video.mp4":             video,
		"Cat/B - Stale/song.txt":                "#TITLE:Stale\n#ARTIST:B\n#MP3:old name.mp3\n" + body,
		"Cat/B - Stale/song.mp3":                audio,
		"Cat/B - Stale/junk.nfo":                "junk",
		"Electric Callboy - Hypa Hypa/song.txt": "#TITLE:Hypa Hypa\n#MP3:song.mp3\n" + body,
		"Electric Callboy - Hypa Hypa/song.mp3": audio,
		// copy.txt is newer (see TestPipelineDryRunMatchesRealRun): fixtxt keeps it and
		// moves song.txt to extra-files/.
		"D - Dup/song.txt":   "#TITLE:Dup\n#ARTIST:D\n#MP3:song.mp3\n" + body,
		"D - Dup/copy.txt":   "#TITLE:Dup\n#ARTIST:D\n#MP3:missing.mp3\n" + body,
		"D - Dup/song.mp3":   audio,
		"E - NoBPM/song.txt": "#TITLE:NoBPM\n#ARTIST:E\n#MP3:song.mp3\n" + notes.String() + "E\n",
		"E - NoBPM/song.mp3": audio,
	}
}

// TestPipelineDryRunMatchesRealRun: the dry run of fixtxt -> cleanup -> check with
// MoveBroken plans exactly the moves the real run then performs, and check reports the
// same findings, because every module reads the txts as the earlier modules would have
// written them (fsops.Ops.ReadFile). The dry run leaves the library byte-identical. With
// ffmpeg, real media make check probe the swapped #MP3 (a video without an audio stream).
func TestPipelineDryRunMatchesRealRun(t *testing.T) {
	for _, withMedia := range []bool{false, true} {
		t.Run(map[bool]string{false: "placeholders", true: "media"}[withMedia], func(t *testing.T) {
			files := dryRunLibrary("audio", "video")
			if withMedia {
				audio, video := generateMedia(t)
				files = dryRunLibrary(audio, video)
			} else {
				t.Setenv("PATH", t.TempDir()) // no ffprobe: check skips the media checks
			}
			songs := filepath.Join(t.TempDir(), "songs")
			for rel, content := range files {
				p := filepath.Join(songs, filepath.FromSlash(rel))
				if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
					t.Fatal(err)
				}
			}
			old := time.Now().Add(-time.Hour)
			if err := os.Chtimes(filepath.Join(songs, "D - Dup", "song.txt"), old, old); err != nil {
				t.Fatal(err)
			}
			paths := libraryPaths(t, songs)

			run := func(cfg config.Config) (*fsops.Ops, string) {
				t.Helper()
				ops := runPipeline(t, cfg)
				if err := check.Run(cfg, ops, check.Options{MoveBroken: true}); err != nil {
					t.Fatal(err)
				}
				return ops, checkSongs(t, cfg)
			}

			dry := mustConfig(t, songs, true)
			before := snapshot(t, songs)
			dryOps, dryReport := run(dry)
			if after := snapshot(t, songs); after != before {
				t.Fatal("dry run modified the library")
			}
			var planned []string
			for _, p := range paths {
				if dryOps.WouldBeMoved(p) && !dryOps.WouldBeMoved(filepath.Dir(p)) {
					planned = append(planned, p)
				}
			}

			// The real run uses a fresh output dir, so its targets match the plan's.
			if err := os.RemoveAll(dry.OutDir); err != nil {
				t.Fatal(err)
			}
			realOps, realReport := run(mustConfig(t, songs, false))
			var moved []string
			for _, p := range paths {
				if !pathExists(p) && pathExists(filepath.Dir(p)) {
					moved = append(moved, p)
				}
			}

			if !slices.Equal(planned, moved) {
				t.Errorf("dry run planned to move\n  %q\nthe real run moved\n  %q", planned, moved)
			}
			if d, r := dryOps.Stats(), realOps.Stats(); d.Moved != r.Moved || d.Written != r.Written {
				t.Errorf("dry run planned %+v, the real run did %+v", d, r)
			}
			if dryReport != realReport {
				t.Errorf("check findings differ:\ndry run:  %s\nreal run: %s", dryReport, realReport)
			}

			// The moves are the expected ones: song.txt of "D - Dup" (fixtxt), junk.nfo
			// (cleanup) and "E - NoBPM" (check). No song with a txt fixtxt repairs is moved.
			want := []string{
				filepath.Join(songs, "Cat", "B - Stale", "junk.nfo"),
				filepath.Join(songs, "D - Dup", "song.txt"),
				filepath.Join(songs, "E - NoBPM"),
			}
			if !slices.Equal(moved, want) {
				t.Errorf("moved %q, want %q", moved, want)
			}
		})
	}
}

// generateMedia returns a 3 s mp3 and a 3 s mp4 without an audio stream, or skips the test
// if ffmpeg cannot make them.
func generateMedia(t *testing.T) (audio, video string) {
	t.Helper()
	dir := t.TempDir()
	for _, gen := range []struct {
		name string
		args []string
	}{
		{"a.mp3", []string{"-f", "lavfi", "-i", "sine=d=3"}},
		{"v.mp4", []string{"-f", "lavfi", "-i", "testsrc=d=3:s=64x64:r=10", "-c:v", "libx264", "-pix_fmt", "yuv420p"}},
	} {
		args := append(append([]string{"-v", "error", "-y"}, gen.args...), filepath.Join(dir, gen.name))
		if out, err := exec.Command("ffmpeg", args...).CombinedOutput(); err != nil {
			t.Skipf("cannot generate test media with ffmpeg: %v\n%s", err, out)
		}
	}
	a, err := os.ReadFile(filepath.Join(dir, "a.mp3"))
	if err != nil {
		t.Fatal(err)
	}
	v, err := os.ReadFile(filepath.Join(dir, "v.mp4"))
	if err != nil {
		t.Fatal(err)
	}
	return string(a), string(v)
}

// libraryPaths returns every file and folder below root, sorted.
func libraryPaths(t *testing.T, root string) []string {
	t.Helper()
	var paths []string
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if path != root {
			paths = append(paths, path)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return paths
}

func pathExists(path string) bool {
	_, err := os.Lstat(path)
	return err == nil
}

// checkSongs returns the counts and songs of check_report.json (everything but the
// dry-run flag), as JSON.
func checkSongs(t *testing.T, cfg config.Config) string {
	t.Helper()
	data, err := os.ReadFile(cfg.ReportPath("check_report.json"))
	if err != nil {
		t.Fatal(err)
	}
	var rep map[string]any
	if err := json.Unmarshal(data, &rep); err != nil {
		t.Fatal(err)
	}
	delete(rep, "dryRun")
	out, err := json.Marshal(rep)
	if err != nil {
		t.Fatal(err)
	}
	return string(out)
}
