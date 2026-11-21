package organize

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"sort"
	"strings"
	"testing"

	"github.com/nkreimeyer/library-manager/config"
	"github.com/nkreimeyer/library-manager/fsops"
	"github.com/nkreimeyer/library-manager/internal/testutil"
	log "github.com/sirupsen/logrus"
)

// duetTxt is a valid duet body with P1 and P2, sung to lyric.
func duetTxt(lyric string) string {
	var b strings.Builder
	b.WriteString("#BPM:300\n#GAP:0\n")
	for _, p := range []string{"P1", "P2"} {
		b.WriteString(p + "\n")
		for i := range 10 {
			fmt.Fprintf(&b, ": %d 2 0 %s\n", i*4, lyric)
		}
	}
	b.WriteString("E\n")
	return b.String()
}

// songFiles returns the files of a working song folder dir (relative path) with a
// placeholder mp3 (enough without ffprobe). extra: "duet" (P1/P2 notes), "cover" and
// "background" (a referenced, valid image), "warning" (a repeated #GENRE: one song
// warning), or a header line.
func songFiles(dir, artist, title string, extra ...string) map[string]string {
	head := "#ARTIST:" + artist + "\n#TITLE:" + title + "\n#MP3:song.mp3\n"
	files := map[string]string{dir + "/song.mp3": "audio of " + dir}
	lyric := artist + " / " + title // different songs, different content
	body := testutil.NotesTxt(lyric)
	for _, e := range extra {
		switch {
		case e == "duet":
			body = duetTxt(lyric)
		case e == "cover":
			head += "#COVER:cover.png\n"
			files[dir+"/cover.png"] = testutil.PNG(4, 4)
		case e == "background":
			head += "#BACKGROUND:background.png\n"
			files[dir+"/background.png"] = testutil.PNG(16, 9)
		case e == "video":
			head += "#VIDEO:video.mp4\n"
			files[dir+"/video.mp4"] = "video of " + dir
		case e == "warning":
			head += "#GENRE:Pop\n#GENRE:Rock\n"
		default:
			head += e + "\n"
		}
	}
	files[dir+"/song.txt"] = head + body
	return files
}

func merge(ms ...map[string]string) map[string]string {
	out := map[string]string{}
	for _, m := range ms {
		for k, v := range m {
			out[k] = v
		}
	}
	return out
}

// tree lists every entry below root: "dir/" for folders, "<path> <sha256>" for files.
// A missing root gives nil.
func tree(t *testing.T, root string) []string {
	t.Helper()
	if _, err := os.Stat(root); os.IsNotExist(err) {
		return nil
	}
	var out []string
	err := filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(root, p)
		rel = filepath.ToSlash(rel)
		if d.IsDir() {
			out = append(out, rel+"/")
			return nil
		}
		data, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		out = append(out, fmt.Sprintf("%s %x", rel, sha256.Sum256(data)))
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	sort.Strings(out)
	return out
}

// files is tree without the folders.
func files(t *testing.T, root string) []string {
	var out []string
	for _, e := range tree(t, root) {
		if !strings.HasSuffix(e, "/") {
			out = append(out, e)
		}
	}
	return out
}

// noFFprobe hides ffmpeg/ffprobe, so media are not probed and placeholders work.
func noFFprobe(t *testing.T) { t.Setenv("PATH", t.TempDir()) }

func organizeCfg(t *testing.T, tiers [][]string, lib, out string, dry bool) config.OrganizeConfig {
	t.Helper()
	cfg, err := config.NewOrganize(tiers, lib, out, dry)
	if err != nil {
		t.Fatal(err)
	}
	if err := cfg.PrepareOutDir(); err != nil {
		t.Fatal(err)
	}
	return cfg
}

func runOrganize(t *testing.T, cfg config.OrganizeConfig, opts Options) (fsops.Stats, planReport) {
	t.Helper()
	ops := fsops.New(cfg.DryRun)
	if err := Run(cfg, ops, opts); err != nil {
		t.Fatalf("Run: %v", err)
	}
	data, err := os.ReadFile(cfg.ReportPath(reportJSON))
	if err != nil {
		t.Fatal(err)
	}
	var rep planReport
	if err := json.Unmarshal(data, &rep); err != nil {
		t.Fatal(err)
	}
	text, err := os.ReadFile(cfg.ReportPath(reportTxt))
	if err != nil {
		t.Errorf("text report: %v", err)
	}
	if want := string(rep.text()); string(text) != want {
		t.Errorf("text report differs from the JSON report:\n%s\nwant:\n%s", text, want)
	}
	return ops.Stats(), rep
}

func runUndo(t *testing.T, cfg config.OrganizeConfig) fsops.Stats {
	t.Helper()
	ops := fsops.New(cfg.DryRun)
	if err := Undo(cfg, ops, UndoOptions{}); err != nil {
		t.Fatalf("Undo: %v", err)
	}
	return ops.Stats()
}

// sources creates the three test sources (hq, low1, low2) below base.
func threeSources(t *testing.T, base string) (hqDir, low1Dir, low2Dir string) {
	hqDir, low1Dir, low2Dir = filepath.Join(base, "hq"), filepath.Join(base, "low1"), filepath.Join(base, "low2")
	testutil.WriteFiles(t, hqDir, merge(
		songFiles("ABBA - SOS", "ABBA", "SOS"),
		// Broken: no #BPM. The working low1 version replaces it.
		map[string]string{
			"Broken/song.txt": "#ARTIST:Queen\n#TITLE:Bohemian Rhapsody\n#MP3:song.mp3\n: 0 2 0 la\nE\n",
			"Broken/song.mp3": "x",
		},
	))
	testutil.WriteFiles(t, low1Dir, merge(
		songFiles("Pop/ABBA - SOS", "ABBA", "SOS", "cover"), // better (a cover), but lower tier
		songFiles("Queen - Bohemian Rhapsody", "Queen", "Bohemian Rhapsody"),
		songFiles("Elton", "Elton John", "Song"),
		songFiles("Enc", "Mot\x81rhead", "Ace of Spades"), // 0x81 is undefined in CP1252: an encoding problem
	))
	testutil.WriteFiles(t, low2Dir, merge(
		songFiles("Rock/Beatles", "The Beatles", "Help!"),
		songFiles("Elton Duet", "Elton John", "Song", "duet"),
	))
	for _, d := range []string{filepath.Join(low2Dir, "Empty", "Sub"), filepath.Join(low1Dir, "Pop", "Nothing")} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	// Folders with only hidden files (macOS leftovers) count as empty.
	testutil.WriteFiles(t, low2Dir, map[string]string{"Empty/Sub/.DS_Store": "x"})
	testutil.WriteFiles(t, low1Dir, map[string]string{"Pop/Nothing/._song.txt": "#ARTIST:X\n#TITLE:Y\n"})
	return
}

