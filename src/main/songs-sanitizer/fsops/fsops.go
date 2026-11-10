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
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"

	log "github.com/sirupsen/logrus"
)

// Stats counts the operations performed (or, in dry-run mode, planned) during a run.
type Stats struct {
	Written  int64 // files (re)written
	Moved    int64 // files or folders moved
	Removed  int64 // files removed
	Replaced int64 // files replaced by a processed version
	Failed   int64 // operations that returned an error
}

// Ops performs file system modifications. It is safe for concurrent use.
type Ops struct {
	DryRun bool

	written, moved, removed, replaced, failed atomic.Int64

	// reserveMu serializes choosing a free target name, so two goroutines moving
	// equally named items into the same directory do not pick the same name.
	reserveMu sync.Mutex
	reserved  map[string]bool

	// plannedMu guards plannedMoves: in dry-run mode, the sources of planned moves, so that
	// later modules can skip what an earlier module would have moved away.
	plannedMu    sync.Mutex
	plannedMoves map[string]bool
}

// New returns an Ops value. In dry-run mode no modification is executed; each planned
// change is logged with a "[dry-run]" prefix instead.
func New(dryRun bool) *Ops {
	return &Ops{DryRun: dryRun, reserved: map[string]bool{}, plannedMoves: map[string]bool{}}
}

// Stats returns a snapshot of the counters.
func (o *Ops) Stats() Stats {
	return Stats{
		Written:  o.written.Load(),
		Moved:    o.moved.Load(),
		Removed:  o.removed.Load(),
		Replaced: o.replaced.Load(),
		Failed:   o.failed.Load(),
	}
}

// Fail records a failed operation that happened outside of Ops (e.g. a failed ffmpeg run),
// so it is reflected in the summary and the exit code.
func (o *Ops) Fail() { o.failed.Add(1) }

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
func (o *Ops) WriteFileAtomic(path string, data []byte) (bool, error) {
	mode := os.FileMode(0o644)
	current, err := os.ReadFile(path)
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

// WriteReport writes a report file (outside the library). Reports are written in dry-run
// mode too, because they do not modify the library.
func (o *Ops) WriteReport(path string, data []byte) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return o.fail(fmt.Errorf("creating report dir for %s: %w", path, err))
	}
	if err := os.WriteFile(path, data, 0o644); err != nil {
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
	dst, err := o.reserveTarget(filepath.Join(dstDir, relPath), srcInfo.IsDir())
	if err != nil {
		return "", o.fail(err)
	}
	defer o.release(dst)

	if o.DryRun {
		o.plannedMu.Lock()
		o.plannedMoves[filepath.Clean(src)] = true
		o.plannedMu.Unlock()
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
	o.moved.Add(1)
	return dst, nil
}

// Rename renames src to dst within the library. Unlike MoveInto it does not pick a new
// name: it fails if dst already exists.
func (o *Ops) Rename(src, dst string) error {
	if _, err := os.Lstat(dst); err == nil {
		return o.fail(fmt.Errorf("renaming %s -> %s: target exists", src, dst))
	}
	if o.DryRun {
		log.Infof("[dry-run] would rename %s -> %s", src, dst)
		o.moved.Add(1)
		return nil
	}
	if err := move(src, dst); err != nil {
		return o.fail(fmt.Errorf("renaming %s -> %s: %w", src, dst, err))
	}
	o.moved.Add(1)
	return nil
}

// Replace atomically replaces dst with the processed file tmp (which must be on the same
// file system, i.e. created next to dst). Used after ffmpeg produced a new version.
func (o *Ops) Replace(tmp, dst string) error {
	if o.DryRun {
		// Nothing is touched in dry-run mode, not even tmp; callers do not create temp
		// files in dry-run mode (see PlanReplace).
		log.Infof("[dry-run] would replace %s", dst)
		o.replaced.Add(1)
		return nil
	}
	if err := os.Rename(tmp, dst); err != nil {
		os.Remove(tmp)
		return o.fail(fmt.Errorf("replacing %s: %w", dst, err))
	}
	o.replaced.Add(1)
	return nil
}

// PlanReplace records, in dry-run mode, that path would be replaced by a processed
// version ("action" describes how, e.g. "re-encode"). It is counted like Replace.
func (o *Ops) PlanReplace(path, action string) {
	log.Infof("[dry-run] would %s %s", action, path)
	o.replaced.Add(1)
}

// WouldBeMoved reports whether path or one of its parent folders was moved away by an
// earlier MoveInto in dry-run mode. It is always false outside dry-run mode, where moved
// paths simply no longer exist.
func (o *Ops) WouldBeMoved(path string) bool {
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
	if !isDir {
		ext = filepath.Ext(want)
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

// move renames src to dst, falling back to copy + delete across file systems.
func move(src, dst string) error {
	err := os.Rename(src, dst)
	if err == nil || !errors.Is(err, syscall.EXDEV) {
		return err
	}
	if err := copyTree(src, dst); err != nil {
		os.RemoveAll(dst) // do not leave a partial copy behind; src is untouched
		return fmt.Errorf("copy across file systems: %w", err)
	}
	return os.RemoveAll(src)
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
