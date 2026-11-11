package check

import (
	"bytes"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"io/fs"
	"math"
	"math/rand"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"testing"

	"github.com/nkreimeyer/ultrastar-songs-sanitizer/config"
	"github.com/nkreimeyer/ultrastar-songs-sanitizer/fsops"
)

func writeFiles(t *testing.T, root string, files map[string]string) {
	t.Helper()
	for rel, content := range files {
		p := filepath.Join(root, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
}

func newConfig(t *testing.T, lib string, dry bool) config.Config {
	t.Helper()
	cfg, err := config.New(lib, t.TempDir(), dry)
	if err != nil {
		t.Fatal(err)
	}
	if err := cfg.PrepareOutDir(); err != nil {
		t.Fatal(err)
	}
	return cfg
}

func run(t *testing.T, cfg config.Config, opts Options) (*fsops.Ops, report) {
	t.Helper()
	ops := fsops.New(cfg.DryRun)
	if err := Run(cfg, ops, opts); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(cfg.ReportPath(reportJSON))
	if err != nil {
		t.Fatal(err)
	}
	var r report
	if err := json.Unmarshal(data, &r); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(cfg.ReportPath(reportTxt)); err != nil {
		t.Error(err)
	}
	return ops, r
}

// songCodes maps song path to its sorted "severity:code" list.
func songCodes(r report) map[string][]string {
	m := map[string][]string{}
	for _, s := range r.Songs {
		m[s.Path] = codes(s.Findings)
	}
	return m
}

func findSong(t *testing.T, r report, path string) songReport {
	t.Helper()
	for _, s := range r.Songs {
		if s.Path == path {
			return s
		}
	}
	t.Fatalf("song %s not in report: %+v", path, r.Songs)
	return songReport{}
}

func snapshot(t *testing.T, root string) string {
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
		fmt.Fprintf(&sb, "%s %v %v", path, info.Mode(), info.ModTime().UnixNano())
		if d.Type().IsRegular() {
			data, err := os.ReadFile(path)
			if err != nil {
				return err
			}
			fmt.Fprintf(&sb, " %x", sha256.Sum256(data))
		}
		sb.WriteString("\n")
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return sb.String()
}

// withoutFFmpeg hides ffmpeg and ffprobe, so only the txt and file checks run.
func withoutFFmpeg(t *testing.T) { t.Setenv("PATH", t.TempDir()) }

func song(mp3 string, n int) string {
	return "#TITLE:T\n#ARTIST:A\n#MP3:" + mp3 + "\n#BPM:300\n" + notes(n)
}

// heicHeader is the start of a HEIF image (an iPhone export), a format
// utils.GetImageDimensions does not support.
const heicHeader = "\x00\x00\x00\x18ftypheic\x00\x00\x00\x00\x00\x00\x00\x00"

// txtLibrary needs no ffmpeg: five healthy, three broken and four suspicious songs.
func txtLibrary(t *testing.T) string {
	lib := filepath.Join(t.TempDir(), "songs")
	latin := song("M\xe4dchen.mp3", 10)
	writeFiles(t, lib, map[string]string{
		// CRLF and old-Mac CR line endings: the raw CP1252 name must still be found.
		"Cat/LatinCRLF/song.txt":       strings.ReplaceAll(latin, "\n", "\r\n"),
		"Cat/LatinCRLF/M\xe4dchen.mp3": "x",
		"Cat/LatinCR/song.txt":         strings.ReplaceAll(latin, "\n", "\r"),
		"Cat/LatinCR/M\xe4dchen.mp3":   "x",
		// A repeated #MP3: players use the last one whose file exists.
		"Cat/Dup/song.mp3": "x",
		"Cat/Dup/song.txt": "#TITLE:T\n#ARTIST:A\n#MP3:song.mp3\n#BPM:300\n#MP3:old.mp3\n" + notes(10),
		// A cover in a format that cannot be measured is no unreadable image.
		"Cat/Heic/song.txt": "#COVER:c.jpg\n" + song("song.mp3", 10),
		"Cat/Heic/song.mp3": "x",
		"Cat/Heic/c.jpg":    heicHeader,
		// A CP1250 txt read as CP1252 (an encoding Problem): the UTF-8 named "říkám.mp3" is
		// not found under the guessed name. Broken, but never moved.
		"Cat/CP1250/song.txt":  "#TITLE:Za\xbf\xf3\xb3\xe6\n#ARTIST:A\n#MP3:\xf8\xedk\xe1m.mp3\n#BPM:300\n" + notes(10),
		"Cat/CP1250/říkám.mp3": "x",
	})
	writeFiles(t, lib, map[string]string{
		"Good/song.txt":            song("song.mp3", 10),
		"Good/song.mp3":            "x",
		"Cat/Broken/song.txt":      song("missing.mp3", 10),
		"Cat/Broken/other.mp3":     "x",
		"Cat/Few/song.txt":         song("song.mp3", 9),
		"Cat/Few/song.mp3":         "x",
		"NoTxt/song.mp3":           "x",
		"Cat/Images/song.txt":      "#COVER:c.jpg\n#BACKGROUND:b.tif\n" + song("song.mp3", 10),
		"Cat/Images/song.mp3":      "x",
		"Cat/Images/c.jpg":         "not a jpeg",
		"Cat/Images/b.tif":         "not a tiff, but TIFF cannot be checked anyway",
		"Cat/Latin/song.txt":       song("M\xe4dchen.mp3", 10), // CP1252 txt referencing a CP1252 file name
		"Cat/Latin/M\xe4dchen.mp3": "x",
		"Cat/Encoding/song.txt":    "#ENCODING:FOO\n" + song("song.mp3", 10),
		"Cat/Encoding/song.mp3":    "x",
	})
	return lib
}

func TestRunWithoutFFprobe(t *testing.T) {
	withoutFFmpeg(t)
	lib := txtLibrary(t)
	before := snapshot(t, lib)
	cfg := newConfig(t, lib, false)
	ops, r := run(t, cfg, Options{Deep: true})

	want := map[string][]string{
		"Cat/Broken":   {"error:missing-file"},
		"NoTxt":        {"error:no-song-txt"},
		"Cat/CP1250":   {"error:missing-file", "warning:encoding"},
		"Cat/Few":      {"warning:few-notes"},
		"Cat/Images":   {"warning:unreadable-image"},
		"Cat/Encoding": {"warning:encoding"},
		"Cat/Dup":      {"warning:duplicate-tag"},
	}
	if got := songCodes(r); !reflect.DeepEqual(got, want) {
		t.Errorf("findings = %v, want %v", got, want)
	}
	if r.Checked != 12 || r.Broken != 3 || r.Suspicious != 4 || r.MediaChecked || r.Deep {
		t.Errorf("summary = %+v", r)
	}
	if !reflect.DeepEqual(r.Notes, []string{noteNoFFprobe, noteNoFFmpeg}) {
		t.Errorf("notes = %q", r.Notes)
	}
	if s := findSong(t, r, "Cat/Broken"); s.Status != statusBroken || s.Moved || s.Txt != "song.txt" ||
		s.Findings[0].File != "missing.mp3" || s.Findings[0].Line != 3 {
		t.Errorf("Cat/Broken = %+v", s)
	}
	if s := findSong(t, r, "Cat/Few"); s.Status != statusSuspicious {
		t.Errorf("Cat/Few = %+v", s)
	}
	if st := ops.Stats(); st != (fsops.Stats{}) {
		t.Errorf("stats = %+v, want nothing done and nothing failed", st)
	}
	if after := snapshot(t, lib); after != before {
		t.Errorf("check modified the library")
	}
	if _, err := os.Stat(cfg.InvalidDir()); !os.IsNotExist(err) {
		t.Errorf("check created %s without MoveBroken", cfg.InvalidDir())
	}
	txt, _ := os.ReadFile(cfg.ReportPath(reportTxt))
	for _, s := range []string{"12 song(s) checked: 3 broken, 4 suspicious, 5 ok", "note: " + noteNoFFprobe,
		"== Broken songs (errors) ==\n\nCat/Broken\n  error   missing-file (song.txt:3): ", "\nNoTxt\n  error   no-song-txt: "} {
		if !strings.Contains(string(txt), s) {
			t.Errorf("text report lacks %q:\n%s", s, txt)
		}
	}
	if strings.Index(string(txt), "Broken songs") > strings.Index(string(txt), "Suspicious songs") {
		t.Errorf("broken songs must come first:\n%s", txt)
	}
}

func TestRunMoveBroken(t *testing.T) {
	withoutFFmpeg(t)
	lib := txtLibrary(t)

	// Dry run: planned, library and out dir untouched apart from the reports.
	before := snapshot(t, lib)
	dry := newConfig(t, lib, true)
	ops, r := run(t, dry, Options{MoveBroken: true})
	if after := snapshot(t, lib); after != before {
		t.Errorf("dry-run modified the library")
	}
	if _, err := os.Stat(dry.InvalidDir()); !os.IsNotExist(err) {
		t.Errorf("dry-run created %s", dry.InvalidDir())
	}
	if got := ops.Stats().Moved; got != 2 {
		t.Errorf("planned moves = %d, want 2", got)
	}
	if !r.DryRun || !findSong(t, r, "Cat/Broken").Moved || findSong(t, r, "Cat/Few").Moved {
		t.Errorf("dry-run report = %+v", r.Songs)
	}
	if s := findSong(t, r, "Cat/CP1250"); s.Moved || s.NotMoved != notMovedEncoding {
		t.Errorf("song with an encoding problem: %+v, want not moved", s)
	}

	// Real run.
	cfg := newConfig(t, lib, false)
	ops, r = run(t, cfg, Options{MoveBroken: true})
	if st := ops.Stats(); st.Moved != 2 || st.Failed != 0 {
		t.Errorf("stats = %+v", st)
	}
	for _, rel := range []string{"Cat/Broken/song.txt", "Cat/Broken/other.mp3", "NoTxt/song.mp3"} {
		if _, err := os.Stat(filepath.Join(cfg.InvalidDir(), filepath.FromSlash(rel))); err != nil {
			t.Errorf("expected invalid/%s: %v", rel, err)
		}
	}
	for _, rel := range []string{"Cat/Broken", "NoTxt"} {
		if _, err := os.Stat(filepath.Join(lib, rel)); !os.IsNotExist(err) {
			t.Errorf("%s still in the library", rel)
		}
	}
	for _, rel := range []string{"Cat/Few/song.txt", "Cat/CP1250/song.txt"} {
		if _, err := os.Stat(filepath.Join(lib, filepath.FromSlash(rel))); err != nil {
			t.Errorf("song was moved: %v", err)
		}
	}
	if !findSong(t, r, "NoTxt").Moved || r.DryRun {
		t.Errorf("report = %+v", r)
	}

	// The next run finds nothing to move.
	ops, r = run(t, newConfig(t, lib, false), Options{MoveBroken: true})
	if ops.Stats().Moved != 0 || r.Broken != 1 {
		t.Errorf("second run: stats %+v, report %+v", ops.Stats(), r)
	}
}

func TestRunConcurrentDeterministic(t *testing.T) {
	withoutFFmpeg(t)
	old := workers
	workers = 8
	t.Cleanup(func() { workers = old })

	lib := filepath.Join(t.TempDir(), "songs")
	files := map[string]string{}
	for i := range 40 {
		dir := fmt.Sprintf("Cat%d/Song %02d", i%3, i)
		files[dir+"/song.txt"] = song("song.mp3", 5+i%10)
		if i%4 != 0 {
			files[dir+"/song.mp3"] = "x"
		}
	}
	writeFiles(t, lib, files)
	cfg := newConfig(t, lib, false)
	_, r := run(t, cfg, Options{})
	first := [2][]byte{}
	first[0], _ = os.ReadFile(cfg.ReportPath(reportJSON))
	first[1], _ = os.ReadFile(cfg.ReportPath(reportTxt))
	if r.Checked != 40 {
		t.Errorf("checked = %d", r.Checked)
	}
	paths := []string{}
	for _, s := range r.Songs {
		paths = append(paths, s.Path)
	}
	if !sort.StringsAreSorted(paths) {
		t.Errorf("songs not sorted: %v", paths)
	}
	for range 3 {
		run(t, cfg, Options{})
		for i, name := range []string{reportJSON, reportTxt} {
			if again, _ := os.ReadFile(cfg.ReportPath(name)); !bytes.Equal(again, first[i]) {
				t.Errorf("%s differs between runs", name)
			}
		}
	}
}

// --- ffmpeg tests -------------------------------------------------------------------

func requireFFmpeg(t *testing.T) {
	t.Helper()
	for _, tool := range []string{"ffmpeg", "ffprobe"} {
		if _, err := exec.LookPath(tool); err != nil {
			t.Skipf("%s not found", tool)
		}
	}
}

// lavfi runs ffmpeg to generate a test file.
func lavfi(t *testing.T, out string, args ...string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(out), 0o755); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command("ffmpeg", append(append([]string{"-v", "error", "-y"}, args...), out)...)
	if b, err := cmd.CombinedOutput(); err != nil {
		// Typically an ffmpeg build without libx264 or libmp3lame.
		t.Skipf("cannot generate test media with ffmpeg %v: %v\n%s", args, err, b)
	}
}

func sine(t *testing.T, out string, sec float64) {
	lavfi(t, out, "-f", "lavfi", "-i", fmt.Sprintf("sine=d=%g", sec))
}

func testVideo(t *testing.T, out string, sec float64, withAudio bool) {
	args := []string{"-f", "lavfi", "-i", fmt.Sprintf("testsrc=d=%g:s=64x64:r=10", sec)}
	if withAudio {
		args = append(args, "-f", "lavfi", "-i", fmt.Sprintf("sine=d=%g", sec), "-c:a", "aac")
	}
	lavfi(t, out, append(args, "-c:v", "libx264", "-pix_fmt", "yuv420p")...)
}

func garbage(n int) string {
	b := make([]byte, n)
	rand.New(rand.NewSource(1)).Read(b)
	return string(b)
}

func TestRunMedia(t *testing.T) {
	requireFFmpeg(t)
	lib := filepath.Join(t.TempDir(), "songs")
	// 10 notes end at 1.9 s.
	writeFiles(t, lib, map[string]string{
		"Healthy/song.txt":      "#VIDEO:v.mp4\n#COVER:c.jpg\n" + song("a.mp3", 10),
		"Past/song.txt":         song("a.ogg", 10),
		"Garbage/song.txt":      song("a.mp3", 10),
		"Garbage/a.mp3":         garbage(4000),
		"VideoOnly/song.txt":    "#VIDEO:v.mp4\n" + song("v.mp4", 10),
		"VideoNoAudio/song.txt": "#VIDEO:v.mp4\n" + song("v.mp4", 10),
		"ShortVideo/song.txt":   "#VIDEO:v.mp4\n" + song("a.mp3", 10),
	})
	sine(t, filepath.Join(lib, "Healthy", "a.mp3"), 3)
	testVideo(t, filepath.Join(lib, "Healthy", "v.mp4"), 3, false)
	lavfi(t, filepath.Join(lib, "Healthy", "c.jpg"), "-f", "lavfi", "-i", "color=c=red:s=32x32", "-frames:v", "1")
	sine(t, filepath.Join(lib, "Past", "a.ogg"), 0.5)
	testVideo(t, filepath.Join(lib, "VideoOnly", "v.mp4"), 3, true)
	testVideo(t, filepath.Join(lib, "VideoNoAudio", "v.mp4"), 3, false)
	sine(t, filepath.Join(lib, "ShortVideo", "a.mp3"), 25)
	testVideo(t, filepath.Join(lib, "ShortVideo", "v.mp4"), 2, false)

	cfg := newConfig(t, lib, false)
	before := snapshot(t, lib)
	ops, r := run(t, cfg, Options{})
	want := map[string][]string{
		"Past":         {"error:notes-past-audio"},
		"Garbage":      {"error:unreadable-media"},
		"VideoNoAudio": {"error:no-audio-stream"},
		"ShortVideo":   {"warning:video-too-short"},
	}
	if got := songCodes(r); !reflect.DeepEqual(got, want) {
		t.Errorf("findings = %v, want %v\n%+v", got, want, r.Songs)
	}
	if !r.MediaChecked || r.Checked != 6 || len(r.Notes) != 0 {
		t.Errorf("summary = %+v", r)
	}
	if f := findSong(t, r, "Past").Findings[0]; !strings.Contains(f.Message, "0:01.9") || !strings.Contains(f.Message, "0:00.5") {
		t.Errorf("notes-past-audio message = %q, want both times", f.Message)
	}
	if f := findSong(t, r, "Garbage").Findings[0]; strings.Contains(f.Message, " @ 0x") || strings.Contains(f.Message, lib) {
		t.Errorf("tool message not cleaned: %q", f.Message)
	}
	if ops.Stats().Failed != 0 {
		t.Errorf("stats = %+v", ops.Stats())
	}

	// Same bytes on the next run (ffprobe messages contain object addresses).
	first, _ := os.ReadFile(cfg.ReportPath(reportJSON))
	run(t, cfg, Options{})
	if again, _ := os.ReadFile(cfg.ReportPath(reportJSON)); !bytes.Equal(first, again) {
		t.Errorf("report differs between runs:\n%s\n---\n%s", first, again)
	}

	// MoveBroken in dry-run plans the three broken songs and changes nothing.
	dry := newConfig(t, lib, true)
	ops, _ = run(t, dry, Options{MoveBroken: true})
	if ops.Stats().Moved != 3 {
		t.Errorf("planned moves = %d, want 3", ops.Stats().Moved)
	}
	if after := snapshot(t, lib); after != before {
		t.Errorf("library modified")
	}
	if _, err := os.Stat(dry.InvalidDir()); !os.IsNotExist(err) {
		t.Errorf("dry-run created %s", dry.InvalidDir())
	}
}

func TestRunDeep(t *testing.T) {
	requireFFmpeg(t)
	lib := filepath.Join(t.TempDir(), "songs")
	writeFiles(t, lib, map[string]string{"Song/song.txt": song("a.flac", 10)})
	full := filepath.Join(t.TempDir(), "full.flac")
	sine(t, full, 5)
	data, err := os.ReadFile(full)
	if err != nil {
		t.Fatal(err)
	}
	// A truncated flac: the header still claims 5 s, decoding fails in the middle.
	writeFiles(t, lib, map[string]string{"Song/a.flac": string(data[:len(data)/2])})

	cfg := newConfig(t, lib, false)
	if _, r := run(t, cfg, Options{}); len(r.Songs) != 0 || r.Deep {
		t.Errorf("without -deep: %+v", r)
	}
	_, r := run(t, cfg, Options{Deep: true})
	if got, want := songCodes(r), map[string][]string{"Song": {"warning:decode-warning"}}; !reflect.DeepEqual(got, want) {
		t.Errorf("findings = %v, want %v\n%+v", got, want, r.Songs)
	}
	if !r.Deep {
		t.Errorf("report deep = false")
	}
}

// chmodUnreadable removes all permissions from path and skips the test if the file can
// still be read (running as root).
func chmodUnreadable(t *testing.T, path string) {
	t.Helper()
	if err := os.Chmod(path, 0); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.Chmod(path, 0o644) })
	if f, err := os.Open(path); err == nil {
		f.Close()
		t.Skip("permissions are not enforced (running as root?)")
	}
}

