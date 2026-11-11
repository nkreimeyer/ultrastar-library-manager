// Package fixvideos re-encodes song videos with ffmpeg: inefficient codecs and high-bitrate
// videos are converted to H.264 MP4, videos larger than Full HD (wider than 1920 or taller
// than 1080 px) are downscaled, and the audio track is stripped when the song has a separate
// audio file. Videos ffmpeg cannot read are listed in potentiallyCorruptedFiles.txt in the
// output folder.
//
// Every video written gets the metadata tag SANITIZER_VIDEO=processed, and tagged videos are
// skipped on later runs. A video whose re-encode would be larger than the original is
// tagged by a lossless remux, so it is not re-encoded on every run. Containers that cannot
// carry the tag (AVI, MPEG) are remembered in the media journal instead.
package fixvideos

import (
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"

	"github.com/nkreimeyer/ultrastar-songs-sanitizer/config"
	"github.com/nkreimeyer/ultrastar-songs-sanitizer/fsops"
	"github.com/nkreimeyer/ultrastar-songs-sanitizer/journal"
	"github.com/nkreimeyer/ultrastar-songs-sanitizer/utils"

	log "github.com/sirupsen/logrus"
)

const (
	bitrateThreshold = 3000000 // 3 Mbit/s

	// markerTag is written into every video fixvideos writes; tagged videos are skipped.
	markerTag   = "SANITIZER_VIDEO"
	markerValue = "processed"

	// Media journal kinds for videos whose container cannot carry markerTag.
	journalChecked   = "video-checked"   // processing would not help (result was larger)
	journalProcessed = "video-processed" // written by fixvideos

	// durationTolerance is the maximum duration difference (in seconds) between an
	// original and its tagged remux.
	durationTolerance = 0.5

	// corruptedReportName is the report listing videos ffmpeg could not read.
	corruptedReportName = "potentiallyCorruptedFiles.txt"

	// exitInvalidData is ffmpeg's exit code for AVERROR_INVALIDDATA ("Invalid data found
	// when processing input"), i.e. the input file is probably corrupted.
	exitInvalidData = 183

	showEntries   = "-show_entries"
	selectStreams = "-select_streams"
)

// Videos larger than maxWidth x maxHeight are downscaled to fit within it. They are
// variables so tests can lower them.
var maxWidth, maxHeight = 1920, 1080

// workers is the number of videos processed in parallel (each ffmpeg is multi-threaded).
var workers = 4

// markerExts are the containers verified to keep markerTag through "-c copy" (ffmpeg 5.1+);
// mp4Exts additionally need "-movflags +use_metadata_tags" for custom tags. AVI and MPEG
// program/elementary streams silently drop it.
var (
	markerExts = map[string]bool{".mp4": true, ".m4v": true, ".mov": true, ".mkv": true, ".webm": true, ".flv": true, ".wmv": true}
	mp4Exts    = map[string]bool{".mp4": true, ".m4v": true, ".mov": true}
)

// codecsNeedReencode lists video codecs worth converting to H.264: vp9, av1 and hevc are
// too new for some players, everything older than H.264 is inefficient.
var codecsNeedReencode = []string{"vp9", "av1", "hevc", "mpeg4", "mpeg2video", "msmpeg4v2", "msmpeg4v3", "flv1", "h263", "h263p"}

// processor holds the per-run state shared by all workers.
type processor struct {
	cfg     config.Config
	ops     *fsops.Ops
	journal *journal.Journal

	// renameMu serializes the extension-change step (rename, txt update, moving the
	// original), so two videos of one song folder never update its txt concurrently.
	renameMu sync.Mutex

	mu        sync.Mutex
	corrupted map[string]struct{} // videos ffmpeg could not read
}

// Run processes the videos of all song folders below cfg.SongDir with a pool of workers,
// then writes the corrupted-files report and the media journal.
func Run(cfg config.Config, ops *fsops.Ops) error {
	for _, tool := range []string{"ffmpeg", "ffprobe"} {
		if _, err := exec.LookPath(tool); err != nil {
			return fmt.Errorf("%s is required for the video module: %w", tool, err)
		}
	}

	entries, err := utils.GetAllSongFolders(ops, cfg.SongDir)
	if err != nil {
		return fmt.Errorf("reading song dir %q: %w", cfg.SongDir, err)
	}

	// Without the journal, videos recorded there would be re-encoded and discarded again,
	// and saving it would drop the entries that could not be read.
	j, err := journal.Open(cfg.JournalPath())
	if err != nil {
		return fmt.Errorf("reading media journal %q: %w", cfg.JournalPath(), err)
	}

	p := &processor{cfg: cfg, ops: ops, journal: j, corrupted: map[string]struct{}{}}

	videos := p.collectVideos(entries)
	queue := make(chan string)
	var wg sync.WaitGroup
	for range workers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for file := range queue {
				p.processVideoFile(filepath.Dir(file), file)
			}
		}()
	}
	for _, file := range videos {
		queue <- file
	}
	close(queue)
	wg.Wait()

	p.writeCorruptedReport()
	// Nothing is added in dry-run mode; the check only makes sure the out dir stays empty.
	if !cfg.DryRun {
		if err := j.Save(ops); err != nil {
			log.Errorf("%v", err) // counted as a failure by ops
		}
	}
	log.Infof("All video files have been processed.")
	return nil
}

