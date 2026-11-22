// Package cleanup removes clutter from song folders: files that the song txt does not
// reference are moved to <out>/extra-files/, folders with at most one visible entry are
// moved to <out>/invalid/. Both targets mirror the folder's path relative to the library
// root.
package cleanup

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"unicode/utf8"

	"github.com/nkreimeyer/library-manager/config"
	"github.com/nkreimeyer/library-manager/fsops"
	"github.com/nkreimeyer/library-manager/utils"

	log "github.com/sirupsen/logrus"
)

// workers is the number of song folders cleaned up concurrently. It is the size of the
// batches the module used before its worker pool, so no more folders (and their open
// files) are worked on at the same time than before; the work is file I/O, not CPU.
const workers = 100

// Options selects the cleanup passes and how a run is driven.
type Options struct {
	LeftoverFiles bool // move files no song txt references to extra-files/
	BrokenFolders bool // move folders with at most one entry to invalid/

	Ctx      context.Context       // nil = Background; once done no further folder starts
	Progress func(done, total int) // over the folders of the leftover-file pass, may be nil
}

// DefaultOptions returns the options of the CLI's -c: both passes on.
func DefaultOptions() Options {
	return Options{LeftoverFiles: true, BrokenFolders: true}
}

// Run performs the cleanup on every song folder below cfg.SongDir with DefaultOptions.
func Run(cfg config.Config, ops *fsops.Ops) error {
	return RunWith(cfg, ops, DefaultOptions())
}

// RunWith performs the cleanup passes opts selects on every song folder below
// cfg.SongDir. It only returns an error if the library cannot be read at all or the run
// was stopped (opts.Ctx done: the error wraps ctx.Err()); per-song problems are logged
// and counted by ops. A panic in one folder is logged and counted as a failure, and the
// other folders are still cleaned up.
func RunWith(cfg config.Config, ops *fsops.Ops, opts Options) error {
	ctx := opts.Ctx
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return fmt.Errorf("cleanup stopped: %w", err)
	}
	entries, err := utils.GetAllSongFolders(ops, cfg.SongDir)
	if err != nil {
		return fmt.Errorf("listing song folders: %w", err)
	}

	validEntries, err := filterEntries(ctx, cfg, ops, entries, opts.BrokenFolders)
	if err != nil {
		return fmt.Errorf("cleanup stopped: %w", err)
	}
	if !opts.LeftoverFiles {
		return nil
	}

	err = utils.ForEachParallel(ctx, validEntries, utils.Parallel{
		Workers:  workers,
		Progress: opts.Progress,
		OnPanic: func(i int, _ any) {
			log.Errorf("cleanup: %s not finished: internal error (logged above)", validEntries[i])
			ops.Fail()
		},
	}, func(_ int, entry string) {
		// A folder that only its leftover files kept from looking empty is set aside now,
		// not on the next run.
		if directoryCleaner(cfg, ops, entry) && opts.BrokenFolders {
			if files, err := os.ReadDir(entry); err != nil {
				reportUnreadable(ops, entry, err)
			} else {
				moveIfBroken(cfg, ops, entry, files)
			}
		}
	})
	if err != nil {
		return fmt.Errorf("cleanup stopped: %w", err)
	}
	return nil
}

// directoryCleaner is what Run runs per folder; a variable so tests can make it panic.
var directoryCleaner = cleanupDirectory

// filterEntries moves folders with at most one visible entry to the invalid folder (only
// if moveBroken is set; otherwise they stay and are returned like the others) and returns
// the remaining folders. Moved (or, in dry-run mode, to-be-moved) folders and unreadable
// folders are excluded from further cleanup. An empty folder whose counterpart already
// exists in invalid/ (a category whose songs fixtxt moved there) is merged into it by
// ops.MoveInto. Once ctx is done no further folder is looked at and ctx.Err() is returned.
func filterEntries(ctx context.Context, cfg config.Config, ops *fsops.Ops, entries []string, moveBroken bool) ([]string, error) {
	validEntries := make([]string, 0, len(entries))
	for _, entry := range entries {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		files, err := os.ReadDir(entry)
		if err != nil {
			reportUnreadable(ops, entry, err)
			continue
		}
		if !moveBroken {
			validEntries = append(validEntries, entry)
			continue
		}
		if moveIfBroken(cfg, ops, entry, files) {
			continue
		}
		validEntries = append(validEntries, entry)
	}
	return validEntries, nil
}

