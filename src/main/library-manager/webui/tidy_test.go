package webui

import (
	"context"
	"maps"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/nkreimeyer/library-manager/fsops"
	"github.com/nkreimeyer/library-manager/internal/testutil"
	"github.com/nkreimeyer/library-manager/mediacache"
	"github.com/nkreimeyer/library-manager/organize"
)

// allTidy is every tidy-up option the test server can run (no ffmpeg: no media steps).
func allTidy() *tidyOptions {
	return &tidyOptions{UTF8: true, FileTags: true, FillTags: true, Leftovers: true, Folders: true, Broken: true, Dedupe: true}
}

// tidyLibrary is dedupeLibrary with something for every tidy-up step: a CP1252 song text, a
// wrong #MP3, a leftover file, a folder with only an audio file and a song without notes
// (broken, and there is no working version of it).
func tidyLibrary(t *testing.T) (lib, out string) {
	t.Helper()
	lib, out = dedupeLibrary(t)
	write := func(rel, content string) {
		t.Helper()
		p := filepath.Join(lib, rel)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("C/Cafe - Song/song.txt", "#ARTIST:Caf\xe9\n#TITLE:Song\n#MP3:song.mp3\n"+testutil.NotesTxt("cafe song"))
	write("C/Cafe - Song/song.mp3", "audio cafe")
	write("W/Wrong - Ref/song.txt", "#ARTIST:Wrong\n#TITLE:Ref\n#MP3:missing.mp3\n"+testutil.NotesTxt("wrong ref"))
	write("W/Wrong - Ref/audio.mp3", "audio wrong")
	write("Q/Queen - Rock/readme.nfo", "leftover")
	write("Junk/song.mp3", "audio without a song")
	write("L/Lone - Broken/song.txt", "#ARTIST:Lone\n#TITLE:Broken\n#MP3:song.mp3\nE\n")
	write("L/Lone - Broken/song.mp3", "audio lone")
	return lib, out
}

func TestTidyFlow(t *testing.T) {
	s := newTestServer(t)
	lib, out := tidyLibrary(t)
	before := libTree(t, lib)
	q := organizeRequest{Mode: "dedupe", Library: lib, Out: out, Tidy: allTidy()}

	v := analyseOrganize(t, s, q)
	if !maps.Equal(libTree(t, lib), before) {
		t.Fatal("the check changed the library")
	}
	td := v.Tidy
	if td == nil || !td.Songs {
		t.Fatalf("tidy view = %+v", td)
	}
	for _, want := range []string{filepath.Join("C", "Cafe - Song", "song.txt"), filepath.Join("W", "Wrong - Ref", "song.txt")} {
		if !slices.Contains(td.TextFiles, want) {
			t.Errorf("text files %q lack %s", td.TextFiles, want)
		}
	}
	if !slices.Equal(td.Leftovers, []string{filepath.Join("Q", "Queen - Rock", "readme.nfo")}) || !slices.Equal(td.BrokenFolders, []string{"Junk"}) {
		t.Errorf("leftovers %q, broken folders %q", td.Leftovers, td.BrokenFolders)
	}
	// The song step saw the library as the text and cleanup steps leave it: the broken
	// folder is gone, the lone broken song goes to invalid/ (BrokenSetAside).
	if v.Summary.Duplicates != 1 || v.Summary.Invalid != 1 || v.Summary.Songs != 6 {
		t.Errorf("summary = %+v", v.Summary)
	}
	if _, lone := findSong(v, "library:L/Lone - Broken"); lone.Action != "invalid" {
		t.Errorf("lone broken song: %+v", lone)
	}
	var steps []string
	for _, st := range td.Steps {
		steps = append(steps, st.Step)
	}
	if !slices.Equal(steps, []string{stepText, stepCleanup, stepSongs}) {
		t.Errorf("steps = %v", steps)
	}

	var j Job
	if got := s.do("POST", "/api/organize/apply", map[string]any{"analysis": v.ID}, &j); got != http.StatusAccepted || j.Kind != "tidy-apply" {
		t.Fatalf("apply: %d, kind %q", got, j.Kind)
	}
	// Done (not a changed plan): the check planned what the real run found.
	if j = s.wait(); j.State != stateDone || j.Stats.Failed != 0 {
		t.Fatalf("apply job: %+v", j)
	}
	res := j.Result.(map[string]any)
	if res["tidy"] != true || len(res["steps"].([]any)) != 3 {
		t.Errorf("result = %v", res)
	}
	for _, p := range []string{
		filepath.Join(out, "duplicates", "library", "Pop", "ABBA - SOS", "song.txt"),
		filepath.Join(out, "invalid", "library", "L", "Lone - Broken", "song.txt"),
		filepath.Join(out, "invalid", "Junk", "song.mp3"),
		filepath.Join(out, "extra-files", "Q", "Queen - Rock", "readme.nfo"),
	} {
		if _, err := os.Stat(p); err != nil {
			t.Errorf("not set aside: %v", err)
		}
	}
	cafe, _ := os.ReadFile(filepath.Join(lib, "C", "Cafe - Song", "song.txt"))
	if !utf8.Valid(cafe) || !strings.Contains(string(cafe), "#ARTIST:Café") {
		t.Errorf("not converted to UTF-8:\n%s", cafe)
	}
	if ref, _ := os.ReadFile(filepath.Join(lib, "W", "Wrong - Ref", "song.txt")); !strings.Contains(string(ref), "#MP3:audio.mp3") {
		t.Errorf("#MP3 not fixed:\n%s", ref)
	}

	// Undo puts back the song folders (the journal's moves), not the text and cleanup steps.
	s.do("POST", "/api/organize/undo", folderRequest{Library: lib, Out: out}, &j)
	if j = s.wait(); j.State != stateDone || j.Stats.Moved != 2 {
		t.Fatalf("undo: %+v", j)
	}
	for _, rel := range []string{"Pop/ABBA - SOS/song.txt", "L/Lone - Broken/song.txt"} {
		if _, err := os.Stat(filepath.Join(lib, rel)); err != nil {
			t.Errorf("not put back: %v", err)
		}
	}
}

// Without "deduplicate" no song is set aside as a duplicate; without "broken songs" the
// lone broken song and the folder without a song stay.
func TestTidyOptionsOff(t *testing.T) {
	s := newTestServer(t)
	lib, out := tidyLibrary(t)
	noDedupe := allTidy()
	noDedupe.Dedupe = false
	v := analyseOrganize(t, s, organizeRequest{Mode: "dedupe", Library: lib, Out: out, Tidy: noDedupe})
	if v.Summary.Duplicates != 0 || v.Summary.Invalid != 1 || len(v.Similar) != 0 {
		t.Errorf("no dedupe: summary %+v, similar %v", v.Summary, v.Similar)
	}

	noBroken := allTidy()
	noBroken.Broken, noBroken.Folders = false, false
	v = analyseOrganize(t, s, organizeRequest{Mode: "dedupe", Library: lib, Out: out, Tidy: noBroken})
	if v.Summary.Duplicates != 1 || v.Summary.Invalid != 0 || len(v.Tidy.BrokenFolders) != 0 {
		t.Errorf("no broken: summary %+v, broken folders %q", v.Summary, v.Tidy.BrokenFolders)
	}
	if _, lone := findSong(v, "library:L/Lone - Broken"); lone.Action != "left" {
		t.Errorf("lone broken song: %+v", lone)
	}

	// Only the text steps: no song step, no song plan, nothing to leave alone.
	textOnly := &tidyOptions{UTF8: true}
	v = analyseOrganize(t, s, organizeRequest{Mode: "dedupe", Library: lib, Out: out, Tidy: textOnly})
	if v.Tidy.Songs || len(v.Songs) != 0 || !slices.Contains(v.Tidy.TextFiles, filepath.Join("C", "Cafe - Song", "song.txt")) {
		t.Errorf("text only: %+v", v.Tidy)
	}
	// The wrong #MP3 is a file-tag fix, which is off.
	if slices.Contains(v.Tidy.TextFiles, filepath.Join("W", "Wrong - Ref", "song.txt")) {
		t.Errorf("text only plans the file-tag fix: %q", v.Tidy.TextFiles)
	}
	var e apiError
	if got := s.do("POST", "/api/organize/exclude", map[string]any{"analysis": v.ID, "exclude": []string{"library:A/ABBA - SOS"}}, &e); got != http.StatusConflict {
		t.Errorf("exclude without a song plan: %d %q", got, e.Error)
	}
	before := libTree(t, lib)
	var j Job
	s.do("POST", "/api/organize/apply", map[string]any{"analysis": v.ID}, &j)
	if j = s.wait(); j.State != stateDone || j.Stats.Written == 0 || j.Stats.Moved != 0 {
		t.Fatalf("apply: %+v", j)
	}
	if after := libTree(t, lib); len(after) != len(before) {
		t.Error("a text-only tidy-up moved files")
	}
	if got := s.do("POST", "/api/organize/apply", map[string]any{"analysis": v.ID}, &e); got != http.StatusConflict {
		t.Errorf("second apply: %d", got)
	}
}

func TestTidyRefused(t *testing.T) {
	s := newTestServer(t)
	lib, out := tidyLibrary(t)
	var e apiError
	for _, c := range []struct {
		q    organizeRequest
		want string
	}{
		{organizeRequest{Mode: "dedupe", Library: lib, Out: out, Tidy: &tidyOptions{}}, "choose at least one"},
		{organizeRequest{Mode: "organize", Library: filepath.Join(out, "..", "new"), Tiers: [][]string{{lib}}, Tidy: allTidy()}, "dedupe"},
		{organizeRequest{Mode: "dedupe", Library: lib, Out: out, Tidy: &tidyOptions{Audio: true}}, "ffmpeg"}, // the test server has no ffmpeg
	} {
		if got := s.do("POST", "/api/organize/analyse", c.q, &e); got != http.StatusBadRequest || !strings.Contains(e.Error, c.want) {
			t.Errorf("%+v: %d %q, want 400 with %q", c.q.Tidy, got, e.Error, c.want)
		}
	}
	// validate gives the cheap errors too (not the ffmpeg one: it is asked while typing).
	if got := s.do("POST", "/api/organize/validate", organizeRequest{Mode: "dedupe", Library: lib, Tidy: &tidyOptions{}}, &e); got != http.StatusBadRequest {
		t.Errorf("validate nothing chosen: %d", got)
	}
}

// A failing song step ends the run: the media steps would convert the songs the review set
// aside. (organize.Run refuses KeepDuplicates outside dedupe before touching anything.)
func TestTidySongStepFailureSkipsMedia(t *testing.T) {
	base := t.TempDir()
	src, lib := filepath.Join(base, "src"), filepath.Join(base, "lib")
	writeSong(t, filepath.Join(src, "A - B"), "A", "B", nil)
	writeSong(t, filepath.Join(lib, "C", "C - D"), "C", "D", nil)
	cfg, err := organize.NewConfig(organize.ModeOrganize, [][]string{{src}}, lib, "", false)
	if err != nil {
		t.Fatal(err)
	}
	r := &stepRun{
		tidy: &tidyOptions{Folders: true, Broken: true, Video: true, Audio: true}, cfg: cfg, ops: fsops.New(false), ctx: context.Background(),
		c: jobControl{r: newJobRunner(), j: &job{}}, songs: organize.NewOptions(organize.ModeOrganize, false, false, false),
	}
	steps, err := r.run()
	var names []string
	for _, st := range steps {
		names = append(names, st.Step)
	}
	if err == nil || !slices.Equal(names, []string{stepText, stepCleanup, stepSongs}) || steps[2].Error == "" {
		t.Errorf("steps %v (%+v), err %v; want the run to end at the failed song step", names, steps, err)
	}
	// The job's media cache is saved also after a failed step.
	if _, err := os.Stat(filepath.Join(lib, mediacache.FileName)); err != nil {
		t.Errorf("no media cache saved after the failure: %v", err)
	}
}

// A job of text steps only probes no media: it opens no cache.
func TestTidyTextOnlyOpensNoCache(t *testing.T) {
	s := newTestServer(t)
	lib, out := tidyLibrary(t)
	analyseOrganize(t, s, organizeRequest{Mode: "dedupe", Library: lib, Out: out, Tidy: &tidyOptions{UTF8: true}})
	if _, err := os.Stat(filepath.Join(lib, mediacache.FileName)); !os.IsNotExist(err) {
		t.Errorf("a text-only check wrote a media cache (%v)", err)
	}
	analyseOrganize(t, s, organizeRequest{Mode: "dedupe", Library: lib, Out: out, Tidy: &tidyOptions{Dedupe: true}})
	if _, err := os.Stat(filepath.Join(lib, mediacache.FileName)); err != nil {
		t.Errorf("a check with the song step saved no media cache: %v", err)
	}
}

// A tidy-up whose song plan changed after the text and cleanup steps ran says that no song
// folder was moved, not that nothing was.
func TestTidyApplyPlanChanged(t *testing.T) {
	s := newTestServer(t)
	lib, out := tidyLibrary(t)
	v := analyseOrganize(t, s, organizeRequest{Mode: "dedupe", Library: lib, Out: out, Tidy: allTidy()})
	writeSong(t, filepath.Join(lib, "New", "ABBA - SOS"), "ABBA", "SOS", map[string]string{"cover.png": "x"})
	var j Job
	s.do("POST", "/api/organize/apply", map[string]any{"analysis": v.ID}, &j)
	if j.Kind != "tidy-apply" {
		t.Errorf("job kind %q", j.Kind)
	}
	j = s.wait()
	if j.State != stateFailed || !strings.Contains(j.Error, "no song folder was moved") || strings.Contains(j.Error, "nothing was moved") {
		t.Fatalf("job = %+v", j)
	}
	if res, _ := j.Result.(map[string]any); res["planChanged"] != true || len(res["steps"].([]any)) != 3 {
		t.Errorf("result = %v", j.Result)
	}
	var v2 organizeView
	s.do("GET", "/api/organize/analysis?id="+v.ID, nil, &v2)
	if !v2.Tidy.Done || len(v2.Tidy.TextFiles) != 0 {
		t.Errorf("tidy view after the changed plan: %+v", v2.Tidy)
	}
}

// The apply job reuses the probes its check (a dry run) saved in the library's cache.
func TestApplyReusesCheckProbes(t *testing.T) {
	path := os.Getenv("PATH")
	s := newTestServer(t)
	t.Setenv("PATH", path)
	testutil.RequireFFmpeg(t)
	lib, out := dedupeLibrary(t)
	for _, rel := range []string{"A/ABBA - SOS/song.mp3", "Pop/ABBA - SOS/song.mp3", "Q/Queen - Rock/song.mp3"} {
		testutil.GenerateMediaAt(t, filepath.Join(lib, rel), "-f", "lavfi", "-i", "sine=frequency=440:duration=2", "-c:a", "libmp3lame")
		old := time.Now().Add(-time.Hour) // older than mediacache.RacyWindow
		if err := os.Chtimes(filepath.Join(lib, rel), old, old); err != nil {
			t.Fatal(err)
		}
	}
	hook := testutil.CaptureLogs(t)
	summary := func() string {
		t.Helper()
		defer hook.Reset()
		var got string
		for _, e := range hook.AllEntries() {
			if strings.HasPrefix(e.Message, "Media cache: ") {
				got = e.Message
			}
		}
		return got
	}
	v := analyseOrganize(t, s, organizeRequest{Mode: "dedupe", Library: lib, Out: out, Tidy: allTidy()})
	if got := summary(); got != "Media cache: 0 reused, 3 scanned, 3 stored" {
		t.Errorf("check: %q", got)
	}
	var j Job
	s.do("POST", "/api/organize/apply", map[string]any{"analysis": v.ID}, &j)
	if j = s.wait(); j.State != stateDone {
		t.Fatalf("apply job: %+v", j)
	}
	if got := summary(); got != "Media cache: 3 reused, 0 scanned, 0 stored" {
		t.Errorf("apply: %q", got)
	}
}
