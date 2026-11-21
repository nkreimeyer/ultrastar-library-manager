// Package fsops is the single place where the sanitizer modifies the file system.
//
// Every mutating operation goes through an *Ops value so that dry-run mode, atomic writes,
// cross-device moves, collision-free target names and the end-of-run statistics are
// handled consistently. Modules must not call os.Rename/os.Remove/os.WriteFile on library
// files directly.
package fsops

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"

	log "github.com/sirupsen/logrus"
)

// Stats counts the operations performed (or, in dry-run mode, planned) during a run.
type Stats struct {
	Written  int64 `json:"written"`  // files (re)written
	Moved    int64 `json:"moved"`    // files or folders moved
	Removed  int64 `json:"removed"`  // files removed
	Replaced int64 `json:"replaced"` // files replaced by a processed version
	Copied   int64 `json:"copied"`   // files backed up (copied out of the library)
	Failed   int64 `json:"failed"`   // operations that returned an error
}

// Ops performs file system modifications. It is safe for concurrent use.
type Ops struct {
	DryRun bool

	written, moved, removed, replaced, copied, failed atomic.Int64

	// failedKeys deduplicates failures reported with FailOnce.
	failedMu   sync.Mutex
	failedKeys map[string]bool

	// reserveMu serializes choosing a free target name, so two goroutines moving
	// equally named items into the same directory do not pick the same name.
	reserveMu sync.Mutex
	reserved  map[string]bool

	// plannedMu guards plannedMoves: in dry-run mode, the sources of planned moves, so that
	// later modules can skip what an earlier module would have moved away.
	plannedMu    sync.Mutex
	plannedMoves map[string]bool
	runTargets   []string // targets of the (real or planned) moves of this run
	// plannedPresent: in dry-run mode, the paths a planned move took away (false) or
	// put in place (true), in the order the moves were planned. MoveTo uses it to see
	// the library as a real run would have left it so far (see dryRunExists).
	plannedPresent map[string]bool

	// writesMu guards plannedWrites: in dry-run mode, the content of every planned write
	// that changes a file (cleaned path -> data), so that later modules read the txts as
	// an earlier module would have left them (see ReadFile).
	writesMu      sync.Mutex
	plannedWrites map[string][]byte
}

// New returns an Ops value. In dry-run mode no modification is executed; each planned
// change is logged with a "[dry-run]" prefix instead.
func New(dryRun bool) *Ops {
	return &Ops{DryRun: dryRun, reserved: map[string]bool{}, plannedMoves: map[string]bool{},
		plannedPresent: map[string]bool{}, plannedWrites: map[string][]byte{}, failedKeys: map[string]bool{}}
}

// Stats returns a snapshot of the counters.
func (o *Ops) Stats() Stats {
	return Stats{
		Written:  o.written.Load(),
		Moved:    o.moved.Load(),
		Removed:  o.removed.Load(),
		Replaced: o.replaced.Load(),
		Copied:   o.copied.Load(),
		Failed:   o.failed.Load(),
	}
}

// Fail records a failed operation that happened outside of Ops (e.g. a failed ffmpeg run),
// so it is reflected in the summary and the exit code.
func (o *Ops) Fail() { o.failed.Add(1) }

// FailOnce records a failure identified by key only the first time it is reported, e.g.
// an unreadable folder that every module runs into.
func (o *Ops) FailOnce(key string) {
	o.failedMu.Lock()
	defer o.failedMu.Unlock()
	if !o.failedKeys[key] {
		o.failedKeys[key] = true
		o.failed.Add(1)
	}
}

func (o *Ops) fail(err error) error {
	if err != nil {
		o.failed.Add(1)
	}
	return err
}

