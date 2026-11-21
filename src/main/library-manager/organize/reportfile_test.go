package organize

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/nkreimeyer/library-manager/config"
	"github.com/nkreimeyer/library-manager/fsops"
	"github.com/nkreimeyer/library-manager/internal/testutil"
)

// "&", "<" and ">" in song names are written literally to the JSON reports (no HTML
// escaping), and each report file is exactly the shared report encoding of its content
// (two-space indent, one trailing newline) next to the matching text report.
func TestReportFilesKeepAmpersandLiteral(t *testing.T) {
	noFFprobe(t)
	const name = "Simon & Garfunkel - I <3 You > Me"
	checkJSON := func(t *testing.T, path string) []byte {
		t.Helper()
		data, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(string(data), `"name": "`+name+`"`) {
			t.Errorf("%s lacks the literal name %q:\n%s", filepath.Base(path), name, data)
		}
		for _, esc := range []string{`\u0026`, `\u003c`, `\u003e`} {
			if strings.Contains(string(data), esc) {
				t.Errorf("%s has the HTML escape %s:\n%s", filepath.Base(path), esc, data)
			}
		}
		return data
	}
	// sameEncoding checks that data is MarshalReport of its own decoding into v.
	sameEncoding := func(t *testing.T, data []byte, v any) {
		t.Helper()
		if err := json.Unmarshal(data, v); err != nil {
			t.Fatal(err)
		}
		again, err := fsops.MarshalReport(v)
		if err != nil {
			t.Fatal(err)
		}
		if string(again) != string(data) {
			t.Errorf("report is not the shared encoding:\n%s\nwant:\n%s", data, again)
		}
	}
	readText := func(t *testing.T, path string) string {
		t.Helper()
		data, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		return string(data)
	}

	t.Run("organize", func(t *testing.T) {
		base := t.TempDir()
		src := filepath.Join(base, "src")
		testutil.WriteFiles(t, src, songFiles("Simon", "Simon & Garfunkel", "I <3 You > Me"))
		cfg := organizeCfg(t, [][]string{{src}}, filepath.Join(base, "lib"), filepath.Join(base, "out"), true)
		if err := Run(cfg, fsops.New(true), Options{}); err != nil {
			t.Fatal(err)
		}
		data := checkJSON(t, cfg.ReportPath(reportJSON))
		var rep planReport
		sameEncoding(t, data, &rep)
		if len(rep.ToLibrary) != 1 || rep.ToLibrary[0].Name != name {
			t.Fatalf("toLibrary = %+v", rep.ToLibrary)
		}
		if got := readText(t, cfg.ReportPath(reportTxt)); got != string(rep.text()) {
			t.Errorf("%s = %q, want %q", reportTxt, got, rep.text())
		}
	})

	t.Run("remove", func(t *testing.T) {
		base := t.TempDir()
		lib := filepath.Join(base, "lib")
		testutil.WriteFiles(t, lib, songFiles("S/Simon & Garfunkel - I 3 You Me", "Simon & Garfunkel", "I <3 You > Me"))
		cfg := prepared(t)(config.NewDedupe(lib, filepath.Join(base, "out"), true))
		if err := Remove(cfg, fsops.New(true), []Pattern{mustPattern(t, PatternArtist, "Simon & Garfunkel")}); err != nil {
			t.Fatal(err)
		}
		data := checkJSON(t, cfg.ReportPath(removeReportJSON))
		var rep removeReport
		sameEncoding(t, data, &rep)
		if len(rep.Removed) != 1 || rep.Removed[0].Name != name {
			t.Fatalf("removed = %+v", rep.Removed)
		}
		if got := readText(t, cfg.ReportPath(removeReportTxt)); got != string(rep.text()) {
			t.Errorf("%s = %q, want %q", removeReportTxt, got, rep.text())
		}
	})
}