func TestRunMovesIntoBucketsAndUndo(t *testing.T) {
	noFFprobe(t)
	base := t.TempDir()
	hqDir, low1Dir, low2Dir := threeSources(t, base)
	lib, out := filepath.Join(base, "library"), filepath.Join(base, "out")
	beforeSources := map[string][]string{}
	for _, d := range []string{hqDir, low1Dir, low2Dir} {
		beforeSources[d] = tree(t, d)
	}
	tiers := [][]string{{hqDir}, {low1Dir, low2Dir}}

	cfg := organizeCfg(t, tiers, lib, out, false)
	st, rep := runOrganize(t, cfg, Options{Priority: true})

	wantLib := []string{
		"A/ABBA - SOS/song.mp3", "A/ABBA - SOS/song.txt",
		"E/Elton John - Song [DUET]/song.mp3", "E/Elton John - Song [DUET]/song.txt",
		"E/Elton John - Song/song.mp3", "E/Elton John - Song/song.txt",
		"Q/Queen - Bohemian Rhapsody/song.mp3", "Q/Queen - Bohemian Rhapsody/song.txt",
		"T/The Beatles - Help!/song.mp3", "T/The Beatles - Help!/song.txt",
	}
	var gotLib []string
	for _, f := range files(t, lib) {
		gotLib = append(gotLib, f[:strings.LastIndex(f, " ")])
	}
	if !reflect.DeepEqual(gotLib, wantLib) {
		t.Errorf("library files:\n%s\nwant:\n%s", strings.Join(gotLib, "\n"), strings.Join(wantLib, "\n"))
	}
	// The kept ABBA is the hq one (priority), the low1 one is a duplicate.
	if data, _ := os.ReadFile(filepath.Join(lib, "A", "ABBA - SOS", "song.mp3")); string(data) != "audio of ABBA - SOS" {
		t.Errorf("kept ABBA = %q, want the hq version", data)
	}
	for _, p := range []string{"duplicates/low1/Pop/ABBA - SOS/cover.png", "invalid/hq/Broken/song.txt"} {
		if _, err := os.Stat(filepath.Join(out, filepath.FromSlash(p))); err != nil {
			t.Errorf("%s: %v", p, err)
		}
	}
	if len(rep.Duplicates) != 1 || rep.Duplicates[0].Keeper != "hq:ABBA - SOS" || rep.Duplicates[0].Reason != "lower priority tier (-s #2 vs #1)" {
		t.Errorf("duplicates = %+v", rep.Duplicates)
	}
	if len(rep.Invalid) != 1 || rep.Invalid[0].Replacement != "low1:Queen - Bohemian Rhapsody" || rep.Invalid[0].Outcome != outcomeMoved {
		t.Errorf("invalid = %+v", rep.Invalid)
	}
	if !reflect.DeepEqual(rep.EmptyFolders, []string{"low1:Pop/Nothing", "low2:Empty"}) {
		t.Errorf("empty folders = %v", rep.EmptyFolders)
	}
	// The duet and the solo Elton are different songs: both in the library, not listed.
	if !reflect.DeepEqual(rep.PossibleDuplicates, [][]string{}) {
		t.Errorf("possible duplicates = %v", rep.PossibleDuplicates)
	}
	if len(rep.LeftInPlace) != 1 || rep.LeftInPlace[0].Song != "low1:Enc" || rep.LeftInPlace[0].Reason != reasonEncoding {
		t.Errorf("left in place = %+v", rep.LeftInPlace)
	}
	if _, err := os.Stat(filepath.Join(low1Dir, "Enc", "song.txt")); err != nil {
		t.Errorf("the song with an encoding problem must stay: %v", err)
	}
	if rep.Summary.ToLibrary != 5 || st.Failed != 0 {
		t.Errorf("summary = %+v, failed = %d", rep.Summary, st.Failed)
	}
	// The report's destinations are where the songs went.
	for _, dest := range []string{rep.Duplicates[0].Dest, rep.Invalid[0].Dest} {
		if _, err := os.Stat(filepath.Join(out, filepath.FromSlash(dest), "song.txt")); err != nil {
			t.Errorf("reported dest %s: %v", dest, err)
		}
	}
	if !slices.Contains(rep.Notes, noteNoMediaQuality) {
		t.Errorf("notes = %q, want the no-ffprobe note", rep.Notes)
	}
	text, _ := os.ReadFile(cfg.ReportPath(reportTxt))
	for _, want := range []string{
		"priority: on",
		"note: " + noteNoMediaQuality,
		"== Into the library (5) ==\nA/ABBA - SOS  <-  hq:ABBA - SOS  [moved]\n",
		"== Duplicates (moved to duplicates/) (1) ==\nlow1:Pop/ABBA - SOS  (ABBA - SOS)  [moved]\n    kept: hq:ABBA - SOS\n    reason: lower priority tier (-s #2 vs #1)\n",
		"hq:Broken  [moved]\n    errors: ",
		"    used instead: low1:Queen - Bohemian Rhapsody\n",
		"== Left in place (1) ==\nlow1:Enc\n    reason: " + reasonEncoding + "\n",
		"== Possible duplicates (review manually; nothing was merged) (0) ==\n",
		"== Empty folders (ignored) (2) ==\nlow1:Pop/Nothing\nlow2:Empty\n",
	} {
		if !strings.Contains(string(text), want) {
			t.Errorf("text report lacks %q:\n%s", want, text)
		}
	}
	if int64(rep.Summary.Moved) != st.Moved || rep.Summary.Failed != 0 {
		t.Errorf("report moved %d, ops moved %d", rep.Summary.Moved, st.Moved)
	}
	journal, err := os.ReadFile(cfg.JournalPath())
	if err != nil {
		t.Fatal(err)
	}
	if lines := strings.Split(strings.TrimSpace(string(journal)), "\n"); !strings.Contains(lines[0], `"op":"run"`) ||
		int64(strings.Count(string(journal), `"op":"move"`)) != st.Moved || strings.Count(string(journal), `"op":"mkdir"`) != 5 {
		t.Errorf("journal:\n%s", journal)
	}

	// A second run with the same sources moves nothing, nor does one over the library.
	if st, rep := runOrganize(t, organizeCfg(t, tiers, lib, out, false), Options{Priority: true}); st.Moved != 0 || st.Failed != 0 {
		t.Errorf("second run: %+v, %+v", st, rep.Summary)
	}
	st2, rep2 := runOrganize(t, organizeCfg(t, [][]string{{lib}}, lib, out, false), Options{})
	if st2.Moved != 0 || st2.Failed != 0 || rep2.Summary.AlreadyInPlace != 5 {
		t.Errorf("run over the library: %+v, %+v", st2, rep2.Summary)
	}

	// Undo restores the sources exactly and removes the library it created.
	if st := runUndo(t, organizeCfg(t, nil, lib, out, false)); st.Failed != 0 || st.Moved == 0 {
		t.Errorf("undo: %+v", st)
	}
	for d, want := range beforeSources {
		if got := tree(t, d); !reflect.DeepEqual(got, want) {
			t.Errorf("%s after undo:\n%s\nwant:\n%s", d, strings.Join(got, "\n"), strings.Join(want, "\n"))
		}
	}
	if got := tree(t, lib); got != nil {
		t.Errorf("library after undo: %v", got)
	}
	if got := files(t, filepath.Join(out, "duplicates")); len(got) != 0 {
		t.Errorf("duplicates after undo: %v", got)
	}
	// Nothing left to undo.
	if st := runUndo(t, organizeCfg(t, nil, "", out, false)); st.Moved != 0 || st.Failed != 0 {
		t.Errorf("second undo: %+v", st)
	}
}

