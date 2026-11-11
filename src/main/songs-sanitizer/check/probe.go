package check

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
)

// errToolFailed marks an ffprobe/ffmpeg run that failed for reasons other than the file
// (the tool could not be started). It is an operation failure, not a finding.
var errToolFailed = errors.New("tool failed")

// maxStderr caps the ffmpeg/ffprobe output kept in memory. Only the first lines are
// quoted, but a badly corrupt video can print a line per frame.
const maxStderr = 64 << 10

// cappedBuffer keeps the first max bytes written to it and discards the rest.
type cappedBuffer struct {
	buf bytes.Buffer
	max int
}

func (b *cappedBuffer) Write(p []byte) (int, error) {
	if room := b.max - b.buf.Len(); room > 0 {
		b.buf.Write(p[:min(len(p), room)])
	}
	return len(p), nil
}

func (b *cappedBuffer) String() string { return b.buf.String() }

// estimatedMsg is what ffprobe logs (at warning level) when it guesses the duration of
// an mp3 from the bitrate of its first frames: a VBR file without Xing/VBRI header, as
// left behind by many cutting tools. The guess can be off by a factor of two or more.
const estimatedMsg = "Estimating duration from bitrate"

// probeMedia runs ffprobe on path (an absolute path, so it never looks like an option)
// and returns the container duration and the stream types. An estimated duration is
// replaced by a measured one (measureDuration); if that fails, DurationEstimated is set.
// An error wrapping errToolFailed means ffprobe could not run; any other error means the
// file is unreadable.
func probeMedia(path string) (mediaInfo, error) {
	// Warning level, to see estimatedMsg; stderr only counts as an error with a non-zero exit.
	cmd := exec.Command("ffprobe", "-v", "warning",
		"-show_entries", "format=duration:stream=codec_type,codec_name",
		"-of", "json", path)
	stderr := &cappedBuffer{max: maxStderr}
	cmd.Stderr = stderr
	out, err := cmd.Output()
	if err != nil {
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) {
			msg := toolMessage(stderr.String(), path, 2)
			if msg == "" {
				msg = err.Error()
			}
			return mediaInfo{}, errors.New(msg)
		}
		return mediaInfo{}, fmt.Errorf("%w: ffprobe: %v", errToolFailed, err)
	}
	info, err := parseProbe(out)
	if err != nil {
		return info, err
	}
	if info.HasAudio && strings.Contains(stderr.String(), estimatedMsg) {
		if d, ok := measureDuration(path); ok {
			info.DurationSec, info.HasDuration = d, true
		} else {
			info.DurationEstimated = true
		}
	}
	return info, nil
}

// measureDuration returns the length of the first audio stream of path by reading all of
// its packets (demuxing only, no decoding: fast). ok is false if it cannot be measured.
func measureDuration(path string) (sec float64, ok bool) {
	cmd := exec.Command("ffmpeg", "-nostdin", "-hide_banner", "-v", "error", "-i", path,
		"-map", "0:a:0", "-c", "copy", "-f", "null", "-progress", "pipe:1", "-nostats", "-")
	cmd.Stderr = &cappedBuffer{max: maxStderr}
	out, err := cmd.Output()
	if err != nil {
		return 0, false
	}
	return parseProgressDuration(string(out))
}

// parseProgressDuration returns the last out_time_us of ffmpeg -progress output, in seconds.
func parseProgressDuration(out string) (float64, bool) {
	sec, ok := 0.0, false
	for _, line := range strings.Split(out, "\n") {
		v, found := strings.CutPrefix(strings.TrimSpace(line), "out_time_us=")
		if !found {
			continue
		}
		if us, err := strconv.ParseInt(v, 10, 64); err == nil && us > 0 {
			sec, ok = float64(us)/1e6, true
		}
	}
	return sec, ok
}

