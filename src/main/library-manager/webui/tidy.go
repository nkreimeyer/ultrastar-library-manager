package webui

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"strings"

	log "github.com/sirupsen/logrus"

	"github.com/nkreimeyer/library-manager/cleanup"
	"github.com/nkreimeyer/library-manager/config"
	"github.com/nkreimeyer/library-manager/fixaudio"
	"github.com/nkreimeyer/library-manager/fixtxt"
	"github.com/nkreimeyer/library-manager/fixvideos"
	"github.com/nkreimeyer/library-manager/fsops"
	"github.com/nkreimeyer/library-manager/mediatools"
	"github.com/nkreimeyer/library-manager/organize"
)

// tidyOptions are the choices of the "Tidy up my library" options screen. A tidy-up is a
// dedupe run with the per-library modules around it, all in one job on one fsops.Ops, so
// the check (a dry run) sees the text fixes and moves the dedupe step would see.
type tidyOptions struct {
	UTF8      bool `json:"utf8"`      // fixtxt: song texts to UTF-8
	FileTags  bool `json:"fileTags"`  // fixtxt: the tags naming files (#MP3, #VIDEO, #COVER, …)
	FillTags  bool `json:"fillTags"`  // fixtxt: a missing #ARTIST/#TITLE from the folder name
	Leftovers bool `json:"leftovers"` // fixtxt (extra song txts), cleanup: files no song uses to extra-files/
	Broken    bool `json:"broken"`    // fixtxt, cleanup: folders without a song; dedupe: songs with errors to invalid/
	Dedupe    bool `json:"dedupe"`    // dedupe: duplicates to duplicates/
	Audio     bool `json:"audio"`     // fixaudio
	Video     bool `json:"video"`     // fixvideos
}

// The steps of a job, in the order they run (the CLI's module order, with the song step
// before the media steps: duplicates are not converted, and the dedupe plan compares the
// media as they are). The step names are the progress phases of the modules.
const (
	stepText    = "text"    // fixtxt
	stepCleanup = "cleanup" // cleanup
	stepSongs   = "songs"   // organize, dedupe or add; its phases are organize.PhaseInspect and PhaseMove
	stepVideo   = "video"   // fixvideos
	stepAudio   = "audio"   // fixaudio
)

// steps returns the steps the options choose. The media steps only run for real: their dry
// runs decode every file (slow) and plan nothing a later step depends on.
func (t *tidyOptions) steps(dryRun bool) []string {
	if t == nil {
		return []string{stepSongs}
	}
	var s []string
	if t.UTF8 || t.FileTags || t.FillTags || t.Leftovers || t.Broken {
		s = append(s, stepText)
	}
	if t.Leftovers || t.Broken {
		s = append(s, stepCleanup)
	}
	if t.Dedupe || t.Broken {
		s = append(s, stepSongs)
	}
	if !dryRun && t.Video {
		s = append(s, stepVideo)
	}
	if !dryRun && t.Audio {
		s = append(s, stepAudio)
	}
	return s
}

func (t *tidyOptions) media() bool { return t != nil && (t.Audio || t.Video) }

// check is the error of options that cannot run: a tidy-up is a dedupe, and something
// must be chosen.
func (t *tidyOptions) check(m organize.Mode) error {
	if t == nil {
		return nil
	}
	if m != organize.ModeDedupe {
		return fmt.Errorf("the tidy-up options belong to a dedupe run, not to %s", m)
	}
	if len(t.steps(false)) == 0 {
		return errors.New("choose at least one thing to do")
	}
	return nil
}

// checkTools is the error of media steps without a usable ffmpeg: they would fail only
// after the other steps ran.
func (t *tidyOptions) checkTools(ctx context.Context) error {
	if !t.media() {
		return nil
	}
	tools := mediatools.Detect(ctx)
	if mediatools.AllOK(tools) {
		return nil
	}
	var problems []string
	for _, tl := range tools {
		if !tl.OK {
			problems = append(problems, tl.Problem)
		}
	}
	return fmt.Errorf("evening out the audio and converting videos need ffmpeg and ffprobe 5.1 or newer (%s)", strings.Join(problems, "; "))
}

// organizeOptions adds the tidy-up's rules to the song step's options: broken songs go to
// invalid/ only if chosen (then also those without a working version), and without
// "deduplicate" no song is set aside as a duplicate.
func (t *tidyOptions) organizeOptions(opts organize.Options) organize.Options {
	if t == nil {
		return opts
	}
	opts.Broken = organize.BrokenKeep
	if t.Broken {
		opts.Broken = organize.BrokenSetAside
	}
	opts.KeepDuplicates = !t.Dedupe
	return opts
}

// stepResult is what one step did (planned, in a dry run).
type stepResult struct {
	Step  string      `json:"step"`
	Stats fsops.Stats `json:"stats"`
	Error string      `json:"error,omitempty"`
}

// tidyPlan is what the check of a tidy-up found besides the song plan: the text and
// cleanup steps' planned writes and moves, relative to the library.
type tidyPlan struct {
	TextFiles     []string `json:"textFiles"`     // song texts that are rewritten
	Leftovers     []string `json:"leftovers"`     // files (and extra song texts) set aside in extra-files/
	BrokenFolders []string `json:"brokenFolders"` // folders without a song, set aside in invalid/
}

