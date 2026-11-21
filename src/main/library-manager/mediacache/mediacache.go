// Package mediacache keeps the results of media scans (ffprobe, ffmpeg measurements) of a
// library between runs, in one hidden gzip-compressed JSON file in the library root
// (FileName). Re-running a module on a large library then only probes the media files that
// changed. Txts are never cached: reading them is cheap and must stay exact.
//
// A file counts as unchanged if its size and modification time (in ns) equal the stored
// ones. A file found at a new path with the same base name, size and mtime (a folder moved
// with preserved mtimes, as organize, mv or rsync -a do) keeps its results: they are
// re-keyed to the new path. A result is only stored if the file did not change while it was
// obtained and its mtime is at least RacyWindow old (like git's racy-clean rule: a file
// changed again within the same timestamp tick would look unchanged). All results are
// dropped when the ffprobe or ffmpeg version changes; each result kind carries a version
// ("probe/1"), so a change in how a result is computed only needs a new kind.
//
// A nil *Cache is valid everywhere and disables caching. The cache file is written in a dry
// run too (fsops.Ops.WriteCacheFile): it is the only file a dry run may change.
package mediacache

import (
	"bytes"
	"compress/gzip"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/nkreimeyer/library-manager/fsops"
	"github.com/nkreimeyer/library-manager/mediatools"
	"github.com/nkreimeyer/library-manager/utils"
	log "github.com/sirupsen/logrus"
	"golang.org/x/text/unicode/norm"
)

// FileName is the name of the cache file in the library root. It is hidden, so discovery
// and the media modules ignore it (utils.IsHidden).
const FileName = ".library-manager-cache.json.gz"

// formatVersion is the version of the file layout.
const formatVersion = 1

// RacyWindow is how old a file's mtime must be (at the time its result is obtained) for the
// result to be stored. FAT has a 2 s mtime resolution.
const RacyWindow = 2 * time.Second

// Stats counts what the cache did in this run.
type Stats struct {
	Hits   int64 `json:"hits"`   // lookups answered from the cache
	Misses int64 `json:"misses"` // lookups that had to compute the result
	Stored int64 `json:"stored"` // results stored
}

// Cache is the scan cache of one library. Its methods are safe for concurrent use.
type Cache struct {
	root  string // absolute
	path  string // root/FileName
	tools map[string]string
	now   func() time.Time
	// listDir lists a folder for prune (os.ReadDir; tests count the calls).
	listDir func(string) ([]os.DirEntry, error)

	mu      sync.Mutex
	dropped bool // the file held results of other tool versions (see Save)
	updated time.Time
	folders map[string]*folder // by folder key (see keyOf)
	index   map[secKey]map[string]bool
	stats   Stats
}

// secKey is the secondary index key: a file with the same base name, size and mtime at
// another path is taken to be the same file, moved or copied with its mtime.
type secKey struct {
	name    string
	size    int64
	mtimeNs int64
}

type folder struct {
	Seen  time.Time             `json:"seen"`
	Files map[string]*fileEntry `json:"files"`
	run   bool                  // seen in this run
}

type fileEntry struct {
	Size    int64                      `json:"size"`
	MtimeNs int64                      `json:"mtimeNs"`
	Results map[string]json.RawMessage `json:"results"`
	run     bool
}

type diskFile struct {
	Format  int                `json:"format"`
	Tools   map[string]string  `json:"tools"`
	Updated time.Time          `json:"updated"`
	Folders map[string]*folder `json:"folders"`
}

// Stamp is the stat of a file taken by Lookup, before its result is computed. Store only
// stores a result if the file still has this stat. The zero Stamp is invalid.
type Stamp struct {
	path    string
	size    int64
	mtimeNs int64
	ok      bool
}

// Valid reports whether the stat succeeded (on a regular file).
func (s Stamp) Valid() bool { return s.ok }

func stampOf(path string) Stamp {
	st, err := os.Stat(path) // follows symlinks, like the probes
	if err != nil || !st.Mode().IsRegular() {
		return Stamp{path: path}
	}
	return Stamp{path: path, size: st.Size(), mtimeNs: st.ModTime().UnixNano(), ok: true}
}

