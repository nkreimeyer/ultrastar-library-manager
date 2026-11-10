// Package fixtxt sanitizes the UltraStar song txt files of a library: it removes duplicate
// song txts from song folders, converts the txts to UTF-8 and fixes the #MP3/#AUDIO,
// #VIDEO, #COVER and #BACKGROUND tags so that they point to files that actually exist.
package fixtxt

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"sync"
	"unicode/utf8"

	"github.com/nkreimeyer/ultrastar-songs-sanitizer/config"
	"github.com/nkreimeyer/ultrastar-songs-sanitizer/fsops"
	"github.com/nkreimeyer/ultrastar-songs-sanitizer/utils"
	log "github.com/sirupsen/logrus"
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

// batchSize is the number of song folders processed concurrently.
const batchSize = 100

// Report file names (in the output dir).
const (
	missingFilesReport   = "missing_files.txt"
	encodingIssuesReport = "encoding_issues.txt"
)

// songFiles are the media files found in a song folder.
type songFiles struct {
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

// Run scans all song folders below cfg.SongDir and applies the text sanitization rules:
//   - no song txt, or neither audio nor video → the folder is moved to invalid/
//   - several song .txt files → the most recently modified one is kept, the others are
//     moved to extra-files/ (other txts such as License.txt are never touched)
//
// The song txt of every remaining folder is decoded, converted to UTF-8 and its file tags
// are fixed. Txts that cannot be decoded unambiguously are left untouched and listed in
// encoding_issues.txt. Songs without a cover or background are listed in missing_files.txt.
// Folders are processed concurrently, in batches.
func Run(cfg config.Config, ops *fsops.Ops) error {
	folders, err := utils.GetAllSongFolders(ops, cfg.SongDir)
	if err != nil {
		return fmt.Errorf("finding song folders in %q: %w", cfg.SongDir, err)
	}

	col := &collector{}
	for _, batch := range utils.SplitIntoBatches(folders, batchSize) {
		var wg sync.WaitGroup
		for _, folder := range batch {
			wg.Add(1)
			go func(folder string) {
				defer wg.Done()
				if checkFolderForDupTxtFiles(cfg, ops, folder) {
					handleDirectory(cfg, ops, col, folder)
				}
			}(folder)
		}
		wg.Wait()
	}

	writeReport(cfg, ops, missingFilesReport, col.missingFiles)
	writeReport(cfg, ops, encodingIssuesReport, col.encodingIssues)
	if n := len(col.encodingIssues); n > 0 {
		log.Warnf("%d song txt(s) have encoding issues and were not changed, see %s", n, cfg.ReportPath(encodingIssuesReport))
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

// checkFolderForDupTxtFiles makes sure folder contains at most one song txt. It reports
// whether the folder should be processed further; it returns false if the folder has no
// txt at all (it is moved to invalid/, or would be in dry-run mode) or cannot be read.
func checkFolderForDupTxtFiles(cfg config.Config, ops *fsops.Ops, folder string) bool {
	txtFiles, err := utils.ListTxtFiles(folder)
	if err != nil {
		log.Errorf("Could not read folder %s: %v", folder, err)
		ops.Fail()
		return false
	}

	switch len(txtFiles) {
	case 0:
		log.Infof("Folder %s contains no .txt file, moving it to invalid", folder)
		moveToInvalid(cfg, ops, folder)
		return false
	case 1:
		return true
	}

	// Only song txts are candidates; License.txt, readme.txt etc. stay where they are.
	var candidates []string
	for _, f := range txtFiles {
		if utils.IsSongTxt(f) {
			candidates = append(candidates, f)
		}
	}
	if len(candidates) < 2 {
		return true
	}

	keep, err := utils.NewestFile(candidates)
	if err != nil {
		log.Errorf("Could not determine the most recently modified song txt in %s: %v", folder, err)
		ops.Fail()
		return false
	}
	for _, f := range candidates {
		if f == keep {
			continue
		}
		rel := filepath.Join(cfg.RelPath(folder), filepath.Base(f))
		if _, err := ops.MoveInto(f, cfg.ExtraFilesDir(), rel); err != nil {
			log.Errorf("Could not move duplicate song txt: %v", err)
		}
	}
	log.Infof("Folder %s had %d song .txt files; kept the most recently modified one (%s)",
		folder, len(candidates), filepath.Base(keep))
	return true
}

// moveToInvalid moves a broken song folder to the invalid dir, mirroring its path
// relative to the library root. Errors are logged (and counted by ops).
func moveToInvalid(cfg config.Config, ops *fsops.Ops, folder string) {
	if _, err := ops.MoveInto(folder, cfg.InvalidDir(), cfg.RelPath(folder)); err != nil {
		log.Errorf("Could not move %s to invalid: %v", folder, err)
	}
}

// handleDirectory checks the files in a song folder and updates the file tags in its song
// txt. Songs without audio and video, or without a song txt, are moved to invalid/. Songs
// without cover or background are recorded in col.
func handleDirectory(cfg config.Config, ops *fsops.Ops, col *collector, rootDir string) {
	var files songFiles
	files.audio = usableNames(col, rootDir, utils.AudioFiles(rootDir))
	files.video = usableNames(col, rootDir, utils.VideoFiles(rootDir))

	// The video may contain the audio track, so only a song without both is broken.
	if len(files.audio) == 0 && len(files.video) == 0 {
		log.Infof("Folder %s has neither audio nor video, moving it to invalid", rootDir)
		moveToInvalid(cfg, ops, rootDir)
		return
	}

	txtFile, err := utils.FindSongTxt(rootDir)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			log.Infof("Folder %s has no UltraStar song txt, moving it to invalid", rootDir)
			moveToInvalid(cfg, ops, rootDir)
		} else {
			log.Errorf("Could not find the song txt in %s: %v", rootDir, err)
			ops.Fail()
		}
		return
	}

	files.cover = utils.FindCoverImage(rootDir)
	files.background = utils.FindBackgroundImage(rootDir)
	updateTxtFile(cfg, ops, col, rootDir, txtFile, files)

	var missing []string
	if files.cover == "" {
		missing = append(missing, "Missing cover file in "+rootDir)
	}
	if len(files.video) == 0 && files.background == "" {
		missing = append(missing, "No video or background file in "+rootDir)
	}
	if len(missing) > 0 {
		col.addMissing(missing...)
	}
}

// usableNames drops file names that are not valid UTF-8 (e.g. Latin-1 names from old
// archives): they cannot be written into a UTF-8 txt. They are reported as encoding issues.
func usableNames(col *collector, dir string, names []string) []string {
	usable := names[:0:0]
	for _, n := range names {
		if utf8.ValidString(n) {
			usable = append(usable, n)
		} else {
			col.addEncodingIssue(filepath.Join(dir, n), "file name is not valid UTF-8, rename it")
		}
	}
	return usable
}

// updateTxtFile rewrites txtFile as UTF-8 with fixed file tags (only if that changes the
// file). Txts that cannot be decoded unambiguously are left untouched and reported.
func updateTxtFile(cfg config.Config, ops *fsops.Ops, col *collector, rootDir, txtFile string, files songFiles) {
	raw, err := os.ReadFile(txtFile)
	if err != nil {
		log.Errorf("Could not read %s: %v", txtFile, err)
		ops.Fail()
		return
	}
	decoded := utils.DecodeTxt(raw)
	if decoded.Problem != "" {
		log.Warnf("Not changing %s: %s", txtFile, decoded.Problem)
		col.addEncodingIssue(txtFile, decoded.Problem)
		return
	}
	if decoded.Damaged {
		// Still safe to fix tags; the U+FFFD characters need a manual or assisted repair.
		col.addEncodingIssue(txtFile, "contains U+FFFD replacement characters (damaged by an earlier conversion)")
	}
	if decoded.Encoding != utils.EncUTF8 {
		log.Debugf("Decoded %s as %s", txtFile, decoded.Encoding)
	}

	updatedLines := fixTxtLines(splitLines(decoded.Text), rootDir, files)
	changed, err := ops.WriteFileAtomic(txtFile, []byte(strings.Join(updatedLines, "")))
	switch {
	case err != nil:
		log.Errorf("Could not write %s: %v", txtFile, err)
	case changed && !cfg.DryRun: // in dry-run mode ops already logged the planned write
		log.Infof("Rewrote %s (read as %s)", txtFile, decoded.Encoding)
	}
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

// fixTxtLines removes duplicate tags and a stale #ENCODING tag, and updates or inserts the
// #MP3/#AUDIO, #VIDEO, #COVER and #BACKGROUND tags. Tags that already point to an existing
// file of the right kind are left alone (cleanup takes care of unreferenced files later).
func fixTxtLines(lines []string, rootDir string, files songFiles) []string {
	// With fewer than 3 lines it's not a valid song txt, so return the lines unchanged.
	if len(lines) < 3 {
		return lines
	}

	lines = dropEncodingTag(removeDuplicateTags(lines))

	info := getSongInfoFromTxtFile(lines)
	// Images with the wrong shape for their tag are replaced; unreadable ones are re-detected.
	forceUpdateCover, forceUpdateBackground, info := checkDimensionsOfImagesTagged(rootDir, info)
	upd := tagUpdates{
		cover:      files.cover != "" && (!fileExists(rootDir, info.Cover) || forceUpdateCover),
		background: files.background != "" && (!fileExists(rootDir, info.Background) || forceUpdateBackground),
	}

	updatedLines := make([]string, len(lines))
	foundTagFlags := updateTagsInLines(rootDir, lines, updatedLines, files, upd)

	// Add the missing tags, inserting them as line 2.
	var linesToInsert []string
	if !isFlagSet(foundTagFlags, flagAudio) && len(files.audio) > 0 {
		linesToInsert = append(linesToInsert, "#MP3:"+files.audio[0]+"\n")
	}
	if !isFlagSet(foundTagFlags, flagVideo) && len(files.video) > 0 {
		linesToInsert = append(linesToInsert, "#VIDEO:"+files.video[0]+"\n")
	}
	if !isFlagSet(foundTagFlags, flagCover) && files.cover != "" {
		linesToInsert = append(linesToInsert, "#COVER:"+files.cover+"\n")
	}
	if !isFlagSet(foundTagFlags, flagBackground) && files.background != "" {
		linesToInsert = append(linesToInsert, "#BACKGROUND:"+files.background+"\n")
	}
	if len(linesToInsert) > 0 {
		log.Debugf("Inserting the following lines: %q", linesToInsert)
	}
	return slices.Insert(updatedLines, 1, linesToInsert...)
}

// fileExists reports whether name exists in dir with exactly this spelling (UltraStar
// opens files byte-for-byte on Linux).
func fileExists(dir, name string) bool {
	_, exact, found := utils.ResolveFileName(dir, name)
	return found && exact
}

// resolveFileTag decides the value of a file tag (#MP3/#AUDIO with the audio files as
// candidates, #VIDEO with the video files). A value naming an existing file of the right
// kind is kept, or corrected to the on-disk spelling if it only differs in case or Unicode
// normalization. A missing file, or a file of the wrong kind (e.g. #MP3 pointing to a
// video while an audio file exists, as left behind by an old bug), is replaced by the
// first candidate. It returns the new value and whether the tag has to be rewritten.
func resolveFileTag(rootDir, value string, candidates []string, isKind func(string) bool) (string, bool) {
	onDisk, exact, found := utils.ResolveFileName(rootDir, value)
	if found && (isKind(onDisk) || len(candidates) == 0) {
		if exact || !utf8.ValidString(onDisk) {
			return value, false
		}
		return onDisk, true
	}
	if len(candidates) > 0 && candidates[0] != value {
		return candidates[0], true
	}
	return value, false
}

// updateTagsInLines copies lines to updatedLines (which must have the same length),
// rewriting the file tags that need it. Lines that are not rewritten are copied unchanged.
// It returns the flags of the file tags that were found.
func updateTagsInLines(rootDir string, lines, updatedLines []string, files songFiles, upd tagUpdates) int {
	var found int
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
			if v, ok := resolveFileTag(rootDir, value, files.audio, utils.IsAudioFile); ok {
				rewrite(i, tag, v)
			}
		case hasTag(line, "#VIDEO"):
			found = setFlag(found, flagVideo)
			if v, ok := resolveFileTag(rootDir, tagValue(line, "#VIDEO"), files.video, utils.IsVideoFile); ok {
				rewrite(i, "#VIDEO", v)
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
	return found
}

// hasTag reports whether line is a header line of tag (e.g. "#VIDEO"). The tag is matched
// case-insensitively together with its colon, so "#VIDEO" does not match "#VIDEOGAP:".
func hasTag(line, tag string) bool {
	n := len(tag) + 1
	return len(line) >= n && strings.EqualFold(line[:n], tag+":")
}

// splitTag splits a header line into the upper-cased tag (with "#") and its trimmed value.
func splitTag(line string) (string, string) {
	name, value, _ := strings.Cut(line, ":")
	return strings.ToUpper(name), strings.TrimSpace(value)
}

// tagValue returns the trimmed value of a header line of tag.
func tagValue(line, tag string) string {
	return strings.TrimSpace(line[len(tag)+1:])
}

// checkDimensionsOfImagesTagged checks the images referenced by #COVER and #BACKGROUND.
// It reports whether the cover (not square) or background (square) has to be replaced.
// Tags pointing to unreadable images are cleared in the returned SongInfo.
func checkDimensionsOfImagesTagged(rootDir string, info SongInfo) (bool, bool, SongInfo) {
	forceUpdateCover := false
	forceUpdateBackground := false
	if info.Cover != "" {
		x, y, err := utils.GetImageDimensions(filepath.Join(rootDir, info.Cover))
		if err != nil {
			log.Debugf("Could not get image dimensions for %s in %s: %v", info.Cover, rootDir, err)
			info.Cover = ""
		} else if x != y {
			forceUpdateCover = true
		}
	}
	if info.Background != "" {
		x, y, err := utils.GetImageDimensions(filepath.Join(rootDir, info.Background))
		if err != nil {
			log.Debugf("Could not get image dimensions for %s in %s: %v", info.Background, rootDir, err)
			info.Background = ""
		} else if x == y {
			forceUpdateBackground = true
		}
	}
	return forceUpdateCover, forceUpdateBackground, info
}

// removeDuplicateTags drops every repetition of a header tag, keeping its first line.
// Tag names are compared case-insensitively ("#mp3:" duplicates "#MP3:").
func removeDuplicateTags(lines []string) []string {
	seen := make(map[string]bool)
	result := make([]string, 0, len(lines))
	for _, line := range lines {
		name, _, isTag := strings.Cut(line, ":")
		if !strings.HasPrefix(line, "#") || !isTag {
			result = append(result, line)
			continue
		}
		key := strings.ToUpper(name)
		if seen[key] {
			log.Debugf("Removing duplicate tag line %q", strings.TrimSpace(line))
			continue
		}
		seen[key] = true
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
		switch strings.ToUpper(tagValue(line, "#ENCODING")) {
		case "UTF8", "UTF-8":
			return false
		}
		log.Debugf("Removing stale tag line %q", strings.TrimSpace(line))
		return true
	})
}

// getTagValue returns the trimmed value of the first header line of tag (e.g. "#COVER"),
// or "" if there is none.
func getTagValue(lines []string, tag string) string {
	for _, line := range lines {
		if hasTag(line, tag) {
			return tagValue(line, tag)
		}
	}
	return ""
}

func setFlag(flags, flag int) int { return flags | flag }

func isFlagSet(flags, flag int) bool { return flags&flag != 0 }

func getSongInfoFromTxtFile(lines []string) SongInfo {
	return SongInfo{
		Artist:     getTagValue(lines, "#ARTIST"),
		Title:      getTagValue(lines, "#TITLE"),
		Cover:      getTagValue(lines, "#COVER"),
		Background: getTagValue(lines, "#BACKGROUND"),
	}
}
