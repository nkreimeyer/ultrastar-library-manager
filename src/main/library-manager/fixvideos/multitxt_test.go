package fixvideos

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"

	"github.com/nkreimeyer/library-manager/fsops"
	"github.com/nkreimeyer/library-manager/internal/testutil"
)

// A folder with two song txts that both name the video (fixtxt left the extra txt, e.g.
// with -t=false): an extension-changing conversion would update only the newest txt and
// delete the video the other one uses. It is refused before encoding, in both runs.
func TestConversionRefusedWhenAnotherTxtReferencesTheVideo(t *testing.T) {
	testutil.RequireFFmpeg(t)
	cfg, songDir, _ := newLibrary(t, false)
	song := filepath.Join(songDir, "A - S")
	newer := filepath.Join(song, "A - S.txt")
	older := filepath.Join(song, "A - S [DUET].txt")
	testutil.WriteFile(t, newer, "#TITLE:S\n#ARTIST:A\n#MP3:song.mp3\n#VIDEO:video.avi\n")
	testutil.WriteFile(t, older, "#TITLE:S [DUET]\n#ARTIST:A\n#MP3:song.mp3\n#VIDEO:Video.AVI\n")
	old := time.Now().Add(-time.Hour)
	if err := os.Chtimes(older, old, old); err != nil {
		t.Fatal(err)
	}
	testutil.WriteFile(t, filepath.Join(song, "song.mp3"), "audio")
	makeMedia(t, filepath.Join(song, "video.avi"), "-c:v", "mpeg4", "-q:v", "1", "-c:a", "ac3")
	before := testutil.Snapshot(t, songDir)

	for _, dry := range []bool{true, false} {
		cfg.DryRun = dry
		ops := fsops.New(dry)
		if err := Run(cfg, ops); err != nil {
			t.Fatal(err)
		}
		if st := ops.Stats(); st != (fsops.Stats{}) {
			t.Errorf("dry=%v: Stats = %+v, want nothing planned or done", dry, st)
		}
	}
	if after := testutil.Snapshot(t, songDir); after != before {
		t.Errorf("library changed\nbefore: %v\n after: %v", before, after)
	}
}

// An extra txt that does not name the video does not block the conversion.
func TestConversionWithUnrelatedExtraTxt(t *testing.T) {
	testutil.RequireFFmpeg(t)
	cfg, songDir, _ := newLibrary(t, false)
	song := filepath.Join(songDir, "A - S")
	testutil.WriteFile(t, filepath.Join(song, "A - S.txt"), "#TITLE:S\n#ARTIST:A\n#MP3:song.mp3\n#VIDEO:video.avi\n")
	older := filepath.Join(song, "A - S (old).txt")
	testutil.WriteFile(t, older, "#TITLE:S\n#ARTIST:A\n#MP3:song.mp3\n")
	old := time.Now().Add(-time.Hour)
	if err := os.Chtimes(older, old, old); err != nil {
		t.Fatal(err)
	}
	testutil.WriteFile(t, filepath.Join(song, "song.mp3"), "audio")
	makeMedia(t, filepath.Join(song, "video.avi"), "-c:v", "mpeg4", "-q:v", "1", "-c:a", "ac3")
	ops := fsops.New(false)
	if err := Run(cfg, ops); err != nil {
		t.Fatal(err)
	}
	if st := ops.Stats(); st.Removed != 1 || st.Failed != 0 {
		t.Errorf("Stats = %+v, want the conversion (original removed)", st)
	}
	if _, err := os.Stat(filepath.Join(song, "video.mp4")); err != nil {
		t.Errorf("video.mp4: %v", err)
	}
}

// A failed txt update is one failure: ops counts a failed write, updateTxtReference only
// the errors that are no ops operation.
func TestUpdateTxtReferenceCountsOnce(t *testing.T) {
	t.Run("write fails", func(t *testing.T) {
		if runtime.GOOS == "windows" || os.Getuid() == 0 {
			t.Skip("needs a folder the user cannot write to")
		}
		dir := filepath.Join(t.TempDir(), "A - S")
		testutil.WriteFile(t, filepath.Join(dir, "song.txt"), "#TITLE:S\n#ARTIST:A\n#VIDEO:v.avi\n")
		if err := os.Chmod(dir, 0o555); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { os.Chmod(dir, 0o755) })
		ops := fsops.New(false)
		if err := updateTxtReference(ops, dir, "v.avi", "v.mp4"); err == nil {
			t.Fatal("no error")
		}
		if n := ops.Stats().Failed; n != 1 {
			t.Errorf("Failed = %d, want 1", n)
		}
	})
	t.Run("no tag names the file", func(t *testing.T) {
		dir := filepath.Join(t.TempDir(), "A - S")
		testutil.WriteFile(t, filepath.Join(dir, "song.txt"), "#TITLE:S\n#ARTIST:A\n#VIDEO:other.avi\n")
		ops := fsops.New(false)
		if err := updateTxtReference(ops, dir, "v.avi", "v.mp4"); err == nil {
			t.Fatal("no error")
		}
		if n := ops.Stats().Failed; n != 1 {
			t.Errorf("Failed = %d, want 1", n)
		}
	})
}
