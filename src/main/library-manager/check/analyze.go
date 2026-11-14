package check

import (
	"fmt"
	"math"
	"path/filepath"
	"slices"
	"sort"
	"strings"

	"github.com/nkreimeyer/library-manager/songtxt"
	"github.com/nkreimeyer/library-manager/utils"
)

// Severity of a finding. A song with at least one error is broken, a song with only
// warnings is suspicious.
type Severity string

const (
	SevError   Severity = "error"
	SevWarning Severity = "warning"
)

// Finding is one problem found in a song.
type Finding struct {
	Severity Severity `json:"severity"`
	Code     string   `json:"code"`
	Message  string   `json:"message"`
	File     string   `json:"file,omitempty"` // file name inside the song folder
	Line     int      `json:"line,omitempty"` // 1-based line of the song txt
	// Tag is the upper-case file tag (e.g. "VIDEO") a file finding is about; "" for
	// findings about the txt or the timing. Not part of the report.
	Tag string `json:"-"`
}

// Thresholds of the timing checks.
const (
	// pastAudioToleranceMs: notes may end up to this much after the end of the audio
	// before it counts as an error (or after #END before it is a warning). Encoders pad or
	// trim a few frames, and the last note is often held into the fade-out.
	pastAudioToleranceMs = 1000
	// earlyEndRatio and earlyEndMinGapMs: notes that end before earlyEndRatio of the song
	// AND more than earlyEndMinGapMs before its end point to truncated lyrics or a wrong
	// #BPM. Both conditions are needed: long instrumental outros are common, but rarely
	// half of a song, and short songs must not be flagged for a short outro.
	earlyEndRatio    = 0.5
	earlyEndMinGapMs = 30000
	// videoShortToleranceMs: a separate video may end this much before the song before it
	// is flagged. Players show the background image (or a black screen) after the video.
	videoShortToleranceMs = 10000
	// fewNotes: songs with fewer notes are suspicious (placeholders, truncated txts).
	fewNotes = 10
)

// Codes of the findings.
const (
	codeNoSongTxt       = "no-song-txt"
	codeEncoding        = "encoding"
	codeDamagedText     = "damaged-text"
	codeMissingTag      = "missing-tag"
	codeBadBPM          = "bad-bpm"
	codeBadGap          = "bad-gap"
	codeBadVideoGap     = "bad-videogap"
	codeBadStart        = "bad-start"
	codeBadEnd          = "bad-end"
	codeNoNotes         = "no-notes"
	codeFewNotes        = "few-notes"
	codeNoEnd           = "no-end"
	codeDuplicateTag    = "duplicate-tag"
	codeDuetStructure   = "duet-structure"
	codeNotMonotonic    = "beats-not-monotonic"
	codeOverlapping     = "overlapping-notes"
	codeZeroLength      = "zero-length-note"
	codeBreakOrder      = "break-order"
	codeMissingFile     = "missing-file"
	codeEmptyFile       = "empty-file"
	codeUnreadableMedia = "unreadable-media"
	codeNoAudioStream   = "no-audio-stream"
	codeNoVideoStream   = "no-video-stream"
	codeUnreadableImage = "unreadable-image"
	codeDecodeError     = "decode-error"
	codeDecodeWarning   = "decode-warning"
	codeNotesPastAudio  = "notes-past-audio"
	codeNotesPastEnd    = "notes-past-end"
	codeNoteBeforeAudio = "note-before-audio"
	codeNotesEndEarly   = "notes-end-early"
	codeVideoTooShort   = "video-too-short"
)

// issueSeverity maps parser issue codes to severities. Unknown codes are warnings.
var issueSeverity = map[string]Severity{
	songtxt.IssueMalformedNote:  SevError,
	songtxt.IssueMalformedBreak: SevError,
	songtxt.IssueMalformedBPM:   SevError, // a lost BPM change shifts every later note
	songtxt.IssueBadPlayer:      SevError,
	songtxt.IssueUnknownLine:    SevError,
	songtxt.IssueMalformedTag:   SevWarning,
	songtxt.IssueTagAfterNotes:  SevWarning,
}

