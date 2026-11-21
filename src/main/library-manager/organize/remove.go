package organize

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"path/filepath"
	"sort"
	"strings"

	log "github.com/sirupsen/logrus"

	"github.com/nkreimeyer/library-manager/config"
	"github.com/nkreimeyer/library-manager/fsops"
	"github.com/nkreimeyer/library-manager/songtxt"
	"github.com/nkreimeyer/library-manager/utils"
)

// ModeRemove (remove) moves the songs that match patterns out of the library.
const ModeRemove Mode = "remove"

// Report file names of the remove subcommand.
const (
	removeReportJSON = "remove_plan.json"
	removeReportTxt  = "remove_plan.txt"
)

// Pattern kinds.
const (
	PatternArtist = "artist"
	PatternSong   = "song"
)

// Pattern selects songs to remove. Patterns are compared with the tags like the
// duplicate detection compares songs (SongKey: case, accents, spaces, punctuation, a
// leading "The", feat. parts and noise qualifiers do not matter).
//
//   - An artist pattern matches every song whose artist names (#ARTIST, the names of a
//     collaboration "A & B", "A x B", ... each) include all of the pattern's names:
//     "Elton John" matches "Elton John" and "Elton John & George Michael", but not
//     "George Michael feat. Elton John" (featured artists are not compared).
//   - A song pattern "Artist - Title" matches that song: the same artist names and title,
//     the solo and the duet version, but not other versions ("Title (Live)") unless the
//     pattern names them. A pattern with a duet qualifier ("Title [DUET]") matches only
//     the duet.
type Pattern struct {
	Kind string // PatternArtist or PatternSong
	Text string // as given
	// Source says where the pattern comes from ("-artist", "file:3"), for messages.
	Source string

	names []string // artist: the artist names
	key   string   // song: SongKey without the duet flag (with it if the pattern is a duet)
	duet  bool     // song: the pattern names the duet
}

// String is "<kind>: <text>".
func (p Pattern) String() string { return p.Kind + ": " + p.Text }

// ParsePattern parses an artist or song pattern. A song pattern is "Artist - Title",
// split at the first " - ".
func ParsePattern(kind, text, source string) (Pattern, error) {
	p := Pattern{Kind: kind, Text: strings.TrimSpace(text), Source: source}
	switch kind {
	case PatternArtist:
		w := artistWords(keyInput(p.Text))
		if len(w) == 0 {
			return p, fmt.Errorf("%s: the artist %q has no letters or digits", source, p.Text)
		}
		p.names = artistNames(w)
	case PatternSong:
		artist, title, ok := strings.Cut(p.Text, " - ")
		if !ok {
			return p, fmt.Errorf("%s: the song %q must be written as \"Artist - Title\"", source, p.Text)
		}
		a, t, duet := keyParts(artist, title, false)
		if a == "" {
			return p, fmt.Errorf("%s: the song %q needs an artist and a title with letters or digits", source, p.Text)
		}
		p.key, p.duet = a+keySep+t, duet
		if duet {
			p.key += keySep + "duet" // as SongKey writes it
		}
	default:
		return p, fmt.Errorf("%s: unknown pattern kind %q", source, kind)
	}
	return p, nil
}

// ParsePatternFile reads patterns, one per line: "artist: <name>", "song: <Artist -
// Title>", or a line without prefix, which is a song if it contains " - " and an artist
// otherwise. Blank lines and lines starting with '#' are ignored. name is used in error
// messages ("<name>:<line>").
func ParsePatternFile(r io.Reader, name string) ([]Pattern, error) {
	var out []Pattern
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 64*1024), 1024*1024)
	for n := 1; sc.Scan(); n++ {
		line := strings.TrimSpace(strings.TrimPrefix(sc.Text(), "\ufeff"))
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		kind := PatternArtist
		if strings.Contains(line, " - ") {
			kind = PatternSong
		}
		if k, rest, ok := strings.Cut(line, ":"); ok {
			switch strings.ToLower(strings.TrimSpace(k)) {
			case PatternArtist:
				kind, line = PatternArtist, rest
			case PatternSong:
				kind, line = PatternSong, rest
			}
		}
		p, err := ParsePattern(kind, line, fmt.Sprintf("%s:%d", name, n))
		if err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	if err := sc.Err(); err != nil {
		return nil, fmt.Errorf("reading %s: %w", name, err)
	}
	return out, nil
}

