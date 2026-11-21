// Package repairtxt repairs song txts that an earlier conversion damaged: every byte it
// could not decode became U+FFFD ("f�r", "I�m"). Each U+FFFD stands for one lost
// CP1252/CP1250 character, so the repair puts exactly one character in its place.
//
// What a damaged word stood for is decided from evidence, strongest first:
//
//  1. the song itself: the file names in its folder ("Släckers.mp3"), the fetched lyrics
//     (optional, lrclib.net), and its undamaged words;
//  2. the corpus of the library's undamaged song txts in the song's language (#LANGUAGE,
//     else detected from its words);
//  3. word lists given with -wordlist (e.g. a German hunspell dictionary);
//  4. the corpus of all languages.
//
// A level decides only if one candidate clearly wins; otherwise the next level breaks the
// tie between the plausible candidates. Tag values naming files (#MP3, #VIDEO, …) are
// repaired only by matching a file in the folder. A U+FFFD at the edge of a word or
// between two known words is punctuation ("Hallo…", "„Hallo"). Case is restored from the
// word and the corpus (see caseRule). Words that stay ambiguous are left as they are and
// listed in the report with their candidates and lyric line.
//
// Only the U+FFFD bytes are replaced: line endings, BOM and every other byte stay as they
// are. The original of every rewritten txt is copied to <out>/originals/ first.
package repairtxt

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"sync/atomic"

	log "github.com/sirupsen/logrus"

	"github.com/nkreimeyer/library-manager/config"
	"github.com/nkreimeyer/library-manager/fsops"
	"github.com/nkreimeyer/library-manager/songtxt"
	"github.com/nkreimeyer/library-manager/utils"
)

// Report file names.
const (
	ReportTxt  = "fffd_repair.txt"
	ReportJSON = "fffd_repair.json"
)

// Options configure Run.
type Options struct {
	WordLists []WordList
	// Words are files of reviewed spellings (one word per line, as for word lists). They
	// are the first level for every song: a damaged word that exactly one of them fits is
	// repaired that way, whatever the other evidence says.
	Words []string
	// Lyrics is asked for the songs that still have unrepaired words after the offline
	// pass; nil = no lookups.
	Lyrics LyricsSource
	// Strict leaves the weak repairs undone (they are listed with their suggestion).
	Strict bool
	// Workers is the number of folders read in parallel (0 = 16).
	Workers int
	// SkipFiles lists damaged txts (by FileReport.Path) that are planned and reported but
	// never written (FileReport.Excluded).
	SkipFiles map[string]bool
	// Ctx cancels the run (nil = never). It is checked while scanning and planning (then
	// Run returns Ctx.Err() before anything is written) and before each write (then the
	// report lists what was written so far).
	Ctx context.Context
	// Progress, if set, is called with the phase (PhaseScan, PhasePlan, PhaseLyrics,
	// PhaseWrite), the number of items done and their total. It may be called from
	// several goroutines at once.
	Progress func(phase string, done, total int)
}

// Phases reported to Options.Progress.
const (
	PhaseScan   = "scan"
	PhasePlan   = "plan"
	PhaseLyrics = "lyrics"
	PhaseWrite  = "write"
)

func (o Options) progress(phase string, done, total int) {
	if o.Progress != nil {
		o.Progress(phase, done, total)
	}
}

type damagedTxt struct {
	path, rel string
	raw       []byte
	song      *songtxt.Song
	folder    string // name of the song folder
	names     []string
	// own is the oldest copy in <out>/originals/ (nil if none): the song's own evidence
	// comes from it, so words repaired by an earlier run are no evidence.
	own []byte
	// songWords are the reviewed spellings of the -words sections for this song (or nil).
	songWords *langLex
}

// Skipped is a damaged txt the repair does not touch.
type Skipped struct {
	Path   string `json:"path"`
	Reason string `json:"reason"`
}