// collectVideos returns the paths of the visible video files directly inside the song
// folders dirs.
func (p *processor) collectVideos(dirs []string) []string {
	var videos []string
	for _, dir := range dirs {
		files, err := os.ReadDir(dir)
		if err != nil {
			log.Errorf("reading directory %q: %v", dir, err)
			p.ops.FailOnce("unreadable:" + dir) // same key as the song folder discovery
			continue
		}
		for _, file := range files {
			// Hidden files are leftovers of interrupted runs or AppleDouble files.
			if utils.IsHidden(file.Name()) || !utils.IsVideoFile(file.Name()) || !utils.IsRegularFile(dir, file) {
				continue
			}
			videos = append(videos, filepath.Join(dir, file.Name()))
		}
	}
	return videos
}

// encodePlan describes what ffmpeg has to do with a video.
type encodePlan struct {
	removeAudio bool
	reEncode    bool
	resize      bool
	crf         int // only used when reEncode is set
}

// outputExt returns the extension of the processed file: re-encoded videos become MP4,
// otherwise the streams are copied into the original container.
func (pl encodePlan) outputExt(input string) string {
	if pl.reEncode {
		return ".mp4"
	}
	return filepath.Ext(input)
}

// renames reports whether the processed file gets a different name than input. An
// extension that only changes its case (v.MP4 -> .mp4) does not count: the original is
// replaced in place, because on case-insensitive file systems both names are the same file.
func (pl encodePlan) renames(input string) bool {
	return !strings.EqualFold(pl.outputExt(input), filepath.Ext(input))
}

// describe returns a human-readable summary of the planned actions.
func (pl encodePlan) describe() string {
	var actions []string
	if pl.reEncode {
		actions = append(actions, "re-encode")
	}
	if pl.resize {
		actions = append(actions, "resize")
	}
	if pl.removeAudio {
		actions = append(actions, "strip audio")
	}
	return strings.Join(actions, " + ")
}

// buildFFmpegArgs returns the ffmpeg arguments that turn input into output according to pl.
// Only the first video stream is kept. Audio streams are copied unless they are removed;
// without the explicit audio mapping "-map 0:v:0" would silently drop them.
func buildFFmpegArgs(input, output string, pl encodePlan) []string {
	args := []string{
		"-hide_banner",
		"-nostdin", // never read from the terminal; several ffmpeg processes run in parallel
		"-loglevel", "error",
		"-i", filepath.Clean(input),
		"-map", "0:v:0",
	}

	if pl.removeAudio {
		args = append(args, "-an")
	} else {
		args = append(args, "-map", "0:a?", "-c:a", "copy")
	}

	if pl.reEncode {
		args = append(args,
			"-c:v", "libx264",
			// 8-bit 4:2:0: libx264 keeps the source format otherwise, and High 10 or
			// 4:4:4 H.264 (from 10-bit HEVC/VP9) plays on fewer devices than the source.
			"-pix_fmt", "yuv420p",
			"-crf", strconv.Itoa(pl.crf), // Constant Rate Factor (18-28, lower = higher quality)
			"-maxrate", "2.5M", // Maximum bitrate cap
			"-bufsize", "5.0M", // Buffer size (typically 2x maxrate)
			"-preset", "fast", // Encoding speed vs compression efficiency
		)
	} else {
		args = append(args, "-c:v", "copy")
	}

	if pl.resize {
		args = append(args, "-vf", scaleFilter())
	} else if pl.reEncode {
		// libx264 refuses odd sizes in 4:2:0 (853x480 DivX). No upscaling, unlike scaleFilter.
		args = append(args, "-vf", evenFilter)
	}

	args = append(args, markerArgs(output)...)
	// The output file already exists (created by fsops.TempFileNear), hence -y.
	return append(args, "-y", filepath.Clean(output))
}

