package organize

import (
	"reflect"
	"strings"
	"testing"

	"github.com/nkreimeyer/library-manager/check"
	"github.com/nkreimeyer/library-manager/config"
)

// libFirst is a library song as add and dedupe see it: the library is the first source.
func libFirst(rel, artist, title string, q Quality) Candidate {
	c := song(lib, rel, artist, title, q)
	c.Source, c.Tier = -1, 0
	return c
}

func marked(c Candidate) Candidate { c.MarkedCopy = true; return c }

func brokenLib(rel, artist, title string, codes ...string) Candidate {
	c := libFirst(rel, artist, title, Quality{})
	c.Broken, c.Errors = true, codes
	return c
}

// MakePlan with LibraryInPlace (dedupe, add): library keepers stay where they are, only
// keepers from other sources move, broken library songs only go when replaced.
func TestMakePlanLibraryInPlace(t *testing.T) {
	good := Quality{HasVideo: true, VideoHeight: 1080, AudioBitrate: 320000, HasCover: true}
	poor := Quality{AudioBitrate: 128000}
	nonSong := func(rel string) (string, string, bool) { return rel, "", rel == "A/ABBA - SOS" }
	tests := []struct {
		name     string
		occupied func(string) (string, string, bool)
		cands    []Candidate
		want     []string
	}{
		{"dedupe: the keeper stays at its path, the others go to duplicates/", nil, []Candidate{
			libFirst("Pop/abba sos", "ABBA", "SOS", poor),
			libFirst("Rock/ABBA - SOS", "ABBA", "SOS", good),
			libFirst("Songs/X - Y", "X", "Y", poor),
		}, []string{
			"library:Pop/abba sos: duplicate <library:Rock/ABBA - SOS> (no video)",
			"library:Rock/ABBA - SOS: in-place Rock/ABBA - SOS",
			"library:Songs/X - Y: in-place Songs/X - Y",
		}},
		{"dedupe: a _dupl copy loses even when it is better", nil, []Candidate{
			libFirst("A/ABBA - SOS", "ABBA", "SOS", poor),
			marked(libFirst("A/ABBA - SOS_dupl1", "ABBA", "SOS", good)),
		}, []string{
			"library:A/ABBA - SOS: in-place A/ABBA - SOS",
			"library:A/ABBA - SOS_dupl1: duplicate <library:A/ABBA - SOS> (marked as a copy (_dupl suffix))",
		}},
		{"dedupe: between copies quality decides, a lone copy stays", nil, []Candidate{
			marked(libFirst("A/ABBA - SOS_dupl1", "ABBA", "SOS", poor)),
			marked(libFirst("A/ABBA - SOS_dupl2", "ABBA", "SOS", good)),
			marked(libFirst("X/X - Y_dupl1", "X", "Y", poor)),
		}, []string{
			"library:A/ABBA - SOS_dupl1: duplicate <library:A/ABBA - SOS_dupl2> (no video)",
			"library:A/ABBA - SOS_dupl2: in-place A/ABBA - SOS_dupl2",
			"library:X/X - Y_dupl1: in-place X/X - Y_dupl1",
		}},
		{"dedupe: a broken song goes only with a working replacement", nil, []Candidate{
			brokenLib("A/ABBA - SOS (bad)", "ABBA", "SOS", "no-notes"),
			libFirst("A/ABBA - SOS", "ABBA", "SOS", poor),
			brokenLib("Q/Queen - Song", "Queen", "Song", "bad-bpm", "missing-file"),
		}, []string{
			"library:A/ABBA - SOS (bad): invalid <library:A/ABBA - SOS> (errors: no-notes)",
			"library:A/ABBA - SOS: in-place A/ABBA - SOS",
			"library:Q/Queen - Song: left (broken (errors: bad-bpm, missing-file), but no working version replaces it, so it stays; check -k -move-broken moves broken songs)",
		}},
		{"add: a new song goes to its bucket, a known one stays", nil, []Candidate{
			libFirst("Old/ABBA - SOS", "ABBA", "SOS", good),
			song(low1, "new/ABBA - SOS", "ABBA", "SOS", poor),
			song(low1, "new/X - Y", "X", "Y", poor),
		}, []string{
			"library:Old/ABBA - SOS: in-place Old/ABBA - SOS",
			"low1:new/ABBA - SOS: duplicate <library:Old/ABBA - SOS> (no video)",
			"low1:new/X - Y: library X/X - Y",
		}},
		{"add: a better input replaces the library copy", nil, []Candidate{
			libFirst("A/ABBA - SOS", "ABBA", "SOS", poor),
			song(low1, "ABBA - SOS", "ABBA", "SOS", good),
			song(low2, "ABBA - SOS", "ABBA", "SOS", poor),
		}, []string{
			"library:A/ABBA - SOS: duplicate <low1:ABBA - SOS> (no video)",
			"low1:ABBA - SOS: library A/ABBA - SOS",
			"low2:ABBA - SOS: duplicate <low1:ABBA - SOS> (no video)",
		}},
		{"add: on a tie the library copy stays", nil, []Candidate{
			libFirst("A/ABBA - SOS", "ABBA", "SOS", poor),
			song(low1, "ABBA - SOS", "ABBA", "SOS", poor),
		}, []string{
			"library:A/ABBA - SOS: in-place A/ABBA - SOS",
			"low1:ABBA - SOS: duplicate <library:A/ABBA - SOS> (same quality; the version from the earlier source is kept)",
		}},
		{"add: an input never takes the name of a library song that stays", nil, []Candidate{
			libFirst("A/ABBA - SOS", "ABBA", "SOS [DUET]", poor),
			libFirst("A/ABBA - SOS (2)", "ABBA", "SOS (Live)", poor),
			song(low1, "ABBA - SOS", "ABBA", "SOS", poor),
		}, []string{
			"library:A/ABBA - SOS: in-place A/ABBA - SOS",
			"library:A/ABBA - SOS (2): in-place A/ABBA - SOS (2)",
			"low1:ABBA - SOS: library A/ABBA - SOS (3)",
		}},
		{"add: broken inputs go to invalid/, a broken library song is replaced", nil, []Candidate{
			brokenLib("A/ABBA - SOS", "ABBA", "SOS", "no-notes"),
			song(low1, "ABBA - SOS", "ABBA", "SOS", poor),
			brokenSong(low1, "Broken", "Queen", "Song", "bad-bpm"),
		}, []string{
			"library:A/ABBA - SOS: invalid <low1:ABBA - SOS> (errors: no-notes)",
			"low1:ABBA - SOS: library A/ABBA - SOS",
			"low1:Broken: invalid (errors: bad-bpm)",
		}},
		{"add: a replacement that cannot move in keeps the library copies", nonSong, []Candidate{
			brokenLib("Misc/sos", "ABBA", "SOS", "no-notes"),
			libFirst("Misc/sos2", "ABBA", "SOS", poor),
			song(low1, "ABBA - SOS", "ABBA", "SOS", good),
			brokenSong(low2, "SOS broken", "ABBA", "SOS", "no-notes"),
		}, []string{
			"library:Misc/sos: left (" + strings.Replace(reasonKeeperLeft, "%s", "low1:ABBA - SOS", 1) + ")",
			"library:Misc/sos2: left (" + strings.Replace(reasonKeeperLeft, "%s", "low1:ABBA - SOS", 1) + ")",
			"low1:ABBA - SOS: left (" + strings.Replace(reasonNotASong, "%s", "A/ABBA - SOS", 1) + ")",
			"low2:SOS broken: invalid <low1:ABBA - SOS> (errors: no-notes)", // an input is always consumed
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := summary(tt.cands, MakePlan(tt.cands, PlanOptions{LibraryInPlace: true, Occupied: tt.occupied}))
			if !reflect.DeepEqual(got, tt.want) {
				t.Errorf("plan:\n%s\nwant:\n%s", strings.Join(got, "\n"), strings.Join(tt.want, "\n"))
			}
		})
	}
}

