package webui

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"unicode"
	"unicode/utf8"

	log "github.com/sirupsen/logrus"
	"golang.org/x/text/encoding/charmap"
	"golang.org/x/text/unicode/norm"

	"github.com/nkreimeyer/library-manager/config"
	"github.com/nkreimeyer/library-manager/fsops"
	"github.com/nkreimeyer/library-manager/repairtxt"
)

// uiWordsName is the -words file in the output folder that holds the spellings chosen in
// the UI, in sections per song folder. Every repair from the UI reads it, so earlier
// choices keep applying.
const uiWordsName = "ui-reviewed-words.txt"

type repairRequest struct {
	SongDir     string   `json:"songDir"`
	Out         string   `json:"out"`
	WordLists   []string `json:"wordLists"` // "[lang=]path"
	Words       []string `json:"words"`
	Lyrics      bool     `json:"lyrics"`
	LyricsCache string   `json:"lyricsCache"`
	Strict      bool     `json:"strict"`
}

func (q repairRequest) config(dry bool) (config.Config, error) {
	if q.SongDir == "" {
		return config.Config{}, errors.New("a song folder is required")
	}
	return config.New(q.SongDir, q.Out, dry)
}

// options builds the repair options like the repair subcommand does, plus the UI's own
// words file if it exists.
func (q repairRequest) options(cfg config.Config, ops *fsops.Ops) (repairtxt.Options, error) {
	opts := repairtxt.Options{Strict: q.Strict}
	for _, w := range q.WordLists {
		wl, err := repairtxt.ParseWordListFlag(w)
		if err != nil {
			return opts, err
		}
		if _, err := os.Stat(wl.Path); err != nil {
			return opts, fmt.Errorf("word list: %w", err)
		}
		opts.WordLists = append(opts.WordLists, wl)
	}
	for _, w := range q.Words {
		if _, err := os.Stat(w); err != nil {
			return opts, fmt.Errorf("words file: %w", err)
		}
		opts.Words = append(opts.Words, w)
	}
	ui := filepath.Join(cfg.OutDir, uiWordsName)
	if _, err := os.Stat(ui); err == nil {
		opts.Words = append(opts.Words, ui)
	}
	if q.Lyrics {
		cache := q.LyricsCache
		if cache == "" {
			cache = filepath.Join(cfg.OutDir, "lyrics-cache")
		}
		// The cache is written in the analysis (a dry run) too.
		cache, err := cfg.OutsideSongDir(cache)
		if err != nil {
			return opts, fmt.Errorf("lyrics cache: %w", err)
		}
		opts.Lyrics = repairtxt.NewLRCLib(cache, ops)
	} else if q.LyricsCache != "" {
		return opts, errors.New("a lyrics cache needs the online lyrics lookup")
	}
	return opts, nil
}

func (s *Server) handleRepairValidate(w http.ResponseWriter, r *http.Request) {
	var q repairRequest
	if !readJSON(w, r, &q) {
		return
	}
	cfg, err := q.config(true)
	if err == nil {
		_, err = q.options(cfg, fsops.New(true))
	}
	if err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"songDir": cfg.SongDir, "outDir": cfg.OutDir})
}

type repairAnalysis struct {
	id     string
	req    repairRequest
	cfg    config.Config
	report repairtxt.Report
}

func readRepairReport(cfg config.Config) (repairtxt.Report, error) {
	var rep repairtxt.Report
	data, err := os.ReadFile(cfg.ReportPath(repairtxt.ReportJSON))
	if err != nil {
		return rep, err
	}
	return rep, json.Unmarshal(data, &rep)
}

type repairSummary struct {
	Files   int `json:"files"`
	Damaged int `json:"damaged"`
	Fixed   int `json:"fixed"`
	Weak    int `json:"weak"`
	Left    int `json:"left"`
	Skipped int `json:"skipped"`
	Written int `json:"written"`
}

func summarizeRepair(rep repairtxt.Report) repairSummary {
	sum := repairSummary{Files: len(rep.Files), Damaged: rep.Damaged, Fixed: rep.Fixed, Weak: rep.Weak, Left: rep.Damaged - rep.Fixed, Skipped: len(rep.Skipped)}
	for _, f := range rep.Files {
		if f.Written {
			sum.Written++
		}
	}
	return sum
}

