package utils

import (
	"strings"
	"testing"
)

func TestTagsFromFolderName(t *testing.T) {
	const nfdMaedchen = "Ma\u0308dchen" // "Mädchen" in NFD, as macOS writes folder names
	tests := []struct {
		name                  string
		folder                string
		artist, title         string
		wantArtist, wantTitle string // "" = not filled
	}{
		// Nothing to do.
		{"both present", "X - Y", "A", "T", "", ""},
		{"both present, folder unrelated", "whatever", "A", "T", "", ""},

		// Both missing.
		{"both missing", "Electric Callboy - Hypa Hypa", "", "", "Electric Callboy", "Hypa Hypa"},
		{"both whitespace", "Electric Callboy - Hypa Hypa", "  ", "\t", "Electric Callboy", "Hypa Hypa"},
		{"collab artist", "Babymetal x Electric Callboy - Ratatata", "", "", "Babymetal x Electric Callboy", "Ratatata"},
		{"hyphen without spaces", "AC-DC - Back in Black", "", "", "AC-DC", "Back in Black"},
		{"only hyphen without spaces", "AC-DC-Back in Black", "", "", "", ""},
		{"two separators ambiguous", "Artist - Song - Live", "", "", "", ""},
		{"qualifier kept", "Artist - Title [DUET]", "", "", "Artist", "Title [DUET]"},
		{"collision suffix kept", "Artist - Title (2)", "", "", "Artist", "Title (2)"},
		{"track number", "01 - Hypa Hypa", "", "", "", ""},
		{"track number with dot", "01. - Hypa Hypa", "", "", "", ""},
		{"digits in the artist", "50 Cent - In da Club", "", "", "50 Cent", "In da Club"},
		{"track number, title present", "01 - Hypa Hypa", "", "Hypa Hypa", "", ""},
		{"BOM", "\ufeffArtist - Song", "", "", "", ""},
		{"zero-width space", "Art\u200bist - Song", "", "", "", ""},
		{"apostrophes", "Guns N' Roses - Sweet Child O' Mine", "", "", "Guns N' Roses", "Sweet Child O' Mine"},
		{"nfd folder normalized", "Die Ärzte - " + nfdMaedchen, "", "", "Die Ärzte", "Mädchen"},
		{"trimmed", "  Artist  -  Title  ", "", "", "Artist", "Title"},
		{"empty title part", "Artist - ", "", "", "", ""},
		{"empty artist part", " - Title", "", "", "", ""},
		{"empty both parts", " - ", "", "", "", ""},
		{"en dash is no separator", "Artist – Title", "", "", "", ""},
		{"no separator", "Hypa Hypa", "", "", "", ""},
		{"invalid utf8", "Artist - M\xe4dchen", "", "", "", ""},
		{"control character", "Artist - Ti\ttle", "", "", "", ""},
		{"newline", "Artist - Title\n", "", "", "", ""},
		{"empty folder name", "", "", "", "", ""},

		// Title present, artist missing.
		{"artist from title", "Electric Callboy - Hypa Hypa", "", "Hypa Hypa", "Electric Callboy", ""},
		{"artist, case differs", "electric callboy - HYPA hypa", "", "Hypa Hypa", "electric callboy", ""},
		{"artist, whitespace differs", "Electric Callboy - Hypa   Hypa", "", " Hypa Hypa ", "Electric Callboy", ""},
		{"artist, two splits one matches", "Artist - Song - Live", "", "Song - Live", "Artist", ""},
		{"artist, two splits second matches", "Artist - Song - Live", "", "Live", "", ""},
		{"artist, pack prefix", "SingStar - Electric Callboy - Hypa Hypa", "", "Hypa Hypa", "", ""},
		{"artist, track number prefix", "01 - Village People - Y.M.C.A.", "", "Y.M.C.A.", "", ""},
		{"artist, category prefix", "Rock - Queen - Bohemian Rhapsody", "", "Bohemian Rhapsody", "", ""},
		{"artist, trailing dot stripped by Windows", "Village People - Y.M.C.A", "", "Y.M.C.A.", "Village People", ""},
		{"artist, trailing dot only in the folder", "Artist - Song.", "", "Song", "Artist", ""},
		{"artist, typographic apostrophe in the title", "Queen - Don't Stop Me Now", "", "Don\u2019t Stop Me Now", "Queen", ""},
		{"artist, typographic apostrophe in the folder", "Queen - Don\u2019t Stop Me Now", "", "Don't Stop Me Now", "Queen", ""},
		{"artist, typographic quotes", "Artist - Song Title", "", "Song \u201cTitle\u201d", "Artist", ""},
		{"artist, self-titled", "Black Sabbath - Black Sabbath", "", "Black Sabbath", "Black Sabbath", ""},
		{"artist, self-titled with prefix", "X - Black Sabbath - Black Sabbath", "", "Black Sabbath", "", ""},
		{"artist, two splits title too short", "Artist - Song - Live", "", "Song", "", ""},
		{"artist, reversed naming", "Last Christmas - Wham!", "", "Last Christmas", "", ""},
		{"artist, no match", "Electric Callboy - Hypa Hypa", "", "Pump It", "", ""},
		{"artist, folder qualifier", "Artist - Song [DUET]", "", "Song", "Artist", ""},
		{"artist, several folder qualifiers", "Artist - Song [DUET] (Live)", "", "Song", "Artist", ""},
		{"artist, qualifier on the tag too", "Artist - Song [DUET] (Live)", "", "Song [DUET]", "Artist", ""},
		{"artist, qualifier only on the tag", "Artist - Song", "", "Song (Live)", "", ""},
		{"artist, invalid file name chars", "Artist - What's Up", "", "What's Up?", "Artist", ""},
		{"artist, colon removed", "Artist - Part 1 Intro", "", "Part 1: Intro", "Artist", ""},
		{"artist, nfd folder", "Die Ärzte - " + nfdMaedchen, "", "Mädchen", "Die Ärzte", ""},
		{"artist, nfd title", "Die Ärzte - Mädchen", "", nfdMaedchen, "Die Ärzte", ""},
		{"artist, unicode case folding", "Artist - STRASSE", "", "Straße", "Artist", ""},
		{"artist, empty left", "- Hypa Hypa", "", "Hypa Hypa", "", ""},
		{"artist, title matches both sides", "Song - Song", "", "Song", "Song", ""},
		{"artist, title matches three parts", "Song - Song - Song", "", "Song", "", ""},
		{"artist, title only unsafe chars", "Artist - ?", "", "?", "", ""},
		{"artist, en dash", "Artist – Song", "", "Song", "", ""},
		{"artist, collision suffix", "Artist - Song (2)", "", "Song", "Artist", ""},

		// Artist present, title missing.
		{"title from artist", "Wham! - Last Christmas", "Wham!", "", "", "Last Christmas"},
		{"title, whole remainder", "Artist - Song - Live", "Artist", "", "", "Song - Live"},
		{"title, artist with separator", "Artist - Song - Live", "Artist - Song", "", "", "Live"},
		{"title, qualifier kept", "Artist - Title [DUET]", "Artist", "", "", "Title [DUET]"},
		{"title, reversed naming", "Last Christmas - Wham!", "Wham!", "", "", ""},
		{"title, case differs", "WHAM! - Last Christmas", "wham!", "", "", "Last Christmas"},
		{"title, apostrophes", "Guns N' Roses - Sweet Child O' Mine", "Guns N' Roses", "", "", "Sweet Child O' Mine"},
		{"title, AC/DC", "ACDC - Back in Black", "AC/DC", "", "", "Back in Black"},
		{"title, AC/DC as AC-DC", "AC-DC - Back in Black", "AC/DC", "", "", "Back in Black"},
		{"title, AC/DC as AC_DC", "AC_DC - Thunderstruck", "AC/DC", "", "", "Thunderstruck"},
		{"title, AC/DC as AC DC", "AC DC - Thunderstruck", "AC/DC", "", "", "Thunderstruck"},
		{"title, self-titled", "Iron Maiden - Iron Maiden", "Iron Maiden", "", "", "Iron Maiden"},
		{"title, album in between", "Queen - Greatest Hits - Bohemian Rhapsody", "Queen", "", "", "Greatest Hits - Bohemian Rhapsody"},
		{"title, no match", "Wham! - Last Christmas", "Queen", "", "", ""},
		{"title, empty right", "Wham! -", "Wham!", "", "", ""},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := TagsFromFolderName(tc.folder, tc.artist, tc.title)
			if got.FillArtist != (tc.wantArtist != "") || got.Artist != tc.wantArtist {
				t.Errorf("artist: got fill=%v %q, want %q", got.FillArtist, got.Artist, tc.wantArtist)
			}
			if got.FillTitle != (tc.wantTitle != "") || got.Title != tc.wantTitle {
				t.Errorf("title: got fill=%v %q, want %q", got.FillTitle, got.Title, tc.wantTitle)
			}
			// A reason is given exactly when a missing tag stays missing.
			wantReason := (isBlank(tc.artist) && tc.wantArtist == "") || (isBlank(tc.title) && tc.wantTitle == "")
			if (got.Reason != "") != wantReason {
				t.Errorf("reason = %q, want a reason: %v", got.Reason, wantReason)
			}
		})
	}
}