// WriteFileAtomic writes data to path only if the content differs from the current file.
// The data is written to a temporary file in the same directory which then replaces the
// original, so a crash never leaves a truncated file behind. The file mode of an existing
// file is preserved. It reports whether the file was (or, in dry-run mode, would be) changed.
//
// In dry-run mode the data is compared with the content an earlier planned write left
// (see ReadFile), and a changing write is recorded, so that ReadFile returns it.
func (o *Ops) WriteFileAtomic(path string, data []byte) (bool, error) {
	mode := os.FileMode(0o644)
	current, err := o.ReadFile(path)
	switch {
	case err == nil:
		if bytes.Equal(current, data) {
			return false, nil
		}
		if st, err := os.Stat(path); err == nil {
			mode = st.Mode().Perm()
		}
	case !errors.Is(err, os.ErrNotExist):
		return false, o.fail(fmt.Errorf("reading %s: %w", path, err))
	}

	if o.DryRun {
		log.Infof("[dry-run] would rewrite %s", path)
		o.writesMu.Lock()
		o.plannedWrites[filepath.Clean(path)] = bytes.Clone(data)
		o.writesMu.Unlock()
		o.written.Add(1)
		return true, nil
	}

	tmp, err := os.CreateTemp(filepath.Dir(path), "."+filepath.Base(path)+".*.tmp")
	if err != nil {
		return false, o.fail(fmt.Errorf("creating temp file for %s: %w", path, err))
	}
	tmpName := tmp.Name()
	cleanup := func(err error) (bool, error) {
		tmp.Close()
		os.Remove(tmpName)
		return false, o.fail(err)
	}
	if _, err := tmp.Write(data); err != nil {
		return cleanup(fmt.Errorf("writing %s: %w", tmpName, err))
	}
	if err := tmp.Sync(); err != nil {
		return cleanup(fmt.Errorf("syncing %s: %w", tmpName, err))
	}
	if err := tmp.Close(); err != nil {
		return cleanup(fmt.Errorf("closing %s: %w", tmpName, err))
	}
	if err := os.Chmod(tmpName, mode); err != nil {
		return cleanup(fmt.Errorf("chmod %s: %w", tmpName, err))
	}
	if err := os.Rename(tmpName, path); err != nil {
		os.Remove(tmpName)
		return false, o.fail(fmt.Errorf("replacing %s: %w", path, err))
	}
	o.written.Add(1)
	return true, nil
}

// ReadFile reads a library file as this run has left it so far. Outside dry-run mode (and
// for a nil *Ops) it is os.ReadFile. In dry-run mode a file that an earlier MoveInto
// planned to move away (the file itself or a parent folder, see WouldBeMoved) reads as
// not existing (an error wrapping fs.ErrNotExist), a file with a planned WriteFileAtomic
// returns the planned content, and every other file is read from disk.
//
// Only planned writes and planned moves away are simulated. A file planned to be moved or
// renamed INTO path, or replaced by a processed version (Replace, PlanReplace), is not:
// path then reads as it is on disk. Modules must read every txt they base a decision on
// with ReadFile, so a dry run plans what a real run does.
func (o *Ops) ReadFile(path string) ([]byte, error) {
	if o == nil || !o.DryRun {
		return os.ReadFile(path)
	}
	if o.WouldBeMoved(path) {
		return nil, &fs.PathError{Op: "read", Path: path, Err: fmt.Errorf("planned to be moved away: %w", fs.ErrNotExist)}
	}
	o.writesMu.Lock()
	data, ok := o.plannedWrites[filepath.Clean(path)]
	o.writesMu.Unlock()
	if ok {
		return bytes.Clone(data), nil
	}
	return os.ReadFile(path)
}

// WriteReport atomically writes a report file (outside the library). Reports are written
// in dry-run mode too, because they do not modify the library.
func (o *Ops) WriteReport(path string, data []byte) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return o.fail(fmt.Errorf("creating report dir for %s: %w", path, err))
	}
	// Write atomically (temp file + rename): a crash must not truncate a report or the
	// media journal.
	tmp, err := os.CreateTemp(filepath.Dir(path), "."+filepath.Base(path)+".*.tmp")
	if err != nil {
		return o.fail(fmt.Errorf("writing report %s: %w", path, err))
	}
	_, werr := tmp.Write(data)
	if err := errors.Join(werr, tmp.Chmod(0o644), tmp.Sync(), tmp.Close()); err != nil {
		os.Remove(tmp.Name())
		return o.fail(fmt.Errorf("writing report %s: %w", path, err))
	}
	if err := os.Rename(tmp.Name(), path); err != nil {
		os.Remove(tmp.Name())
		return o.fail(fmt.Errorf("writing report %s: %w", path, err))
	}
	return nil
}