func (s *Server) handleRepairAnalyse(w http.ResponseWriter, r *http.Request) {
	var q repairRequest
	if !readJSON(w, r, &q) {
		return
	}
	cfg, err := q.config(true)
	if err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	ops := fsops.New(true)
	opts, err := q.options(cfg, ops)
	if err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	a := &repairAnalysis{id: s.newID("r"), req: q, cfg: cfg}
	job, err := s.jobs.start(a.id, "repair-analyse", ops, func(ctx context.Context, c jobControl) (any, error) {
		if err := cfg.PrepareOutDir(); err != nil {
			return nil, err
		}
		opts.Ctx, opts.Progress = ctx, c.Progress
		log.Infof("Checking %s for damaged characters (dry run, nothing is changed); output %s", cfg.SongDir, cfg.OutDir)
		if err := repairtxt.Run(cfg, ops, opts); err != nil {
			return nil, err
		}
		rep, err := readRepairReport(cfg)
		if err != nil {
			return nil, fmt.Errorf("reading the report: %w", err)
		}
		a.report = rep
		s.mu.Lock()
		s.repair[a.id] = a
		s.mu.Unlock()
		return map[string]any{"analysis": a.id, "summary": summarizeRepair(rep)}, nil
	})
	if err != nil {
		writeError(w, http.StatusConflict, err)
		return
	}
	writeJSON(w, http.StatusAccepted, job)
}

func (s *Server) repairAnalysis(id string) (*repairAnalysis, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	a := s.repair[id]
	if a == nil {
		return nil, fmt.Errorf("no such analysis %q; check again", id)
	}
	return a, nil
}

func (s *Server) handleRepairAnalysis(w http.ResponseWriter, r *http.Request) {
	a, err := s.repairAnalysis(r.URL.Query().Get("id"))
	if err != nil {
		writeError(w, http.StatusNotFound, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"id": a.id, "songDir": a.cfg.SongDir, "outDir": a.cfg.OutDir, "lyrics": a.req.Lyrics,
		"summary": summarizeRepair(a.report), "report": a.report,
	})
}

// repairChoice is a spelling the user chose for a damaged word of one txt.
type repairChoice struct {
	Path string `json:"path"` // FileReport.Path
	Word string `json:"word"` // the damaged word, with U+FFFD
	Form string `json:"form"` // the chosen spelling
}

// fits reports whether form can repair word: the same runes, except that each U+FFFD of
// word is one other rune (case is not compared: the repair restores it).
func fits(word, form string) bool {
	w, f := []rune(word), []rune(form)
	if len(w) != len(f) || !strings.ContainsRune(word, utf8.RuneError) {
		return false
	}
	for i := range w {
		switch {
		case w[i] == utf8.RuneError:
			if f[i] == utf8.RuneError || unicode.IsSpace(f[i]) || unicode.IsControl(f[i]) {
				return false
			}
		case unicode.ToLower(w[i]) != unicode.ToLower(f[i]):
			return false
		}
	}
	return true
}