// refKind is what a file tag references.
type refKind int

const (
	kindAudio refKind = iota
	kindVideo
	kindImage
)

// ref is a file reference of the song txt.
type ref struct {
	Tag    string // upper-case tag key, e.g. "MP3"
	Value  string // the file name as written in the txt
	Line   int
	Kind   refKind
	Source bool // the audio source of the song (#AUDIO if present, else #MP3)
}

// refTags are the tags that reference files, in report order.
var refTags = []struct {
	tag  string
	kind refKind
}{
	{"AUDIO", kindAudio}, {"MP3", kindAudio}, {"VOCALS", kindAudio}, {"INSTRUMENTAL", kindAudio},
	{"VIDEO", kindVideo}, {"COVER", kindImage}, {"BACKGROUND", kindImage},
}

// isRefTag reports whether the upper-case key is one of refTags.
func isRefTag(key string) bool {
	for _, rt := range refTags {
		if rt.tag == key {
			return true
		}
	}
	return false
}

// refCandidate is a file tag with all its non-empty occurrences, in file order.
type refCandidate struct {
	Tag  string // upper-case key
	Kind refKind
	Occ  []songtxt.Tag
}

// refCandidates returns the file tags of the song that have a non-empty value, in
// refTags order.
func refCandidates(s *songtxt.Song) []refCandidate {
	var cands []refCandidate
	for _, rt := range refTags {
		c := refCandidate{Tag: rt.tag, Kind: rt.kind}
		for _, t := range s.Tags {
			if strings.EqualFold(t.Key, rt.tag) && t.Value != "" {
				c.Occ = append(c.Occ, t)
			}
		}
		if len(c.Occ) > 0 {
			cands = append(cands, c)
		}
	}
	return cands
}

// fileRefs returns the file references of the song as UltraStar Deluxe resolves them. A
// player assigns every header line as it reads it, so of a repeated tag the last
// occurrence wins; for file tags it is the last one whose file exists. observeFiles makes
// that choice and records it in fileObs.Value; without it (no observation, or no
// occurrence found on disk) the last occurrence is used.
func fileRefs(s *songtxt.Song, files map[string]fileObs) []ref {
	cands := refCandidates(s)
	source := "MP3"
	for _, c := range cands {
		if c.Tag == "AUDIO" {
			source = "AUDIO" // format >= 1.1: #AUDIO replaces #MP3
		}
	}
	refs := make([]ref, 0, len(cands))
	for _, c := range cands {
		t := c.Occ[len(c.Occ)-1]
		if v := files[c.Tag].Value; v != "" {
			for i := len(c.Occ) - 1; i >= 0; i-- {
				if c.Occ[i].Value == v {
					t = c.Occ[i]
					break
				}
			}
		}
		refs = append(refs, ref{Tag: c.Tag, Value: t.Value, Line: t.Line, Kind: c.Kind, Source: c.Tag == source})
	}
	return refs
}

// playerView returns the song with every repeated tag reduced to the occurrence a player
// uses: the chosen reference for file tags (see fileRefs), the last occurrence for all
// others. s itself is returned if no tag is repeated.
func playerView(s *songtxt.Song, refs []ref) *songtxt.Song {
	if len(s.Duplicates()) == 0 {
		return s
	}
	keep := map[string]int{} // upper-case key -> line of the occurrence to keep
	for _, t := range s.Tags {
		keep[strings.ToUpper(t.Key)] = t.Line
	}
	for _, r := range refs {
		keep[r.Tag] = r.Line
	}
	v := *s
	v.Tags = nil
	for _, t := range s.Tags {
		if keep[strings.ToUpper(t.Key)] == t.Line {
			v.Tags = append(v.Tags, t)
		}
	}
	return &v
}