// moveIfBroken moves entry, a folder with the entries files, to the invalid folder if it
// has at most one entry that makes up a song (countEntries), and reports whether it did
// (or tried to).
func moveIfBroken(cfg config.Config, ops *fsops.Ops, entry string, files []os.DirEntry) bool {
	n := countEntries(ops, entry, files)
	if n > 1 {
		return false
	}
	log.Infof("Song folder %q has %d entries, moving it to invalid", entry, n)
	if _, err := ops.MoveInto(entry, cfg.InvalidDir(), cfg.RelPath(entry)); err != nil {
		log.Errorf("moving broken song folder: %v", err)
	}
	return true
}

// countEntries counts the entries of dir that make up a song. Hidden files (leftovers of
// interrupted runs, macOS "._*" files) and Synology "@eaDir" folders are no song content,
// so a lone audio file next to "._song.mp3" still counts as a broken song; otherwise it
// would only be detected on the next run, after cleanup moved the hidden file away. In
// dry-run mode, entries an earlier module would have moved away (ops.WouldBeMoved) are not
// counted either, so the plan matches a real run.
func countEntries(ops *fsops.Ops, dir string, files []os.DirEntry) int {
	n := 0
	for _, f := range files {
		if utils.IsNoiseEntry(ops, dir, f.Name()) {
			continue
		}
		n++
	}
	return n
}

// cleanupDirectory moves all files of songFolder that none of its song txts references to
// extra-files/<relative song path>/, and reports whether it moved (or planned to move)
// any. Folders without a readable song txt are left alone: without the txt, nothing can
// be known to be unreferenced. Every song txt of the folder counts, not only the newest:
// fixtxt moves the others away first (in a dry run they are planned moves, which
// SongTxtFiles skips), but with fixtxt off (-t=false) they stay and must keep their files.
func cleanupDirectory(cfg config.Config, ops *fsops.Ops, songFolder string) bool {
	songFolder = filepath.Clean(songFolder)
	txts, err := utils.SongTxtFiles(ops, songFolder)
	if err != nil {
		reportUnreadable(ops, songFolder, err)
		return false
	}
	if len(txts) == 0 {
		log.Debugf("Song folder %q has no song txt, leaving its files alone", songFolder)
		return false
	}
	// fixtxt leaves txts it cannot decode unambiguously untouched (and reports them); their
	// references may be stale or byte-encoded differently, so cleanup leaves the song alone
	// too until the txt is fixed.
	datas := make([][]byte, 0, len(txts))
	for _, txt := range txts {
		raw, err := ops.ReadFile(txt)
		if err != nil {
			reportUnreadable(ops, txt, err)
			return false
		}
		if d := utils.DecodeTxt(raw); d.Problem != "" {
			log.Infof("Not cleaning up %q: its song txt %s has an encoding issue (%s)", songFolder, filepath.Base(txt), d.Problem)
			return false
		}
		datas = append(datas, raw)
	}
	files, err := os.ReadDir(songFolder)
	if err != nil {
		reportUnreadable(ops, songFolder, err)
		return false
	}
	moved := false
	for _, file := range files {
		moved = handleFileCleanup(cfg, ops, songFolder, file, datas) || moved
	}
	return moved
}

// handleFileCleanup moves a single file to the extra-files folder unless it must be kept
// (txts are the contents of the folder's song txts), and reports whether it moved it (or
// tried to).
func handleFileCleanup(cfg config.Config, ops *fsops.Ops, songFolder string, file os.DirEntry, txts [][]byte) bool {
	name := file.Name()
	src := filepath.Join(songFolder, name)
	if !shouldConsider(songFolder, file) || ops.WouldBeMoved(src) {
		return false
	}
	if !utf8.ValidString(name) {
		// A CP1252 txt may reference this name byte-exactly, and once fixtxt converted the
		// txt to UTF-8 the reference no longer matches. fixtxt lists the name in its
		// encoding report; the user has to rename the file first.
		log.Warnf("Not moving %q: its name is not valid UTF-8, rename it first", src)
		return false
	}
	if utils.IsNameReferencedIn(txts, name) || isUntaggedMedia(ops, songFolder, name) {
		return false
	}
	rel := filepath.Join(cfg.RelPath(songFolder), name)
	if _, err := ops.MoveInto(src, cfg.ExtraFilesDir(), rel); err != nil {
		log.Errorf("moving unreferenced file to extra-files: %v", err)
	}
	return true
}

