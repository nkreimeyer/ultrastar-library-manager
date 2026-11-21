package webui

import (
	"context"
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/nkreimeyer/library-manager/config"
	"github.com/nkreimeyer/library-manager/fsops"
	"github.com/nkreimeyer/library-manager/organize"
	"github.com/nkreimeyer/library-manager/repairtxt"
)

// dedupeLibrary is a library with one song in two versions (the one with a cover wins)
// and one other song.
func dedupeLibrary(t *testing.T) (lib, out string) {
	t.Helper()
	base := t.TempDir()
	lib, out = filepath.Join(base, "library"), filepath.Join(base, "out")
	writeSong(t, filepath.Join(lib, "A", "ABBA - SOS"), "ABBA", "SOS", nil)
	writeSong(t, filepath.Join(lib, "Pop", "ABBA - SOS"), "ABBA", "SOS", nil)
	writeSong(t, filepath.Join(lib, "Q", "Queen - Rock"), "Queen", "Rock", nil)
	return lib, out
}

func analyseOrganize(t *testing.T, s *testServer, q organizeRequest) organizeView {
	t.Helper()
	var j Job
	if got := s.do("POST", "/api/organize/analyse", q, &j); got != http.StatusAccepted {
		t.Fatalf("analyse: %d", got)
	}
	j = s.wait()
	if j.State != stateDone || !j.DryRun {
		t.Fatalf("analyse job: %+v", j)
	}
	res := j.Result.(map[string]any)
	var v organizeView
	if got := s.do("GET", "/api/organize/analysis?id="+res["analysis"].(string), nil, &v); got != http.StatusOK {
		t.Fatalf("analysis: %d", got)
	}
	return v
}

func findSong(v organizeView, ref string) (int, songView) {
	for i, sv := range v.Songs {
		if sv.Ref == ref {
			return i, sv
		}
	}
	return -1, songView{}
}

func TestOrganizeValidate(t *testing.T) {
	s := newTestServer(t)
	lib, _ := dedupeLibrary(t)
	var cv configView
	if got := s.do("POST", "/api/organize/validate", organizeRequest{Mode: "dedupe", Library: lib}, &cv); got != http.StatusOK {
		t.Fatalf("validate: %d", got)
	}
	if cv.OutDir != filepath.Join(filepath.Dir(lib), "organize-output") || len(cv.Sources) != 1 || cv.Sources[0].Path != lib {
		t.Errorf("config = %+v", cv)
	}
	var e apiError
	for _, q := range []organizeRequest{
		{Mode: "dedupe", Library: filepath.Join(lib, "missing")},
		{Mode: "organize", Library: lib},
		{Mode: "add", Library: lib, Tiers: [][]string{{lib}}},
		{Mode: "nonsense", Library: lib},
	} {
		if got := s.do("POST", "/api/organize/validate", q, &e); got != http.StatusBadRequest || e.Error == "" {
			t.Errorf("%+v: %d %q", q, got, e.Error)
		}
	}
	if got := s.do("POST", "/api/organize/validate", map[string]any{"mode": "dedupe", "bogus": 1}, &e); got != http.StatusBadRequest {
		t.Errorf("unknown field: %d", got)
	}
}