// FileReport is the outcome for one damaged txt.
type FileReport struct {
	Path         string   `json:"path"`
	Languages    []string `json:"languages,omitempty"`
	LanguageFrom string   `json:"language_from,omitempty"`
	Damaged      int      `json:"damaged"`
	Fixed        int      `json:"fixed"`
	Weak         int      `json:"weak"`
	Left         int      `json:"left"`
	Outside      int      `json:"outside_tags_and_notes,omitempty"`
	LyricsUsed   bool     `json:"lyrics_used,omitempty"`
	Written      bool     `json:"written"`
	Excluded     bool     `json:"excluded,omitempty"` // in Options.SkipFiles: never written
	Fixes        []Fix    `json:"fixes,omitempty"`
	Unresolved   []Left   `json:"unresolved,omitempty"`
}

// Report is the content of fffd_repair.json.
type Report struct {
	DryRun bool `json:"dry_run"`
	Corpus int  `json:"corpus_txts"`
	// RepairedBefore counts the undamaged txts left out of the corpus because an earlier
	// run repaired them.
	RepairedBefore int          `json:"repaired_before,omitempty"`
	Damaged        int          `json:"damaged_chars"`
	Fixed          int          `json:"fixed_chars"`
	Weak           int          `json:"weak_chars"`
	Files          []FileReport `json:"files"`
	Skipped        []Skipped    `json:"skipped,omitempty"`
	// UnmatchedSections are the "[Artist - Title]" sections of the -words files that name
	// no damaged txt (a typo, or a song repaired already).
	UnmatchedSections []string `json:"unmatched_sections,omitempty"`
}

// listNames returns the names of the entries of dir (hidden ones left out): like the
// folder's own name, they may spell the song's words correctly.
func listNames(dir string) []string {
	var names []string
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil
	}
	for _, e := range entries {
		if !utils.IsHidden(e.Name()) {
			names = append(names, e.Name())
		}
	}
	return names
}

// oldestOriginal returns the first copy of the txt rel that an earlier run backed up to
// <out>/originals/ (later copies get " (2)" etc.), or nil if there is none or it is not
// clean UTF-8.
func oldestOriginal(cfg config.Config, rel string) []byte {
	raw, err := os.ReadFile(filepath.Join(cfg.OriginalsDir(), rel))
	if err != nil {
		return nil
	}
	if d := utils.DecodeTxt(raw); d.Problem != "" || d.Encoding != utils.EncUTF8 {
		return nil
	}
	return raw
}