// MoveInto moves src (a file or a folder) to dstDir/relPath, creating parent folders as
// needed. relPath is typically the item's path relative to the library root, so the
// library structure is mirrored in the target. If the target already exists, a numeric
// suffix (" (2)", " (3)", ...) is appended. Moves across file systems fall back to
// copy + delete. It returns the final target path.
func (o *Ops) MoveInto(src, dstDir, relPath string) (string, error) {
	srcInfo, err := os.Lstat(src)
	if err != nil {
		return "", o.fail(fmt.Errorf("moving %s: %w", src, err))
	}
	// A folder is merged into an existing target folder instead of creating "X (2)" if
	// this run already moved something into that target (a category whose songs were just
	// moved there), or if it is empty. A song folder colliding with one from an earlier run
	// is not merged: it gets its own " (2)" folder.
	if want := filepath.Join(dstDir, relPath); srcInfo.IsDir() &&
		(o.movedBelowThisRun(want) || (isEmptyDir(src) && isDir(want))) {
		return want, o.mergeInto(src, dstDir, relPath)
	}
	dst, err := o.reserveTarget(filepath.Join(dstDir, relPath), srcInfo.IsDir())
	if err != nil {
		return "", o.fail(err)
	}
	defer o.release(dst)

	if o.DryRun {
		o.markPlanned(src, dst)
		log.Infof("[dry-run] would move %s -> %s", src, dst)
		o.moved.Add(1)
		return dst, nil
	}
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return "", o.fail(fmt.Errorf("creating %s: %w", filepath.Dir(dst), err))
	}
	if err := move(src, dst); err != nil {
		return "", o.fail(fmt.Errorf("moving %s -> %s: %w", src, dst, err))
	}
	log.Infof("Moved %s -> %s", src, dst)
	o.recordTarget(dst)
	o.moved.Add(1)
	return dst, nil
}

// MoveTo moves src (a file or a folder) to exactly dst, creating the parent folders of
// dst as needed. Unlike MoveInto it never picks another name and never merges: it fails
// if dst exists (Lstat, so a dangling symlink counts) or is reserved by a concurrent move
// of this run. Moves across file systems fall back to copy + delete.
//
// In dry-run mode nothing is touched; the move is logged, counted and recorded, so that
// later MoveTo calls of the same run see the library as a real run would have left it:
// a planned source counts as gone (WouldBeMoved, and ReadFile below it) and a planned
// target as taken. A path moved away and then refilled by a planned MoveTo counts as
// present again (the ordered moves of organize, e.g. breaking a cycle through a
// temporary name), but files below such a refilled path are not simulated.
func (o *Ops) MoveTo(src, dst string) error {
	src, dst = filepath.Clean(src), filepath.Clean(dst)
	if !o.exists(src) {
		return o.fail(fmt.Errorf("moving %s -> %s: %w", src, dst, fs.ErrNotExist))
	}
	o.reserveMu.Lock()
	switch {
	case o.reserved[dst]:
		o.reserveMu.Unlock()
		return o.fail(fmt.Errorf("moving %s -> %s: target is reserved by another move", src, dst))
	case o.exists(dst):
		o.reserveMu.Unlock()
		return o.fail(fmt.Errorf("moving %s -> %s: target exists", src, dst))
	}
	o.reserved[dst] = true
	o.reserveMu.Unlock()
	defer o.release(dst)

	if o.DryRun {
		o.markPlanned(src, dst)
		log.Infof("[dry-run] would move %s -> %s", src, dst)
		o.moved.Add(1)
		return nil
	}
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return o.fail(fmt.Errorf("creating %s: %w", filepath.Dir(dst), err))
	}
	if err := move(src, dst); err != nil {
		return o.fail(fmt.Errorf("moving %s -> %s: %w", src, dst, err))
	}
	log.Infof("Moved %s -> %s", src, dst)
	o.recordTarget(dst)
	o.moved.Add(1)
	return nil
}

// exists reports whether path exists as this run has left it so far: on disk outside
// dry-run mode, and in dry-run mode after the planned moves (see MoveTo).
func (o *Ops) exists(path string) bool {
	if o.DryRun {
		o.plannedMu.Lock()
		present, known := o.plannedPresent[path]
		o.plannedMu.Unlock()
		if known {
			return present
		}
		if o.WouldBeMoved(path) {
			return false
		}
	}
	_, err := os.Lstat(path)
	return err == nil
}

