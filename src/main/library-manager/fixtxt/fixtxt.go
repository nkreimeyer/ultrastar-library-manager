// Package fixtxt sanitizes the UltraStar song txt files of a library: it removes duplicate
// song txts from song folders, converts the txts to UTF-8, fixes the #MP3/#AUDIO,
// #VIDEO, #COVER and #BACKGROUND tags so that they point to files that actually exist, and
// fills a missing #ARTIST/#TITLE from an obvious "Artist - Title" folder name.
package fixtxt

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"sync"
	"unicode/utf8"

	"github.com/nkreimeyer/library-manager/config"
	"github.com/nkreimeyer/library-manager/fsops"
	"github.com/nkreimeyer/library-manager/songtxt"
	"github.com/nkreimeyer/library-manager/utils"
	log "github.com/sirupsen/logrus"
	"golang.org/x/text/unicode/norm"
)

// SongInfo holds the header tags fixtxt reads from a song txt.
type SongInfo struct {
	Artist     string
	Title      string
	Cover      string
	Background string
}

// Bit flags recording which file tags were found in a txt.
const (
	flagAudio = 1 << iota // #MP3 or #AUDIO
	flagVideo
	flagCover
	flagBackground
)

// workers is the number of song folders processed concurrently. It is the size of the
// batches the module used before its worker pool, so no more folders (and their open
// files) are worked on at the same time than before; the work is file I/O, not CPU.
const workers = 100

// Report file names (in the output dir).
const (
	missingFilesReport   = "missing_files.txt"
	encodingIssuesReport = "encoding_issues.txt"
	filledTagsReport     = "filled_tags.txt"
)

// Endings of the filled_tags.txt lines: a fill written by a real run, and one a dry run
// only planned. The report accumulates the written fills of all runs; planned lines are
// replaced by every run.
const (
	filledSuffix  = " (from the folder name)"
	plannedSuffix = " (from the folder name, dry run: not written yet)"
)

// songFiles are the media files found in a song folder.
type songFiles struct {
	txtName    string   // file name of the song txt
	audio      []string // audio file names
	video      []string // video file names
	cover      string   // square image, "" if none
	background string   // non-square image, "" if none
}

// tagUpdates says which image tags have to be rewritten.
type tagUpdates struct {
	cover, background bool
}

// collector gathers the results of one run from the concurrently processed folders.
type collector struct {
	mu             sync.Mutex
	missingFiles   []string
	encodingIssues []string
	filledTags     []string
	unchanged      int // songs left untouched because of an encoding issue
}

func (c *collector) addMissing(entries ...string) {
	c.mu.Lock()
	c.missingFiles = append(c.missingFiles, entries...)
	c.mu.Unlock()
}

func (c *collector) addEncodingIssue(path, problem string) {
	c.mu.Lock()
	c.encodingIssues = append(c.encodingIssues, path+": "+problem)
	c.mu.Unlock()
}

func (c *collector) addFilled(entries ...string) {
	c.mu.Lock()
	c.filledTags = append(c.filledTags, entries...)
	c.mu.Unlock()
}

// leftUnchanged counts a song that was not changed because of an encoding issue.
func (c *collector) leftUnchanged() {
	c.mu.Lock()
	c.unchanged++
	c.mu.Unlock()
}

// Options selects what fixtxt does; DefaultOptions is everything (what Run and the CLI do).
type Options struct {
	Encoding      bool                  // write song txts as UTF-8 (BOM, line endings, stale #ENCODING dropped)
	FileTags      bool                  // fix #MP3/#AUDIO/#VIDEO/#COVER/#BACKGROUND/#VOCALS/#INSTRUMENTAL (and the header's repeated tags)
	FillTags      bool                  // fill a missing #ARTIST/#TITLE from an obvious "Artist - Title" folder name
	DuplicateTxts bool                  // move all but the newest song txt of a folder to extra-files/
	MoveBroken    bool                  // move folders without a txt, without media or without a song txt to invalid/
	Ctx           context.Context       // nil = context.Background(); once done no further folder starts
	Progress      func(done, total int) // per folder, may be nil
}

// DefaultOptions returns the options with every step on.
func DefaultOptions() Options {
	return Options{Encoding: true, FileTags: true, FillTags: true, DuplicateTxts: true, MoveBroken: true}
}

// Run is RunWith with DefaultOptions.
func Run(cfg config.Config, ops *fsops.Ops) error {
	return RunWith(cfg, ops, DefaultOptions())
}

