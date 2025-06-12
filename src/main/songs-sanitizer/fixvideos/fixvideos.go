package fixvideos

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"

	"github.com/nkreimeyer/ultrastar-songs-sanitizer/utils"

	log "github.com/sirupsen/logrus"
)

const (
	bitrateThreshold = 3000000 // 3 Mbit/s
	fullHDWidth      = 1920
	targetHeight     = 1080
)

var songDir string
var configOnce sync.Once

// constants
const showEntries = "-show_entries"
const selectStreams = "-select_streams"

// The `Run` function reads directories in a target directory, processes video files in batches
// concurrently, and logs the completion of processing.
func Run(targetDir, logLevel string) error {
	configOnce.Do(func() {
		songDir = targetDir
	})

	// Set log level
	level, _ := log.ParseLevel(logLevel)
	log.SetLevel(level)

	entries, err := os.ReadDir(songDir)
	if err != nil {
		return fmt.Errorf("reading songDir %q: %w", songDir, err)
	}

	// Loop over all directories, build batches of songs and process them concurrently
	batchSize := 100
	validEntries := make([]os.DirEntry, 0, len(entries))
	for _, entry := range entries {
		if !entry.IsDir() {
			// We only care about directories. Remove the entry from the list.
			continue
		} else {
			validEntries = append(validEntries, entry)
		}
	}
	entries = validEntries

	batches := utils.SplitIntoBatches(entries, batchSize)

	// Process batches with a maximum of 4 running in parallel
	semaphore := make(chan struct{}, 4)
	var batchWg sync.WaitGroup
	for _, batch := range batches {
		handleBatch(batch, semaphore, &batchWg)
	}
	batchWg.Wait()

	log.Infof("All video files have been processed.")
	return nil
}

// The `handleBatch` function processes a batch of directory entries, finds video files, and processes
// them concurrently using a semaphore to control access.
func handleBatch(batch []os.DirEntry, semaphore chan struct{}, batchWg *sync.WaitGroup) {
	batchWg.Add(1)
	go func(batch []os.DirEntry) {
		defer batchWg.Done()
		semaphore <- struct{}{}        // Acquire semaphore
		defer func() { <-semaphore }() // Release semaphore

		for _, entry := range batch {
			// find video files and process them
			files, err := os.ReadDir(filepath.Join(songDir, entry.Name()))
			if err != nil {
				log.Errorf("reading directory %q: %v\n", filepath.Join(songDir, entry.Name()), err)
				continue
			}
			for _, file := range files {
				if utils.IsVideoFile(file.Name()) {
					processVideoFile(filepath.Join(songDir, entry.Name(), file.Name()))
				}
			}
		}
	}(batch)
}

// The `processVideoFile` function processes a video file by potentially removing audio, resizing, and
// re-encoding it using ffmpeg based on specified criteria.
func processVideoFile(file string) {
	baseName := strings.TrimSuffix(file, filepath.Ext(file))
	outputFile := baseName + "_output"

	needsRemoveAudio := needsRemoveAudio(file)

	needsResize := getNeedsResize(file)
	needsReEncode := getNeedsReEncode(file) || needsResize

	log.Debugf("File: %s, needsRemoveAudio: %v, NeedsResize: %v, NeedsReEncode: %v\n", file, needsRemoveAudio, needsResize, needsReEncode)

	if !needsRemoveAudio && !needsReEncode {
		log.Debugf("No processing needed for: %s\n", file)
		return
	}

	// Build ffmpeg command
	args := []string{
		"-hide_banner",
		"-loglevel",
		"error",
		"-i", filepath.Clean(file)}

	if needsRemoveAudio {
		args = append(args, "-an")
	} else {
		args = append(args, "-c:a", "copy")
	}

	crf := 23
	// set crf based on resolution
	width, _, _ := getVideoResolution(file)
	if width > 1280 {
		crf = 23
	} else {
		crf = 28
	}

	// Re-encoding with variable bitrate if the bitrate is too high or if we need to scale down the video
	// only encode the first video stream, copy all other streams
	if needsReEncode {
		args = append(args,
			"-map", "0:v:0", // Select only the first video stream
			"-c:v", "libx264",
			"-crf", fmt.Sprint(crf), // Constant Rate Factor (18-28, lower = higher quality)
			"-maxrate", "2.75M", // Maximum bitrate cap
			"-bufsize", "5.5M", // Buffer size (typically 2x maxrate)
			"-preset", "fast", // Encoding speed vs compression efficiency
		)
		outputFile += ".mp4"
	} else {
		args = append(args,
			"-c:v", "copy")
		outputFile += filepath.Ext(file)
	}

	// Set the resolution scale if resizing is needed
	if needsResize {
		args = append(args,
			"-vf", fmt.Sprintf("scale=-1:%d", targetHeight))
	}

	args = append(args, "-y", filepath.Clean(outputFile))

	log.Infof("Re-encoding file: %s\n", file)
	log.Debugf("Running ffmpeg with args: %v\n", args)
	// Run ffmpeg command
	cmd := exec.Command("ffmpeg", args...)
	if err := cmd.Run(); err != nil {
		os.Remove(outputFile)
		// if ffmpeg returns 183, it means it encountered an error while reading the file, so it could be corrupted
		// in this case, we flag the file in ../potentially-corrupted-files.txt
		if exitError, ok := err.(*exec.ExitError); ok && exitError.ExitCode() == 183 {
			// Write the file path to the list of potentially corrupted files
			flagPotentiallyCorruptedFile(file)
		} else {
			// Print the error if it's not a 183 error
			fmt.Printf("Error processing %s: %v\n", file, err)
		}
		return
	}

	// Replace original file with processed file
	replaceOriginalFile(file, outputFile)
}

