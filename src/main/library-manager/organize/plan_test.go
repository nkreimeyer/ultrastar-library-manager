package organize

import (
	"fmt"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

// source describes a test source: its label and tier.
type source struct {
	label string
	tier  int
}

var (
	hq   = source{"hq", 0}
	low1 = source{"low1", 1}
	low2 = source{"low2", 1}
	lib  = source{"library", 2}
)

// sourceIndex is the flattened order of the test sources.
var sourceIndex = map[string]int{"hq": 0, "low1": 1, "low2": 2, "library": 3}

func song(s source, rel, artist, title string, q Quality) Candidate {
	return Candidate{
		Source: sourceIndex[s.label], Tier: s.tier, Label: s.label, Rel: rel,
		Folder: filepath.Join("/src", s.label, rel), InLibrary: s.label == "library",
		Artist: artist, Title: title, Duet: IsDuetTitle(title), Quality: q,
	}
}

func brokenSong(s source, rel, artist, title string, codes ...string) Candidate {
	c := song(s, rel, artist, title, Quality{})
	c.Broken, c.Errors = true, codes
	return c
}

// summary renders the decisions as "<ref>: <action> <target/other> (<reason>)".
func summary(cands []Candidate, p Plan) []string {
	var out []string
	for i, d := range p.Decisions {
		s := cands[i].Ref() + ": " + string(d.Action)
		if d.Target != "" {
			s += " " + d.Target
		}
		if d.Other >= 0 {
			s += " <" + cands[d.Other].Ref() + ">"
		}
		if d.Reason != "" {
			s += " (" + d.Reason + ")"
		}
		out = append(out, s)
	}
	return out
}

func TestMakePlanKeeper(t *testing.T) {
	good := Quality{HasVideo: true, VideoHeight: 1080, AudioBitrate: 320000, HasCover: true, HasBackground: true}
	poor := Quality{AudioBitrate: 128000}
	tests := []struct {
		name     string
		priority bool
		cands    []Candidate
		want     []string
	}{
		{"priority: hq beats a better low version", true, []Candidate{
			song(hq, "ABBA - SOS", "ABBA", "SOS", poor),
			song(low1, "x/ABBA - SOS", "ABBA", "SOS", good),
		}, []string{
			"hq:ABBA - SOS: library A/ABBA - SOS",
			"low1:x/ABBA - SOS: duplicate <hq:ABBA - SOS> (lower priority tier (-s #2 vs #1))",
		}},
		{"priority: inside a tier quality wins", true, []Candidate{
			song(low1, "s", "ABBA", "SOS", poor),
			song(low2, "s", "ABBA", "SOS", good),
		}, []string{
			"low1:s: duplicate <low2:s> (no video)",
			"low2:s: library A/ABBA - SOS",
		}},
		{"no priority: quality wins across tiers", false, []Candidate{
			song(hq, "s", "ABBA", "SOS", poor),
			song(low1, "s", "ABBA", "SOS", good),
		}, []string{
			"hq:s: duplicate <low1:s> (no video)",
			"low1:s: library A/ABBA - SOS",
		}},
		{"priority: a broken hq song loses to a working low version", true, []Candidate{
			brokenSong(hq, "s", "ABBA", "SOS", "bad-bpm", "no-notes"),
			song(low1, "s", "ABBA", "SOS", poor),
		}, []string{
			"hq:s: invalid <low1:s> (errors: bad-bpm, no-notes)",
			"low1:s: library A/ABBA - SOS",
		}},
		{"broken without replacement", true, []Candidate{
			brokenSong(hq, "s", "ABBA", "SOS", "missing-file"),
		}, []string{"hq:s: invalid (errors: missing-file)"}},
		{"encoding problem and unreadable are left in place", true, []Candidate{
			func() Candidate {
				c := song(hq, "enc", "ABBA", "SOS", good)
				c.EncodingProblem, c.Broken = true, true
				return c
			}(),
			func() Candidate { c := song(hq, "unr", "", "", Quality{}); c.Unreadable = true; return c }(),
			song(low1, "s", "ABBA", "SOS", poor),
		}, []string{
			"hq:enc: left (" + reasonEncoding + ")",
			"hq:unr: left (" + reasonUnreadable + ")",
			"low1:s: library A/ABBA - SOS",
		}},
		{"duet and solo are variants, both kept", true, []Candidate{
			song(hq, "solo", "Elton John", "Don't Go Breaking My Heart", good),
			func() Candidate {
				c := song(low1, "duet", "Elton John", "Don't Go Breaking My Heart", poor)
				c.Duet = true // P1/P2 markers
				return c
			}(),
			song(low2, "duet2", "Elton John", "Don't Go Breaking My Heart [DUET]", good),
		}, []string{
			"hq:solo: library E/Elton John - Don't Go Breaking My Heart",
			"low1:duet: duplicate <low2:duet2> (no video)",
			"low2:duet2: library E/Elton John - Don't Go Breaking My Heart [DUET]",
		}},
		{"marker duet gets a [DUET] suffix", true, []Candidate{
			func() Candidate { c := song(low1, "duet", "Elton John", "Song", poor); c.Duet = true; return c }(),
		}, []string{"low1:duet: library E/Elton John - Song [DUET]"}},
		{"identity key: feat, case and diacritics", false, []Candidate{
			song(hq, "a", "Beyoncé", "Halo", poor),
			song(low1, "b", "beyonce feat. Someone", "HALO (Official Video)", good),
		}, []string{
			"hq:a: duplicate <low1:b> (no video)",
			"low1:b: library B/beyonce feat. Someone - HALO (Official Video)",
		}},
		{"bitrate tolerance is measured against the best of the group", true, []Candidate{
			song(low1, "a", "X", "Y", Quality{AudioBitrate: 300000, HasCover: true}),
			song(low1, "b", "X", "Y", Quality{AudioBitrate: 330000}),
			song(low2, "c", "X", "Y", Quality{AudioBitrate: 363000}),
		}, []string{
			"low1:a: duplicate <low1:b> (lower audio bitrate (300 vs 363 kbit/s))",
			"low1:b: library X/X - Y",
			"low2:c: duplicate <low1:b> (same quality; the version from the earlier source is kept)",
		}},
		{"priority: an invisible soft hyphen does not make a different song", true, []Candidate{
			song(hq, "x", "Oasis", "Wonder\u00adwall", poor),
			song(low1, "y", "Oasis", "Wonderwall", good),
		}, []string{
			"hq:x: library O/Oasis - Wonderwall",
			"low1:y: duplicate <hq:x> (lower priority tier (-s #2 vs #1))",
		}},
		{"different kana are different songs", false, []Candidate{
			song(hq, "x", "Kenshi", "カゲ", poor),
			song(low1, "y", "Kenshi", "カケ", good),
		}, []string{
			"hq:x: library K/Kenshi - カゲ",
			"low1:y: library K/Kenshi - カケ",
		}},
		{"tie: earlier source, then path", false, []Candidate{
			song(low2, "a", "X", "Y", poor),
			song(low1, "b", "X", "Y", poor),
			song(low1, "a", "X", "Y", poor),
		}, []string{
			"low2:a: duplicate <low1:a> (same quality; the version from the earlier source is kept)",
			"low1:b: duplicate <low1:a> (same quality; the version with the first path is kept)",
			"low1:a: library X/X - Y",
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := summary(tt.cands, MakePlan(tt.cands, PlanOptions{Priority: tt.priority}))
			if !reflect.DeepEqual(got, tt.want) {
				t.Errorf("plan:\n%s\nwant:\n%s", strings.Join(got, "\n"), strings.Join(tt.want, "\n"))
			}
		})
	}
}

func TestMakePlanTargets(t *testing.T) {
	long := strings.Repeat("Long ", 40) // FolderName cuts it: the names collide
	occupied := func(m map[string]string) func(string) (string, string, bool) {
		return func(rel string) (string, string, bool) {
			for k, v := range m {
				if NameKey(k) == NameKey(rel) {
					occ, note, _ := strings.Cut(v, "|")
					return occ, note, true
				}
			}
			return "", "", false
		}
	}
	tests := []struct {
		name      string
		ignoreThe bool
		occupied  map[string]string // target -> occupant (library-relative)[|note]
		cands     []Candidate
		want      []string
	}{
		{"collisions get (2), (3) in key order", false, nil, []Candidate{
			song(hq, "c", "Artist", long+"c", Quality{}),
			song(hq, "a", "Artist", long+"a", Quality{}),
			song(hq, "b", "Artist", long+"b", Quality{}),
		}, []string{
			"hq:c: library A/" + FolderName("Artist", long) + " (3)",
			"hq:a: library A/" + FolderName("Artist", long),
			"hq:b: library A/" + FolderName("Artist", long) + " (2)",
		}},
		{"collisions: keepers in place keep their numbered name", false, nil, []Candidate{
			song(lib, "A/"+FolderName("Artist", long)+" (2)", "Artist", long+"a", Quality{}),
			song(hq, "b", "Artist", long+"b", Quality{}),
		}, []string{
			"library:A/" + FolderName("Artist", long) + " (2): in-place A/" + FolderName("Artist", long) + " (2)",
			"hq:b: library A/" + FolderName("Artist", long),
		}},
		{"a numbered name skips another song's plain name", false, nil, []Candidate{
			song(hq, "a", "A", `Rock"n"Roll`, Quality{}),     // key a|rock n roll, name A - Rock'n'Roll
			song(hq, "b", "A", "Rock'n'Roll", Quality{}),     // key a|rocknroll, same name
			song(hq, "c", "A", "Rock'n'Roll (2)", Quality{}), // its plain name is A - Rock'n'Roll (2)
		}, []string{
			"hq:a: library A/A - Rock'n'Roll",
			"hq:b: library A/A - Rock'n'Roll (3)",
			"hq:c: library A/A - Rock'n'Roll (2)",
		}},
		{"a keeper in place at another song's plain name is renumbered", false, nil, []Candidate{
			song(lib, "A/A - Rock'n'Roll (2)", "A", `Rock"n"Roll`, Quality{}),
			song(hq, "b", "A", "Rock'n'Roll", Quality{}),
			song(hq, "c", "A", "Rock'n'Roll (2)", Quality{}),
		}, []string{
			"library:A/A - Rock'n'Roll (2): library A/A - Rock'n'Roll",
			"hq:b: library A/A - Rock'n'Roll (3)",
			"hq:c: library A/A - Rock'n'Roll (2)",
		}},
		{"a single song with a numbered name loses the number", false, nil, []Candidate{
			song(lib, "A/ABBA - SOS (2)", "ABBA", "SOS", Quality{}),
		}, []string{"library:A/ABBA - SOS (2): library A/ABBA - SOS"}},
		{"already in place (also case-insensitively)", false, map[string]string{"A/ABBA - SOS": "A/abba - sos"}, []Candidate{
			song(lib, "A/abba - sos", "ABBA", "SOS", Quality{}),
		}, []string{"library:A/abba - sos: in-place A/abba - sos"}},
		{"re-bucketing within the library", true, nil, []Candidate{
			song(lib, "old/The Who - Tommy", "The Who", "Tommy", Quality{}),
		}, []string{"library:old/The Who - Tommy: library W/The Who - Tommy"}},
		{"existing target that is no candidate: left in place", false, map[string]string{"A/ABBA - SOS": "A/ABBA - SOS"}, []Candidate{
			song(hq, "s", "ABBA", "SOS", Quality{}),
			song(low1, "s", "ABBA", "SOS", Quality{}),
		}, []string{
			"hq:s: left (" + reasonTargetExists + ")",
			"low1:s: duplicate <hq:s> (same quality; the version from the earlier source is kept)",
		}},
		{"bucket that is a file: left in place", false, map[string]string{"A/ABBA - SOS": "A"}, []Candidate{
			song(hq, "s", "ABBA", "SOS", Quality{}),
		}, []string{"hq:s: left (target A/ABBA - SOS exists in the library as A; add the library as a source (-s) to deduplicate against it)"}},
		{"bucket that cannot take songs: left in place with the note", false, map[string]string{"A/ABBA - SOS": "A|the bucket folder A is a song folder"}, []Candidate{
			song(hq, "s", "ABBA", "SOS", Quality{}),
		}, []string{"hq:s: left (target A/ABBA - SOS cannot be used: the bucket folder A is a song folder)"}},
		{"target taken by a library song that moves away", false, map[string]string{"A/ABBA - SOS": "A/ABBA - SOS"}, []Candidate{
			song(lib, "A/ABBA - SOS", "Other", "Song", Quality{}),
			song(hq, "s", "ABBA", "SOS", Quality{}),
		}, []string{
			"library:A/ABBA - SOS: library O/Other - Song",
			"hq:s: library A/ABBA - SOS",
		}},
		{"target taken by a library song that stays", false, map[string]string{"A/ABBA - SOS": "A/ABBA - SOS"}, []Candidate{
			func() Candidate {
				c := song(lib, "A/ABBA - SOS", "ABBA", "SOS", Quality{})
				c.EncodingProblem = true
				return c
			}(),
			song(hq, "s", "ABBA", "SOS", Quality{}),
		}, []string{
			"library:A/ABBA - SOS: left (" + reasonEncoding + ")",
			"hq:s: left (target is occupied by library:A/ABBA - SOS, which stays in place)",
		}},
		{"chain: the keeper that would free the target stays", false, map[string]string{"A/ABBA - SOS": "A/ABBA - SOS", "B/B - C": "B/B - C"}, []Candidate{
			song(lib, "A/ABBA - SOS", "B", "C", Quality{}), // wants B/B - C, which is no candidate
			song(hq, "s", "ABBA", "SOS", Quality{}),
		}, []string{
			"library:A/ABBA - SOS: left (" + reasonTargetExists + ")",
			"hq:s: left (target is occupied by library:A/ABBA - SOS, which stays in place)",
		}},
		{"song folder named like the bucket", false, nil, []Candidate{
			func() Candidate { c := song(lib, "A", "Zed", "Song", Quality{}); return c }(),
			func() Candidate { c := song(lib, "B", "B", "B", Quality{}); c.EncodingProblem = true; return c }(),
			song(hq, "a", "Abba", "SOS", Quality{}),
			song(hq, "b", "Bee Gees", "Stayin' Alive", Quality{}),
		}, []string{
			"library:A: library Z/Zed - Song",
			"library:B: left (" + reasonEncoding + ")",
			"hq:a: library A/Abba - SOS",
			"hq:b: left (bucket folder B is the song folder library:B, which stays in place)",
		}},
		{"ignore the", true, nil, []Candidate{
			song(hq, "s", "The Beatles", "Help!", Quality{}),
		}, []string{"hq:s: library B/The Beatles - Help!"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			opts := PlanOptions{IgnoreThe: tt.ignoreThe}
			if tt.occupied != nil {
				opts.Occupied = occupied(tt.occupied)
			}
			got := summary(tt.cands, MakePlan(tt.cands, opts))
			if !reflect.DeepEqual(got, tt.want) {
				t.Errorf("plan:\n%s\nwant:\n%s", strings.Join(got, "\n"), strings.Join(tt.want, "\n"))
			}
		})
	}
}

func TestMakePlanDeterministic(t *testing.T) {
	cands := []Candidate{
		song(low1, "b", "X", "Y", Quality{}),
		song(low1, "a", "X", "Y", Quality{}),
		song(hq, "z", "Q", strings.Repeat("Long ", 40)+"1", Quality{}),
		song(hq, "y", "Q", strings.Repeat("Long ", 40)+"2", Quality{}),
	}
	want := summary(cands, MakePlan(cands, PlanOptions{}))
	reversed := []Candidate{cands[3], cands[2], cands[1], cands[0]}
	got := summary(reversed, MakePlan(reversed, PlanOptions{}))
	for i, j := range []int{3, 2, 1, 0} {
		if got[i] != want[j] {
			t.Errorf("input order changed the plan: %q vs %q", got[i], want[j])
		}
	}
}

func TestPossibleDuplicates(t *testing.T) {
	cands := []Candidate{
		song(hq, "a", "Nirvana", "Smells Like Teen Spirit", Quality{}),
		song(hq, "b", "Nirvana", "Smells Like Teen Spirit (Live)", Quality{}),
		song(low1, "c", "Nirvana", "Smells Like Teen Spirit (Live)", Quality{}), // duplicate of b
		song(hq, "d", "Nirvana", "Smells Like Teen Spirit [DUET]", Quality{}),
		song(hq, "e", "Other", "Song", Quality{}),
		brokenSong(hq, "f", "Nirvana", "Smells Like Teen Spirit (Acoustic)", "no-notes"),
	}
	p := MakePlan(cands, PlanOptions{})
	// d is the duet version: a different song, never listed with the solo ones.
	if want := [][]int{{0, 1}}; !reflect.DeepEqual(p.PossibleDuplicates, want) {
		t.Errorf("possible duplicates = %v, want %v", p.PossibleDuplicates, want)
	}
	if d := p.Decisions[2]; d.Action != ActDuplicate || d.Other != 1 {
		t.Errorf("c = %+v, want a duplicate of b", d)
	}
}

func TestBetter(t *testing.T) {
	base := Quality{SongWarnings: 1, HasVideo: true, VideoHeight: 720, VideoBitrate: 2000000, AudioBitrate: 192000, HasCover: true, HasBackground: true, HasVersion: true}
	with := func(f func(*Quality)) Quality { q := base; f(&q); return q }
	tests := []struct {
		name     string
		priority bool
		a, b     Quality // a has the earlier source, unless decided otherwise
		aWins    bool
		reason   string
	}{
		{"fewer song warnings", true, base, with(func(q *Quality) { q.SongWarnings = 0 }), false, "more song warnings (1 vs 0)"},
		{"video", true, base, with(func(q *Quality) { q.HasVideo, q.VideoHeight = false, 0 }), true, "no video"},
		{"video height", true, base, with(func(q *Quality) { q.VideoHeight = 1080 }), false, "lower video height (720p vs 1080p)"},
		{"bitrate", true, base, with(func(q *Quality) { q.AudioBitrate = 320000 }), false, "lower audio bitrate (192 vs 320 kbit/s)"},
		{"bitrate within 10 % is equal", true, base, with(func(q *Quality) { q.AudioBitrate = 210000; q.HasCover = false }), true, "no cover"},
		{"unknown bitrate loses", true, with(func(q *Quality) { q.AudioBitrate = 0 }), base, false, "unknown audio bitrate (vs 192 kbit/s)"},
		{"both unknown are equal", true, with(func(q *Quality) { q.AudioBitrate = 0 }), with(func(q *Quality) { q.AudioBitrate = 0; q.HasBackground = false }), true, "no background"},
		{"version", true, base, with(func(q *Quality) { q.HasVersion = false }), true, "no #VERSION"},
		{"video bitrate above fixvideos' limit loses before the height", true, base,
			with(func(q *Quality) { q.VideoHeight, q.VideoBitrate = 1080, 5000000 }), true,
			"video bitrate too high (5000 kbit/s, above the 3000 kbit/s of fixvideos)"},
		{"video bitrate at the limit is fine", true, base,
			with(func(q *Quality) { q.VideoHeight, q.VideoBitrate = 1080, 3000000 }), false, "lower video height (720p vs 1080p)"},
		{"unknown video bitrate is fine", true, base,
			with(func(q *Quality) { q.VideoHeight, q.VideoBitrate = 1080, 0 }), false, "lower video height (720p vs 1080p)"},
		{"both too high: the height decides", true, with(func(q *Quality) { q.VideoBitrate = 4000000 }),
			with(func(q *Quality) { q.VideoHeight, q.VideoBitrate = 1080, 6000000 }), false, "lower video height (720p vs 1080p)"},
		{"no video loses against a high-bitrate video", true, with(func(q *Quality) { q.HasVideo, q.VideoHeight, q.VideoBitrate = false, 0, 0 }),
			with(func(q *Quality) { q.VideoBitrate = 9000000 }), false, "no video"},
		{"the height counts up to 1080p", true, with(func(q *Quality) { q.VideoHeight = 1080 }),
			with(func(q *Quality) { q.VideoHeight = 2160; q.HasCover = false }), true, "no cover"},
		{"tie", true, base, base, true, "same quality; the version from the earlier source is kept"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			a := &Candidate{Source: 0, Tier: 1, Rel: "a", Quality: tt.a}
			b := &Candidate{Source: 1, Tier: 1, Rel: "b", Quality: tt.b}
			wins, reason := better(a, b, tt.priority)
			if wins != tt.aWins || reason != tt.reason {
				t.Errorf("better = %v, %q, want %v, %q", wins, reason, tt.aWins, tt.reason)
			}
			if back, _ := better(b, a, tt.priority); back == wins {
				t.Errorf("better(b, a) = %v, want the opposite", back)
			}
		})
	}
	// The tier only counts with priority.
	a := &Candidate{Source: 1, Tier: 1, Quality: with(func(q *Quality) { q.SongWarnings = 0 })}
	b := &Candidate{Source: 0, Tier: 0, Quality: base}
	if wins, _ := better(a, b, true); wins {
		t.Error("with priority the lower tier must win")
	}
	if wins, _ := better(a, b, false); !wins {
		t.Error("without priority quality must win")
	}
}