// RunWith scans all song folders below cfg.SongDir and applies the text sanitization rules
// opts selects (all of them with DefaultOptions):
//   - a media or image file whose name is not valid UTF-8 → the folder is reported in
//     encoding_issues.txt and left completely untouched
//   - no .txt at all, neither audio nor video, or no song txt → the folder is moved to
//     invalid/ as a whole (MoveBroken; otherwise it is logged and left alone)
//   - several song .txt files → the most recently modified one is kept, the others are
//     moved to extra-files/ (DuplicateTxts; otherwise they stay, and the newest one is still
//     the one fixed). Other txts such as License.txt are never touched.
//
// The song txt of every remaining folder is decoded, converted to UTF-8 (Encoding) and its
// file tags are fixed (FileTags). Txts that cannot be decoded unambiguously are left
// untouched and listed in encoding_issues.txt. A missing or empty #ARTIST/#TITLE is filled
// from the folder name if that is obvious (FillTags, utils.TagsFromFolderName). Every fill
// is added to filled_tags.txt for review (the report keeps the fills of earlier runs), and
// a tag that cannot be filled is noted in missing_files.txt. Songs without a cover or
// background, or with several audio (video) files to choose #MP3 (#VIDEO) from, are listed
// in missing_files.txt. Folders are processed concurrently by a pool of workers; a panic in
// one folder is logged and counted as a failure, and the other folders are still processed.
//
// Once opts.Ctx is done no further folder starts; the reports are then not written (a
// partial run would overwrite the full report of an earlier one) and ctx.Err() is returned.
func RunWith(cfg config.Config, ops *fsops.Ops, opts Options) error {
	ctx := opts.Ctx
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	folders, err := utils.GetAllSongFolders(ops, cfg.SongDir)
	if err != nil {
		return fmt.Errorf("finding song folders in %q: %w", cfg.SongDir, err)
	}

	col := &collector{}
	err = utils.ForEachParallel(ctx, folders, utils.Parallel{
		Workers:  workers,
		Progress: opts.Progress,
		OnPanic: func(i int, _ any) {
			log.Errorf("fixtxt: %s not finished: internal error (logged above)", folders[i])
			ops.Fail()
		},
	}, func(_ int, folder string) { folderHandler(cfg, ops, col, opts, folder) })
	if err != nil {
		log.Warnf("fixtxt stopped: %v (reports not written)", err)
		return err
	}

	writeReport(cfg, ops, missingFilesReport, col.missingFiles)
	writeReport(cfg, ops, encodingIssuesReport, col.encodingIssues)
	writeFilledReport(cfg, ops, col.filledTags)
	if n := col.unchanged; n > 0 {
		log.Warnf("%d song(s) have encoding issues and were not changed, see %s", n, cfg.ReportPath(encodingIssuesReport))
	}
	return nil
}

// writeReport writes the sorted lines to a report in the output dir. Sorting makes the
// output independent of goroutine scheduling.
func writeReport(cfg config.Config, ops *fsops.Ops, name string, lines []string) {
	sort.Strings(lines)
	report := strings.Join(lines, "\n")
	if report != "" {
		report += "\n"
	}
	path := cfg.ReportPath(name)
	if err := ops.WriteReport(path, []byte(report)); err != nil {
		log.Errorf("Could not write report: %v", err)
		return
	}
	log.Infof("Wrote %s", path)
}

// writeFilledReport writes filled_tags.txt: the fills of this run merged with the fills
// written by earlier runs, so that a later run (also a dry run) does not erase the list
// of guessed values before it was reviewed. Lines a dry run only planned are dropped.
func writeFilledReport(cfg config.Config, ops *fsops.Ops, lines []string) {
	path := cfg.ReportPath(filledTagsReport)
	old, err := os.ReadFile(path)
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		log.Errorf("Could not read %s, not updating it: %v", path, err)
		ops.Fail()
		return
	}
	for _, line := range strings.Split(string(old), "\n") {
		if line != "" && !strings.HasSuffix(line, plannedSuffix) {
			lines = append(lines, line)
		}
	}
	slices.Sort(lines)
	writeReport(cfg, ops, filledTagsReport, slices.Compact(lines))
}

// folderHandler is what RunWith runs per folder; a variable so tests can make it panic.
var folderHandler = handleFolder

// handleFolder checks a song folder and fixes its song txt. Broken songs (no txt, neither
// audio nor video, or no song txt) are moved to invalid/ as a whole (if opts.MoveBroken),
// before any duplicate song txt is split off. Folders that cannot be read, or that contain
// a media file whose name is not valid UTF-8, are left untouched.
func handleFolder(cfg config.Config, ops *fsops.Ops, col *collector, opts Options, folder string) {
	entries, err := os.ReadDir(folder)
	if err != nil {
		ops.SkipUnreadable(folder, err)
		return
	}
	// A txt references media byte-for-byte. Converting a CP1252 txt to UTF-8 would break
	// its reference to a Latin-1 file name, and a UTF-8 txt cannot reference it at all, so
	// such a folder must be renamed by hand first.
	if bad := utils.NonUTF8MediaNames(entries); len(bad) > 0 {
		for _, n := range bad {
			col.addEncodingIssue(filepath.Join(folder, n), "file name is not valid UTF-8, rename it")
		}
		log.Warnf("Not changing %s: %d media file name(s) are not valid UTF-8", folder, len(bad))
		col.leftUnchanged()
		return
	}

	txtFiles, err := utils.ListTxtFiles(folder)
	if err != nil {
		ops.SkipUnreadable(folder, err)
		return
	}
	if len(txtFiles) == 0 {
		handleBroken(cfg, ops, opts, folder, "contains no .txt file")
		return
	}
	// An unreadable txt might be the song txt: never mistake the folder for a broken song.
	songTxts, err := utils.SongTxtFiles(ops, folder)
	if err != nil {
		ops.SkipUnreadable(folder, err)
		return
	}

	var files songFiles
	files.audio = utils.AudioFiles(folder)
	files.video = utils.VideoFiles(folder)
	// The video may contain the audio track, so only a song without both is broken.
	if len(files.audio) == 0 && len(files.video) == 0 {
		handleBroken(cfg, ops, opts, folder, "has neither audio nor video")
		return
	}
	if len(songTxts) == 0 {
		handleBroken(cfg, ops, opts, folder, "has no UltraStar song txt")
		return
	}

	txtFile, ok := keepNewestSongTxt(cfg, ops, opts, folder, songTxts)
	if !ok {
		return
	}
	files.txtName = filepath.Base(txtFile)
	files.cover = utils.FindCoverImage(ops, folder)
	files.background = utils.FindBackgroundImage(ops, folder)
	// These notes only report, so they are written with FileTags off too. The notes follow
	// the tags the txt has after this run. If that is unknown (the txt
	// could not be read or decoded), they follow what was detected on disk.
	hasCover, hasBackground := files.cover != "", files.background != ""
	if final := updateTxtFile(cfg, ops, col, opts, folder, txtFile, files); final != nil {
		header := final[:headerLen(final)]
		hasCover = tagNamesImage(folder, header, "#COVER")
		hasBackground = tagNamesImage(folder, header, "#BACKGROUND")
	}

	if !hasCover {
		col.addMissing("Missing cover file in " + folder)
	}
	if len(files.video) == 0 && !hasBackground {
		col.addMissing("No video or background file in " + folder)
	}
}