// plannedChanges lists what the dry run in ops planned so far inside the library.
func plannedChanges(cfg config.Config, ops *fsops.Ops) tidyPlan {
	p := tidyPlan{TextFiles: []string{}, Leftovers: []string{}, BrokenFolders: []string{}}
	rel := func(path string) (string, bool) {
		r, err := filepath.Rel(cfg.SongDir, path)
		if err != nil || r == "." || r == ".." || strings.HasPrefix(r, ".."+string(filepath.Separator)) {
			return "", false
		}
		return r, true
	}
	for _, w := range ops.PlannedWrites() {
		if r, ok := rel(w); ok { // reports go to the output folder
			p.TextFiles = append(p.TextFiles, r)
		}
	}
	for _, m := range ops.PlannedMoves() {
		r, ok := rel(m.Src)
		switch {
		case !ok:
		case config.IsWithin(m.Dst, cfg.ExtraFilesDir()):
			p.Leftovers = append(p.Leftovers, r)
		case config.IsWithin(m.Dst, cfg.InvalidDir()):
			p.BrokenFolders = append(p.BrokenFolders, r)
		}
	}
	return p
}

// stepRun is one job's run of the steps.
type stepRun struct {
	tidy   *tidyOptions
	cfg    config.OrganizeConfig
	ops    *fsops.Ops
	ctx    context.Context
	c      jobControl
	songs  organize.Options // the song step's options (Ctx and Progress are set by run)
	onText func(tidyPlan)   // dry run: called after the text and cleanup steps with what they planned
}

// run runs the steps in order and returns what each did. A stop ends the run with
// context.Canceled. A failing song step ends it with its error (a changed plan is a
// detailedError): the media steps would otherwise convert the duplicates and broken songs
// the review set aside. Any other failing step is logged and the next one runs, and the
// failures are returned together at the end.
func (r *stepRun) run() ([]stepResult, error) {
	dry := r.ops.DryRun
	var modCfg config.Config
	steps := r.tidy.steps(dry)
	if r.tidy != nil {
		var err error
		// The per-library modules write into the same output folder as the song step, so a
		// tidy-up has one folder to look in (and one media journal across runs).
		if modCfg, err = config.New(r.cfg.Library, r.cfg.OutDir, dry); err != nil {
			return nil, err
		}
	}
	progress := func(phase string) func(done, total int) {
		return func(done, total int) { r.c.Progress(phase, done, total) }
	}
	t := r.tidy
	var results []stepResult
	var failed []string
	for _, step := range steps {
		if err := r.ctx.Err(); err != nil {
			return results, err
		}
		before := r.ops.Stats()
		var err error
		switch step {
		case stepText:
			log.Infof("Fixing song texts")
			err = fixtxt.RunWith(modCfg, r.ops, fixtxt.Options{
				Encoding: t.UTF8, FileTags: t.FileTags, FillTags: t.FillTags, DuplicateTxts: t.Leftovers, MoveBroken: t.Broken,
				Ctx: r.ctx, Progress: progress(stepText),
			})
		case stepCleanup:
			log.Infof("Setting aside leftover files and empty folders")
			err = cleanup.RunWith(modCfg, r.ops, cleanup.Options{
				LeftoverFiles: t.Leftovers, BrokenFolders: t.Broken, Ctx: r.ctx, Progress: progress(stepCleanup),
			})
		case stepSongs:
			opts := r.tidy.organizeOptions(r.songs)
			opts.Ctx, opts.Progress = r.ctx, r.c.Progress
			err = organize.Run(r.cfg, r.ops, opts)
		case stepVideo:
			log.Infof("Converting videos")
			err = fixvideos.RunWith(modCfg, r.ops, fixvideos.Options{Ctx: r.ctx, Progress: progress(stepVideo)})
		case stepAudio:
			log.Infof("Evening out the audio volume")
			err = fixaudio.RunWith(modCfg, r.ops, fixaudio.Options{Ctx: r.ctx, Progress: progress(stepAudio)})
		}
		res := stepResult{Step: step, Stats: r.ops.Stats().Sub(before)}
		if (step == stepText || step == stepCleanup) && dry && r.onText != nil && err == nil {
			r.onText(plannedChanges(modCfg, r.ops)) // before the song step plans its own moves
		}
		var de *detailedError
		switch {
		case err == nil:
		case errors.Is(err, context.Canceled):
			return append(results, res), err
		case step == stepSongs:
			res.Error = err.Error()
			if errors.As(err, &de) || r.tidy == nil {
				return append(results, res), err // a plain organize, dedupe or add run is this one step
			}
			return append(results, res), &detailedError{
				msg:     fmt.Sprintf("checking and moving the songs failed, so the later steps did not run: %v", err),
				details: append(failed, fmt.Sprintf("%s: %v", step, err)),
			}
		case dry:
			return append(results, res), fmt.Errorf("%s: %w", step, err)
		default:
			log.Errorf("%s failed: %v", step, err)
			res.Error = err.Error()
			failed = append(failed, fmt.Sprintf("%s: %v", step, err))
		}
		results = append(results, res)
	}
	if len(failed) > 0 {
		return results, &detailedError{msg: fmt.Sprintf("%d step(s) did not finish", len(failed)), details: failed}
	}
	return results, nil
}
