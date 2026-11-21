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
	"context"
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

	"github.com/nkreimeyer/library-manager/config"
	"github.com/nkreimeyer/library-manager/fsops"
	"github.com/nkreimeyer/library-manager/journal"
	"github.com/nkreimeyer/library-manager/mediatools"
	"github.com/nkreimeyer/library-manager/utils"

	log "github.com/sirupsen/logrus"
)

// BitrateThreshold: videos above this bitrate (bit/s) are re-encoded. organize prefers a
// video at or below it when choosing between versions of a song.
const BitrateThreshold = 3000000 // 3 Mbit/s

// MaxWidth and MaxHeight: larger videos are downscaled to fit (see maxWidth, maxHeight).
const MaxWidth, MaxHeight = 1920, 1080

const (

	// markerTag is written into every video fixvideos writes; tagged videos are skipped.
	markerTag   = "SANITIZER_VIDEO"
	markerValue = "processed"

	// Media journal kinds for videos whose container cannot carry markerTag.
	journalChecked   = "video-checked"   // processing would not help (result was larger)
	journalProcessed = "video-processed" // written by fixvideos

	// durationTolerance is the maximum duration difference (in seconds) between an
	// original and its tagged remux, or between the video streams of an original and its
	// processed version.
	durationTolerance = 0.5

	// corruptedReportName is the report listing videos ffmpeg could not read, or whose
	// processed version did not keep their duration.
	corruptedReportName = "potentiallyCorruptedFiles.txt"

	// exitInvalidData is the exit code of newer ffmpeg releases for AVERROR_INVALIDDATA
	// ("Invalid data found when processing input"), i.e. the input file is probably
	// corrupted. Older releases in the supported range (5.1, 6.0) exit with 1 (see
	// isCorruptInput).
	exitInvalidData = 183

	showEntries   = "-show_entries"
	selectStreams = "-select_streams"
)

// Videos larger than maxWidth x maxHeight are downscaled to fit within it. They are
// variables so tests can lower them.
var maxWidth, maxHeight = MaxWidth, MaxHeight

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
	readable  map[string]struct{} // videos this run found readable (see markReadable)
}