// tagNamesImage reports whether the (last) header line of tag names a readable image in
// dir: the exact on-disk name (what a player opens on a case-sensitive file system, as
// fileExists checks), an image extension and a header utils.ReadImageHeader accepts. A
// corrupt image or a non-image does not count as a cover or background.
func tagNamesImage(dir string, header []string, tag string) bool {
	onDisk, exact, found := utils.ResolveFileName(dir, getTagValue(header, tag))
	if !found || !exact || !utils.IsImageFile(onDisk) {
		return false
	}
	_, err := utils.ReadImageHeader(filepath.Join(dir, onDisk))
	return err == nil
}

// keepNewestSongTxt makes sure folder keeps only one of its song txts: the most recently
// modified one stays, the others are moved to extra-files/ (other txts such as License.txt
// are not in songTxts and stay where they are). With opts.DuplicateTxts off the others
// stay too. It returns the song txt to fix and false if the folder cannot be processed.
func keepNewestSongTxt(cfg config.Config, ops *fsops.Ops, opts Options, folder string, songTxts []string) (string, bool) {
	keep, err := utils.NewestFile(songTxts)
	if err != nil {
		log.Errorf("Could not determine the most recently modified song txt in %s: %v", folder, err)
		ops.Fail()
		return "", false
	}
	if len(songTxts) < 2 {
		return keep, true
	}
	if !opts.DuplicateTxts {
		log.Infof("Folder %s has %d song .txt files; fixing the most recently modified one (%s), leaving the others",
			folder, len(songTxts), filepath.Base(keep))
		return keep, true
	}
	for _, f := range songTxts {
		if f == keep {
			continue
		}
		rel := filepath.Join(cfg.RelPath(folder), filepath.Base(f))
		if _, err := ops.MoveInto(f, cfg.ExtraFilesDir(), rel); err != nil {
			log.Errorf("Could not move duplicate song txt: %v", err)
		}
	}
	log.Infof("Folder %s had %d song .txt files; kept the most recently modified one (%s)",
		folder, len(songTxts), filepath.Base(keep))
	return keep, true
}

// handleBroken moves a broken song folder to invalid/ (moveToInvalid), or with
// opts.MoveBroken off only logs it. why completes "Folder <folder> ...".
func handleBroken(cfg config.Config, ops *fsops.Ops, opts Options, folder, why string) {
	if !opts.MoveBroken {
		log.Infof("Folder %s %s, leaving it (moving broken folders is off)", folder, why)
		return
	}
	log.Infof("Folder %s %s, moving it to invalid", folder, why)
	moveToInvalid(cfg, ops, folder)
}

// moveToInvalid moves a broken song folder to the invalid dir, mirroring its path
// relative to the library root. Errors are logged (and counted by ops).
func moveToInvalid(cfg config.Config, ops *fsops.Ops, folder string) {
	if _, err := ops.MoveInto(folder, cfg.InvalidDir(), cfg.RelPath(folder)); err != nil {
		log.Errorf("Could not move %s to invalid: %v", folder, err)
	}
}