// AppendJournal appends line (a newline is added if it lacks one) to the journal file
// path, creating the file and its folder if needed, and fsyncs it, so a completed step is
// on disk before the next one starts. It is meant for journals in the output dir, never
// for library files. In dry-run mode it does nothing: a dry run must leave no journal
// entries behind. A crash can leave a truncated last line, which readers must skip; the
// next append starts a new line after it, so its own entry is not merged into it.
func (o *Ops) AppendJournal(path string, line []byte) error {
	if o.DryRun {
		return nil
	}
	if !bytes.HasSuffix(line, []byte("\n")) {
		line = append(bytes.Clone(line), '\n')
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return o.fail(fmt.Errorf("creating journal dir for %s: %w", path, err))
	}
	f, err := os.OpenFile(path, os.O_RDWR|os.O_APPEND|os.O_CREATE, 0o644)
	if err != nil {
		return o.fail(fmt.Errorf("opening journal %s: %w", path, err))
	}
	// After a crash the last line may be truncated: start a new line, so the truncated
	// garbage stays alone on its line (which readers skip) and this entry is kept.
	st, err := f.Stat()
	if err != nil {
		f.Close()
		return o.fail(fmt.Errorf("reading journal %s: %w", path, err))
	}
	if st.Size() > 0 {
		last := make([]byte, 1)
		if _, err := f.ReadAt(last, st.Size()-1); err != nil {
			f.Close()
			return o.fail(fmt.Errorf("reading journal %s: %w", path, err))
		}
		if last[0] != '\n' {
			line = append([]byte{'\n'}, line...)
		}
	}
	_, werr := f.Write(line)
	if err := errors.Join(werr, f.Sync(), f.Close()); err != nil {
		return o.fail(fmt.Errorf("appending to journal %s: %w", path, err))
	}
	return nil
}

// RemoveEmptyDir removes the folder path if it is empty and reports whether it did (or,
// in dry-run mode, would). It is meant for undoing a run: removing the folders a MoveTo
// of that run created as parents. It never removes data: os.Remove refuses a folder that
// is not empty, and a folder with any entry (hidden ones included) is left alone and not
// counted as a failure. A missing path, a file and a symlink are left alone, no error; a
// folder that cannot be read is a counted failure. In dry-run mode the folder counts as
// empty if every entry on disk was planned to be moved away and no planned move put
// anything below it or the folder itself in place; it then counts as gone for later
// MoveTo calls. Removed folders are
// not counted in Stats (they held no files).
func (o *Ops) RemoveEmptyDir(path string) (bool, error) {
	path = filepath.Clean(path)
	if !o.exists(path) {
		return false, nil
	}
	// Lstat: a symlink to a folder is no folder of the run; removing it would delete it.
	if st, err := os.Lstat(path); err != nil || !st.IsDir() {
		return false, nil
	}
	if o.DryRun {
		entries, err := os.ReadDir(path)
		if err != nil {
			return false, o.fail(fmt.Errorf("reading %s: %w", path, err))
		}
		for _, e := range entries {
			if o.exists(filepath.Join(path, e.Name())) {
				return false, nil
			}
		}
		o.plannedMu.Lock()
		prefix := path + string(filepath.Separator)
		for p, present := range o.plannedPresent {
			// A folder a planned move put in place has unknown content: never empty.
			if present && (p == path || strings.HasPrefix(p, prefix)) {
				o.plannedMu.Unlock()
				return false, nil
			}
		}
		o.plannedMu.Unlock()
		o.markPlanned(path, "")
		log.Infof("[dry-run] would remove the empty folder %s", path)
		return true, nil
	}
	if empty, err := dirIsEmpty(path); err != nil {
		return false, o.fail(fmt.Errorf("reading %s: %w", path, err))
	} else if !empty {
		return false, nil
	}
	if err := os.Remove(path); err != nil {
		return false, o.fail(fmt.Errorf("removing empty folder %s: %w", path, err))
	}
	log.Infof("Removed the empty folder %s", path)
	return true, nil
}

// Rename renames src to dst within the library. Unlike MoveInto it does not pick a new
// name: it fails if dst already exists. A regular file src is fsynced first and the folder
// of dst after the rename, because callers delete the original once src is in place
// (fixvideos' conversions), and ffmpeg does not fsync its output.
func (o *Ops) Rename(src, dst string) error {
	if _, err := os.Lstat(dst); err == nil {
		return o.fail(fmt.Errorf("renaming %s -> %s: target exists", src, dst))
	}
	if o.DryRun {
		log.Infof("[dry-run] would rename %s -> %s", src, dst)
		o.moved.Add(1)
		return nil
	}
	if st, err := os.Lstat(src); err == nil && st.Mode().IsRegular() {
		if err := syncFile(src); err != nil {
			return o.fail(fmt.Errorf("renaming %s -> %s: syncing %s: %w", src, dst, src, err))
		}
	}
	if err := move(src, dst); err != nil {
		return o.fail(fmt.Errorf("renaming %s -> %s: %w", src, dst, err))
	}
	syncDir(filepath.Dir(dst))
	o.moved.Add(1)
	return nil
}