// mediaInfo is what ffprobe reports about a media file.
type mediaInfo struct {
	DurationSec float64 // valid if HasDuration
	HasDuration bool
	// DurationEstimated: ffprobe only estimated the duration from the bitrate (a VBR mp3
	// without Xing/VBRI header) and measuring it failed, so it may be far off.
	DurationEstimated bool
	HasAudio          bool
	HasVideo          bool // a video stream that is not an attached picture (cover art)
	// AudioBitrate in bit/s: of the first audio stream, else of the container if the
	// file has no moving video stream (the container rate would include the video); 0 if
	// unknown.
	AudioBitrate int64
	// VideoWidth and VideoHeight of the first video stream that is not an attached
	// picture; 0 if there is none.
	VideoWidth, VideoHeight int
	// VideoBitrate in bit/s of that stream: its bit_rate, else its BPS tag (Matroska),
	// else the container bit_rate if the file has no audio stream; 0 if unknown.
	VideoBitrate int64
}

// fileObs are the observations about one referenced file, gathered by the checker.
type fileObs struct {
	// Value is the tag value these observations belong to, when observeFiles chose one of
	// several occurrences of the tag (see fileRefs). "" means the last occurrence.
	Value   string
	Name    string // on-disk name; "" if not found
	Found   bool
	StatErr string // the file exists in the listing but cannot be stat'ed
	// Unreadable: the file exists but cannot be opened (permission denied). That is an
	// operation failure of the run (counted by the checker), not a problem of the song, so
	// no finding is reported for the file.
	Unreadable bool
	Size       int64

	Probed   bool // ffprobe ran; Media or ProbeErr is set
	ProbeErr string
	Media    mediaInfo

	ImageErr string // GetImageDimensions failed on a supported image format

	DecodeErr  string // -deep: ffmpeg exited with an error
	DecodeWarn string // -deep: ffmpeg succeeded but printed errors
}

// songInput is everything the analysis needs about one song.
type songInput struct {
	Song            *songtxt.Song // nil if the txt could not be decoded at all
	Issues          []songtxt.Issue
	EncodingProblem string
	Damaged         bool
	Files           map[string]fileObs // keyed by ref.Tag
	// FolderName is the base name of the song folder if fixtxt would fill a missing
	// #ARTIST/#TITLE from it (utils.TagsFromFolderName); "" if fixtxt leaves the txt alone.
	FolderName string
}

// analyze returns the findings of one song. It is pure: all file system and ffmpeg
// observations are in in.Files.
func analyze(in songInput) []Finding {
	var fs findings
	if in.EncodingProblem != "" {
		fs.add(SevWarning, codeEncoding, 0, "", "the txt could not be decoded unambiguously (%s); it is checked as read", in.EncodingProblem)
	}
	if in.Damaged {
		fs.add(SevWarning, codeDamagedText, 0, "", "the txt contains U+FFFD replacement characters (text damaged by an earlier conversion)")
	}
	s := in.Song
	if s == nil {
		return fs.sorted()
	}
	for _, is := range in.Issues {
		sev, ok := issueSeverity[is.Code]
		if !ok {
			sev = SevWarning
		}
		fs.add(sev, is.Code, is.Line, "", "%s", is.Msg)
	}
	refs := fileRefs(s, in.Files)
	view := playerView(s, refs) // repeated tags as the player resolves them
	checkDuplicates(&fs, s)
	checkTags(&fs, view, in)
	checkNotes(&fs, view)
	checkNoteTimes(&fs, view)
	checkFiles(&fs, refs, in.Files)
	checkTiming(&fs, view, refs, in.Files)
	return fs.sorted()
}

// tagLine returns the line of the first tag with key (0 if absent).
func tagLine(s *songtxt.Song, key string) int {
	for _, t := range s.Tags {
		if strings.EqualFold(t.Key, key) {
			return t.Line
		}
	}
	return 0
}