// updateTxtFile rewrites txtFile as UTF-8 with fixed file tags (only if that changes the
// file). Txts that cannot be decoded unambiguously are left untouched and reported. A txt
// that is a symlink is never rewritten (that would replace the link by a regular file, and
// its target may lie outside the library); it is noted in missing_files.txt if it needs a
// change. It returns the lines the txt has after this run (in a dry run: the planned
// ones), or nil if the txt could not be read or decoded.
func updateTxtFile(cfg config.Config, ops *fsops.Ops, col *collector, opts Options, rootDir, txtFile string, files songFiles) []string {
	raw, err := ops.ReadFile(txtFile)
	if err != nil {
		log.Errorf("Could not read %s: %v", txtFile, err)
		ops.Fail()
		return nil
	}
	decoded := utils.DecodeTxt(raw)
	if decoded.Problem != "" {
		log.Warnf("Not changing %s: %s", txtFile, decoded.Problem)
		col.addEncodingIssue(txtFile, decoded.Problem)
		col.leftUnchanged()
		return nil
	}
	if decoded.Damaged {
		// Still safe to fix tags; the U+FFFD characters need a manual or assisted repair.
		col.addEncodingIssue(txtFile, "contains U+FFFD replacement characters (damaged by an earlier conversion)")
	}
	if decoded.Encoding != utils.EncUTF8 {
		log.Debugf("Decoded %s as %s", txtFile, decoded.Encoding)
	}

	lines := splitLines(decoded.Text)
	updatedLines, notes, filled := fixTxtLines(lines, rootDir, files, opts)
	col.addMissing(notes...)
	content := []byte(strings.Join(updatedLines, ""))
	needsWrite := !bytes.Equal(content, raw)
	if !opts.Encoding {
		// Only the other steps may cause a write. Every write is UTF-8, so a txt in another
		// encoding stays as it is. A UTF-8 txt that is written loses its BOM and CRLF
		// line endings on the way, which is harmless.
		needsWrite = string(content) != strings.Join(lines, "")
		if needsWrite && decoded.Encoding != utils.EncUTF8 {
			log.Warnf("Not changing %s: it is %s, and fixing it needs the UTF-8 conversion", txtFile, decoded.Encoding)
			col.addEncodingIssue(txtFile, "not UTF-8 (read as "+decoded.Encoding+"); fixing it needs the UTF-8 conversion")
			col.leftUnchanged()
			return lines
		}
	}
	if !needsWrite {
		return lines
	}
	if st, err := os.Lstat(txtFile); err == nil && st.Mode()&os.ModeSymlink != 0 {
		log.Warnf("Not changing %s: it is a symlink, replace it by the file it points to", txtFile)
		col.addMissing("Song txt is a symlink and needs changes, replace it by the file it points to: " + txtFile)
		return lines
	}
	changed, err := ops.WriteFileAtomic(txtFile, content)
	if err != nil {
		log.Errorf("Could not write %s: %v", txtFile, err)
		return lines
	}
	if changed && !cfg.DryRun { // in dry-run mode ops already logged the planned write
		log.Infof("Rewrote %s (read as %s)", txtFile, decoded.Encoding)
	}
	verb, suffix := "Filled", filledSuffix
	if cfg.DryRun {
		verb, suffix = "Would fill", plannedSuffix
	}
	for _, f := range filled {
		log.Infof("%s %s in %s from the folder name", verb, f, txtFile)
		col.addFilled(txtFile + ": " + f + suffix)
	}
	return updatedLines
}

// splitLines splits text into lines that each keep their trailing "\n". A missing final
// newline is added.
func splitLines(text string) []string {
	if text == "" {
		return nil
	}
	lines := strings.SplitAfter(text, "\n")
	if last := lines[len(lines)-1]; last == "" {
		lines = lines[:len(lines)-1]
	} else {
		lines[len(lines)-1] = last + "\n"
	}
	return lines
}

// fixTxtLines applies the txt steps opts selects to the lines of a song txt: it drops a
// stale #ENCODING tag (Encoding), fills a missing #ARTIST or #TITLE from the folder name
// (FillTags, see fillNameTags), and removes duplicate tags and updates or inserts the
// #MP3/#AUDIO, #VIDEO, #COVER and #BACKGROUND tags (FileTags, see fixFileTags). Only the
// header is changed (see headerLen): "#" lines after it are ignored by players and copied
// unchanged, like the notes. It also returns notes for missing_files.txt (an audio file
// that cannot be chosen, a name tag that cannot be filled) and the filled tags
// ("#ARTIST:value"). The lines passed in are not modified.
func fixTxtLines(lines []string, rootDir string, files songFiles, opts Options) ([]string, []string, []string) {
	// With fewer than 3 lines it's not a valid song txt, so return the lines unchanged.
	if len(lines) < 3 {
		return lines, nil, nil
	}

	n := headerLen(lines)
	body := lines[n:]
	// A copy: fillNameTags and dropEncodingTag change the slice they get.
	header := slices.Clone(lines[:n])
	if opts.FileTags {
		header = removeDuplicateTags(header)
	}
	if opts.Encoding {
		header = dropEncodingTag(header)
	}

	info := getSongInfoFromTxtFile(header)
	var nameLines, filled, notes []string
	if opts.FillTags {
		// Without FileTags the header may repeat a tag; nameTagLine fills the last one,
		// the one players read.
		nameLines, filled, notes = fillNameTags(header, rootDir, &info)
	}
	updatedLines := header
	if opts.FileTags {
		var tagNotes []string
		updatedLines, tagNotes = fixFileTags(header, rootDir, files, info)
		notes = append(notes, tagNotes...)
	}

	// A filled #TITLE/#ARTIST goes to the top of the header, as players write it, but
	// after a leading #VERSION: format 1.x wants it as the first line.
	at := 0
	if len(updatedLines) > 0 && hasTag(updatedLines[0], "#VERSION") {
		at = 1
	}
	updatedLines = slices.Insert(updatedLines, at, nameLines...)
	return append(updatedLines, body...), notes, filled
}

