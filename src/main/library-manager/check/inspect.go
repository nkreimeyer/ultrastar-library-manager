package check

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"path/filepath"

	log "github.com/sirupsen/logrus"

	"github.com/nkreimeyer/library-manager/fsops"
	"github.com/nkreimeyer/library-manager/mediatools"
	"github.com/nkreimeyer/library-manager/songtxt"
	"github.com/nkreimeyer/library-manager/utils"
)

// Inspector checks single song folders. It is what Run uses for every song, and what
// other modules (organize) use to decide whether a song works and how good it is. It
// never modifies anything. It is safe for concurrent use.
type Inspector struct {
	ops     *fsops.Ops
	media   bool // ffprobe is available
	deep    bool // deep requested and ffmpeg (and ffprobe) are available
	deepSem chan struct{}
}

// Inspection is the outcome of inspecting one song folder.
type Inspection struct {
	// Txt is the file name (not the path) of the song txt inside the folder; "" if the
	// folder has none (then Findings is a single no-song-txt error).
	Txt string
	// Findings are sorted (severity, line, code, file, message), as in check_report.
	Findings []Finding
	// Broken: at least one finding is an error.
	Broken bool
	// EncodingProblem: the txt could not be decoded unambiguously, so its errors may only
	// come from the guessed codepage. Such songs must not be moved (see -move-broken).
	EncodingProblem bool
	// Song is the parsed txt; nil if there is no txt or it could not be decoded at all.
	Song *songtxt.Song
	// Media summarizes the referenced media for quality comparisons.
	Media MediaSummary
}

// MediaSummary is what the inspection found out about the media files of a song, as the
// player resolves the file tags (the last occurrence whose file exists).
type MediaSummary struct {
	// Probed: ffprobe ran successfully on the audio source (#AUDIO, else #MP3). False if
	// ffprobe is missing, the source is missing, empty or unreadable.
	Probed bool
	// AudioBitrate of the audio source in bit/s: the bit_rate of its first audio stream,
	// else the container bit_rate if the file has no (moving) video stream; 0 if unknown.
	AudioBitrate int64
	// HasVideo: #VIDEO references an existing, non-empty file that ffprobe read and that
	// has a video stream that is not an attached picture (cover art), and, with -deep, it
	// decoded without error. Without ffprobe it only means that #VIDEO references an
	// existing, non-empty file (the size is then unknown: 0).
	HasVideo bool
	// VideoWidth and VideoHeight of that video stream (0 if !HasVideo).
	VideoWidth, VideoHeight int
	// VideoBitrate of that video stream in bit/s; 0 if unknown or !HasVideo.
	VideoBitrate int64
	// HasCover / HasBackground: #COVER / #BACKGROUND references an existing, non-empty,
	// readable image (formats the header reader does not support count as readable).
	HasCover, HasBackground bool
}

// NewInspector returns an inspector. deep requests a full decode of every referenced
// audio and video file (-deep). The notes describe the checks that cannot run (no
// ffprobe/ffmpeg on PATH) or run with a tool older than the tested minimum
// (mediatools.MinMajor.MinMinor); they are also logged as warnings.
//
// The tools are looked up with mediatools.Lookup, as the web UI does. An old version is
// still used: check only probes and decodes, which old releases do the same way, while
// skipping the media checks would let broken media pass. A tool that is found but fails
// to run is used too: its failures are counted (see errToolFailed), as before.
func NewInspector(ops *fsops.Ops, deep bool) (*Inspector, []string) {
	i := &Inspector{ops: ops, deepSem: make(chan struct{}, maxDeepDecodes)}
	var notes []string
	oldTool := func(t mediatools.Tool, what string) {
		if t.TooOld() {
			note := fmt.Sprintf("%s %s is older than %d.%d, the oldest tested release: %s may differ", t.Name, t.Version,
				mediatools.MinMajor, mediatools.MinMinor, what)
			log.Warnf("%s", note)
			notes = append(notes, note)
		}
	}
	probe := mediatools.Lookup(context.Background(), "ffprobe")
	if !probe.Found {
		log.Warnf("ffprobe not found on PATH: media files are not checked")
		notes = append(notes, noteNoFFprobe)
	} else {
		i.media = true
		oldTool(probe, "the media checks")
	}
	if deep {
		ffmpeg := mediatools.Lookup(context.Background(), "ffmpeg")
		if !ffmpeg.Found {
			log.Warnf("ffmpeg not found on PATH: the deep decode check is skipped")
			notes = append(notes, noteNoFFmpeg)
		} else {
			i.deep = i.media // without ffprobe nothing is known to be a readable media file
			if i.deep {
				oldTool(ffmpeg, "the deep decode checks")
			}
		}
	}
	return i, notes
}

// MediaChecked reports whether media files are probed (ffprobe is available).
func (i *Inspector) MediaChecked() bool { return i.media }

// DeepChecked reports whether media files are fully decoded (deep requested, ffmpeg and
// ffprobe available).
func (i *Inspector) DeepChecked() bool { return i.deep }

