package config

import (
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

// SamePath folds like IsWithin, in both directions: case on darwin and windows, Unicode
// normalization on darwin only; Linux compares exactly. A folder below the other is never
// the same.
func TestSamePathFoldsLikeIsWithin(t *testing.T) {
	nfc, nfd := "/m/Ärzte", "/m/Ärzte"
	tests := []struct {
		a, b, goos string
		want       bool
	}{
		{"/m/songs", "/m/songs", "linux", true},
		{"/m/songs", "/m/Songs", "linux", false},
		{"/m/songs", "/m/Songs", "darwin", true},
		{"/m/Songs", "/m/songs", "windows", true},
		{nfc, nfd, "darwin", true},
		{nfd, nfc, "darwin", true},
		{nfc, nfd, "windows", false},
		{nfc, nfd, "linux", false},
		{"/m/songs/a", "/m/Songs", "darwin", false},
		{"/m/Songs", "/m/songs/a", "windows", false},
		{"/m/songs", "/m/songs2", "darwin", false},
	}
	for _, tt := range tests {
		if got := samePath(tt.a, tt.b, tt.goos); got != tt.want {
			t.Errorf("samePath(%q, %q, %s) = %v, want %v", tt.a, tt.b, tt.goos, got, tt.want)
		}
	}
}

// On darwin and windows a library or source spelled in another case (on darwin also in
// another Unicode normalization) is the same folder: the library is a source (with the
// source's spelling), a source given twice is "given twice", and add refuses the library as
// a source. Linux compares exactly. The folders exist in both spellings, as the test file
// system is case-sensitive.
func TestNewOrganizeComparesPathsLikeTheFileSystem(t *testing.T) {
	nfc, nfd := "Ärzte", "Ärzte"
	root := realRoot(t, "songs", "Songs", nfc, nfd)
	j := func(rel string) string { return filepath.Join(root, rel) }
	tests := []struct {
		name, goos string
		add        bool
		tiers      [][]string
		lib        string
		wantErr    string   // "" = no error
		wantLib    string   // when no error
		wantSrcs   []string // source paths, when no error
	}{
		{"library is a source in another case", "darwin", false, [][]string{{j("songs")}}, j("Songs"), "", j("songs"), []string{j("songs")}},
		{"library is a source in another case (windows)", "windows", false, [][]string{{j("Songs")}}, j("songs"), "", j("Songs"), []string{j("Songs")}},
		{"library is a source in another normalization", "darwin", false, [][]string{{j(nfc)}}, j(nfd), "", j(nfc), []string{j(nfc)}},
		{"other normalization is another folder on windows", "windows", false, [][]string{{j(nfc)}}, j(nfd), "", j(nfd), []string{j(nfc)}},
		{"other case is another folder on linux", "linux", false, [][]string{{j("songs")}}, j("Songs"), "", j("Songs"), []string{j("songs")}},
		{"source twice in another case", "darwin", false, [][]string{{j("songs")}, {j("Songs")}}, j("lib"), "is given twice", "", nil},
		{"source twice in another case (windows)", "windows", false, [][]string{{j("songs"), j("Songs")}}, j("lib"), "is given twice", "", nil},
		{"two sources on linux", "linux", false, [][]string{{j("songs")}, {j("Songs")}}, j("lib"), "", j("lib"), []string{j("songs"), j("Songs")}},
		{"add: the library as a source in another case", "darwin", true, [][]string{{j("Songs")}}, j("songs"), "is the library", "", nil},
		{"add: the library as a source in another case (windows)", "windows", true, [][]string{{j("songs")}}, j("Songs"), "is the library", "", nil},
		{"add on linux: another folder", "linux", true, [][]string{{j("Songs")}}, j("songs"), "", j("songs"), []string{j("songs"), j("Songs")}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var cfg OrganizeConfig
			var err error
			if tt.add {
				cfg, err = newAdd(tt.tiers, tt.lib, j("out"), true, tt.goos)
			} else {
				cfg, err = newOrganize(tt.tiers, tt.lib, j("out"), true, tt.goos)
			}
			if tt.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
					t.Fatalf("err = %v, want one containing %q", err, tt.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			var srcs []string
			for _, s := range cfg.Sources {
				srcs = append(srcs, s.Path)
			}
			if cfg.Library != tt.wantLib || !reflect.DeepEqual(srcs, tt.wantSrcs) {
				t.Errorf("Library %q, sources %q; want %q, %q", cfg.Library, srcs, tt.wantLib, tt.wantSrcs)
			}
		})
	}
}
