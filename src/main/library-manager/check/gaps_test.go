package check

import (
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/nkreimeyer/library-manager/fsops"
)

// fakeTools makes PATH a fresh folder holding only the given shell scripts (name ->
// body after the shebang). It returns the folder.
func fakeTools(t *testing.T, scripts map[string]string) string {
	t.Helper()
	dir := t.TempDir()
	for name, body := range scripts {
		if err := os.WriteFile(filepath.Join(dir, name), []byte("#!/bin/sh\n"+body), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv("PATH", dir)
	return dir
}

// brokenTool is an executable that cannot be started (its interpreter does not exist):
// exec.LookPath finds it, running it fails with a start error, not an exit status.
const brokenTool = "#!/nonexistent/interpreter\n"

func writeBrokenTool(t *testing.T, dir, name string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, name), []byte(brokenTool), 0o755); err != nil {
		t.Fatal(err)
	}
}

const (
	audioJSON = `{"streams":[{"codec_type":"audio","bit_rate":"128000"}],"format":{"duration":"2.0"}}`
	videoJSON = `{"streams":[{"codec_type":"video","width":64,"height":48}],"format":{"duration":"2.0"}}`
)

// printJSON returns a script body that prints json to stdout, optionally a line to stderr.
func printJSON(json, stderr string) string {
	body := "printf '%s\\n' '" + json + "'\n"
	if stderr != "" {
		body += "printf '%s\\n' '" + stderr + "' >&2\n"
	}
	return body
}

func TestProbeMediaFakeTools(t *testing.T) {
	path := filepath.Join(t.TempDir(), "x.mp3")

	t.Run("ffprobe cannot run", func(t *testing.T) {
		t.Setenv("PATH", t.TempDir())
		if _, err := probeMedia(path); !errors.Is(err, errToolFailed) {
			t.Errorf("err = %v, want errToolFailed", err)
		}
	})
	t.Run("stderr of a failed probe is cleaned and cut to 2 lines", func(t *testing.T) {
		fakeTools(t, map[string]string{"ffprobe": `for a; do last=$a; done
echo "[mp3 @ 0x55e57d9db000] Header missing in $last" >&2
echo "" >&2
echo "second" >&2
echo "third" >&2
exit 1
`})
		_, err := probeMedia(path)
		if err == nil || errors.Is(err, errToolFailed) || err.Error() != "[mp3] Header missing in x.mp3 | second" {
			t.Errorf("err = %v", err)
		}
	})
	t.Run("failed probe without stderr", func(t *testing.T) {
		fakeTools(t, map[string]string{"ffprobe": "exit 3\n"})
		if _, err := probeMedia(path); err == nil || errors.Is(err, errToolFailed) || err.Error() != "exit status 3" {
			t.Errorf("err = %v, want exit status 3", err)
		}
	})
	t.Run("invalid json", func(t *testing.T) {
		fakeTools(t, map[string]string{"ffprobe": "echo nope\n"})
		if _, err := probeMedia(path); err == nil || errors.Is(err, errToolFailed) || !strings.Contains(err.Error(), "parsing ffprobe output") {
			t.Errorf("err = %v", err)
		}
	})

	tests := []struct {
		name          string
		json, stderr  string
		ffmpeg        string // "" = no ffmpeg on PATH
		wantSec       float64
		wantEstimated bool
	}{
		{"estimated, measuring fails", audioJSON, estimatedMsg, "exit 1\n", 2, true},
		{"estimated, no ffmpeg", audioJSON, estimatedMsg, "", 2, true},
		{"estimated, measured", audioJSON, estimatedMsg, "echo out_time_us=5000000\n", 5, false},
		{"not estimated: not measured", audioJSON, "", "echo out_time_us=5000000\n", 2, false},
		{"estimated video without audio: not measured", videoJSON, estimatedMsg, "echo out_time_us=5000000\n", 2, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			scripts := map[string]string{"ffprobe": printJSON(tt.json, tt.stderr)}
			if tt.ffmpeg != "" {
				scripts["ffmpeg"] = tt.ffmpeg
			}
			fakeTools(t, scripts)
			info, err := probeMedia(path)
			if err != nil || info.DurationSec != tt.wantSec || info.DurationEstimated != tt.wantEstimated || !info.HasDuration {
				t.Errorf("probeMedia = %+v, %v, want %v s, estimated %v", info, err, tt.wantSec, tt.wantEstimated)
			}
		})
	}
}

