package utils

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/nkreimeyer/library-manager/fsops"
	"github.com/nkreimeyer/library-manager/internal/testutil"
)

// A song txt that exists but cannot be read: the read error is returned (ReadSongTags,
// FileReferenceReplacement) or, for IsFileReferenced, the file counts as referenced.
func TestTxtReadErrors(t *testing.T) {
	testutil.SkipIfRoot(t)
	dir := t.TempDir()
	txt := filepath.Join(dir, "song.txt")
	testutil.WriteFile(t, txt, "#ARTIST:A\n#TITLE:T\n#MP3:a.mp3\n: 0 1 0 x\nE\n")
	if err := os.Chmod(txt, 0o000); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(txt, 0o644) })
	for _, ops := range []*fsops.Ops{nil, fsops.New(false)} {
		if tags, err := ReadSongTags(ops, dir); err == nil {
			t.Errorf("ReadSongTags = %v, nil error", tags)
		}
		if !IsFileReferenced(ops, dir, "never-mentioned.avi") {
			t.Error("an unreadable txt must count as referencing the file")
		}
		if _, content, err := FileReferenceReplacement(ops, dir, "a.mp3", "b.mp3"); err == nil || content != nil {
			t.Errorf("FileReferenceReplacement = %q, %v", content, err)
		}
	}
}