// A referenced file without read permission is an operation failure of the run, not a
// broken song: no finding, not moved, and counted once.
func TestRunUnreadableFile(t *testing.T) {
	withoutFFmpeg(t)
	lib := filepath.Join(t.TempDir(), "songs")
	writeFiles(t, lib, map[string]string{
		"Song/song.txt": "#COVER:c.png\n" + song("a.mp3", 10),
		"Song/a.mp3":    "x",
		"Song/c.png":    "\x89PNG\r\n\x1a\n not a real png",
	})
	chmodUnreadable(t, filepath.Join(lib, "Song", "c.png"))
	ops, r := run(t, newConfig(t, lib, false), Options{MoveBroken: true})
	if len(r.Songs) != 0 || r.Checked != 1 {
		t.Errorf("report = %+v, want no findings", r)
	}
	if st := ops.Stats(); st.Failed != 1 || st.Moved != 0 {
		t.Errorf("stats = %+v, want 1 failure and nothing moved", st)
	}
}

func TestRunUnreadableMedia(t *testing.T) {
	requireFFmpeg(t)
	lib := filepath.Join(t.TempDir(), "songs")
	writeFiles(t, lib, map[string]string{"Song/song.txt": song("a.mp3", 10)})
	sine(t, filepath.Join(lib, "Song", "a.mp3"), 3)
	chmodUnreadable(t, filepath.Join(lib, "Song", "a.mp3"))
	ops, r := run(t, newConfig(t, lib, false), Options{MoveBroken: true, Deep: true})
	if len(r.Songs) != 0 {
		t.Errorf("findings = %+v, want none", r.Songs)
	}
	if st := ops.Stats(); st.Failed != 1 || st.Moved != 0 {
		t.Errorf("stats = %+v, want 1 failure and nothing moved", st)
	}
	if _, err := os.Lstat(filepath.Join(lib, "Song", "song.txt")); err != nil {
		t.Errorf("song moved: %v", err)
	}
}