// The function `replaceOriginalFile` replaces the original file with a new file, considering file
// extensions and sizes, and updates references in a text file before deleting the original file.
func replaceOriginalFile(file, outputFile string) {
	// determine new file name (take extension from outputFile)
	newFileName := file[:len(file)-len(filepath.Ext(file))] + filepath.Ext(outputFile)
	if strings.EqualFold(filepath.Ext(file), filepath.Ext(outputFile)) {
		// Check if output file is larger than original file,
		// and if so, remove the output file and return.
		infoInput, errInput := os.Stat(file)
		infoOutput, errOutput := os.Stat(outputFile)
		if errInput == nil && infoInput.Size() > 0 && errOutput == nil && infoOutput.Size() > infoInput.Size() {
			os.Remove(outputFile)
			return
		}
		// replace the original file name with the new file name in the txt file and delete the original file
		utils.ReplaceInUSTxtFile(filepath.Dir(file), filepath.Base(file), filepath.Base(newFileName))
		os.Remove(file)
	}
	if err := os.Rename(outputFile, newFileName); err != nil {
		log.Warnf("Error renaming %s to %s: %v\n", outputFile, file, err)
	}
}

// The function `needsRemoveAudio` checks if an audio stream exists in a video file and if an audio
// file exists in the same directory before deciding if the audio should be removed.
func needsRemoveAudio(file string) bool {

	cmd := exec.Command("ffprobe", "-v", "error", showEntries, "stream=codec_type", "-of", "default=nokey=1:noprint_wrappers=1", "--", filepath.Clean(file))
	output, err := cmd.Output()
	log.Debugf("Checking audio stream for %s\n", file)
	log.Debugf("ffprobe output: %s\n", string(output))
	if err != nil {
		log.Debugf("Error checking audio stream for %s: %v\n", file, err)
		return false
	}
	audioInVideo := strings.Contains(string(output), "audio")

	// do not remove if no audio file exists in directory
	audioFileExists := utils.AudioFileExists(filepath.Dir(file))

	return audioFileExists && audioInVideo
}

// The function `getNeedsReEncode` determines if a video file needs to be re-encoded based on its
// bitrate, codec, and estimated size of re-encoded video.
func getNeedsReEncode(file string) bool {
	// Check video bitrate
	bitrate, err := getVideoBitrate(file)
	if err != nil {
		log.Debugf("Error getting bitrate for %s: %v\n", file, err)
		bitrate = 9999999999
	}

	// check if the file is vp9 or av1 encoded
	codec, err := getVideoCodec(file)
	if err != nil {
		log.Debugf("Error getting codec for %s: %v\n", file, err)
		return true
	}

	// vp9, av1 and hevc are too new, everything older than x264 is too  and thus too inefficient
	codecsNeedReencode := []string{"vp9", "av1", "hevc", "mpeg4", "mpeg2video", "msmpeg4v2", "msmpeg4v3", "flv1", "h263", "h263p"}

	// estimate the size of the reencoded video based on its resolution, framerate and duration
	// and determine if it's worth reencoding based on size of input file
	sizeOutput, err := estimatedSizeOfReEncodedVideo(file)
	if err != nil {
		log.Debugf("Error getting size for %s: %v\n", file, err)
	}
	infoInput, err := os.Stat(file)
	if err != nil {
		log.Debugf("Error getting size for %s: %v\n", file, err)
	}
	sizeInput := infoInput.Size()

	// Determine if we need to re-encode or resize
	needsReEncode := (bitrate > bitrateThreshold || slices.Contains(codecsNeedReencode, codec)) && sizeOutput < sizeInput

	return needsReEncode
}

// The function `getNeedsResize` checks if a video file needs to be resized based on its width compared
// to a predefined full HD width.
func getNeedsResize(file string) bool {
	// Check video resolution
	width, _, err := getVideoResolution(file)
	if err != nil {
		log.Debugf("Error getting resolution for %s: %v\n", file, err)
		return false
	}
	return width > fullHDWidth
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

// The function `flagPotentiallyCorruptedFile` checks if a file is potentially corrupted and logs its
// path in a separate file if not already present.
func flagPotentiallyCorruptedFile(file string) error {
	// set the logfile path to 2 levels up from the given file path
	// and name potentiallyCorruptedFiles.txt
	logFilePath := filepath.Join(filepath.Dir(songDir), "potentiallyCorruptedFiles.txt")
	// if not exist, create the logfile
	if _, err := os.Stat(logFilePath); os.IsNotExist(err) {
		os.Create(logFilePath)
	}
	// open the logfile at ../potentiallyCorruptedFiles.txt
	logFile, err := os.OpenFile(logFilePath, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0644)
	if err != nil {
		return err
	}
	defer logFile.Close()
	// write the file path to the logfile, when it is not already in the logfile
	// read the logfile
	logFileContent, err := os.ReadFile(logFilePath)
	if err != nil {
		return err
	}
	// check if the file is already in the logfile
	if strings.Contains(string(logFileContent), file) {
		return nil
	}
	// finally, if the file is not in the logfile, write the file path to the logfile
	log.Warnf("Potentially corrupted video file %q. Writing to logfile.", file)
	_, err = logFile.WriteString(file + "\n")
	return err
}

// Internal Utility Functions -------------------------------------------------------------------------------

func getVideoCodec(file string) (string, error) {
	cmd := exec.Command("ffprobe", "-v", "error", selectStreams, "v:0", showEntries, "stream=codec_name", "-of", "csv=p=0", "--", filepath.Clean(file))
	output, err := cmd.Output()
	if err != nil {
		return "", err
	}
	log.Debugf("Video codec for %s: %s\n", file, output)
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