// scaleFilter fits the video within maxWidth x maxHeight keeping its aspect ratio (it is
// only applied to videos exceeding either limit) with even dimensions, which libx264
// requires. fitWithin mirrors it for the size estimate.
func scaleFilter() string {
	return fmt.Sprintf("scale=w=%d:h=%d:force_original_aspect_ratio=decrease:force_divisible_by=2", maxWidth, maxHeight)
}

// evenFilter rounds odd video dimensions down to even ones, which libx264 requires.
const evenFilter = "scale=trunc(iw/2)*2:trunc(ih/2)*2"

// needsResize reports whether a width x height video exceeds the size limits.
func needsResize(width, height int) bool {
	return width > maxWidth || height > maxHeight
}

// fitWithin returns the size of a width x height video after scaleFilter (or evenFilter,
// if it is not downscaled).
func fitWithin(width, height int) (int, int) {
	even := func(v float64) int { return max(2, int(v)&^1) }
	if !needsResize(width, height) {
		return even(float64(width)), even(float64(height))
	}
	f := min(float64(maxWidth)/float64(width), float64(maxHeight)/float64(height))
	return even(float64(width) * f), even(float64(height) * f)
}

// markerArgs returns the ffmpeg output arguments that write markerTag into output. It is
// harmless for containers that cannot store it (see markerExts).
func markerArgs(output string) []string {
	args := []string{"-metadata", markerTag + "=" + markerValue}
	if mp4Exts[strings.ToLower(filepath.Ext(output))] {
		args = append(args, "-movflags", "+use_metadata_tags") // custom tags in MP4/MOV
	}
	return args
}

// canCarryMarker reports whether the container of file keeps markerTag.
func canCarryMarker(file string) bool {
	return markerExts[strings.ToLower(filepath.Ext(file))]
}

// plan probes file and decides what has to be done with it. ok is false if the file
// needs no processing.
func (p *processor) plan(dir, file string) (pl encodePlan, ok bool) {
	codec, err := getVideoCodec(file)
	switch {
	case err != nil:
		// ffprobe cannot read the video stream. Try to re-encode anyway: if ffmpeg cannot
		// read the file either, it is reported as potentially corrupted.
		log.Debugf("Error getting codec for %s: %v", file, err)
		pl.reEncode = true
	case codec == "":
		log.Debugf("No video stream in %s, skipping", file)
		return encodePlan{}, false
	default:
		pl.reEncode = getNeedsReEncode(file, codec)
	}

	pl.removeAudio = needsRemoveAudio(p.ops, dir, file)

	width, height, err := getVideoResolution(file)
	if err != nil {
		log.Debugf("Error getting resolution for %s: %v", file, err)
	}
	pl.resize = needsResize(width, height)
	pl.reEncode = pl.reEncode || pl.resize

	// set crf based on resolution
	pl.crf = 28
	if width > 1280 {
		pl.crf = 23
	}

	log.Debugf("File: %s, needsRemoveAudio: %v, needsResize: %v, needsReEncode: %v", file, pl.removeAudio, pl.resize, pl.reEncode)
	return pl, pl.removeAudio || pl.reEncode
}

// processVideoFile processes a video by potentially removing audio, resizing and
// re-encoding it with ffmpeg into a temp file next to it, which then replaces the original.
func (p *processor) processVideoFile(dir, file string) {
	if p.ops.WouldBeMoved(file) {
		log.Debugf("Skipping %s: an earlier module would move it", file)
		return
	}
	if p.journal.Has(journalChecked, file) || p.journal.Has(journalProcessed, file) {
		log.Debugf("Skipping %s: recorded in the media journal", file)
		return
	}
	if m, err := probeMarker(file); err == nil && m.tagged {
		log.Debugf("Skipping %s: it carries the %s tag", file, markerTag)
		return
	}

	pl, ok := p.plan(dir, file)
	if !ok {
		log.Debugf("No processing needed for: %s", file)
		return
	}

	if pl.renames(file) {
		if !isVideoReferenced(p.ops, dir, file) {
			// Converting changes the file name, which only works if the txt references it.
			log.Warnf("Not converting %s to %s: the song txt's #VIDEO tag does not reference it", file, pl.outputExt(file))
			return
		}
		// Checked before encoding, so a blocked conversion is not encoded and discarded on
		// every run, and the dry run plans what the real run does. In a dry run, a file an
		// earlier module would move away does not count (in the real run it is gone).
		if newPath := renamedPath(file, pl); p.exists(newPath) {
			log.Warnf("Cannot convert %s: %s already exists, keeping the original", file, newPath)
			return
		}
	}

	if p.cfg.DryRun {
		action := pl.describe()
		if pl.renames(file) {
			action += fmt.Sprintf(" (to %s, updating the song txt and deleting the original)", pl.outputExt(file))
		}
		p.ops.PlanReplace(file, action)
		return
	}

	p.encode(dir, file, pl)
}