func TestOrganizeFlow(t *testing.T) {
	s := newTestServer(t)
	lib, out := dedupeLibrary(t)
	before := tree(t, lib)
	q := organizeRequest{Mode: "dedupe", Library: lib, Out: out}

	v := analyseOrganize(t, s, q)
	if !equalTrees(tree(t, lib), before) {
		t.Fatal("the analysis changed the library")
	}
	if v.Summary.Songs != 3 || v.Summary.InPlace != 2 || v.Summary.Duplicates != 1 || len(v.Notes) == 0 || v.Applied {
		t.Errorf("summary = %+v, notes %v", v.Summary, v.Notes)
	}
	ki, keeper := findSong(v, "library:A/ABBA - SOS")
	_, dup := findSong(v, "library:Pop/ABBA - SOS")
	if keeper.Action != "in-place" || dup.Action != "duplicate" || dup.Other != ki || dup.Reason == "" || dup.Artist != "ABBA" {
		t.Errorf("keeper %+v\nduplicate %+v", keeper, dup)
	}

	// Leave the duplicate alone: the plan changes at once, without a new check.
	var v2 organizeView
	s.do("POST", "/api/organize/exclude", map[string]any{"analysis": v.ID, "exclude": []string{dup.Ref}}, &v2)
	if v2.Summary.Duplicates != 0 || v2.Summary.Excluded != 1 || len(v2.Excluded) != 1 {
		t.Errorf("after exclude: %+v %v", v2.Summary, v2.Excluded)
	}
	if _, d := findSong(v2, dup.Ref); d.Action != "left" || !d.Excluded {
		t.Errorf("excluded song: %+v", d)
	}
	// And back: nothing excluded.
	s.do("POST", "/api/organize/exclude", map[string]any{"analysis": v.ID, "exclude": []string{}}, &v2)
	if v2.Summary.Duplicates != 1 {
		t.Errorf("after clearing: %+v", v2.Summary)
	}

	var j Job
	if got := s.do("POST", "/api/organize/apply", map[string]any{"analysis": v.ID}, &j); got != http.StatusAccepted {
		t.Fatalf("apply: %d", got)
	}
	j = s.wait()
	if j.State != stateDone || j.DryRun || j.Stats.Moved != 1 || j.Stats.Failed != 0 {
		t.Fatalf("apply job: %+v", j)
	}
	// The browser reads the JSON names.
	var raw map[string]any
	s.do("GET", "/api/job", nil, &raw)
	if st, _ := raw["stats"].(map[string]any); st["moved"] != 1.0 || st["failed"] != 0.0 {
		t.Errorf("stats JSON = %v", raw["stats"])
	}
	if _, err := os.Stat(filepath.Join(out, "duplicates", "library", "Pop", "ABBA - SOS", "song.txt")); err != nil {
		t.Errorf("duplicate not moved: %v", err)
	}
	res := j.Result.(map[string]any)
	if rep, ok := res["report"].(map[string]any); !ok || rep["dryRun"] != false {
		t.Errorf("result report = %v", res["report"])
	}
	// Applying twice is refused.
	var e apiError
	if got := s.do("POST", "/api/organize/apply", map[string]any{"analysis": v.ID}, &e); got != http.StatusConflict {
		t.Errorf("second apply: %d %v", got, e)
	}

	var hist struct {
		OutDir string `json:"outDir"`
		Runs   []struct {
			Moves  int  `json:"moves"`
			Undone bool `json:"undone"`
			Next   bool `json:"next"`
		} `json:"runs"`
	}
	s.do("POST", "/api/organize/history", folderRequest{Library: lib, Out: out}, &hist)
	if len(hist.Runs) != 1 || hist.Runs[0].Moves != 1 || !hist.Runs[0].Next || hist.OutDir != out {
		t.Errorf("history = %+v", hist)
	}
	if got := s.do("POST", "/api/organize/undo", folderRequest{Library: lib, Out: out}, &j); got != http.StatusAccepted {
		t.Fatalf("undo: %d", got)
	}
	if j = s.wait(); j.State != stateDone || j.Stats.Moved != 1 || j.Phase != organize.PhaseUndo || j.Total == 0 || j.Done != j.Total {
		t.Errorf("undo job: %+v", j)
	}
	// The result names the folders, so the UI can offer to try a partial undo again.
	if res, _ := j.Result.(map[string]any); res["library"] != lib || res["outDir"] != out {
		t.Errorf("undo result = %#v, want library %s and outDir %s", j.Result, lib, out)
	}
	if !equalTrees(tree(t, lib), before) {
		t.Error("undo did not restore the library")
	}
	s.do("POST", "/api/organize/history", folderRequest{Library: lib, Out: out}, &hist)
	if len(hist.Runs) != 1 || !hist.Runs[0].Undone || hist.Runs[0].Next {
		t.Errorf("history after undo = %+v", hist)
	}
	// A second run is listed first.
	v = analyseOrganize(t, s, q)
	s.do("POST", "/api/organize/apply", map[string]any{"analysis": v.ID}, &j)
	if j = s.wait(); j.State != stateDone {
		t.Fatalf("second apply: %+v", j)
	}
	s.do("POST", "/api/organize/history", folderRequest{Library: lib, Out: out}, &hist)
	if len(hist.Runs) != 2 || hist.Runs[0].Undone || !hist.Runs[0].Next || !hist.Runs[1].Undone {
		t.Errorf("history after a second run = %+v", hist)
	}
}