func (s Stamp) same(o Stamp) bool {
	return s.ok && o.ok && s.size == o.size && s.mtimeNs == o.mtimeNs
}

func (e *fileEntry) matches(s Stamp) bool {
	return e.Size == s.size && e.MtimeNs == s.mtimeNs
}

// Open loads the cache of the library root. It never fails: a missing file gives an empty
// cache, and a corrupt, unreadable or unknown file gives an empty cache and a warning (the
// file is replaced by Save). The current ffprobe and ffmpeg versions are looked up; results
// stored with other versions are dropped. If root itself is a song folder, caching is
// disabled (nil): cleanup would move a hidden file out of a song folder. So it is if ctx
// is done (before or while the versions are looked up): a stopped "-version" run would
// look like a tool change and drop every result.
func Open(ctx context.Context, root string) *Cache {
	if root == "" || ctx.Err() != nil {
		return nil
	}
	tools := map[string]string{}
	for _, name := range []string{"ffprobe", "ffmpeg"} {
		tools[name] = mediatools.Lookup(ctx, name).Version
	}
	if ctx.Err() != nil {
		return nil
	}
	return open(root, tools, time.Now)
}

// open is Open with the tool versions and the clock given.
func open(root string, tools map[string]string, now func() time.Time) *Cache {
	abs, err := filepath.Abs(root)
	if err != nil {
		log.Warnf("Media cache disabled: %v", err)
		return nil
	}
	if txts, err := utils.SongTxtFiles(nil, abs); err == nil && len(txts) > 0 {
		log.Debugf("Media cache disabled: %s is a song folder", abs)
		return nil
	}
	c := &Cache{root: abs, path: filepath.Join(abs, FileName), tools: tools, now: now, listDir: os.ReadDir,
		folders: map[string]*folder{}, index: map[secKey]map[string]bool{}}
	c.load()
	return c
}

// load reads the cache file; see Open.
func (c *Cache) load() {
	f, err := os.Open(c.path)
	if errors.Is(err, fs.ErrNotExist) {
		return
	}
	if err != nil {
		log.Warnf("Media cache %s cannot be read, starting empty: %v", c.path, err)
		return
	}
	defer f.Close()
	d, err := decode(f)
	if err != nil {
		log.Warnf("Media cache %s is unusable, starting empty (it is replaced when saved): %v", c.path, err)
		return
	}
	if !maps.Equal(d.Tools, c.tools) {
		c.dropped = len(d.Folders) > 0
		log.Infof("Media cache %s: ffprobe/ffmpeg changed (%v -> %v), every file is probed again", c.path, d.Tools, c.tools)
		return
	}
	c.updated = d.Updated
	n := 0
	for key, fo := range d.Folders {
		if fo == nil || len(fo.Files) == 0 {
			continue
		}
		for name, e := range fo.Files {
			if e == nil {
				delete(fo.Files, name)
				continue
			}
			if e.Results == nil {
				e.Results = map[string]json.RawMessage{}
			}
			c.indexAdd(key, name, e)
			n++
		}
		c.folders[key] = fo
	}
	log.Debugf("Media cache %s: %d files of %d folders, as of %s", c.path, n, len(c.folders), d.Updated.Format(time.RFC3339))
}

func decode(r io.Reader) (diskFile, error) {
	zr, err := gzip.NewReader(r)
	if err != nil {
		return diskFile{}, err
	}
	var d diskFile
	dec := json.NewDecoder(zr)
	if err := dec.Decode(&d); err != nil {
		return diskFile{}, err
	}
	if d.Format != formatVersion {
		return diskFile{}, fmt.Errorf("unknown format %d", d.Format)
	}
	if err := zr.Close(); err != nil {
		return diskFile{}, err
	}
	return d, nil
}

// keyOf returns the folder key and the base name of the absolute file path: the folder
// relative to the root, slash-separated ("." for the root), or its absolute path if it is
// outside the root.
func (c *Cache) keyOf(path string) (string, string) {
	dir, name := filepath.Split(path)
	dir = filepath.Clean(dir)
	rel, err := filepath.Rel(c.root, dir)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return filepath.ToSlash(dir), name
	}
	return filepath.ToSlash(rel), name
}

