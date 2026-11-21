package webui

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/nkreimeyer/library-manager/fsops"
	"github.com/nkreimeyer/library-manager/repairtxt"
)

func TestRepairOptionsLyricsCache(t *testing.T) {
	lib, out := damagedLib(t)
	for _, tt := range []struct{ cache, want string }{
		{"", filepath.Join(out, "lyrics-cache")},
		{filepath.Join(out, "mine"), filepath.Join(out, "mine")},
	} {
		q := repairRequest{SongDir: lib, Out: out, Lyrics: true, LyricsCache: tt.cache}
		cfg, err := q.config(true)
		if err != nil {
			t.Fatal(err)
		}
		opts, err := q.options(cfg, fsops.New(true))
		if err != nil {
			t.Fatal(err)
		}
		if l, ok := opts.Lyrics.(*repairtxt.LRCLib); !ok || l.CacheDir != tt.want {
			t.Errorf("cache %q: lyrics = %#v, want cache %s", tt.cache, opts.Lyrics, tt.want)
		}
	}
	// Without the online lookup there is no lyrics source.
	q := repairRequest{SongDir: lib, Out: out}
	cfg, _ := q.config(true)
	if opts, err := q.options(cfg, fsops.New(true)); err != nil || opts.Lyrics != nil {
		t.Errorf("no lyrics: %v, %v", opts.Lyrics, err)
	}
}

// The input errors name the form's fields, not the CLI's flags.
func TestRepairOptionsErrorNames(t *testing.T) {
	lib, out := damagedLib(t)
	missing := filepath.Join(out, "missing")
	for _, tt := range []struct {
		q      repairRequest
		prefix string
	}{
		{repairRequest{WordLists: []string{missing}}, "word list: stat " + missing},
		{repairRequest{Words: []string{missing}}, "words file: stat " + missing},
		{repairRequest{Lyrics: true, LyricsCache: filepath.Join(lib, "cache")}, "lyrics cache: "},
		{repairRequest{LyricsCache: missing}, "a lyrics cache needs the online lyrics lookup"},
	} {
		tt.q.SongDir, tt.q.Out = lib, out
		cfg, err := tt.q.config(true)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := tt.q.options(cfg, fsops.New(true)); err == nil || !strings.HasPrefix(err.Error(), tt.prefix) {
			t.Errorf("%+v: error %v, want prefix %q", tt.q, err, tt.prefix)
		}
	}
}