// matches reports whether the pattern selects a song with these tags (duet: the txt has
// duet markers).
func (p Pattern) matches(artist, title string, duet bool) bool {
	switch p.Kind {
	case PatternArtist:
		have := map[string]bool{}
		for _, n := range artistNames(artistWords(keyInput(artist))) {
			have[n] = true
		}
		for _, n := range p.names {
			if !have[n] {
				return false
			}
		}
		return true
	case PatternSong:
		// A duet pattern's key ends with the duet marker, so only the first test can match.
		k := SongKey(artist, title, duet)
		return k == p.key || k == p.key+keySep+"duet"
	}
	return false
}

// removeSong is one song folder of the library as the remove subcommand sees it.
type removeSong struct {
	Folder, Rel   string
	Artist, Title string
	Duet          bool
	// EncodingProblem: the txt cannot be decoded unambiguously, so its tags may be wrong.
	EncodingProblem bool
	// NoTags: the folder has no song txt or the txt cannot be read; it is never matched.
	NoTags bool
	// FromFolder: #ARTIST or #TITLE is missing and was taken from the folder name
	// (utils.TagsFromFolderName, as fixtxt fills it).
	FromFolder bool
}

// removeDecision is the plan for one song: Pattern is the index of the first matching
// pattern (-1: none); Left: it matches but stays (Reason says why).
type removeDecision struct {
	Pattern int
	Left    bool
	Reason  string
}

const reasonRemoveEncoding = "the txt has an encoding problem, so its tags may be read wrongly; fix the txt first (fixtxt) and run remove again"

// planRemove decides for every song whether it is removed. It is pure. counts holds the
// number of songs every pattern matched (first match only).
func planRemove(songs []removeSong, patterns []Pattern) (ds []removeDecision, counts []int) {
	ds = make([]removeDecision, len(songs))
	counts = make([]int, len(patterns))
	for i, s := range songs {
		ds[i].Pattern = -1
		if s.NoTags {
			continue
		}
		for k, p := range patterns {
			if p.matches(s.Artist, s.Title, s.Duet) {
				ds[i].Pattern = k
				counts[k]++
				break
			}
		}
		if ds[i].Pattern >= 0 && s.EncodingProblem {
			ds[i].Left, ds[i].Reason = true, reasonRemoveEncoding
		}
	}
	return ds, counts
}

// readRemoveSong reads the tags of one song folder. A missing #ARTIST or #TITLE is taken
// from an obvious "Artist - Title" folder name, so a library that was not sanitized yet
// can be cleaned too.
func readRemoveSong(ops *fsops.Ops, lib, folder string) removeSong {
	rel, _ := filepath.Rel(lib, folder)
	s := removeSong{Folder: folder, Rel: filepath.ToSlash(rel), NoTags: true}
	txt, err := utils.FindSongTxt(ops, folder)
	if errors.Is(err, fs.ErrNotExist) {
		return s
	}
	var raw []byte
	if err == nil {
		raw, err = ops.ReadFile(txt)
	}
	if err != nil {
		log.Errorf("Skipping %s: %v", folder, err)
		path := folder
		var pathErr *fs.PathError
		if errors.As(err, &pathErr) {
			path = pathErr.Path
		}
		ops.FailOnce("unreadable:" + path)
		return s
	}
	d := utils.DecodeTxt(raw)
	song, _ := songtxt.Parse(d.Text)
	s.NoTags = false
	s.EncodingProblem = d.Problem != ""
	s.Artist, _ = StripCopySuffix(tagValue(song, "ARTIST"), false)
	s.Title, _ = StripCopySuffix(tagValue(song, "TITLE"), false)
	if f := utils.TagsFromFolderName(filepath.Base(folder), s.Artist, s.Title); f.FillArtist || f.FillTitle {
		if f.FillArtist {
			s.Artist, _ = StripCopySuffix(f.Artist, false)
		}
		if f.FillTitle {
			s.Title, _ = StripCopySuffix(f.Title, false)
		}
		s.FromFolder = true
	}
	s.Duet = len(song.Players) > 0
	return s
}