// renamedPath returns the path file gets when pl changes its extension.
func renamedPath(file string, pl encodePlan) string {
	return strings.TrimSuffix(file, filepath.Ext(file)) + pl.outputExt(file)
}

// exists reports whether path exists and no earlier module would move it away in a dry run.
func (p *processor) exists(path string) bool {
	_, err := os.Lstat(path)
	return err == nil && !p.ops.WouldBeMoved(path)
}

// encode runs ffmpeg on file according to pl into a temp file next to it and replaces the
// original with the result (see replaceOriginal).
func (p *processor) encode(dir, file string, pl encodePlan) {
	tmp, err := fsops.TempFileNear(file, pl.outputExt(file))
	if err != nil {
		log.Errorf("creating temp file for %s: %v", file, err)
		p.ops.Fail()
		return
	}

	args := buildFFmpegArgs(file, tmp, pl)
	log.Infof("Processing video (%s): %s", pl.describe(), file)
	log.Debugf("Running ffmpeg with args: %v", args)
	out, err := exec.Command("ffmpeg", args...).CombinedOutput()
	if err != nil {
		os.Remove(tmp) // our own temp file, not a library file
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) && exitErr.ExitCode() == exitInvalidData {
			// ffmpeg could not read the input: report it instead of counting a failure.
			p.flagCorrupted(file)
			return
		}
		log.Errorf("ffmpeg failed for %s: %v: %s", file, err, strings.TrimSpace(string(out)))
		p.ops.Fail()
		return
	}

	p.replaceOriginal(dir, file, tmp, pl)
}

// replaceOriginal replaces file with the processed tmp file, the output of pl. A larger
// result is discarded and the original marked as checked; if pl also strips the audio, the
// audio is stripped by a stream copy instead (marking the original would keep its audio
// forever). If the extension changed (not only its case), tmp is
// renamed to the new name, the song txt is updated to reference it and the original is
// deleted: like an in-place replacement, a conversion is not undoable (keeping originals
// would make the library very large). If anything goes wrong before the txt points to the
// new file, the original stays in place and keeps being referenced, so the song keeps
// working.
func (p *processor) replaceOriginal(dir, file, tmp string, pl encodePlan) {
	discard := func() { os.Remove(tmp) } // our own temp file, not a library file

	inInfo, err := os.Stat(file)
	if err != nil {
		log.Errorf("stat %s: %v", file, err)
		p.ops.Fail()
		discard()
		return
	}
	outInfo, err := os.Stat(tmp)
	if err != nil || outInfo.Size() == 0 {
		log.Errorf("ffmpeg produced no usable output for %s (%s): %v", file, tmp, err)
		p.ops.Fail()
		discard()
		return
	}
	if outInfo.Size() > inInfo.Size() {
		discard()
		if pl.reEncode && pl.removeAudio {
			log.Infof("Re-encoded video is larger than the original (%d > %d bytes), only stripping the audio of %s", outInfo.Size(), inInfo.Size(), file)
			p.encode(dir, file, encodePlan{removeAudio: true})
			return
		}
		log.Infof("Processed video is larger than the original (%d > %d bytes), keeping %s", outInfo.Size(), inInfo.Size(), file)
		p.markChecked(file)
		return
	}

	// Same extension, or one that only differs in case (v.MP4 -> v.mp4): replace in place.
	// On case-insensitive file systems (SMB, NTFS, macOS) the new name would be the
	// original itself, so the rename path below would discard the result on every run.
	if strings.EqualFold(filepath.Ext(tmp), filepath.Ext(file)) {
		if err := p.ops.Replace(tmp, file); err != nil {
			log.Errorf("%v", err)
			return
		}
		p.rememberProcessed(file)
		return
	}

	// The extension changed, so the txt must follow.
	p.renameMu.Lock()
	defer p.renameMu.Unlock()
	newPath := strings.TrimSuffix(file, filepath.Ext(file)) + filepath.Ext(tmp)
	if _, err := os.Lstat(newPath); err == nil { // created since processVideoFile checked
		log.Warnf("Cannot convert %s: %s already exists, keeping the original", file, newPath)
		discard()
		return
	}
	oldName, newName := filepath.Base(file), filepath.Base(newPath)
	if err := p.ops.Rename(tmp, newPath); err != nil {
		log.Errorf("%v", err)
		discard()
		return
	}

	replaced, err := utils.ReplaceFileReference(p.ops, dir, oldName, newName)
	if err == nil && !replaced {
		err = fmt.Errorf("no file tag of the song txt references %s", oldName)
	}
	if err != nil {
		log.Errorf("Not converting %s: updating the song txt failed, keeping the original video: %v", file, err)
		p.ops.Fail()
		// Undo the conversion; the new file is our own output and nothing references it.
		if err := p.ops.Remove(newPath); err != nil {
			log.Errorf("%v", err)
		}
		return
	}

	p.rememberProcessed(newPath)

	// If this fails, the unreferenced original is moved to extra-files by the next cleanup.
	if err := p.ops.Remove(file); err != nil {
		log.Errorf("Deleting the original video: %v", err)
	}
}

