package check

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/nkreimeyer/library-manager/config"
	"github.com/nkreimeyer/library-manager/fsops"
)

// A stopped run checks no song, moves nothing and writes no report: a partial report
// would look complete.
func TestRunStopped(t *testing.T) {
	lib, out := t.TempDir(), t.TempDir()
	song := filepath.Join(lib, "A - B")
	if err := os.MkdirAll(song, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(song, "song.txt"), []byte("#ARTIST:A\n#TITLE:B\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	cfg, err := config.New(lib, out, false)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	err = Run(cfg, fsops.New(false), Options{MoveBroken: true, Ctx: ctx})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("Run = %v, want context.Canceled", err)
	}
	if _, err := os.Stat(cfg.ReportPath(reportJSON)); err == nil {
		t.Error("a report was written")
	}
	if _, err := os.Stat(filepath.Join(song, "song.txt")); err != nil {
		t.Errorf("the song was moved: %v", err)
	}
}

// Progress counts every song folder.
func TestRunProgress(t *testing.T) {
	lib, out := t.TempDir(), t.TempDir()
	for _, n := range []string{"A - B", "C - D"} {
		if err := os.MkdirAll(filepath.Join(lib, n), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(lib, n, "song.txt"), []byte("#ARTIST:A\n#TITLE:B\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	cfg, err := config.New(lib, out, true)
	if err != nil {
		t.Fatal(err)
	}
	var last, total int
	if err := Run(cfg, fsops.New(true), Options{Progress: func(d, n int) { last, total = d, n }}); err != nil {
		t.Fatal(err)
	}
	if last != 2 || total != 2 {
		t.Errorf("progress ended at %d of %d, want 2 of 2", last, total)
	}
}
