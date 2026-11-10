// Package fixaudio downmixes multichannel audio to stereo and peak-normalizes every audio
// file of the library to -3 dB.
//
// Files are re-encoded with the source codec and (for lossy codecs) the source bitrate,
// keeping metadata and, where the container supports it, embedded cover art. The encoder
// writes to a temporary file next to the input, which then atomically replaces it.
package fixaudio

import (
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"

	"github.com/nkreimeyer/ultrastar-songs-sanitizer/config"
	"github.com/nkreimeyer/ultrastar-songs-sanitizer/fsops"
	"github.com/nkreimeyer/ultrastar-songs-sanitizer/utils"
	log "github.com/sirupsen/logrus"
)

const (
	// targetPeakDB is the peak level files are normalized to.
	targetPeakDB = -3.0
	// peakToleranceDB: files whose peak is within this distance of the target are left alone.
	peakToleranceDB = 0.5
	// maxLossyBitrate caps the bitrate passed to lossy encoders. The fallback probe
	// (container bitrate) includes overhead such as cover art, and a downmixed 5.1 source
	// does not need its full multichannel bitrate in stereo.
	maxLossyBitrate = 320000
	// maxParallelBatches limits how many batches are processed at the same time.
	maxParallelBatches = 16
	// stereoFilter downmixes to stereo inside the filter graph, so the peak can be measured
	// on exactly the signal that is encoded later (an output "-ac 2" would only be applied
	// after volumedetect). rematrix_maxval=1 pins the downmix matrix: by default
	// swresample normalizes it only for integer sample formats, so the measuring graph
	// (s16) and the encoding graph (float, because of the volume filter) would produce
	// differently scaled downmixes. It also guarantees the downmix cannot clip.
	stereoFilter = "aresample=ochl=stereo:rematrix_maxval=1"
	// normalizedTag is a metadata tag written into normalized files. Peak normalization of
	// a lossy file does not converge (the peak moves slightly with every lossy encode), so
	// files carrying the tag are not normalized again on later runs.
	normalizedTag = "SANITIZER_NORMALIZED"
)

// noMetadataExts cannot carry the custom normalizedTag. Both are fine without it: WAV is
// lossless (a re-measured peak stays within the tolerance), raw ADTS AAC is rare.
var noMetadataExts = map[string]bool{".wav": true, ".aac": true}

// batchSize is the number of song folders handled sequentially by one goroutine. It is a
// variable so tests can force folders to be processed concurrently.
var batchSize = 100

// coverExts are the containers verified to carry embedded cover art via "-c:v copy".
// Other formats (ogg/opus store pictures as metadata, wav/wma/aac cannot) map audio only.
var coverExts = map[string]bool{".mp3": true, ".m4a": true, ".flac": true}

// lossyExts are used to decide about "-b:a" when the source codec is unknown.
var lossyExts = map[string]bool{".mp3": true, ".m4a": true, ".aac": true, ".ogg": true, ".opus": true, ".wma": true}

// Run processes all audio files of all song folders below cfg.SongDir.
func Run(cfg config.Config, ops *fsops.Ops) error {
	for _, tool := range []string{"ffmpeg", "ffprobe"} {
		if _, err := exec.LookPath(tool); err != nil {
			return fmt.Errorf("%s not found on PATH: %w", tool, err)
		}
	}

	folders, err := utils.GetAllSongFolders(ops, cfg.SongDir)
	if err != nil {
		return fmt.Errorf("reading song dir %q: %w", cfg.SongDir, err)
	}

	semaphore := make(chan struct{}, maxParallelBatches)
	var wg sync.WaitGroup
	for _, batch := range utils.SplitIntoBatches(folders, batchSize) {
		wg.Add(1)
		go func(batch []string) {
			defer wg.Done()
			semaphore <- struct{}{}
			defer func() { <-semaphore }()
			for _, folder := range batch {
				processFolder(ops, folder)
			}
		}(batch)
	}
	wg.Wait()

	log.Infof("All audio files have been processed.")
	return nil
}

// processFolder processes every audio file directly inside folder.
func processFolder(ops *fsops.Ops, folder string) {
	entries, err := os.ReadDir(folder)
	if err != nil {
		log.Errorf("reading directory %q: %v", folder, err)
		ops.Fail()
		return
	}
	for _, entry := range entries {
		name := entry.Name()
		// Hidden files are skipped: they are leftovers of interrupted runs
		// (".song.sanitizer-123.mp3") or macOS AppleDouble files ("._song.mp3").
		if utils.IsHidden(name) || !utils.IsAudioFile(name) || !utils.IsRegularFile(folder, entry) {
			continue
		}
		processFile(ops, filepath.Join(folder, name))
	}
}