// rememberProcessed records a video written by fixvideos in the media journal if its
// container cannot carry markerTag (e.g. audio stripped from an AVI by stream copy).
func (p *processor) rememberProcessed(file string) {
	if canCarryMarker(file) {
		return
	}
	p.record(journalProcessed, file)
}

// record adds file to the media journal as kind and saves the journal. A failing Save is
// counted by ops (WriteReport); a failing Add (fingerprinting the file) is not.
func (p *processor) record(kind, file string) {
	if err := p.journal.Add(kind, file); err != nil {
		log.Errorf("Recording %s in the media journal: %v", file, err)
		p.ops.Fail()
	} else if err := p.journal.Save(p.ops); err != nil {
		log.Errorf("Saving the media journal after %s: %v", file, err) // counted by ops
	}
}

// markChecked makes sure file, whose processed version was discarded, is not processed
// again on later runs: containers that can carry markerTag get it by a lossless remux,
// all others (and failed remuxes) are recorded in the media journal.
func (p *processor) markChecked(file string) {
	if canCarryMarker(file) {
		err := p.tagByRemux(file)
		if err == nil {
			log.Infof("Tagged %s as checked (lossless remux)", file)
			return
		}
		log.Infof("Could not tag %s (%v), recording it in the media journal instead", file, err)
	}
	p.record(journalChecked, file)
}

// tagByRemux replaces file with a stream copy of all its streams that carries markerTag.
// The copy is only used if it has the tag and the same duration as file.
func (p *processor) tagByRemux(file string) error {
	orig, err := probeMarker(file)
	if err != nil {
		return err
	}
	if orig.duration <= 0 {
		return errors.New("unknown duration")
	}
	tmp, err := fsops.TempFileNear(file, filepath.Ext(file))
	if err != nil {
		return fmt.Errorf("creating temp file: %w", err)
	}
	args := slices.Concat([]string{
		"-hide_banner", "-nostdin", "-loglevel", "error",
		"-i", filepath.Clean(file),
		"-map", "0", "-c", "copy",
	}, markerArgs(tmp), []string{"-y", filepath.Clean(tmp)})
	if out, err := exec.Command("ffmpeg", args...).CombinedOutput(); err != nil {
		os.Remove(tmp) // our own temp file, not a library file
		return fmt.Errorf("ffmpeg remux: %v: %s", err, strings.TrimSpace(string(out)))
	}
	got, err := probeMarker(tmp)
	switch {
	case err != nil:
	case !got.tagged:
		err = errors.New("the remux does not carry the tag")
	case !durationsMatch(got.duration, orig.duration):
		err = fmt.Errorf("the remux is %.3f s long instead of %.3f s", got.duration, orig.duration)
	}
	if err != nil {
		os.Remove(tmp) // our own temp file, not a library file
		return err
	}
	return p.ops.Replace(tmp, file) // removes tmp on failure
}

// durationsMatch reports whether two durations (in seconds) differ by at most
// durationTolerance.
func durationsMatch(a, b float64) bool {
	return math.Abs(a-b) <= durationTolerance
}

// isVideoReferenced reports whether the #VIDEO tag of the song txt in dir names file
// (case- and NFC-insensitively). The txt is read through ops (see utils.ReadSongTags).
func isVideoReferenced(ops *fsops.Ops, dir, file string) bool {
	tags, err := utils.ReadSongTags(ops, dir)
	if err != nil {
		return false
	}
	onDisk, _, found := utils.ResolveFileName(dir, tags["VIDEO"])
	return found && onDisk == filepath.Base(file)
}