// fixFileTags updates or inserts the #MP3/#AUDIO, #VIDEO, #COVER and #BACKGROUND tags of a
// header without repeated tags. Tags that already point to an existing file of the right
// kind are left alone (cleanup takes care of unreferenced files later). info holds the
// header's tags (with filled name tags, which the audio choice uses). It returns the new
// header and notes for missing_files.txt (several audio or video files to choose from).
func fixFileTags(lines []string, rootDir string, files songFiles, info SongInfo) ([]string, []string) {
	var notes []string
	video := chooseVideo(rootDir, lines, files, info)
	audio := preferTaggedAudio(rootDir, lines, chooseAudio(rootDir, lines, files, info, video))
	// Images with the wrong shape for their tag are replaced; unreadable ones are re-detected.
	forceUpdateCover, forceUpdateBackground, info := checkDimensionsOfImagesTagged(rootDir, info)
	upd := tagUpdates{
		cover:      files.cover != "" && (!fileExists(rootDir, info.Cover) || forceUpdateCover),
		background: files.background != "" && (!fileExists(rootDir, info.Background) || forceUpdateBackground),
	}
	// FindCoverImage and FindBackgroundImage never pick the same image, but the image one
	// of them picks may be the one the other tag keeps. Never point both tags at one file.
	if files.cover != "" && !upd.background && sameFileName(files.cover, info.Background) {
		upd.cover = false
		files.cover = ""
	}
	if files.background != "" && !upd.cover && sameFileName(files.background, info.Cover) {
		upd.background = false
		files.background = ""
	}

	updatedLines := make([]string, len(lines))
	foundTagFlags, audioUnresolved, videoUnresolved := updateTagsInLines(rootDir, lines, updatedLines, files, audio, video, upd)

	if (audioUnresolved || !isFlagSet(foundTagFlags, flagAudio)) && len(audio.ambiguous) > 0 {
		log.Warnf("Several audio files in %s, cannot choose #MP3: %q", rootDir, audio.ambiguous)
		notes = append(notes, fmt.Sprintf("Several audio files, cannot choose #MP3 in %s: %s",
			rootDir, strings.Join(audio.ambiguous, ", ")))
	}
	// Without audio candidates the audio tag takes the video choice, so this note covers
	// an #MP3 left out for the same reason.
	if (videoUnresolved || !isFlagSet(foundTagFlags, flagVideo)) && len(video.ambiguous) > 0 {
		log.Warnf("Several video files in %s, cannot choose #VIDEO: %q", rootDir, video.ambiguous)
		notes = append(notes, fmt.Sprintf("Several video files, cannot choose #VIDEO in %s: %s",
			rootDir, strings.Join(video.ambiguous, ", ")))
	}

	// Add the missing file tags, inserting them as line 2.
	var linesToInsert []string
	if !isFlagSet(foundTagFlags, flagAudio) && audio.name != "" {
		linesToInsert = append(linesToInsert, "#MP3:"+audio.name+"\n")
	}
	if !isFlagSet(foundTagFlags, flagVideo) && video.name != "" {
		linesToInsert = append(linesToInsert, "#VIDEO:"+video.name+"\n")
	}
	if !isFlagSet(foundTagFlags, flagCover) && files.cover != "" {
		linesToInsert = append(linesToInsert, "#COVER:"+files.cover+"\n")
	}
	if !isFlagSet(foundTagFlags, flagBackground) && files.background != "" {
		linesToInsert = append(linesToInsert, "#BACKGROUND:"+files.background+"\n")
	}
	if len(linesToInsert) > 0 {
		log.Debugf("Inserting into %s: %q", rootDir, linesToInsert)
	}
	// A header-less txt (only possible for an odd file) gets the lines at the top.
	return slices.Insert(updatedLines, min(1, len(updatedLines)), linesToInsert...), notes
}

// fillNameTags fills a missing or empty #TITLE and #ARTIST from the folder name of the
// song, if utils.TagsFromFolderName finds it obvious. An empty tag line gets the value in
// place (lines is modified, the tag keeps its spelling); an absent tag is returned as a
// line to insert (#TITLE before #ARTIST). Tag lines are found the way players and check
// read them (see nameTagLine). info is updated with the filled values, so that
// the audio choice can use them. lines must not contain repeated tags. It also returns the
// filled tags ("#ARTIST:value") and a note for missing_files.txt if a missing tag cannot be
// filled.
func fillNameTags(lines []string, rootDir string, info *SongInfo) (insert, filled, notes []string) {
	fill := utils.TagsFromFolderName(filepath.Base(rootDir), info.Artist, info.Title)
	if fill.Reason != "" {
		var missing []string
		for _, m := range []struct{ tag, value string }{{"#ARTIST", info.Artist}, {"#TITLE", info.Title}} {
			if m.value == "" {
				missing = append(missing, m.tag)
			}
		}
		what := strings.Join(missing, " and ")
		log.Warnf("%s missing in %s, cannot fill it from the folder name: %s", what, rootDir, fill.Reason)
		notes = append(notes, fmt.Sprintf("Missing %s in %s, cannot fill it from the folder name: %s", what, rootDir, fill.Reason))
	}
	for _, f := range []struct {
		tag   string
		ok    bool
		value string
		dst   *string
	}{
		{"#TITLE", fill.FillTitle, fill.Title, &info.Title},
		{"#ARTIST", fill.FillArtist, fill.Artist, &info.Artist},
	} {
		if !f.ok {
			continue
		}
		*f.dst = f.value
		filled = append(filled, f.tag+":"+f.value)
		if i, _ := nameTagLine(lines, f.tag); i >= 0 {
			colon := strings.IndexByte(lines[i], ':')
			lines[i] = lines[i][:colon+1] + f.value + "\n"
			continue
		}
		insert = append(insert, f.tag+":"+f.value+"\n")
	}
	return insert, filled, notes
}

