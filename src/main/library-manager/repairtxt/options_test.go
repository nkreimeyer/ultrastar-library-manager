package repairtxt

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"sync"
	"testing"

	"github.com/nkreimeyer/library-manager/config"
	"github.com/nkreimeyer/library-manager/fsops"
)

// twoDamaged writes a library with two damaged txts and a corpus that repairs both.
func twoDamaged(t *testing.T) (cfg config.Config, paths []string, originals []string) {
	t.Helper()
	base := t.TempDir()
	lib := filepath.Join(base, "lib")
	h := "#ARTIST:A\n#MP3:a.mp3\n#BPM:300\n#LANGUAGE:German\n"
	one := songText("#TITLE:Eins\n"+h, "f"+R+"r dich")
	two := songText("#TITLE:Zwei\n"+h, "das Gl"+R+"ck")
	writeLib(t, lib, map[string]string{
		"A/A - Corpus": songText("#TITLE:Corpus\n"+h, "für dich", "das Glück für mich", "kein Glück"),
		"A/A - Drei":   songText("#TITLE:Drei\n"+h, "nur für dich", "Glück"),
		"A/A - Eins":   one,
		"A/A - Zwei":   two,
	})
	cfg, err := config.New(lib, filepath.Join(base, "out"), false)
	if err != nil {
		t.Fatal(err)
	}
	return cfg, []string{filepath.Join(lib, "A/A - Eins/song.txt"), filepath.Join(lib, "A/A - Zwei/song.txt")}, []string{one, two}
}

func readReport(t *testing.T, cfg config.Config) (Report, bool) {
	t.Helper()
	data, err := os.ReadFile(cfg.ReportPath(ReportJSON))
	if os.IsNotExist(err) {
		return Report{}, false
	}
	var rep Report
	if err != nil || json.Unmarshal(data, &rep) != nil {
		t.Fatalf("report: %v", err)
	}
	return rep, true
}

func unchanged(t *testing.T, path, want string) bool {
	t.Helper()
	got, err := os.ReadFile(path)
	return err == nil && string(got) == want
}

func TestRunSkipFiles(t *testing.T) {
	cfg, paths, orig := twoDamaged(t)
	rel := filepath.Join("A", "A - Eins", "song.txt")
	ops := fsops.New(false)
	if err := Run(cfg, ops, Options{SkipFiles: map[string]bool{rel: true}}); err != nil {
		t.Fatal(err)
	}
	if !unchanged(t, paths[0], orig[0]) {
		t.Error("the skipped txt was written")
	}
	if unchanged(t, paths[1], orig[1]) {
		t.Error("the other txt was not repaired")
	}
	rep, _ := readReport(t, cfg)
	if len(rep.Files) != 2 || rep.Files[0].Path != rel || !rep.Files[0].Excluded || rep.Files[0].Written || rep.Files[0].Fixed != 1 ||
		rep.Files[1].Excluded || !rep.Files[1].Written {
		t.Errorf("report files = %+v", rep.Files)
	}
	if st := ops.Stats(); st.Written != 1 || st.Copied != 1 {
		t.Errorf("stats = %+v", st)
	}
}

type progressLog struct {
	mu    sync.Mutex
	last  map[string][2]int // phase -> highest done, total
	calls map[string]int
}

func (p *progressLog) record(phase string, done, total int) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.last == nil {
		p.last, p.calls = map[string][2]int{}, map[string]int{}
	}
	if done >= p.last[phase][0] {
		p.last[phase] = [2]int{done, total}
	}
	p.calls[phase]++
}

func TestRunProgress(t *testing.T) {
	cfg, _, _ := twoDamaged(t)
	var p progressLog
	if err := Run(cfg, fsops.New(false), Options{Progress: p.record, Lyrics: fakeLyrics{}}); err != nil {
		t.Fatal(err)
	}
	want := map[string][2]int{PhaseScan: {4, 4}, PhasePlan: {2, 2}, PhaseLyrics: {0, 0}, PhaseWrite: {2, 2}}
	for phase, w := range want {
		if p.last[phase] != w {
			t.Errorf("%s: last = %v, want %v", phase, p.last[phase], w)
		}
	}
	if p.calls[PhaseScan] != 5 || p.calls[PhaseWrite] != 3 {
		t.Errorf("calls = %v (want a 0 call plus one per item)", p.calls)
	}
}

