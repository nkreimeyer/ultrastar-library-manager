// Package fixvideos re-encodes song videos with ffmpeg: inefficient codecs and high-bitrate
// videos are converted to H.264 MP4, videos above Full HD are downscaled, and the audio
// track is stripped when the song has a separate audio file. Videos ffmpeg cannot read are
// listed in potentiallyCorruptedFiles.txt in the output folder.
package fixvideos

import (
	"errors"
	"fmt"
	"maps"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"

	"github.com/nkreimeyer/ultrastar-songs-sanitizer/config"
	"github.com/nkreimeyer/ultrastar-songs-sanitizer/fsops"
	"github.com/nkreimeyer/ultrastar-songs-sanitizer/utils"

	log "github.com/sirupsen/logrus"
)

const (
	bitrateThreshold = 3000000 // 3 Mbit/s
	fullHDWidth      = 1920
	targetHeight     = 1080

	// corruptedReportName is the report listing videos ffmpeg could not read.
	corruptedReportName = "potentiallyCorruptedFiles.txt"

	// exitInvalidData is ffmpeg's exit code for AVERROR_INVALIDDATA ("Invalid data found
	// when processing input"), i.e. the input file is probably corrupted.
	exitInvalidData = 183

	showEntries   = "-show_entries"
	selectStreams = "-select_streams"
)

// codecsNeedReencode lists video codecs worth converting to H.264: vp9, av1 and hevc are
// too new for some players, everything older than H.264 is inefficient.
var codecsNeedReencode = []string{"vp9", "av1", "hevc", "mpeg4", "mpeg2video", "msmpeg4v2", "msmpeg4v3", "flv1", "h263", "h263p"}

// processor holds the per-run state shared by all workers.
type processor struct {
	cfg config.Config
	ops *fsops.Ops

	mu        sync.Mutex
	corrupted map[string]struct{} // videos ffmpeg could not read
}

// Run processes the videos of all song folders below cfg.SongDir, at most 4 batches in
// parallel, and writes the corrupted-files report at the end.
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

	p := &processor{cfg: cfg, ops: ops, corrupted: map[string]struct{}{}}

	// Process batches with a maximum of 4 running in parallel
	semaphore := make(chan struct{}, 4)
	var batchWg sync.WaitGroup
	for _, batch := range utils.SplitIntoBatches(entries, 100) {
		batchWg.Add(1)
		go func(batch []string) {
			defer batchWg.Done()
			semaphore <- struct{}{}        // Acquire semaphore
			defer func() { <-semaphore }() // Release semaphore
			p.handleBatch(batch)
		}(batch)
	}
	batchWg.Wait()

	p.writeCorruptedReport()
	log.Infof("All video files have been processed.")
	return nil
}

// handleBatch processes all video files of the song folders in batch, one after another.
func (p *processor) handleBatch(batch []string) {
	for _, dir := range batch {
		files, err := os.ReadDir(dir)
		if err != nil {
			log.Errorf("reading directory %q: %v", dir, err)
			p.ops.Fail()
			continue
		}
		for _, file := range files {
			// Hidden files are leftovers of interrupted runs or AppleDouble files.
			if utils.IsHidden(file.Name()) || !utils.IsVideoFile(file.Name()) || !utils.IsRegularFile(dir, file) {
				continue
			}
			p.processVideoFile(dir, filepath.Join(dir, file.Name()))
		}
	}
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
			"-crf", strconv.Itoa(pl.crf), // Constant Rate Factor (18-28, lower = higher quality)
			"-maxrate", "2.5M", // Maximum bitrate cap
			"-bufsize", "5.0M", // Buffer size (typically 2x maxrate)
			"-preset", "fast", // Encoding speed vs compression efficiency
		)
	} else {
		args = append(args, "-c:v", "copy")
	}

	if pl.resize {
		// -2 keeps the aspect ratio with an even width, which libx264 requires.
		args = append(args, "-vf", fmt.Sprintf("scale=-2:%d", targetHeight))
	}

	// The output file already exists (created by fsops.TempFileNear), hence -y.
	return append(args, "-y", filepath.Clean(output))
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

	pl.removeAudio = needsRemoveAudio(dir, file)

	width, _, err := getVideoResolution(file)
	if err != nil {
		log.Debugf("Error getting resolution for %s: %v", file, err)
	}
	pl.resize = width > fullHDWidth
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
	pl, ok := p.plan(dir, file)
	if !ok {
		log.Debugf("No processing needed for: %s", file)
		return
	}

	if ext := pl.outputExt(file); ext != filepath.Ext(file) && !isVideoReferenced(dir, file) {
		// Converting changes the file name, which only works if the txt references it.
		log.Warnf("Not converting %s to %s: the song txt's #VIDEO tag does not reference it", file, ext)
		return
	}

	if p.cfg.DryRun {
		action := pl.describe()
		if ext := pl.outputExt(file); ext != filepath.Ext(file) {
			action += fmt.Sprintf(" (to %s, updating the song txt and moving the original to extra-files)", ext)
		}
		p.ops.PlanReplace(file, action)
		return
	}

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

	p.replaceOriginal(dir, file, tmp)
}