// needsRemoveAudio reports whether the audio stream of the video should be removed: only
// if the video has an audio stream and the song's #AUDIO/#MP3 tag names a separate,
// existing, visible, non-empty audio file. A song whose #MP3 points to the video itself, to a hidden
// file (e.g. "._v.mp3" left by old versions) or to nothing keeps the video's audio,
// because it may be the only audio track of the song. The txt is read through ops, so in
// a dry run the #MP3 fixtxt would write counts.
func needsRemoveAudio(ops *fsops.Ops, dir, file string) bool {
	tags, err := utils.ReadSongTags(ops, dir)
	if err != nil {
		return false
	}
	audioRef := tags["AUDIO"]
	if audioRef == "" {
		audioRef = tags["MP3"]
	}
	// utils.ResolveFileName ignores hidden files too; this guard does not rely on it.
	if utils.IsHidden(filepath.Base(audioRef)) {
		return false
	}
	onDisk, _, found := utils.ResolveFileName(dir, audioRef)
	if !found || utils.IsHidden(onDisk) || !utils.IsAudioFile(onDisk) || onDisk == filepath.Base(file) {
		return false
	}
	// An empty file (e.g. a failed download) is no audio source either.
	if info, err := os.Stat(filepath.Join(dir, onDisk)); err != nil || info.Size() == 0 {
		return false
	}

	cmd := exec.Command("ffprobe", "-v", "error", showEntries, "stream=codec_type", "-of", "default=nokey=1:noprint_wrappers=1", "--", filepath.Clean(file))
	output, err := cmd.Output()
	if err != nil {
		log.Debugf("Error checking audio stream for %s: %v", file, err)
		return false
	}
	log.Debugf("ffprobe stream types for %s: %s", file, strings.TrimSpace(string(output)))
	return strings.Contains(string(output), "audio")
}

// getNeedsReEncode determines if a video with the given codec should be re-encoded based
// on its bitrate, codec and the estimated size of the re-encoded video.
func getNeedsReEncode(file, codec string) bool {
	infoInput, err := os.Stat(file)
	if err != nil {
		log.Warnf("Error getting size of %s: %v", file, err)
		return false
	}

	bitrate, bitrateErr := getVideoBitrate(file)
	if bitrateErr != nil {
		log.Debugf("Error getting bitrate for %s: %v", file, bitrateErr)
	}
	// estimate the size of the reencoded video based on its resolution, framerate and duration
	estimate, estimateErr := estimatedSizeOfReEncodedVideo(file)
	if estimateErr != nil {
		log.Debugf("Error estimating re-encoded size for %s: %v", file, estimateErr)
	}
	return reEncodeWorthIt(codec, bitrate, bitrateErr, estimate, estimateErr, infoInput.Size())
}

// reEncodeWorthIt decides whether a video of the given codec, bitrate (bit/s) and size
// (bytes) should be re-encoded, given the estimated size of the re-encode: only if its
// codec is in codecsNeedReencode or its bitrate is above bitrateThreshold, and the
// estimate is smaller than the video. An unknown bitrate is not treated as high; without
// an estimate, the codec alone decides.
func reEncodeWorthIt(codec string, bitrate int, bitrateErr error, estimate int64, estimateErr error, size int64) bool {
	codecReason := slices.Contains(codecsNeedReencode, codec)
	if estimateErr != nil {
		return codecReason
	}
	highBitrate := bitrateErr == nil && bitrate > bitrateThreshold
	return (highBitrate || codecReason) && estimate < size
}

// The function estimates the size of a re-encoded video file based on its duration, resolution, and
// framerate.
func estimatedSizeOfReEncodedVideo(file string) (int64, error) {
	// Get video duration
	duration, err := getVideoDuration(file)
	if err != nil {
		return 0, err
	}
	// Get video resolution
	width, height, err := getVideoResolution(file)
	if err != nil {
		return 0, err
	}
	width, height, err = encodedResolution(width, height)
	if err != nil {
		return 0, err
	}

	// Get video framerate
	framerate, err := getVideoFramerate(file)
	if err != nil {
		return 0, err
	}
	return estimateSize(width, height, framerate, duration), nil
}

// encodedResolution returns the size a width x height video is encoded at (scaled down
// if it is too large, see fitWithin). A dimension of 0 or less (unknown) is an error.
func encodedResolution(width, height int) (int, int, error) {
	if width <= 0 || height <= 0 {
		return 0, 0, fmt.Errorf("invalid resolution %dx%d", width, height)
	}
	w, h := fitWithin(width, height)
	return w, h, nil
}

// estimateSize returns the estimated size in bytes of a width x height H.264 video at
// framerate fps lasting duration seconds: 1.8 Mbit/s for 1080p at 30 fps, scaled by the
// number of pixels per second.
func estimateSize(width, height int, framerate, duration float64) int64 {
	bitrate := 1800000 * (float64(width) / 1920) * (float64(height) / 1080) * (framerate / 30)
	return int64(bitrate * duration / 8)
}

