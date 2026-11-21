// Package testutil holds the test helpers several packages of the module share: fixture
// files, folder snapshots, ffmpeg checks and media generators, fake tools and log capture.
//
// It is for _test.go files only (TestNoNonTestImports enforces this): it imports testing
// and logrus' test hook, and its helpers call t.Fatal and t.Skip.
package testutil

import (
	"bytes"
	"crypto/sha256"
	"fmt"
	"image"
	"image/png"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	log "github.com/sirupsen/logrus"
	logtest "github.com/sirupsen/logrus/hooks/test"
)

// WriteFiles writes files (slash-separated path relative to root -> content) below root,
// creating the folders on the way.
func WriteFiles(t testing.TB, root string, files map[string]string) {
	t.Helper()
	for rel, content := range files {
		WriteFile(t, filepath.Join(root, filepath.FromSlash(rel)), content)
	}
}

// WriteFile creates path (and its parents) with the given content.
func WriteFile(t testing.TB, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

// ReadFile returns the content of path and fails the test if it cannot be read.
func ReadFile(t testing.TB, path string) string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

// Exists reports whether path exists (a dangling symlink does).
func Exists(path string) bool {
	_, err := os.Lstat(path)
	return err == nil
}

// DirNames returns the names in dir, sorted, and fails the test if it cannot be read.
func DirNames(t testing.TB, dir string) []string {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, e := range entries {
		names = append(names, e.Name())
	}
	return names
}

// Snapshot describes everything below root, one line per entry in walk order: path, mode,
// size and mtime (ns), plus the SHA-256 of a regular file and the target of a symlink. Two
// equal snapshots mean the folder was left byte-identical (dry runs).
func Snapshot(t testing.TB, root string) string {
	t.Helper()
	var sb strings.Builder
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		info, err := d.Info()
		if err != nil {
			return err
		}
		fmt.Fprintf(&sb, "%s %v %d %d", path, info.Mode(), info.Size(), info.ModTime().UnixNano())
		switch {
		case d.Type().IsRegular():
			data, err := os.ReadFile(path)
			if err != nil {
				return err
			}
			fmt.Fprintf(&sb, " %x", sha256.Sum256(data))
		case d.Type()&fs.ModeSymlink != 0:
			target, err := os.Readlink(path)
			if err != nil {
				return err
			}
			fmt.Fprintf(&sb, " -> %s", target)
		}
		sb.WriteString("\n")
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return sb.String()
}

// Tree maps every entry below root (slash-separated, relative to root) to the content of
// the file, or "<dir>" for a folder.
func Tree(t testing.TB, root string) map[string]string {
	t.Helper()
	got := map[string]string{}
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(root, path)
		if err != nil || rel == "." {
			return err
		}
		rel = filepath.ToSlash(rel)
		if d.IsDir() {
			got[rel] = "<dir>"
			return nil
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		got[rel] = string(data)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return got
}

// PNG is a valid w x h grayscale PNG.
func PNG(w, h int) string {
	var b bytes.Buffer
	if err := png.Encode(&b, image.NewGray(image.Rect(0, 0, w, h))); err != nil {
		panic(err) // cannot happen: encoding into memory
	}
	return b.String()
}

// WritePNG writes PNG(w, h) to path; the folder must exist.
func WritePNG(t testing.TB, path string, w, h int) {
	t.Helper()
	if err := os.WriteFile(path, []byte(PNG(w, h)), 0o644); err != nil {
		t.Fatal(err)
	}
}

// RequireFFmpeg skips the test unless ffmpeg and ffprobe are on PATH.
func RequireFFmpeg(t testing.TB) {
	t.Helper()
	for _, tool := range []string{"ffmpeg", "ffprobe"} {
		RequireTool(t, tool)
	}
}

// RequireTool returns the path of tool on PATH and skips the test if there is none.
func RequireTool(t testing.TB, tool string) string {
	t.Helper()
	p, err := exec.LookPath(tool)
	if err != nil {
		t.Skipf("%s not available: %v", tool, err)
	}
	return p
}

// ffmpegArgs are the options every generator call starts with.
var ffmpegArgs = []string{"-nostdin", "-v", "error", "-y"}

// FFmpeg runs ffmpeg with args (after -nostdin -v error -y) and fails the test if it
// fails. For media a test cannot do without, where a skip would hide a broken setup.
func FFmpeg(t testing.TB, args ...string) {
	t.Helper()
	if out, err := exec.Command("ffmpeg", append(ffmpegArgs[:len(ffmpegArgs):len(ffmpegArgs)], args...)...).CombinedOutput(); err != nil {
		t.Fatalf("ffmpeg %v: %v\n%s", args, err, out)
	}
}

// GenerateMedia runs ffmpeg with args (after -nostdin -v error -y; the output file is the
// last argument) and skips the test if it fails, typically an ffmpeg build without an
// encoder the test needs (libx264, libmp3lame, libwebp).
func GenerateMedia(t testing.TB, args ...string) {
	t.Helper()
	if out, err := exec.Command("ffmpeg", append(ffmpegArgs[:len(ffmpegArgs):len(ffmpegArgs)], args...)...).CombinedOutput(); err != nil {
		t.Skipf("cannot generate test media with ffmpeg %v: %v\n%s", args, err, out)
	}
}

// GenerateMediaAt is GenerateMedia writing to out, whose folder it creates first.
func GenerateMediaAt(t testing.TB, out string, args ...string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(out), 0o755); err != nil {
		t.Fatal(err)
	}
	GenerateMedia(t, append(args[:len(args):len(args)], out)...)
}

// FakeTools puts the scripts (name -> full script, shebang included) into a fresh folder,
// makes that folder the whole PATH and returns it. It skips on Windows, which cannot run
// shell scripts.
func FakeTools(t testing.TB, scripts map[string]string) string {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("fake tools are shell scripts")
	}
	dir := t.TempDir()
	for name, body := range scripts {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv("PATH", dir)
	return dir
}

// FakeVersions makes ffmpeg and ffprobe on PATH answer "-version" with the given versions
// (and fail on everything else).
func FakeVersions(t testing.TB, ffmpeg, ffprobe string) {
	t.Helper()
	scripts := map[string]string{}
	for name, v := range map[string]string{"ffmpeg": ffmpeg, "ffprobe": ffprobe} {
		scripts[name] = "#!/bin/sh\nif [ \"$1\" = -version ]; then echo '" + name + " version " + v +
			" Copyright (c) the FFmpeg developers'; exit 0; fi\nexit 1\n"
	}
	FakeTools(t, scripts)
}

// SkipIfRoot skips the test for root, who ignores file permissions.
func SkipIfRoot(t testing.TB) {
	t.Helper()
	if os.Geteuid() == 0 {
		t.Skip("permissions are not enforced for root")
	}
}

// ChmodUnreadable removes every permission of path for the rest of the test (a folder gets
// 0755 back afterwards, a file 0644) and skips the test if it can still be opened (root).
func ChmodUnreadable(t testing.TB, path string) {
	t.Helper()
	restore := os.FileMode(0o644)
	if st, err := os.Stat(path); err == nil && st.IsDir() {
		restore = 0o755
	}
	if err := os.Chmod(path, 0); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.Chmod(path, restore) })
	if f, err := os.Open(path); err == nil {
		f.Close()
		t.Skip("permissions are not enforced (running as root?)")
	}
}