// Replace atomically replaces dst with the processed file tmp (which must be on the same
// file system, i.e. created next to dst). Used after ffmpeg produced a new version. tmp is
// fsynced before the rename (ffmpeg does not fsync its output, and the replaced video is
// not kept), and the folder after it.
func (o *Ops) Replace(tmp, dst string) error {
	if o.DryRun {
		// Nothing is touched in dry-run mode, not even tmp; callers do not create temp
		// files in dry-run mode (see PlanReplace).
		log.Infof("[dry-run] would replace %s", dst)
		o.replaced.Add(1)
		return nil
	}
	if err := syncFile(tmp); err != nil {
		os.Remove(tmp)
		return o.fail(fmt.Errorf("replacing %s: syncing %s: %w", dst, tmp, err))
	}
	if err := os.Rename(tmp, dst); err != nil {
		os.Remove(tmp)
		return o.fail(fmt.Errorf("replacing %s: %w", dst, err))
	}
	syncDir(filepath.Dir(dst))
	o.replaced.Add(1)
	return nil
}

// syncFile fsyncs the file at path, so its data is on disk before a rename makes it the
// only copy. A variable, so tests can make it fail.
var syncFile = func(path string) error {
	flag := os.O_RDONLY
	if runtime.GOOS == "windows" {
		flag = os.O_RDWR // FlushFileBuffers needs write access there
	}
	f, err := os.OpenFile(path, flag, 0)
	if err != nil {
		return err
	}
	return errors.Join(f.Sync(), f.Close())
}

// syncDir fsyncs the folder dir, so a rename into it is on disk before the caller deletes
// the original. It is best effort: Windows cannot fsync a folder, and some network file
// systems refuse to, so a failure is only logged.
func syncDir(dir string) {
	if runtime.GOOS == "windows" {
		return
	}
	f, err := os.Open(dir)
	if err != nil {
		log.Debugf("Syncing the folder %s: %v", dir, err)
		return
	}
	if err := errors.Join(f.Sync(), f.Close()); err != nil {
		log.Debugf("Syncing the folder %s: %v", dir, err)
	}
}

// CopyInto copies the regular file src to dstDir/relPath (creating parent folders, adding
// a " (2)" suffix if the target exists), e.g. to keep a backup of a file before it is
// replaced. Permissions and mtime are preserved. It returns the final target path.
func (o *Ops) CopyInto(src, dstDir, relPath string) (string, error) {
	info, err := os.Stat(src)
	if err != nil {
		return "", o.fail(fmt.Errorf("copying %s: %w", src, err))
	}
	if !info.Mode().IsRegular() {
		return "", o.fail(fmt.Errorf("copying %s: not a regular file", src))
	}
	dst, err := o.reserveTarget(filepath.Join(dstDir, relPath), false)
	if err != nil {
		return "", o.fail(err)
	}
	defer o.release(dst)
	if o.DryRun {
		log.Infof("[dry-run] would copy %s -> %s", src, dst)
		o.copied.Add(1)
		return dst, nil
	}
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return "", o.fail(fmt.Errorf("creating %s: %w", filepath.Dir(dst), err))
	}
	if err := copyFile(src, dst, info); err != nil {
		os.Remove(dst)
		return "", o.fail(fmt.Errorf("copying %s -> %s: %w", src, dst, err))
	}
	o.copied.Add(1)
	return dst, nil
}

// markPlanned records a planned (dry-run) move of src to dst ("" if src just disappears).
func (o *Ops) markPlanned(src, dst string) {
	o.plannedMu.Lock()
	o.plannedMoves[filepath.Clean(src)] = true
	o.plannedPresent[filepath.Clean(src)] = false
	if dst != "" {
		o.plannedPresent[filepath.Clean(dst)] = true
	}
	o.plannedMu.Unlock()
	if dst != "" {
		o.recordTarget(dst)
	}
}

