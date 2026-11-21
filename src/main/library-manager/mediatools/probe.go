package mediatools

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
)

// ErrNotRun marks a Probe whose ffprobe could not be started (not on PATH, not
// executable). It is a problem of the tool, not of the file.
var ErrNotRun = errors.New("ffprobe could not be run")

// ProbeError is returned by Probe when ffprobe ran but failed (it exited with an error):
// the file is unreadable.
type ProbeError struct {
	Err    error  // the *exec.ExitError
	Stderr string // ffprobe's messages (capped, see CapOutput)
}

func (e *ProbeError) Error() string {
	if msg := strings.TrimSpace(e.Stderr); msg != "" {
		return fmt.Sprintf("ffprobe: %v: %s", e.Err, msg)
	}
	return fmt.Sprintf("ffprobe: %v", e.Err)
}

func (e *ProbeError) Unwrap() error { return e.Err }

// ProbeInfo is what ffprobe reports about a media file ("-show_format -show_streams").
// Numbers ffprobe prints as strings (bit_rate, duration) stay strings: they may be "N/A"
// or missing; see ParseBitrate and ParseSeconds.
type ProbeInfo struct {
	Streams []Stream `json:"streams"`
	Format  Format   `json:"format"`
	// Log is what ffprobe printed at warning level (capped, see CapOutput), e.g.
	// "Estimating duration from bitrate, this may be inaccurate".
	Log string `json:"-"`
}

// Stream is one stream of a ProbeInfo.
type Stream struct {
	Index       int               `json:"index"`
	CodecType   string            `json:"codec_type"` // "video", "audio", "subtitle", "data", "attachment"
	CodecName   string            `json:"codec_name"`
	Profile     string            `json:"profile"`
	Width       int               `json:"width"`
	Height      int               `json:"height"`
	PixFmt      string            `json:"pix_fmt"`
	SampleFmt   string            `json:"sample_fmt"`
	Channels    int               `json:"channels"`
	RFrameRate  string            `json:"r_frame_rate"` // "30000/1001"
	BitRate     string            `json:"bit_rate"`
	Duration    string            `json:"duration"`
	Disposition Disposition       `json:"disposition"`
	Tags        map[string]string `json:"tags"`
}

// Disposition holds the stream disposition flags that are used.
type Disposition struct {
	// AttachedPic is 1 for cover art stored as a (one-frame) video stream.
	AttachedPic int `json:"attached_pic"`
}

// Format is the container part of a ProbeInfo.
type Format struct {
	Duration string            `json:"duration"`
	BitRate  string            `json:"bit_rate"`
	Tags     map[string]string `json:"tags"`
}

// Probe runs ffprobe once on path and returns everything it reports about the container
// and its streams. An error wrapping ErrNotRun means ffprobe could not be started; a
// *ProbeError means it failed on the file; any other error means its output could not
// be parsed. Log is set in every case ffprobe ran.
func Probe(ctx context.Context, path string) (ProbeInfo, error) {
	// "--": a file name starting with "-" is no option. Warning level, so that Log holds
	// warnings such as an estimated duration; they only count as an error with a
	// non-zero exit.
	cmd := exec.CommandContext(ctx, "ffprobe", "-v", "warning", "-print_format", "json",
		"-show_format", "-show_streams", "--", filepath.Clean(path))
	stderr := CapOutput()
	cmd.Stderr = stderr
	out, err := cmd.Output() // parsed as data: read in full
	if err != nil {
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) {
			return ProbeInfo{Log: stderr.String()}, &ProbeError{Err: err, Stderr: stderr.String()}
		}
		return ProbeInfo{}, fmt.Errorf("%w: %v", ErrNotRun, err)
	}
	info, err := ParseProbe(out)
	info.Log = stderr.String()
	return info, err
}

// ParseProbe parses ffprobe's JSON output ("-print_format json -show_format -show_streams",
// or a subset of it).
func ParseProbe(data []byte) (ProbeInfo, error) {
	var info ProbeInfo
	if err := json.Unmarshal(data, &info); err != nil {
		return ProbeInfo{}, fmt.Errorf("parsing ffprobe output: %w", err)
	}
	return info, nil
}

// FirstStream returns the first stream of the given codec type, as ffprobe's
// "-select_streams v:0" / "a:0" select it (an attached picture counts as a video stream).
func (p ProbeInfo) FirstStream(codecType string) (Stream, bool) {
	for _, s := range p.Streams {
		if s.CodecType == codecType {
			return s, true
		}
	}
	return Stream{}, false
}

// HasStream reports whether the file has a stream of the given codec type.
func (p ProbeInfo) HasStream(codecType string) bool {
	_, ok := p.FirstStream(codecType)
	return ok
}

// ParseBitrate parses an ffprobe bit rate or BPS tag ("128000", "N/A", ""; blanks
// around it are ignored); 0 if it is not a positive number.
func ParseBitrate(s string) int64 {
	if n, err := strconv.ParseInt(strings.TrimSpace(s), 10, 64); err == nil && n > 0 {
		return n
	}
	return 0
}

// ParseSeconds parses an ffprobe duration ("123.456000", "N/A", ""); ok is false unless
// it is a positive number.
func ParseSeconds(s string) (sec float64, ok bool) {
	if d, err := strconv.ParseFloat(s, 64); err == nil && d > 0 {
		return d, true
	}
	return 0, false
}

// VideoBitrate returns the bitrate in bit/s of the video stream s of probe, by the rule
// check and fixvideos share (organize compares check's value with
// fixvideos.BitrateThreshold, so both must read the same number):
//
//  1. the stream's bit_rate;
//  2. else its BPS statistics tag (mkvmerge writes one per Matroska track, as "BPS" or,
//     for a track with a language, "BPS-<lang>"): a plain BPS (any case) first, then the
//     BPS-* tags in sorted order, so the result does not depend on map order;
//  3. else the container bit_rate, if the file has no audio stream: it is then the video
//     rate plus the container overhead. With audio it would count the audio as video;
//  4. else 0 (unknown).
func VideoBitrate(probe ProbeInfo, s Stream) int64 {
	if b := ParseBitrate(s.BitRate); b > 0 {
		return b
	}
	var plain, lang []string
	for k := range s.Tags {
		switch {
		case strings.EqualFold(k, "BPS"):
			plain = append(plain, k)
		case len(k) > 4 && strings.EqualFold(k[:4], "BPS-"):
			lang = append(lang, k)
		}
	}
	slices.Sort(plain)
	slices.Sort(lang)
	for _, k := range append(plain, lang...) {
		if b := ParseBitrate(s.Tags[k]); b > 0 {
			return b
		}
	}
	if !probe.HasStream("audio") {
		return ParseBitrate(probe.Format.BitRate)
	}
	return 0
}

// HasTag reports whether the container or any of the streams carries the tag name,
// whatever its value. Tag names are matched case-insensitively: some muxers change their
// case (Ogg/Vorbis comments, MP4 with use_metadata_tags). This is how the media modules
// recognize the marker tags they write (SANITIZER_VIDEO, SANITIZER_NORMALIZED): ffmpeg
// stores the global "-metadata" in the container or, for formats with per-stream
// comments (Ogg), in the stream, and nothing but these modules writes the names.
func (p ProbeInfo) HasTag(name string) bool {
	if hasKey(p.Format.Tags, name) {
		return true
	}
	for _, s := range p.Streams {
		if hasKey(s.Tags, name) {
			return true
		}
	}
	return false
}

func hasKey(tags map[string]string, name string) bool {
	for k := range tags {
		if strings.EqualFold(k, name) {
			return true
		}
	}
	return false
}