func TestOrderMoves(t *testing.T) {
	temp := func(n int) string { return fmt.Sprintf("/lib/.tmp-%d", n) }
	render := func(steps []step) []string {
		var out []string
		for _, s := range steps {
			out = append(out, fmt.Sprintf("%d:%s->%s", s.Cand, s.Src, s.Dst))
		}
		return out
	}
	tests := []struct {
		name  string
		moves []libMove
		want  []string
	}{
		{"independent moves in target order", []libMove{{0, "/s/b", "/lib/B/b"}, {1, "/s/a", "/lib/A/a"}},
			[]string{"1:/s/a->/lib/A/a", "0:/s/b->/lib/B/b"}},
		{"target freed first", []libMove{{0, "/s/x", "/lib/A/a"}, {1, "/lib/A/a", "/lib/Z/z"}},
			[]string{"1:/lib/A/a->/lib/Z/z", "0:/s/x->/lib/A/a"}},
		{"case-insensitive target", []libMove{{0, "/s/x", "/lib/A/abc"}, {1, "/lib/A/ABC", "/lib/Z/z"}},
			[]string{"1:/lib/A/ABC->/lib/Z/z", "0:/s/x->/lib/A/abc"}},
		{"swap needs a temporary name", []libMove{{0, "/lib/A/a", "/lib/B/b"}, {1, "/lib/B/b", "/lib/A/a"}},
			[]string{"1:/lib/B/b->/lib/.tmp-0", "0:/lib/A/a->/lib/B/b", "1:/lib/.tmp-0->/lib/A/a"}},
		{"waiting move outside a cycle: only a cycle member is parked", []libMove{
			{0, "/s/x", "/lib/A/a"}, {1, "/lib/A", "/lib/B/b"}, {2, "/lib/B/b", "/lib/A/x"}},
			[]string{"2:/lib/B/b->/lib/.tmp-0", "1:/lib/A->/lib/B/b", "0:/s/x->/lib/A/a", "2:/lib/.tmp-0->/lib/A/x"}},
		{"song folder named like the bucket of its own target", []libMove{{0, "/lib/A", "/lib/A/Abba - SOS"}},
			[]string{"0:/lib/A->/lib/.tmp-0", "0:/lib/.tmp-0->/lib/A/Abba - SOS"}},
		{"song folder named like a bucket moves first", []libMove{{0, "/s/x", "/lib/A/Abba - SOS"}, {1, "/lib/A", "/lib/Z/Zed - Song"}},
			[]string{"1:/lib/A->/lib/Z/Zed - Song", "0:/s/x->/lib/A/Abba - SOS"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			for i := range tt.moves {
				tt.moves[i].Src, tt.moves[i].Dst = filepath.FromSlash(tt.moves[i].Src), filepath.FromSlash(tt.moves[i].Dst)
			}
			got := render(orderMoves(tt.moves, func(n int) string { return filepath.FromSlash(temp(n)) }))
			for i := range tt.want {
				tt.want[i] = filepath.FromSlash(tt.want[i])
			}
			if !reflect.DeepEqual(got, tt.want) {
				t.Errorf("steps:\n%s\nwant:\n%s", strings.Join(got, "\n"), strings.Join(tt.want, "\n"))
			}
		})
	}
}