// recordTarget remembers the target of a (real or planned) move of this run.
func (o *Ops) recordTarget(dst string) {
	o.plannedMu.Lock()
	o.runTargets = append(o.runTargets, filepath.Clean(dst))
	o.plannedMu.Unlock()
}

// movedBelowThisRun reports whether this run moved (or, in dry-run mode, planned to move)
// something into a path below dir.
func (o *Ops) movedBelowThisRun(dir string) bool {
	o.plannedMu.Lock()
	defer o.plannedMu.Unlock()
	prefix := filepath.Clean(dir) + string(filepath.Separator)
	for _, t := range o.runTargets {
		if strings.HasPrefix(t, prefix) {
			return true
		}
	}
	return false
}

func isDir(path string) bool {
	st, err := os.Stat(path)
	return err == nil && st.IsDir()
}

func isEmptyDir(dir string) bool {
	empty, err := dirIsEmpty(dir)
	return err == nil && empty
}

// dirIsEmpty reports whether the folder dir has no entries; an error if it cannot be read.
func dirIsEmpty(dir string) (bool, error) {
	f, err := os.Open(dir)
	if err != nil {
		return false, err
	}
	defer f.Close()
	if _, err = f.Readdirnames(1); errors.Is(err, io.EOF) {
		return true, nil
	}
	return false, err
}

// mergeInto moves every entry of the folder src into dstDir/relPath (recursively merging
// subfolders that exist there too; files that exist get a " (2)" suffix) and then removes
// the emptied src.
func (o *Ops) mergeInto(src, dstDir, relPath string) error {
	entries, err := os.ReadDir(src)
	if err != nil {
		return o.fail(fmt.Errorf("merging %s: %w", src, err))
	}
	var errs []error
	for _, e := range entries {
		child := filepath.Join(src, e.Name())
		if o.WouldBeMoved(child) {
			continue // dry-run: already planned away by an earlier module
		}
		if _, err := o.MoveInto(child, dstDir, filepath.Join(relPath, e.Name())); err != nil {
			errs = append(errs, err) // already counted
		}
	}
	if len(errs) > 0 {
		return fmt.Errorf("merging %s: %w", src, errors.Join(errs...))
	}
	if o.DryRun {
		log.Infof("[dry-run] would merge %s into %s", src, filepath.Join(dstDir, relPath))
		o.markPlanned(src, "")
		o.moved.Add(1)
		return nil
	}
	if err := os.Remove(src); err != nil {
		return o.fail(fmt.Errorf("removing merged folder %s: %w", src, err))
	}
	log.Infof("Merged %s into %s", src, filepath.Join(dstDir, relPath))
	o.moved.Add(1)
	return nil
}

// PlanReplace records, in dry-run mode, that path would be replaced by a processed
// version ("action" describes how, e.g. "re-encode"). It is counted like Replace.
func (o *Ops) PlanReplace(path, action string) {
	log.Infof("[dry-run] would %s %s", action, path)
	o.replaced.Add(1)
}

// Exists reports whether path exists as this run has left it so far: on disk outside
// dry-run mode, and in dry-run mode after the planned moves (MoveInto, MoveTo).
func (o *Ops) Exists(path string) bool {
	return o.exists(filepath.Clean(path))
}

// WouldBeMoved reports whether path or one of its parent folders was moved away by an
// earlier MoveInto in dry-run mode. It is always false outside dry-run mode, where moved
// paths simply no longer exist. A nil *Ops plans nothing.
func (o *Ops) WouldBeMoved(path string) bool {
	if o == nil {
		return false
	}
	o.plannedMu.Lock()
	defer o.plannedMu.Unlock()
	if len(o.plannedMoves) == 0 {
		return false
	}
	for p := filepath.Clean(path); ; p = filepath.Dir(p) {
		if o.plannedMoves[p] {
			return true
		}
		if parent := filepath.Dir(p); parent == p {
			return false
		}
	}
}

// Remove deletes a single file.
func (o *Ops) Remove(path string) error {
	if o.DryRun {
		log.Infof("[dry-run] would remove %s", path)
		o.removed.Add(1)
		return nil
	}
	if err := os.Remove(path); err != nil {
		return o.fail(fmt.Errorf("removing %s: %w", path, err))
	}
	o.removed.Add(1)
	return nil
}

