package fixtxt

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/nkreimeyer/library-manager/config"
	"github.com/nkreimeyer/library-manager/fsops"
	"github.com/nkreimeyer/library-manager/internal/testutil"
	log "github.com/sirupsen/logrus"
)

// A panic while one folder is processed is recovered, logged with the folder, counted as
// a failure, and the other folders are still fixed.
func TestRunRecoversPanicInOneFolder(t *testing.T) {
	hook := testutil.CaptureLogsAt(t, log.InfoLevel)
	cfg := newLibrary(t, false)
	testutil.WriteFiles(t, cfg.SongDir, map[string]string{
		"Boom - Song/song.txt":  "#MP3:song.mp3\n" + notesBody,
		"Boom - Song/song.mp3":  "a",
		"Other - Song/song.txt": "#MP3:song.mp3\n" + notesBody,
		"Other - Song/song.mp3": "a",
	})
	boom := filepath.Join(cfg.SongDir, "Boom - Song")
	orig := folderHandler
	folderHandler = func(cfg config.Config, ops *fsops.Ops, col *collector, opts Options, folder string) {
		if folder == boom {
			panic("boom")
		}
		orig(cfg, ops, col, opts, folder)
	}
	t.Cleanup(func() { folderHandler = orig })

	ops := run(t, cfg)
	if f := ops.Stats().Failed; f != 1 {
		t.Errorf("Failed = %d, want 1", f)
	}
	if len(messages(hook, "fixtxt: "+boom+" not finished")) != 1 {
		t.Errorf("panic not logged with the folder: %v", hook.AllEntries())
	}
	if got := testutil.ReadFile(t, filepath.Join(cfg.SongDir, "Other - Song", "song.txt")); !strings.Contains(got, "#ARTIST:Other") {
		t.Errorf("the other song was not processed: %q", got)
	}
	if got := testutil.ReadFile(t, filepath.Join(boom, "song.txt")); got != "#MP3:song.mp3\n"+notesBody {
		t.Errorf("the panicking song changed: %q", got)
	}
}

// A folder that cannot be listed is skipped and counted, and nothing is modified.
func TestHandleFolderUnreadableFolder(t *testing.T) {
	testutil.SkipIfRoot(t)
	hook := testutil.CaptureLogsAt(t, log.InfoLevel)
	cfg := newLibrary(t, false)
	raw := "#MP3:song.mp3\n" + notesBody
	testutil.WriteFiles(t, cfg.SongDir, map[string]string{"A - S/song.txt": raw, "A - S/song.mp3": "a"})
	folder := filepath.Join(cfg.SongDir, "A - S")
	before := testutil.Snapshot(t, cfg.SongDir)
	testutil.ChmodUnreadable(t, folder)

	ops := fsops.New(false)
	handleFolder(cfg, ops, &collector{}, DefaultOptions(), folder)
	if f := ops.Stats().Failed; f != 1 {
		t.Errorf("Failed = %d, want 1", f)
	}
	if len(messages(hook, "Skipping "+folder)) != 1 {
		t.Error("skip not logged")
	}
	if err := os.Chmod(folder, 0o755); err != nil {
		t.Fatal(err)
	}
	if after := testutil.Snapshot(t, cfg.SongDir); after != before {
		t.Errorf("library changed:\n%s\n%s", before, after)
	}
}

// A song txt that cannot be read (SongTxtFiles fails) means the folder is skipped, never
// treated as broken and moved to invalid/.
func TestHandleFolderUnreadableTxtIsNotBroken(t *testing.T) {
	testutil.SkipIfRoot(t)
	cfg := newLibrary(t, false)
	testutil.WriteFiles(t, cfg.SongDir, map[string]string{"A - S/song.txt": "#MP3:song.mp3\n" + notesBody, "A - S/song.mp3": "a"})
	folder := filepath.Join(cfg.SongDir, "A - S")
	testutil.ChmodUnreadable(t, filepath.Join(folder, "song.txt"))

	ops := fsops.New(false)
	handleFolder(cfg, ops, &collector{}, DefaultOptions(), folder)
	st := ops.Stats()
	if st.Failed != 1 || st.Moved != 0 || st.Written != 0 {
		t.Errorf("stats = %+v, want one failure and nothing else", st)
	}
	if !testutil.Exists(filepath.Join(folder, "song.mp3")) || testutil.Exists(cfg.InvalidDir()) {
		t.Error("folder was moved")
	}
}

// If no song txt can be stat'ed, keepNewestSongTxt fails the folder and moves nothing.
func TestKeepNewestSongTxtStatFails(t *testing.T) {
	hook := testutil.CaptureLogsAt(t, log.InfoLevel)
	cfg := newLibrary(t, false)
	folder := filepath.Join(cfg.SongDir, "A - S")
	testutil.WriteFile(t, filepath.Join(folder, "keep.txt"), "x")
	ops := fsops.New(false)
	gone := []string{filepath.Join(folder, "gone1.txt"), filepath.Join(folder, "gone2.txt")}
	got, ok := keepNewestSongTxt(cfg, ops, DefaultOptions(), folder, gone)
	if ok || got != "" {
		t.Errorf("keepNewestSongTxt = %q, %v, want \"\", false", got, ok)
	}
	if f := ops.Stats().Failed; f != 1 {
		t.Errorf("Failed = %d, want 1", f)
	}
	if len(messages(hook, "Could not determine the most recently modified")) != 1 {
		t.Error("not logged")
	}
	if st := ops.Stats(); st.Moved != 0 {
		t.Errorf("stats = %+v", st)
	}
}

// A txt that cannot be read (here: a dangling symlink, then a mode-000 file) is counted as
// a failure, nothing is written and no lines are returned.
func TestUpdateTxtFileReadFails(t *testing.T) {
	cfg := newLibrary(t, false)
	folder := filepath.Join(cfg.SongDir, "A - S")
	testutil.WriteFile(t, filepath.Join(folder, "song.mp3"), "a")
	dangling := filepath.Join(folder, "dangling.txt")
	if err := os.Symlink(filepath.Join(folder, "nowhere.txt"), dangling); err != nil {
		t.Skip("symlinks unavailable:", err)
	}
	col := &collector{}
	ops := fsops.New(false)
	if lines := updateTxtFile(cfg, ops, col, DefaultOptions(), folder, dangling, songFiles{txtName: "dangling.txt"}); lines != nil {
		t.Errorf("lines = %q, want nil", lines)
	}
	if st := ops.Stats(); st.Failed != 1 || st.Written != 0 {
		t.Errorf("stats = %+v, want one failure, no writes", st)
	}
	if len(col.missingFiles)+len(col.encodingIssues) != 0 {
		t.Errorf("reports got entries: %v %v", col.missingFiles, col.encodingIssues)
	}
}