// A VBR mp3 without Xing header that starts loud: ffprobe estimates its 60 s as about
// half of that. The real length is measured, so notes up to 50 s are fine.
func TestRunEstimatedDuration(t *testing.T) {
	requireFFmpeg(t)
	lib := filepath.Join(t.TempDir(), "songs")
	mp3 := filepath.Join(lib, "Song", "a.mp3")
	lavfi(t, mp3, "-f", "lavfi", "-i", "anoisesrc=d=20:a=0.5", "-f", "lavfi", "-i", "anullsrc=d=40",
		"-filter_complex", "[0][1]concat=n=2:v=0:a=1", "-c:a", "libmp3lame", "-q:a", "2", "-write_xing", "0")
	if out, _ := exec.Command("ffprobe", "-v", "warning", "-i", mp3).CombinedOutput(); !strings.Contains(string(out), estimatedMsg) {
		t.Skipf("this ffprobe does not estimate the duration: %s", out)
	}
	// BPM 300: beat 990 + 10 ends at 50 s.
	writeFiles(t, lib, map[string]string{"Song/song.txt": "#TITLE:T\n#ARTIST:A\n#MP3:a.mp3\n#BPM:300\n" + padNoEnd(0) + ": 990 10 0 x\nE\n"})
	info, err := probeMedia(mp3)
	if err != nil || info.DurationEstimated || math.Abs(info.DurationSec-60) > 0.5 {
		t.Errorf("probeMedia = %+v, %v, want a measured 60 s", info, err)
	}
	if _, r := run(t, newConfig(t, lib, false), Options{}); len(r.Songs) != 0 {
		t.Errorf("findings = %+v, want none", r.Songs)
	}
}