// planChanged analyses a library, adds two songs that change the plan and applies it.
func planChanged(t *testing.T, s *testServer) (v organizeView, j Job, lib string, before map[string]string) {
	t.Helper()
	lib, out := dedupeLibrary(t)
	v = analyseOrganize(t, s, organizeRequest{Mode: "dedupe", Library: lib, Out: out})
	// A better version appears after the review: it would become the keeper.
	writeSong(t, filepath.Join(lib, "New", "ABBA - SOS"), "ABBA", "SOS", map[string]string{"cover.png": "x"})
	writeSong(t, filepath.Join(lib, "R", "Roxette - Joy"), "Roxette", "Joy", nil)
	before = tree(t, lib)
	s.do("POST", "/api/organize/apply", map[string]any{"analysis": v.ID}, &j)
	return v, s.wait(), lib, before
}

func setMaxDiffLines(t *testing.T, n int) {
	old := maxDiffLines
	maxDiffLines = n
	t.Cleanup(func() { maxDiffLines = old })
}

func TestOrganizeApplyPlanChangedTruncated(t *testing.T) {
	setMaxDiffLines(t, 1)
	_, j, _, _ := planChanged(t, newTestServer(t))
	if len(j.Details) != 2 || j.Details[1] != "… and 1 more" || !strings.Contains(j.Error, "2 song folder(s)") {
		t.Errorf("details = %q, error %q", j.Details, j.Error)
	}
}

func TestOrganizeApplyPlanChanged(t *testing.T) {
	setMaxDiffLines(t, 2) // exactly at the limit: nothing cut
	s := newTestServer(t)
	v, j, lib, before := planChanged(t, s)
	if j.State != stateFailed || !strings.Contains(j.Error, "nothing was moved") || j.Stats.Moved != 0 {
		t.Fatalf("job = %+v", j)
	}
	if strings.Join(j.Details, "\n") != strings.Join([]string{
		"library:New/ABBA - SOS: new (duplicate (instead: library:A/ABBA - SOS))",
		"library:R/Roxette - Joy: new (in-place R/Roxette - Joy)",
	}, "\n") {
		t.Errorf("details:\n%s", strings.Join(j.Details, "\n"))
	}
	if res, _ := j.Result.(map[string]any); res["planChanged"] != true || res["analysis"] != v.ID {
		t.Errorf("result = %v", j.Result)
	}
	if !equalTrees(tree(t, lib), before) {
		t.Error("a changed plan moved something")
	}
	// The analysis now holds the new plan; applying it works.
	var v2 organizeView
	s.do("GET", "/api/organize/analysis?id="+v.ID, nil, &v2)
	if v2.Summary.Songs != 5 || v2.Summary.Duplicates != 2 {
		t.Errorf("new plan: %+v", v2.Summary)
	}
	s.do("POST", "/api/organize/apply", map[string]any{"analysis": v.ID}, &j)
	if j = s.wait(); j.State != stateDone || j.Stats.Moved != 2 {
		t.Errorf("second apply: %+v", j)
	}
}

func TestOrganizeBusyAndUnknown(t *testing.T) {
	s := newTestServer(t)
	var e apiError
	if got := s.do("GET", "/api/organize/analysis?id=nope", nil, &e); got != http.StatusNotFound {
		t.Errorf("unknown analysis: %d", got)
	}
	if got := s.do("POST", "/api/organize/apply", map[string]any{"analysis": "nope"}, &e); got != http.StatusNotFound {
		t.Errorf("apply unknown: %d", got)
	}
	block := make(chan struct{})
	defer close(block)
	s.srv.jobs.start("b", "test", fsops.New(true), func(context.Context, jobControl) (any, error) { <-block; return nil, nil })
	lib, out := dedupeLibrary(t)
	if got := s.do("POST", "/api/organize/analyse", organizeRequest{Mode: "dedupe", Library: lib, Out: out}, &e); got != http.StatusConflict || e.Error != errBusy.Error() {
		t.Errorf("busy: %d %q", got, e.Error)
	}
}