// comparable strips what differs between a dry run and a real run from a report.
func comparable(r planReport) planReport {
	r.DryRun = false
	r.ToLibrary = append([]libraryEntry(nil), r.ToLibrary...)
	r.Duplicates = append([]duplicateEntry(nil), r.Duplicates...)
	r.Invalid = append([]invalidEntry(nil), r.Invalid...)
	r.Summary.Moved, r.Summary.Failed, r.Summary.EmptyCategoriesLeft = 0, 0, 0
	for i := range r.ToLibrary {
		r.ToLibrary[i].Outcome = ""
	}
	for i := range r.Duplicates {
		r.Duplicates[i].Outcome = ""
	}
	for i := range r.Invalid {
		r.Invalid[i].Outcome = ""
	}
	return r
}

func TestRunDryRunMatchesRealRun(t *testing.T) {
	noFFprobe(t)
	base := t.TempDir()
	hqDir, low1Dir, low2Dir := threeSources(t, base)
	lib := filepath.Join(base, "library")
	testutil.WriteFiles(t, lib, songFiles("misc/Cher - Believe", "Cher", "Believe"))
	tiers := [][]string{{lib}, {hqDir}, {low1Dir, low2Dir}}
	roots := []string{hqDir, low1Dir, low2Dir, lib}
	before := map[string][]string{}
	for _, d := range roots {
		before[d] = tree(t, d)
	}

	dryCfg := organizeCfg(t, tiers, lib, filepath.Join(base, "dry-out"), true)
	dst, dryRep := runOrganize(t, dryCfg, Options{Priority: true})
	for _, d := range roots {
		if got := tree(t, d); !reflect.DeepEqual(got, before[d]) {
			t.Errorf("dry run changed %s:\n%s", d, strings.Join(got, "\n"))
		}
	}
	if _, err := os.Stat(dryCfg.JournalPath()); !os.IsNotExist(err) {
		t.Errorf("dry run wrote a journal: %v", err)
	}
	if !dryRep.DryRun || dst.Moved == 0 || dst.Failed != 0 {
		t.Errorf("dry run: %+v %+v", dst, dryRep.Summary)
	}
	for _, e := range dryRep.ToLibrary {
		if e.Outcome != outcomePlanned {
			t.Errorf("dry run outcome %+v", e)
		}
	}

	realCfg := organizeCfg(t, tiers, lib, filepath.Join(base, "out"), false)
	rst, realRep := runOrganize(t, realCfg, Options{Priority: true})
	if rst.Moved != dst.Moved || rst.Failed != 0 {
		t.Errorf("real run moved %d (failed %d), dry run planned %d", rst.Moved, rst.Failed, dst.Moved)
	}
	if a, b := comparable(dryRep), comparable(realRep); !reflect.DeepEqual(a, b) {
		da, _ := fsops.MarshalReport(a)
		db, _ := fsops.MarshalReport(b)
		t.Errorf("dry-run plan differs from the real run:\n%s\nreal:\n%s", da, db)
	}
	for _, e := range realRep.ToLibrary {
		if e.Outcome != outcomeMoved {
			t.Errorf("real run outcome %+v", e)
		}
		if _, err := os.Stat(filepath.Join(lib, filepath.FromSlash(e.Target), "song.txt")); err != nil {
			t.Errorf("planned target missing: %v", err)
		}
	}
	// Cher moved from misc/ into C/; misc/ stays as an empty category.
	if realRep.Summary.EmptyCategoriesLeft == 0 {
		t.Errorf("emptied categories not counted: %+v", realRep.Summary)
	}
}

// TestRunLibraryAsSource re-buckets a library that is also a source: two songs whose
// folders carry each other's names (a cycle through a temporary name), a song folder
// named like a bucket, plus a new song from another source.
func TestRunLibraryAsSource(t *testing.T) {
	noFFprobe(t)
	base := t.TempDir()
	lib, newDir, out := filepath.Join(base, "library"), filepath.Join(base, "new"), filepath.Join(base, "out")
	testutil.WriteFiles(t, lib, merge(
		songFiles("A/ABBA - SOS", "Zed", "Song"), // wants Z/Zed - Song
		songFiles("Z/Zed - Song", "ABBA", "SOS"), // wants A/ABBA - SOS
		songFiles("B", "Blondie", "Call Me"),     // a song folder named like its bucket
		songFiles("misc/Cher - Believe", "Cher", "Believe"),
		songFiles("C/Cher - Believe (2)", "Cher", "Believe", "cover"), // the better version (a cover)
	))
	testutil.WriteFiles(t, newDir, songFiles("Bee Gees - Stayin' Alive", "Bee Gees", "Stayin' Alive"))
	beforeLib, beforeNew := tree(t, lib), tree(t, newDir)
	tiers := [][]string{{lib, newDir}}

	_, dryRep := runOrganize(t, organizeCfg(t, tiers, lib, filepath.Join(base, "dry"), true), Options{})
	if got := tree(t, lib); !reflect.DeepEqual(got, beforeLib) {
		t.Errorf("dry run changed the library")
	}
	st, rep := runOrganize(t, organizeCfg(t, tiers, lib, out, false), Options{})
	if st.Failed != 0 {
		t.Fatalf("failed operations: %+v", st)
	}
	if a, b := comparable(dryRep), comparable(rep); !reflect.DeepEqual(a, b) {
		t.Errorf("dry-run plan differs from the real run:\n%+v\n%+v", a, b)
	}
	want := map[string]string{
		"A/ABBA - SOS":               "#ARTIST:ABBA\n",
		"Z/Zed - Song":               "#ARTIST:Zed\n",
		"B/Blondie - Call Me":        "#ARTIST:Blondie\n",
		"B/Bee Gees - Stayin' Alive": "#ARTIST:Bee Gees\n",
		"C/Cher - Believe":           "#COVER:cover.png",
	}
	for dir, content := range want {
		data, err := os.ReadFile(filepath.Join(lib, filepath.FromSlash(dir), "song.txt"))
		if err != nil || !strings.Contains(string(data), content) {
			t.Errorf("%s: %q, %v", dir, data, err)
		}
	}
	if _, err := os.Stat(filepath.Join(out, "duplicates", "library", "misc", "Cher - Believe", "song.txt")); err != nil {
		t.Errorf("duplicate: %v", err)
	}
	if len(rep.Duplicates) != 1 || rep.Duplicates[0].Reason != "no cover" {
		t.Errorf("duplicates = %+v, want misc/Cher losing for no cover", rep.Duplicates)
	}
	entries, _ := os.ReadDir(lib)
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), tempPrefix) {
			t.Errorf("temporary folder left: %s", e.Name())
		}
	}

	// Idempotent: nothing moves on the finished library.
	if st, rep := runOrganize(t, organizeCfg(t, tiers, lib, out, false), Options{}); st.Moved != 0 || st.Failed != 0 || rep.Summary.AlreadyInPlace != 5 {
		t.Errorf("second run: %+v %+v", st, rep.Summary)
	}
	// Undo restores both trees exactly, including the bucket folder B that replaced the
	// song folder B. A dry-run undo plans the same moves.
	dryUndo := runUndo(t, organizeCfg(t, nil, lib, out, true))
	if st := runUndo(t, organizeCfg(t, nil, lib, out, false)); st.Failed != 0 || st != dryUndo {
		t.Errorf("undo: %+v, dry-run undo: %+v", st, dryUndo)
	}
	if got := tree(t, lib); !reflect.DeepEqual(got, beforeLib) {
		t.Errorf("library after undo:\n%s\nwant:\n%s", strings.Join(got, "\n"), strings.Join(beforeLib, "\n"))
	}
	if got := tree(t, newDir); !reflect.DeepEqual(got, beforeNew) {
		t.Errorf("new source after undo: %v", got)
	}
}