// fileChoice is the value to use for a missing or wrong file tag.
type fileChoice struct {
	name       string   // replacement value, "" if there is none
	candidates int      // number of files of the tag's own kind
	ambiguous  []string // the candidates, if there are several and none is preferred
	// stems are the on-disk names of the #VOCALS/#INSTRUMENTAL files (audio choices only):
	// they are no candidates, and an #MP3/#AUDIO naming one is of the wrong kind (see
	// isMainAudio).
	stems map[string]bool
}

// isMainAudio reports whether name is an audio file and not one of choice's stems: the
// kind an #MP3/#AUDIO tag must name. An audio tag naming the vocals stem while the full
// mix is there would make the song play only the vocals (and cleanup move the mix away).
func (choice fileChoice) isMainAudio(name string) bool {
	return utils.IsAudioFile(name) && !choice.stems[name]
}

// chooseAudio picks the file for a missing or wrong #MP3/#AUDIO tag. Audio files used by
// #VOCALS or #INSTRUMENTAL are not candidates. A single candidate is used. Of several, the
// one named like the song txt, the folder or "<#ARTIST> - <#TITLE>" is used; otherwise
// there is no choice and the candidates are returned as ambiguous. Without any candidate
// the video choice is used (none if it is ambiguous): the video carries the audio, and
// UltraStar needs an audio tag.
func chooseAudio(rootDir string, lines []string, files songFiles, info SongInfo, video fileChoice) fileChoice {
	stems := map[string]bool{}
	for _, tag := range []string{"#VOCALS", "#INSTRUMENTAL"} {
		if onDisk, _, found := utils.ResolveFileName(rootDir, getTagValue(lines, tag)); found {
			stems[onDisk] = true
		}
	}
	var candidates []string
	for _, a := range files.audio {
		if !stems[a] {
			candidates = append(candidates, a)
		}
	}

	choice := fileChoice{candidates: len(candidates), stems: stems}
	switch len(candidates) {
	case 0:
		choice.name = video.name
		return choice
	case 1:
		choice.name = candidates[0]
		return choice
	}
	if choice.name = preferredFile(rootDir, candidates, files, info); choice.name == "" {
		choice.ambiguous = candidates
	}
	return choice
}

// chooseVideo picks the file for a missing or wrong #VIDEO tag (and the audio tag of a
// song without audio files, see chooseAudio). A single video is used. Of several, the one
// an existing #VIDEO, #MP3 or #AUDIO line names (in this order) is used, then the one
// named like the song txt, the folder or "<#ARTIST> - <#TITLE>"; otherwise there is no
// choice and the videos are returned as ambiguous (the first by name would be a guess:
// "Song (lyrics).mp4" before "Song.mp4").
func chooseVideo(rootDir string, lines []string, files songFiles, info SongInfo) fileChoice {
	choice := fileChoice{candidates: len(files.video)}
	switch len(files.video) {
	case 0:
		return choice
	case 1:
		choice.name = files.video[0]
		return choice
	}
	for _, tag := range []string{"#VIDEO", "#MP3", "#AUDIO"} {
		if onDisk, _, found := utils.ResolveFileName(rootDir, getTagValue(lines, tag)); found && slices.Contains(files.video, onDisk) {
			choice.name = onDisk
			return choice
		}
	}
	if choice.name = preferredFile(rootDir, files.video, files, info); choice.name == "" {
		choice.ambiguous = files.video
	}
	return choice
}

// preferredFile returns the only one of candidates named (without extension) like the
// song txt, else the only one named like the folder, else the only one named
// "<#ARTIST> - <#TITLE>", and "" if no preference picks exactly one.
func preferredFile(rootDir string, candidates []string, files songFiles, info SongInfo) string {
	preferred := []string{stem(files.txtName), filepath.Base(rootDir)}
	if info.Artist != "" && info.Title != "" {
		preferred = append(preferred, info.Artist+" - "+info.Title)
	}
	for _, want := range preferred {
		var matches []string
		for _, c := range candidates {
			if sameName(stem(c), want) {
				matches = append(matches, c)
			}
		}
		if len(matches) == 1 {
			return matches[0]
		}
	}
	return ""
}

// preferTaggedAudio resolves an ambiguous audio choice with the txt itself: if an #MP3 or
// #AUDIO line already names one of the candidates, that file is the obvious value for a
// stale sibling tag (format 1.1 txts carry both, and players use #AUDIO).
func preferTaggedAudio(rootDir string, header []string, audio fileChoice) fileChoice {
	for _, line := range header {
		if !hasTag(line, "#MP3") && !hasTag(line, "#AUDIO") {
			continue
		}
		if onDisk, _, found := utils.ResolveFileName(rootDir, tagValue(line)); found && slices.Contains(audio.ambiguous, onDisk) {
			return fileChoice{name: onDisk, candidates: audio.candidates, stems: audio.stems}
		}
	}
	return audio
}