// damagedLib writes a song whose word "s�rprise" no evidence repairs, and one whose
// "f�r" the other songs repair.
func damagedLib(t *testing.T) (lib, out string) {
	t.Helper()
	base := t.TempDir()
	lib, out = filepath.Join(base, "lib"), filepath.Join(base, "out")
	h := "#ARTIST:A\n#MP3:a.mp3\n#BPM:300\n#LANGUAGE:German\n"
	songs := map[string]string{
		"A - Corpus": h + "#TITLE:Corpus\n: 0 2 0 für\n: 4 2 0  dich\n- 8\n: 8 2 0 das\n: 12 2 0  Glück\nE\n",
		"A - Zwei":   h + "#TITLE:Zwei\n: 0 2 0 für\n: 4 2 0  mich\nE\n",
		"A - Weg":    h + "#TITLE:Weg\n: 0 2 0 s" + R + "rprise\n- 4\n: 4 2 0 f" + R + "r\n: 8 2 0  dich\nE\n",
		"A - Other":  h + "#TITLE:Other\n: 0 2 0 f" + R + "r\n: 4 2 0  dich\nE\n",
	}
	for name, txt := range songs {
		dir := filepath.Join(lib, name)
		os.MkdirAll(dir, 0o755)
		os.WriteFile(filepath.Join(dir, "song.txt"), []byte(txt), 0o644)
		os.WriteFile(filepath.Join(dir, "a.mp3"), []byte("x"), 0o644)
	}
	return lib, out
}

