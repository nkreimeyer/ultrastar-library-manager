package check

import (
	"fmt"
	"sort"
	"strings"
)

// Report file names in the output dir.
const (
	reportJSON = "check_report.json"
	reportTxt  = "check_report.txt"
)

// Song statuses.
const (
	statusBroken     = "broken"
	statusSuspicious = "suspicious"
)

// report is the content of check_report.json. It is fully regenerated on every run and
// deterministic: no timestamps, songs sorted by path, findings sorted (see sortFindings).
type report struct {
	Checked      int          `json:"checked"`
	Broken       int          `json:"broken"`
	Suspicious   int          `json:"suspicious"`
	MediaChecked bool         `json:"mediaChecked"`
	Deep         bool         `json:"deep"`
	DryRun       bool         `json:"dryRun"`          // "moved" means "would be moved"
	Notes        []string     `json:"notes,omitempty"` // checks that could not run
	Songs        []songReport `json:"songs"`
}

type songReport struct {
	Path     string    `json:"path"` // relative to the song dir, with "/"
	Txt      string    `json:"txt,omitempty"`
	Status   string    `json:"status"`
	Moved    bool      `json:"moved"`
	NotMoved string    `json:"notMoved,omitempty"` // why MoveBroken left a broken song in place
	Findings []Finding `json:"findings"`
}

// sortResults sorts the results by relative path.
func sortResults(results []songResult) {
	sort.Slice(results, func(i, j int) bool { return results[i].rel < results[j].rel })
}

func buildReport(checked int, results []songResult, notes []string, media, deep, dryRun bool) report {
	r := report{Checked: checked, MediaChecked: media, Deep: deep, DryRun: dryRun, Notes: notes, Songs: []songReport{}}
	for _, res := range results {
		status := statusSuspicious
		if isBroken(res.findings) {
			status = statusBroken
			r.Broken++
		} else {
			r.Suspicious++
		}
		r.Songs = append(r.Songs, songReport{Path: res.rel, Txt: res.txt, Status: status, Moved: res.moved, NotMoved: res.notMoved, Findings: res.findings})
	}
	return r
}

// text renders the readable report: a summary, then the broken songs, then the
// suspicious ones, each with its findings indented.
func (r report) text() []byte {
	var sb strings.Builder
	yesNo := map[bool]string{true: "yes", false: "no"}
	fmt.Fprintf(&sb, "%d song(s) checked: %d broken, %d suspicious, %d ok (media checked: %s, deep: %s)\n",
		r.Checked, r.Broken, r.Suspicious, r.Checked-r.Broken-r.Suspicious, yesNo[r.MediaChecked], yesNo[r.Deep])
	for _, n := range r.Notes {
		fmt.Fprintf(&sb, "note: %s\n", n)
	}
	moved := "moved to invalid/"
	if r.DryRun {
		moved = "would be moved to invalid/"
	}
	for _, sec := range []struct{ status, title string }{
		{statusBroken, "Broken songs (errors)"}, {statusSuspicious, "Suspicious songs (warnings only)"},
	} {
		first := true
		for _, s := range r.Songs {
			if s.Status != sec.status {
				continue
			}
			if first {
				fmt.Fprintf(&sb, "\n== %s ==\n", sec.title)
				first = false
			}
			sb.WriteString("\n" + s.Path)
			if s.Moved {
				sb.WriteString("  [" + moved + "]")
			}
			if s.NotMoved != "" {
				sb.WriteString("  [not moved: " + s.NotMoved + "]")
			}
			sb.WriteString("\n")
			for _, f := range s.Findings {
				loc := ""
				switch {
				case f.Line > 0 && s.Txt != "":
					loc = fmt.Sprintf(" (%s:%d)", s.Txt, f.Line)
				case f.Line > 0:
					loc = fmt.Sprintf(" (line %d)", f.Line)
				}
				fmt.Fprintf(&sb, "  %-7s %s%s: %s\n", f.Severity, f.Code, loc, f.Message)
			}
		}
	}
	return []byte(sb.String())
}