// stem returns name without its extension.
func stem(name string) string {
	return strings.TrimSuffix(name, filepath.Ext(name))
}

// sameName compares two file names case- and Unicode-normalization-insensitively.
func sameName(a, b string) bool {
	return a != "" && strings.EqualFold(norm.NFC.String(a), norm.NFC.String(b))
}

// fileExists reports whether name exists in dir with exactly this spelling (UltraStar
// opens files byte-for-byte on Linux).
func fileExists(dir, name string) bool {
	_, exact, found := utils.ResolveFileName(dir, name)
	return found && exact
}

// resolveFileTag decides the value of a file tag (#MP3/#AUDIO, #VIDEO). A value naming an
// existing file of the right kind is kept, or corrected to the on-disk spelling if it only
// differs in case or Unicode normalization. A file of another kind is kept too if there is
// no file of the right kind (#MP3 may name the video of a video-only song). A missing
// file, or a file of the wrong kind (e.g. #MP3 pointing to a video while an audio file
// exists, as left behind by an old bug), is replaced by choice.name. It returns the new
// value, whether the tag has to be rewritten, and whether it would need a new value but
// there is no choice.
func resolveFileTag(rootDir, value string, choice fileChoice, isKind func(string) bool) (string, bool, bool) {
	onDisk, exact, found := utils.ResolveFileName(rootDir, value)
	if found && (isKind(onDisk) || choice.candidates == 0) {
		if exact || !utf8.ValidString(onDisk) {
			return value, false, false
		}
		return onDisk, true, false
	}
	if choice.name == "" {
		return value, false, true
	}
	if choice.name != value {
		return choice.name, true, false
	}
	return value, false, false
}

// updateTagsInLines copies lines to updatedLines (which must have the same length),
// rewriting the file tags that need it. Lines that are not rewritten are copied unchanged.
// It returns the flags of the file tags that were found, and whether the audio (video) tag
// needs a new value that audio (video) does not provide.
func updateTagsInLines(rootDir string, lines, updatedLines []string, files songFiles, audio, video fileChoice, upd tagUpdates) (int, bool, bool) {
	var found int
	audioUnresolved, videoUnresolved := false, false
	rewrite := func(i int, tag, value string) {
		updatedLines[i] = tag + ":" + value + "\n"
		log.Debugf("Updated %s in %s to %s", tag, rootDir, value)
	}
	for i, line := range lines {
		updatedLines[i] = line
		switch {
		case hasTag(line, "#MP3"), hasTag(line, "#AUDIO"):
			found = setFlag(found, flagAudio)
			tag, value := splitTag(line)
			v, ok, unresolved := resolveFileTag(rootDir, value, audio, audio.isMainAudio)
			if ok {
				rewrite(i, tag, v)
			}
			audioUnresolved = audioUnresolved || unresolved
		case hasTag(line, "#VIDEO"):
			found = setFlag(found, flagVideo)
			v, ok, unresolved := resolveFileTag(rootDir, tagValue(line), video, utils.IsVideoFile)
			if ok {
				rewrite(i, "#VIDEO", v)
			}
			videoUnresolved = videoUnresolved || unresolved
		case hasTag(line, "#VOCALS"), hasTag(line, "#INSTRUMENTAL"):
			// Stems are never chosen or inserted, only spelled exactly like the file on disk.
			tag, value := splitTag(line)
			if onDisk, exact, found := utils.ResolveFileName(rootDir, value); found && !exact && utf8.ValidString(onDisk) {
				rewrite(i, tag, onDisk)
			}
		case hasTag(line, "#COVER"):
			found = setFlag(found, flagCover)
			if upd.cover {
				rewrite(i, "#COVER", files.cover)
			}
		case hasTag(line, "#BACKGROUND"):
			found = setFlag(found, flagBackground)
			if upd.background {
				rewrite(i, "#BACKGROUND", files.background)
			}
		}
	}
	return found, audioUnresolved, videoUnresolved
}

// parseTagLine splits a header line the way players and the songtxt parser read it:
// leading blanks and blanks around the key are ignored ("  #Mp3 :x"). It returns the key
// without "#" and the trimmed value; ok is false if line is not a tag line.
func parseTagLine(line string) (key, value string, ok bool) {
	body, ok := strings.CutPrefix(strings.TrimLeft(line, " \t"), "#")
	if !ok {
		return "", "", false
	}
	key, value, ok = strings.Cut(body, ":")
	key = strings.TrimSpace(key)
	if !ok || key == "" {
		return "", "", false
	}
	return key, strings.TrimSpace(value), true
}

// hasTag reports whether line is a header line of tag (e.g. "#VIDEO"). The tag is matched
// case-insensitively as a whole key, so "#VIDEO" does not match "#VIDEOGAP:".
func hasTag(line, tag string) bool {
	key, _, ok := parseTagLine(line)
	return ok && strings.EqualFold(key, tag[1:])
}

// splitTag splits a header line into the upper-cased tag (with "#") and its trimmed value.
func splitTag(line string) (string, string) {
	key, value, _ := parseTagLine(line)
	return "#" + strings.ToUpper(key), value
}

