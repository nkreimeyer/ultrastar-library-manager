package fsops

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"unicode/utf8"

	log "github.com/sirupsen/logrus"
	logtest "github.com/sirupsen/logrus/hooks/test"
)

// captureWarnings records the warnings logged during the test.
func captureWarnings(t *testing.T) *logtest.Hook {
	t.Helper()
	hook := logtest.NewGlobal()
	t.Cleanup(func() { log.StandardLogger().ReplaceHooks(make(log.LevelHooks)) })
	return hook
}

// warnings returns the messages of the captured warnings.
func warnings(hook *logtest.Hook) []string {
	var msgs []string
	for _, e := range hook.AllEntries() {
		if e.Level == log.WarnLevel {
			msgs = append(msgs, e.Message)
		}
	}
	return msgs
}

// Real runs log every modification, as dry runs log every planned one: CopyInto, Rename,
// Replace and Remove used to log only in dry-run mode (MoveInto and MoveTo log both).
func TestRealRunLogsEveryModification(t *testing.T) {
	hook := captureWarnings(t)
	dir := t.TempDir()
	song := filepath.Join(dir, "song.mp3")
	writeFile(t, song, "a", 0o644)
	o := New(false)
	backup, err := o.CopyInto(song, filepath.Join(dir, "orig"), "song.mp3")
	if err != nil {
		t.Fatal(err)
	}
	tmp := filepath.Join(dir, ".tmp.mp3")
	writeFile(t, tmp, "b", 0o644)
	if err := o.Replace(tmp, song); err != nil {
		t.Fatal(err)
	}
	renamed := filepath.Join(dir, "renamed.mp3")
	if err := o.Rename(song, renamed); err != nil {
		t.Fatal(err)
	}
	if err := o.Remove(renamed); err != nil {
		t.Fatal(err)
	}
	var infos []string
	for _, e := range hook.AllEntries() {
		if e.Level == log.InfoLevel {
			infos = append(infos, e.Message)
		}
	}
	want := []string{
		"Copied " + song + " -> " + backup,
		"Replaced " + song,
		"Renamed " + song + " -> " + renamed,
		"Removed " + renamed,
	}
	if strings.Join(infos, "\n") != strings.Join(want, "\n") {
		t.Errorf("info logs = %q, want %q", infos, want)
	}
}

// reserveTarget tries the wanted name and the numbers (2) to (10000), and gives up after
// that instead of looping forever.
func TestReserveTargetLimit(t *testing.T) {
	dir := t.TempDir()
	for _, tc := range []struct {
		want, ext string
		isDir     bool
	}{
		{"Song", "", true},
		{"cover.jpg", ".jpg", false},
	} {
		o := New(false)
		want := filepath.Join(dir, tc.want)
		base := strings.TrimSuffix(want, tc.ext)
		o.reserved[want] = true
		for i := 2; i < 10000; i++ {
			o.reserved[fmt.Sprintf("%s (%d)%s", base, i, tc.ext)] = true
		}
		last := fmt.Sprintf("%s (10000)%s", base, tc.ext)
		if got, err := o.reserveTarget(want, tc.isDir); got != last || err != nil {
			t.Errorf("%s with up to (9999) taken: reserveTarget = %q, %v, want %q", tc.want, got, err, last)
		}
		// Now (10000) is reserved too: no free name is left.
		if got, err := o.reserveTarget(want, tc.isDir); err == nil || !strings.Contains(err.Error(), "no free target name") {
			t.Errorf("%s with up to (10000) taken: reserveTarget = %q, %v, want an error", tc.want, got, err)
		}
	}
}

// nextParkNames creates the next n names parkName would hand out in dir.
func nextParkNames(t *testing.T, dir string, n int) {
	t.Helper()
	next := parkSeq.Load()
	for i := uint64(1); i <= uint64(n); i++ {
		p := filepath.Join(dir, fmt.Sprintf(".fsops-move-%d-%d", os.Getpid(), next+i))
		if err := os.WriteFile(p, nil, 0o644); err != nil {
			t.Fatal(err)
		}
	}
}