// scan reads every song txt below cfg.SongDir: the undamaged ones go into the corpus, the
// damaged ones are returned. A txt with a copy in <out>/originals/ was repaired by an
// earlier run and stays out of the corpus, so its repairs never count as evidence and a
// second run decides like the first.
func scan(ctx context.Context, cfg config.Config, ops *fsops.Ops, lex *Lexicon, workers int, progress func(string, int, int)) (damaged []damagedTxt, skipped []Skipped, corpus, repairedBefore int, err error) {
	folders, err := utils.GetAllSongFolders(ops, cfg.SongDir)
	if err != nil {
		return nil, nil, 0, 0, err
	}
	var scanned atomic.Int64
	progress(PhaseScan, 0, len(folders))
	var mu sync.Mutex
	jobs := make(chan string)
	var wg sync.WaitGroup
	for range workers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for dir := range jobs {
				if ctx.Err() != nil {
					continue
				}
				progress(PhaseScan, int(scanned.Add(1)), len(folders))
				txts, err := utils.SongTxtFiles(ops, dir)
				if err != nil {
					log.Warnf("%s: %v", dir, err)
					continue
				}
				for _, txt := range txts {
					raw, err := ops.ReadFile(txt)
					if err != nil {
						log.Warnf("reading %s: %v", txt, err)
						continue
					}
					d := utils.DecodeTxt(raw)
					song, _ := songtxt.Parse(d.Text)
					rel := cfg.RelPath(txt)
					skip := ""
					switch {
					case !d.Damaged:
					case d.Problem != "":
						skip = "decoding problem: " + d.Problem
					case d.Encoding != utils.EncUTF8:
						skip = "encoded as " + d.Encoding + " (run fixtxt first)"
					default:
						if st, err := os.Lstat(txt); err == nil && st.Mode()&os.ModeSymlink != 0 {
							skip = "symlink"
						}
					}
					if !d.Damaged {
						if d.Problem != "" {
							continue
						}
						if _, err := os.Stat(filepath.Join(cfg.OriginalsDir(), rel)); err == nil {
							// Repaired by an earlier run: its repairs are no evidence.
							mu.Lock()
							repairedBefore++
							mu.Unlock()
							continue
						}
						var langs []string
						if v, ok := song.Tag("LANGUAGE"); ok {
							langs = LanguageCodes(v)
						}
						for _, rs := range corpusText(segments([]byte(d.Text), song)) {
							lex.addText(langs, rs)
						}
						mu.Lock()
						corpus++
						mu.Unlock()
						continue
					}
					mu.Lock()
					if skip != "" {
						skipped = append(skipped, Skipped{rel, skip})
					} else {
						damaged = append(damaged, damagedTxt{path: txt, rel: rel, raw: raw, song: song, folder: filepath.Base(dir), names: listNames(dir), own: oldestOriginal(cfg, rel)})
					}
					mu.Unlock()
				}
			}
		}()
	}
	for _, f := range folders {
		jobs <- f
	}
	close(jobs)
	wg.Wait()
	if err := ctx.Err(); err != nil {
		return nil, nil, 0, 0, err
	}
	sort.Slice(damaged, func(i, j int) bool { return damaged[i].rel < damaged[j].rel })
	sort.Slice(skipped, func(i, j int) bool { return skipped[i].Path < skipped[j].Path })
	return damaged, skipped, corpus, repairedBefore, nil
}

// planAll plans every damaged txt (in parallel; the planner only reads).
func planAll(ctx context.Context, p *planner, damaged []damagedTxt, refs map[int][]string, only []int, plans []*filePlan, workers int, progress func(string, int, int)) error {
	total := len(damaged)
	if only != nil {
		total = len(only)
	}
	var planned atomic.Int64
	progress(PhasePlan, 0, total)
	jobs := make(chan int)
	var wg sync.WaitGroup
	for range workers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := range jobs {
				if ctx.Err() != nil {
					continue
				}
				d := damaged[i]
				plans[i] = p.plan(songInput{raw: d.raw, song: d.song, folder: d.folder, names: d.names, refs: refs[i], own: d.own, songWords: d.songWords})
				progress(PhasePlan, int(planned.Add(1)), total)
			}
		}()
	}
	if only == nil {
		for i := range damaged {
			jobs <- i
		}
	} else {
		for _, i := range only {
			jobs <- i
		}
	}
	close(jobs)
	wg.Wait()
	return ctx.Err()
}