// Inspect checks one song folder. The song txt is found and read through the Ops, so in
// a dry run it is checked as an earlier module would have written it (and a duplicate
// song txt fixtxt would move away is not used).
//
// A folder without song txt is no error: its Inspection has a single no-song-txt error
// finding. An error means the folder or its txt cannot be read; it has already been
// logged and counted once (ops.FailOnce with the key "unreadable:<path>", the key the
// folder discovery and the other modules use), so callers must not count it again.
// Unreadable media files and failing ffprobe runs are counted the same way and do not
// make Inspect fail.
func (i *Inspector) Inspect(folder string) (Inspection, error) {
	txt, err := utils.FindSongTxt(i.ops, folder)
	if errors.Is(err, fs.ErrNotExist) {
		return Inspection{
			Findings: []Finding{{Severity: SevError, Code: codeNoSongTxt, Message: "the folder contains no song txt"}},
			Broken:   true,
		}, nil
	}
	if err != nil {
		return Inspection{}, i.skipUnreadable(folder, err)
	}
	raw, err := i.ops.ReadFile(txt)
	if err != nil {
		return Inspection{}, i.skipUnreadable(folder, err)
	}

	d := utils.DecodeTxt(raw)
	in := songInput{EncodingProblem: d.Problem, Damaged: d.Damaged}
	if fixTxtWouldChange(folder, txt, d) {
		in.FolderName = filepath.Base(folder)
	}
	if d.Text != "" {
		in.Song, in.Issues = songtxt.Parse(d.Text)
		in.Files = i.observeFiles(folder, refCandidates(in.Song), rawHeaderValues(raw, d.Encoding))
	}
	findings := analyze(in)
	res := Inspection{
		Txt:             filepath.Base(txt),
		Findings:        findings,
		Broken:          isBroken(findings),
		EncodingProblem: d.Problem != "",
		Song:            in.Song,
	}
	if in.Song != nil {
		res.Media = summarizeMedia(fileRefs(in.Song, in.Files), in.Files)
	}
	return res, nil
}

// skipUnreadable logs an unreadable path and counts it once per run (same key as the
// folder discovery and the other modules). It returns the error for Inspect.
func (i *Inspector) skipUnreadable(folder string, err error) error {
	log.Errorf("Skipping %s: %v", folder, err)
	path := folder
	var pathErr *fs.PathError
	if errors.As(err, &pathErr) {
		path = pathErr.Path
	}
	i.ops.FailOnce("unreadable:" + path)
	return fmt.Errorf("inspecting %s: %w", folder, err)
}

// summarizeMedia derives the MediaSummary from the references a player uses and their
// observations. It is pure.
func summarizeMedia(refs []ref, files map[string]fileObs) MediaSummary {
	var m MediaSummary
	if _, src, ok := sourceObs(refs, files); ok && usableFile(src) && src.Probed && src.ProbeErr == "" {
		m.Probed = true
		m.AudioBitrate = src.Media.AudioBitrate
	}
	for _, r := range refs {
		o := files[r.Tag]
		switch r.Tag {
		case "VIDEO":
			switch {
			case usableFile(o) && o.Probed && o.ProbeErr == "" && o.DecodeErr == "" && o.Media.VideoHeight > 0:
				m.HasVideo = true
				m.VideoWidth, m.VideoHeight = o.Media.VideoWidth, o.Media.VideoHeight
				m.VideoBitrate = o.Media.VideoBitrate
			case usableFile(o) && !o.Probed:
				// Not probed (no ffprobe): an existing, non-empty video file is all that is
				// known, which still beats having no video at all. The size stays unknown.
				m.HasVideo = true
			}
		case "COVER":
			m.HasCover = usableFile(o) && o.ImageErr == ""
		case "BACKGROUND":
			m.HasBackground = usableFile(o) && o.ImageErr == ""
		}
	}
	return m
}

// usableFile reports whether the observed file exists, is not empty and can be opened.
func usableFile(o fileObs) bool {
	return o.Found && o.StatErr == "" && !o.Unreadable && o.Size > 0
}

// mediaOnlyTags are the file tags whose findings do not count as song warnings: a
// missing or broken video, cover, background or stem does not affect singing the song.
var mediaOnlyTags = map[string]bool{"VIDEO": true, "COVER": true, "BACKGROUND": true, "VOCALS": true, "INSTRUMENTAL": true}

// mediaOnlyCodes are finding codes that are only ever about such files (#VIDEOGAP only
// shifts the video).
var mediaOnlyCodes = map[string]bool{codeNoVideoStream: true, codeUnreadableImage: true, codeVideoTooShort: true, codeBadVideoGap: true}

// SongWarnings counts the warnings about the txt, the note timing or the audio source.
// Warnings that concern only the video, cover, background or the #VOCALS/#INSTRUMENTAL
// stems (Finding.Tag, or a code that is only used for them) are not counted. Errors are
// not counted either: a song with errors is broken.
func SongWarnings(findings []Finding) int {
	n := 0
	for _, f := range findings {
		if f.Severity == SevWarning && !mediaOnlyTags[f.Tag] && !mediaOnlyCodes[f.Code] {
			n++
		}
	}
	return n
}
