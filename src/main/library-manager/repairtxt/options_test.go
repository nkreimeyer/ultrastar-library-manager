package repairtxt

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	logtest "github.com/sirupsen/logrus/hooks/test"

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
	// The totals count only the written txt; the excluded one keeps its plan.
	if rep.Damaged != 2 || rep.Fixed != 1 || rep.Weak != 0 {
		t.Errorf("totals: damaged %d, fixed %d, weak %d; want 2, 1, 0", rep.Damaged, rep.Fixed, rep.Weak)
	}
	txt, err := os.ReadFile(cfg.ReportPath(ReportTxt))
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{": 1 repaired", "1 left.", "    1 -> 1    " + rel,
		"1 of 1 would be repaired; excluded, not written, 1 left\n   planned f" + R + "r -> für"} {
		if !strings.Contains(string(txt), want) {
			t.Errorf("%s lacks %q:\n%s", ReportTxt, want, txt)
		}
	}
	if strings.Contains(string(txt), "fixed f"+R+"r") {
		t.Errorf("%s lists the excluded txt's plan as fixed:\n%s", ReportTxt, txt)
	}
	checkLeftSum(t, rep)
	if rep.Files[0].Left != 1 || rep.Files[1].Left != 0 {
		t.Errorf("left = %d, %d; want 1, 0", rep.Files[0].Left, rep.Files[1].Left)
	}
}

// checkLeftSum: the files' Left add up to the characters the totals leave.
func checkLeftSum(t *testing.T, rep Report) {
	t.Helper()
	left := 0
	for _, f := range rep.Files {
		left += f.Left
	}
	if left != rep.Damaged-rep.Fixed {
		t.Errorf("files leave %d, the totals %d - %d", left, rep.Damaged, rep.Fixed)
	}
}