// Run repairs the damaged song txts below cfg.SongDir and writes the report.
func Run(cfg config.Config, ops *fsops.Ops, opts Options) error {
	workers := opts.Workers
	if workers <= 0 {
		workers = 16
	}
	wl, err := loadWordLists(opts.WordLists)
	if err != nil {
		return err
	}
	rw, err := loadReviewed(opts.Words)
	if err != nil {
		return err
	}
	ctx := opts.Ctx
	if ctx == nil {
		ctx = context.Background()
	}
	lex := NewLexicon()
	damaged, skipped, corpus, repairedBefore, err := scan(ctx, cfg, ops, lex, workers, opts.progress)
	if err != nil {
		return err
	}
	for i := range damaged {
		d := &damaged[i]
		artist, _ := d.song.Tag("ARTIST")
		title, _ := d.song.Tag("TITLE")
		d.songWords = rw.forSong(d.rel, artist, title)
	}
	unmatched := rw.unmatched()
	for _, name := range unmatched {
		log.Warnf("-words: section [%s] matches no damaged txt", name)
	}
	lex.finalize()
	log.Infof("repair: %d damaged txts, corpus of %d undamaged txts (%d words)", len(damaged), corpus, lex.global().total)
	p := &planner{lex: lex, wordlists: wl, reviewed: rw.global, strict: opts.Strict}
	plans := make([]*filePlan, len(damaged))
	if err := planAll(ctx, p, damaged, nil, nil, plans, workers, opts.progress); err != nil {
		return err
	}

	refs := map[int][]string{}
	if opts.Lyrics != nil {
		var redo, ask []int
		for i := range damaged {
			if len(plans[i].left) > 0 {
				ask = append(ask, i)
			}
		}
		opts.progress(PhaseLyrics, 0, len(ask))
		for n, i := range ask {
			if err := ctx.Err(); err != nil {
				return err
			}
			opts.progress(PhaseLyrics, n+1, len(ask))
			d := damaged[i]
			tags := utils.HeaderTags(string(plans[i].apply(d.raw)))
			artist, title := tags["ARTIST"], tags["TITLE"]
			if artist == "" || title == "" {
				continue
			}
			text, err := opts.Lyrics.Lyrics(artist, title)
			if err != nil {
				log.Warnf("lyrics for %s: %v", d.rel, err)
				continue
			}
			if text != "" {
				refs[i] = []string{text}
				redo = append(redo, i)
			}
		}
		log.Infof("repair: lyrics found for %d songs", len(redo))
		if err := planAll(ctx, p, damaged, refs, redo, plans, workers, opts.progress); err != nil {
			return err
		}
	}

	rep := Report{DryRun: cfg.DryRun, Corpus: corpus, RepairedBefore: repairedBefore, Skipped: skipped, UnmatchedSections: unmatched}
	stopped := false
	opts.progress(PhaseWrite, 0, len(damaged))
	for i, d := range damaged {
		fp := plans[i]
		fr := FileReport{
			Path: d.rel, Languages: fp.langs, LanguageFrom: fp.langHow,
			Damaged: fp.total, Fixed: len(fp.repl), Weak: fp.weakCount, Left: fp.total - len(fp.repl),
			Outside: fp.outside, LyricsUsed: len(refs[i]) > 0, Fixes: fp.fixes, Unresolved: fp.left,
		}
		fr.Excluded = opts.SkipFiles[d.rel]
		if !stopped && ctx.Err() != nil {
			stopped = true
			log.Warnf("repair: stopped; no further txts are written")
		}
		if len(fp.repl) > 0 && !fr.Excluded && !stopped {
			fr.Written = writeRepaired(cfg, ops, d, fp.apply(d.raw))
		}
		opts.progress(PhaseWrite, i+1, len(damaged))
		rep.Damaged += fr.Damaged
		rep.Fixed += fr.Fixed
		rep.Weak += fr.Weak
		rep.Files = append(rep.Files, fr)
	}
	log.Infof("repair: %d of %d U+FFFD characters repaired (%d weak), %d left", rep.Fixed, rep.Damaged, rep.Weak, rep.Damaged-rep.Fixed)
	if err := writeReports(cfg, ops, rep); err != nil {
		return err
	}
	if stopped {
		return ctx.Err()
	}
	return nil
}

// writeRepaired backs the original up to <out>/originals/ and writes the repaired txt.
func writeRepaired(cfg config.Config, ops *fsops.Ops, d damagedTxt, data []byte) bool {
	if _, err := ops.CopyInto(d.path, cfg.OriginalsDir(), d.rel); err != nil {
		log.Errorf("backing up %s: %v (not repaired)", d.path, err)
		return false
	}
	changed, err := ops.WriteFileAtomic(d.path, data)
	if err != nil {
		log.Errorf("writing %s: %v", d.path, err)
		return false
	}
	return changed
}