func TestUndoDryRunAndFailures(t *testing.T) {
	noFFprobe(t)
	base := t.TempDir()
	src, lib, out := filepath.Join(base, "src"), filepath.Join(base, "lib"), filepath.Join(base, "out")
	testutil.WriteFiles(t, src, merge(songFiles("a", "Abba", "SOS"), songFiles("b", "Bee Gees", "Tragedy")))
	cfg := organizeCfg(t, [][]string{{src}}, lib, out, false)
	if st, _ := runOrganize(t, cfg, Options{}); st.Moved != 2 {
		t.Fatalf("moved %d", st.Moved)
	}
	afterRun := tree(t, lib)

	// A dry-run undo plans the moves but changes nothing, not even the journal.
	journal, _ := os.ReadFile(cfg.JournalPath())
	if st := runUndo(t, organizeCfg(t, nil, lib, out, true)); st.Moved != 2 || st.Failed != 0 {
		t.Errorf("dry-run undo: %+v", st)
	}
	if got := tree(t, lib); !reflect.DeepEqual(got, afterRun) {
		t.Errorf("dry-run undo changed the library")
	}
	if j, _ := os.ReadFile(cfg.JournalPath()); string(j) != string(journal) {
		t.Errorf("dry-run undo changed the journal:\n%s", j)
	}

	// One song was moved away by hand: its undo fails, the other one is undone.
	if err := os.Rename(filepath.Join(lib, "B", "Bee Gees - Tragedy"), filepath.Join(base, "elsewhere")); err != nil {
		t.Fatal(err)
	}
	if st := runUndo(t, organizeCfg(t, nil, lib, out, false)); st.Moved != 1 || st.Failed != 1 {
		t.Errorf("undo: %+v", st)
	}
	if _, err := os.Stat(filepath.Join(src, "a", "song.txt")); err != nil {
		t.Errorf("a not restored: %v", err)
	}
	if st := runUndo(t, organizeCfg(t, nil, lib, out, false)); st.Moved != 0 || st.Failed != 0 {
		t.Errorf("the run is undone, nothing must be left to undo: %+v", st)
	}
}

func TestRunMediaQuality(t *testing.T) {
	testutil.RequireFFmpeg(t)
	base := t.TempDir()
	a, b, c := filepath.Join(base, "a"), filepath.Join(base, "b"), filepath.Join(base, "c")
	mp3 := func(out, rate string) {
		testutil.GenerateMediaAt(t, out, "-f", "lavfi", "-i", "sine=d=3", "-c:a", "libmp3lame", "-b:a", rate)
	}
	// Song 1: the same song at 96 and 256 kbit/s; the higher bitrate wins (no -priority).
	testutil.WriteFiles(t, a, songFiles("One", "Artist", "One"))
	testutil.WriteFiles(t, b, songFiles("One", "Artist", "One"))
	mp3(filepath.Join(a, "One", "song.mp3"), "96k")
	mp3(filepath.Join(b, "One", "song.mp3"), "256k")
	// Song 2: the version with a video wins over the higher bitrate.
	testutil.WriteFiles(t, b, songFiles("Two", "Artist", "Two"))
	testutil.WriteFiles(t, c, songFiles("Two", "Artist", "Two", "#VIDEO:video.mp4"))
	mp3(filepath.Join(b, "Two", "song.mp3"), "256k")
	mp3(filepath.Join(c, "Two", "song.mp3"), "96k")
	video := func(out, size string) {
		testutil.GenerateMediaAt(t, out, "-f", "lavfi", "-i", "testsrc=d=3:s="+size+":r=10", "-c:v", "libx264", "-pix_fmt", "yuv420p")
	}
	video(filepath.Join(c, "Two", "video.mp4"), "64x48")
	// Song 4: both have a video; the higher one wins.
	testutil.WriteFiles(t, a, songFiles("Four", "Artist", "Four", "#VIDEO:video.mp4"))
	testutil.WriteFiles(t, b, songFiles("Four", "Artist", "Four", "#VIDEO:video.mp4"))
	mp3(filepath.Join(a, "Four", "song.mp3"), "128k")
	mp3(filepath.Join(b, "Four", "song.mp3"), "128k")
	video(filepath.Join(a, "Four", "video.mp4"), "64x48")
	video(filepath.Join(b, "Four", "video.mp4"), "128x96")

	lib := filepath.Join(base, "lib")
	st, rep := runOrganize(t, organizeCfg(t, [][]string{{a}, {b}, {c}}, lib, filepath.Join(base, "out"), false), Options{})
	if st.Failed != 0 {
		t.Fatalf("failed: %+v", st)
	}
	got := map[string]string{}
	for _, d := range rep.Duplicates {
		got[d.Song] = d.Keeper + ": " + d.Reason
	}
	want := map[string]string{
		"a:One":  "b:One: lower audio bitrate (96 vs 256 kbit/s)",
		"b:Two":  "c:Two: no video",
		"a:Four": "b:Four: lower video height (48p vs 96p)",
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("duplicates = %v, want %v", got, want)
	}
	if len(rep.Notes) != 0 {
		t.Errorf("notes = %v", rep.Notes)
	}
	// With -priority, the first source wins although it is worse.
	testutil.WriteFiles(t, filepath.Join(base, "d"), songFiles("Three", "Artist", "Three"))
	testutil.WriteFiles(t, filepath.Join(base, "e"), songFiles("Three", "Artist", "Three"))
	mp3(filepath.Join(base, "d", "Three", "song.mp3"), "96k")
	mp3(filepath.Join(base, "e", "Three", "song.mp3"), "256k")
	_, rep = runOrganize(t, organizeCfg(t, [][]string{{filepath.Join(base, "d")}, {filepath.Join(base, "e")}}, lib, filepath.Join(base, "out2"), true), Options{Priority: true})
	if len(rep.Duplicates) != 1 || rep.Duplicates[0].Song != "e:Three" {
		t.Errorf("priority: duplicates = %+v", rep.Duplicates)
	}
}

