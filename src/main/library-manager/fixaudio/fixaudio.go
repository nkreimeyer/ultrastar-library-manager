// Package fixaudio downmixes multichannel audio to stereo and peak-normalizes every audio
// file of the library to -3 dB.
//
// Files are re-encoded with the source codec and (for lossy codecs) the source bitrate,
// keeping metadata and, where the container supports it, embedded cover art. The encoder
// writes to a temporary file next to the input. A copy of the original is kept in
// cfg.OriginalsDir() before the temporary file atomically replaces it.
//
// Stems referenced by #VOCALS or #INSTRUMENTAL are never touched: normalizing them
// independently would change their mix balance.
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
	"runtime"
	"strconv"
	"strings"
	"sync"

	"github.com/nkreimeyer/library-manager/config"
	"github.com/nkreimeyer/library-manager/fsops"
	"github.com/nkreimeyer/library-manager/journal"
	"github.com/nkreimeyer/library-manager/utils"
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
	// maxWorkers caps the number of song folders processed at the same time.
	maxWorkers = 16
	// stereoFilter downmixes to stereo inside the filter graph, so the peak can be measured
	// on exactly the signal that is encoded later (an output "-ac 2" would only be applied
	// after the measurement). rematrix_maxval=1 pins the downmix matrix: by default
	// swresample normalizes it only for integer sample formats, so graphs negotiating
	// different sample formats would produce differently scaled downmixes. It also
	// guarantees the downmix cannot clip.
	stereoFilter = "aresample=ochl=stereo:rematrix_maxval=1"
	// peakFilter measures the true sample peak. It runs on float samples: lossy decoders
	// output float and their peaks can exceed 0 dBFS, which an integer measurement (such
	// as volumedetect, which works on s16) would clamp to 0 dB. The volume filter of the
	// encode works on float as well.
	peakFilter = "aformat=sample_fmts=flt,astats=measure_perchannel=none:measure_overall=Peak_level"
	// journalKind is the media journal entry for files normalized by this module.
	journalKind = "audio-normalized"
	// normalizedTag is a metadata tag written into normalized files. Peak normalization of
	// a lossy file does not converge (the peak moves slightly with every lossy encode), so
	// files carrying the tag are not normalized again on later runs.
	normalizedTag = "SANITIZER_NORMALIZED"
)

// noMetadataExts cannot carry the custom normalizedTag. Files of these formats are
// recorded in the media journal instead (see processor.journal), which matters most for
// lossy raw ADTS AAC, whose peak drifts with every re-encode. MP4 (.m4a) could carry it
// only with "-movflags +use_metadata_tags", which writes all metadata as QuickTime mdta
// keys instead of iTunes ilst atoms: the cover ("covr") is dropped and players reading
// iTunes tags (TagLib, mutagen, iTunes) lose title and artist.
var noMetadataExts = map[string]bool{".wav": true, ".aac": true, ".m4a": true}

// workers is the number of song folders processed at the same time. It is a variable so
// tests can force folders to be processed concurrently on machines with a single CPU.
var workers = min(maxWorkers, runtime.NumCPU())

// coverExts are the containers verified to carry embedded cover art via "-c:v copy".
// Other formats (ogg/opus store pictures as metadata, wav/wma/aac cannot) map audio only.
// Matroska (.mka) is not among them: ffmpeg reads a cover attachment as a picture
// stream, but writes it back as a video track, not as an attachment, so it is dropped
// (the original keeps it, in originals/).
var coverExts = map[string]bool{".mp3": true, ".m4a": true, ".flac": true}

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
	// Without the journal, files without a marker tag would be re-encoded on every run.
	j, err := journal.Open(cfg.JournalPath())
	if err != nil {
		return fmt.Errorf("opening media journal: %w", err)
	}

	p := &processor{cfg: cfg, ops: ops, journal: j}
	forEachParallel(folders, workers, p.processFolder)

	if err := j.Save(ops); err != nil {
		log.Errorf("Saving media journal: %v", err) // counted by ops
	}
	log.Infof("All audio files have been processed.")
	return nil
}

// forEachParallel calls fn for every item, using up to n goroutines.
func forEachParallel[T any](items []T, n int, fn func(T)) {
	n = max(1, min(n, len(items)))
	queue := make(chan T)
	var wg sync.WaitGroup
	wg.Add(n)
	for range n {
		go func() {
			defer wg.Done()
			for item := range queue {
				fn(item)
			}
		}()
	}
	for _, item := range items {
		queue <- item
	}
	close(queue)
	wg.Wait()
}

// processor holds the state of one run. It is safe for concurrent use.
type processor struct {
	cfg config.Config
	ops *fsops.Ops
	// journal records normalized files, for formats that cannot carry normalizedTag.
	journal *journal.Journal
}