// flagCorrupted records a video ffmpeg could not read for the report.
func (p *processor) flagCorrupted(file string) {
	log.Warnf("Potentially corrupted video file %q (ffmpeg could not read it)", file)
	p.mu.Lock()
	p.corrupted[file] = struct{}{}
	p.mu.Unlock()
}

// writeCorruptedReport merges the videos flagged in this run with the entries of an
// existing report and writes them sorted and deduplicated. Nothing is written if no video
// was flagged.
func (p *processor) writeCorruptedReport() {
	p.mu.Lock()
	defer p.mu.Unlock()
	if len(p.corrupted) == 0 {
		return
	}

	path := p.cfg.ReportPath(corruptedReportName)
	entries := maps.Clone(p.corrupted)
	existing, err := os.ReadFile(path)
	switch {
	case err == nil:
		for _, line := range strings.Split(string(existing), "\n") {
			if line = strings.TrimSpace(line); line != "" {
				entries[line] = struct{}{}
			}
		}
	case !errors.Is(err, os.ErrNotExist):
		// Do not overwrite a report we cannot read.
		log.Errorf("reading %s: %v; not updating it", path, err)
		p.ops.Fail()
		return
	}

	data := strings.Join(slices.Sorted(maps.Keys(entries)), "\n") + "\n"
	if err := p.ops.WriteReport(path, []byte(data)); err != nil {
		log.Errorf("%v", err)
	}
}

// Internal Utility Functions -------------------------------------------------------------------------------

// markerProbe is the result of probeMarker.
type markerProbe struct {
	tagged   bool    // the container or a stream carries markerTag
	duration float64 // container duration in seconds, 0 if unknown
}

// probeMarker reads the format and stream tags and the duration of file.
func probeMarker(file string) (markerProbe, error) {
	out, err := exec.Command("ffprobe", "-v", "error",
		showEntries, "format=duration:format_tags:stream_tags",
		"-of", "json", "--", filepath.Clean(file)).Output()
	if err != nil {
		return markerProbe{}, fmt.Errorf("ffprobe: %w", err)
	}
	return parseMarkerProbe(out)
}

func parseMarkerProbe(data []byte) (markerProbe, error) {
	var probe struct {
		Streams []struct {
			Tags map[string]string `json:"tags"`
		} `json:"streams"`
		Format struct {
			Duration string            `json:"duration"`
			Tags     map[string]string `json:"tags"`
		} `json:"format"`
	}
	if err := json.Unmarshal(data, &probe); err != nil {
		return markerProbe{}, fmt.Errorf("parsing ffprobe output: %w", err)
	}
	m := markerProbe{tagged: hasMarker(probe.Format.Tags)}
	for _, s := range probe.Streams {
		m.tagged = m.tagged || hasMarker(s.Tags)
	}
	if d, err := strconv.ParseFloat(probe.Format.Duration, 64); err == nil && d > 0 {
		m.duration = d
	}
	return m, nil
}

// hasMarker reports whether tags contain markerTag, whatever its value (tag names are
// case-insensitive, and some muxers change their case).
func hasMarker(tags map[string]string) bool {
	for k := range tags {
		if strings.EqualFold(k, markerTag) {
			return true
		}
	}
	return false
}

func getVideoCodec(file string) (string, error) {
	cmd := exec.Command("ffprobe", "-v", "error", selectStreams, "v:0", showEntries, "stream=codec_name", "-of", "csv=p=0", "--", filepath.Clean(file))
	output, err := cmd.Output()
	if err != nil {
		return "", err
	}
	log.Debugf("Video codec for %s: %s", file, output)
	codec := strings.TrimSpace(string(output))
	return codec, nil
}

// getVideoBitrate returns the bitrate of the first video stream of file in bit/s. Matroska
// and WebM store no per-stream bitrate; then the BPS statistics tag (mkvmerge) is used, or
// the sizes of the video packets are summed up. The container bitrate is never used: it
// includes the audio, so a large audio track would make a low-bitrate video look high.
func getVideoBitrate(file string) (int, error) {
	out, err := exec.Command("ffprobe", "-v", "error", selectStreams, "v:0", showEntries, "stream=bit_rate:stream_tags", "-of", "json", "--", filepath.Clean(file)).Output()
	if err != nil {
		return 0, err
	}
	if bitrate, ok := parseStreamBitrate(out); ok {
		return bitrate, nil
	}
	duration, err := getVideoDuration(file)
	if err != nil {
		return 0, err
	}
	out, err = exec.Command("ffprobe", "-v", "error", selectStreams, "v:0", showEntries, "packet=size", "-of", "csv=p=0", "--", filepath.Clean(file)).Output()
	if err != nil {
		return 0, err
	}
	return packetBitrate(out, duration)
}

