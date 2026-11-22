package cleanup

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/nkreimeyer/library-manager/config"
	"github.com/nkreimeyer/library-manager/fsops"
	"github.com/nkreimeyer/library-manager/internal/testutil"
)

// When the song txt cannot be read (an error from ReadSongTags: none found, or not
// readable), no media file is clutter: it is never moved. Other files are still no media.
func TestIsUntaggedMediaKeepsMediaWithoutReadableTxt(t *testing.T) {
	testutil.SkipIfRoot(t)
	lib := t.TempDir()
	testutil.WriteFiles(t, lib, map[string]string{
		"NoTxt/a.mp3": "x", "NoTxt/a.mp4": "x", "NoTxt/a.jpg": "x", "NoTxt/a.nfo": "x",
		"Locked/Song.txt": songTxt, "Locked/a.mp3": "x", "Locked/a.avi": "x", "Locked/a.png": "x", "Locked/a.nfo": "x",
	})
	testutil.ChmodUnreadable(t, filepath.Join(lib, "Locked", "Song.txt"))
	ops := fsops.New(false)
	for _, folder := range []string{"NoTxt", "Locked"} {
		dir := filepath.Join(lib, folder)
		for _, name := range []string{"a.mp3", "a.mp4", "a.avi", "a.jpg", "a.png"} {
			if _, err := os.Stat(filepath.Join(dir, name)); err != nil {
				continue
			}
			if !isUntaggedMedia(ops, dir, name) {
				t.Errorf("%s/%s: not kept as media although the txt cannot be read", folder, name)
			}
		}
		if isUntaggedMedia(ops, dir, "a.nfo") {
			t.Errorf("%s/a.nfo: a non-media file counts as media", folder)
		}
	}
}

// handleFileCleanup moves nothing when the txt turns unreadable between the check of
// cleanupDirectory and the media decision: the media stay, the clutter file may go.
func TestHandleFileCleanupNeverMovesMediaOfUnreadableTxt(t *testing.T) {
	testutil.SkipIfRoot(t)
	cfg := newEnv(t, false)
	testutil.WriteFiles(t, cfg.SongDir, map[string]string{
		"Song/Song.txt": songTxt, "Song/song.mp3": "audio", "Song/clip.mp4": "video", "Song/cover.jpg": "img",
	})
	dir := filepath.Join(cfg.SongDir, "Song")
	testutil.ChmodUnreadable(t, filepath.Join(dir, "Song.txt"))
	ops := fsops.New(false)
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		handleFileCleanup(cfg, ops, dir, e)
	}
	for _, name := range []string{"song.mp3", "clip.mp4", "cover.jpg"} {
		if !testutil.Exists(filepath.Join(dir, name)) {
			t.Errorf("%s was moved", name)
		}
	}
	if testutil.Exists(cfg.ExtraFilesDir()) {
		t.Error("extra-files was created")
	}
}

// A library that is not a folder (a file, or gone) is a module error, nothing is created.
func TestRunFailsWithoutSongDir(t *testing.T) {
	base := t.TempDir()
	file := filepath.Join(base, "file")
	testutil.WriteFile(t, file, "x")
	for name, dir := range map[string]string{"file": file, "missing": filepath.Join(base, "missing")} {
		t.Run(name, func(t *testing.T) {
			out := filepath.Join(base, "out-"+name)
			ops := fsops.New(false)
			if err := Run(config.Config{SongDir: dir, OutDir: out}, ops); err == nil {
				t.Error("Run succeeded")
			}
			if st := ops.Stats(); st.Moved != 0 {
				t.Errorf("stats = %+v", st)
			}
			if testutil.Exists(out) {
				t.Error("the output folder was created")
			}
		})
	}
}