func TestRunCancelled(t *testing.T) {
	t.Run("before", func(t *testing.T) {
		cfg, paths, orig := twoDamaged(t)
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		var p progressLog
		err := Run(cfg, fsops.New(false), Options{Ctx: ctx, Progress: p.record})
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("Run = %v", err)
		}
		if !unchanged(t, paths[0], orig[0]) || !unchanged(t, paths[1], orig[1]) {
			t.Error("a cancelled run wrote a txt")
		}
		if _, ok := readReport(t, cfg); ok {
			t.Error("a run cancelled before writing wrote a report")
		}
		if p.last[PhaseScan][0] != 0 || p.calls[PhasePlan] != 0 {
			t.Errorf("progress after the cancel: %v", p.last)
		}
	})
	t.Run("while planning", func(t *testing.T) {
		cfg, paths, orig := twoDamaged(t)
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		planned := 0
		err := Run(cfg, fsops.New(false), Options{Ctx: ctx, Workers: 1, Progress: func(phase string, done, total int) {
			if phase == PhasePlan && done == 1 {
				cancel()
			}
			if phase == PhasePlan && done > 0 {
				planned++
			}
		}})
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("Run = %v", err)
		}
		if planned != 1 {
			t.Errorf("%d txts planned, want 1", planned)
		}
		if !unchanged(t, paths[0], orig[0]) || !unchanged(t, paths[1], orig[1]) {
			t.Error("a run cancelled while planning wrote a txt")
		}
		if _, ok := readReport(t, cfg); ok {
			t.Error("a report was written")
		}
	})
	t.Run("while asking for lyrics", func(t *testing.T) {
		cfg, paths, orig := twoDamaged(t)
		// "s�rprise" is not in the corpus, so both songs ask for lyrics.
		for i, p := range paths {
			orig[i] = songText("#TITLE:"+[]string{"Eins", "Zwei"}[i]+"\n#ARTIST:A\n#MP3:a.mp3\n#BPM:300\n#LANGUAGE:German\n", "s"+R+"rprise")
			if err := os.WriteFile(p, []byte(orig[i]), 0o644); err != nil {
				t.Fatal(err)
			}
		}
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		asked := 0
		lyrics := lyricsFunc(func(string, string) (string, error) {
			asked++
			cancel()
			return "Sürprise", nil
		})
		err := Run(cfg, fsops.New(false), Options{Ctx: ctx, Lyrics: lyrics})
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("Run = %v", err)
		}
		if asked != 1 {
			t.Errorf("asked %d times, want 1", asked)
		}
		if !unchanged(t, paths[0], orig[0]) || !unchanged(t, paths[1], orig[1]) {
			t.Error("a txt was written")
		}
	})
	t.Run("while writing", func(t *testing.T) {
		cfg, paths, orig := twoDamaged(t)
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		err := Run(cfg, fsops.New(false), Options{Ctx: ctx, Progress: func(phase string, done, total int) {
			if phase == PhaseWrite && done == 1 {
				cancel()
			}
		}})
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("Run = %v", err)
		}
		if unchanged(t, paths[0], orig[0]) || !unchanged(t, paths[1], orig[1]) {
			t.Error("want the first txt written and the second not")
		}
		rep, ok := readReport(t, cfg)
		if !ok || len(rep.Files) != 2 || !rep.Files[0].Written || rep.Files[1].Written {
			t.Errorf("report = %+v", rep.Files)
		}
	})
}

type lyricsFunc func(artist, title string) (string, error)

func (f lyricsFunc) Lyrics(artist, title string) (string, error) { return f(artist, title) }
