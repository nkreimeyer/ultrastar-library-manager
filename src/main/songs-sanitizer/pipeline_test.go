package main

import (
	"crypto/sha256"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"

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
	// Planned moves: "B - NoTxt" and "C - NoMedia" to invalid/ (fixtxt); junk.nfo and
	// song.mp3 (cleanup, because the txt fix is not applied in dry-run mode, see README).
	// cleanup must skip "C - NoMedia", otherwise it would also plan to move its junk.jpg.
	if got := ops.Stats().Moved; got != 4 {
		t.Errorf("planned moves = %d, want 4", got)
	}
	if _, err := os.Stat(dry.InvalidDir()); !os.IsNotExist(err) {
		t.Errorf("dry-run created %s", dry.InvalidDir())
	}
}
