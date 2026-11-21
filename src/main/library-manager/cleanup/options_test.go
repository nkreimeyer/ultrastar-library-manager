package cleanup

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"reflect"
	"sync"
	"testing"

	"github.com/nkreimeyer/library-manager/config"
	"github.com/nkreimeyer/library-manager/fsops"
	"github.com/nkreimeyer/library-manager/internal/testutil"
)

// optionsLibrary holds a song with a leftover file and a near-empty folder.
var optionsLibrary = map[string]string{
	"Song/Song.txt":  songTxt,
	"Song/song.mp3":  "audio",
	"Song/cover.jpg": "cover",
	"Song/junk.jpg":  "junk",
	"Lone/song.mp3":  "audio",
}

// runWith runs RunWith and fails the test on an error or failed operations.
func runWith(t *testing.T, cfg config.Config, opts Options) *fsops.Ops {
	t.Helper()
	ops := fsops.New(cfg.DryRun)
	if err := RunWith(cfg, ops, opts); err != nil {
		t.Fatalf("RunWith: %v", err)
	}
	if st := ops.Stats(); st.Failed != 0 {
		t.Fatalf("%d failed operations", st.Failed)
	}
	return ops
}

func TestDefaultOptions(t *testing.T) {
	if got := DefaultOptions(); !got.LeftoverFiles || !got.BrokenFolders || got.Ctx != nil || got.Progress != nil {
		t.Errorf("DefaultOptions() = %+v, want both passes on and nothing else", got)
	}
}

// Without BrokenFolders the near-empty folder stays, the leftover file still moves.
func TestRunWithoutBrokenFolders(t *testing.T) {
	cfg := newEnv(t, false)
	testutil.WriteFiles(t, cfg.SongDir, optionsLibrary)
	runWith(t, cfg, Options{LeftoverFiles: true})
	checkTrees(t, cfg.SongDir, cfg.ExtraFilesDir(),
		[]string{"Lone/song.mp3", "Song/Song.txt", "Song/cover.jpg", "Song/song.mp3"},
		[]string{"Song/junk.jpg"})
	if testutil.Exists(cfg.InvalidDir()) {
		t.Errorf("invalid/ created without BrokenFolders")
	}
}

// Without LeftoverFiles only the near-empty folder moves.
func TestRunWithoutLeftoverFiles(t *testing.T) {
	cfg := newEnv(t, false)
	testutil.WriteFiles(t, cfg.SongDir, optionsLibrary)
	runWith(t, cfg, Options{BrokenFolders: true})
	checkTrees(t, cfg.SongDir, cfg.ExtraFilesDir(),
		[]string{"Song/Song.txt", "Song/cover.jpg", "Song/junk.jpg", "Song/song.mp3"}, nil)
	if got := files(testutil.Tree(t, cfg.InvalidDir())); !reflect.DeepEqual(got, []string{"Lone/song.mp3"}) {
		t.Errorf("invalid files = %q, want the lone folder", got)
	}
}

// The near-empty folders BrokenFolders would move go through the leftover pass when it
// is off, and that pass moves none of their files: a lone txt is never moved, and a lone
// media file has no txt to be unreferenced by.
func TestRunNearEmptyFoldersKeepTheirFiles(t *testing.T) {
	for _, dry := range []bool{false, true} {
		cfg := newEnv(t, dry)
		testutil.WriteFiles(t, cfg.SongDir, map[string]string{
			"OnlyTxt/Song.txt":    songTxt,
			"OnlyAudio/song.mp3":  "audio",
			"OnlyImage/x.jpg":     "img",
			"TxtNoise/Song.txt":   songTxt,
			"TxtNoise/._Song.txt": "x",
		})
		before := testutil.Tree(t, cfg.SongDir)
		ops := runWith(t, cfg, Options{LeftoverFiles: true})
		if st := ops.Stats(); st.Moved != 0 {
			t.Errorf("dry=%v: stats = %+v, want no moves", dry, st)
		}
		if after := testutil.Tree(t, cfg.SongDir); !reflect.DeepEqual(before, after) {
			t.Errorf("dry=%v: library changed:\nbefore %q\nafter  %q", dry, before, after)
		}
	}
}

// A ctx that is already done stops the run before any move, in either pass.
func TestRunWithCancelledCtxMovesNothing(t *testing.T) {
	for _, opts := range []Options{DefaultOptions(), {BrokenFolders: true}, {LeftoverFiles: true}} {
		cfg := newEnv(t, false)
		testutil.WriteFiles(t, cfg.SongDir, optionsLibrary)
		before := testutil.Tree(t, cfg.SongDir)
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		opts.Ctx = ctx
		ops := fsops.New(false)
		err := RunWith(cfg, ops, opts)
		if !errors.Is(err, context.Canceled) {
			t.Errorf("RunWith(%+v) = %v, want context.Canceled", opts, err)
		}
		if st := ops.Stats(); st.Moved != 0 {
			t.Errorf("stats = %+v, want no moves", st)
		}
		if after := testutil.Tree(t, cfg.SongDir); !reflect.DeepEqual(before, after) {
			t.Errorf("library changed:\nbefore %q\nafter  %q", before, after)
		}
	}
}