func TestDecodeCheckFakeTools(t *testing.T) {
	path := filepath.Join(t.TempDir(), "x.mp4")
	t.Run("ffmpeg cannot run", func(t *testing.T) {
		t.Setenv("PATH", t.TempDir())
		if _, _, err := decodeCheck(path); !errors.Is(err, errToolFailed) {
			t.Errorf("err = %v, want errToolFailed", err)
		}
	})
	tests := []struct {
		name, script      string
		wantErr, wantWarn string
	}{
		{"failure without stderr", "exit 1\n", "exit status 1", ""},
		{"failure: first 3 lines", "while [ $# -gt 0 ]; do [ \"$1\" = -i ] && p=$2; shift; done\necho \"[h264 @ 0xabc] a $p\" >&2\necho b >&2\necho c >&2\necho d >&2\nexit 1\n",
			"[h264] a x.mp4 | b | c", ""},
		{"success with errors: null muxer lines dropped", "echo '[null @ 0x1] non monotonically increasing dts' >&2\necho '[h264 @ 0x2] error' >&2\n",
			"", "[h264] error"},
		{"clean success", "exit 0\n", "", ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			fakeTools(t, map[string]string{"ffmpeg": tt.script})
			decErr, decWarn, err := decodeCheck(path)
			if err != nil || decErr != tt.wantErr || decWarn != tt.wantWarn {
				t.Errorf("decodeCheck = %q, %q, %v, want %q, %q", decErr, decWarn, err, tt.wantErr, tt.wantWarn)
			}
		})
	}
}

func TestDecodeCheckGarbage(t *testing.T) {
	requireFFmpeg(t)
	path := filepath.Join(t.TempDir(), "x.mp3")
	if err := os.WriteFile(path, []byte(garbage(4000)), 0o644); err != nil {
		t.Fatal(err)
	}
	decErr, decWarn, err := decodeCheck(path)
	if err != nil || decErr == "" || decWarn != "" || strings.Contains(decErr, filepath.Dir(path)) {
		t.Errorf("decodeCheck = %q, %q, %v, want a decode error only", decErr, decWarn, err)
	}
}

