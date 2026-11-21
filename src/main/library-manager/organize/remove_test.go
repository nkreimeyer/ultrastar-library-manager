package organize

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/nkreimeyer/library-manager/config"
	"github.com/nkreimeyer/library-manager/fsops"
	"github.com/nkreimeyer/library-manager/internal/testutil"
)

func mustPattern(t *testing.T, kind, text string) Pattern {
	t.Helper()
	p, err := ParsePattern(kind, text, "test")
	if err != nil {
		t.Fatal(err)
	}
	return p
}

func TestParsePattern(t *testing.T) {
	for _, c := range []struct{ kind, text, err string }{
		{PatternArtist, "  ", `test: the artist "" has no letters or digits`},
		{PatternArtist, "...", `test: the artist "..." has no letters or digits`},
		{PatternSong, "ABBA", `test: the song "ABBA" must be written as "Artist - Title"`},
		{PatternSong, "ABBA - !", `test: the song "ABBA - !" needs an artist and a title with letters or digits`},
		{PatternSong, "? - SOS", `test: the song "? - SOS" needs an artist and a title with letters or digits`},
		{"title", "SOS", `test: unknown pattern kind "title"`},
	} {
		if _, err := ParsePattern(c.kind, c.text, "test"); err == nil || err.Error() != c.err {
			t.Errorf("ParsePattern(%q, %q) error = %v, want %q", c.kind, c.text, err, c.err)
		}
	}
	if p := mustPattern(t, PatternSong, " ABBA - SOS "); p.String() != "song: ABBA - SOS" || p.Source != "test" {
		t.Errorf("pattern = %+v", p)
	}
}