// tagValue returns the trimmed value of a header line.
func tagValue(line string) string {
	_, value, _ := parseTagLine(line)
	return value
}

// headerLen returns the number of header lines. The header ends at the first body line,
// the same lines at which songtxt.Parse switches to the body (songtxt.IsBodyLine); "#" lines
// after it are ignored by players and never treated as tags. Other lines (blank lines,
// junk such as "Created with editor") do not end the header and are copied unchanged.
func headerLen(lines []string) int {
	for i, line := range lines {
		if songtxt.IsBodyLine(line) {
			return i
		}
	}
	return len(lines)
}

// sameFileName reports whether a tag value names the on-disk file name (case-insensitive,
// as utils.ResolveFileName matches).
func sameFileName(onDisk, tagValue string) bool {
	return tagValue != "" && strings.EqualFold(onDisk, strings.TrimSpace(tagValue))
}

// checkDimensionsOfImagesTagged checks the images referenced by #COVER and #BACKGROUND.
// It reports whether the cover (not square) or background (square) has to be replaced,
// using the same shape rule as utils.FindCoverImage (utils.IsSquareImage).
// Tags pointing to unreadable images are cleared in the returned SongInfo.
func checkDimensionsOfImagesTagged(rootDir string, info SongInfo) (bool, bool, SongInfo) {
	forceUpdateCover := false
	forceUpdateBackground := false
	if info.Cover != "" {
		x, y, err := utils.GetImageDimensions(filepath.Join(rootDir, info.Cover))
		if err != nil {
			log.Debugf("Could not get image dimensions for %s in %s: %v", info.Cover, rootDir, err)
			info.Cover = ""
		} else if !utils.IsSquareImage(x, y) {
			forceUpdateCover = true
		}
	}
	if info.Background != "" {
		x, y, err := utils.GetImageDimensions(filepath.Join(rootDir, info.Background))
		if err != nil {
			log.Debugf("Could not get image dimensions for %s in %s: %v", info.Background, rootDir, err)
			info.Background = ""
		} else if utils.IsSquareImage(x, y) {
			forceUpdateBackground = true
		}
	}
	return forceUpdateCover, forceUpdateBackground, info
}

// removeDuplicateTags drops every repetition of a header tag, keeping its LAST line:
// UltraStar Deluxe and Performous read the header top to bottom and let a later tag
// overwrite an earlier one, so the last value is the one that plays. Tag names are
// compared case-insensitively and without surrounding blanks ("#mp3 :" duplicates
// "#MP3:"). lines must be the header only (see headerLen).
func removeDuplicateTags(lines []string) []string {
	tagKey := func(line string) (string, bool) {
		key, _, ok := parseTagLine(line)
		return strings.ToUpper(key), ok
	}
	last := make(map[string]int)
	for i, line := range lines {
		if key, ok := tagKey(line); ok {
			last[key] = i
		}
	}
	result := make([]string, 0, len(lines))
	for i, line := range lines {
		if key, ok := tagKey(line); ok && last[key] != i {
			log.Debugf("Removing duplicate tag line %q (a later one overrides it)", strings.TrimSpace(line))
			continue
		}
		result = append(result, line)
	}
	return result
}

// dropEncodingTag removes an #ENCODING tag that does not say UTF-8. The file is written as
// UTF-8, and players honour the tag, so a stale "#ENCODING:CP1252" would turn every umlaut
// into mojibake. UltraStar format 1.0 deprecates the tag anyway.
func dropEncodingTag(lines []string) []string {
	return slices.DeleteFunc(lines, func(line string) bool {
		if !hasTag(line, "#ENCODING") {
			return false
		}
		switch strings.ToUpper(tagValue(line)) {
		case "UTF8", "UTF-8":
			return false
		}
		log.Debugf("Removing stale tag line %q", strings.TrimSpace(line))
		return true
	})
}

// getTagValue returns the trimmed value of the last header line of tag (e.g. "#COVER"),
// the one players use, or "" if there is none.
func getTagValue(lines []string, tag string) string {
	value := ""
	for _, line := range lines {
		if hasTag(line, tag) {
			value = tagValue(line)
		}
	}
	return value
}

func setFlag(flags, flag int) int { return flags | flag }

func isFlagSet(flags, flag int) bool { return flags&flag != 0 }

// nameTagLine finds the line of tag (e.g. "#ARTIST") the way players and the songtxt
// parser read it: leading blanks and blanks around the key are ignored ("  #Artist :X"),
// and of repeated lines the last one wins. It returns the line index and the trimmed
// value, or -1 and "" if there is no such line.
func nameTagLine(lines []string, tag string) (int, string) {
	idx, value := -1, ""
	for i, line := range lines {
		if hasTag(line, tag) {
			idx, value = i, tagValue(line)
		}
	}
	return idx, value
}

func getSongInfoFromTxtFile(lines []string) SongInfo {
	_, artist := nameTagLine(lines, "#ARTIST")
	_, title := nameTagLine(lines, "#TITLE")
	return SongInfo{
		Artist:     artist,
		Title:      title,
		Cover:      getTagValue(lines, "#COVER"),
		Background: getTagValue(lines, "#BACKGROUND"),
	}
}