// Remove moves every song of the library that matches one of the patterns to
// <out>/removed/<its path in the library>. Nothing is deleted. The moves are journaled
// (real runs only), so Undo reverts them, and remove_plan.json/.txt list what was removed,
// what matched but stayed, and the patterns that matched nothing. It returns an error
// only if the library cannot be listed, or the journal or a report cannot be written;
// failed moves are counted in ops.
func Remove(cfg config.OrganizeConfig, ops *fsops.Ops, patterns []Pattern) error {
	if cfg.Library == "" || len(patterns) == 0 {
		return errors.New("remove needs a library and at least one pattern")
	}
	folders, err := utils.GetAllSongFolders(ops, cfg.Library)
	if err != nil {
		return fmt.Errorf("finding song folders in %q: %w", cfg.Library, err)
	}
	songs := make([]removeSong, len(folders))
	forEachParallel(len(folders), workers, func(i int) { songs[i] = readRemoveSong(ops, cfg.Library, folders[i]) })
	sort.Slice(songs, func(a, b int) bool { return songs[a].Rel < songs[b].Rel })
	ds, counts := planRemove(songs, patterns)

	// The applier journals the moves; it only needs the folder, the path and a label.
	cands := make([]Candidate, len(songs))
	for i, s := range songs {
		cands[i] = Candidate{Folder: s.Folder, Rel: s.Rel, Label: "library", InLibrary: true}
	}
	outcomes := map[int]moveOutcome{}
	in := removeInput{cfg: cfg, patterns: patterns, songs: songs, ds: ds, counts: counts, outcomes: outcomes}
	if !cfg.DryRun {
		for i, d := range ds {
			if d.Pattern >= 0 && !d.Left {
				outcomes[i] = moveOutcome{Status: outcomePending}
			}
		}
		if err := writeRemoveReports(cfg, ops, buildRemoveReport(in)); err != nil {
			return err
		}
	}
	a := newApplier(cfg, ops, cands, outcomes)
	for i, d := range ds {
		if d.Pattern < 0 || d.Left || !a.ready(i) {
			continue
		}
		dst, err := ops.MoveInto(songs[i].Folder, cfg.RemovedDir(), filepath.FromSlash(songs[i].Rel))
		if err != nil {
			log.Errorf("Removing %s: %v", songs[i].Rel, err) // counted by ops
			outcomes[i] = moveOutcome{Status: outcomeFailed}
			continue
		}
		log.Infof("Removed %s (%s)", songs[i].Rel, patterns[d.Pattern])
		a.done(i, dst)
		a.journalMove(songs[i].Folder, dst)
	}
	rep := buildRemoveReport(in)
	if err := writeRemoveReports(cfg, ops, rep); err != nil {
		return err
	}
	verb := "moved"
	if cfg.DryRun {
		verb = "planned"
	}
	log.Infof("Remove: %d of %d song(s) removed, %d matching song(s) left in place, %d pattern(s) without a match; %d move(s) %s, %d failed; see %s",
		rep.Summary.Removed, rep.Summary.Songs, rep.Summary.LeftInPlace, len(rep.Unmatched), rep.Summary.Moved, verb, rep.Summary.Failed, cfg.ReportPath(removeReportTxt))
	if a.j.failed {
		return fmt.Errorf("the journal %s cannot be written; the remaining moves were not attempted", cfg.JournalPath())
	}
	return nil
}

type removeInput struct {
	cfg      config.OrganizeConfig
	patterns []Pattern
	songs    []removeSong
	ds       []removeDecision
	counts   []int
	outcomes map[int]moveOutcome
}

// removeReport is the content of remove_plan.json. It is deterministic.
type removeReport struct {
	Mode     Mode                `json:"mode"`
	DryRun   bool                `json:"dryRun"`
	Library  string              `json:"library"`
	Patterns []patternReport     `json:"patterns"`
	Summary  removeSummaryReport `json:"summary"`
	Removed  []removedEntry      `json:"removed"`
	Left     []leftEntry         `json:"leftInPlace"`
	// Unmatched are the patterns that matched no song ("<kind>: <text>").
	Unmatched []string `json:"unmatched"`
}

type patternReport struct {
	Pattern string `json:"pattern"` // "<kind>: <text>"
	Source  string `json:"source"`
	Songs   int    `json:"songs"` // songs it matched first
}

type removeSummaryReport struct {
	Songs       int `json:"songs"`
	Removed     int `json:"removed"`
	LeftInPlace int `json:"leftInPlace"`
	Moved       int `json:"moved"`
	Failed      int `json:"failed"`
}