// processFolder processes every audio file directly inside folder.
func (p *processor) processFolder(folder string) {
	entries, err := os.ReadDir(folder)
	if err != nil {
		log.Errorf("reading directory %q: %v", folder, err)
		p.ops.FailOnce("unreadable:" + folder) // same key as the folder discovery
		return
	}
	stems, err := stemFiles(p.ops, folder)
	if err != nil {
		// Without the txt it is unknown which files are stems; leave the folder alone.
		log.Errorf("Skipping %s: reading the song txt: %v", folder, err)
		p.ops.Fail()
		return
	}
	for _, entry := range entries {
		name := entry.Name()
		// Hidden files are skipped: they are leftovers of interrupted runs
		// (".song.sanitizer-123.mp3") or macOS AppleDouble files ("._song.mp3").
		if utils.IsHidden(name) || !utils.IsAudioFile(name) || !utils.IsRegularFile(folder, entry) {
			continue
		}
		if stems[name] {
			log.Infof("Skipping %s: it is a #VOCALS/#INSTRUMENTAL stem, normalizing it would change the mix balance", filepath.Join(folder, name))
			continue
		}
		p.processFile(folder, name)
	}
}

// stemFiles returns the on-disk names of the files referenced by the #VOCALS and
// #INSTRUMENTAL tags of the song txt in folder, read through ops (see utils.ReadSongTags).
// A repeated tag resolves like UltraStar does: the last occurrence names the stem.
// A folder without a song txt has no stems.
func stemFiles(ops *fsops.Ops, folder string) (map[string]bool, error) {
	tags, err := utils.ReadSongTags(ops, folder)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	stems := map[string]bool{}
	for _, tag := range []string{"VOCALS", "INSTRUMENTAL"} {
		if name, _, found := utils.ResolveFileName(folder, tags[tag]); found {
			stems[name] = true
		}
	}
	return stems, nil
}