// dirOf is the inverse of keyOf for the folder part.
func (c *Cache) dirOf(key string) string {
	dir := filepath.FromSlash(key)
	if filepath.IsAbs(dir) {
		return dir
	}
	return filepath.Join(c.root, dir)
}

func absPath(path string) string {
	if abs, err := filepath.Abs(path); err == nil {
		return abs
	}
	return filepath.Clean(path)
}

func (c *Cache) indexAdd(key, name string, e *fileEntry) {
	k := secKey{name, e.Size, e.MtimeNs}
	if c.index[k] == nil {
		c.index[k] = map[string]bool{}
	}
	c.index[k][key] = true
}

func (c *Cache) indexRemove(key, name string, e *fileEntry) {
	k := secKey{name, e.Size, e.MtimeNs}
	delete(c.index[k], key)
	if len(c.index[k]) == 0 {
		delete(c.index, k)
	}
}

// entry returns the entry of the file, nil if there is none. c.mu must be held.
func (c *Cache) entry(key, name string) *fileEntry {
	if fo := c.folders[key]; fo != nil {
		return fo.Files[name]
	}
	return nil
}

// put sets the entry of the file, replacing (and unindexing) an older one, and marks it
// seen. c.mu must be held.
func (c *Cache) put(key, name string, e *fileEntry) {
	fo := c.folders[key]
	if fo == nil {
		fo = &folder{Files: map[string]*fileEntry{}}
		c.folders[key] = fo
	}
	if old := fo.Files[name]; old != nil {
		c.indexRemove(key, name, old)
	}
	fo.Files[name] = e
	c.indexAdd(key, name, e)
	c.markSeen(fo, e)
}

// remove drops the entry of the file (and its folder if that is left empty). c.mu must be
// held.
func (c *Cache) remove(key, name string) {
	fo := c.folders[key]
	if fo == nil {
		return
	}
	if e := fo.Files[name]; e != nil {
		c.indexRemove(key, name, e)
		delete(fo.Files, name)
	}
	if len(fo.Files) == 0 {
		delete(c.folders, key)
	}
}

func (c *Cache) markSeen(fo *folder, e *fileEntry) {
	fo.run, e.run = true, true
	fo.Seen = c.now().UTC().Truncate(time.Second)
}

// Lookup stats path (following symlinks) and reports whether a result of the kind is
// stored for a file with exactly this stat; if so, it is decoded into v. The returned
// Stamp is the stat taken now: pass it to Store with the computed result. A file not
// known at path is looked up by its base name, size and mtime (a moved or copied folder);
// a match is re-keyed to path (copied if the old file still exists, else moved).
func (c *Cache) Lookup(path, kind string, v any) (Stamp, bool) {
	if c == nil {
		return Stamp{}, false
	}
	st := stampOf(absPath(path))
	if !st.ok {
		c.count(false)
		return st, false
	}
	key, name := c.keyOf(st.path)
	c.mu.Lock()
	if e := c.entry(key, name); e != nil && e.matches(st) {
		c.markSeen(c.folders[key], e)
		raw, ok := e.Results[kind]
		c.mu.Unlock()
		return st, c.decode(raw, ok, v)
	}
	var cands []string
	for k := range c.index[secKey{name, st.size, st.mtimeNs}] {
		if k != key {
			cands = append(cands, k)
		}
	}
	c.mu.Unlock()
	slices.Sort(cands)
	for _, cand := range cands {
		// A copy leaves the old file in place: it keeps its entry. A folder renamed only in
		// case is a move, though the old spelling still resolves on a case-insensitive file
		// system (to the same file).
		oldPath := filepath.Join(c.dirOf(cand), name)
		stillThere := stampOf(oldPath).same(st) && !c.sameFileAlias(oldPath, st.path)
		c.mu.Lock()
		ce := c.entry(cand, name)
		if ce == nil || !ce.matches(st) {
			c.mu.Unlock()
			continue
		}
		ne := &fileEntry{Size: ce.Size, MtimeNs: ce.MtimeNs, Results: maps.Clone(ce.Results)}
		if !stillThere {
			c.remove(cand, name)
		}
		c.put(key, name, ne)
		raw, ok := ne.Results[kind]
		c.mu.Unlock()
		log.Debugf("Media cache: %s found as %s", st.path, filepath.Join(c.dirOf(cand), name))
		return st, c.decode(raw, ok, v)
	}
	c.count(false)
	return st, false
}