type removedEntry struct {
	Song    string `json:"song"` // path in the library, with "/"
	Name    string `json:"name"` // "Artist - Title"
	Pattern string `json:"pattern"`
	Dest    string `json:"dest"` // relative to the output dir, with "/"
	Outcome string `json:"outcome"`
}

func buildRemoveReport(in removeInput) removeReport {
	r := removeReport{Mode: ModeRemove, DryRun: in.cfg.DryRun, Library: in.cfg.Library,
		Removed: []removedEntry{}, Left: []leftEntry{}, Unmatched: []string{}}
	for k, p := range in.patterns {
		r.Patterns = append(r.Patterns, patternReport{Pattern: p.String(), Source: p.Source, Songs: in.counts[k]})
		if in.counts[k] == 0 {
			r.Unmatched = append(r.Unmatched, p.String())
		}
	}
	r.Summary.Songs = len(in.songs)
	for i, d := range in.ds {
		if d.Pattern < 0 {
			continue
		}
		s := &in.songs[i]
		name := s.Artist + " - " + s.Title // a matched song has an artist
		if d.Left {
			r.Left = append(r.Left, leftEntry{Song: s.Rel, Name: name, Reason: d.Reason})
			continue
		}
		o := in.outcomes[i]
		dest := "removed/" + s.Rel
		if o.Dst != "" {
			if rel, err := filepath.Rel(in.cfg.OutDir, o.Dst); err == nil {
				dest = filepath.ToSlash(rel)
			}
		}
		pattern := in.patterns[d.Pattern].String()
		if s.FromFolder {
			pattern += " (tags from the folder name)"
		}
		r.Removed = append(r.Removed, removedEntry{Song: s.Rel, Name: name, Pattern: pattern, Dest: dest, Outcome: o.Status})
		switch o.Status {
		case outcomeMoved, outcomePlanned:
			r.Summary.Moved++
		case outcomeFailed:
			r.Summary.Failed++
		}
	}
	r.Summary.Removed, r.Summary.LeftInPlace = len(r.Removed), len(r.Left)
	return r
}

func (r removeReport) json() ([]byte, error) {
	data, err := json.MarshalIndent(r, "", "  ")
	if err != nil {
		return nil, err
	}
	return append(data, '\n'), nil
}

func (r removeReport) text() []byte {
	var b bytes.Buffer
	run := "real run"
	if r.DryRun {
		run = "dry run: nothing was moved"
	}
	fmt.Fprintf(&b, "remove plan (%s)\nlibrary: %s\npatterns:\n", run, r.Library)
	for _, p := range r.Patterns {
		fmt.Fprintf(&b, "  %s  (%s; %d songs)\n", p.Pattern, p.Source, p.Songs)
	}
	moved := "moved"
	if r.DryRun {
		moved = "planned moves"
	}
	s := r.Summary
	fmt.Fprintf(&b, "\nsummary: %d songs: %d removed, %d matching songs left in place; %d patterns without a match; %d %s, %d failed\n",
		s.Songs, s.Removed, s.LeftInPlace, len(r.Unmatched), s.Moved, moved, s.Failed)
	fmt.Fprintf(&b, "\n== Removed (moved to removed/) (%d) ==\n", len(r.Removed))
	for _, e := range r.Removed {
		fmt.Fprintf(&b, "%s  (%s)%s\n    matched: %s\n", e.Song, e.Name, withOutcome(e.Outcome), e.Pattern)
	}
	fmt.Fprintf(&b, "\n== Left in place (%d) ==\n", len(r.Left))
	for _, e := range r.Left {
		fmt.Fprintf(&b, "%s  (%s)\n    reason: %s\n", e.Song, e.Name, e.Reason)
	}
	fmt.Fprintf(&b, "\n== Patterns without a match (%d) ==\n", len(r.Unmatched))
	for _, u := range r.Unmatched {
		fmt.Fprintf(&b, "%s\n", u)
	}
	return b.Bytes()
}

func writeRemoveReports(cfg config.OrganizeConfig, ops *fsops.Ops, rep removeReport) error {
	data, err := rep.json()
	if err != nil {
		return fmt.Errorf("encoding remove report: %w", err)
	}
	if err := ops.WriteReport(cfg.ReportPath(removeReportJSON), data); err != nil {
		return err
	}
	return ops.WriteReport(cfg.ReportPath(removeReportTxt), rep.text())
}