func hasValue(s *songtxt.Song, key string) bool {
	v, ok := s.Tag(key)
	return ok && strings.TrimSpace(v) != ""
}

func checkTags(fs *findings, s *songtxt.Song, in songInput) {
	checkNameTags(fs, s, in)
	if !hasValue(s, "MP3") && !hasValue(s, "AUDIO") {
		fs.add(SevError, codeMissingTag, 0, "", "neither #MP3 nor #AUDIO is set: the song has no audio")
	}
	if _, err := s.BPM(); err != nil {
		fs.add(SevError, codeBadBPM, tagLine(s, "BPM"), "", "%v", err)
	}
	if _, err := s.GapMs(); err != nil {
		fs.add(SevError, codeBadGap, tagLine(s, "GAP"), "", "%v", err)
	}
	if _, err := s.VideoGapSec(); err != nil {
		fs.add(SevWarning, codeBadVideoGap, tagLine(s, "VIDEOGAP"), "", "%v", err)
	}
	start, _, startErr := s.StartSec()
	if startErr != nil {
		fs.add(SevWarning, codeBadStart, tagLine(s, "START"), "", "%v", startErr)
	}
	end, hasEnd, endErr := s.EndMs()
	if endErr != nil {
		fs.add(SevWarning, codeBadEnd, tagLine(s, "END"), "", "%v", endErr)
	}
	// UltraStar Deluxe only applies #END when it is > 0: "#END:0" means "no end".
	if hasEnd && endErr == nil && startErr == nil && end > 0 && end <= start*1000 {
		fs.add(SevError, codeBadEnd, tagLine(s, "END"), "", "#END (%s) is not after #START (%s): nothing would be played",
			fmtMs(end), fmtMs(start*1000))
	}
}

// checkNameTags reports a missing or empty #TITLE or #ARTIST: UltraStar Deluxe refuses such
// songs. If fixtxt fills the tag from the folder name, the message says so. This only
// happens when fixtxt did not run before check: otherwise the tag is already filled (in a
// dry run, check reads the txt fixtxt would write).
func checkNameTags(fs *findings, s *songtxt.Song, in songInput) {
	if hasValue(s, "TITLE") && hasValue(s, "ARTIST") {
		return
	}
	var fill utils.NameFill
	if in.FolderName != "" {
		artist, _ := s.Tag("ARTIST")
		title, _ := s.Tag("TITLE")
		fill = utils.TagsFromFolderName(in.FolderName, artist, title)
	}
	for _, t := range []struct {
		key   string
		ok    bool
		value string
	}{{"TITLE", fill.FillTitle, fill.Title}, {"ARTIST", fill.FillArtist, fill.Artist}} {
		switch {
		case hasValue(s, t.key):
		case !t.ok:
			fs.add(SevError, codeMissingTag, tagLine(s, t.key), "", "#%s is missing or empty", t.key)
		default:
			fs.add(SevError, codeMissingTag, tagLine(s, t.key), "", "#%s is missing or empty; fixtxt fills it from the folder name: %s", t.key, t.value)
		}
	}
}

// checkDuplicates reports repeated tags of the song as written. The other checks use the
// occurrence a player uses (see playerView).
func checkDuplicates(fs *findings, s *songtxt.Song) {
	for _, key := range s.Duplicates() {
		used := "the last one"
		if isRefTag(key) {
			used = "the last one whose file exists"
		}
		tag := "" // a repeated file tag is about that file (Finding.Tag), others about the txt
		if isRefTag(key) {
			tag = key
		}
		fs.addFile(tag, SevWarning, codeDuplicateTag, dupLine(s, key), "", "#%s occurs more than once; players use %s", key, used)
	}
}