// TempFileNear creates an empty temporary file next to target with the given extension
// (ffmpeg picks the output format from the extension). The temp file gets the permissions
// of target (0644 if target does not exist), so the processed file that replaces target
// keeps them. ffmpeg keeps the mode of an existing output file. The caller must pass ffmpeg
// "-y" because the file already exists, and must remove it on failure.
func TempFileNear(target, ext string) (string, error) {
	f, err := os.CreateTemp(filepath.Dir(target), "."+strings.TrimSuffix(filepath.Base(target), filepath.Ext(target))+".sanitizer-*"+ext)
	if err != nil {
		return "", err
	}
	name := f.Name()
	mode := os.FileMode(0o644)
	if st, err := os.Stat(target); err == nil {
		mode = st.Mode().Perm()
	}
	if err := f.Chmod(mode); err != nil {
		f.Close()
		os.Remove(name)
		return "", err
	}
	if err := f.Close(); err != nil {
		os.Remove(name)
		return "", err
	}
	return name, nil
}

// reserveTarget returns a path based on want that neither exists nor is reserved by a
// concurrent move. For files the suffix goes before the extension ("cover (2).jpg"); for
// folders it is appended, since folder names often contain dots ("Mr. Brightside (2)").
func (o *Ops) reserveTarget(want string, isDir bool) (string, error) {
	o.reserveMu.Lock()
	defer o.reserveMu.Unlock()
	ext := ""
	if name := filepath.Base(want); !isDir {
		// A leading dot belongs to the name: ".DS_Store" -> ".DS_Store (2)".
		if ext = filepath.Ext(name); ext == name {
			ext = ""
		}
	}
	base := strings.TrimSuffix(want, ext)
	candidate := want
	for i := 2; ; i++ {
		if _, err := os.Lstat(candidate); errors.Is(err, os.ErrNotExist) && !o.reserved[candidate] {
			o.reserved[candidate] = true
			return candidate, nil
		} else if err != nil && !errors.Is(err, os.ErrNotExist) {
			return "", fmt.Errorf("checking %s: %w", candidate, err)
		}
		if i > 10000 {
			return "", fmt.Errorf("no free target name for %s", want)
		}
		candidate = fmt.Sprintf("%s (%d)%s", base, i, ext)
	}
}

func (o *Ops) release(path string) {
	o.reserveMu.Lock()
	delete(o.reserved, path)
	o.reserveMu.Unlock()
}

// rename is os.Rename; tests replace it to simulate a move across file systems (EXDEV).
var rename = os.Rename

// copyAcross is copyTree; tests wrap it to look at the source while it is copied.
var copyAcross = copyTree

// move renames src to dst, falling back to copy + delete across file systems.
func move(src, dst string) error {
	err := rename(src, dst)
	if err == nil || !isCrossDevice(runtime.GOOS, err) {
		return err
	}
	return moveAcross(src, dst)
}

// errNotSameDevice is ERROR_NOT_SAME_DEVICE, the error os.Rename gets on Windows for a move
// to another drive (MoveFileEx without MOVEFILE_COPY_ALLOWED). On Windows syscall.EXDEV is
// an invented errno that the system never returns, and errors.Is does not map one to the
// other. On Unix the same number is EEXIST, so it only counts on Windows.
const errNotSameDevice = syscall.Errno(17)

// isCrossDevice reports whether err from os.Rename on goos means that src and dst are on
// different file systems. goos is a parameter so that the Windows rule is tested on every
// platform.
func isCrossDevice(goos string, err error) bool {
	var errno syscall.Errno
	if !errors.As(err, &errno) {
		return false
	}
	return errno == syscall.EXDEV || goos == "windows" && errno == errNotSameDevice
}