// TestApplyMovesBackAfterFailedCycle: two library songs swap names through a temporary
// name. If the first final move fails, the second cannot reach its target either; the
// song parked under the temporary name is moved back to its own place, so nothing stays
// hidden, and the journal still lets -undo revert everything.
func TestApplyMovesBackAfterFailedCycle(t *testing.T) {
	noFFprobe(t)
	base := t.TempDir()
	lib, out := filepath.Join(base, "lib"), filepath.Join(base, "out")
	testutil.WriteFiles(t, lib, merge(songFiles("A/a", "Bee", "B"), songFiles("B/b", "Abba", "A")))
	before := tree(t, lib)
	cfg := organizeCfg(t, [][]string{{lib}}, lib, out, false)
	ops := fsops.New(false)
	cands := []Candidate{
		{Label: "lib", Rel: "A/a", Folder: filepath.Join(lib, "A", "a"), InLibrary: true},
		{Label: "lib", Rel: "B/b", Folder: filepath.Join(lib, "B", "b"), InLibrary: true, Source: 0},
	}
	plan := Plan{Decisions: []Decision{
		{Action: ActMove, Target: "B/b", Other: -1},
		{Action: ActMove, Target: "A/a", Other: -1},
	}}
	a := newApplier(cfg, ops, cands, map[int]moveOutcome{})
	a.moveTo = func(src, dst string) error {
		if src == cands[0].Folder {
			ops.Fail()
			return fmt.Errorf("injected failure")
		}
		return ops.MoveTo(src, dst)
	}
	a.apply(plan)
	if got := tree(t, lib); !reflect.DeepEqual(got, before) {
		t.Errorf("library:\n%s\nwant:\n%s", strings.Join(got, "\n"), strings.Join(before, "\n"))
	}
	if a.outcomes[0].Status != outcomeFailed || a.outcomes[1].Status != outcomeFailed {
		t.Errorf("outcomes = %+v", a.outcomes)
	}
	if st := ops.Stats(); st.Failed != 2 || st.Moved != 2 {
		t.Errorf("stats = %+v (want the injected and the blocked failure, park and move back)", st)
	}
	if st := runUndo(t, organizeCfg(t, nil, lib, out, false)); st.Failed != 0 || st.Moved != 2 {
		t.Errorf("undo: %+v", st)
	}
	if got := tree(t, lib); !reflect.DeepEqual(got, before) {
		t.Errorf("library after undo:\n%s", strings.Join(got, "\n"))
	}
}

// TestApplySkipsFinalMoveAfterFailedPark: if parking a song under the temporary name
// fails, its final move is not attempted (it would only fail again).
func TestApplySkipsFinalMoveAfterFailedPark(t *testing.T) {
	noFFprobe(t)
	base := t.TempDir()
	lib, out := filepath.Join(base, "lib"), filepath.Join(base, "out")
	testutil.WriteFiles(t, lib, merge(songFiles("A/a", "Bee", "B"), songFiles("B/b", "Abba", "A")))
	before := tree(t, lib)
	ops := fsops.New(false)
	cands := []Candidate{
		{Label: "lib", Rel: "A/a", Folder: filepath.Join(lib, "A", "a"), InLibrary: true},
		{Label: "lib", Rel: "B/b", Folder: filepath.Join(lib, "B", "b"), InLibrary: true},
	}
	plan := Plan{Decisions: []Decision{{Action: ActMove, Target: "B/b", Other: -1}, {Action: ActMove, Target: "A/a", Other: -1}}}
	a := newApplier(organizeCfg(t, [][]string{{lib}}, lib, out, false), ops, cands, map[int]moveOutcome{})
	a.moveTo = func(src, dst string) error {
		if strings.HasPrefix(filepath.Base(dst), tempPrefix) {
			ops.Fail()
			return fmt.Errorf("injected failure")
		}
		return ops.MoveTo(src, dst)
	}
	a.apply(plan)
	if st := ops.Stats(); st.Failed != 2 || st.Moved != 0 {
		t.Errorf("stats = %+v, want the injected failure and the blocked move only", st)
	}
	if got := tree(t, lib); !reflect.DeepEqual(got, before) {
		t.Errorf("library changed:\n%s", strings.Join(got, "\n"))
	}
}

// TestRunExistingLibraryNotSource: a library that is not given as a source is never
// written into where something is in the way: an exact-name target, a case variant of
// it, a file at a bucket path and a bucket folder that is a song folder itself all leave
// the keeper in place (with the reason), and nothing fails.
func TestRunExistingLibraryNotSource(t *testing.T) {
	noFFprobe(t)
	base := t.TempDir()
	src, lib := filepath.Join(base, "src"), filepath.Join(base, "lib")
	testutil.WriteFiles(t, lib, merge(
		songFiles("A/abba - sos", "ABBA", "SOS"),
		songFiles("Q/Queen - Bohemian Rhapsody", "Queen", "Bohemian Rhapsody"),
		map[string]string{"C": "a file where the bucket C would go"},
		songFiles("D", "Some", "Song"), // a song folder named like the bucket D
	))
	testutil.WriteFiles(t, src, merge(
		songFiles("ABBA - SOS", "ABBA", "SOS"),
		songFiles("Queen - Bohemian Rhapsody", "Queen", "Bohemian Rhapsody"),
		songFiles("Cher - Believe", "Cher", "Believe"),
		songFiles("Depeche Mode - Enjoy the Silence", "Depeche Mode", "Enjoy the Silence"),
		songFiles("Elton John - Song", "Elton John", "Song"),
	))
	beforeLib := tree(t, lib)
	tiers := [][]string{{src}}
	_, dryRep := runOrganize(t, organizeCfg(t, tiers, lib, filepath.Join(base, "dry"), true), Options{})
	st, rep := runOrganize(t, organizeCfg(t, tiers, lib, filepath.Join(base, "out"), false), Options{})
	if st.Failed != 0 || st.Moved != 1 {
		t.Errorf("stats = %+v, want only Elton moved", st)
	}
	if a, b := comparable(dryRep), comparable(rep); !reflect.DeepEqual(a, b) {
		t.Errorf("dry-run plan differs from the real run:\n%+v\n%+v", a, b)
	}
	addTo := "; add the library as a source (-s) to deduplicate against it"
	want := map[string]string{
		"src:ABBA - SOS":                "target A/ABBA - SOS exists in the library as A/abba - sos" + addTo,
		"src:Queen - Bohemian Rhapsody": reasonTargetExists,
		"src:Cher - Believe":            "target C/Cher - Believe exists in the library as C" + addTo,
		"src:Depeche Mode - Enjoy the Silence": "target D/Depeche Mode - Enjoy the Silence cannot be used: the bucket folder D is a song folder " +
			"(it contains the song txt song.txt); move it away or add the library as a source (-s)",
	}
	got := map[string]string{}
	for _, e := range rep.LeftInPlace {
		got[e.Song] = e.Reason
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("left in place:\n%v\nwant:\n%v", got, want)
	}
	if _, err := os.Stat(filepath.Join(lib, "E", "Elton John - Song", "song.txt")); err != nil {
		t.Errorf("Elton: %v", err)
	}
	var libNow []string
	for _, e := range tree(t, lib) {
		if !strings.HasPrefix(e, "E/") {
			libNow = append(libNow, e)
		}
	}
	if !reflect.DeepEqual(libNow, beforeLib) {
		t.Errorf("library changed:\n%s\nwant:\n%s", strings.Join(libNow, "\n"), strings.Join(beforeLib, "\n"))
	}
}

