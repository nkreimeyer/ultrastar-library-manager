package repairtxt

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/nkreimeyer/library-manager/config"
)

func TestResolveWordFiles(t *testing.T) {
	dir := t.TempDir()
	wl, words := filepath.Join(dir, "de.dic"), filepath.Join(dir, "words.txt")
	for _, p := range []string{wl, words} {
		if err := os.WriteFile(p, []byte("für\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	names := InputNames{WordList: "WL", Words: "WF", LyricsCache: "LC"}
	lists, files, err := ResolveWordFiles([]string{"de=" + wl, wl}, []string{words}, names)
	if err != nil || len(lists) != 2 || lists[0] != (WordList{Lang: "de", Path: wl}) || lists[1] != (WordList{Path: wl}) || len(files) != 1 || files[0] != words {
		t.Errorf("got %+v %q %v", lists, files, err)
	}
	if lists, files, err := ResolveWordFiles(nil, nil, names); err != nil || lists != nil || files != nil {
		t.Errorf("no inputs: %+v %q %v", lists, files, err)
	}
	missing := filepath.Join(dir, "missing")
	for _, tt := range []struct {
		lists, words []string
		prefix       string
	}{
		{[]string{missing}, nil, "WL: stat " + missing + ": "},
		{nil, []string{words, missing}, "WF: stat " + missing + ": "},
		{[]string{"Klingon=" + wl}, nil, "-wordlist Klingon=" + wl + ": unknown language"},
	} {
		if _, _, err := ResolveWordFiles(tt.lists, tt.words, names); err == nil || !strings.HasPrefix(err.Error(), tt.prefix) {
			t.Errorf("%q %q: error %v, want prefix %q", tt.lists, tt.words, err, tt.prefix)
		}
	}
}

func TestLyricsCacheDir(t *testing.T) {
	base, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	songs := filepath.Join(base, "songs")
	if err := os.Mkdir(songs, 0o755); err != nil {
		t.Fatal(err)
	}
	cfg, err := config.New(songs, filepath.Join(base, "out"), true)
	if err != nil {
		t.Fatal(err)
	}
	names := InputNames{LyricsCache: "LC"}
	for _, tt := range []struct{ cache, want string }{
		{"", filepath.Join(base, "out", "lyrics-cache")},
		{filepath.Join(base, "mine"), filepath.Join(base, "mine")},
	} {
		if got, err := LyricsCacheDir(cfg, tt.cache, names); err != nil || got != tt.want {
			t.Errorf("%q: %q, %v; want %q", tt.cache, got, err, tt.want)
		}
	}
	if _, err := LyricsCacheDir(cfg, filepath.Join(songs, "cache"), names); err == nil || !strings.HasPrefix(err.Error(), "LC: ") {
		t.Errorf("cache in the song dir: %v", err)
	}
}