// processFile downmixes and/or normalizes one audio file in a single encode.
func processFile(ops *fsops.Ops, path string) {
	info, err := probeAudio(path)
	if err != nil {
		log.Errorf("Skipping %s: probing audio failed: %v", path, err)
		ops.Fail()
		return
	}

	downmix := info.Channels > 2
	maxVolume, err := getMaxVolume(path, downmix)
	if err != nil {
		log.Errorf("Skipping %s: measuring volume failed: %v", path, err)
		ops.Fail()
		return
	}
	if math.IsInf(maxVolume, -1) {
		log.Warnf("Skipping %s: audio is silent", path)
		return
	}

	gain := targetPeakDB - maxVolume
	needGain := math.Abs(gain) > peakToleranceDB && !info.Normalized
	if info.Normalized && math.Abs(gain) > peakToleranceDB {
		log.Debugf("Not normalizing %s again: it carries the %s tag (max volume %.1f dB)", path, normalizedTag, maxVolume)
	}
	if !downmix && !needGain {
		log.Debugf("Skipping %s: %d channel(s), max volume %.1f dB, gain needed %.2f dB", path, info.Channels, maxVolume, gain)
		return
	}

	var filters, actions []string
	if downmix {
		filters = append(filters, stereoFilter)
		actions = append(actions, fmt.Sprintf("downmix %d channels to stereo", info.Channels))
	}
	if needGain {
		filters = append(filters, fmt.Sprintf("volume=%.2fdB", gain))
		actions = append(actions, fmt.Sprintf("apply %+.2f dB gain", gain))
	}
	action := strings.Join(actions, " and ")

	if ops.DryRun {
		ops.PlanReplace(path, fmt.Sprintf("%s (max volume %.1f dB) to", action, maxVolume))
		return
	}

	tmp, err := fsops.TempFileNear(path, strings.ToLower(filepath.Ext(path)))
	if err != nil {
		log.Errorf("Skipping %s: creating temp file: %v", path, err)
		ops.Fail()
		return
	}
	if err := encode(path, tmp, info, filters); err != nil {
		os.Remove(tmp)
		log.Errorf("Failed to %s to %s: %v", action, path, err)
		ops.Fail()
		return
	}
	if err := ops.Replace(tmp, path); err != nil {
		log.Errorf("Failed to %s to %s: %v", action, path, err)
		return
	}
	log.Infof("Processed %s: %s", path, action)
}

// audioInfo describes the first audio stream of a file.
type audioInfo struct {
	Codec     string
	Channels  int
	SampleFmt string
	Bitrate   int // bits per second, 0 if unknown
	// Normalized reports that the file carries normalizedTag, i.e. it was normalized by an
	// earlier run.
	Normalized bool
}

// probeAudio reads codec, channel count, sample format and bitrate of the first audio
// stream. The stream bitrate is preferred; if the container does not report one (e.g.
// opus), the overall format bitrate is used instead.
func probeAudio(path string) (audioInfo, error) {
	out, err := exec.Command("ffprobe",
		"-v", "error",
		"-select_streams", "a:0",
		"-show_entries", "stream=codec_name,channels,sample_fmt,bit_rate:stream_tags:format=bit_rate:format_tags",
		"-of", "json",
		path).Output()
	if err != nil {
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) {
			return audioInfo{}, fmt.Errorf("ffprobe: %v: %s", err, strings.TrimSpace(string(exitErr.Stderr)))
		}
		return audioInfo{}, fmt.Errorf("ffprobe: %w", err)
	}
	return parseProbe(out)
}

// hasTag reports whether tags contains name (tag names are case-insensitive).
func hasTag(tags map[string]string, name string) bool {
	for k := range tags {
		if strings.EqualFold(k, name) {
			return true
		}
	}
	return false
}

func parseProbe(data []byte) (audioInfo, error) {
	var probe struct {
		Streams []struct {
			CodecName string            `json:"codec_name"`
			Channels  int               `json:"channels"`
			SampleFmt string            `json:"sample_fmt"`
			BitRate   string            `json:"bit_rate"`
			Tags      map[string]string `json:"tags"`
		} `json:"streams"`
		Format struct {
			BitRate string            `json:"bit_rate"`
			Tags    map[string]string `json:"tags"`
		} `json:"format"`
	}
	if err := json.Unmarshal(data, &probe); err != nil {
		return audioInfo{}, fmt.Errorf("parsing ffprobe output: %w", err)
	}
	if len(probe.Streams) == 0 {
		return audioInfo{}, errors.New("no audio stream")
	}
	s := probe.Streams[0]
	if s.Channels <= 0 {
		return audioInfo{}, errors.New("unknown channel count")
	}
	info := audioInfo{Codec: s.CodecName, Channels: s.Channels, SampleFmt: s.SampleFmt,
		Normalized: hasTag(s.Tags, normalizedTag) || hasTag(probe.Format.Tags, normalizedTag)}
	for _, br := range []string{s.BitRate, probe.Format.BitRate} {
		if n, err := strconv.Atoi(br); err == nil && n > 0 {
			info.Bitrate = n
			break
		}
	}
	return info, nil
}

