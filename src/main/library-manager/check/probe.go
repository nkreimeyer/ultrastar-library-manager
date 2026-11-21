package check

import (
	"context"
	"errors"
	"fmt"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"

	"github.com/nkreimeyer/library-manager/mediatools"
)

// errToolFailed marks an ffprobe/ffmpeg run that failed for reasons other than the file
// (the tool could not be started). It is an operation failure, not a finding.
var errToolFailed = errors.New("tool failed")

// maxStderr caps the ffmpeg/ffprobe output kept in memory. Only the first lines are
// quoted, but a badly corrupt video can print a line per frame.
const maxStderr = 64 << 10

// capStderr returns a writer for ffmpeg/ffprobe messages. Only their start is used (its
// Head): the report quotes the first lines, so it does not change with a longer tail.
func capStderr() *mediatools.OutputCap { return mediatools.NewOutputCap(maxStderr, 0) }

// estimatedMsg is what ffprobe logs (at warning level) when it guesses the duration of
// an mp3 from the bitrate of its first frames: a VBR file without Xing/VBRI header, as
// left behind by many cutting tools. The guess can be off by a factor of two or more.
const estimatedMsg = "Estimating duration from bitrate"

// probeMedia runs ffprobe on path (an absolute path) and returns the container duration,
// the stream types, the audio bitrate and the video size and bitrate. An estimated
// duration is replaced by a measured one (measureDuration); if that fails,
// DurationEstimated is set. An error wrapping errToolFailed means ffprobe could not run;
// any other error means the file is unreadable.
func probeMedia(path string) (mediaInfo, error) {
	probe, err := mediatools.Probe(context.Background(), path)
	if err != nil {
		var probeErr *mediatools.ProbeError
		switch {
		case errors.As(err, &probeErr):
			msg := toolMessage(probe.Log, path, 2)
			if msg == "" {
				msg = probeErr.Err.Error()
			}
			return mediaInfo{}, errors.New(msg)
		case errors.Is(err, mediatools.ErrNotRun):
			return mediaInfo{}, fmt.Errorf("%w: %v", errToolFailed, err)
		}
		return mediaInfo{}, err // unparsable output
	}
	info := fromProbe(probe)
	if info.HasAudio && strings.Contains(probe.Log, estimatedMsg) {
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
	cmd.Stderr = capStderr()
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

// fromProbe extracts what check uses from an ffprobe result.
func fromProbe(probe mediatools.ProbeInfo) mediaInfo {
	var info mediaInfo
	for _, s := range probe.Streams {
		switch s.CodecType {
		case "audio":
			if !info.HasAudio {
				info.AudioBitrate = mediatools.ParseBitrate(s.BitRate)
			}
			info.HasAudio = true
		case "video":
			if s.Disposition.AttachedPic != 0 {
				continue // cover art of an audio file, not a video
			}
			info.HasVideo = true
			if info.VideoHeight == 0 && s.Width > 0 && s.Height > 0 {
				info.VideoWidth, info.VideoHeight = s.Width, s.Height
				// Matroska/WebM streams carry no bit_rate; mkvmerge writes a BPS tag.
				info.VideoBitrate = mediatools.ParseBitrate(s.BitRate)
				for _, tag := range []string{"BPS", "BPS-eng"} {
					if info.VideoBitrate == 0 {
						info.VideoBitrate = mediatools.ParseBitrate(tagFold(s.Tags, tag))
					}
				}
			}
		}
	}
	// Streams in some containers (Ogg, Matroska, WebM) carry no bit_rate; the container
	// rate is then the audio rate, unless a video stream is part of it.
	if info.HasAudio && info.AudioBitrate == 0 && info.VideoHeight == 0 {
		info.AudioBitrate = mediatools.ParseBitrate(probe.Format.BitRate)
	}
	// Likewise the container rate is the video rate of a file without audio.
	if !info.HasAudio && info.VideoHeight > 0 && info.VideoBitrate == 0 {
		info.VideoBitrate = mediatools.ParseBitrate(probe.Format.BitRate)
	}
	// "N/A" or missing for streams without a known length.
	info.DurationSec, info.HasDuration = mediatools.ParseSeconds(probe.Format.Duration)
	return info
}

// tagFold returns the value of the tag key, matched case-insensitively.
func tagFold(tags map[string]string, key string) string {
	for k, v := range tags {
		if strings.EqualFold(k, key) {
			return v
		}
	}
	return ""
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
	stderr := capStderr()
	cmd.Stderr = stderr
	if err := cmd.Run(); err != nil {
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) {
			msg := toolMessage(stderr.Head(), path, 3)
			if msg == "" {
				msg = err.Error()
			}
			return msg, "", nil
		}
		return "", "", fmt.Errorf("%w: ffmpeg: %v", errToolFailed, err)
	}
	return "", toolMessage(dropNullMuxerLines(stderr.Head()), path, 3), nil
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
