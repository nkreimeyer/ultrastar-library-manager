package organize

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/nkreimeyer/library-manager/config"
	"github.com/nkreimeyer/library-manager/fsops"
)

// prepared returns a function that fails the test on err, else prepares the out dir of
// cfg: prepared(t)(config.NewDedupe(...)).
func prepared(t *testing.T) func(config.OrganizeConfig, error) config.OrganizeConfig {
	t.Helper()
	return func(cfg config.OrganizeConfig, err error) config.OrganizeConfig {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
		if err := cfg.PrepareOutDir(); err != nil {
			t.Fatal(err)
		}
		return cfg
	}
}

// renamed returns files with every key "dir/from" renamed to "dir/to" and the txt
// references to from replaced.
func renamed(files map[string]string, dir, from, to string) map[string]string {
	out := map[string]string{}
	for k, v := range files {
		if k == dir+"/"+from {
			k = dir + "/" + to
		}
		out[k] = strings.ReplaceAll(v, ":"+from+"\n", ":"+to+"\n")
	}
	return out
}

// dedupeLibrary is a library with copies marked in every supported way: the folder name,
// #TITLE, the txt name and the audio name. Each marked copy is the better version, so
// only the mark makes it lose.
func dedupeLibrary(t *testing.T, lib string) {
	t.Helper()
	writeFiles(t, lib, merge(
		songFiles("Pop/ABBA - SOS", "ABBA", "SOS", "cover"),
		songFiles("Misc/ABBA - SOS_dupl1", "ABBA", "SOS", "cover", "video"),
		songFiles("C/Cher - Believe", "Cher", "Believe"),
		songFiles("C/Cher - Believe (other)", "Cher", "Believe_dupl1", "video"),
		songFiles("T/Toto - Africa", "Toto", "Africa"),
		renamed(songFiles("T/Toto - Africa 2", "Toto", "Africa", "video"), "T/Toto - Africa 2", "song.txt", "song_dupl1.txt"),
		songFiles("M/Muse - Uprising", "Muse", "Uprising"),
		renamed(songFiles("M/Muse - Uprising 2", "Muse", "Uprising", "video"), "M/Muse - Uprising 2", "song.mp3", "song_dupl3.mp3"),
		songFiles("Z/Zed - Lone_dupl1", "Zed", "Lone"),
		songFiles("Unsorted/Queen - Song", "Queen", "Song"),
		songFiles("Unsorted/Queen - Song (low)", "Queen", "Song", "warning"),
		map[string]string{
			// Broken (no #BPM): replaced by Pop/ABBA - SOS.
			"Pop/ABBA - SOS (broken)/song.txt": "#ARTIST:ABBA\n#TITLE:SOS\n#MP3:song.mp3\n: 0 2 0 la\nE\n",
			"Pop/ABBA - SOS (broken)/song.mp3": "x",
			// Broken without a working version: stays.
			"B/Blur - Song 2/song.txt": "#ARTIST:Blur\n#TITLE:Song 2\n#MP3:song.mp3\n: 0 2 0 la\nE\n",
			"B/Blur - Song 2/song.mp3": "x",
		},
	))
}