// TestRunSkipFilesDryRun: a dry run counts what it would write, so an excluded txt is
// left out of the totals there too.
func TestRunSkipFilesDryRun(t *testing.T) {
	cfg, paths, orig := twoDamaged(t)
	cfg.DryRun = true
	rel := filepath.Join("A", "A - Eins", "song.txt")
	if err := Run(cfg, fsops.New(true), Options{SkipFiles: map[string]bool{rel: true}}); err != nil {
		t.Fatal(err)
	}
	if !unchanged(t, paths[0], orig[0]) || !unchanged(t, paths[1], orig[1]) {
		t.Error("the dry run changed a txt")
	}
	rep, _ := readReport(t, cfg)
	if len(rep.Files) != 2 || rep.Files[0].Written || !rep.Files[1].Written || rep.Damaged != 2 || rep.Fixed != 1 {
		t.Errorf("report: damaged %d, fixed %d, files %+v", rep.Damaged, rep.Fixed, rep.Files)
	}
	checkLeftSum(t, rep)
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

// TestRunLyricsProgressAndUnrepaired: a txt whose only damaged word stays unresolved is
// asked for lyrics (and the progress says so), and it is neither written nor backed up.
func TestRunLyricsProgressAndUnrepaired(t *testing.T) {
	base := t.TempDir()
	lib := filepath.Join(base, "lib")
	h := "#ARTIST:A\n#MP3:a.mp3\n#BPM:300\n#LANGUAGE:German\n"
	weg := songText("#TITLE:Weg\n"+h, "s"+R+"rprise")
	writeLib(t, lib, map[string]string{
		"A - Corpus": songText("#TITLE:Corpus\n"+h, "für dich", "das Glück"),
		"A - Weg":    weg,
	})
	cfg, err := config.New(lib, filepath.Join(base, "out"), false)
	if err != nil {
		t.Fatal(err)
	}
	var p progressLog
	ops := fsops.New(false)
	if err := Run(cfg, ops, Options{Progress: p.record, Lyrics: fakeLyrics{}}); err != nil {
		t.Fatal(err)
	}
	if p.last[PhaseLyrics] != [2]int{1, 1} || p.calls[PhaseLyrics] != 2 {
		t.Errorf("lyrics progress: last %v, %d calls", p.last[PhaseLyrics], p.calls[PhaseLyrics])
	}
	if !unchanged(t, filepath.Join(lib, "A - Weg", "song.txt"), weg) {
		t.Error("the unrepaired txt changed")
	}
	if st := ops.Stats(); st.Written != 0 || st.Copied != 0 {
		t.Errorf("stats = %+v", st)
	}
	if rep, _ := readReport(t, cfg); len(rep.Files) != 1 || rep.Files[0].Written || len(rep.Files[0].Unresolved) != 1 {
		t.Errorf("report files = %+v", rep.Files)
	}
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
	t.Run("lookup stopped by the cancel", func(t *testing.T) {
		cfg, paths, orig := twoDamaged(t)
		for i, p := range paths {
			orig[i] = songText("#TITLE:"+[]string{"Eins", "Zwei"}[i]+"\n#ARTIST:A\n#MP3:a.mp3\n#BPM:300\n#LANGUAGE:German\n", "s"+R+"rprise")
			if err := os.WriteFile(p, []byte(orig[i]), 0o644); err != nil {
				t.Fatal(err)
			}
		}
		hook := logtest.NewGlobal()
		t.Cleanup(hook.Reset)
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		// A lookup the cancel interrupts fails with the ctx error: that is the stop, not a
		// lyrics problem worth a warning.
		lyrics := lyricsFunc(func(string, string) (string, error) {
			cancel()
			return "", fmt.Errorf("lrclib: %w", context.Canceled)
		})
		err := Run(cfg, fsops.New(false), Options{Ctx: ctx, Lyrics: lyrics})
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("Run = %v", err)
		}
		for _, e := range hook.AllEntries() {
			if strings.HasPrefix(e.Message, "lyrics for") {
				t.Errorf("logged %q", e.Message)
			}
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
		// The second txt was planned but not written: the totals count only the first.
		if rep.Damaged != 2 || rep.Fixed != 1 || rep.Files[1].Fixed != 1 {
			t.Errorf("totals: damaged %d, fixed %d (file 2 plans %d)", rep.Damaged, rep.Fixed, rep.Files[1].Fixed)
		}
		checkLeftSum(t, rep)
		txt, err := os.ReadFile(cfg.ReportPath(ReportTxt))
		if err != nil {
			t.Fatal(err)
		}
		for _, want := range []string{"1 of 1 repaired, 0 left\n   fixed f" + R + "r -> für",
			"1 of 1 would be repaired; not written, 1 left\n   planned Gl" + R + "ck -> Glück"} {
			if !strings.Contains(string(txt), want) {
				t.Errorf("%s lacks %q:\n%s", ReportTxt, want, txt)
			}
		}
		if strings.Contains(string(txt), "excluded") {
			t.Errorf("%s calls the stopped txt excluded:\n%s", ReportTxt, txt)
		}
	})
}

type lyricsFunc func(artist, title string) (string, error)

func (f lyricsFunc) Lyrics(_ context.Context, artist, title string) (string, error) {
	return f(artist, title)
}

// TestReportLabelsUnwrittenFixes: the fixes of a txt that was not written are labelled
// "planned" and "planned WEAK", never like done repairs.
func TestReportLabelsUnwrittenFixes(t *testing.T) {
	cfg, err := config.New(t.TempDir(), t.TempDir(), false)
	if err != nil {
		t.Fatal(err)
	}
	fixes := []Fix{
		{Before: "f" + R + "r", After: "für", Source: srcCorpus, Lines: []int{3}},
		{Before: "s" + R + "ß", After: "süß", Source: srcCorpus, Weak: true, Lines: []int{4}},
	}
	rep := Report{Damaged: 4, Fixed: 2, Weak: 1, Files: []FileReport{
		{Path: "written.txt", Damaged: 2, Fixed: 2, Weak: 1, Written: true, Fixes: fixes},
		{Path: "excluded.txt", Damaged: 2, Fixed: 2, Weak: 1, Left: 2, Excluded: true, Fixes: fixes},
	}}
	if err := writeReports(cfg, fsops.New(false), rep); err != nil {
		t.Fatal(err)
	}
	txt, err := os.ReadFile(cfg.ReportPath(ReportTxt))
	if err != nil {
		t.Fatal(err)
	}
	_, unwritten, _ := strings.Cut(string(txt), "== excluded.txt")
	written, _, _ := strings.Cut(string(txt), "== excluded.txt")
	for _, want := range []string{"   fixed f" + R + "r -> für", "   WEAK  s" + R + "ß -> süß"} {
		if !strings.Contains(written, want) {
			t.Errorf("the written txt lacks %q:\n%s", want, txt)
		}
	}
	for _, want := range []string{"   planned f" + R + "r -> für", "   planned WEAK s" + R + "ß -> süß"} {
		if !strings.Contains(unwritten, want) {
			t.Errorf("the unwritten txt lacks %q:\n%s", want, txt)
		}
	}
}
