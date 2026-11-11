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
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"

	log "github.com/sirupsen/logrus"

	"github.com/nkreimeyer/ultrastar-songs-sanitizer/config"
	"github.com/nkreimeyer/ultrastar-songs-sanitizer/fsops"
	"github.com/nkreimeyer/ultrastar-songs-sanitizer/songtxt"
	"github.com/nkreimeyer/ultrastar-songs-sanitizer/utils"
)

// Options configure the check module.
type Options struct {
	// Deep decodes every referenced audio and video file completely with ffmpeg. Slow.
	Deep bool
	// MoveBroken moves songs with at least one error to invalid/.
	MoveBroken bool
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
	ops     *fsops.Ops
	media   bool // ffprobe is available
	deep    bool // Deep requested and ffmpeg is available
	deepSem chan struct{}
	mu      sync.Mutex
	results []songResult // songs with findings
	checked int
}

// Run checks every song folder below cfg.SongDir and writes the reports. It returns an
// error only if the library cannot be listed or a report cannot be written; findings are
// not failures.
func Run(cfg config.Config, ops *fsops.Ops, opts Options) error {
	folders, err := utils.GetAllSongFolders(ops, cfg.SongDir)
	if err != nil {
		return fmt.Errorf("finding song folders in %q: %w", cfg.SongDir, err)
	}

	c := &checker{cfg: cfg, ops: ops, deepSem: make(chan struct{}, maxDeepDecodes)}
	var notes []string
	if _, err := exec.LookPath("ffprobe"); err != nil {
		log.Warnf("ffprobe not found on PATH: media files are not checked")
		notes = append(notes, noteNoFFprobe)
	} else {
		c.media = true
	}
	if opts.Deep {
		if _, err := exec.LookPath("ffmpeg"); err != nil {
			log.Warnf("ffmpeg not found on PATH: the deep decode check is skipped")
			notes = append(notes, noteNoFFmpeg)
		} else {
			c.deep = c.media // without ffprobe nothing is known to be a readable media file
		}
	}

	forEachParallel(folders, workers, c.checkFolder)

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

	rep := buildReport(c.checked, results, notes, c.media, c.deep, cfg.DryRun)
	jsonData, err := rep.json()
	if err != nil {
		return fmt.Errorf("encoding check report: %w", err)
	}
	for name, data := range map[string][]byte{reportJSON: jsonData, reportTxt: rep.text()} {
		if err := ops.WriteReport(cfg.ReportPath(name), data); err != nil {
			return err
		}
	}
	log.Infof("Check: %d song(s) checked, %d broken, %d suspicious; see %s",
		rep.Checked, rep.Broken, rep.Suspicious, cfg.ReportPath(reportTxt))
	return nil
}

// forEachParallel calls fn for every item, using up to n goroutines.
func forEachParallel[T any](items []T, n int, fn func(T)) {
	n = max(1, min(n, len(items)))
	queue := make(chan T)
	var wg sync.WaitGroup
	wg.Add(n)
	for range n {
		go func() {
			defer wg.Done()
			for item := range queue {
				fn(item)
			}
		}()
	}
	for _, item := range items {
		queue <- item
	}
	close(queue)
	wg.Wait()
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

// skipUnreadable logs an unreadable path and counts it once per run (same key as the
// folder discovery and the other modules).
func (c *checker) skipUnreadable(folder string, err error) {
	log.Errorf("Skipping %s: %v", folder, err)
	path := folder
	var pathErr *fs.PathError
	if errors.As(err, &pathErr) {
		path = pathErr.Path
	}
	c.ops.FailOnce("unreadable:" + path)
}

// checkFolder checks one song folder.
func (c *checker) checkFolder(folder string) {
	txt, err := utils.FindSongTxt(folder)
	if errors.Is(err, fs.ErrNotExist) {
		c.record(songResult{folder: folder, findings: []Finding{{Severity: SevError, Code: codeNoSongTxt, Message: "the folder contains no song txt"}}})
		return
	}
	if err != nil {
		c.skipUnreadable(folder, err)
		return
	}
	raw, err := os.ReadFile(txt)
	if err != nil {
		c.skipUnreadable(folder, err)
		return
	}

	d := utils.DecodeTxt(raw)
	in := songInput{EncodingProblem: d.Problem, Damaged: d.Damaged}
	if d.Text != "" {
		in.Song, in.Issues = songtxt.Parse(d.Text)
		in.Files = c.observeFiles(folder, refCandidates(in.Song), rawHeaderValues(raw, d.Encoding))
	}
	c.record(songResult{folder: folder, txt: filepath.Base(txt), findings: analyze(in), encodingProblem: d.Problem != ""})
}

// observeFiles gathers the file system and media observations for the file tags. Of a
// repeated tag, the last occurrence whose file exists is observed, as a player does (see
// fileRefs). rawValues are the undecoded tag values of a txt in a legacy codepage, by
// line: a CP1252 txt references a file named in CP1252 bytes, which the decoded value
// does not match.
func (c *checker) observeFiles(folder string, cands []refCandidate, rawValues map[int]string) map[string]fileObs {
	obs := map[string]fileObs{}
	byName := map[string]fileObs{} // one probe per file, even if several tags reference it
	for _, cand := range cands {
		var name, value string
		found := false
		for i := len(cand.Occ) - 1; i >= 0 && !found; i-- {
			t := cand.Occ[i]
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
			o = c.observeFile(filepath.Join(folder, name), cand.Kind)
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

func (c *checker) observeFile(path string, kind refKind) fileObs {
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
		c.ops.FailOnce("unreadable:" + path)
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
	if !c.media {
		return o
	}
	info, err := probeMedia(path)
	switch {
	case errors.Is(err, errToolFailed):
		log.Errorf("Checking %s: %v", path, err)
		c.ops.Fail()
		return o
	case err != nil:
		o.Probed, o.ProbeErr = true, err.Error()
		return o
	}
	o.Probed, o.Media = true, info
	if c.deep {
		c.deepSem <- struct{}{}
		decErr, decWarn, err := decodeCheck(path)
		<-c.deepSem
		if err != nil {
			log.Errorf("Checking %s: %v", path, err)
			c.ops.Fail()
			return o
		}
		o.DecodeErr, o.DecodeWarn = decErr, decWarn
	}
	return o
}

// rawHeaderValues returns the undecoded values of the tag lines of a txt in a
// single-byte codepage, keyed by their 1-based line (the same lines songtxt.Parse
// reports: \r\n, \r and \n all end a line). It is nil for UTF-8 and UTF-16 txts.
func rawHeaderValues(raw []byte, encoding string) map[int]string {
	if encoding == utils.EncUTF8 || encoding == utils.EncUTF16LE || encoding == utils.EncUTF16BE {
		return nil
	}
	raw = bytes.TrimPrefix(raw, []byte("\xef\xbb\xbf"))
	raw = bytes.ReplaceAll(raw, []byte("\r\n"), []byte("\n"))
	raw = bytes.ReplaceAll(raw, []byte("\r"), []byte("\n"))
	values := map[int]string{}
	for i, line := range bytes.Split(raw, []byte("\n")) {
		line = bytes.TrimSpace(line)
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