// processFile downmixes and/or normalizes the audio file name in folder in a single
// encode, keeping a copy of the original.
func (p *processor) processFile(folder, name string) {
	ops := p.ops
	path := filepath.Join(folder, name)
	if ops.WouldBeMoved(path) {
		log.Debugf("Skipping %s: an earlier module would have moved it", path)
		return
	}

	info, err := probeAudio(path)
	if err != nil {
		log.Errorf("Skipping %s: probing audio failed: %v", path, err)
		ops.Fail()
		return
	}
	if why := cannotKeep(info); why != "" {
		log.Warnf("Skipping %s: %s, it is left unchanged", path, why)
		return
	}

	downmix := info.Channels > 2
	peak, err := measurePeak(path, downmix)
	if err != nil {
		log.Errorf("Skipping %s: measuring the peak failed: %v", path, err)
		ops.Fail()
		return
	}
	if math.IsInf(peak, -1) {
		log.Warnf("Skipping %s: audio is silent", path)
		return
	}

	gain := targetPeakDB - peak
	needGain := math.Abs(gain) > peakToleranceDB
	if needGain && (info.Normalized || p.journal.Has(journalKind, path)) {
		log.Debugf("Not normalizing %s again: it was normalized by an earlier run (peak %.1f dB)", path, peak)
		needGain = false
	}
	if !downmix && !needGain {
		log.Debugf("Skipping %s: %d channel(s), peak %.1f dB, gain needed %.2f dB", path, info.Channels, peak, gain)
		return
	}

	filters := baseFilters(downmix)
	var actions []string
	if downmix {
		actions = append(actions, fmt.Sprintf("downmix %d channels to stereo", info.Channels))
	}
	if needGain {
		filters = append(filters, fmt.Sprintf("volume=%.2fdB", gain))
		actions = append(actions, fmt.Sprintf("apply %+.2f dB gain", gain))
	}
	action := strings.Join(actions, " and ")
	backupRel := filepath.Join(p.cfg.RelPath(folder), name)

	if ops.DryRun {
		// Both calls only log and count in dry-run mode.
		if _, err := ops.CopyInto(path, p.cfg.OriginalsDir(), backupRel); err != nil {
			log.Errorf("Would not %s to %s: %v", action, path, err)
			return
		}
		ops.PlanReplace(path, fmt.Sprintf("%s (peak %.1f dB) to", action, peak))
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
	// The original is only replaced once a copy of it is safe.
	if _, err := ops.CopyInto(path, p.cfg.OriginalsDir(), backupRel); err != nil {
		os.Remove(tmp)
		log.Errorf("Not replacing %s: keeping a copy of the original failed: %v", path, err)
		return
	}
	if err := ops.Replace(tmp, path); err != nil {
		log.Errorf("Failed to %s to %s: %v", action, path, err)
		return
	}
	// A failing Save is counted by ops (WriteReport); a failing Add (fingerprinting the
	// new file) is not.
	if err := p.journal.Add(journalKind, path); err != nil {
		log.Errorf("Recording %s in the media journal: %v", path, err)
		ops.Fail()
	} else if err := p.journal.Save(ops); err != nil {
		log.Errorf("Saving the media journal after %s: %v", path, err) // counted by ops
	}
	log.Infof("Processed %s: %s", path, action)
}

// baseFilters returns the filters that precede the gain in the encode. The peak is
// measured after exactly these filters, so the gain is computed on the encoded signal.
func baseFilters(downmix bool) []string {
	if downmix {
		return []string{stereoFilter}
	}
	return nil
}

// heAACProfiles are AAC profiles with SBR (and PS). ffmpeg's native aac encoder only
// produces AAC-LC, which at the low bitrates HE-AAC is used with sounds much worse.
var heAACProfiles = map[string]bool{"HE-AAC": true, "HE-AACv2": true}

// cannotKeep returns why the audio described by info cannot be re-encoded as itself, or
// "" if it can.
func cannotKeep(info audioInfo) string {
	if _, _, known := encoderFor(info.Codec); !known {
		// ffmpeg would pick its default encoder for the extension (Vorbis at a low default
		// quality for .mka): a silent loss of quality, fixed in place by the marker tag.
		return fmt.Sprintf("no encoder known to keep its %q audio", info.Codec)
	}
	if info.Codec == "aac" && heAACProfiles[info.Profile] {
		return fmt.Sprintf("its %s audio would become AAC-LC (ffmpeg's aac encoder has no SBR)", info.Profile)
	}
	return ""
}

// audioInfo describes the first audio stream of a file.
type audioInfo struct {
	Codec     string
	Profile   string // codec profile as ffprobe names it, e.g. "LC" or "HE-AAC"
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
		"-show_entries", "stream=codec_name,profile,channels,sample_fmt,bit_rate:stream_tags:format=bit_rate:format_tags",
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
			Profile   string            `json:"profile"`
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
	info := audioInfo{Codec: s.CodecName, Profile: s.Profile, Channels: s.Channels, SampleFmt: s.SampleFmt,
		Normalized: hasTag(s.Tags, normalizedTag) || hasTag(probe.Format.Tags, normalizedTag)}
	for _, br := range []string{s.BitRate, probe.Format.BitRate} {
		if n, err := strconv.Atoi(br); err == nil && n > 0 {
			info.Bitrate = n
			break
		}
	}
	return info, nil
}

// peakLevelRe matches astats' result line, e.g. "Peak level dB: -12.345678" or "-inf".
var peakLevelRe = regexp.MustCompile(`Peak level dB:\s*(\S+)`)

// measurePeak returns the sample peak in dBFS (-Inf for silence), measured on float
// samples so that peaks above 0 dBFS are not clamped. If downmix is set, the peak of the
// stereo downmix is measured, because that is what will be encoded.
func measurePeak(path string, downmix bool) (float64, error) {
	filter := strings.Join(append(baseFilters(downmix), peakFilter), ",")
	out, err := exec.Command("ffmpeg",
		"-nostdin", "-hide_banner", "-v", "info",
		"-i", path,
		"-map", "0:a:0",
		"-af", filter,
		"-f", "null", "-").CombinedOutput()
	peak, perr := parsePeakLevel(string(out))
	if perr != nil {
		if err != nil {
			return 0, fmt.Errorf("ffmpeg astats: %v: %s", err, lastLines(string(out), 3))
		}
		return 0, perr
	}
	return peak, nil
}

// parsePeakLevel returns the last peak level in astats output, i.e. the overall one if
// per-channel values were printed as well.
func parsePeakLevel(output string) (float64, error) {
	m := peakLevelRe.FindAllStringSubmatch(output, -1)
	if m == nil {
		return 0, errors.New("peak level not found in ffmpeg output")
	}
	value := m[len(m)-1][1]
	peak, err := strconv.ParseFloat(value, 64) // also accepts "-inf"
	if err != nil {
		return 0, fmt.Errorf("parsing peak level %q: %w", value, err)
	}
	return peak, nil
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
	}
	if len(filters) > 0 {
		args = append(args, "-af", strings.Join(filters, ","))
	}

	// processFile only encodes codecs encoderFor knows.
	encoder, lossy, _ := encoderFor(info.Codec)
	if encoder != "" {
		args = append(args, "-c:a", encoder)
	}
	switch {
	case lossy && info.Bitrate > 0:
		args = append(args, "-b:a", strconv.Itoa(min(info.Bitrate, maxLossyBitrate)))
	case !lossy && info.SampleFmt != "":
		// Keep the bit depth: the volume filter outputs float, which would otherwise turn
		// a 16-bit FLAC into a 24-bit one.
		args = append(args, "-sample_fmt", info.SampleFmt)
	}
	return append(args, out)
}

// encoderFor returns the ffmpeg encoder that produces the given codec, and whether it is
// lossy. known is false for codecs not listed here (DTS, TrueHD, ...: their ffmpeg
// encoders are experimental or missing); such files are not re-encoded.
func encoderFor(codec string) (encoder string, lossy, known bool) {
	switch codec {
	case "mp3":
		return "libmp3lame", true, true
	case "mp2", "ac3", "eac3":
		return codec, true, true // ffmpeg's native encoders
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