// shouldConsider reports whether file is a candidate for removal at all. Only regular files
// (or symlinks to them) are; folders, symlinks to folders, broken symlinks and special
// files are left alone. usdb files and txt files (any case) are always kept. Temp files
// left behind by an interrupted run are not referenced, so they are moved to extra-files
// like any other clutter; modules run one after another, so no temp file is in use while
// cleanup runs.
func shouldConsider(dir string, file os.DirEntry) bool {
	name := file.Name()
	switch {
	case !utils.IsRegularFile(dir, file):
		return false
	case strings.EqualFold(filepath.Ext(name), ".usdb"):
		return false
	case utils.IsTxtFile(name):
		return false
	}
	return true
}

// reportUnreadable logs an unreadable folder or song txt and counts it once per run. The
// key matches the one used by song folder discovery, so a path every module runs into is
// counted only once.
func reportUnreadable(ops *fsops.Ops, path string, err error) {
	path = fsops.ErrPath(err, path)
	log.Errorf("Skipping unreadable %s: %v", path, err)
	ops.FailUnreadable(path)
}

// isUntaggedMedia reports whether name is an audio file of a song whose txt has no
// #MP3/#AUDIO tag naming an existing file, a video file of a song without such a #VIDEO
// tag, or the image fixtxt would use as cover (background) of a song without such a
// #COVER (#BACKGROUND) tag. fixtxt inserts or fixes that tag, unless it cannot choose
// between several files or does not run (-t=false); either way the file is the song's
// media, not clutter. A tag naming a missing file (a typo, an old name) counts as absent:
// it references none of the files, so treating it as set would move all of them away. So
// does a tag naming a file of the wrong kind (#MP3 naming the video, #VIDEO an mp3), and an
// #MP3/#AUDIO naming the #VOCALS/#INSTRUMENTAL stem: fixtxt replaces such a value when it
// can choose (see fixtxt's resolveFileTag) and leaves it when it cannot, and then the files
// it would choose from are the ones that must stay.
// The txt is read through ops, so in a dry run the tags fixtxt would write count.
func isUntaggedMedia(ops *fsops.Ops, songFolder, name string) bool {
	isAudio, isVideo, isImage := utils.IsAudioFile(name), utils.IsVideoFile(name), utils.IsImageFile(name)
	if (!isAudio && !isVideo && !isImage) || utils.IsHidden(name) {
		// Hidden files (macOS "._x.mp3", leftovers of interrupted runs) are never media:
		// fixtxt never chooses them.
		return false
	}
	tags, err := utils.ReadSongTags(ops, songFolder)
	if err != nil {
		return true // no readable song txt: never move media
	}
	resolve := func(tag string) (string, bool) {
		onDisk, _, found := utils.ResolveFileName(songFolder, tags[tag])
		return onDisk, found
	}
	stems := map[string]bool{}
	for _, tag := range []string{"VOCALS", "INSTRUMENTAL"} {
		if onDisk, found := resolve(tag); found {
			stems[onDisk] = true
		}
	}
	// untagged reports whether tag names no existing file of the kind isKind (nil = any).
	untagged := func(tag string, isKind func(string) bool) bool {
		onDisk, found := resolve(tag)
		return !found || (isKind != nil && !isKind(onDisk))
	}
	if isImage {
		return (untagged("COVER", nil) && utils.FindCoverImage(ops, songFolder) == name) ||
			(untagged("BACKGROUND", nil) && utils.FindBackgroundImage(ops, songFolder) == name)
	}
	// name itself is a visible audio (video) file, so a file of the right kind exists. (A
	// stem is never moved anyway: its own tag references it.)
	mainAudio := func(n string) bool { return utils.IsAudioFile(n) && !stems[n] }
	if isAudio && untagged("MP3", mainAudio) && untagged("AUDIO", mainAudio) {
		return true
	}
	return isVideo && untagged("VIDEO", utils.IsVideoFile)
}