func TestRunDedupe(t *testing.T) {
	noFFprobe(t)
	base := t.TempDir()
	lib := filepath.Join(base, "library")
	dedupeLibrary(t, lib)
	before := tree(t, lib)

	dry := prepared(t)(config.NewDedupe(lib, filepath.Join(base, "dry-out"), true))
	dst, dryRep := runOrganize(t, dry, Options{Mode: ModeDedupe})
	if got := tree(t, lib); !reflect.DeepEqual(got, before) {
		t.Errorf("dry run changed the library:\n%s", strings.Join(got, "\n"))
	}

	cfg := prepared(t)(config.NewDedupe(lib, filepath.Join(base, "out"), false))
	st, rep := runOrganize(t, cfg, Options{Mode: ModeDedupe})
	if st.Moved != dst.Moved || st.Failed != 0 {
		t.Errorf("real run moved %d (failed %d), dry run planned %d", st.Moved, st.Failed, dst.Moved)
	}
	if a, b := comparable(dryRep), comparable(rep); !reflect.DeepEqual(a, b) {
		da, _ := fsops.MarshalReport(a)
		db, _ := fsops.MarshalReport(b)
		t.Errorf("dry-run plan differs from the real run:\n%s\nreal:\n%s", da, db)
	}
	if rep.Mode != ModeDedupe || !strings.HasPrefix(string(rep.text()), "dedupe plan (real run)\n") {
		t.Errorf("mode = %q, text starts %q", rep.Mode, string(rep.text())[:30])
	}
	if len(rep.ToLibrary) != 0 {
		t.Errorf("dedupe moved songs within the library: %+v", rep.ToLibrary)
	}
	dups := map[string]string{}
	for _, d := range rep.Duplicates {
		dups[d.Song] = d.Keeper + " / " + d.Reason
	}
	const markedReason = " / marked as a copy (_dupl suffix)"
	wantDups := map[string]string{
		"library:Misc/ABBA - SOS_dupl1":       "library:Pop/ABBA - SOS" + markedReason,
		"library:C/Cher - Believe (other)":    "library:C/Cher - Believe" + markedReason,
		"library:T/Toto - Africa 2":           "library:T/Toto - Africa" + markedReason,
		"library:M/Muse - Uprising 2":         "library:M/Muse - Uprising" + markedReason,
		"library:Unsorted/Queen - Song (low)": "library:Unsorted/Queen - Song / more song warnings (1 vs 0)",
	}
	if !reflect.DeepEqual(dups, wantDups) {
		t.Errorf("duplicates = %v\nwant %v", dups, wantDups)
	}
	if len(rep.Invalid) != 1 || rep.Invalid[0].Song != "library:Pop/ABBA - SOS (broken)" {
		t.Errorf("invalid = %+v", rep.Invalid)
	}
	if len(rep.LeftInPlace) != 1 || rep.LeftInPlace[0].Song != "library:B/Blur - Song 2" {
		t.Errorf("left in place = %+v", rep.LeftInPlace)
	}
	var dirs []string
	for _, f := range files(t, lib) {
		if d := filepath.ToSlash(filepath.Dir(f)); len(dirs) == 0 || dirs[len(dirs)-1] != d {
			dirs = append(dirs, d)
		}
	}
	wantDirs := []string{"B/Blur - Song 2", "C/Cher - Believe", "M/Muse - Uprising", "Pop/ABBA - SOS", "T/Toto - Africa", "Unsorted/Queen - Song", "Z/Zed - Lone_dupl1"}
	if !reflect.DeepEqual(dirs, wantDirs) {
		t.Errorf("library songs = %q, want %q", dirs, wantDirs)
	}
	for _, p := range []string{"duplicates/library/Misc/ABBA - SOS_dupl1/video.mp4", "duplicates/library/T/Toto - Africa 2/song_dupl1.txt", "invalid/library/Pop/ABBA - SOS (broken)/song.txt"} {
		if _, err := os.Stat(filepath.Join(cfg.OutDir, filepath.FromSlash(p))); err != nil {
			t.Errorf("%s: %v", p, err)
		}
	}

	// Idempotent: a second run moves nothing.
	again := prepared(t)(config.NewDedupe(lib, filepath.Join(base, "out2"), false))
	if st2, _ := runOrganize(t, again, Options{Mode: ModeDedupe}); st2.Moved != 0 {
		t.Errorf("second run moved %d", st2.Moved)
	}
	if ust := runUndo(t, cfg); ust.Failed != 0 {
		t.Errorf("undo failed %d", ust.Failed)
	}
	if got := tree(t, lib); !reflect.DeepEqual(got, before) {
		t.Errorf("undo did not restore the library:\n%s\nwant:\n%s", strings.Join(got, "\n"), strings.Join(before, "\n"))
	}
}

