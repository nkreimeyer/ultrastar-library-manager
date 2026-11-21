// Package check is a read-only module that reports broken and suspicious songs: txt
// syntax and structure (via songtxt), missing or unreadable referenced files, media
// without the expected streams (ffprobe), optionally a full decode check (ffmpeg), and
// note timing compared with the audio and video lengths.
//
// It writes check_report.json and check_report.txt to the output dir. The library is
// never modified, except that with Options.MoveBroken songs with errors are moved to
// invalid/.
package check

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"

	log "github.com/sirupsen/logrus"

	"github.com/nkreimeyer/library-manager/config"
	"github.com/nkreimeyer/library-manager/fsops"
	"github.com/nkreimeyer/library-manager/songtxt"
	"github.com/nkreimeyer/library-manager/utils"
)

// Options configure the check module.
type Options struct {
	// Deep decodes every referenced audio and video file completely with ffmpeg. Slow.
	Deep bool
	// MoveBroken moves songs with at least one error to invalid/.
	MoveBroken bool
	// Ctx stops the run: no further folder is checked, running probes are killed, and Run
	// returns ctx.Err() without writing a report or moving anything. nil = never stopped.
	Ctx context.Context
	// Progress is called after each song folder with the number done and the total; may be nil.
	Progress func(done, total int)
}

const maxWorkers = 16

// workers is the number of song folders checked at the same time. It is a variable so
// tests can force concurrency on machines with a single CPU.
var workers = min(maxWorkers, runtime.NumCPU())

// maxDeepDecodes limits the full decodes (-deep) running at the same time. Each decode
// is single-threaded (decodeCheck), but a 4K video decoder still needs a few hundred MB.
const maxDeepDecodes = 4

// notMovedEncoding is recorded for broken songs MoveBroken leaves in place.
const notMovedEncoding = "the txt has an encoding problem: its errors may only come from the guessed codepage"

// Notes recorded in the report when checks cannot run.
const (
	noteNoFFprobe = "media checks skipped: ffprobe not found"
	noteNoFFmpeg  = "deep decode checks skipped: ffmpeg not found"
)

// songResult is the outcome for one song folder.
type songResult struct {
	folder   string
	rel      string
	txt      string // name of the song txt; "" if there is none
	findings []Finding
	// encodingProblem: the txt could not be decoded unambiguously, so file references
	// may only fail because of the guessed codepage. Such songs are never moved (fixtxt
	// and cleanup leave them alone, too).
	encodingProblem bool
	moved           bool
	notMoved        string // why a broken song was not moved with MoveBroken
}

// checker holds the state of one run. It is safe for concurrent use.
type checker struct {
	cfg     config.Config
	mu      sync.Mutex
	results []songResult // songs with findings
	checked int
}

// Run checks every song folder below cfg.SongDir and writes the reports. It returns an
// error only if the library cannot be listed or a report cannot be written; findings are
// not failures. A panic while checking one folder is logged and counted as a failure (the
// folder is not in the report), and the other folders are still checked.
func Run(cfg config.Config, ops *fsops.Ops, opts Options) error {
	folders, err := utils.GetAllSongFolders(ops, cfg.SongDir)
	if err != nil {
		return fmt.Errorf("finding song folders in %q: %w", cfg.SongDir, err)
	}

	ctx := opts.Ctx
	if ctx == nil {
		ctx = context.Background()
	}
	insp, notes := NewInspector(ctx, ops, opts.Deep)
	c := &checker{cfg: cfg}
	_ = utils.ForEachParallel(ctx, folders, utils.Parallel{
		Workers:  workers,
		Progress: opts.Progress,
		OnPanic: func(i int, _ any) {
			log.Errorf("check: %s not checked: internal error (logged above)", folders[i])
			ops.Fail()
		},
	}, func(_ int, folder string) {
		res, err := insp.Inspect(folder)
		if err != nil {
			return // logged and counted by Inspect
		}
		c.record(songResult{folder: folder, txt: res.Txt, findings: res.Findings, encodingProblem: res.EncodingProblem})
	})

	if err := ctx.Err(); err != nil {
		return err // stopped: a partial report would look complete
	}
	results := c.results
	sortResults(results)
	if opts.MoveBroken {
		for i := range results {
			if !isBroken(results[i].findings) {
				continue
			}
			if results[i].encodingProblem {
				log.Warnf("Not moving broken song %s: %s", results[i].folder, notMovedEncoding)
				results[i].notMoved = notMovedEncoding
				continue
			}
			if _, err := ops.MoveInto(results[i].folder, cfg.InvalidDir(), cfg.RelPath(results[i].folder)); err != nil {
				log.Errorf("Moving broken song %s: %v", results[i].folder, err) // counted by ops
				continue
			}
			results[i].moved = true
		}
	}

	rep := buildReport(c.checked, results, notes, insp.MediaChecked(), insp.DeepChecked(), cfg.DryRun)
	// fsops writes JSON reports without HTML escaping: messages like "0:19.7 > 0:03.0"
	// stay readable.
	if err := ops.WriteJSONReport(cfg.ReportPath(reportJSON), rep); err != nil {
		return err
	}
	if err := ops.WriteReport(cfg.ReportPath(reportTxt), rep.text()); err != nil {
		return err
	}
	log.Infof("Check: %d song(s) checked, %d broken, %d suspicious; see %s",
		rep.Checked, rep.Broken, rep.Suspicious, cfg.ReportPath(reportTxt))
	return nil
}

func (c *checker) record(res songResult) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.checked++
	if len(res.findings) > 0 {
		res.rel = filepath.ToSlash(c.cfg.RelPath(res.folder))
		c.results = append(c.results, res)
	}
}