// dupLine returns the line of the second occurrence of key.
func dupLine(s *songtxt.Song, key string) int {
	seen := false
	for _, t := range s.Tags {
		if strings.EqualFold(t.Key, key) {
			if seen {
				return t.Line
			}
			seen = true
		}
	}
	return 0
}

// occurrences collects the lines of one kind of note problem, so that only the first
// occurrence and a count are reported. Lines are deduplicated because a P3 note belongs
// to both duet tracks.
type occurrences struct {
	lines map[int]bool
	first int
	msg   string // message of the first occurrence
}

func (o *occurrences) add(line int, format string, args ...any) {
	if o.lines == nil {
		o.lines = map[int]bool{}
	}
	msg := fmt.Sprintf(format, args...)
	if o.lines[line] {
		// The same line again (a P3 note checked for both singers): keep the smaller
		// message, so the report does not depend on the order of the checks.
		if line == o.first && msg < o.msg {
			o.msg = msg
		}
		return
	}
	o.lines[line] = true
	if o.first == 0 || line < o.first {
		o.first = line
		o.msg = msg
	}
}

func (o *occurrences) report(fs *findings, sev Severity, code string) {
	if len(o.lines) == 0 {
		return
	}
	msg := o.msg
	if n := len(o.lines); n > 1 {
		msg += fmt.Sprintf(" (%d occurrences)", n)
	}
	fs.add(sev, code, o.first, "", "%s", msg)
}

func checkNotes(fs *findings, s *songtxt.Song) {
	switch n := len(s.Notes); {
	case n == 0:
		fs.add(SevError, codeNoNotes, 0, "", "the txt has no notes")
	case n < fewNotes:
		fs.add(SevWarning, codeFewNotes, s.Notes[0].Line, "", "only %d note(s)", n)
	}
	if !s.HasEnd {
		fs.add(SevWarning, codeNoEnd, 0, "", "no \"E\" end-of-song line")
	}

	duet := len(s.Players) > 0
	if duet {
		checkDuet(fs, s)
	}

	var notMono, overlap, zero, breakOrder occurrences
	for _, n := range s.Notes {
		if n.Length <= 0 {
			zero.add(n.Line, "note at beat %d has length %d", n.Start, n.Length)
		}
	}

	// Order: in file order, per player marker (P1, P2, P3; 0 before the first marker or in
	// a song without markers). A section continues the previous section of the same
	// marker, so a duet may alternate P1 and P2 sections; a P3 section (both singers)
	// after complete P1 and P2 sections, the usual layout, starts again from its own first
	// note.
	type event struct {
		note *songtxt.Note
		brk  *songtxt.LineBreak
		line int
	}
	events := make([]event, 0, len(s.Notes)+len(s.Breaks))
	for i := range s.Notes {
		events = append(events, event{note: &s.Notes[i], line: s.Notes[i].Line})
	}
	for i := range s.Breaks {
		events = append(events, event{brk: &s.Breaks[i], line: s.Breaks[i].Line})
	}
	sort.SliceStable(events, func(i, j int) bool { return events[i].line < events[j].line })
	prev := map[int]*songtxt.Note{} // last note per player marker
	for _, ev := range events {
		if ev.brk != nil {
			if p := prev[ev.brk.Player]; p != nil && ev.brk.Beat < p.Start {
				breakOrder.add(ev.line, "line break at beat %d lies before the previous note (beat %d)", ev.brk.Beat, p.Start)
			}
			continue
		}
		n := ev.note
		if p := prev[n.Player]; p != nil && n.Start < p.Start {
			notMono.add(n.Line, "note starts at beat %d, before the previous note (beat %d, line %d)", n.Start, p.Start, p.Line)
		}
		prev[n.Player] = n
	}

	// Overlaps: per singer, in time order (P3 notes belong to both duet singers; notes
	// before the first marker belong to none and are reported by checkDuet).
	singers := map[int][]int{0: {0}} // singer -> player markers whose notes they sing
	if duet {
		singers = map[int][]int{1: {1, 3}, 2: {2, 3}}
	}
	for _, singer := range sortedKeys(singers) {
		var track []*songtxt.Note
		for i := range s.Notes {
			if slices.Contains(singers[singer], s.Notes[i].Player) {
				track = append(track, &s.Notes[i])
			}
		}
		sort.SliceStable(track, func(i, j int) bool {
			if track[i].Start != track[j].Start {
				return track[i].Start < track[j].Start
			}
			return track[i].Line < track[j].Line
		})
		var longest *songtxt.Note // the note that ends last so far
		for _, n := range track {
			if longest != nil && n.Start < longest.Start+longest.Length {
				overlap.add(n.Line, "note at beat %d starts before the note at line %d ends (beat %d)", n.Start, longest.Line, longest.Start+longest.Length)
			}
			if longest == nil || n.Start+n.Length > longest.Start+longest.Length {
				longest = n
			}
		}
	}
	notMono.report(fs, SevError, codeNotMonotonic)
	overlap.report(fs, SevWarning, codeOverlapping)
	zero.report(fs, SevWarning, codeZeroLength)
	breakOrder.report(fs, SevWarning, codeBreakOrder)
}