func TestParsePatternFile(t *testing.T) {
	in := "\ufeff# songs to drop\n\nABBA - SOS\nElton John\n  artist: Queen - The Band  \nSONG: AC/DC - T.N.T.\nRe:Zero\n#artist: commented\n"
	ps, err := ParsePatternFile(strings.NewReader(in), "list.txt")
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, p := range ps {
		got = append(got, p.String()+" @"+p.Source)
	}
	want := []string{
		"song: ABBA - SOS @list.txt:3",
		"artist: Elton John @list.txt:4",
		"artist: Queen - The Band @list.txt:5", // the prefix wins over " - "
		"song: AC/DC - T.N.T. @list.txt:6",
		"artist: Re:Zero @list.txt:7", // not a known prefix
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("patterns:\n%s\nwant:\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
	if _, err := ParsePatternFile(strings.NewReader("ABBA\nsong: SOS\n"), "l"); err == nil || !strings.HasPrefix(err.Error(), "l:2: ") {
		t.Errorf("error = %v, want one for line 2", err)
	}
	if ps, err := ParsePatternFile(strings.NewReader("# nothing\n\n"), "l"); err != nil || len(ps) != 0 {
		t.Errorf("empty file: %v, %v", ps, err)
	}
}

func TestPatternMatches(t *testing.T) {
	type song struct {
		artist, title string
		duet          bool
	}
	for _, c := range []struct {
		kind, text string
		yes, no    []song
	}{
		{PatternArtist, "Elton John", []song{
			{"Elton John", "Your Song", false},
			{"ELTON JOHN", "Sacrifice", false},
			{"Elton John & George Michael", "Don't Let The Sun", true},
			{"George Michael x Elton John", "Song", false},
			{"Elton John feat. Kiki Dee", "Song", false}, // the main artist
			{"Elton John &amp; Kiki Dee", "Song", false},
		}, []song{
			{"George Michael feat. Elton John", "Song", false}, // only featured
			{"Elton", "Song", false},
			{"Elton Johnson", "Song", false},
			{"", "Song", false},
		}},
		{PatternArtist, "The Cure", []song{{"Cure", "A Forest", false}, {"Cure (The)", "Lullaby", false}}, nil},
		{PatternArtist, "Simon & Garfunkel", []song{{"Garfunkel and Simon", "x", false}}, []song{{"Simon", "x", false}}},
		{PatternSong, "ABBA - SOS", []song{
			{"ABBA", "SOS", false},
			{"Abba", "S.O.S.", false},
			{"ABBA", "SOS (Remastered)", false},
			{"ABBA", "SOS", true},
			{"ABBA", "SOS [DUET]", false},
		}, []song{
			{"ABBA", "SOS (Live)", false},
			{"ABBA", "Waterloo", false},
			{"ABBA & Cher", "SOS", false},
			{"", "", false},
		}},
		{PatternSong, "ABBA - SOS [DUET]", []song{{"ABBA", "SOS", true}, {"ABBA", "SOS (Duet)", false}}, []song{{"ABBA", "SOS", false}}},
	} {
		p := mustPattern(t, c.kind, c.text)
		for _, s := range c.yes {
			if !p.matches(s.artist, s.title, s.duet) {
				t.Errorf("%s does not match %+v", p, s)
			}
		}
		for _, s := range c.no {
			if p.matches(s.artist, s.title, s.duet) {
				t.Errorf("%s matches %+v", p, s)
			}
		}
	}
	if (Pattern{Kind: "x"}).matches("a", "b", false) {
		t.Error("an unknown kind matches")
	}
}

func TestPlanRemove(t *testing.T) {
	songs := []removeSong{
		{Rel: "A/ABBA - SOS", Artist: "ABBA", Title: "SOS"},
		{Rel: "A/ABBA - Waterloo", Artist: "ABBA", Title: "Waterloo"},
		{Rel: "C/Cher - Believe", Artist: "Cher", Title: "Believe", EncodingProblem: true},
		{Rel: "X/no txt", NoTags: true, Artist: "ABBA", Title: "SOS"},
		{Rel: "Q/Queen - Song", Artist: "Queen", Title: "Song"},
		{Rel: "Q/Queen - Other", Artist: "Queen", Title: "Other", EncodingProblem: true}, // no match: nothing to say
	}
	patterns := []Pattern{mustPattern(t, PatternSong, "ABBA - SOS"), mustPattern(t, PatternArtist, "ABBA"), mustPattern(t, PatternArtist, "Cher"), mustPattern(t, PatternArtist, "Nobody")}
	ds, counts := planRemove(songs, patterns)
	want := []removeDecision{{Pattern: 0}, {Pattern: 1}, {Pattern: 2, Left: true, Reason: reasonRemoveEncoding}, {Pattern: -1}, {Pattern: -1}, {Pattern: -1}}
	if !reflect.DeepEqual(ds, want) || !reflect.DeepEqual(counts, []int{1, 1, 1, 0}) {
		t.Errorf("decisions = %+v, counts %v", ds, counts)
	}
}

func TestRunRemove(t *testing.T) {
	noFFprobe(t)
	base := t.TempDir()
	lib := filepath.Join(base, "library")
	testutil.WriteFiles(t, lib, merge(
		songFiles("A/ABBA - SOS", "ABBA", "SOS"),
		songFiles("A/ABBA - Waterloo", "ABBA", "Waterloo"),
		songFiles("E/Elton John & Kiki Dee - Don't Go", "Elton John & Kiki Dee", "Don't Go Breaking My Heart", "duet"),
		songFiles("E/Elton John - Your Song", "Elton John", "Your Song"),
		songFiles("Q/Queen - Bohemian Rhapsody", "Queen", "Bohemian Rhapsody"),
		songFiles("D/Duo - Song", "Duo", "Song", "duet"),       // P1/P2: matched by a duet pattern
		songFiles("D/Duo - Song (solo)", "Duo", "Song (Solo)"), // another title
		songFiles("C/Copy - Song", "Copycat_dupl1", "Song"),    // the copy mark is ignored
		map[string]string{ // no #ARTIST: taken from the folder name
			"E/Elton John - Rocket Man/song.txt": "#TITLE:Rocket Man\n#MP3:a.mp3\n" + testutil.NotesTxt("r"),
			"E/Elton John - Rocket Man/a.mp3":    "a",
			"E/Untagged/song.txt":                "#MP3:a.mp3\n" + testutil.NotesTxt("u"), // no obvious name: never matched
			"E/Untagged/a.mp3":                   "a",
			"M/Muse - Uprising/song.txt":         "#ARTIST:Muse\n#MP3:a.mp3\n" + testutil.NotesTxt("m"), // no #TITLE
			"M/Muse - Uprising/a.mp3":            "a",
		},
		map[string]string{
			// CP1252 with an ambiguous byte: an encoding problem, so it stays.
			"Q/Queen - Broken/song.txt": "#ARTIST:Queen\n#TITLE:Br\x81ken\n#MP3:a.mp3\n" + testutil.NotesTxt("x"),
			"Q/Queen - Broken/a.mp3":    "a",
			"Z/notxt/a.mp3":             "a",
		},
	))
	before := tree(t, lib)
	patterns := []Pattern{mustPattern(t, PatternArtist, "Elton John"), mustPattern(t, PatternSong, "abba - s.o.s."), mustPattern(t, PatternArtist, "Queen"), mustPattern(t, PatternArtist, "Nobody"),
		mustPattern(t, PatternSong, "Duo - Song [DUET]"), mustPattern(t, PatternArtist, "Copycat"), mustPattern(t, PatternSong, "Muse - Uprising")}

	dry := prepared(t)(config.NewDedupe(lib, filepath.Join(base, "dry"), true))
	if err := Remove(dry, fsops.New(dry.DryRun), patterns); err != nil {
		t.Fatal(err)
	}
	if got := tree(t, lib); !reflect.DeepEqual(got, before) {
		t.Errorf("dry run changed the library:\n%s", strings.Join(got, "\n"))
	}
	cfg := prepared(t)(config.NewDedupe(lib, filepath.Join(base, "out"), false))
	ops := fsops.New(false)
	if err := Remove(cfg, ops, patterns); err != nil {
		t.Fatal(err)
	}
	if st := ops.Stats(); st.Moved != 8 || st.Failed != 0 {
		t.Errorf("stats = %+v, want 8 moves", st)
	}
	var dirs []string
	for _, f := range files(t, lib) {
		if d := filepath.ToSlash(filepath.Dir(f)); len(dirs) == 0 || dirs[len(dirs)-1] != d {
			dirs = append(dirs, d)
		}
	}
	if want := []string{"A/ABBA - Waterloo", "D/Duo - Song (solo)", "E/Untagged", "Q/Queen - Broken", "Z/notxt"}; !reflect.DeepEqual(dirs, want) {
		t.Errorf("library = %q, want %q", dirs, want)
	}
	if _, err := os.Stat(filepath.Join(cfg.RemovedDir(), "E", "Elton John - Your Song", "song.txt")); err != nil {
		t.Errorf("removed song not in removed/: %v", err)
	}
	read := func(dir, name string) string {
		data, err := os.ReadFile(filepath.Join(dir, name))
		if err != nil {
			t.Fatal(err)
		}
		return string(data)
	}
	txt := read(cfg.OutDir, removeReportTxt)
	for _, want := range []string{
		"remove plan (real run)\n",
		"  artist: Elton John  (test; 3 songs)\n",
		"summary: 13 songs: 8 removed, 1 matching songs left in place; 1 patterns without a match; 8 moved, 0 failed\n",
		"E/Elton John - Rocket Man  (Elton John - Rocket Man)  [moved]\n    matched: artist: Elton John (tags from the folder name)\n",
		"M/Muse - Uprising  (Muse - Uprising)  [moved]\n    matched: song: Muse - Uprising (tags from the folder name)\n",
		"== Removed (moved to removed/) (8) ==\nA/ABBA - SOS  (ABBA - SOS)  [moved]\n    matched: song: abba - s.o.s.\n",
		"C/Copy - Song  (Copycat - Song)  [moved]\n    matched: artist: Copycat\n",
		"D/Duo - Song  (Duo - Song)  [moved]\n    matched: song: Duo - Song [DUET]\n",
		"E/Elton John & Kiki Dee - Don't Go  (Elton John & Kiki Dee - Don't Go Breaking My Heart)  [moved]\n    matched: artist: Elton John\n",
		"== Left in place (1) ==\nQ/Queen - Broken  (Queen - Br",
		"    reason: " + reasonRemoveEncoding + "\n",
		"== Patterns without a match (1) ==\nartist: Nobody\n",
	} {
		if !strings.Contains(txt, want) {
			t.Errorf("report lacks %q:\n%s", want, txt)
		}
	}
	// The dry-run plan is the real plan.
	if d, r := strings.Replace(read(dry.OutDir, removeReportJSON), `"dryRun": true`, `"dryRun": false`, 1), read(cfg.OutDir, removeReportJSON); strings.ReplaceAll(strings.ReplaceAll(d, `"planned"`, `"moved"`), dry.OutDir, "") != strings.ReplaceAll(r, cfg.OutDir, "") {
		t.Errorf("dry-run report differs:\n%s\nreal:\n%s", d, r)
	}

	// Idempotent: nothing left to remove.
	again := prepared(t)(config.NewDedupe(lib, filepath.Join(base, "out2"), false))
	ops2 := fsops.New(false)
	if err := Remove(again, ops2, patterns); err != nil || ops2.Stats().Moved != 0 {
		t.Errorf("second run: %v, %+v", err, ops2.Stats())
	}
	if ust := runUndo(t, cfg); ust.Failed != 0 {
		t.Errorf("undo failed %d", ust.Failed)
	}
	if got := tree(t, lib); !reflect.DeepEqual(got, before) {
		t.Errorf("undo did not restore the library:\n%s", strings.Join(got, "\n"))
	}
	if err := Remove(cfg, ops, nil); err == nil {
		t.Error("Remove without patterns works")
	}
}