// fixTxtWouldChange reports whether fixtxt rewrites txt, the song txt of folder, so that
// it fills a missing #ARTIST/#TITLE from the folder name: it never rewrites a txt with a
// decode Problem, a symlinked txt (it only lists it in missing_files.txt), nor any txt of
// a folder with a media file name that is not valid UTF-8.
func fixTxtWouldChange(folder, txt string, d utils.DecodedTxt) bool {
	if d.Problem != "" {
		return false
	}
	if st, err := os.Lstat(txt); err != nil || st.Mode()&os.ModeSymlink != 0 {
		return false
	}
	entries, err := os.ReadDir(folder)
	return err == nil && len(utils.NonUTF8MediaNames(entries)) == 0
}

// observeFiles gathers the file system and media observations for the file tags. Of a
// repeated tag, the last occurrence whose file exists is observed, as a player does (see
// fileRefs). rawValues are the undecoded tag values of a txt in a legacy codepage, by
// line: a CP1252 txt references a file named in CP1252 bytes, which the decoded value
// does not match.
func (i *Inspector) observeFiles(folder string, cands []refCandidate, rawValues map[int]string) map[string]fileObs {
	obs := map[string]fileObs{}
	byName := map[string]fileObs{} // one probe per file, even if several tags reference it
	for _, cand := range cands {
		var name, value string
		found := false
		for k := len(cand.Occ) - 1; k >= 0 && !found; k-- {
			t := cand.Occ[k]
			name, _, found = utils.ResolveFileName(folder, t.Value)
			if !found {
				if rv, ok := rawValues[t.Line]; ok && rv != t.Value {
					name, _, found = utils.ResolveFileName(folder, rv)
				}
			}
			value = t.Value
		}
		if !found {
			obs[cand.Tag] = fileObs{}
			continue
		}
		key := "media:" + name
		if cand.Kind == kindImage {
			key = "image:" + name
		}
		o, ok := byName[key]
		if !ok {
			o = i.observeFile(filepath.Join(folder, name), cand.Kind)
			o.Name, o.Found = name, true
			byName[key] = o
		}
		o.Value = value
		obs[cand.Tag] = o
	}
	return obs
}

// supportedImageExts are the image formats utils.GetImageDimensions can read. Other
// extensions are not checked, whatever the content.
var supportedImageExts = map[string]bool{".jpg": true, ".jpeg": true, ".jfif": true, ".png": true, ".gif": true, ".bmp": true, ".webp": true}

func (i *Inspector) observeFile(path string, kind refKind) fileObs {
	var o fileObs
	st, err := os.Stat(path)
	if err != nil {
		o.StatErr = err.Error()
		return o
	}
	if st.IsDir() {
		o.StatErr = "is a folder"
		return o
	}
	o.Size = st.Size()
	if o.Size == 0 {
		return o
	}
	// stat works on a file without read permission; ffprobe would then report it as
	// unreadable media, and the song would count as broken.
	f, err := os.Open(path)
	if err != nil {
		log.Errorf("Cannot check %s: %v", path, err)
		i.ops.FailUnreadable(path)
		o.Unreadable = true
		return o
	}
	f.Close()
	switch kind {
	case kindImage:
		if supportedImageExts[strings.ToLower(filepath.Ext(path))] {
			if _, _, err := utils.GetImageDimensions(path); err != nil && !errors.Is(err, utils.ErrUnsupportedImage) {
				o.ImageErr = err.Error()
			}
		}
		return o
	}
	if !i.media {
		return o
	}
	info, err := probeMedia(i.ctx, path)
	switch {
	case i.ctx.Err() != nil:
		return o // stopped: the result is thrown away
	case errors.Is(err, errToolFailed):
		log.Errorf("Checking %s: %v", path, err)
		i.ops.Fail()
		return o
	case err != nil:
		o.Probed, o.ProbeErr = true, err.Error()
		return o
	}
	o.Probed, o.Media = true, info
	if i.deep {
		decErr, decWarn, err := i.deepDecode(path)
		if i.ctx.Err() != nil {
			return o
		}
		if err != nil {
			log.Errorf("Checking %s: %v", path, err)
			i.ops.Fail()
			return o
		}
		o.DecodeErr, o.DecodeWarn = decErr, decWarn
	}
	return o
}

// deepDecode runs decodeCheck in one of the maxDeepDecodes slots. The slot is released by
// defer, so a panic the worker pool recovers does not keep it.
func (i *Inspector) deepDecode(path string) (decodeErr, decodeWarn string, err error) {
	select {
	case i.deepSem <- struct{}{}:
	case <-i.ctx.Done():
		return "", "", i.ctx.Err()
	}
	defer func() { <-i.deepSem }()
	return decodeCheck(i.ctx, path)
}

// rawHeaderValues returns the undecoded values of the tag lines of a txt in a
// single-byte codepage, keyed by their 1-based line (the lines songtxt.Parse reports,
// songtxt.LineSpans). It is nil for UTF-8 and UTF-16 txts.
func rawHeaderValues(raw []byte, encoding string) map[int]string {
	if encoding == utils.EncUTF8 || encoding == utils.EncUTF16LE || encoding == utils.EncUTF16BE {
		return nil
	}
	values := map[int]string{}
	for i, sp := range songtxt.LineSpans(raw) {
		line := bytes.TrimSpace(raw[sp[0]:sp[1]])
		if len(line) == 0 || line[0] != '#' {
			continue
		}
		if _, value, ok := bytes.Cut(line[1:], []byte(":")); ok {
			values[i+1] = string(bytes.TrimSpace(value))
		}
	}
	return values
}

// isBroken reports whether the findings contain an error.
func isBroken(findings []Finding) bool {
	for _, f := range findings {
		if f.Severity == SevError {
			return true
		}
	}
	return false
}