func lineList(lines []int) string {
	var b strings.Builder
	for i, l := range lines {
		if i == 8 {
			fmt.Fprintf(&b, ", … (%d)", len(lines))
			break
		}
		if i > 0 {
			b.WriteString(", ")
		}
		fmt.Fprint(&b, l)
	}
	return b.String()
}

func writeReports(cfg config.Config, ops *fsops.Ops, rep Report) error {
	data, err := json.MarshalIndent(rep, "", "  ")
	if err != nil {
		return err
	}
	if err := ops.WriteReport(cfg.ReportPath(ReportJSON), append(data, '\n')); err != nil {
		return err
	}
	var b strings.Builder
	verb := "repaired"
	if cfg.DryRun {
		verb = "would be repaired (dry run)"
	}
	fmt.Fprintf(&b, "U+FFFD repair of %s\n%d damaged txts, %d U+FFFD characters: %d %s (%d of them weak), %d left.\n",
		cfg.SongDir, len(rep.Files), rep.Damaged, rep.Fixed, verb, rep.Weak, rep.Damaged-rep.Fixed)
	fmt.Fprintf(&b, "Corpus: %d undamaged txts (%d repaired by earlier runs left out). Originals of rewritten txts: %s\n", rep.Corpus, rep.RepairedBefore, cfg.OriginalsDir())
	b.WriteString("Weak = decided without the usual evidence (a single candidate seen once, or a word the song's language never uses); review those.\n\n")
	b.WriteString("== Damaged txts (U+FFFD before -> left)\n")
	for _, f := range rep.Files {
		fmt.Fprintf(&b, "%5d -> %-4d %s\n", f.Damaged, f.Left, f.Path)
	}
	if len(rep.UnmatchedSections) > 0 {
		b.WriteString("\n== -words sections that match no damaged txt\n")
		for _, s := range rep.UnmatchedSections {
			fmt.Fprintf(&b, "[%s]\n", s)
		}
	}
	if len(rep.Skipped) > 0 {
		b.WriteString("\n== Skipped\n")
		for _, s := range rep.Skipped {
			fmt.Fprintf(&b, "%s: %s\n", s.Path, s.Reason)
		}
	}
	for _, f := range rep.Files {
		lang := strings.Join(f.Languages, "+")
		if lang == "" {
			lang = "unknown"
		}
		if f.LanguageFrom != "" {
			lang += " (" + f.LanguageFrom + ")"
		}
		lyr := ""
		if f.LyricsUsed {
			lyr = ", lyrics fetched"
		}
		fmt.Fprintf(&b, "\n== %s\n   language %s%s; %d of %d repaired, %d left", f.Path, lang, lyr, f.Fixed, f.Damaged, f.Left)
		if f.Outside > 0 {
			fmt.Fprintf(&b, " (%d outside tags and notes)", f.Outside)
		}
		b.WriteString("\n")
		for _, x := range f.Fixes {
			kind := "fixed"
			if x.Weak {
				kind = "WEAK "
			}
			fmt.Fprintf(&b, "   %s %s -> %s  [%s] line %s\n", kind, x.Before, x.After, x.Source, lineList(x.Lines))
		}
		for _, l := range f.Unresolved {
			var cs []string
			for _, c := range l.Candidates {
				if c.Count > 0 {
					cs = append(cs, fmt.Sprintf("%s %d", c.Form, c.Count))
				} else {
					cs = append(cs, c.Form)
				}
			}
			cand := ""
			if len(cs) > 0 {
				cand = "  candidates: " + strings.Join(cs, ", ")
			}
			fmt.Fprintf(&b, "   LEFT  line %d: %s%s\n", l.Line, l.Context, cand)
		}
	}
	return ops.WriteReport(cfg.ReportPath(ReportTxt), []byte(b.String()))
}
