package webui

import (
	"path/filepath"
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