func TestParseProgressDuration(t *testing.T) {
	out := "out_time_us=1000000\nprogress=continue\nout_time_us=N/A\nout_time_us=60024000\nprogress=end\n"
	if sec, ok := parseProgressDuration(out); !ok || sec != 60.024 {
		t.Errorf("parseProgressDuration = %v, %v, want 60.024", sec, ok)
	}
	if _, ok := parseProgressDuration("out_time_us=N/A\nprogress=end\n"); ok {
		t.Error("no time: want ok = false")
	}
}

func TestCappedBuffer(t *testing.T) {
	b := &cappedBuffer{max: 5}
	for _, p := range []string{"abc", "defg", "hij"} {
		if n, err := b.Write([]byte(p)); n != len(p) || err != nil {
			t.Errorf("Write(%q) = %d, %v", p, n, err)
		}
	}
	if b.String() != "abcde" {
		t.Errorf("buffer = %q, want %q", b.String(), "abcde")
	}
}

// ffmpeg's null muxer complains about AVI timestamps (seen on an XVID file with B-frames);
// that is no decode problem and must not become a decode-warning. Real decoder messages stay.
func TestDropNullMuxerLines(t *testing.T) {
	out := "[null @ 0x55d0c0a1b2c0] Application provided invalid, non monotonically increasing dts to muxer in stream 0: 5 >= 5\n" +
		"[mp3float @ 0x55d0c0a1b2c0] Header missing\n" +
		"  [null @ 0x1] Application provided invalid, non monotonically increasing dts to muxer in stream 0: 7 >= 7\n"
	if got, want := toolMessage(dropNullMuxerLines(out), "/x/a.mp3", 3), "[mp3float] Header missing"; got != want {
		t.Errorf("message = %q, want %q", got, want)
	}
	if got := toolMessage(dropNullMuxerLines(strings.Repeat("[null @ 0x2] Application provided invalid dts\n", 3)), "/x/a.avi", 3); got != "" {
		t.Errorf("null muxer output alone must give no message, got %q", got)
	}
}

