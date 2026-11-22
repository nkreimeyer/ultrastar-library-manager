package fsops

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/nkreimeyer/library-manager/internal/testutil"
	log "github.com/sirupsen/logrus"
)

func logged(msgs []*log.Entry, level log.Level, prefix string) bool {
	for _, e := range msgs {
		if e.Level == level && strings.HasPrefix(e.Message, prefix) {
			return true
		}
	}
	return false
}

// A stale report temp that is removed is logged at Info; one that cannot be removed is
// logged as a warning and not as removed.
func TestRemoveReportTempsLogsOutcome(t *testing.T) {
	testutil.SkipIfRoot(t)
	hook := testutil.CaptureLogsAt(t, log.DebugLevel)
	dir := t.TempDir()
	temp := filepath.Join(dir, ".report.json.123.tmp")
	writeFile(t, temp, "x", 0o644)
	removeReportTemps(dir, time.Now().Add(time.Hour))
	if testutil.Exists(temp) {
		t.Fatal("stale temp was not removed")
	}
	if !logged(hook.AllEntries(), log.InfoLevel, "Removed the leftover temp file") {
		t.Errorf("no Info log of the removal: %v", hook.AllEntries())
	}

	hook.Reset()
	writeFile(t, temp, "x", 0o644)
	if err := os.Chmod(dir, 0o555); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.Chmod(dir, 0o755) })
	removeReportTemps(dir, time.Now().Add(time.Hour))
	if !testutil.Exists(temp) {
		t.Skip("permissions are not enforced")
	}
	if !logged(hook.AllEntries(), log.WarnLevel, "Cannot remove the leftover temp file") {
		t.Errorf("no warning for the failed removal: %v", hook.AllEntries())
	}
	if logged(hook.AllEntries(), log.InfoLevel, "Removed the leftover temp file") {
		t.Errorf("a failed removal was logged as removed")
	}
}

// The same for the cache temps.
func TestRemoveCacheTempsLogsOutcome(t *testing.T) {
	testutil.SkipIfRoot(t)
	hook := testutil.CaptureLogsAt(t, log.DebugLevel)
	dir := t.TempDir()
	cache := filepath.Join(dir, "cache.gz")
	temp := filepath.Join(dir, ".cache.gz.123.tmp")
	writeFile(t, temp, "x", 0o644)
	removeCacheTemps(cache, time.Time{})
	if testutil.Exists(temp) {
		t.Fatal("temp was not removed")
	}
	if !logged(hook.AllEntries(), log.DebugLevel, "Removed the leftover cache temp file") {
		t.Errorf("no Debug log of the removal: %v", hook.AllEntries())
	}

	hook.Reset()
	writeFile(t, temp, "x", 0o644)
	if err := os.Chmod(dir, 0o555); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.Chmod(dir, 0o755) })
	removeCacheTemps(cache, time.Time{})
	if !testutil.Exists(temp) {
		t.Skip("permissions are not enforced")
	}
	if !logged(hook.AllEntries(), log.WarnLevel, "Cannot remove the leftover cache temp file") {
		t.Errorf("no warning for the failed removal: %v", hook.AllEntries())
	}
	if logged(hook.AllEntries(), log.DebugLevel, "Removed the leftover cache temp file") {
		t.Errorf("a failed removal was logged as removed")
	}
}

// A name that is exactly prefix+suffix (no unique part) is not a cache temp file, so it
// keeps the folder from counting as empty.
func TestRemoveEmptyDirWithCacheBareAffixesIsNotCache(t *testing.T) {
	const name = "cache.gz"
	prefix, suffix := cacheTempAffixes(filepath.Join("x", name))
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, prefix+suffix), "x", 0o644)
	removed, err := New(false).RemoveEmptyDirWithCache(dir, name)
	if err != nil || removed || !testutil.Exists(dir) {
		t.Errorf("RemoveEmptyDirWithCache = %v, %v (dir exists %v), want kept", removed, err, testutil.Exists(dir))
	}
}

// tempBase: a name of exactly maxTempBase bytes is kept; a longer name made only of
// continuation bytes is cut to nothing without indexing out of range.
func TestTempBaseBoundaries(t *testing.T) {
	exact := strings.Repeat("a", maxTempBase)
	if got := tempBase(exact); got != exact {
		t.Errorf("tempBase of %d bytes = %d bytes, want unchanged", maxTempBase, len(got))
	}
	if got := tempBase(exact + "a"); got != exact {
		t.Errorf("tempBase of %d bytes = %d bytes, want %d", maxTempBase+1, len(got), maxTempBase)
	}
	if got := tempBase(strings.Repeat("\x80", 150)); got != "" {
		t.Errorf("tempBase of continuation bytes = %q, want empty", got)
	}
}

// syncFile fails for a file that cannot be opened and succeeds for one that can.
func TestSyncFileOpenError(t *testing.T) {
	p := filepath.Join(t.TempDir(), "f")
	if err := syncFile(p); err == nil {
		t.Error("syncFile of a missing file returned nil")
	}
	writeFile(t, p, "x", 0o644)
	if err := syncFile(p); err != nil {
		t.Errorf("syncFile = %v", err)
	}
}