func TestRepairFlow(t *testing.T) {
	s := newTestServer(t)
	lib, out := damagedLib(t)
	before := tree(t, lib)
	var e apiError
	if got := s.do("POST", "/api/repair/validate", repairRequest{SongDir: lib, LyricsCache: "x"}, &e); got != http.StatusBadRequest {
		t.Errorf("cache without lyrics: %d", got)
	}
	// The cache is written in the analysis (a dry run) too.
	if got := s.do("POST", "/api/repair/validate", repairRequest{SongDir: lib, Lyrics: true, LyricsCache: filepath.Join(lib, "cache")}, &e); got != http.StatusBadRequest || !strings.Contains(e.Error, "lyrics cache") {
		t.Errorf("cache in the song folder: %d %q", got, e.Error)
	}
	if got := s.do("POST", "/api/repair/analyse", repairRequest{SongDir: lib, Out: out, Lyrics: true, LyricsCache: filepath.Join(lib, "cache")}, &e); got != http.StatusBadRequest {
		t.Errorf("analysis with the cache in the song folder: %d", got)
	}
	if got := s.do("POST", "/api/repair/validate", repairRequest{SongDir: lib, WordLists: []string{"de=/missing"}}, &e); got != http.StatusBadRequest {
		t.Errorf("missing word list: %d", got)
	}
	if got := s.do("POST", "/api/repair/validate", repairRequest{SongDir: lib, Words: []string{"/missing"}}, &e); got != http.StatusBadRequest || !strings.Contains(e.Error, "words file") {
		t.Errorf("missing words file: %d %q", got, e.Error)
	}

	var j Job
	s.do("POST", "/api/repair/analyse", repairRequest{SongDir: lib, Out: out}, &j)
	if j = s.wait(); j.State != stateDone {
		t.Fatalf("analyse: %+v", j)
	}
	if !equalTrees(tree(t, lib), before) {
		t.Fatal("the analysis changed the library")
	}
	id := j.Result.(map[string]any)["analysis"].(string)
	var a struct {
		Summary repairSummary `json:"summary"`
		Report  struct {
			Files []struct {
				Path       string `json:"path"`
				Unresolved []struct {
					Word string `json:"word"`
				} `json:"unresolved"`
			} `json:"files"`
		} `json:"report"`
	}
	s.do("GET", "/api/repair/analysis?id="+id, nil, &a)
	if a.Summary.Files != 2 || a.Summary.Damaged != 3 || a.Summary.Fixed != 2 || a.Summary.Left != 1 || a.Summary.Written != 2 { // planned writes
		t.Fatalf("summary = %+v", a.Summary)
	}
	weg := filepath.Join("A - Weg", "song.txt")
	other := filepath.Join("A - Other", "song.txt")
	found := false
	for _, f := range a.Report.Files {
		if f.Path == weg && len(f.Unresolved) == 1 && f.Unresolved[0].Word == "s"+R+"rprise" {
			found = true
		}
	}
	if !found {
		t.Fatalf("unresolved word not reported: %+v", a.Report.Files)
	}

	// Bad choices are refused before anything runs.
	for _, c := range []repairChoice{
		{Path: weg, Word: "s" + R + "rprise", Form: "surprise!"},
		{Path: weg, Word: "s" + R + "rprise", Form: "s rprise"},
		{Path: weg, Word: "s" + R + "rprise", Form: "[x]"},
		{Path: "elsewhere/song.txt", Word: "s" + R + "rprise", Form: "sürprise"},
	} {
		if got := s.do("POST", "/api/repair/apply", map[string]any{"analysis": id, "choices": []repairChoice{c}}, &e); got != http.StatusBadRequest {
			t.Errorf("%+v: %d", c, got)
		}
	}

	choice := repairChoice{Path: weg, Word: "s" + R + "rprise", Form: "sürprise"}
	s.do("POST", "/api/repair/apply", map[string]any{"analysis": id, "choices": []repairChoice{choice}, "skip": []string{other}}, &j)
	if j = s.wait(); j.State != stateDone || j.Stats.Written < 1 {
		t.Fatalf("apply: %+v", j)
	}
	got, _ := os.ReadFile(filepath.Join(lib, weg))
	if strings.Contains(string(got), R) || !strings.Contains(string(got), "sürprise") || !strings.Contains(string(got), "für") {
		t.Errorf("repaired txt:\n%s", got)
	}
	if after := tree(t, lib); after[filepath.ToSlash(other)] != before[filepath.ToSlash(other)] {
		t.Error("the skipped txt was written")
	}
	words, err := os.ReadFile(filepath.Join(out, uiWordsName))
	if err != nil || !strings.Contains(string(words), "[A - Weg]\nsürprise\n[*]\n") {
		t.Errorf("words file: %v\n%s", err, words)
	}
	if res := j.Result.(map[string]any); res["originals"] != filepath.Join(out, "originals") {
		t.Errorf("result = %v", res)
	}
	data, _ := json.Marshal(j.Result)
	if !strings.Contains(string(data), `"written":1`) {
		t.Errorf("result summary: %s", data)
	}

	// A second repair adds its choices to the same words file.
	s.do("POST", "/api/repair/analyse", repairRequest{SongDir: lib, Out: out}, &j)
	if j = s.wait(); j.State != stateDone {
		t.Fatalf("second analyse: %+v", j)
	}
	id = j.Result.(map[string]any)["analysis"].(string)
	choice = repairChoice{Path: other, Word: "f" + R + "r", Form: "für"}
	s.do("POST", "/api/repair/apply", map[string]any{"analysis": id, "choices": []repairChoice{choice}}, &j)
	if j = s.wait(); j.State != stateDone {
		t.Fatalf("second apply: %+v", j)
	}
	words, _ = os.ReadFile(filepath.Join(out, uiWordsName))
	if want := "# Spellings chosen in the library-manager UI, per song folder ([folder name]).\n[A - Weg]\nsürprise\n[*]\n[A - Other]\nfür\n[*]\n"; string(words) != want {
		t.Errorf("words file after two repairs:\n%s", words)
	}
}