// Report messages contain ">" (e.g. "ends at 0:19.7 > ..."); the JSON must not escape it.
func TestReportJSONIsNotHTMLEscaped(t *testing.T) {
	r := report{Songs: []songReport{{Path: "A", Status: "broken", Findings: []Finding{{Severity: SevError, Code: "x", Message: "a > b & <c>"}}}}}
	data, err := r.json()
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), "a > b & <c>") || !strings.HasSuffix(string(data), "}\n") {
		t.Errorf("json = %s", data)
	}
}

// TestRunMissingNameTagsFixTxt: in a dry run with fixtxt, a song whose missing #ARTIST
// fixtxt would fill from the folder name is only suspicious and not planned to be moved. In
// a real run (or without fixtxt) it stays broken. fixtxt never fills a txt with a decode
// problem or in a folder with a non-UTF-8 media file name, so those stay errors.
func TestRunMissingNameTagsFixTxt(t *testing.T) {
	withoutFFmpeg(t)
	lib := filepath.Join(t.TempDir(), "songs")
	noArtist := "#TITLE:Hypa Hypa\n#MP3:song.mp3\n#BPM:300\n" + notes(10)
	writeFiles(t, lib, map[string]string{
		"Electric Callboy - Hypa Hypa/song.txt":  noArtist,
		"Electric Callboy - Hypa Hypa/song.mp3":  "x",
		"Hypa Hypa - Electric Callboy/song.txt":  noArtist,
		"Hypa Hypa - Electric Callboy/song.mp3":  "x",
		"Undecodable - Hypa Hypa/song.txt":       "#TITLE:Hypa Hypa\n#MP3:song.mp3\n#BPM:300\n#GENRE:\xe4\x81\n" + notes(10),
		"Undecodable - Hypa Hypa/song.mp3":       "x",
		"Latin1 Name - Hypa Hypa/song.txt":       noArtist,
		"Latin1 Name - Hypa Hypa/song.mp3":       "x",
		"Latin1 Name - Hypa Hypa/M\xe4dchen.jpg": "x",
	})
	fillable := "Electric Callboy - Hypa Hypa"
	wantMsg := "#ARTIST is missing or empty; fixtxt fills it from the folder name: Electric Callboy"
	severity := func(t *testing.T, r report, path string) Severity {
		t.Helper()
		for _, f := range findSong(t, r, path).Findings {
			if f.Code == codeMissingTag {
				return f.Severity
			}
		}
		t.Fatalf("%s has no missing-tag finding", path)
		return ""
	}

	for _, tc := range []struct {
		name         string
		dry, fixTxt  bool
		wantFillable Severity
	}{
		{"dry run with fixtxt", true, true, SevWarning},
		{"dry run without fixtxt", true, false, SevError},
		{"real run with fixtxt", false, true, SevError},
	} {
		t.Run(tc.name, func(t *testing.T) {
			before := snapshot(t, lib)
			ops, r := run(t, newConfig(t, lib, tc.dry), Options{MoveBroken: tc.dry, FixTxt: tc.fixTxt})
			if got := severity(t, r, fillable); got != tc.wantFillable {
				t.Errorf("fillable song: severity %s, want %s", got, tc.wantFillable)
			}
			s := findSong(t, r, fillable)
			if s.Findings[0].Message != wantMsg {
				t.Errorf("message = %q, want %q", s.Findings[0].Message, wantMsg)
			}
			if wantMoved := tc.dry && tc.wantFillable == SevError; s.Moved != wantMoved {
				t.Errorf("fillable song moved = %v, want %v", s.Moved, wantMoved)
			}
			for _, p := range []string{"Hypa Hypa - Electric Callboy", "Undecodable - Hypa Hypa", "Latin1 Name - Hypa Hypa"} {
				if got := severity(t, r, p); got != SevError {
					t.Errorf("%s: severity %s, want error", p, got)
				}
				for _, f := range findSong(t, r, p).Findings {
					if strings.Contains(f.Message, "fixtxt fills") {
						t.Errorf("%s: unexpected fill hint %q", p, f.Message)
					}
				}
			}
			if tc.dry {
				if after := snapshot(t, lib); after != before {
					t.Error("dry run modified the library")
				}
				if ops.Stats().Moved == 0 {
					t.Error("dry run planned no moves")
				}
			}
		})
	}
}