// filterEntries stops at a done ctx before its next folder.
func TestFilterEntriesStopsAtDoneCtx(t *testing.T) {
	cfg := newEnv(t, false)
	testutil.WriteFiles(t, cfg.SongDir, optionsLibrary)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	ops := fsops.New(false)
	got, err := filterEntries(ctx, cfg, ops, []string{filepath.Join(cfg.SongDir, "Lone")}, true)
	if !errors.Is(err, context.Canceled) || got != nil {
		t.Errorf("filterEntries = %q, %v, want nil, context.Canceled", got, err)
	}
	if st := ops.Stats(); st.Moved != 0 {
		t.Errorf("stats = %+v, want no moves", st)
	}
}

// Progress counts the folders of the leftover pass: the near-empty folder already went
// to invalid/ and is not part of it.
func TestRunWithProgress(t *testing.T) {
	cfg := newEnv(t, false)
	lib := map[string]string{"Song2/Song.txt": songTxt, "Song2/song.mp3": "a"}
	for k, v := range optionsLibrary {
		lib[k] = v
	}
	testutil.WriteFiles(t, cfg.SongDir, lib)
	var mu sync.Mutex
	var calls [][2]int
	runWith(t, cfg, Options{LeftoverFiles: true, BrokenFolders: true, Progress: func(done, total int) {
		mu.Lock()
		defer mu.Unlock()
		calls = append(calls, [2]int{done, total})
	}})
	if want := [][2]int{{1, 2}, {2, 2}}; !reflect.DeepEqual(calls, want) {
		t.Errorf("progress calls = %v, want %v", calls, want)
	}
}

// A done ctx is an error even when the library has no folders to clean up.
func TestRunWithCancelledCtxEmptyLibrary(t *testing.T) {
	cfg := newEnv(t, false)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := RunWith(cfg, fsops.New(false), Options{LeftoverFiles: true, Ctx: ctx}); !errors.Is(err, context.Canceled) {
		t.Errorf("RunWith = %v, want context.Canceled", err)
	}
}

// A cancel during the leftover pass stops it: with one folder more than workers, the
// last folder only starts after a finished one reported progress, which cancels.
func TestRunWithCancelDuringLeftoverPass(t *testing.T) {
	cfg := newEnv(t, false)
	lib := map[string]string{}
	for i := range workers + 1 {
		dir := fmt.Sprintf("Song%03d/", i)
		lib[dir+"Song.txt"], lib[dir+"song.mp3"], lib[dir+"junk.jpg"] = songTxt, "a", "junk"
	}
	testutil.WriteFiles(t, cfg.SongDir, lib)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	ops := fsops.New(false)
	err := RunWith(cfg, ops, Options{LeftoverFiles: true, Ctx: ctx, Progress: func(int, int) { cancel() }})
	if !errors.Is(err, context.Canceled) {
		t.Errorf("RunWith = %v, want context.Canceled", err)
	}
	if st := ops.Stats(); st.Moved > workers {
		t.Errorf("stats = %+v, want at most %d moves", st, workers)
	}
}

// With moveBroken off, filterEntries returns the near-empty folder as valid, moves
// nothing and still drops (and counts once) a folder it cannot list.
func TestFilterEntriesWithoutMoveBroken(t *testing.T) {
	cfg := newEnv(t, false)
	testutil.WriteFiles(t, cfg.SongDir, optionsLibrary)
	song, lone := filepath.Join(cfg.SongDir, "Song"), filepath.Join(cfg.SongDir, "Lone")
	gone := filepath.Join(cfg.SongDir, "Gone")
	ops := fsops.New(false)
	got, err := filterEntries(context.Background(), cfg, ops, []string{gone, lone, song}, false)
	if want := []string{lone, song}; err != nil || !reflect.DeepEqual(got, want) {
		t.Errorf("filterEntries = %q, %v, want %q, nil", got, err, want)
	}
	if st := ops.Stats(); st.Failed != 1 || st.Moved != 0 {
		t.Errorf("stats = %+v, want 1 failure and no moves", st)
	}
}

// The one exception to "the leftover pass moves nothing new": hidden clutter next to a
// lone song txt goes to extra-files/, as it does in every song folder (with BrokenFolders
// on, it would have gone to invalid/ with the folder).
func TestRunWithoutBrokenFoldersMovesHiddenClutter(t *testing.T) {
	cfg := newEnv(t, false)
	testutil.WriteFiles(t, cfg.SongDir, map[string]string{"TxtOnly/Song.txt": songTxt, "TxtOnly/.DS_Store": "x"})
	runWith(t, cfg, Options{LeftoverFiles: true})
	checkTrees(t, cfg.SongDir, cfg.ExtraFilesDir(), []string{"TxtOnly/Song.txt"}, []string{"TxtOnly/.DS_Store"})
}
