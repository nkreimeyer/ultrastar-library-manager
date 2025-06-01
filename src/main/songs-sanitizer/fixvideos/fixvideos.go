package fixvideos

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/nkreimeyer/ultrastar-songs-sanitizer/utils"
)

const (
	bitrateThreshold = 3500000 // 3.5 Mbit/s
	fullHDWidth      = 1920
	targetWidth      = 1080
)

func Run(SongDir string) error {
	targetDir := SongDir

	err := filepath.Walk(targetDir, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}

		// if info.IsDir() {
		// 	fmt.Printf("Visiting directory: %s\n", path)
		// } else {
		// 	fmt.Printf("Visiting file: %s\n", path)
		// }
		if !info.IsDir() && isVideoFile(info.Name()) {
			// fmt.Printf("Processing file: %s\n", path)
			processVideoFile(path)
		}
		return nil
	})

	if err != nil {
		return err
	}

	fmt.Println("All files have been processed.")
	return nil
}

func processVideoFile(file string) {
	baseName := strings.TrimSuffix(file, filepath.Ext(file))
	outputFile := baseName + "_output"

	//fmt.Printf("Processing file: %s\n", file)

	// Check if the file has an audio stream
	hasAudio := hasAudioStream(file)

	needsResize := getNeedsResize(file)
	needsReEncode := getNeedsReEncode(file) || needsResize

	//fmt.Printf("File: %s, HasAudio: %v, NeedsResize: %v, NeedsReEncode: %v\n", file, hasAudio, needsResize, needsReEncode)

	if !hasAudio && !needsReEncode {
		//fmt.Printf("No processing needed for: %s\n", file)
		return
	}

	// Build ffmpeg command
	args := []string{"-hide_banner", "-loglevel", "error", "-i", filepath.Clean(file)}

	if hasAudio {
		args = append(args, "-an")
	}

	// We need re-encoding if the bitrate is too high or if we need to resize
	if needsReEncode {
		args = append(args, "-c:v", "libx264", "-b:v", "3M")
		outputFile += ".mp4"
	} else {
		args = append(args, "-c:v", "copy")
		outputFile += filepath.Ext(file)
	}

	// Set the resolution scale if resizing is needed
	if needsResize {
		args = append(args, "-vf", fmt.Sprintf("scale=-1:%d", targetWidth))
	}

	args = append(args, "-y", filepath.Clean(outputFile))

	fmt.Printf("Re-encoding file: %s\n", file)
	fmt.Printf("Running ffmpeg with args: %v\n", args)
	// Run ffmpeg command
	cmd := exec.Command("ffmpeg", args...)
	if err := cmd.Run(); err != nil {
		fmt.Printf("Error processing %s: %v\n", file, err)
		return
	}

	// Replace original file with processed file
	replaceOriginalFile(file, outputFile)

	fmt.Printf("Processed file: %s\n", file)
}

func replaceOriginalFile(file, outputFile string) {
	// determine new file name (take extension from outputFile)
	newFileName := file[:len(file)-len(filepath.Ext(file))] + filepath.Ext(outputFile)
	if filepath.Ext(file) != filepath.Ext(outputFile) {
		// replace the original file name with the new file name in the txt file and delete the original file
		utils.ReplaceInUSTxtFile(filepath.Dir(file), filepath.Base(file), filepath.Base(newFileName))
		os.Remove(file)
	}
	if err := os.Rename(outputFile, newFileName); err != nil {
		fmt.Printf("Error renaming %s to %s: %v\n", outputFile, file, err)
	}
}

func hasAudioStream(file string) bool {
	cmd := exec.Command("ffprobe", "-v", "error", "-show_entries", "stream=codec_type", "-of", "default=nokey=1:noprint_wrappers=1", file)
	output, err := cmd.Output()
	// fmt.Printf("Checking audio stream for %s\n", file)
	// fmt.Print(string(output))
	if err != nil {
		fmt.Printf("Error checking audio stream for %s: %v\n", file, err)
		return false
	}
	return strings.Contains(string(output), "audio")
}

func isVideoFile(filename string) bool {
	ext := strings.ToLower(filepath.Ext(filename))
	// fmt.Print(ext)
	return ext == ".mp4" || ext == ".mpeg" || ext == ".mpg" || ext == ".m4v" || ext == ".m2v" || ext == ".avi" ||
		ext == ".mov" || ext == ".mkv" || ext == ".flv" || ext == ".wmv" || ext == ".webm"
}

func getNeedsReEncode(file string) bool {
	// Check video bitrate
	bitrate, err := getVideoBitrate(file)
	if err != nil {
		fmt.Printf("Error getting bitrate for %s: %v\n", file, err)
		bitrate = 999999999999
	}

	// check if the file is vp9 or av1 encoded
	codec, err := getVideoCodec(file)
	if err != nil {
		fmt.Printf("Error getting codec for %s: %v\n", file, err)
		return true
	}

	// Determine if we need to re-encode or resize
	needsReEncode := bitrate > bitrateThreshold || codec == "vp9" || codec == "av1"

	return needsReEncode
}

func getNeedsResize(file string) bool {
	// Check video resolution
	width, _, err := getVideoResolution(file)
	if err != nil {
		fmt.Printf("Error getting resolution for %s: %v\n", file, err)
		return false
	}
	return width > fullHDWidth
}

func getVideoCodec(file string) (string, error) {
	cmd := exec.Command("ffprobe", "-v", "error", "-select_streams", "v:0", "-show_entries", "stream=codec_name", "-of", "csv=p=0", "--", filepath.Clean(file))
	output, err := cmd.Output()
	// fmt.Printf("Checking video codec for %s\n", file)
	// fmt.Print(string(output))
	if err != nil {
		return "", err
	}
	codec := strings.TrimSpace(string(output))
	return codec, nil
}

func getVideoBitrate(file string) (int, error) {
	cmd := exec.Command("ffprobe", "-v", "error", "-select_streams", "v:0", "-show_entries", "stream=bit_rate", "-of", "csv=p=0", "--", filepath.Clean(file))
	output, err := cmd.Output()
	if err != nil {
		return 0, err
	}
	bitrateStr := strings.TrimSpace(string(output))
	bitrate, err := strconv.Atoi(bitrateStr)
	if err != nil {
		return 0, err
	}
	return bitrate, nil
}

func getVideoResolution(file string) (int, int, error) {
	cmd := exec.Command("ffprobe", "-v", "error", "-select_streams", "v:0", "-show_entries", "stream=width,height", "-of", "csv=p=0", "--", filepath.Clean(file))
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