// checkNoteTimes flags notes before the start of the audio. Beats may be negative (a
// note at beat -2 plays 2 beats before #GAP), and so may #GAP, but no note may start
// before song time 0.
func checkNoteTimes(fs *findings, s *songtxt.Song) {
	first, ok := s.FirstNoteStartBeat()
	if !ok {
		return
	}
	// A negative #GAP moves even a note at beat 0 before the audio: decide on song time.
	ms, err := s.BeatToMs(float64(first))
	if err != nil || ms >= 0 {
		return
	}
	line := 0
	for _, n := range s.Notes {
		if n.Start == first {
			line = n.Line
			break
		}
	}
	fs.add(SevWarning, codeNoteBeforeAudio, line, "", "the first note starts at %s, before the audio begins (beat %d; #GAP too small or negative)", fmtMs(ms), first)
}

func sortedKeys[V any](m map[int]V) []int {
	keys := make([]int, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Ints(keys)
	return keys
}

// checkDuet checks the player markers of a duet.
func checkDuet(fs *findings, s *songtxt.Song) {
	var before occurrences
	for _, n := range s.Notes {
		if n.Player == 0 {
			before.add(n.Line, "note before the first player marker (P1/P2): it belongs to no singer")
		}
	}
	before.report(fs, SevError, codeDuetStructure)

	var empty occurrences
	for i, line := range s.PlayerLines {
		next := math.MaxInt
		if i+1 < len(s.PlayerLines) {
			next = s.PlayerLines[i+1]
		}
		hasNote := false
		for _, n := range s.Notes {
			if n.Line > line && n.Line < next {
				hasNote = true
				break
			}
		}
		if !hasNote {
			empty.add(line, "player marker P%d is followed by no notes", s.Players[i])
		}
	}
	empty.report(fs, SevWarning, codeDuetStructure)
}

// checkFiles checks that the referenced files exist and can be read.
func checkFiles(fs *findings, refs []ref, files map[string]fileObs) {
	for _, r := range refs {
		sev := SevWarning
		if r.Source {
			sev = SevError
		}
		obs := files[r.Tag]
		if !obs.Found {
			fs.addFile(r.Tag, sev, codeMissingFile, r.Line, r.Value, "#%s file %q not found", r.Tag, r.Value)
			continue
		}
		if obs.Unreadable {
			continue // counted as a failure of the run; nothing is known about the file
		}
		if obs.StatErr != "" {
			fs.addFile(r.Tag, sev, codeMissingFile, r.Line, obs.Name, "#%s file %q cannot be read: %s", r.Tag, obs.Name, obs.StatErr)
			continue
		}
		if obs.Size == 0 {
			fs.addFile(r.Tag, sev, codeEmptyFile, r.Line, obs.Name, "#%s file %q is empty", r.Tag, obs.Name)
			continue
		}
		if r.Kind == kindImage {
			if obs.ImageErr != "" {
				fs.addFile(r.Tag, SevWarning, codeUnreadableImage, r.Line, obs.Name, "#%s image cannot be read: %s", r.Tag, obs.ImageErr)
			}
			continue
		}
		if obs.Probed {
			switch {
			case obs.ProbeErr != "":
				fs.addFile(r.Tag, sev, codeUnreadableMedia, r.Line, obs.Name, "#%s file cannot be read by ffprobe: %s", r.Tag, obs.ProbeErr)
				continue // a decode check would only repeat the error
			case r.Source && !obs.Media.HasAudio:
				fs.addFile(r.Tag, SevError, codeNoAudioStream, r.Line, obs.Name, "#%s file has no audio stream", r.Tag)
			case r.Kind == kindVideo && !obs.Media.HasVideo:
				fs.addFile(r.Tag, SevWarning, codeNoVideoStream, r.Line, obs.Name, "#VIDEO file has no video stream")
			}
		}
		if obs.DecodeErr != "" {
			fs.addFile(r.Tag, sev, codeDecodeError, r.Line, obs.Name, "decoding failed: %s", obs.DecodeErr)
		} else if obs.DecodeWarn != "" {
			fs.addFile(r.Tag, SevWarning, codeDecodeWarning, r.Line, obs.Name, "decoding reported errors: %s", obs.DecodeWarn)
		}
	}
}

// sourceObs returns the audio source reference and its observations.
func sourceObs(refs []ref, files map[string]fileObs) (ref, fileObs, bool) {
	for _, r := range refs {
		if r.Source {
			return r, files[r.Tag], true
		}
	}
	return ref{}, fileObs{}, false
}

// checkTiming compares the note timing with the audio and video lengths.
func checkTiming(fs *findings, s *songtxt.Song, refs []ref, files map[string]fileObs) {
	srcRef, src, ok := sourceObs(refs, files)
	if !ok || !src.Found || !src.Probed || src.ProbeErr != "" || !src.Media.HasAudio || !src.Media.HasDuration {
		return
	}
	audioMs := src.Media.DurationSec * 1000

	start, hasStart, startErr := s.StartSec()
	if hasStart && startErr == nil && start*1000 >= audioMs {
		fs.add(SevWarning, codeBadStart, tagLine(s, "START"), "", "#START (%s) is not before the end of the audio (%s)",
			fmtMs(start*1000), fmtMs(audioMs))
	}

	// The song ends with the audio, or earlier at #END. Notes after #END are legal (it
	// cuts a song short; they are never sung), so only notes past the audio are an error.
	songEndMs, endBy := audioMs, "the end of the audio ("+srcRef.Value+")"
	end, hasEnd, err := s.EndMs()
	cutByEnd := hasEnd && err == nil && end > 0 && end < audioMs
	if cutByEnd {
		songEndMs, endBy = end, "#END"
	}
	estimated := ""
	pastAudioSev := SevError
	if src.Media.DurationEstimated {
		// A VBR mp3 without Xing header: the length may be far off in either direction.
		estimated = "; the audio length is only estimated by ffprobe and may be wrong"
		pastAudioSev = SevWarning
	}

	if lastBeat, ok := s.LastNoteEndBeat(); ok {
		if lastMs, err := s.BeatToMs(float64(lastBeat)); err == nil {
			line := 0
			for _, n := range s.Notes {
				if n.Start+n.Length == lastBeat {
					line = n.Line
					break
				}
			}
			switch {
			case lastMs > audioMs+pastAudioToleranceMs:
				fs.add(pastAudioSev, codeNotesPastAudio, line, "", "the last note ends at %s, after the end of the audio (%s) at %s (wrong #BPM/#GAP or truncated audio%s)",
					fmtMs(lastMs), srcRef.Value, fmtMs(audioMs), estimated)
			case cutByEnd && lastMs > end+pastAudioToleranceMs:
				fs.add(SevWarning, codeNotesPastEnd, line, "", "the last note ends at %s, after #END at %s: the notes after #END are never sung",
					fmtMs(lastMs), fmtMs(end))
			case lastMs < earlyEndRatio*songEndMs && songEndMs-lastMs > earlyEndMinGapMs:
				fs.add(SevWarning, codeNotesEndEarly, line, "", "the last note ends at %s, long before %s at %s (truncated lyrics or wrong #BPM%s)",
					fmtMs(lastMs), endBy, fmtMs(songEndMs), estimated)
			}
		}
	}

	// A separate video plays at song time t as video time t + VIDEOGAP, so it ends at
	// song time videoDuration - VIDEOGAP.
	for _, r := range refs {
		if r.Kind != kindVideo {
			continue
		}
		v := files[r.Tag]
		if !v.Found || v.Name == src.Name || !v.Probed || v.ProbeErr != "" || !v.Media.HasVideo || !v.Media.HasDuration {
			continue
		}
		gap, err := s.VideoGapSec()
		if err != nil {
			continue
		}
		videoEndMs := (v.Media.DurationSec - gap) * 1000
		if videoEndMs < songEndMs-videoShortToleranceMs {
			fs.addFile(r.Tag, SevWarning, codeVideoTooShort, r.Line, v.Name, "the video ends at song time %s, %s before %s at %s",
				fmtMs(videoEndMs), fmtMs(songEndMs-videoEndMs), endBy, fmtMs(songEndMs))
		}
	}
}

// fmtMs formats milliseconds as m:ss.s.
func fmtMs(ms float64) string {
	sign := ""
	if ms < 0 {
		sign, ms = "-", -ms
	}
	tenths := int64(math.Round(ms / 100))
	return fmt.Sprintf("%s%d:%02d.%d", sign, tenths/600, tenths/10%60, tenths%10)
}

// findings collects the findings of one song. File findings are deduplicated: when two
// tags reference the same file (#MP3 and #VIDEO of a video-only song), a problem of that
// file is reported once, with the higher severity.
type findings struct{ list []Finding }

func (f *findings) add(sev Severity, code string, line int, file, format string, args ...any) {
	f.addFile("", sev, code, line, file, format, args...)
}

// addFile adds a finding about the file referenced by tag (see Finding.Tag).
func (f *findings) addFile(tag string, sev Severity, code string, line int, file, format string, args ...any) {
	nf := Finding{Severity: sev, Code: code, Message: fmt.Sprintf(format, args...), File: file, Line: line, Tag: tag}
	if file != "" {
		for i, old := range f.list {
			if old.Code == code && old.File != "" && sameFile(old.File, file) {
				if old.Severity == SevWarning && sev == SevError {
					f.list[i] = nf
				}
				return
			}
		}
	}
	f.list = append(f.list, nf)
}

func sameFile(a, b string) bool { return filepath.Clean(a) == filepath.Clean(b) }

func sevRank(s Severity) int {
	if s == SevError {
		return 0
	}
	return 1
}

// sorted returns the findings ordered by severity, line, code, file and message.
func (f *findings) sorted() []Finding {
	sortFindings(f.list)
	return f.list
}

func sortFindings(list []Finding) {
	sort.SliceStable(list, func(i, j int) bool {
		a, b := list[i], list[j]
		if sevRank(a.Severity) != sevRank(b.Severity) {
			return sevRank(a.Severity) < sevRank(b.Severity)
		}
		if a.Line != b.Line {
			return a.Line < b.Line
		}
		if a.Code != b.Code {
			return a.Code < b.Code
		}
		if a.File != b.File {
			return a.File < b.File
		}
		return a.Message < b.Message
	})
}