func TestFirstLines(t *testing.T) {
	long := strings.Repeat("x", 400)
	// Byte 300 is the second byte of "ä".
	umlaut := strings.Repeat("x", 299) + "ä" + "yyy"
	exact := strings.Repeat("x", maxMessageLen)
	tests := []struct {
		name string
		in   string
		n    int
		want string
	}{
		{"n lines", "a\n\n  b \nc\nd\ne\n", 3, "a | b | c"},
		{"fewer lines than n", "a\nb\n", 3, "a | b"},
		{"one line", "a\nb\n", 1, "a"},
		{"cut at the limit", long, 3, strings.Repeat("x", maxMessageLen) + "…"},
		{"exactly the limit", exact, 3, exact},
		{"cut before a multi-byte rune", umlaut, 3, strings.Repeat("x", 299) + "…"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := firstLines(tt.in, tt.n)
			if got != tt.want || !utf8.ValidString(got) {
				t.Errorf("firstLines = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestParseProbeEdgeCases(t *testing.T) {
	tests := []struct {
		name string
		json string
		want mediaInfo
	}{
		{"video without width: next stream gives the size",
			`{"streams":[{"codec_type":"video","width":0,"height":360},{"codec_type":"video","width":1920,"height":1080}],"format":{}}`,
			mediaInfo{HasVideo: true, VideoWidth: 1920, VideoHeight: 1080}},
		{"video without height: size unknown",
			`{"streams":[{"codec_type":"video","width":640,"height":0}],"format":{}}`,
			mediaInfo{HasVideo: true}},
		{"zero duration is unknown", `{"streams":[],"format":{"duration":"0.000000"}}`, mediaInfo{}},
		{"N/A duration is unknown", `{"streams":[],"format":{"duration":"N/A"}}`, mediaInfo{}},
		{"video bitrate of the stream",
			`{"streams":[{"codec_type":"video","width":1280,"height":720,"bit_rate":"2500000","tags":{"BPS":"9"}},{"codec_type":"audio","bit_rate":"128000"}],"format":{"bit_rate":"9000000"}}`,
			mediaInfo{HasVideo: true, HasAudio: true, VideoWidth: 1280, VideoHeight: 720, VideoBitrate: 2500000, AudioBitrate: 128000}},
		{"video bitrate from the Matroska BPS tag",
			`{"streams":[{"codec_type":"video","width":1280,"height":720,"tags":{"BPS":"4000000"}},{"codec_type":"audio"}],"format":{"bit_rate":"9000000"}}`,
			mediaInfo{HasVideo: true, HasAudio: true, VideoWidth: 1280, VideoHeight: 720, VideoBitrate: 4000000}},
		{"video bitrate from the BPS-eng tag",
			`{"streams":[{"codec_type":"video","width":1280,"height":720,"tags":{"BPS":"N/A","BPS-eng":"3500000"}}],"format":{}}`,
			mediaInfo{HasVideo: true, VideoWidth: 1280, VideoHeight: 720, VideoBitrate: 3500000}},
		{"video bitrate from the container without audio",
			`{"streams":[{"codec_type":"video","width":640,"height":360}],"format":{"bit_rate":"1500000"}}`,
			mediaInfo{HasVideo: true, VideoWidth: 640, VideoHeight: 360, VideoBitrate: 1500000}},
		{"container bitrate with audio is not the video bitrate",
			`{"streams":[{"codec_type":"video","width":640,"height":360},{"codec_type":"audio","bit_rate":"128000"}],"format":{"bit_rate":"1500000"}}`,
			mediaInfo{HasVideo: true, HasAudio: true, VideoWidth: 640, VideoHeight: 360, AudioBitrate: 128000}},
		{"zero bitrate is unknown", `{"streams":[{"codec_type":"audio","bit_rate":"0"}],"format":{"bit_rate":"64000"}}`,
			mediaInfo{HasAudio: true, AudioBitrate: 64000}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got, err := parseProbe([]byte(tt.json)); err != nil || got != tt.want {
				t.Errorf("parseProbe = %+v, %v, want %+v", got, err, tt.want)
			}
		})
	}
}

func TestParseProgressDurationZero(t *testing.T) {
	if sec, ok := parseProgressDuration("out_time_us=0\nprogress=end\n"); ok {
		t.Errorf("parseProgressDuration = %v, true, want not ok for 0", sec)
	}
}

func TestNewInspectorTools(t *testing.T) {
	tests := []struct {
		name            string
		tools           []string
		deep            bool
		wantNotes       []string
		wantMedia, want bool
	}{
		{"ffprobe only, deep", []string{"ffprobe"}, true, []string{noteNoFFmpeg}, true, false},
		{"ffprobe only, not deep", []string{"ffprobe"}, false, nil, true, false},
		{"ffmpeg only, deep", []string{"ffmpeg"}, true, []string{noteNoFFprobe}, false, false},
		{"both, deep", []string{"ffprobe", "ffmpeg"}, true, nil, true, true},
		{"none, deep", nil, true, []string{noteNoFFprobe, noteNoFFmpeg}, false, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			scripts := map[string]string{}
			for _, tool := range tt.tools {
				scripts[tool] = "exit 0\n"
			}
			fakeTools(t, scripts)
			insp, notes := NewInspector(fsops.New(false), tt.deep)
			if !reflect.DeepEqual(notes, tt.wantNotes) || insp.MediaChecked() != tt.wantMedia || insp.DeepChecked() != tt.want {
				t.Errorf("notes %v, media %v, deep %v; want %v, %v, %v", notes, insp.MediaChecked(), insp.DeepChecked(),
					tt.wantNotes, tt.wantMedia, tt.want)
			}
		})
	}
}

// observeFile: a referenced name that is a folder or a dangling symlink is a missing file
// with the reason in the message.
func TestRunReferenceNotAFile(t *testing.T) {
	withoutFFmpeg(t)
	lib := filepath.Join(t.TempDir(), "songs")
	writeFiles(t, lib, map[string]string{
		"Folder/song.txt":         song("song.mp3", 10),
		"Folder/song.mp3/inner.x": "x",
		"Dangling/song.txt":       song("song.mp3", 10),
	})
	if err := os.Symlink(filepath.Join(lib, "nowhere.mp3"), filepath.Join(lib, "Dangling", "song.mp3")); err != nil {
		t.Fatal(err)
	}
	ops, r := run(t, newConfig(t, lib, false), Options{})
	for path, want := range map[string]string{"Folder": `#MP3 file "song.mp3" cannot be read: is a folder`, "Dangling": "cannot be read: stat "} {
		s := findSong(t, r, path)
		if got := codes(s.Findings); !reflect.DeepEqual(got, []string{"error:missing-file"}) || !strings.Contains(s.Findings[0].Message, want) {
			t.Errorf("%s: findings = %+v, want missing-file with %q", path, s.Findings, want)
		}
	}
	if ops.Stats().Failed != 0 {
		t.Errorf("stats = %+v", ops.Stats())
	}
}

// ffprobe or ffmpeg failing to start is a failure of the run, not a finding.
func TestRunToolCannotStart(t *testing.T) {
	newLib := func(t *testing.T) string {
		lib := filepath.Join(t.TempDir(), "songs")
		writeFiles(t, lib, map[string]string{"Song/song.txt": song("a.mp3", 10), "Song/a.mp3": "x"})
		return lib
	}
	t.Run("ffprobe", func(t *testing.T) {
		dir := fakeTools(t, nil)
		writeBrokenTool(t, dir, "ffprobe")
		lib := newLib(t)
		ops, r := run(t, newConfig(t, lib, false), Options{MoveBroken: true})
		if len(r.Songs) != 0 || r.Checked != 1 || !r.MediaChecked {
			t.Errorf("report = %+v, want no findings", r)
		}
		if st := ops.Stats(); st.Failed != 1 || st.Moved != 0 {
			t.Errorf("stats = %+v, want 1 failure", st)
		}
	})
	t.Run("ffmpeg (deep)", func(t *testing.T) {
		dir := fakeTools(t, map[string]string{"ffprobe": printJSON(audioJSON, "")})
		writeBrokenTool(t, dir, "ffmpeg")
		lib := newLib(t)
		ops, r := run(t, newConfig(t, lib, false), Options{Deep: true})
		if len(r.Songs) != 0 || !r.Deep {
			t.Errorf("report = %+v, want no findings", r)
		}
		if st := ops.Stats(); st.Failed != 1 {
			t.Errorf("stats = %+v, want 1 failure", st)
		}
	})
}

// One probe per file, even if several tags reference it; an image tag naming the same
// file as a media tag still gets its own image observation.
func TestObserveFilesProbeOnce(t *testing.T) {
	logFile := filepath.Join(t.TempDir(), "calls")
	fakeTools(t, map[string]string{"ffprobe": "echo call >> '" + logFile + "'\n" +
		printJSON(`{"streams":[{"codec_type":"video","width":64,"height":48},{"codec_type":"audio"}],"format":{"duration":"2.0"}}`, "")})
	lib := filepath.Join(t.TempDir(), "songs")
	writeFiles(t, lib, map[string]string{
		"Song/song.txt": "#VIDEO:x.png\n#COVER:x.png\n" + song("x.png", 10),
		"Song/x.png":    "\x89PNG\r\n\x1a\n not a real png",
	})
	insp, _ := NewInspector(fsops.New(false), false)
	res, err := insp.Inspect(filepath.Join(lib, "Song"))
	if err != nil {
		t.Fatal(err)
	}
	if got := codes(res.Findings); !reflect.DeepEqual(got, []string{"warning:unreadable-image"}) {
		t.Errorf("findings = %+v, want only the cover's unreadable-image", res.Findings)
	}
	if !res.Media.HasVideo || res.Media.HasCover || res.Media.VideoHeight != 48 {
		t.Errorf("media = %+v", res.Media)
	}
	data, err := os.ReadFile(logFile)
	if err != nil {
		t.Fatal(err)
	}
	if n := strings.Count(string(data), "call"); n != 1 {
		t.Errorf("ffprobe ran %d times, want 1", n)
	}
}

// A failing move of a broken song is counted, the song is reported as not moved, and the
// reports are still written.
func TestRunMoveBrokenFails(t *testing.T) {
	withoutFFmpeg(t)
	lib := filepath.Join(t.TempDir(), "songs")
	writeFiles(t, lib, map[string]string{"Broken/song.txt": song("missing.mp3", 10)})
	cfg := newConfig(t, lib, false)
	if err := os.WriteFile(cfg.InvalidDir(), []byte("in the way"), 0o644); err != nil {
		t.Fatal(err)
	}
	ops, r := run(t, cfg, Options{MoveBroken: true})
	if s := findSong(t, r, "Broken"); s.Moved || s.Status != statusBroken {
		t.Errorf("song = %+v, want broken and not moved", s)
	}
	if st := ops.Stats(); st.Failed != 1 || st.Moved != 0 {
		t.Errorf("stats = %+v, want 1 failure", st)
	}
	if _, err := os.Stat(filepath.Join(lib, "Broken", "song.txt")); err != nil {
		t.Errorf("song left the library: %v", err)
	}
	txt, err := os.ReadFile(cfg.ReportPath(reportTxt))
	if err != nil || !strings.Contains(string(txt), "\nBroken\n") || strings.Contains(string(txt), "moved to invalid/") {
		t.Errorf("report text = %q, %v", txt, err)
	}
}

func TestReportText(t *testing.T) {
	r := report{Checked: 5, Broken: 2, Suspicious: 1, MediaChecked: true, Notes: []string{"n1"}, Songs: []songReport{
		{Path: "A", Txt: "song.txt", Status: statusBroken, Moved: true, Findings: []Finding{
			{Severity: SevError, Code: "c1", Line: 3, Message: "m1"}, {Severity: SevWarning, Code: "c2", Message: "m2"}}},
		{Path: "B", Status: statusBroken, NotMoved: "why", Findings: []Finding{{Severity: SevError, Code: "c3", Line: 4, Message: "m3"}}},
		{Path: "C", Txt: "c.txt", Status: statusSuspicious, Findings: []Finding{{Severity: SevWarning, Code: "c4", Message: "m4"}}},
	}}
	want := "5 song(s) checked: 2 broken, 1 suspicious, 2 ok (media checked: yes, deep: no)\n" +
		"note: n1\n" +
		"\n== Broken songs (errors) ==\n" +
		"\nA  [moved to invalid/]\n" +
		"  error   c1 (song.txt:3): m1\n" +
		"  warning c2: m2\n" +
		"\nB  [not moved: why]\n" +
		"  error   c3 (line 4): m3\n" +
		"\n== Suspicious songs (warnings only) ==\n" +
		"\nC\n" +
		"  warning c4: m4\n"
	if got := string(r.text()); got != want {
		t.Errorf("text =\n%s\nwant\n%s", got, want)
	}
	r.DryRun = true
	if got := string(r.text()); !strings.Contains(got, "\nA  [would be moved to invalid/]\n") {
		t.Errorf("dry-run text =\n%s", got)
	}
}

func TestSortFindings(t *testing.T) {
	w := func(line int, code, file, msg string) Finding {
		return Finding{Severity: SevWarning, Line: line, Code: code, File: file, Message: msg}
	}
	tests := []struct {
		name string
		in   []Finding // in the wrong order
	}{
		{"severity", []Finding{w(1, "a", "a", "a"), {Severity: SevError, Line: 9, Code: "z", File: "z", Message: "z"}}},
		{"line", []Finding{w(9, "a", "a", "a"), w(3, "z", "z", "z")}},
		{"code", []Finding{w(3, "b", "a", "a"), w(3, "a", "z", "z")}},
		{"file", []Finding{w(3, "a", "b.mp3", "a"), w(3, "a", "a.mp3", "z")}},
		{"message", []Finding{w(3, "a", "", "b"), w(3, "a", "", "a")}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			list := append([]Finding(nil), tt.in...)
			sortFindings(list)
			if want := []Finding{tt.in[1], tt.in[0]}; !reflect.DeepEqual(list, want) {
				t.Errorf("sorted = %+v, want %+v", list, want)
			}
			// Already in order: unchanged.
			sortFindings(list)
			if want := []Finding{tt.in[1], tt.in[0]}; !reflect.DeepEqual(list, want) {
				t.Errorf("sorted twice = %+v, want %+v", list, want)
			}
		})
	}
}

// The lines and messages of the note and timing findings.
func TestAnalyzeFindingDetails(t *testing.T) {
	src := func(sec float64) map[string]fileObs { return map[string]fileObs{"MP3": audio("a.mp3", sec)} }
	// The note that ends last (line 16) is in the middle of the file, neither the first
	// nor the last note.
	duet := header + "P1\n" + padNoEnd(0) + ": 900 100 0 x\nP2\n" + pad(0)
	tests := []struct {
		name  string
		txt   string
		files map[string]fileObs
		want  Finding
	}{
		{"few notes: line of the first note", header + "#GAP:0\n" + notes(5), src(2),
			Finding{Severity: SevWarning, Code: codeFewNotes, Line: 6, Message: "only 5 note(s)"}},
		{"note before audio: line of the earliest note", header + "P1\n" + padNoEnd(0) + "P2\n: -2 2 0 la\n" + pad(2), src(2),
			Finding{Severity: SevWarning, Code: codeNoteBeforeAudio, Line: 17,
				Message: "the first note starts at -0:00.1, before the audio begins (beat -2; #GAP too small or negative)"}},
		{"notes past audio: line of the note that ends last", duet, src(10),
			Finding{Severity: SevError, Code: codeNotesPastAudio, Line: 16,
				Message: "the last note ends at 0:50.0, after the end of the audio (a.mp3) at 0:10.0 (wrong #BPM/#GAP or truncated audio)"}},
		{"notes past #END", strings.Replace(duet, "P1\n", "#END:10000\nP1\n", 1), src(60),
			Finding{Severity: SevWarning, Code: codeNotesPastEnd, Line: 17,
				Message: "the last note ends at 0:50.0, after #END at 0:10.0: the notes after #END are never sung"}},
		{"notes end early before the audio end", duet, src(200),
			Finding{Severity: SevWarning, Code: codeNotesEndEarly, Line: 16,
				Message: "the last note ends at 0:50.0, long before the end of the audio (a.mp3) at 3:20.0 (truncated lyrics or wrong #BPM)"}},
		{"notes end early before #END", header + "#END:40000\n" + notes(10), src(60),
			Finding{Severity: SevWarning, Code: codeNotesEndEarly, Line: 15,
				Message: "the last note ends at 0:01.9, long before #END at 0:40.0 (truncated lyrics or wrong #BPM)"}},
		{"estimated length", header + notes(10),
			map[string]fileObs{"MP3": func() fileObs { o := audio("a.mp3", 60); o.Media.DurationEstimated = true; return o }()},
			Finding{Severity: SevWarning, Code: codeNotesEndEarly, Line: 14,
				Message: "the last note ends at 0:01.9, long before the end of the audio (a.mp3) at 1:00.0 (truncated lyrics or wrong #BPM; the audio length is only estimated by ffprobe and may be wrong)"}},
		{"bad start", header + "#START:2\n" + notes(10), src(2),
			Finding{Severity: SevWarning, Code: codeBadStart, Line: 5, Message: "#START (0:02.0) is not before the end of the audio (0:02.0)"}},
		{"video too short", header + "#VIDEO:v.mp4\n" + notes(10), map[string]fileObs{"MP3": audio("a.mp3", 30), "VIDEO": video("v.mp4", 19.9)},
			Finding{Severity: SevWarning, Code: codeVideoTooShort, Line: 5, File: "v.mp4", Tag: "VIDEO",
				Message: "the video ends at song time 0:19.9, 0:10.1 before the end of the audio (a.mp3) at 0:30.0"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			fs := analyze(input(tt.txt, tt.files))
			if len(fs) != 1 || fs[0] != tt.want {
				t.Errorf("findings = %+v\nwant [%+v]", fs, tt.want)
			}
		})
	}
}

// "#END:0" means "no end" (UltraStar Deluxe applies #END only when > 0).
func TestAnalyzeEndZero(t *testing.T) {
	if fs := analyze(input(header+"#END:0\n"+notes(10), healthyFiles())); len(fs) != 0 {
		t.Errorf("findings = %+v, want none", fs)
	}
}

// Notes with the same start beat are ordered by line for the overlap check.
func TestAnalyzeOverlapSameStart(t *testing.T) {
	txt := header + ": 0 2 0 a\n: 0 3 0 b\n" + pad(100)
	fs := analyze(input(txt, noMedia()))
	want := Finding{Severity: SevWarning, Code: codeOverlapping, Line: 6, Message: "note at beat 0 starts before the note at line 5 ends (beat 2)"}
	if len(fs) != 1 || fs[0] != want {
		t.Errorf("findings = %+v, want [%+v]", fs, want)
	}
}

// A P3 note is checked for both singers: the smaller message is kept even when the
// second check produces it.
func TestAnalyzeDuetSmallerMessageLater(t *testing.T) {
	// P2's note is in line 6, P1's in line 8. Singer 1 (checked first) reports the P3 note
	// against line 8, singer 2 against line 6, which sorts first.
	txt := header + "P2\n: 0 10 0 b\nP1\n: 0 20 0 a\nP3\n: 5 1 0 c\n" + pad(100)
	fs := analyze(input(txt, noMedia()))
	want := Finding{Severity: SevWarning, Code: codeOverlapping, Line: 10, Message: "note at beat 5 starts before the note at line 6 ends (beat 10)"}
	if len(fs) != 1 || fs[0] != want {
		t.Errorf("findings = %+v, want [%+v]", fs, want)
	}
}

// A song txt that is a symlink to a txt in the folder is still the song txt.
func TestInspectSymlinkedTxt(t *testing.T) {
	withoutFFmpeg(t)
	lib := filepath.Join(t.TempDir(), "songs")
	writeFiles(t, lib, map[string]string{"Song/real.data": song("a.mp3", 10), "Song/a.mp3": "x"})
	if err := os.Symlink("real.data", filepath.Join(lib, "Song", "song.txt")); err != nil {
		t.Fatal(err)
	}
	insp, _ := NewInspector(fsops.New(false), false)
	res, err := insp.Inspect(filepath.Join(lib, "Song"))
	if err != nil || res.Txt != "song.txt" || res.Broken || len(res.Findings) != 0 {
		t.Errorf("Inspect = %+v, %v, want song.txt without findings", res, err)
	}
}