// TestRunLibrarySourceWithoutSongs: a library given as a source that holds no song (only
// an empty folder and an empty category) is still a source, so a target taken by such a
// folder gets reasonNotASong, never the hint to add the library as a source, in the
// plan of Run and in Review.Replan (the web UI's exclusions).
func TestRunLibrarySourceWithoutSongs(t *testing.T) {
	noFFprobe(t)
	base := t.TempDir()
	src, lib := filepath.Join(base, "src"), filepath.Join(base, "lib")
	for _, d := range []string{filepath.Join(lib, "A", "ABBA - SOS"), filepath.Join(lib, "Pop", "Empty")} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	testutil.WriteFiles(t, src, songFiles("ABBA - SOS", "ABBA", "SOS"))
	want := fmt.Sprintf(reasonNotASong, "A/ABBA - SOS")
	var replanned Plan
	opts := Options{Review: func(rv *Review) error {
		replanned = rv.Replan(nil)
		return nil
	}}
	_, rep := runOrganize(t, organizeCfg(t, [][]string{{lib}, {src}}, lib, filepath.Join(base, "out"), false), opts)
	if len(rep.LeftInPlace) != 1 || rep.LeftInPlace[0].Song != "src:ABBA - SOS" || rep.LeftInPlace[0].Reason != want {
		t.Errorf("left in place = %+v, want src:ABBA - SOS with %q", rep.LeftInPlace, want)
	}
	if len(replanned.Decisions) != 1 || replanned.Decisions[0].Reason != want {
		t.Errorf("Review.Replan = %+v, want reason %q", replanned.Decisions, want)
	}
	if _, err := os.Stat(filepath.Join(src, "ABBA - SOS", "song.txt")); err != nil {
		t.Errorf("the song must stay in its source: %v", err)
	}
}

// TestRunUnreadableBucket: a bucket folder that cannot be read is counted as a failure,
// and the songs bound for it are left in place with that reason.
func TestRunUnreadableBucket(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root can read any folder")
	}
	noFFprobe(t)
	base := t.TempDir()
	src, lib := filepath.Join(base, "src"), filepath.Join(base, "lib")
	testutil.WriteFiles(t, src, songFiles("ABBA - SOS", "ABBA", "SOS"))
	if err := os.MkdirAll(filepath.Join(lib, "A"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(filepath.Join(lib, "A"), 0); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.Chmod(filepath.Join(lib, "A"), 0o755) })
	st, rep := runOrganize(t, organizeCfg(t, [][]string{{src}}, lib, filepath.Join(base, "out"), false), Options{})
	if st.Failed != 1 || st.Moved != 0 {
		t.Errorf("stats = %+v, want one failure and no move", st)
	}
	if len(rep.LeftInPlace) != 1 || rep.LeftInPlace[0].Reason != "target A/ABBA - SOS cannot be used: the bucket folder A cannot be read" {
		t.Errorf("left in place = %+v", rep.LeftInPlace)
	}
}

// TestRunBucketSongStays: the library is a source and holds a broken song folder named
// like the bucket A. If moving it to invalid/ fails, no keeper may be moved into it.
func TestRunBucketSongStays(t *testing.T) {
	noFFprobe(t)
	base := t.TempDir()
	src, lib, out := filepath.Join(base, "src"), filepath.Join(base, "library"), filepath.Join(base, "out")
	testutil.WriteFiles(t, lib, map[string]string{"A/song.txt": "#ARTIST:Some\n#TITLE:Song\n#MP3:song.mp3\n: 0 2 0 la\nE\n", "A/song.mp3": "x"}) // no #BPM
	testutil.WriteFiles(t, src, songFiles("Abba - Y", "Abba", "Y"))
	beforeLib, beforeSrc := tree(t, lib), tree(t, src)
	cfg := organizeCfg(t, [][]string{{lib}, {src}}, lib, out, false)
	testutil.WriteFiles(t, out, map[string]string{"invalid/library": "a file: the move to invalid/ fails"})
	st, rep := runOrganize(t, cfg, Options{})
	if st.Failed != 2 || st.Moved != 0 {
		t.Errorf("stats = %+v, want the failed invalid move and the refused library move", st)
	}
	if got := tree(t, lib); !reflect.DeepEqual(got, beforeLib) {
		t.Errorf("library:\n%s\nwant:\n%s", strings.Join(got, "\n"), strings.Join(beforeLib, "\n"))
	}
	if got := tree(t, src); !reflect.DeepEqual(got, beforeSrc) {
		t.Errorf("source changed: %v", got)
	}
	if len(rep.ToLibrary) != 1 || rep.ToLibrary[0].Outcome != outcomeFailed {
		t.Errorf("to library = %+v", rep.ToLibrary)
	}
}

// TestApplyBucketSongParkFails: a song folder named like its own bucket is parked under a
// temporary name to make room; if that fails, another keeper bound for the same bucket is
// not moved into the song folder.
func TestApplyBucketSongParkFails(t *testing.T) {
	noFFprobe(t)
	base := t.TempDir()
	lib, src, out := filepath.Join(base, "lib"), filepath.Join(base, "src"), filepath.Join(base, "out")
	testutil.WriteFiles(t, lib, songFiles("A", "Abba", "SOS"))
	testutil.WriteFiles(t, src, songFiles("x", "Abba", "Y"))
	before := tree(t, lib)
	ops := fsops.New(false)
	cands := []Candidate{
		{Label: "lib", Rel: "A", Folder: filepath.Join(lib, "A"), InLibrary: true},
		{Label: "src", Rel: "x", Folder: filepath.Join(src, "x"), Source: 1},
	}
	plan := Plan{Decisions: []Decision{{Action: ActMove, Target: "A/Abba - SOS", Other: -1}, {Action: ActMove, Target: "A/Abba - Y", Other: -1}}}
	a := newApplier(organizeCfg(t, [][]string{{lib}, {src}}, lib, out, false), ops, cands, map[int]moveOutcome{})
	a.moveTo = func(src, dst string) error {
		if strings.HasPrefix(filepath.Base(dst), tempPrefix) {
			ops.Fail()
			return fmt.Errorf("injected failure")
		}
		return ops.MoveTo(src, dst)
	}
	a.apply(plan)
	if got := tree(t, lib); !reflect.DeepEqual(got, before) {
		t.Errorf("library:\n%s\nwant:\n%s", strings.Join(got, "\n"), strings.Join(before, "\n"))
	}
	if a.outcomes[0].Status != outcomeFailed || a.outcomes[1].Status != outcomeFailed {
		t.Errorf("outcomes = %+v", a.outcomes)
	}
}

