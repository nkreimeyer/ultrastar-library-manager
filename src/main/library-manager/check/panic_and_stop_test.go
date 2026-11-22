package check

import (
	"context"
	"errors"
	"path/filepath"
	"testing"

	"github.com/nkreimeyer/library-manager/fsops"
	"github.com/nkreimeyer/library-manager/internal/testutil"
	log "github.com/sirupsen/logrus"
)

// A panic while one folder is checked is recovered, logged with the folder, counted as a
// failure; the folder is not in the report and the others are still checked.
func TestRunRecoversPanicInOneFolder(t *testing.T) {
	withoutFFmpeg(t)
	hook := testutil.CaptureLogsAt(t, log.InfoLevel)
	lib := filepath.Join(t.TempDir(), "songs")
	testutil.WriteFiles(t, lib, map[string]string{
		"Boom/song.txt":  song("song.mp3", 10),
		"Other/song.txt": song("song.mp3", 10), // audio missing: a finding
	})
	boom := filepath.Join(lib, "Boom")
	orig := folderInspector
	folderInspector = func(insp *Inspector, folder string) (Inspection, error) {
		if folder == boom {
			panic("boom")
		}
		return orig(insp, folder)
	}
	t.Cleanup(func() { folderInspector = orig })

	ops, r := run(t, newConfig(t, lib, false), Options{})
	if f := ops.Stats().Failed; f != 1 {
		t.Errorf("Failed = %d, want 1", f)
	}
	found := false
	for _, e := range hook.AllEntries() {
		found = found || e.Message == "check: "+boom+" not checked: internal error (logged above)"
	}
	if !found {
		t.Error("panic not logged with the folder")
	}
	if r.Checked != 1 {
		t.Errorf("Checked = %d, want 1", r.Checked)
	}
	findSong(t, r, "Other")
	for _, s := range r.Songs {
		if s.Path == "Boom" {
			t.Error("the panicking folder is in the report")
		}
	}
}

// A stop while waiting for a free decode slot returns the ctx error, takes no slot and
// records no result.
func TestDeepDecodeStoppedWhileWaitingForSlot(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	i := &Inspector{ctx: ctx, ops: fsops.New(false), deepSem: make(chan struct{}, 1)}
	i.deepSem <- struct{}{} // the only slot is taken
	decErr, decWarn, fileResult, err := i.deepDecode(filepath.Join(t.TempDir(), "song.mp3"))
	if !errors.Is(err, context.Canceled) {
		t.Errorf("err = %v, want context.Canceled", err)
	}
	if fileResult || decErr != "" || decWarn != "" {
		t.Errorf("recorded a result: %q %q %v", decErr, decWarn, fileResult)
	}
	if len(i.deepSem) != 1 {
		t.Errorf("slot count = %d, want the one taken before", len(i.deepSem))
	}
}

// A nil ctx means "never stopped".
func TestNewInspectorNilCtxIsBackground(t *testing.T) {
	var nilCtx context.Context
	i, _ := NewInspector(nilCtx, fsops.New(true), false)
	if i.ctx == nil {
		t.Fatal("ctx is nil")
	}
	if i.ctx.Err() != nil || i.ctx.Done() != nil {
		t.Errorf("ctx = %v, want context.Background()", i.ctx)
	}
}

// A song txt that cannot be read makes Inspect fail once: counted as unreadable, logged,
// and not reported as a folder without song txt.
func TestInspectUnreadableTxtIsSkipped(t *testing.T) {
	testutil.SkipIfRoot(t)
	hook := testutil.CaptureLogsAt(t, log.InfoLevel)
	lib := t.TempDir()
	testutil.WriteFiles(t, lib, map[string]string{"A/song.txt": song("song.mp3", 10)})
	txt := filepath.Join(lib, "A", "song.txt")
	testutil.ChmodUnreadable(t, txt)

	ops := fsops.New(false)
	i, _ := NewInspector(nil, ops, false)
	res, err := i.Inspect(filepath.Join(lib, "A"))
	if err == nil {
		t.Fatalf("Inspect succeeded: %+v", res)
	}
	if len(res.Findings) != 0 || res.Broken {
		t.Errorf("result = %+v, want empty", res)
	}
	if f := ops.Stats().Failed; f != 1 {
		t.Errorf("Failed = %d, want 1", f)
	}
	found := false
	for _, e := range hook.AllEntries() {
		found = found || e.Level == log.ErrorLevel
	}
	if !found {
		t.Error("nothing logged")
	}
}
