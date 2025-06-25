package fixaudio

import (
	"fmt"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"

	"github.com/nkreimeyer/ultrastar-songs-sanitizer/utils"
	log "github.com/sirupsen/logrus"
)

var songDir string
var configOnce sync.Once

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

	// Process batches with a maximum of 16 running in parallel
	semaphore := make(chan struct{}, 16)
	var batchWg sync.WaitGroup
	for _, batch := range batches {
		handleBatch(batch, semaphore, &batchWg)
	}
	batchWg.Wait()

	log.Infof("All audio files have been processed.")
	return nil
}

// The `handleBatch` function processes a batch of directory entries, finds audio file, a
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
				if utils.IsAudioFile(file.Name()) {
					convertToStereo(filepath.Join(songDir, entry.Name(), file.Name()))
					normalizeAudioFile(filepath.Join(songDir, entry.Name(), file.Name()))
				}
			}
		}
	}(batch)
}

func convertToStereo(inputFile string) {
	outputFile := filepath.Base(inputFile) + ".stereo" + filepath.Ext(inputFile)
	channels := getChannelCount(inputFile)
	if channels <= 2 && channels != 0 {
		log.Debugf("Skipping File: %s, already stereo or mono", inputFile)
		return
		cmd := exec.Command("ffmpeg", "-i", inputFile, "-ac", "2", outputFile)
		err := cmd.Run()
		if err != nil {
			log.Errorf("Error converting to stereo: %v", err)
			return
		}
		err = os.Rename(outputFile, inputFile)
		if err != nil {
			log.Errorf("Error renaming file: %v", err)
		}
		log.Debugf("Converted to stereo: %s", inputFile)
	}
}

func normalizeAudioFile(inputFile string) {
	tempFile := filepath.Base(inputFile) + ".tmp" + filepath.Ext(inputFile)

	maxVolume, err := getMaxVolume(inputFile)
	if err != nil {
		log.Errorf("Error getting max volume: %v", err)
		return
	}

	// Calculate gain needed to reach -3dB (3dB below 0dB maximum)
	targetVolume := -3.0
	gainNeeded := targetVolume - maxVolume

	if math.Abs(gainNeeded) <= 0.5 {
		log.Debugf("Skipping File: %s, Max volume: %f dB, Gain needed: %f dB", inputFile, maxVolume, gainNeeded)
		return
	} else {
		log.Debugf("File: %s, Max volume: %f dB, Gain needed: %f dB", inputFile, maxVolume, gainNeeded)
	}

	// Apply gain
	err = applyGain(inputFile, tempFile, gainNeeded)
	if err != nil {
		log.Errorf("Error applying gain: %v", err)
		return
	}
	err = os.Rename(tempFile, inputFile)
	if err != nil {
		os.Remove(tempFile) // Clean up temp file on error
		log.Fatalf("Error replacing original file: %v", err)
	}
}

func getMaxVolume(filename string) (float64, error) {
	cmd := exec.Command("ffmpeg",
		"-hide_banner",
		"-i", filename,
		"-af", "volumedetect",
		"-f", "null", "-")
	output, err := cmd.CombinedOutput()
	if err != nil {
		// ffmpeg returns non-zero exit code when outputting to null, check for volumedetect results
		if !strings.Contains(string(output), "max_volume:") {
			return 0, fmt.Errorf("ffmpeg volumedetect failed: %v", err)
		}
	}

	// Parse the max_volume from output
	lines := strings.SplitSeq(string(output), "\n")
	for line := range lines {
		parts := strings.Fields(line)
		for i, part := range parts {
			if part == "max_volume:" && i+1 < len(parts) {
				volumeStr := strings.TrimSuffix(parts[i+1], " dB")
				volume, err := strconv.ParseFloat(volumeStr, 64)
				if err != nil {
					return 0, fmt.Errorf("failed to parse max volume: %v", err)
				}
				return volume, nil
			}
		}
	}

	return 0, fmt.Errorf("max_volume not found in ffmpeg output")
}

func applyGain(inputFile, outputFile string, gain float64) error {
	gainStr := fmt.Sprintf("%.2f", gain)

	cmd := exec.Command("ffmpeg",
		"-i", filepath.Clean(inputFile),
		"-af", fmt.Sprintf("volume=%sdB", gainStr),
		"-y", filepath.Clean(outputFile))

	output, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("ffmpeg gain application failed: %v\nOutput: %s", err, string(output))
	}
	return nil
}

func getChannelCount(inputFile string) int {
	cmd := exec.Command("ffprobe",
		"-v", "error",
		"-select_streams", "a:0",
		"-show_entries", "stream=channels",
		"-of", "default=noprint_wrappers=1:nokey=1",
		filepath.Clean(inputFile))

	output, err := cmd.Output()
	if err != nil {
		log.Errorf("ffprobe channel count retrieval failed: %v\nOutput: %s", err, string(output))
		return 0
	}
	channels, err := strconv.Atoi(strings.TrimSpace(string(output)))
	if err != nil {
		log.Errorf("failed to parse channel count: %v", err)
		return 0
	}
	return channels
}