// failingJournal makes the journal append fail for the lines fail selects (after the
// first failure every append fails, like a full disk).
func failingJournal(ops *fsops.Ops, j *journal, fail func(line string) bool) {
	broken := false
	j.write = func(path string, line []byte) error {
		if broken || fail(string(line)) {
			broken = true
			ops.Fail()
			return fmt.Errorf("injected journal failure")
		}
		return ops.AppendJournal(path, line)
	}
}

// TestApplyJournalFailureAfterPark: if the park of a song under the temporary name cannot
// be journaled, the song is moved back right away instead of staying hidden there.
func TestApplyJournalFailureAfterPark(t *testing.T) {
	noFFprobe(t)
	base := t.TempDir()
	lib, out := filepath.Join(base, "lib"), filepath.Join(base, "out")
	testutil.WriteFiles(t, lib, merge(songFiles("A/a", "Bee", "B"), songFiles("B/b", "Abba", "A")))
	before := tree(t, lib)
	cfg := organizeCfg(t, [][]string{{lib}}, lib, out, false)
	ops := fsops.New(false)
	cands := []Candidate{
		{Label: "lib", Rel: "A/a", Folder: filepath.Join(lib, "A", "a"), InLibrary: true},
		{Label: "lib", Rel: "B/b", Folder: filepath.Join(lib, "B", "b"), InLibrary: true},
	}
	plan := Plan{Decisions: []Decision{{Action: ActMove, Target: "B/b", Other: -1}, {Action: ActMove, Target: "A/a", Other: -1}}}
	a := newApplier(cfg, ops, cands, map[int]moveOutcome{})
	failingJournal(ops, a.j, func(line string) bool { return strings.Contains(line, tempPrefix) })
	a.apply(plan)
	if got := tree(t, lib); !reflect.DeepEqual(got, before) {
		t.Errorf("library:\n%s\nwant:\n%s", strings.Join(got, "\n"), strings.Join(before, "\n"))
	}
	if a.outcomes[0].Status != outcomeSkipped || a.outcomes[1].Status != outcomeSkipped {
		t.Errorf("outcomes = %+v", a.outcomes)
	}
	if !a.j.failed {
		t.Error("the journal failure was not noticed")
	}
}

// TestApplyJournalFailureAfterSwapMove: the park is journaled, but the move into the
// parked song's place cannot be: the parked song cannot go back (its place is taken), so
// it goes to its planned target instead of staying hidden.
func TestApplyJournalFailureAfterSwapMove(t *testing.T) {
	noFFprobe(t)
	base := t.TempDir()
	lib, out := filepath.Join(base, "lib"), filepath.Join(base, "out")
	testutil.WriteFiles(t, lib, merge(songFiles("A/a", "Bee", "B"), songFiles("B/b", "Abba", "A")))
	cfg := organizeCfg(t, [][]string{{lib}}, lib, out, false)
	ops := fsops.New(false)
	cands := []Candidate{
		{Label: "lib", Rel: "A/a", Folder: filepath.Join(lib, "A", "a"), InLibrary: true},
		{Label: "lib", Rel: "B/b", Folder: filepath.Join(lib, "B", "b"), InLibrary: true},
	}
	plan := Plan{Decisions: []Decision{{Action: ActMove, Target: "B/b", Other: -1}, {Action: ActMove, Target: "A/a", Other: -1}}}
	a := newApplier(cfg, ops, cands, map[int]moveOutcome{})
	moves := 0
	failingJournal(ops, a.j, func(line string) bool {
		if strings.Contains(line, `"op":"move"`) {
			moves++
		}
		return moves == 2
	})
	a.apply(plan)
	entries, _ := os.ReadDir(lib)
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), tempPrefix) {
			t.Errorf("song left hidden at %s", e.Name())
		}
	}
	for dir, artist := range map[string]string{"A/a": "Abba", "B/b": "Bee"} {
		data, err := os.ReadFile(filepath.Join(lib, filepath.FromSlash(dir), "song.txt"))
		if err != nil || !strings.Contains(string(data), "#ARTIST:"+artist+"\n") {
			t.Errorf("%s: %q, %v", dir, data, err)
		}
	}
}

// TestApplyStopsAfterJournalFailure: once a journal append fails, no further song is
// moved (it could not be undone).
func TestApplyStopsAfterJournalFailure(t *testing.T) {
	noFFprobe(t)
	base := t.TempDir()
	src, lib, out := filepath.Join(base, "src"), filepath.Join(base, "lib"), filepath.Join(base, "out")
	testutil.WriteFiles(t, src, merge(songFiles("a", "A", "A"), songFiles("b", "B", "B"), songFiles("c", "C", "C")))
	ops := fsops.New(false)
	var cands []Candidate
	var ds []Decision
	for _, n := range []string{"a", "b", "c"} {
		cands = append(cands, Candidate{Label: "src", Rel: n, Folder: filepath.Join(src, n)})
		// One bucket: no mkdir entry (which would fail too) is needed after the first move.
		ds = append(ds, Decision{Action: ActMove, Target: "A/" + n, Other: -1})
	}
	a := newApplier(organizeCfg(t, [][]string{{src}}, lib, out, false), ops, cands, map[int]moveOutcome{})
	failingJournal(ops, a.j, func(line string) bool { return strings.Contains(line, `"op":"move"`) })
	a.apply(Plan{Decisions: ds})
	if st := ops.Stats(); st.Moved != 1 {
		t.Errorf("stats = %+v, want only the first move", st)
	}
	if a.outcomes[0].Status != outcomeMoved || a.outcomes[1].Status != outcomeSkipped || a.outcomes[2].Status != outcomeSkipped {
		t.Errorf("outcomes = %+v", a.outcomes)
	}
	for _, n := range []string{"b", "c"} {
		if _, err := os.Stat(filepath.Join(src, n, "song.txt")); err != nil {
			t.Errorf("%s was moved: %v", n, err)
		}
	}
}

