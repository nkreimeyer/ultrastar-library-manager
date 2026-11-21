package webui

import (
	"slices"
	"testing"
)

// "Folders without a song" and "broken songs" are separate choices of the tidy-up.
func TestTidyFoldersAndBrokenSongsAreSeparate(t *testing.T) {
	s := newTestServer(t)
	lib, out := tidyLibrary(t)

	// Only the folders: the empty folder is set aside, the song with errors stays.
	v := analyseOrganize(t, s, organizeRequest{Mode: "dedupe", Library: lib, Out: out, Tidy: &tidyOptions{Folders: true}})
	if !slices.Equal(v.Tidy.BrokenFolders, []string{"Junk"}) || v.Summary.Invalid != 0 {
		t.Errorf("folders only: broken folders %q, summary %+v", v.Tidy.BrokenFolders, v.Summary)
	}
	if _, lone := findSong(v, "library:L/Lone - Broken"); lone.Action == "invalid" {
		t.Errorf("folders only: the broken song was set aside: %+v", lone)
	}

	// Only the songs: the song with errors is set aside, the empty folder stays.
	v = analyseOrganize(t, s, organizeRequest{Mode: "dedupe", Library: lib, Out: out, Tidy: &tidyOptions{Broken: true}})
	if len(v.Tidy.BrokenFolders) != 0 || v.Summary.Invalid < 1 {
		t.Errorf("songs only: broken folders %q, summary %+v", v.Tidy.BrokenFolders, v.Summary)
	}
	if _, lone := findSong(v, "library:L/Lone - Broken"); lone.Action != "invalid" {
		t.Errorf("songs only: the broken song stays: %+v", lone)
	}
	if v.Summary.Duplicates != 0 {
		t.Errorf("songs only: duplicates were set aside: %+v", v.Summary)
	}

	// Neither: nothing to do.
	var e apiError
	if got := s.do("POST", "/api/organize/analyse", organizeRequest{Mode: "dedupe", Library: lib, Out: out, Tidy: &tidyOptions{}}, &e); got != 400 {
		t.Errorf("nothing chosen: %d", got)
	}
}