// sameFileAlias reports whether a and b are spellings of one path that differ only in case
// (or normalization) and name the same file.
func (c *Cache) sameFileAlias(a, b string) bool {
	if !sameFolding(a, b) {
		return false
	}
	sa, err := os.Stat(a)
	if err != nil {
		return false
	}
	sb, err := os.Stat(b)
	return err == nil && os.SameFile(sa, sb)
}

// decode unmarshals a stored result into v and counts the lookup.
func (c *Cache) decode(raw json.RawMessage, ok bool, v any) bool {
	if ok && json.Unmarshal(raw, v) != nil {
		ok = false
	}
	c.count(ok)
	return ok
}

func (c *Cache) count(hit bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if hit {
		c.stats.Hits++
	} else {
		c.stats.Misses++
	}
}

// Store stores v (as JSON) as the result of the kind for the file st was taken of, if the
// file still has that stat (it did not change while v was computed) and its mtime is at
// least RacyWindow older than now (else it might change again unnoticed within the same
// timestamp tick). An invalid Stamp is ignored.
func (c *Cache) Store(st Stamp, kind string, v any) {
	if c == nil || !st.ok {
		return
	}
	if c.now().Sub(time.Unix(0, st.mtimeNs)) < RacyWindow { // also an mtime in the future
		return
	}
	if !stampOf(st.path).same(st) {
		log.Debugf("Media cache: %s changed while it was scanned, not stored", st.path)
		return
	}
	data, err := json.Marshal(v)
	if err != nil {
		log.Debugf("Media cache: cannot store %s for %s: %v", kind, st.path, err)
		return
	}
	key, name := c.keyOf(st.path)
	c.mu.Lock()
	defer c.mu.Unlock()
	e := c.entry(key, name)
	if e == nil || !e.matches(st) {
		e = &fileEntry{Size: st.size, MtimeNs: st.mtimeNs, Results: map[string]json.RawMessage{}}
		c.put(key, name, e)
	} else {
		c.markSeen(c.folders[key], e)
	}
	e.Results[kind] = data
	c.stats.Stored++
}