// replaceOriginal replaces file with the processed tmp file. A larger result is discarded.
// If the extension changed, tmp is renamed to the new name, the song txt is updated to
// reference it and the original is moved to extra-files (never deleted). If anything goes
// wrong, the original stays in place and keeps being referenced, so the song keeps working.
func (p *processor) replaceOriginal(dir, file, tmp string) {
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
		log.Infof("Processed video is larger than the original (%d > %d bytes), keeping %s", outInfo.Size(), inInfo.Size(), file)
		discard()
		return
	}

	newPath := strings.TrimSuffix(file, filepath.Ext(file)) + filepath.Ext(tmp)
	if newPath == file {
		if err := p.ops.Replace(tmp, file); err != nil {
			log.Errorf("%v", err)
		}
		return
	}

	// The extension changed (possibly only its case), so the txt must follow.
	if _, err := os.Lstat(newPath); err == nil {
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

	rel := filepath.Join(p.cfg.RelPath(dir), oldName)
	if _, err := p.ops.MoveInto(file, p.cfg.ExtraFilesDir(), rel); err != nil {
		log.Errorf("Moving the original video away: %v", err)
	}
}

// isVideoReferenced reports whether the #VIDEO tag of the song txt in dir names file
// (case- and NFC-insensitively).
func isVideoReferenced(dir, file string) bool {
	tags, err := utils.ReadSongTags(dir)
	if err != nil {
		return false
	}
	onDisk, _, found := utils.ResolveFileName(dir, tags["VIDEO"])
	return found && onDisk == filepath.Base(file)
}

// needsRemoveAudio reports whether the audio stream of the video should be removed: only
// if the video has an audio stream and the song's #AUDIO/#MP3 tag names a separate,
// existing audio file. A song whose #MP3 points to the video itself (or to nothing) keeps
// the video's audio, because it may be the only audio track of the song.
func needsRemoveAudio(dir, file string) bool {
	tags, err := utils.ReadSongTags(dir)
	if err != nil {
		return false
	}
	audioRef := tags["AUDIO"]
	if audioRef == "" {
		audioRef = tags["MP3"]
	}
	onDisk, _, found := utils.ResolveFileName(dir, audioRef)
	if !found || !utils.IsAudioFile(onDisk) || onDisk == filepath.Base(file) {
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

	codecReason := slices.Contains(codecsNeedReencode, codec)

	// An unknown bitrate is not treated as high.
	bitrate, err := getVideoBitrate(file)
	if err != nil {
		log.Debugf("Error getting bitrate for %s: %v", file, err)
	}
	highBitrate := err == nil && bitrate > bitrateThreshold

	// estimate the size of the reencoded video based on its resolution, framerate and duration
	// and determine if it's worth reencoding based on size of input file
	sizeOutput, err := estimatedSizeOfReEncodedVideo(file)
	if err != nil {
		log.Debugf("Error estimating re-encoded size for %s: %v", file, err)
		return codecReason
	}
	return (highBitrate || codecReason) && sizeOutput < infoInput.Size()
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
	if width <= 0 || height <= 0 {
		return 0, fmt.Errorf("invalid resolution %dx%d", width, height)
	}
	// Scale down the resolution if it's too large
	// Preserve aspect ratio
	if width > 1920 || height > 1080 {
		aspectRatio := float64(width) / float64(height)
		if width > 1920 {
			width = 1920
			height = int(float64(width) / aspectRatio)
		}
		if height > 1080 {
			height = 1080
			width = int(float64(height) * aspectRatio)
		}
	}

	// Get video framerate
	framerate, err := getVideoFramerate(file)
	if err != nil {
		return 0, err
	}
	// Estimate the bitrate of the reencoded video with 1.8mbps as a base for 1080p 30fps
	// and scale it down for lower resolutions and framerates
	bitrate := 1800000 * (float64(width) / 1920) * (float64(height) / 1080) * (framerate / 30)
	// Calculate the estimated size in bytes
	size := int64(bitrate * duration / 8)
	return size, nil
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

// The `getVideoBitrate` function retrieves the bitrate of a video file using ffprobe and calculates it
// if not directly available.
func getVideoBitrate(file string) (int, error) {
	cmd := exec.Command("ffprobe", "-v", "error", selectStreams, "v:0", showEntries, "stream=bit_rate", "-of", "csv=p=0", "--", filepath.Clean(file))
	output, err := cmd.Output()
	if err != nil {
		return 0, err
	}
	bitrateStr := strings.TrimSpace(string(output))
	if bitrateStr == "N/A" {
		// calculate bitrate from duration and file size using ffprobe
		cmd = exec.Command("ffprobe", "-v", "error", showEntries, "format=duration,size", "-of", "csv=p=0", "--", filepath.Clean(file))
		output, err = cmd.Output()
		if err != nil {
			return 0, err
		}
		fields := strings.Split(strings.TrimSpace(string(output)), ",")
		if len(fields) != 2 {
			return 0, fmt.Errorf("unexpected output format: %s", output)
		}
		duration, err := strconv.ParseFloat(fields[0], 64)
		if err != nil {
			return 0, err
		}
		if duration <= 0 {
			return 0, fmt.Errorf("invalid duration %v", duration)
		}
		size, err := strconv.ParseInt(fields[1], 10, 64)
		if err != nil {
			return 0, err
		}
		bitrate := int(float64(size) * 8 / duration)
		return bitrate, nil
	}
	bitrate, err := strconv.Atoi(bitrateStr)
	if err != nil {
		return 0, err
	}
	return bitrate, nil
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
		return 30.0, err
	}
	framerate := strings.Split(strings.TrimSpace(string(output)), "/")
	if len(framerate) != 2 {
		return 30.0, fmt.Errorf("unexpected framerate format: %s", output)
	}
	numerator, err := strconv.Atoi(framerate[0])
	if err != nil {
		return 30.0, err
	}
	denominator, err := strconv.Atoi(framerate[1])
	if err != nil {
		return 30.0, err
	}
	if denominator == 0 {
		return 30.0, fmt.Errorf("invalid framerate %s", output)
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
