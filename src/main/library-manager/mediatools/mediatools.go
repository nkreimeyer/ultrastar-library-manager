// Package mediatools finds ffmpeg and ffprobe and checks that their version is new enough,
// probes media files with one ffprobe call (Probe) and caps the tool output kept in memory
// (OutputCap).
package mediatools

import (
	"bufio"
	"bytes"
	"context"
	"fmt"
	"os/exec"
	"regexp"
	"strconv"
	"time"
)

// Names of the tools the media modules need, in the order Detect reports them.
var Names = []string{"ffmpeg", "ffprobe"}

// Minimum release the media modules are tested with.
const (
	MinMajor = 5
	MinMinor = 1
)

// minGitDate is the date of the 5.1 release: date-versioned git builds from then on have
// everything 5.1 has.
const minGitDate = "2022-07-22"

// Timeout for one "-version" call.
var Timeout = 5 * time.Second

// Tool describes one tool on PATH.
type Tool struct {
	Name    string `json:"name"`
	Path    string `json:"path,omitempty"`    // empty if not found
	Version string `json:"version,omitempty"` // as printed, e.g. "7.1.1-1ubuntu1" or "N-118000-g1234abcd"
	Found   bool   `json:"found"`
	// Known is true if the version could be read and compared with the minimum.
	Known bool `json:"known"`
	// OK is true if the tool is found and not known to be too old.
	OK      bool   `json:"ok"`
	Problem string `json:"problem,omitempty"`
}

// Detect looks up every tool in Names and runs "<tool> -version".
func Detect(ctx context.Context) []Tool {
	tools := make([]Tool, len(Names))
	for i, name := range Names {
		tools[i] = detect(ctx, name)
	}
	return tools
}

// AllOK reports whether every tool is OK.
func AllOK(tools []Tool) bool {
	for _, t := range tools {
		if !t.OK {
			return false
		}
	}
	return true
}

func detect(ctx context.Context, name string) Tool {
	t := Tool{Name: name}
	path, err := exec.LookPath(name)
	if err != nil {
		t.Problem = name + " was not found on PATH"
		return t
	}
	t.Path, t.Found = path, true
	ctx, cancel := context.WithTimeout(ctx, Timeout)
	defer cancel()
	out, err := exec.CommandContext(ctx, path, "-version").Output()
	if err != nil {
		t.Problem = fmt.Sprintf("%s -version failed: %v", name, err)
		return t
	}
	v, ok := ParseVersion(name, out)
	t.Version = v.Raw
	switch {
	case !ok:
		t.OK = true // found and runs; we just cannot tell the version
		t.Problem = "the version could not be read"
	case v.TooOld():
		t.Known = true
		t.Problem = fmt.Sprintf("version %s is too old, %d.%d or newer is needed", v.Raw, MinMajor, MinMinor)
	default:
		t.Known, t.OK = true, true
	}
	return t
}

// Version is a parsed "-version" line.
type Version struct {
	Raw          string
	Major, Minor int    // 0 for git builds
	Git          bool   // a build from the development branch ("N-…" or a date)
	Date         string // "YYYY-MM-DD" of a date-versioned git build, else ""
}

// TooOld reports whether the version is known to be older than MinMajor.MinMinor.
func (v Version) TooOld() bool {
	if v.Git {
		return v.Date != "" && v.Date < minGitDate
	}
	return v.Major < MinMajor || v.Major == MinMajor && v.Minor < MinMinor
}

var (
	releaseRe = regexp.MustCompile(`^n?(\d+)\.(\d+)`)
	gitRe     = regexp.MustCompile(`^N-\d+`)
	dateRe    = regexp.MustCompile(`^(\d{4}-\d{2}-\d{2})-git`)
)

// ParseVersion reads the first line of "<name> -version": "<name> version <v> Copyright …".
// Known forms of <v>: "7.1.1", "n6.0", "7.1-full_build-www.gyan.dev", "N-118000-g1234abcd"
// and "2024-03-07-git-97beb63a66-full_build-www.gyan.dev".
func ParseVersion(name string, out []byte) (Version, bool) {
	line, _, _ := bufio.NewReader(bytes.NewReader(out)).ReadLine()
	fields := bytes.Fields(line)
	if len(fields) < 3 || string(fields[0]) != name || string(fields[1]) != "version" {
		return Version{}, false
	}
	v := Version{Raw: string(fields[2])}
	if m := releaseRe.FindStringSubmatch(v.Raw); m != nil {
		v.Major, _ = strconv.Atoi(m[1])
		v.Minor, _ = strconv.Atoi(m[2])
		return v, true
	}
	if gitRe.MatchString(v.Raw) {
		v.Git = true
		return v, true
	}
	if m := dateRe.FindStringSubmatch(v.Raw); m != nil {
		v.Git, v.Date = true, m[1]
		return v, true
	}
	return v, false
}

// InstallHint returns a short install instruction for goos (runtime.GOOS).
func InstallHint(goos string) string {
	switch goos {
	case "windows":
		return "Install it with \"winget install ffmpeg\" in a terminal, or download it from ffmpeg.org and add its bin folder to PATH."
	case "darwin":
		return "Install it with \"brew install ffmpeg\" (Homebrew), or download it from ffmpeg.org."
	default:
		return "Install the ffmpeg package of your distribution, e.g. \"sudo apt install ffmpeg\" or \"sudo pacman -S ffmpeg\"."
	}
}
