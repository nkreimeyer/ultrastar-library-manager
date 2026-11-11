package organize

import (
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

// A keeper from another source that cannot reach the library must not cost the library
// its own copy of the song.
func TestMakePlanLibraryLoserStaysWhenKeeperLeft(t *testing.T) {
	cands := []Candidate{
		song(lib, "old/ABBA - SOS", "ABBA", "SOS", Quality{AudioBitrate: 128000}),
		song(hq, "s", "ABBA", "SOS", Quality{HasVideo: true, VideoHeight: 1080}),
		song(low1, "s", "ABBA", "SOS", Quality{}),
	}
	occ := func(rel string) (string, string, bool) {
		if NameKey(rel) == NameKey("A/ABBA - SOS") {
			return "A", "the bucket folder A holds the media file x.mp3", true
		}
		return "", "", false
	}
	p := MakePlan(cands, PlanOptions{Occupied: occ})
	want := []string{
		"library:old/ABBA - SOS: left (the better version hq:s cannot be moved into the library, so this copy stays)",
		"hq:s: left (target A/ABBA - SOS cannot be used: the bucket folder A holds the media file x.mp3)",
		"low1:s: duplicate <hq:s> (no video)",
	}
	if got := summary(cands, p); !reflect.DeepEqual(got, want) {
		t.Errorf("got\n  %s\nwant\n  %s", strings.Join(got, "\n  "), strings.Join(want, "\n  "))
	}
}

// A library copy that stays because its keeper is left can block the target of another
// song, which must then be left too (resolveOccupied runs again).
func TestMakePlanLibraryLoserStayingBlocksTarget(t *testing.T) {
	cands := []Candidate{
		// The library copy of ABBA - SOS sits at the target of the song "Bee - Gees".
		song(lib, "B/Bee - Gees", "ABBA", "SOS", Quality{}),
		song(hq, "s", "ABBA", "SOS", Quality{HasVideo: true}),
		song(hq, "t", "Bee", "Gees", Quality{}),
	}
	occ := func(rel string) (string, string, bool) {
		switch NameKey(rel) {
		case NameKey("A/ABBA - SOS"):
			return "A", "the bucket folder A cannot be read", true
		case NameKey("B/Bee - Gees"):
			return "B/Bee - Gees", "", true
		}
		return "", "", false
	}
	p := MakePlan(cands, PlanOptions{Occupied: occ})
	want := []string{
		"library:B/Bee - Gees: left (the better version hq:s cannot be moved into the library, so this copy stays)",
		"hq:s: left (target A/ABBA - SOS cannot be used: the bucket folder A cannot be read)",
		"hq:t: left (target is occupied by library:B/Bee - Gees, which stays in place)",
	}
	if got := summary(cands, p); !reflect.DeepEqual(got, want) {
		t.Errorf("got\n  %s\nwant\n  %s", strings.Join(got, "\n  "), strings.Join(want, "\n  "))
	}
}

// A feat part after a bracket group or inside a dash qualifier is dropped like one before.
func TestSongKeyFeatAfterQualifier(t *testing.T) {
	cases := []struct {
		name string
		a, b songID
	}{
		{"feat after bracket", songID{"Artist", "Song (Live) feat. X", false}, songID{"Artist", "Song feat. X (Live)", false}},
		{"feat after duet bracket", songID{"Artist", "Song [DUET] feat. X", false}, songID{"Artist", "Song feat. X [DUET]", false}},
		{"feat after radio edit", songID{"Artist", "Song (Radio Edit) feat. X", false}, songID{"Artist", "Song feat. X (Radio Edit)", false}},
		{"feat after noise", songID{"Artist", "Song (Remastered) feat. X", false}, songID{"Artist", "Song", false}},
		{"feat in dash qualifier", songID{"Artist", "Song - Live feat. X", false}, songID{"Artist", "Song (Live)", false}},
		{"feat in bracket qualifier", songID{"Artist", "Song (Live feat. X)", false}, songID{"Artist", "Song (Live)", false}},
		{"artist feat after bracket", songID{"Artist (DE) feat. X", "Song", false}, songID{"Artist (DE)", "Song", false}},
	}
	for _, c := range cases {
		ka, kb := SongKey(c.a.artist, c.a.title, c.a.duet), SongKey(c.b.artist, c.b.title, c.b.duet)
		if ka != kb {
			t.Errorf("%s: SongKey %q != %q", c.name, ka, kb)
		}
		la, lb := LooseKey(c.a.artist, c.a.title), LooseKey(c.b.artist, c.b.title)
		if la != lb {
			t.Errorf("%s: LooseKey %q != %q", c.name, la, lb)
		}
	}
	for _, c := range []struct{ artist, title, want string }{
		{"Artist", "Song (Live) feat. X", "artist|song live"},
		{"Artist", "Song - Live feat. X", "artist|song live"},
		{"Artist", "Song (Live) feat. X Remix", "artist|song live x remix"}, // guarded
		{"Artist", "Song (Live) ft. X", "artist|song live ft x"},            // strict: ft. stays in plain text
		{"Artist", "Song (Live) Feat", "artist|song live feat"},             // nothing follows the feat word
		{"Artist", "(Remastered) feat. X", "artist|feat x"},                 // no earlier words
		{"Artist (DE) feat. X", "Song", "artist de|song"},
		{"Artist (DE) ft. X", "Song", "artist de|song"},
		{"Artist", "Song (Live at Ft. Lauderdale)", "artist|song live at ft lauderdale"}, // strict: ft. stays in qualifiers too
		{"Artist", "Song - Live at Ft. Worth", "artist|song live at ft worth"},
	} {
		if got := SongKey(c.artist, c.title, false); got != c.want {
			t.Errorf("SongKey(%q, %q) = %q, want %q", c.artist, c.title, got, c.want)
		}
	}
	// "ft." inside a qualifier may be a place name: different live recordings stay apart.
	for _, p := range [][2]string{
		{"Song (Live at Ft. Lauderdale)", "Song (Live at Ft. Worth)"},
		{"Song - Live at Ft. Lauderdale", "Song - Live at Ft. Worth"},
	} {
		if a, b := SongKey("Artist", p[0], false), SongKey("Artist", p[1], false); a == b {
			t.Errorf("SongKey(%q) == SongKey(%q) = %q, want different keys", p[0], p[1], a)
		}
	}
}

func TestMakePlanEmptySongKeyOrderIndependent(t *testing.T) {
	if k := SongKey("!!!", "Song", false); k != "" {
		t.Fatalf("SongKey = %q, want empty", k)
	}
	c := []Candidate{song(hq, "a", "!!!", "Song", Quality{}), song(hq, "b", "!!!", "Song", Quality{})}
	p1 := MakePlan(c, PlanOptions{})
	p2 := MakePlan([]Candidate{c[1], c[0]}, PlanOptions{})
	if p1.Decisions[0].Target != "#/!!! - Song" || p2.Decisions[1].Target != "#/!!! - Song" {
		t.Errorf("hq:a -> %q and %q, want #/!!! - Song in both input orders", p1.Decisions[0].Target, p2.Decisions[1].Target)
	}
	if p1.Decisions[1].Target != "#/!!! - Song (2)" || p2.Decisions[0].Target != "#/!!! - Song (2)" {
		t.Errorf("hq:b -> %q and %q, want (2)", p1.Decisions[1].Target, p2.Decisions[0].Target)
	}
}

func TestMakePlanEmptySongKeyPathOrder(t *testing.T) {
	var c []Candidate
	for i := 0; i <= 10; i++ {
		c = append(c, song(hq, fmt.Sprintf("%02d", i), "!!!", "Song", Quality{}))
	}
	p := MakePlan(c, PlanOptions{})
	for i, d := range p.Decisions {
		want := "#/!!! - Song"
		if i > 0 {
			want = fmt.Sprintf("#/!!! - Song (%d)", i+1)
		}
		if d.Action != ActMove || d.Target != want {
			t.Errorf("hq:%02d -> %s %q, want library %q", i, d.Action, d.Target, want)
		}
	}
}

// Songs with an empty SongKey are never merged: different titles keep their own names.
func TestMakePlanEmptySongKeyNeverMerged(t *testing.T) {
	c := []Candidate{song(hq, "a", "!!!", "Song A", Quality{}), song(low1, "b", "!!!", "Song B", Quality{})}
	got := summary(c, MakePlan(c, PlanOptions{}))
	want := []string{"hq:a: library #/!!! - Song A", "low1:b: library #/!!! - Song B"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got %q, want %q", got, want)
	}
}

// The report names the folder a duplicate really went to, also when MoveInto added " (2)".
func TestReportDestFollowsCollisionSuffix(t *testing.T) {
	noFFprobe(t)
	for _, dry := range []bool{true, false} {
		base := t.TempDir()
		a, b := filepath.Join(base, "a"), filepath.Join(base, "b")
		lib, out := filepath.Join(base, "library"), filepath.Join(base, "out")
		writeFiles(t, a, songFiles("Song", "Artist", "Song", "cover"))
		writeFiles(t, b, songFiles("Song", "Artist", "Song"))
		writeFiles(t, out, map[string]string{"duplicates/b/Song/old.txt": "x"})
		cfg := organizeCfg(t, [][]string{{a, b}}, lib, out, dry)
		_, rep := runOrganize(t, cfg, Options{})
		if len(rep.Duplicates) != 1 {
			t.Fatalf("dry=%v: duplicates = %+v", dry, rep.Duplicates)
		}
		if got := rep.Duplicates[0].Dest; got != "duplicates/b/Song (2)" {
			t.Errorf("dry=%v: dest = %q, want duplicates/b/Song (2)", dry, got)
		}
		if !dry {
			if _, err := os.Stat(filepath.Join(out, "duplicates", "b", "Song (2)", "song.txt")); err != nil {
				t.Errorf("song not at Song (2): %v", err)
			}
		}
	}
}

// An append after a crash-truncated journal line starts a new line, so the "undone" mark
// of an undo is kept and a second undo does not revert the same run again.
func TestUndoAfterTruncatedJournalLine(t *testing.T) {
	noFFprobe(t)
	base := t.TempDir()
	src := filepath.Join(base, "src")
	writeFiles(t, src, songFiles("ABBA - SOS", "ABBA", "SOS"))
	lib, out := filepath.Join(base, "library"), filepath.Join(base, "out")
	cfg := organizeCfg(t, [][]string{{src}}, lib, out, false)
	if st, _ := runOrganize(t, cfg, Options{}); st.Moved == 0 || st.Failed != 0 {
		t.Fatalf("run: %+v", st)
	}
	f, err := os.OpenFile(cfg.JournalPath(), os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.WriteString(`{"op":"move","run":"x","src":"/s`); err != nil {
		t.Fatal(err)
	}
	f.Close()
	if st := runUndo(t, cfg); st.Failed != 0 {
		t.Fatalf("first undo: %+v", st)
	}
	if _, err := os.Stat(filepath.Join(src, "ABBA - SOS", "song.txt")); err != nil {
		t.Fatalf("first undo did not restore: %v", err)
	}
	if st := runUndo(t, cfg); st.Failed != 0 || st.Moved != 0 {
		j, _ := os.ReadFile(cfg.JournalPath())
		t.Errorf("second undo reverted the same run again: %+v\njournal:\n%s", st, j)
	}
}
