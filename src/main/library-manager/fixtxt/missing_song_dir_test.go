package fixtxt

import (
	"path/filepath"
	"testing"

	"github.com/nkreimeyer/library-manager/config"
	"github.com/nkreimeyer/library-manager/fsops"
	"github.com/nkreimeyer/library-manager/internal/testutil"
)

// A library that is a file or does not exist is a module error; nothing is created or written.
func TestRunFailsWithoutSongDir(t *testing.T) {
	base := t.TempDir()
	file := filepath.Join(base, "file")
	testutil.WriteFile(t, file, "x")
	for name, dir := range map[string]string{"file": file, "missing": filepath.Join(base, "missing")} {
		t.Run(name, func(t *testing.T) {
			out := filepath.Join(base, "out-"+name)
			ops := fsops.New(false)
			if err := Run(config.Config{SongDir: dir, OutDir: out}, ops); err == nil {
				t.Error("Run succeeded")
			}
			if st := ops.Stats(); st.Written+st.Moved != 0 {
				t.Errorf("stats = %+v", st)
			}
			if testutil.Exists(out) {
				t.Error("the output folder was created")
			}
		})
	}
	if testutil.ReadFile(t, file) != "x" {
		t.Error("the file changed")
	}
}