// Run processes the videos of all song folders below cfg.SongDir with a pool of workers,
// then writes the corrupted-files report and the media journal. A panic while processing
// one video is logged and counted as a failure, and the other videos are still processed.
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
	_ = utils.ForEachParallel(context.Background(), videos, utils.Parallel{
		Workers: workers,
		OnPanic: func(i int, _ any) {
			log.Errorf("fixvideos: %s not finished: internal error (logged above)", videos[i])
			ops.Fail()
		},
	}, func(_ int, file string) { p.processVideoFile(filepath.Dir(file), file) })

	p.writeCorruptedReport()
	// Save writes only a changed journal. A dry run never adds to it (entries are added
	// after an encode), so it does not write the journal.
	if err := j.Save(ops); err != nil {
		log.Errorf("%v", err) // counted as a failure by ops
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

// plan decides what has to be done with file from its ffprobe result (probeErr is the
// error of that probe). ok is false if the file needs no processing.
func (p *processor) plan(dir, file string, probe mediatools.ProbeInfo, probeErr error) (pl encodePlan, ok bool) {
	video, _ := probe.FirstStream("video")
	switch {
	case probeErr != nil:
		// ffprobe cannot read the video stream. Try to re-encode anyway: if ffmpeg cannot
		// read the file either, it is reported as potentially corrupted.
		log.Debugf("Error probing %s: %v", file, probeErr)
		pl.reEncode = true
	case video.CodecName == "":
		log.Debugf("No video stream in %s, skipping", file)
		return encodePlan{}, false
	default:
		log.Debugf("Video codec for %s: %s", file, video.CodecName)
		pl.reEncode = needsReEncode(file, video.CodecName, probe)
	}

	pl.removeAudio = needsRemoveAudio(p.ops, dir, file, probe)

	width, height, err := videoResolution(probe)
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
	if st, err := os.Lstat(file); err == nil && st.Mode()&os.ModeSymlink != 0 {
		// Replacing it would turn the link into a processed regular copy (a conversion
		// would delete only the link), and the file it points to (maybe shared by other
		// songs) would stay unprocessed.
		log.Warnf("Skipping %s: it is a symbolic link; process the file it points to instead", file)
		return
	}
	if p.journal.Has(journalChecked, file) || p.journal.Has(journalProcessed, file) {
		log.Debugf("Skipping %s: recorded in the media journal", file)
		p.markReadable(file)
		return
	}
	// One probe for every decision about the file (plan's codec, bitrate, size and audio).
	probe, probeErr := mediatools.Probe(context.Background(), file)
	if probeErr == nil && markerFromProbe(probe).tagged {
		log.Debugf("Skipping %s: it carries the %s tag", file, markerTag)
		p.markReadable(file)
		return
	}

	pl, ok := p.plan(dir, file, probe, probeErr)
	if !ok {
		// plan only skips a video whose codec ffprobe read (an unreadable one is re-encoded).
		log.Debugf("No processing needed for: %s", file)
		p.markReadable(file)
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
		newPath := renamedPath(file, pl)
		if p.exists(newPath) {
			log.Warnf("Cannot convert %s: %s already exists, keeping the original", file, newPath)
			return
		}
		// The same for a txt that cannot follow the new name (a decode Problem, which
		// fixtxt never fixes, or not UTF-8 yet); replaceOriginal would undo the conversion.
		if _, content, err := utils.FileReferenceReplacement(p.ops, dir, filepath.Base(file), filepath.Base(newPath)); err != nil || content == nil {
			if err == nil {
				err = fmt.Errorf("no file tag references %s", filepath.Base(file))
			}
			log.Warnf("Not converting %s to %s: the song txt cannot be updated, keeping the original: %v", file, pl.outputExt(file), err)
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
// original with the result (see replaceOriginal). A result whose video is shorter or
// longer than the original's is discarded and the original reported as potentially
// corrupted: ffmpeg exits 0 on damaged input and drops what it cannot decode, so an AVI
// with a damaged section comes out shorter, and its video would run ahead of the song
// from there on with no original left.
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
	// The "Invalid data found" message isCorruptInput looks for is printed when the
	// input is opened, so it is in the kept start of the output.
	out, err := mediatools.CombinedOutput(exec.Command("ffmpeg", args...))
	if err != nil {
		os.Remove(tmp) // our own temp file, not a library file
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) && isCorruptInput(exitErr.ExitCode(), out) {
			// ffmpeg could not read the input: report it instead of counting a failure.
			p.flagCorrupted(file, "ffmpeg could not read it")
			return
		}
		log.Errorf("ffmpeg failed for %s: %v: %s", file, err, strings.TrimSpace(string(out)))
		p.ops.Fail()
		return
	}
	if ok, msg := sameVideoDuration(file, tmp, !pl.removeAudio); !ok {
		os.Remove(tmp) // our own temp file, not a library file
		p.flagCorrupted(file, msg+", keeping the original")
		return
	}

	p.markReadable(file)
	p.replaceOriginal(dir, file, tmp, pl)
}

// isCorruptInput reports whether an ffmpeg run that exited with code and printed output
// failed because it could not read its input: exit code exitInvalidData, or, from older
// ffmpeg releases that exit with 1 on every error, exit code 1 with the "Invalid
// data found" message.
func isCorruptInput(code int, output []byte) bool {
	return code == exitInvalidData ||
		code == 1 && strings.Contains(strings.ToLower(string(output)), "invalid data found")
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
// Unlike fixaudio, fixvideos journals only such containers: the others carry the tag.
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
	if out, err := mediatools.CombinedOutput(exec.Command("ffmpeg", args...)); err != nil {
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

// sameVideoDuration reports whether the video of the processed out has the duration of
// the video of the original in, and if not, why. The video streams are compared, because
// stripping the audio shortens the container when the audio is the longer stream. If the
// original's video stream has no known duration, the container durations are compared
// when the audio is kept (keepsAudio); with neither known there is nothing to compare and
// the result counts as matching. An out that ffprobe cannot read never matches.
func sameVideoDuration(in, out string, keepsAudio bool) (bool, string) {
	outProbe, err := mediatools.Probe(context.Background(), out)
	if err != nil {
		return false, fmt.Sprintf("ffprobe cannot read the processed video: %v", err)
	}
	got := videoDuration(outProbe)
	inProbe, inErr := mediatools.Probe(context.Background(), in)
	want := videoDuration(inProbe)
	if inErr != nil || want <= 0 || got <= 0 {
		if !keepsAudio {
			log.Debugf("Not comparing the duration of %s with its processed version: unknown video duration", in)
			return true, ""
		}
		if inErr == nil {
			want, err = formatDuration(inProbe)
		}
		if inErr != nil || err != nil || want <= 0 {
			log.Debugf("Not comparing the duration of %s with its processed version: unknown duration", in)
			return true, ""
		}
		if got, err = formatDuration(outProbe); err != nil {
			return false, fmt.Sprintf("ffprobe cannot read the processed video: %v", err)
		}
	}
	if !durationsMatch(got, want) {
		return false, fmt.Sprintf("the processed video is %.3f s long instead of %.3f s (damaged input?)", got, want)
	}
	return true, ""
}

// videoDuration returns the duration in seconds of the first video stream of probe (see
// streamDuration); 0 if it is not known or there is no video stream.
func videoDuration(probe mediatools.ProbeInfo) float64 {
	video, _ := probe.FirstStream("video")
	return streamDuration(video)
}

// streamDuration returns the duration in seconds of the stream: its duration, else its
// DURATION tag (Matroska writes one per stream, maybe with a language suffix); 0 if
// neither is known.
func streamDuration(st mediatools.Stream) float64 {
	if d, err := strconv.ParseFloat(st.Duration, 64); err == nil && d > 0 {
		return d
	}
	for _, k := range slices.Sorted(maps.Keys(st.Tags)) {
		if name, _, _ := strings.Cut(k, "-"); strings.EqualFold(name, "DURATION") {
			if d, ok := parseClock(st.Tags[k]); ok {
				return d
			}
		}
	}
	return 0
}

// parseClock parses a Matroska duration tag ("HH:MM:SS.nnnnnnnnn") into seconds.
func parseClock(v string) (float64, bool) {
	parts := strings.Split(strings.TrimSpace(v), ":")
	if len(parts) != 3 {
		return 0, false
	}
	h, err1 := strconv.Atoi(parts[0])
	m, err2 := strconv.Atoi(parts[1])
	sec, err3 := strconv.ParseFloat(parts[2], 64)
	if err1 != nil || err2 != nil || err3 != nil || h < 0 || m < 0 || m > 59 || sec < 0 || sec >= 60 {
		return 0, false
	}
	d := float64(h*3600+m*60) + sec
	return d, d > 0
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
// a dry run the #MP3 fixtxt would write counts. probe is the ffprobe result of file (zero
// if ffprobe could not read it: then the video has no audio stream that is known).
func needsRemoveAudio(ops *fsops.Ops, dir, file string, probe mediatools.ProbeInfo) bool {
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

	return probe.HasStream("audio")
}

// needsReEncode determines if a video with the given codec should be re-encoded based
// on its bitrate, codec and the estimated size of the re-encoded video. probe is the
// ffprobe result of file.
func needsReEncode(file, codec string, probe mediatools.ProbeInfo) bool {
	infoInput, err := os.Stat(file)
	if err != nil {
		log.Warnf("Error getting size of %s: %v", file, err)
		return false
	}

	bitrate, bitrateErr := videoBitrate(file, probe)
	if bitrateErr != nil {
		log.Debugf("Error getting bitrate for %s: %v", file, bitrateErr)
	}
	// estimate the size of the reencoded video based on its resolution, framerate and duration
	estimate, estimateErr := estimatedReEncodedSize(probe)
	if estimateErr != nil {
		log.Debugf("Error estimating re-encoded size for %s: %v", file, estimateErr)
	}
	return reEncodeWorthIt(codec, bitrate, bitrateErr, estimate, estimateErr, infoInput.Size())
}

// reEncodeWorthIt decides whether a video of the given codec, bitrate (bit/s) and size
// (bytes) should be re-encoded, given the estimated size of the re-encode: only if its
// codec is in codecsNeedReencode or its bitrate is above BitrateThreshold, and the
// estimate is smaller than the video. An unknown bitrate is not treated as high; without
// an estimate, the codec alone decides.
func reEncodeWorthIt(codec string, bitrate int, bitrateErr error, estimate int64, estimateErr error, size int64) bool {
	codecReason := slices.Contains(codecsNeedReencode, codec)
	if estimateErr != nil {
		return codecReason
	}
	highBitrate := bitrateErr == nil && bitrate > BitrateThreshold
	return (highBitrate || codecReason) && estimate < size
}

// estimatedReEncodedSize estimates the size of the re-encoded video from the duration,
// resolution and framerate in probe.
func estimatedReEncodedSize(probe mediatools.ProbeInfo) (int64, error) {
	duration, err := formatDuration(probe)
	if err != nil {
		return 0, err
	}
	width, height, err := videoResolution(probe)
	if err != nil {
		return 0, err
	}
	width, height, err = encodedResolution(width, height)
	if err != nil {
		return 0, err
	}
	framerate, err := videoFramerate(probe)
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

// flagCorrupted records a potentially corrupted video for the report; why is logged.
func (p *processor) flagCorrupted(file, why string) {
	log.Warnf("Potentially corrupted video file %q (%s)", file, why)
	p.mu.Lock()
	p.corrupted[file] = struct{}{}
	p.mu.Unlock()
}

// markReadable records that this run found file readable: it was skipped as already
// processed or checked, needed no processing (ffprobe read its codec), or ffmpeg processed
// it. Its entry in an existing corrupted-files report is dropped.
func (p *processor) markReadable(file string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.readable == nil {
		p.readable = map[string]struct{}{}
	}
	p.readable[file] = struct{}{}
}

// writeCorruptedReport merges the videos flagged in this run with the entries of an
// existing report and writes them sorted and deduplicated. Entries of earlier runs are
// dropped if this run found the video readable (fixed, replaced, tagged) or the file no
// longer exists (converted, removed, moved); the others stay, because a video this run did
// not try to read (a blocked conversion, another library writing to the same out dir) may
// still be corrupted. Nothing is written if there is neither a report nor a flagged video.
// A dry run never rewrites the report: it runs no ffmpeg, so it flags nothing, and the
// entries it would drop (including files an earlier module would move away, which are
// still on disk and still corrupted) are only logged.
func (p *processor) writeCorruptedReport() {
	p.mu.Lock()
	defer p.mu.Unlock()

	path := p.cfg.ReportPath(corruptedReportName)
	entries := maps.Clone(p.corrupted)
	existing, err := os.ReadFile(path)
	switch {
	case err == nil:
		for _, line := range strings.Split(string(existing), "\n") {
			if line = strings.TrimSpace(line); line == "" {
				continue
			}
			if why := p.staleReason(line); why != "" {
				if p.cfg.DryRun {
					log.Infof("Dry run: %s would be dropped from %s: %s", line, corruptedReportName, why)
				}
				continue
			}
			entries[line] = struct{}{}
		}
	case !errors.Is(err, os.ErrNotExist):
		// Do not overwrite a report we cannot read.
		log.Errorf("reading %s: %v; not updating it", path, err)
		p.ops.Fail()
		return
	case len(entries) == 0:
		return
	}

	var data string
	if len(entries) > 0 {
		data = strings.Join(slices.Sorted(maps.Keys(entries)), "\n") + "\n"
	}
	if err == nil && data == string(existing) {
		return
	}
	if p.cfg.DryRun {
		log.Infof("Dry run: not updating %s; it would list %d video(s)", path, len(entries))
		return
	}
	if err := p.ops.WriteReport(path, []byte(data)); err != nil {
		log.Errorf("%v", err)
	}
}

// staleReason returns why an entry of an earlier corrupted-files report is out of date, or
// "" if it stays: this run found the video readable, or the file no longer exists. In a
// dry run a file an earlier module would move away counts as gone too (the real run will
// not find it), which only matters for the dry run's log: a dry run never rewrites the report.
func (p *processor) staleReason(file string) string {
	if _, ok := p.readable[file]; ok {
		return "this run found it readable"
	}
	if _, err := os.Lstat(file); errors.Is(err, os.ErrNotExist) {
		return "it no longer exists"
	}
	if p.ops.WouldBeMoved(file) {
		return "an earlier module would move it away"
	}
	return ""
}

// Internal Utility Functions -------------------------------------------------------------------------------

// markerProbe is the result of probeMarker.
type markerProbe struct {
	tagged   bool    // the container or a stream carries markerTag
	duration float64 // container duration in seconds, 0 if unknown
}

// probeMarker reads the format and stream tags and the duration of file.
func probeMarker(file string) (markerProbe, error) {
	probe, err := mediatools.Probe(context.Background(), file)
	if err != nil {
		return markerProbe{}, err
	}
	return markerFromProbe(probe), nil
}

// markerFromProbe reads the marker and the container duration from an ffprobe result.
func markerFromProbe(probe mediatools.ProbeInfo) markerProbe {
	m := markerProbe{tagged: hasMarker(probe.Format.Tags)}
	for _, s := range probe.Streams {
		m.tagged = m.tagged || hasMarker(s.Tags)
	}
	if d, err := strconv.ParseFloat(probe.Format.Duration, 64); err == nil && d > 0 {
		m.duration = d
	}
	return m
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

// videoBitrate returns the bitrate of the first video stream of file in bit/s, from its
// ffprobe result probe, by mediatools.VideoBitrate (the rule check uses, whose value
// organize compares with BitrateThreshold): the stream bit_rate, else its BPS tag, else
// the container rate of a file without audio. Where that is unknown (Matroska/WebM
// with audio and no BPS tag), the sizes of the video packets are summed up: another
// ffprobe run that reads every packet, too slow for check, which reports 0 (unknown)
// for such a file. The container rate is never used for a file with audio: it includes
// the audio, so a large audio track would make a low-bitrate video look high.
func videoBitrate(file string, probe mediatools.ProbeInfo) (int, error) {
	video, ok := probe.FirstStream("video")
	if !ok {
		return 0, errors.New("no video stream")
	}
	if bitrate := mediatools.VideoBitrate(probe, video); bitrate > 0 {
		return int(bitrate), nil
	}
	duration, err := formatDuration(probe)
	if err != nil {
		return 0, err
	}
	out, err := exec.Command("ffprobe", "-v", "error", selectStreams, "v:0", showEntries, "packet=size", "-of", "csv=p=0", "--", filepath.Clean(file)).Output()
	if err != nil {
		return 0, err
	}
	return packetBitrate(out, duration)
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

// videoResolution returns the size of the first video stream in probe.
func videoResolution(probe mediatools.ProbeInfo) (int, int, error) {
	video, ok := probe.FirstStream("video")
	if !ok {
		return 0, 0, errors.New("no video stream")
	}
	return video.Width, video.Height, nil
}

// videoFramerate returns the frame rate of the first video stream in probe.
func videoFramerate(probe mediatools.ProbeInfo) (float64, error) {
	video, _ := probe.FirstStream("video")
	return parseFramerate(video.RFrameRate)
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

// formatDuration returns the container duration in probe ("N/A" or a missing one is an
// error; 0 is not).
func formatDuration(probe mediatools.ProbeInfo) (float64, error) {
	return strconv.ParseFloat(strings.TrimSpace(probe.Format.Duration), 64)
}