// Stats returns what the cache did so far (zero for a nil cache).
func (c *Cache) Stats() Stats {
	if c == nil {
		return Stats{}
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.stats
}

// Path returns the path of the cache file ("" for a nil cache).
func (c *Cache) Path() string {
	if c == nil {
		return ""
	}
	return c.path
}

// Save prunes the entries of files that were not seen in this run and no longer exist (a
// file seen in this run is kept even if it is gone: it may have been moved, and is then
// found by its name, size and mtime), and writes the cache file with ops.WriteCacheFile
// (also in a dry run). An error is logged as a warning and returned; it is no failure of
// the run. A nil cache does nothing; a library root that does not exist is not created.
// A file whose results were dropped for other tool versions is only replaced once this run
// stored a result: a run without ffprobe on PATH (a text-only tidy-up from another shell)
// would otherwise wipe the results of the tools the media runs use.
func (c *Cache) Save(ops *fsops.Ops) error {
	if c == nil {
		return nil
	}
	st := c.Stats()
	log.Infof("Media cache: %d reused, %d scanned, %d stored", st.Hits, st.Misses, st.Stored)
	if c.dropped && st.Stored == 0 {
		log.Debugf("Media cache %s kept: it holds results of other tool versions and this run stored none", c.path)
		return nil
	}
	if _, err := os.Stat(c.root); errors.Is(err, fs.ErrNotExist) {
		log.Debugf("Media cache not saved: %s does not exist", c.root)
		return nil
	}
	c.prune()
	c.mu.Lock()
	c.updated = c.now().UTC().Truncate(time.Second)
	d := diskFile{Format: formatVersion, Tools: c.tools, Updated: c.updated, Folders: c.folders}
	var buf bytes.Buffer
	zw := gzip.NewWriter(&buf)
	err := json.NewEncoder(zw).Encode(d)
	c.mu.Unlock()
	if err == nil {
		err = zw.Close()
	}
	if err == nil {
		err = ops.WriteCacheFile(c.path, buf.Bytes())
	}
	if err != nil {
		log.Warnf("Media cache could not be saved: %v", err)
	}
	return err
}

// pruneWorkers is how many folders prune checks at the same time: the checks wait on the
// file system (a NAS), not on the CPU.
const pruneWorkers = 16

// pruneFolder is the work of prune on one folder: its unseen entries, and for those the
// seen entries of the same file in folders whose path differs only in case (aliases).
type pruneFolder struct {
	key     string
	unseen  map[string]*fileEntry
	aliases map[string][]string // name -> folder keys of seen entries with the same name, size, mtime
}

// prune drops the files that were not seen in this run and are no longer in their
// folder. A folder that cannot be listed (other than missing) keeps its entries. An
// unseen entry whose folder path differs from that of a seen entry of the same file only
// in case or Unicode normalization, and that is the same file (os.SameFile: on a
// case-insensitive file system both spellings resolve), is the stale record of a
// case-only rename and is dropped as well. The folders are listed in parallel and outside
// c.mu (one listing per folder with an unseen entry: after a run that looked only at the
// audio files, every folder with a video). c.mu must not be held.
func (c *Cache) prune() {
	c.mu.Lock()
	var work []*pruneFolder
	for key, fo := range c.folders {
		var pf *pruneFolder
		for name, e := range fo.Files {
			if e.run {
				continue
			}
			if pf == nil {
				pf = &pruneFolder{key: key, unseen: map[string]*fileEntry{}, aliases: map[string][]string{}}
				work = append(work, pf)
			}
			pf.unseen[name] = e
			for other := range c.index[secKey{name, e.Size, e.MtimeNs}] {
				if oe := c.entry(other, name); other != key && oe != nil && oe.run && sameFolding(c.dirOf(key), c.dirOf(other)) {
					pf.aliases[name] = append(pf.aliases[name], other)
				}
			}
		}
	}
	c.mu.Unlock()

	gone := make([][]string, len(work))
	_ = utils.ForEachParallel(context.Background(), work, utils.Parallel{Workers: pruneWorkers}, func(i int, pf *pruneFolder) {
		dir := c.dirOf(pf.key)
		entries, err := c.listDir(dir)
		if err != nil && !errors.Is(err, fs.ErrNotExist) {
			return
		}
		present := map[string]bool{}
		for _, de := range entries {
			present[de.Name()] = true
		}
		for name := range pf.unseen {
			if !present[name] || c.isAlias(filepath.Join(dir, name), name, pf.aliases[name]) {
				gone[i] = append(gone[i], name)
			}
		}
	})

	c.mu.Lock()
	defer c.mu.Unlock()
	for i, pf := range work {
		for _, name := range gone[i] {
			// Only the entry that was checked: Save runs at the end of a run, but a Lookup
			// may still have re-keyed or refreshed it meanwhile.
			if e := c.entry(pf.key, name); e != nil && e == pf.unseen[name] && !e.run {
				c.remove(pf.key, name)
			}
		}
	}
}

// isAlias reports whether the file at path is the same file as name in one of the folders
// (keys): the same file reached through a spelling of its folder that differs in case.
func (c *Cache) isAlias(path, name string, keys []string) bool {
	if len(keys) == 0 {
		return false
	}
	st, err := os.Stat(path)
	if err != nil {
		return false
	}
	for _, k := range keys {
		if ot, err := os.Stat(filepath.Join(c.dirOf(k), name)); err == nil && os.SameFile(st, ot) {
			return true
		}
	}
	return false
}

// sameFolding reports whether two paths differ at most in case and Unicode normalization,
// as on a case-insensitive file system (NTFS, APFS) they may name the same folder.
func sameFolding(a, b string) bool {
	return strings.EqualFold(norm.NFC.String(a), norm.NFC.String(b))
}