func TestRunAdd(t *testing.T) {
	noFFprobe(t)
	base := t.TempDir()
	lib, newDir, new2 := filepath.Join(base, "library"), filepath.Join(base, "new"), filepath.Join(base, "new2")
	writeFiles(t, lib, merge(
		songFiles("A/ABBA - SOS", "ABBA", "SOS"),
		songFiles("Q/Queen - Song", "Queen", "Song", "video"),
		songFiles("Old/Toto - Africa", "Toto", "Africa"),
	))
	writeFiles(t, newDir, merge(
		songFiles("ABBA - SOS", "ABBA", "SOS", "video"), // better: replaces the library copy
		songFiles("Queen - Song", "Queen", "Song"),      // worse: a duplicate
		songFiles("Toto - Africa", "Toto", "Africa"),    // a tie: the library copy stays
		songFiles("Cher - Believe", "Cher", "Believe"),
		songFiles("Cher - Believe_dupl1", "Cher", "Believe", "video"),
		map[string]string{
			"Broken/song.txt": "#ARTIST:Blur\n#TITLE:Song 2\n#MP3:song.mp3\n: 0 2 0 la\nE\n",
			"Broken/song.mp3": "x",
		},
	))
	writeFiles(t, new2, songFiles("Muse - Uprising_dupl2", "Muse", "Uprising")) // a lone copy: added without the mark
	roots := []string{lib, newDir, new2}
	before := map[string][]string{}
	for _, d := range roots {
		before[d] = tree(t, d)
	}
	tiers := [][]string{{newDir}, {new2}}

	dry := prepared(t)(config.NewAdd(tiers, lib, filepath.Join(base, "dry-out"), true))
	if dry.Sources[0].Path != lib {
		t.Fatalf("the library must be the first source: %+v", dry.Sources)
	}
	dst, dryRep := runOrganize(t, dry, Options{Mode: ModeAdd})
	for _, d := range roots {
		if got := tree(t, d); !reflect.DeepEqual(got, before[d]) {
			t.Errorf("dry run changed %s", d)
		}
	}
	cfg := prepared(t)(config.NewAdd(tiers, lib, filepath.Join(base, "out"), false))
	st, rep := runOrganize(t, cfg, Options{Mode: ModeAdd})
	if st.Moved != dst.Moved || st.Failed != 0 {
		t.Errorf("real run moved %d (failed %d), dry run planned %d", st.Moved, st.Failed, dst.Moved)
	}
	if a, b := comparable(dryRep), comparable(rep); !reflect.DeepEqual(a, b) {
		da, _ := fsops.MarshalReport(a)
		db, _ := fsops.MarshalReport(b)
		t.Errorf("dry-run plan differs from the real run:\n%s\nreal:\n%s", da, db)
	}

	var got []string
	for _, f := range files(t, lib) {
		got = append(got, f[:strings.LastIndex(f, " ")])
	}
	want := []string{
		"A/ABBA - SOS/song.mp3", "A/ABBA - SOS/song.txt", "A/ABBA - SOS/video.mp4",
		"C/Cher - Believe/song.mp3", "C/Cher - Believe/song.txt",
		"M/Muse - Uprising/song.mp3", "M/Muse - Uprising/song.txt",
		"Old/Toto - Africa/song.mp3", "Old/Toto - Africa/song.txt",
		"Q/Queen - Song/song.mp3", "Q/Queen - Song/song.txt", "Q/Queen - Song/video.mp4",
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("library:\n%s\nwant:\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
	for _, p := range []string{
		"duplicates/library/A/ABBA - SOS/song.txt", "duplicates/new/Queen - Song/song.txt",
		"duplicates/new/Toto - Africa/song.txt", "duplicates/new/Cher - Believe_dupl1/video.mp4",
		"invalid/new/Broken/song.txt",
	} {
		if _, err := os.Stat(filepath.Join(cfg.OutDir, filepath.FromSlash(p))); err != nil {
			t.Errorf("%s: %v", p, err)
		}
	}

	if ust := runUndo(t, cfg); ust.Failed != 0 {
		t.Errorf("undo failed %d", ust.Failed)
	}
	for _, d := range roots {
		if got := tree(t, d); !reflect.DeepEqual(got, before[d]) {
			t.Errorf("undo did not restore %s:\n%s\nwant:\n%s", d, strings.Join(got, "\n"), strings.Join(before[d], "\n"))
		}
	}
}

// add into a library that does not exist yet works like organize.
func TestRunAddNewLibrary(t *testing.T) {
	noFFprobe(t)
	base := t.TempDir()
	lib, src := filepath.Join(base, "library"), filepath.Join(base, "new")
	writeFiles(t, src, songFiles("x", "ABBA", "SOS"))
	cfg := prepared(t)(config.NewAdd([][]string{{src}}, lib, "", false))
	if len(cfg.Sources) != 1 {
		t.Fatalf("sources = %+v", cfg.Sources)
	}
	if st, _ := runOrganize(t, cfg, Options{Mode: ModeAdd}); st.Moved != 1 {
		t.Errorf("moved %d", st.Moved)
	}
	if _, err := os.Stat(filepath.Join(lib, "A", "ABBA - SOS", "song.txt")); err != nil {
		t.Error(err)
	}
}