// moveAcross copies src to dst on another file system and then deletes src. It either
// fails with nothing changed (no copy at dst, src in place) or succeeds with the complete
// copy at dst; it never reports a failure while dst holds the complete copy.
//
// src keeps its name while it is copied, which can take minutes for a video: an
// interrupted copy (Ctrl-C, crash) leaves src visible and intact, plus a partial copy at
// dst, as a plain copy + delete would. Before the copy, src is renamed to a hidden name
// and straight back: that fails cleanly (EACCES, nothing touched) where a rename on the
// same file system would fail too, e.g. a folder that is not writable, so no time is spent
// on a copy that would be thrown away. After the complete copy, src is parked under the
// hidden name and deleted from there. If parking fails after all, the copy is removed
// again and the move fails. If deleting the parked source fails (a read-only subfolder),
// the move counts as done and the hidden leftover is logged, so the half-deleted source
// is not found as a song again.
func moveAcross(src, dst string) error {
	parked, err := parkName(src)
	if err != nil {
		return err
	}
	if err := os.Rename(src, parked); err != nil {
		return fmt.Errorf("preparing the copy across file systems: %w", err)
	}
	if err := os.Rename(parked, src); err != nil {
		return fmt.Errorf("preparing the copy across file systems: moving %s back to %s: %w", parked, src, err)
	}
	if err := copyAcross(src, dst); err != nil {
		os.RemoveAll(dst) // do not leave a partial copy behind; src is untouched
		return fmt.Errorf("copy across file systems: %w", err)
	}
	if err := os.Rename(src, parked); err != nil {
		if rerr := os.RemoveAll(dst); rerr != nil {
			return fmt.Errorf("deleting the source after the copy across file systems: %w (and removing the copy %s failed: %v)", err, dst, rerr)
		}
		return fmt.Errorf("deleting the source after the copy across file systems: %w", err)
	}
	if err := os.RemoveAll(parked); err != nil {
		log.Warnf("Copied %s -> %s across file systems, but could not delete the source; its leftover is %s: %v", src, dst, parked, err)
	}
	return nil
}

// parkSeq numbers the hidden names of parkName, so concurrent moves in one folder never
// pick the same name (os.Rename would replace a file parked there by another move).
var parkSeq atomic.Uint64

// parkName returns an unused hidden name next to path. The name does not contain the
// base name of path, so it is short even when that name is close to NAME_MAX.
func parkName(path string) (string, error) {
	dir := filepath.Dir(path)
	for i := 0; i < 10000; i++ {
		p := filepath.Join(dir, fmt.Sprintf(".fsops-move-%d-%d", os.Getpid(), parkSeq.Add(1)))
		if _, err := os.Lstat(p); errors.Is(err, os.ErrNotExist) {
			return p, nil
		} else if err != nil {
			return "", fmt.Errorf("checking %s: %w", p, err)
		}
	}
	return "", fmt.Errorf("no free temporary name next to %s", path)
}

// copyTree copies a file or directory tree, preserving permissions and modification times
// and verifying sizes.
func copyTree(src, dst string) error {
	type dirMeta struct {
		path string
		info os.FileInfo
	}
	var dirs []dirMeta
	err := filepath.Walk(src, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(src, path)
		if err != nil {
			return err
		}
		target := filepath.Join(dst, rel)
		switch {
		case info.IsDir():
			// Keep the directory writable while its contents are copied; the final mode
			// and mtime are applied afterwards.
			dirs = append(dirs, dirMeta{target, info})
			return os.MkdirAll(target, info.Mode().Perm()|0o700)
		case info.Mode().IsRegular():
			return copyFile(path, target, info)
		case info.Mode()&os.ModeSymlink != 0:
			link, err := os.Readlink(path)
			if err != nil {
				return err
			}
			return os.Symlink(link, target)
		default:
			return fmt.Errorf("unsupported file type: %s", path)
		}
	})
	if err != nil {
		return err
	}
	// Deepest directories first, so setting a parent's mtime is not undone by later
	// changes inside it.
	for i := len(dirs) - 1; i >= 0; i-- {
		d := dirs[i]
		if err := os.Chmod(d.path, d.info.Mode().Perm()); err != nil {
			return err
		}
		if err := os.Chtimes(d.path, d.info.ModTime(), d.info.ModTime()); err != nil {
			return err
		}
	}
	return nil
}

func copyFile(src, dst string, info os.FileInfo) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.OpenFile(dst, os.O_WRONLY|os.O_CREATE|os.O_EXCL, info.Mode().Perm())
	if err != nil {
		return err
	}
	n, err := io.Copy(out, in)
	if err != nil {
		out.Close()
		return err
	}
	if err := out.Sync(); err != nil {
		out.Close()
		return err
	}
	if err := out.Close(); err != nil {
		return err
	}
	// OpenFile applies the umask; set the exact source permissions.
	if err := os.Chmod(dst, info.Mode().Perm()); err != nil {
		return err
	}
	if n != info.Size() {
		return fmt.Errorf("copied %d of %d bytes for %s", n, info.Size(), src)
	}
	return os.Chtimes(dst, info.ModTime(), info.ModTime())
}