func isBlank(s string) bool { return strings.TrimSpace(s) == "" }

// TestTagsFromFolderNameReasons checks that the reasons name the problem.
func TestTagsFromFolderNameReasons(t *testing.T) {
	tests := []struct {
		folder, artist, title, want string
	}{
		{"Hypa Hypa", "", "", `no " - "`},
		{"A - B - C", "", "", "ambiguous"},
		{"Last Christmas - Wham!", "", "Last Christmas", "wrong side"},
		{"Song - Song - Song", "", "Song", "several parts"},
		{"A - B", "", "C", "does not contain"},
		{"A - M\xe4dchen", "", "", "not valid UTF-8"},
		{"Artist - ", "", "", `no " - "`},
		{" - Title", "", "", `no " - "`},
		{" - ", "", "", `no " - "`},
		{"- Hypa Hypa", "", "Hypa Hypa", `no " - "`},
		{"Wham! -", "Wham!", "", `no " - "`},
		{"01 - Hypa Hypa", "", "", "track number"},
		{"01 - Hypa Hypa", "", "Hypa Hypa", "track number"},
		{"SingStar - Electric Callboy - Hypa Hypa", "", "Hypa Hypa", "prefix"},
		{"\ufeffA - B", "", "", "format characters"},
	}
	for _, tc := range tests {
		got := TagsFromFolderName(tc.folder, tc.artist, tc.title).Reason
		if !strings.Contains(got, tc.want) {
			t.Errorf("TagsFromFolderName(%q, %q, %q).Reason = %q, want it to contain %q", tc.folder, tc.artist, tc.title, got, tc.want)
		}
	}
}

// matchKey folds every apostrophe and typographic double quote the folder names may use
// to ASCII (the set of IsApostrophe).
func TestMatchKeyQuotes(t *testing.T) {
	for _, r := range "'`’‘´ʼ" {
		if got := matchKey("don"+string(r)+"t", ""); got != "don't" {
			t.Errorf("matchKey(%q) = %q", "don"+string(r)+"t", got)
		}
	}
	for _, r := range "“”„" {
		if got := matchKey(string(r)+"x", "_"); got != "_x" {
			t.Errorf("matchKey(%q) = %q", string(r)+"x", got)
		}
	}
}
