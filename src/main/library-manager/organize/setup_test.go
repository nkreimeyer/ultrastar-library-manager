package organize

import (
	"os"
	"path/filepath"
	"testing"
)

func TestNewConfigPerMode(t *testing.T) {
	base, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	lib, src := filepath.Join(base, "lib"), filepath.Join(base, "src")
	for _, d := range []string{lib, src} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	tiers := [][]string{{src}}
	for _, tt := range []struct {
		m       Mode
		tiers   [][]string
		library string
		sources []string // "" = error
		err     string
	}{
		{ModeOrganize, tiers, lib, []string{src}, ""},
		{ModeOrganize, nil, lib, nil, "at least one source folder is required"},
		{ModeOrganize, tiers, "", nil, "a library folder is required"},
		{ModeDedupe, nil, lib, []string{lib}, ""},
		{ModeDedupe, nil, filepath.Join(base, "missing"), nil, `library dir "` + filepath.Join(base, "missing") + `" must be an existing directory`},
		{ModeRemove, nil, lib, []string{lib}, ""},
		{ModeAdd, tiers, lib, []string{lib, src}, ""},
		{ModeAdd, nil, lib, nil, "at least one source dir is required"},
	} {
		cfg, err := NewConfig(tt.m, tt.tiers, tt.library, "", true)
		if tt.err != "" {
			if err == nil || err.Error() != tt.err {
				t.Errorf("%s %q %q: error %v, want %q", tt.m, tt.tiers, tt.library, err, tt.err)
			}
			continue
		}
		if err != nil {
			t.Errorf("%s %q %q: %v", tt.m, tt.tiers, tt.library, err)
			continue
		}
		var got []string
		for _, s := range cfg.Sources {
			got = append(got, s.Path)
		}
		if len(got) != len(tt.sources) || (len(got) > 0 && got[0] != tt.sources[0]) || cfg.OutDir != filepath.Join(base, "organize-output") || !cfg.DryRun {
			t.Errorf("%s: sources %q, out %s, want sources %q", tt.m, got, cfg.OutDir, tt.sources)
		}
	}
}

func TestNewOptionsDropsBucketFlagsForDedupe(t *testing.T) {
	for _, tt := range []struct {
		m    Mode
		want Options
	}{
		{ModeOrganize, Options{Mode: ModeOrganize, Priority: true, IgnoreThe: true, Deep: true}},
		{ModeAdd, Options{Mode: ModeAdd, Priority: true, IgnoreThe: true, Deep: true}},
		{ModeDedupe, Options{Mode: ModeDedupe, Deep: true}},
	} {
		if got := NewOptions(tt.m, true, true, true); got.Mode != tt.want.Mode || got.Priority != tt.want.Priority || got.IgnoreThe != tt.want.IgnoreThe || got.Deep != tt.want.Deep {
			t.Errorf("NewOptions(%s) = %+v, want %+v", tt.m, got, tt.want)
		}
	}
}