func TestRepairApplyWithoutChoices(t *testing.T) {
	s := newTestServer(t)
	lib, out := damagedLib(t)
	var j Job
	s.do("POST", "/api/repair/analyse", repairRequest{SongDir: lib, Out: out}, &j)
	if j = s.wait(); j.State != stateDone {
		t.Fatalf("analyse: %+v", j)
	}
	id := j.Result.(map[string]any)["analysis"].(string)
	s.do("POST", "/api/repair/apply", map[string]any{"analysis": id}, &j)
	if j = s.wait(); j.State != stateDone || j.Stats.Written != 2 {
		t.Fatalf("apply: %+v", j)
	}
	if _, err := os.Stat(filepath.Join(out, uiWordsName)); !os.IsNotExist(err) {
		t.Errorf("a words file without choices: %v", err)
	}
}

func TestRepairApplyWordsFileUnreadable(t *testing.T) {
	s := newTestServer(t)
	lib, out := damagedLib(t)
	var j Job
	s.do("POST", "/api/repair/analyse", repairRequest{SongDir: lib, Out: out}, &j)
	if j = s.wait(); j.State != stateDone {
		t.Fatalf("analyse: %+v", j)
	}
	if err := os.Mkdir(filepath.Join(out, uiWordsName), 0o755); err != nil {
		t.Fatal(err)
	}
	before := tree(t, lib)
	id := j.Result.(map[string]any)["analysis"].(string)
	choice := repairChoice{Path: filepath.Join("A - Weg", "song.txt"), Word: "s" + R + "rprise", Form: "sürprise"}
	s.do("POST", "/api/repair/apply", map[string]any{"analysis": id, "choices": []repairChoice{choice}}, &j)
	if j = s.wait(); j.State != stateFailed || !strings.Contains(j.Error, "saving the chosen spellings") {
		t.Errorf("apply: %+v", j)
	}
	if !equalTrees(tree(t, lib), before) {
		t.Error("txts were written although the choices could not be saved")
	}
}

func TestFits(t *testing.T) {
	tests := []struct {
		word, form string
		want       bool
	}{
		{"f" + R + "r", "für", true},
		{"F" + R + "R", "für", true}, // case is restored by the repair
		{"f" + R + "r", "fur", true},
		{"f" + R + "r", "f r", false},
		{"f" + R + "r", "fü", false},
		{"f" + R + "r", "für!", false},
		{"f" + R + "r", "gür", false},
		{"f" + R + "r", "f" + R + "r", false},
		{"fur", "fur", false}, // nothing to repair
		{R + R, "äö", true},
	}
	for _, tt := range tests {
		if got := fits(tt.word, tt.form); got != tt.want {
			t.Errorf("fits(%q, %q) = %v", tt.word, tt.form, got)
		}
	}
}

// A changed choice replaces the earlier one for the same damaged word of that folder;
// other sections, other words and hand-written lines stay.
func TestWriteChoicesReplacesEarlierSpelling(t *testing.T) {
	lib, out := damagedLib(t)
	path := filepath.Join(out, uiWordsName)
	if err := os.MkdirAll(out, 0o755); err != nil {
		t.Fatal(err)
	}
	old := "# mine\n[A - Weg]\nsörprise für\n[*]\nglobal\n[A - Other]\nsörprise"
	if err := os.WriteFile(path, []byte(old), 0o644); err != nil {
		t.Fatal(err)
	}
	ops := fsops.New(false)
	weg := filepath.Join("A - Weg", "song.txt")
	if err := writeChoices(ops, path, []repairChoice{{Path: weg, Word: "s" + R + "rprise", Form: "sürprise"}}); err != nil {
		t.Fatal(err)
	}
	got, _ := os.ReadFile(path)
	want := "# mine\n[A - Weg]\nfür\n[*]\nglobal\n[A - Other]\nsörprise\n[A - Weg]\nsürprise\n[*]\n"
	if string(got) != want {
		t.Errorf("words file:\n%s\nwant:\n%s", got, want)
	}

	// The repair follows the new choice.
	cfg, err := config.New(lib, out, false)
	if err != nil {
		t.Fatal(err)
	}
	if err := repairtxt.Run(cfg, ops, repairtxt.Options{Words: []string{path}}); err != nil {
		t.Fatal(err)
	}
	if txt, _ := os.ReadFile(filepath.Join(lib, weg)); !strings.Contains(string(txt), "sürprise") {
		t.Errorf("not repaired with the new choice:\n%s", txt)
	}
}