// TestRunJournalUnwritable: Run reports a journal that cannot be written as an error and
// moves nothing.
func TestRunJournalUnwritable(t *testing.T) {
	noFFprobe(t)
	base := t.TempDir()
	src, lib, out := filepath.Join(base, "src"), filepath.Join(base, "lib"), filepath.Join(base, "out")
	testutil.WriteFiles(t, src, songFiles("a", "Abba", "SOS"))
	before := tree(t, src)
	cfg := organizeCfg(t, [][]string{{src}}, lib, out, false)
	if err := os.MkdirAll(cfg.JournalPath(), 0o755); err != nil { // a folder: appends fail
		t.Fatal(err)
	}
	ops := fsops.New(false)
	if err := Run(cfg, ops, Options{}); err == nil || !strings.Contains(err.Error(), "journal") {
		t.Errorf("Run = %v, want a journal error", err)
	}
	if st := ops.Stats(); st.Moved != 0 || st.Failed == 0 {
		t.Errorf("stats = %+v", st)
	}
	if got := tree(t, src); !reflect.DeepEqual(got, before) {
		t.Errorf("source changed: %v", got)
	}
}

// TestRunQualityWithoutFFprobe: without ffprobe the keeper is chosen by the song
// warnings, cover, background and #VERSION read from the real song folders.
func TestRunQualityWithoutFFprobe(t *testing.T) {
	noFFprobe(t)
	base := t.TempDir()
	var tier []string
	variants := [][]string{
		{"warning", "cover", "background", "#VERSION:1.0.0"},
		{"background", "#VERSION:1.0.0"},
		{"cover", "#VERSION:1.0.0"},
		{"cover", "background"},
		{"cover", "background", "#VERSION:1.0.0"},
	}
	for i, extra := range variants {
		dir := filepath.Join(base, fmt.Sprintf("s%d", i))
		testutil.WriteFiles(t, dir, songFiles("Song", "Artist", "Song", extra...))
		tier = append(tier, dir)
	}
	_, rep := runOrganize(t, organizeCfg(t, [][]string{tier}, filepath.Join(base, "lib"), filepath.Join(base, "out"), true), Options{})
	got := map[string]string{}
	for _, d := range rep.Duplicates {
		got[d.Song] = d.Keeper + ": " + d.Reason
	}
	want := map[string]string{
		"s0:Song": "s4:Song: more song warnings (1 vs 0)",
		"s1:Song": "s4:Song: no cover",
		"s2:Song": "s4:Song: no background",
		"s3:Song": "s4:Song: no #VERSION",
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("duplicates = %v, want %v", got, want)
	}
}

// TestRunVideoWithoutFFprobe: without ffprobe the video cannot be probed, but a version
// whose #VIDEO names an existing file still beats one without a video, whatever the
// source order.
func TestRunVideoWithoutFFprobe(t *testing.T) {
	noFFprobe(t)
	base := t.TempDir()
	without, with := filepath.Join(base, "a"), filepath.Join(base, "b")
	testutil.WriteFiles(t, without, songFiles("Song", "Artist", "Song"))
	testutil.WriteFiles(t, with, songFiles("Song", "Artist", "Song", "video"))
	_, rep := runOrganize(t, organizeCfg(t, [][]string{{without, with}}, filepath.Join(base, "lib"), filepath.Join(base, "out"), true), Options{})
	if len(rep.Duplicates) != 1 || rep.Duplicates[0].Song != "a:Song" || rep.Duplicates[0].Keeper != "b:Song" || rep.Duplicates[0].Reason != "no video" {
		t.Errorf("duplicates = %+v, want a:Song losing to b:Song with \"no video\"", rep.Duplicates)
	}
}

// TestRunIgnoreThe: Run passes IgnoreThe to the plan.
func TestRunIgnoreThe(t *testing.T) {
	noFFprobe(t)
	base := t.TempDir()
	src := filepath.Join(base, "src")
	testutil.WriteFiles(t, src, songFiles("x", "The Beatles", "Help!"))
	_, rep := runOrganize(t, organizeCfg(t, [][]string{{src}}, filepath.Join(base, "lib"), filepath.Join(base, "out"), true), Options{IgnoreThe: true})
	if len(rep.ToLibrary) != 1 || rep.ToLibrary[0].Target != "B/The Beatles - Help!" || !rep.IgnoreThe {
		t.Errorf("to library = %+v, ignoreThe = %v", rep.ToLibrary, rep.IgnoreThe)
	}
}

// TestApplyUnparkFallbackFails: a parked song that can neither go back (its place is
// taken by the swap's other song) nor to its planned target stays at the temporary name.
// The log tells the user how to get it out: -undo if the journal works (a stop), by hand
// if the journal failed.
func TestApplyUnparkFallbackFails(t *testing.T) {
	for _, tc := range []struct {
		name        string
		journalFail bool
		want        string
	}{
		{"stopped", false, "the song stays at the temporary name, -undo moves it back"},
		{"journal failed", true, "the song stays at the temporary name, move it by hand"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			noFFprobe(t)
			base := t.TempDir()
			lib, out := filepath.Join(base, "lib"), filepath.Join(base, "out")
			testutil.WriteFiles(t, lib, merge(songFiles("A/a", "Bee", "B"), songFiles("B/b", "Abba", "A")))
			before := tree(t, lib)
			cfg := organizeCfg(t, [][]string{{lib}}, lib, out, false)
			ops := fsops.New(false)
			cands := []Candidate{
				{Label: "lib", Rel: "A/a", Folder: filepath.Join(lib, "A", "a"), InLibrary: true},
				{Label: "lib", Rel: "B/b", Folder: filepath.Join(lib, "B", "b"), InLibrary: true},
			}
			plan := Plan{Decisions: []Decision{{Action: ActMove, Target: "B/b", Other: -1}, {Action: ActMove, Target: "A/a", Other: -1}}}
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			a := newApplier(cfg, ops, cands, map[int]moveOutcome{})
			a.ctx = ctx
			if tc.journalFail {
				// The park is journaled, the swap's middle move is not.
				failingJournal(ops, a.j, func(line string) bool { return strings.Contains(line, `"move"`) && !strings.Contains(line, tempPrefix) })
			}
			a.moveTo = func(src, dst string) error {
				if strings.HasPrefix(filepath.Base(src), tempPrefix) {
					ops.Fail()
					return fmt.Errorf("injected failure")
				}
				if !tc.journalFail && !strings.HasPrefix(filepath.Base(dst), tempPrefix) {
					defer cancel() // the swap's middle move completes, then the run stops
				}
				return ops.MoveTo(src, dst)
			}
			hook := testutil.CaptureLogs(t)
			a.apply(plan)
			var found bool
			for _, m := range messages(hook, log.ErrorLevel) {
				found = found || strings.Contains(m, tc.want)
			}
			if !found {
				t.Errorf("no error with %q in:\n%s", tc.want, strings.Join(messages(hook, log.ErrorLevel), "\n"))
			}
			if tc.journalFail {
				return
			}
			if st := runUndo(t, organizeCfg(t, nil, lib, out, false)); st.Failed != 0 {
				t.Errorf("undo: %+v", st)
			}
			if got := tree(t, lib); !reflect.DeepEqual(got, before) {
				t.Errorf("library after undo:\n%s\nwant:\n%s", strings.Join(got, "\n"), strings.Join(before, "\n"))
			}
		})
	}
}