// TestChooseKeeperOrderIndependent: the keeper and the reasons do not depend on the order
// of the group, also when the bitrate tolerance chains (300 ~ 330 ~ 363, but 300 !~ 363).
func TestChooseKeeperOrderIndependent(t *testing.T) {
	cands := []Candidate{
		song(low1, "a", "X", "Y", Quality{AudioBitrate: 300000, HasCover: true}),
		song(low1, "b", "X", "Y", Quality{AudioBitrate: 330000}),
		song(low2, "c", "X", "Y", Quality{AudioBitrate: 363000}),
		song(low2, "d", "X", "Y", Quality{AudioBitrate: 0, HasCover: true, HasVersion: true}),
	}
	var want map[string]string
	perms := [][]int{{0, 1, 2, 3}, {3, 2, 1, 0}, {2, 0, 3, 1}, {1, 3, 0, 2}, {2, 1, 0, 3}}
	for _, perm := range perms {
		cs := make([]*Candidate, len(perm))
		for j, i := range perm {
			cs[j] = &cands[i]
		}
		k, reasons := chooseKeeper(cs, false)
		got := map[string]string{"keeper": cs[k].Rel}
		for j, c := range cs {
			if j != k {
				got[c.Rel] = reasons[j]
			}
		}
		if want == nil {
			want = got
			if got["keeper"] != "b" || got["d"] != "unknown audio bitrate (vs 363 kbit/s)" {
				t.Errorf("keeper choice = %v", got)
			}
			continue
		}
		if !reflect.DeepEqual(got, want) {
			t.Errorf("order %v: %v, want %v", perm, got, want)
		}
	}
}