// maxVolumeRe matches volumedetect's result line, e.g. "max_volume: -12.3 dB" or "-inf dB".
var maxVolumeRe = regexp.MustCompile(`max_volume:\s*(\S+) dB`)

// getMaxVolume returns the peak level in dBFS (-Inf for silence). If downmix is set, the
// peak of the stereo downmix is measured, because that is what will be encoded.
func getMaxVolume(path string, downmix bool) (float64, error) {
	filter := "volumedetect"
	if downmix {
		filter = stereoFilter + "," + filter
	}
	out, err := exec.Command("ffmpeg",
		"-nostdin", "-hide_banner",
		"-i", path,
		"-map", "0:a:0",
		"-af", filter,
		"-f", "null", "-").CombinedOutput()
	vol, perr := parseMaxVolume(string(out))
	if perr != nil {
		if err != nil {
			return 0, fmt.Errorf("ffmpeg volumedetect: %v: %s", err, lastLines(string(out), 3))
		}
		return 0, perr
	}
	return vol, nil
}

func parseMaxVolume(output string) (float64, error) {
	m := maxVolumeRe.FindStringSubmatch(output)
	if m == nil {
		return 0, errors.New("max_volume not found in ffmpeg output")
	}
	vol, err := strconv.ParseFloat(m[1], 64) // also accepts "-inf"
	if err != nil {
		return 0, fmt.Errorf("parsing max volume %q: %w", m[1], err)
	}
	return vol, nil
}

// encode runs ffmpeg to write the processed version of in to out (an existing temp file).
func encode(in, out string, info audioInfo, filters []string) error {
	output, err := exec.Command("ffmpeg", encodeArgs(in, out, info, filters)...).CombinedOutput()
	if err != nil {
		return fmt.Errorf("ffmpeg: %v: %s", err, lastLines(string(output), 5))
	}
	return nil
}

// encodeArgs builds the ffmpeg arguments for re-encoding in to out with the given audio
// filters, keeping codec, quality, metadata and (where supported) cover art.
func encodeArgs(in, out string, info audioInfo, filters []string) []string {
	ext := strings.ToLower(filepath.Ext(in))
	args := []string{
		"-nostdin", "-hide_banner", "-v", "error",
		"-y", // out already exists (created by fsops.TempFileNear)
		"-i", in,
		"-map", "0:a:0",
	}
	if coverExts[ext] {
		// Embedded cover art is a video stream with the attached_pic disposition; copy it
		// unchanged. "?" makes the mapping optional for files without a cover.
		args = append(args, "-map", "0:v?", "-c:v", "copy")
	}
	args = append(args, "-map_metadata", "0")
	if !noMetadataExts[ext] {
		args = append(args, "-metadata", fmt.Sprintf("%s=%.1fdB", normalizedTag, targetPeakDB))
		if ext == ".m4a" {
			args = append(args, "-movflags", "+use_metadata_tags") // needed for custom tags in MP4
		}
	}
	if len(filters) > 0 {
		args = append(args, "-af", strings.Join(filters, ","))
	}

	encoder, lossy, known := encoderFor(info.Codec)
	if !known {
		lossy = lossyExts[ext]
	}
	if encoder != "" {
		args = append(args, "-c:a", encoder)
	}
	switch {
	case lossy && info.Bitrate > 0:
		args = append(args, "-b:a", strconv.Itoa(min(info.Bitrate, maxLossyBitrate)))
	case !lossy && known && info.SampleFmt != "":
		// Keep the bit depth: the volume filter outputs float, which would otherwise turn
		// a 16-bit FLAC into a 24-bit one.
		args = append(args, "-sample_fmt", info.SampleFmt)
	}
	return append(args, out)
}

// encoderFor returns the ffmpeg encoder that produces the given codec, and whether it is
// lossy. known is false for codecs not listed here; ffmpeg then picks its default encoder
// for the file extension.
func encoderFor(codec string) (encoder string, lossy, known bool) {
	switch codec {
	case "mp3":
		return "libmp3lame", true, true
	case "aac":
		return "aac", true, true
	case "vorbis":
		return "libvorbis", true, true
	case "opus":
		return "libopus", true, true
	case "wmav1", "wmav2":
		return codec, true, true
	case "flac", "alac":
		return codec, false, true
	}
	if strings.HasPrefix(codec, "pcm_") {
		// The PCM codec fixes the sample format, e.g. pcm_s24le stays 24-bit.
		return codec, false, true
	}
	return "", false, false
}

// lastLines returns the last n non-empty lines of s, for compact error messages.
func lastLines(s string, n int) string {
	lines := strings.Split(strings.TrimSpace(s), "\n")
	if len(lines) > n {
		lines = lines[len(lines)-n:]
	}
	return strings.Join(lines, " | ")
}