// parseProbe parses the JSON output of probeMedia.
func parseProbe(data []byte) (mediaInfo, error) {
	var probe struct {
		Streams []struct {
			CodecType string `json:"codec_type"`
		} `json:"streams"`
		Format struct {
			Duration string `json:"duration"`
		} `json:"format"`
	}
	if err := json.Unmarshal(data, &probe); err != nil {
		return mediaInfo{}, fmt.Errorf("parsing ffprobe output: %w", err)
	}
	var info mediaInfo
	for _, s := range probe.Streams {
		switch s.CodecType {
		case "audio":
			info.HasAudio = true
		case "video":
			info.HasVideo = true
		}
	}
	// "N/A" or missing for streams without a known length.
	if d, err := strconv.ParseFloat(probe.Format.Duration, 64); err == nil && d > 0 {
		info.DurationSec, info.HasDuration = d, true
	}
	return info, nil
}

// decodeCheck decodes the whole file with ffmpeg. decodeErr is set if ffmpeg failed,
// decodeWarn if it succeeded but reported errors (typically a truncated or partly
// corrupt file, which ffmpeg decodes as far as it can). An error wrapping errToolFailed
// means ffmpeg could not run.
//
// The decoder runs single-threaded: frame-threaded video decoders report the errors of
// a corrupt file in a different order on every run, which would make the report differ.
// It also keeps the memory and CPU use of the parallel workers bounded.
func decodeCheck(path string) (decodeErr, decodeWarn string, err error) {
	cmd := exec.Command("ffmpeg", "-nostdin", "-hide_banner", "-v", "error", "-threads", "1", "-i", path, "-f", "null", "-")
	stderr := &cappedBuffer{max: maxStderr}
	cmd.Stderr = stderr
	if err := cmd.Run(); err != nil {
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) {
			msg := toolMessage(stderr.String(), path, 3)
			if msg == "" {
				msg = err.Error()
			}
			return msg, "", nil
		}
		return "", "", fmt.Errorf("%w: ffmpeg: %v", errToolFailed, err)
	}
	return "", toolMessage(dropNullMuxerLines(stderr.String()), path, 3), nil
}

// dropNullMuxerLines removes the messages of ffmpeg's null output muxer ("[null @ 0x…]
// Application provided invalid, non monotonically increasing dts to muxer"). They are
// about the timestamps handed to the discarded output (typical for AVI files with
// B-frames), not about decoding the input, and UltraStar plays such files fine.
func dropNullMuxerLines(out string) string {
	var kept []string
	for _, l := range strings.Split(out, "\n") {
		if !strings.HasPrefix(strings.TrimSpace(l), "[null") {
			kept = append(kept, l)
		}
	}
	return strings.Join(kept, "\n")
}

// maxMessageLen limits tool output quoted in findings.
const maxMessageLen = 300

// addressRe matches the object addresses in ffmpeg log prefixes ("[mp3 @ 0x55e57d9db000]").
var addressRe = regexp.MustCompile(` @ 0x[0-9a-fA-F]+`)

// toolMessage makes ffmpeg/ffprobe output usable in a deterministic report: object
// addresses are removed, the full path is shortened to the file name, and only the first
// n non-empty lines are kept (see firstLines).
func toolMessage(out, path string, n int) string {
	out = addressRe.ReplaceAllString(out, "")
	out = strings.ReplaceAll(out, path, filepath.Base(path))
	return firstLines(out, n)
}

// firstLines returns the first n non-empty lines of s, trimmed and joined with " | ".
func firstLines(s string, n int) string {
	var lines []string
	for _, l := range strings.Split(s, "\n") {
		if l = strings.TrimSpace(l); l != "" {
			lines = append(lines, l)
			if len(lines) == n {
				break
			}
		}
	}
	out := strings.Join(lines, " | ")
	if len(out) > maxMessageLen {
		// Cut at a rune boundary.
		cut := maxMessageLen
		for cut > 0 && out[cut]&0xC0 == 0x80 {
			cut--
		}
		out = out[:cut] + "…"
	}
	return out
}