// CaptureLogs records what the global logrus logger logs for the rest of the test.
func CaptureLogs(t testing.TB) *logtest.Hook {
	t.Helper()
	hook := logtest.NewGlobal()
	t.Cleanup(func() { log.StandardLogger().ReplaceHooks(make(log.LevelHooks)) })
	return hook
}

// CaptureLogsAt is CaptureLogs with the global log level set to level until the test ends.
func CaptureLogsAt(t testing.TB, level log.Level) *logtest.Hook {
	t.Helper()
	hook := CaptureLogs(t)
	old := log.GetLevel()
	log.SetLevel(level)
	t.Cleanup(func() { log.SetLevel(old) })
	return hook
}

// ErrorLogs returns the messages of the error-level entries logged during fn.
func ErrorLogs(t testing.TB, fn func()) []string {
	t.Helper()
	hook := logtest.NewGlobal()
	defer log.StandardLogger().ReplaceHooks(make(log.LevelHooks))
	fn()
	var msgs []string
	for _, e := range hook.AllEntries() {
		if e.Level <= log.ErrorLevel {
			msgs = append(msgs, e.Message)
		}
	}
	return msgs
}

// ContainsPrefix reports whether one of msgs starts with prefix.
func ContainsPrefix(msgs []string, prefix string) bool {
	for _, m := range msgs {
		if strings.HasPrefix(m, prefix) {
			return true
		}
	}
	return false
}

// NotesTxt is the body of a minimal valid chart (BPM 300, GAP 0, 10 notes and E) whose
// notes all sing lyric. Different songs must get different lyrics: organize joins songs
// with the same notes (ContentHash).
func NotesTxt(lyric string) string {
	var b strings.Builder
	b.WriteString("#BPM:300\n#GAP:0\n")
	for i := range 10 {
		fmt.Fprintf(&b, ": %d 2 0 %s\n", i*4, lyric)
	}
	b.WriteString("E\n")
	return b.String()
}
