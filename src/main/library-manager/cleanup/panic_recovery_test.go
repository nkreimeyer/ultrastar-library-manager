package cleanup

import (
	"path/filepath"
	"testing"

	"github.com/nkreimeyer/library-manager/config"
	"github.com/nkreimeyer/library-manager/fsops"
	"github.com/nkreimeyer/library-manager/internal/testutil"
	log "github.com/sirupsen/logrus"
)

// A panic while one folder is cleaned is recovered, logged with the folder, counted as a
// failure, and the other folders are still cleaned.
func TestRunRecoversPanicInOneFolder(t *testing.T) {
	cfg := newEnv(t, false)
	testutil.WriteFiles(t, cfg.SongDir, map[string]string{
		"Boom/song.txt": songTxt, "Boom/song.mp3": "a", "Boom/cover.jpg": "c", "Boom/junk.jpg": "j",
		"Other/song.txt": songTxt, "Other/song.mp3": "a", "Other/cover.jpg": "c", "Other/junk.jpg": "j",
	})
	hook := testutil.CaptureLogsAt(t, log.InfoLevel)
	boom := filepath.Join(cfg.SongDir, "Boom")
	orig := directoryCleaner
	directoryCleaner = func(cfg config.Config, ops *fsops.Ops, folder string) {
		if folder == boom {
			panic("boom")
		}
		orig(cfg, ops, folder)
	}
	t.Cleanup(func() { directoryCleaner = orig })

	ops := fsops.New(false)
	if err := Run(cfg, ops); err != nil {
		t.Fatal(err)
	}
	if f := ops.Stats().Failed; f != 1 {
		t.Errorf("Failed = %d, want 1", f)
	}
	found := false
	for _, e := range hook.AllEntries() {
		found = found || e.Message == "cleanup: "+boom+" not finished: internal error (logged above)"
	}
	if !found {
		t.Error("panic not logged with the folder")
	}
	if testutil.Exists(filepath.Join(cfg.SongDir, "Other", "junk.jpg")) {
		t.Error("the other folder was not cleaned")
	}
	if !testutil.Exists(filepath.Join(boom, "junk.jpg")) {
		t.Error("the panicking folder was changed")
	}
}
