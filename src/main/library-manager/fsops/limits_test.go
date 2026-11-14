package fsops

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

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