// parseStreamBitrate returns the bitrate of the stream in ffprobe's JSON output for
// "stream=bit_rate:stream_tags": its bit_rate, else its BPS tag ("BPS", "BPS-eng"). ok is
// false if neither is a positive number.
func parseStreamBitrate(data []byte) (bitrate int, ok bool) {
	var probe struct {
		Streams []struct {
			BitRate string            `json:"bit_rate"`
			Tags    map[string]string `json:"tags"`
		} `json:"streams"`
	}
	if json.Unmarshal(data, &probe) != nil || len(probe.Streams) == 0 {
		return 0, false
	}
	st := probe.Streams[0]
	if b, err := strconv.Atoi(st.BitRate); err == nil && b > 0 {
		return b, true
	}
	// Plain "BPS" first, then the "BPS-<lang>" tags in sorted order, so that the result
	// does not depend on map iteration order.
	var keys, langKeys []string
	for k := range st.Tags {
		switch {
		case strings.EqualFold(k, "BPS"):
			keys = append(keys, k)
		case strings.HasPrefix(strings.ToUpper(k), "BPS-"):
			langKeys = append(langKeys, k)
		}
	}
	slices.Sort(keys)
	slices.Sort(langKeys)
	for _, k := range append(keys, langKeys...) {
		if b, err := strconv.Atoi(strings.TrimSpace(st.Tags[k])); err == nil && b > 0 {
			return b, true
		}
	}
	return 0, false
}

// packetBitrate returns the bitrate of a stream from its packet sizes (ffprobe
// "packet=size" csv output, one size in bytes per line) and its duration in seconds.
func packetBitrate(sizes []byte, duration float64) (int, error) {
	if duration <= 0 {
		return 0, fmt.Errorf("invalid duration %v", duration)
	}
	var total int64
	for _, line := range strings.Fields(string(sizes)) {
		size, err := strconv.ParseInt(strings.TrimSuffix(line, ","), 10, 64)
		if err != nil {
			return 0, fmt.Errorf("unexpected packet size %q", line)
		}
		total += size
	}
	if total == 0 {
		return 0, errors.New("no video packets")
	}
	return int(float64(total) * 8 / duration), nil
}

func getVideoResolution(file string) (int, int, error) {
	cmd := exec.Command(
		"ffprobe",
		"-v", "error",
		selectStreams, "v:0",
		showEntries, "stream=width,height",
		"-of", "csv=p=0",
		"--",
		filepath.Clean(file),
	)
	output, err := cmd.Output()
	if err != nil {
		return 0, 0, err
	}
	resolution := strings.Split(strings.TrimSpace(string(output)), ",")
	if len(resolution) != 2 {
		return 0, 0, fmt.Errorf("unexpected resolution format: %s", output)
	}
	width, err := strconv.Atoi(resolution[0])
	if err != nil {
		return 0, 0, err
	}
	height, err := strconv.Atoi(resolution[1])
	if err != nil {
		return 0, 0, err
	}
	return width, height, nil
}

func getVideoFramerate(file string) (float64, error) {
	cmd := exec.Command("ffprobe",
		"-v", "error",
		selectStreams, "v:0",
		showEntries, "stream=r_frame_rate",
		"-of", "csv=p=0",
		"--",
		filepath.Clean(file))
	output, err := cmd.Output()
	if err != nil {
		return 0, err
	}
	return parseFramerate(string(output))
}

// parseFramerate parses ffprobe's r_frame_rate ("30000/1001").
func parseFramerate(output string) (float64, error) {
	num, den, ok := strings.Cut(strings.TrimSpace(output), "/")
	if !ok {
		return 0, fmt.Errorf("unexpected framerate format: %s", output)
	}
	numerator, err := strconv.Atoi(num)
	if err != nil {
		return 0, err
	}
	denominator, err := strconv.Atoi(den)
	if err != nil {
		return 0, err
	}
	if numerator <= 0 || denominator <= 0 {
		return 0, fmt.Errorf("invalid framerate %s", output)
	}
	return float64(numerator) / float64(denominator), nil
}

func getVideoDuration(file string) (float64, error) {
	cmd := exec.Command("ffprobe",
		"-v", "error",
		showEntries, "format=duration",
		"-of", "default=noprint_wrappers=1:nokey=1",
		"--",
		filepath.Clean(file))
	output, err := cmd.Output()
	if err != nil {
		return 0, err
	}
	duration, err := strconv.ParseFloat(strings.TrimSpace(string(output)), 64)
	if err != nil {
		return 0, err
	}
	return duration, nil
}