// writeChoices saves the chosen spellings in the UI's words file, one section per song
// folder (the -words section name a damaged txt is matched by). A spelling chosen earlier
// for the same damaged word of that folder is taken out: it fits the word as well, and two
// reviewed spellings for one word decide nothing, so the new choice would be ignored.
func writeChoices(ops *fsops.Ops, path string, choices []repairChoice) error {
	raw, err := os.ReadFile(path)
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	if !utf8.Valid(raw) { // edited by hand: read it as the -words reader does
		if raw, err = charmap.Windows1252.NewDecoder().Bytes(raw); err != nil {
			return err
		}
	}
	bySection := map[string][]repairChoice{}
	for _, c := range choices {
		sec := filepath.Base(filepath.Dir(c.Path))
		bySection[sec] = append(bySection[sec], c)
	}
	superseded := func(sec, form string) bool {
		form = norm.NFC.String(form)
		for _, c := range bySection[sec] {
			if fits(norm.NFC.String(c.Word), form) {
				return true
			}
		}
		return false
	}
	var b bytes.Buffer
	if len(raw) == 0 {
		b.WriteString("# Spellings chosen in the library-manager UI, per song folder ([folder name]).\n")
	}
	sec := "" // the section of the line ("" = words for every song)
	for _, line := range strings.SplitAfter(string(raw), "\n") {
		if line == "" {
			continue
		}
		t := strings.TrimSpace(line)
		switch {
		case t == "" || strings.HasPrefix(t, "#"):
		case strings.HasPrefix(t, "[") && strings.HasSuffix(t, "]"):
			if sec = strings.TrimSpace(t[1 : len(t)-1]); sec == "*" {
				sec = ""
			}
		case sec != "" && len(bySection[sec]) > 0:
			fields := strings.Fields(t)
			kept := slices.DeleteFunc(slices.Clone(fields), func(f string) bool { return superseded(sec, f) })
			if len(kept) == 0 {
				continue
			}
			if len(kept) < len(fields) {
				line = strings.Join(kept, " ")
			}
		}
		b.WriteString(strings.TrimSuffix(line, "\n") + "\n")
	}
	secs := make([]string, 0, len(bySection))
	for sec := range bySection {
		secs = append(secs, sec)
	}
	sort.Strings(secs)
	for _, sec := range secs {
		fmt.Fprintf(&b, "[%s]\n", sec)
		for _, c := range bySection[sec] {
			b.WriteString(c.Form + "\n")
		}
	}
	b.WriteString("[*]\n")
	return ops.WriteReport(path, b.Bytes())
}

// handleRepairApply repairs for real: the choices go into the UI's words file first (so
// they win over every other evidence), the rejected txts are skipped.
func (s *Server) handleRepairApply(w http.ResponseWriter, r *http.Request) {
	var q struct {
		Analysis string         `json:"analysis"`
		Choices  []repairChoice `json:"choices"`
		Skip     []string       `json:"skip"`
	}
	if !readJSON(w, r, &q) {
		return
	}
	a, err := s.repairAnalysis(q.Analysis)
	if err != nil {
		writeError(w, http.StatusNotFound, err)
		return
	}
	known := map[string]bool{}
	for _, f := range a.report.Files {
		known[f.Path] = true
	}
	for _, c := range q.Choices {
		switch {
		case !known[c.Path]:
			writeError(w, http.StatusBadRequest, fmt.Errorf("%s is not a damaged txt of this analysis", c.Path))
			return
		case strings.ContainsAny(c.Form, "\r\n[]#"): // fits refuses blanks and control characters
			writeError(w, http.StatusBadRequest, fmt.Errorf("%q: a spelling is one word without [ ] # or line breaks", c.Form))
			return
		case !fits(c.Word, c.Form):
			writeError(w, http.StatusBadRequest, fmt.Errorf("%q does not fit %q: only the � may change, one character each", c.Form, c.Word))
			return
		}
	}
	skip := map[string]bool{}
	for _, p := range q.Skip {
		if !known[p] {
			writeError(w, http.StatusBadRequest, fmt.Errorf("%s is not a damaged txt of this analysis", p))
			return
		}
		skip[p] = true
	}
	cfg, err := a.req.config(false)
	if err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	ops := fsops.New(false)
	job, err := s.jobs.start(s.newID("w"), "repair-apply", ops, func(ctx context.Context, c jobControl) (any, error) {
		if err := cfg.PrepareOutDir(); err != nil {
			return nil, err
		}
		if len(q.Choices) > 0 {
			if err := writeChoices(ops, filepath.Join(cfg.OutDir, uiWordsName), q.Choices); err != nil {
				return nil, fmt.Errorf("saving the chosen spellings: %w", err)
			}
		}
		opts, err := a.req.options(cfg, ops)
		if err != nil {
			return nil, err
		}
		opts.Ctx, opts.Progress, opts.SkipFiles = ctx, c.Progress, skip
		log.Infof("Repairing %s; originals are kept in %s", cfg.SongDir, cfg.OriginalsDir())
		runErr := repairtxt.Run(cfg, ops, opts)
		rep, err := readRepairReport(cfg)
		res := map[string]any{"outDir": cfg.OutDir, "originals": cfg.OriginalsDir()}
		if err == nil && !rep.DryRun {
			res["summary"] = summarizeRepair(rep)
		}
		return res, runErr
	})
	if err != nil {
		writeError(w, http.StatusConflict, err)
		return
	}
	writeJSON(w, http.StatusAccepted, job)
}