// Without LibraryInPlace (organize), the same library songs are re-bucketed and broken
// ones go to invalid/ even without a replacement.
func TestMakePlanOrganizeMovesLibrary(t *testing.T) {
	cands := []Candidate{
		libFirst("Pop/ABBA - SOS", "ABBA", "SOS", Quality{}),
		brokenLib("Q/Queen - Song", "Queen", "Song", "bad-bpm"),
	}
	got := summary(cands, MakePlan(cands, PlanOptions{}))
	want := []string{"library:Pop/ABBA - SOS: library A/ABBA - SOS", "library:Q/Queen - Song: invalid (errors: bad-bpm)"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("plan:\n%s\nwant:\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
}

// A _dupl copy loses before every other criterion, even the priority tier.
func TestChooseKeeperMarkedCopy(t *testing.T) {
	good := Quality{HasVideo: true, VideoHeight: 1080, HasCover: true}
	a := &Candidate{Source: 0, Tier: 0, Rel: "a", Quality: good, MarkedCopy: true}
	b := &Candidate{Source: 1, Tier: 1, Rel: "b"}
	if wins, reason := better(a, b, true); wins || reason != "marked as a copy (_dupl suffix)" {
		t.Errorf("better = %v, %q, want the unmarked version", wins, reason)
	}
	b.MarkedCopy = true // both copies: quality decides
	if wins, reason := better(a, b, false); !wins || reason != "no video" {
		t.Errorf("better = %v, %q, want the better copy", wins, reason)
	}
}

func TestStripCopySuffix(t *testing.T) {
	tests := []struct {
		in   string
		ext  bool
		want string
		ok   bool
	}{
		{"SOS_dupl1", false, "SOS", true},
		{"ABBA - SOS_DUPL12  ", false, "ABBA - SOS", true},
		{"SOS _dupl3", false, "SOS", true},
		{"song_dupl2.txt", true, "song.txt", true},
		{"song_dupl2.txt", false, "song_dupl2.txt", false},
		{"song.txt", true, "song.txt", false},
		{"SOS_dupl", false, "SOS_dupl", false},
		{"SOS_dupl1 (Live)", false, "SOS_dupl1 (Live)", false},
		{"Duplicity", false, "Duplicity", false},
		{"", true, "", false},
	}
	for _, tt := range tests {
		if got, ok := StripCopySuffix(tt.in, tt.ext); got != tt.want || ok != tt.ok {
			t.Errorf("StripCopySuffix(%q, %v) = %q, %v, want %q, %v", tt.in, tt.ext, got, ok, tt.want, tt.ok)
		}
	}
}

// newCandidate takes the media summary over into the quality.
func TestNewCandidateMedia(t *testing.T) {
	src := config.Source{Path: "/lib", Label: "lib"}
	m := check.MediaSummary{HasVideo: true, VideoHeight: 720, VideoBitrate: 4000000, AudioBitrate: 128000, HasCover: true, HasBackground: true}
	c := newCandidate(config.OrganizeConfig{}, src, "/lib/X", check.Inspection{Media: m}, nil)
	want := Quality{HasVideo: true, VideoHeight: 720, VideoBitrate: 4000000, AudioBitrate: 128000, HasCover: true, HasBackground: true}
	if c.Quality != want || c.MarkedCopy {
		t.Errorf("candidate = %+v, want quality %+v", c, want)
	}
}