// parkName skips names that already exist (left over by a crashed run of the same pid).
func TestParkNameSkipsExistingNames(t *testing.T) {
	dir := t.TempDir()
	nextParkNames(t, dir, 2)
	next := parkSeq.Load()
	p, err := parkName(filepath.Join(dir, "Song"))
	want := filepath.Join(dir, fmt.Sprintf(".fsops-move-%d-%d", os.Getpid(), next+3))
	if err != nil || p != want || exists(p) {
		t.Errorf("parkName with the next 2 names taken = %q, %v, want %q", p, err, want)
	}
}

// parkName gives up after 10000 taken names instead of looping forever.
func TestParkNameGivesUp(t *testing.T) {
	dir := t.TempDir()
	nextParkNames(t, dir, 10000)
	if p, err := parkName(filepath.Join(dir, "Song")); err == nil || !strings.Contains(err.Error(), "no free temporary name") {
		t.Errorf("parkName with the next 10000 names taken = %q, %v, want an error", p, err)
	}
}

// A file name close to NAME_MAX (255 bytes) still gets a temp file next to it: the temp
// names carry only the first maxTempBase bytes of it, cut at a rune boundary, so a long
// txt is rewritten and a long video or audio file is processed instead of failing with
// ENAMETOOLONG on every run.
func TestTempNamesForLongFileNames(t *testing.T) {
	dir := t.TempDir()
	// 2 + 82*3 + 4 = 252 bytes; byte 100 is inside an "あ", so the cut must step back.
	stem := "ab" + strings.Repeat("あ", 82)

	txt := filepath.Join(dir, stem+".txt")
	writeFile(t, txt, "#TITLE:old\n", 0o644)
	o := New(false)
	if changed, err := o.WriteFileAtomic(txt, []byte("#TITLE:new\n")); !changed || err != nil {
		t.Fatalf("WriteFileAtomic on a %d-byte name = %v, %v", len(filepath.Base(txt)), changed, err)
	}
	if got := readFile(t, txt); got != "#TITLE:new\n" {
		t.Errorf("rewritten txt = %q", got)
	}

	report := filepath.Join(dir, "reports", stem+".txt")
	if err := o.WriteReport(report, []byte("r\n")); err != nil {
		t.Fatalf("WriteReport on a long name: %v", err)
	}
	if got := readFile(t, report); got != "r\n" {
		t.Errorf("report = %q", got)
	}
	if o.Stats().Failed != 0 {
		t.Errorf("Failed = %d, want 0", o.Stats().Failed)
	}

	video := filepath.Join(dir, stem+".avi")
	writeFile(t, video, "v", 0o640)
	tmp, err := TempFileNear(video, ".mp4")
	if err != nil {
		t.Fatalf("TempFileNear on a %d-byte name: %v", len(filepath.Base(video)), err)
	}
	// Hidden, valid UTF-8, the marker other code looks for, and the extension ffmpeg picks
	// the muxer from.
	if name := filepath.Base(tmp); filepath.Dir(tmp) != dir || !strings.HasPrefix(name, ".ab") ||
		!strings.Contains(name, ".sanitizer-") || !strings.HasSuffix(name, ".mp4") || !utf8.ValidString(name) {
		t.Errorf("TempFileNear name %q, want a hidden .<stem>.sanitizer-*.mp4 name in %s", name, dir)
	}
	if st, err := os.Stat(tmp); err != nil || st.Mode().Perm() != 0o640 {
		t.Errorf("TempFileNear file: %v, %v; want mode 0640", st, err)
	}
	os.Remove(tmp)
	// Only the files themselves are left: no temp file of the writes stayed behind.
	if names := dirNames(t, dir); len(names) != 3 {
		t.Errorf("files left in %s: %q, want the txt, the video and reports/", dir, names)
	}
	if got, want := tempBase(stem+".txt"), "ab"+strings.Repeat("あ", 32); got != want {
		t.Errorf("tempBase cut = %q (%d bytes), want %q", got, len(got), want)
	}
	if short := "Artist - Song.txt"; tempBase(short) != short {
		t.Errorf("tempBase(%q) = %q, want it unchanged", short, tempBase(short))
	}
}
