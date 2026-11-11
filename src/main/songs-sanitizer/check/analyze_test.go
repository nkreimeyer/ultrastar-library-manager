package check

import (
	"fmt"
	"reflect"
	"sort"
	"strings"
	"testing"

	"github.com/nkreimeyer/ultrastar-songs-sanitizer/songtxt"
)

// header is a complete header; BPM 300 gives 50 ms per beat.
const header = "#TITLE:T\n#ARTIST:A\n#MP3:a.mp3\n#BPM:300\n"

// notes returns n notes of 2 beats every 4 beats (the last one ends at beat 4n-2) and "E".
// With BPM 300 and no GAP, 10 notes end at 1900 ms.
func notes(n int) string {
	var sb strings.Builder
	for i := range n {
		fmt.Fprintf(&sb, ": %d 2 0 la\n", i*4)
	}
	return sb.String() + "E\n"
}

// audio returns a probed, healthy audio file of the given length.
func audio(name string, sec float64) fileObs {
	return fileObs{Name: name, Found: true, Size: 1000, Probed: true,
		Media: mediaInfo{DurationSec: sec, HasDuration: true, HasAudio: true}}
}

func video(name string, sec float64) fileObs {
	return fileObs{Name: name, Found: true, Size: 1000, Probed: true,
		Media: mediaInfo{DurationSec: sec, HasDuration: true, HasVideo: true}}
}

func image(name string) fileObs { return fileObs{Name: name, Found: true, Size: 100} }

func input(txt string, files map[string]fileObs) songInput {
	s, issues := songtxt.Parse(txt)
	return songInput{Song: s, Issues: issues, Files: files}
}

// pad returns 10 notes starting at beat from, and "E".
func pad(from int) string { return padNoEnd(from) + "E\n" }

func padNoEnd(from int) string {
	var sb strings.Builder
	for i := range 10 {
		fmt.Fprintf(&sb, ": %d 2 0 la\n", from+i*4)
	}
	return sb.String()
}

// noMedia is an existing, unprobed audio source: no media or timing checks.
func noMedia() map[string]fileObs {
	return map[string]fileObs{"MP3": {Name: "a.mp3", Found: true, Size: 5}}
}

func healthyFiles() map[string]fileObs { return map[string]fileObs{"MP3": audio("a.mp3", 2)} }

// codes renders findings as sorted "severity:code" strings.
func codes(fs []Finding) []string {
	out := []string{}
	for _, f := range fs {
		out = append(out, string(f.Severity)+":"+f.Code)
	}
	sort.Strings(out)
	return out
}

func with(files map[string]fileObs, tag string, o fileObs) map[string]fileObs {
	files[tag] = o
	return files
}

func TestAnalyze(t *testing.T) {
	tests := []struct {
		name  string
		txt   string
		files map[string]fileObs
		want  []string
	}{
		{"healthy", header + notes(10), healthyFiles(), nil},
		{"healthy without media info", header + notes(10), map[string]fileObs{"MP3": {Name: "a.mp3", Found: true, Size: 5}}, nil},

		// Tags.
		{"missing title", "#ARTIST:A\n#MP3:a.mp3\n#BPM:300\n" + notes(10), healthyFiles(), []string{"error:missing-tag"}},
		{"empty artist", "#TITLE:T\n#ARTIST: \n#MP3:a.mp3\n#BPM:300\n" + notes(10), healthyFiles(), []string{"error:missing-tag"}},
		{"no audio tag", "#TITLE:T\n#ARTIST:A\n#BPM:300\n" + notes(10), nil, []string{"error:missing-tag"}},
		{"bpm missing", "#TITLE:T\n#ARTIST:A\n#MP3:a.mp3\n" + notes(10), healthyFiles(), []string{"error:bad-bpm"}},
		{"bpm zero", "#TITLE:T\n#ARTIST:A\n#MP3:a.mp3\n#BPM:0\n" + notes(10), healthyFiles(), []string{"error:bad-bpm"}},
		{"bpm decimal comma", "#TITLE:T\n#ARTIST:A\n#MP3:a.mp3\n#BPM:300,0\n" + notes(10), healthyFiles(), nil},
		{"bad gap", header + "#GAP:x\n" + notes(10), healthyFiles(), []string{"error:bad-gap"}},
		{"bad videogap/start/end", header + "#VIDEOGAP:x\n#START:y\n#END:z\n" + notes(10), healthyFiles(),
			[]string{"warning:bad-end", "warning:bad-start", "warning:bad-videogap"}},
		{"end equals start", header + "#START:1\n#END:1000\n" + notes(10), healthyFiles(), []string{"error:bad-end"}},
		{"end just after start", header + "#START:1\n#END:1001\n" + notes(10), healthyFiles(), nil},
		// UltraStar Deluxe ignores #END <= 0 ("no end").
		{"end zero without start", header + "#END:0\n" + notes(10), healthyFiles(), nil},
		{"end zero with start zero", header + "#START:0\n#END:0\n" + notes(10), healthyFiles(), nil},
		{"negative end", header + "#START:1\n#END:-5\n" + notes(10), healthyFiles(), nil},
		{"duplicate tag", header + "#TITLE:Other\n" + notes(10), healthyFiles(), []string{"warning:duplicate-tag"}},
		// Repeated tags are resolved like the player does: the last one wins. At #BPM:150 the
		// notes would end at 3.8 s, after the 2 s audio; at #BPM:300 they end at 1.9 s.
		{"duplicate bpm: last wins", "#TITLE:T\n#ARTIST:A\n#MP3:a.mp3\n#BPM:150\n#BPM:300\n" + notes(10), healthyFiles(),
			[]string{"warning:duplicate-tag"}},
		{"duplicate bpm: last is broken", "#TITLE:T\n#ARTIST:A\n#MP3:a.mp3\n#BPM:300\n#BPM:x\n" + notes(10), healthyFiles(),
			[]string{"error:bad-bpm", "warning:duplicate-tag"}},
		{"duplicate empty title: last wins", header + "#TITLE:\n" + notes(10), healthyFiles(),
			[]string{"error:missing-tag", "warning:duplicate-tag"}},

		// Negative beats are legal; a note before song time 0 is not sung.
		{"negative beat after gap", header + "#GAP:1000\n: -2 2 0 la\n" + notes(10), healthyFiles(), nil},
		{"negative beat at time 0", header + "#GAP:100\n: -2 2 0 la\n" + notes(10), healthyFiles(), nil},
		{"note before the audio", header + "#GAP:99\n: -2 2 0 la\n" + notes(10), healthyFiles(), []string{"warning:note-before-audio"}},

		// Notes. pad keeps the note count >= 10; noMedia disables the timing checks.
		{"no notes", header + "E\n", healthyFiles(), []string{"error:no-notes"}},
		{"9 notes", header + notes(9), healthyFiles(), []string{"warning:few-notes"}},
		{"no end", header + strings.TrimSuffix(notes(10), "E\n"), healthyFiles(), []string{"warning:no-end"}},
		{"not monotonic", header + ": 0 2 0 a\n: 12 2 0 b\n: 8 2 0 c\n" + pad(100), noMedia(), []string{"error:beats-not-monotonic"}},
		{"overlap", header + ": 0 4 0 a\n: 3 1 0 b\n" + pad(100), noMedia(), []string{"warning:overlapping-notes"}},
		{"touching is no overlap", header + ": 0 4 0 a\n: 4 1 0 b\n" + pad(100), noMedia(), nil},
		{"zero length", header + ": 0 0 0 a\n" + pad(100), noMedia(), []string{"warning:zero-length-note"}},
		{"break before previous note", header + ": 0 2 0 a\n: 8 2 0 b\n- 7\n" + pad(100), noMedia(), []string{"warning:break-order"}},
		{"break at previous note start", header + ": 0 2 0 a\n: 8 2 0 b\n- 8\n" + pad(100), noMedia(), nil},

		// Parser issues.
		{"unknown line", header + "X foo\n" + notes(10), healthyFiles(), []string{"error:unknown-line"}},
		{"malformed note", header + ": a b c x\n" + notes(10), healthyFiles(), []string{"error:malformed-note"}},
		{"malformed break", header + ": 0 2 0 a\n- x\n" + pad(100), noMedia(), []string{"error:malformed-break"}},
		{"malformed bpm change", header + "B x 1\n" + notes(10), healthyFiles(), []string{"error:malformed-bpm"}},
		{"bad player", header + "P9\n" + notes(10), healthyFiles(), []string{"error:bad-player"}},
		{"malformed tag", "#NOCOLON\n" + header + notes(10), healthyFiles(), []string{"warning:malformed-tag"}},
		{"tag after notes", header + ": 0 2 0 a\n#GAP:0\n" + pad(100), noMedia(), []string{"warning:tag-after-notes"}},

		// Duets: the order is checked per player marker in file order, overlaps per singer
		// (P3 belongs to both) in time order.
		{"duet ok", header + "P1\n" + padNoEnd(0) + "P2\n" + pad(0), noMedia(), nil},
		{"duet P3 section after P1 and P2", header + "P1\n: 0 2 0 a\n: 10 2 0 b\n: 100 2 0 c\nP2\n: 20 2 0 d\n: 30 2 0 e\nP3\n: 40 2 0 f\n: 50 2 0 g\n" + pad(200),
			noMedia(), nil},
		{"duet sections of a player continue", header + "P1\n: 0 2 0 a\nP2\n: 10 2 0 b\nP1\n: 8 2 0 c\nP2\n: 6 1 0 d\n" + pad(100),
			noMedia(), []string{"error:beats-not-monotonic"}},
		{"duet P3 not monotonic", header + "P1\n" + padNoEnd(0) + "P3\n: 50 2 0 a\n: 45 2 0 b\nE\n", noMedia(), []string{"error:beats-not-monotonic"}},
		{"duet P3 overlaps a P1 note", header + "P1\n: 0 10 0 a\nP2\n" + padNoEnd(100) + "P3\n: 5 2 0 b\nE\n", noMedia(),
			[]string{"warning:overlapping-notes"}},
		{"duet P1 and P2 may overlap", header + "P1\n: 0 10 0 a\nP2\n: 5 2 0 b\n" + pad(100), noMedia(), nil},

		{"duet note before first marker", header + ": 0 2 0 a\nP1\n" + pad(100), noMedia(), []string{"error:duet-structure"}},
		{"duet empty section", header + "P1\n" + padNoEnd(0) + "P2\nE\n", noMedia(), []string{"warning:duet-structure"}},
		{"duet empty first section", header + "P1\nP2\n" + pad(0), noMedia(), []string{"warning:duet-structure"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := codes(analyze(input(tt.txt, tt.files)))
			want := tt.want
			if want == nil {
				want = []string{}
			}
			sort.Strings(want)
			if !reflect.DeepEqual(got, want) {
				t.Errorf("findings = %v, want %v\n%+v", got, want, analyze(input(tt.txt, tt.files)))
			}
		})
	}
}

func TestAnalyzeFiles(t *testing.T) {
	full := header + "#VIDEO:v.mp4\n#COVER:c.jpg\n#BACKGROUND:b.jpg\n#VOCALS:voc.mp3\n" + notes(10)
	healthy := func() map[string]fileObs {
		return map[string]fileObs{"MP3": audio("a.mp3", 2), "VIDEO": video("v.mp4", 2), "COVER": image("c.jpg"),
			"BACKGROUND": image("b.jpg"), "VOCALS": audio("voc.mp3", 2)}
	}
	tests := []struct {
		name  string
		txt   string
		files map[string]fileObs
		want  []string
	}{
		{"healthy", full, healthy(), nil},
		{"missing source", full, with(healthy(), "MP3", fileObs{}), []string{"error:missing-file"}},
		{"missing video", full, with(healthy(), "VIDEO", fileObs{}), []string{"warning:missing-file"}},
		{"missing cover", full, with(healthy(), "COVER", fileObs{}), []string{"warning:missing-file"}},
		{"missing vocals", full, with(healthy(), "VOCALS", fileObs{}), []string{"warning:missing-file"}},
		{"unstatable source", full, with(healthy(), "MP3", fileObs{Name: "a.mp3", Found: true, StatErr: "denied"}), []string{"error:missing-file"}},
		{"empty source", full, with(healthy(), "MP3", fileObs{Name: "a.mp3", Found: true}), []string{"error:empty-file"}},
		{"empty background", full, with(healthy(), "BACKGROUND", fileObs{Name: "b.jpg", Found: true}), []string{"warning:empty-file"}},
		{"unreadable source", full, with(healthy(), "MP3", fileObs{Name: "a.mp3", Found: true, Size: 9, Probed: true, ProbeErr: "Invalid data"}),
			[]string{"error:unreadable-media"}},
		{"unreadable video", full, with(healthy(), "VIDEO", fileObs{Name: "v.mp4", Found: true, Size: 9, Probed: true, ProbeErr: "moov atom not found"}),
			[]string{"warning:unreadable-media"}},
		{"unreadable stem", full, with(healthy(), "VOCALS", fileObs{Name: "voc.mp3", Found: true, Size: 9, Probed: true, ProbeErr: "x"}),
			[]string{"warning:unreadable-media"}},
		{"source without audio stream", full, with(healthy(), "MP3", video("a.mp3", 2)), []string{"error:no-audio-stream"}},
		{"stem without audio stream is not checked", full, with(healthy(), "VOCALS", video("voc.mp3", 2)), nil},
		{"video without video stream", full, with(healthy(), "VIDEO", audio("v.mp4", 2)), []string{"warning:no-video-stream"}},
		{"unreadable cover", full, with(healthy(), "COVER", fileObs{Name: "c.jpg", Found: true, Size: 9, ImageErr: "invalid image"}),
			[]string{"warning:unreadable-image"}},
		{"decode error source", full, with(healthy(), "MP3", func() fileObs { o := audio("a.mp3", 2); o.DecodeErr = "x"; return o }()),
			[]string{"error:decode-error"}},
		{"decode error video", full, with(healthy(), "VIDEO", func() fileObs { o := video("v.mp4", 2); o.DecodeErr = "x"; return o }()),
			[]string{"warning:decode-error"}},
		{"decode warning source", full, with(healthy(), "MP3", func() fileObs { o := audio("a.mp3", 2); o.DecodeWarn = "x"; return o }()),
			[]string{"warning:decode-warning"}},

		// #AUDIO (format >= 1.1) is the audio source when present; #MP3 is then a legacy reference.
		{"audio tag is the source", header + "#AUDIO:new.ogg\n" + notes(10),
			map[string]fileObs{"MP3": {}, "AUDIO": audio("new.ogg", 2)}, []string{"warning:missing-file"}},
		{"missing audio tag file", header + "#AUDIO:new.ogg\n" + notes(10),
			map[string]fileObs{"MP3": audio("a.mp3", 2), "AUDIO": {}}, []string{"error:missing-file"}},
		{"same file in mp3 and audio reported once", header + "#AUDIO:a.mp3\n" + notes(10),
			map[string]fileObs{"MP3": {}, "AUDIO": {}}, []string{"error:missing-file"}},

		// Video-only song: #MP3 is the video.
		{"video-only ok", strings.Replace(header, "a.mp3", "v.mp4", 1) + "#VIDEO:v.mp4\n" + notes(10),
			func() map[string]fileObs {
				v := video("v.mp4", 2)
				v.Media.HasAudio = true
				return map[string]fileObs{"MP3": v, "VIDEO": v}
			}(), nil},
		// No permission: a failure of the run (counted by the checker), not of the song.
		{"unreadable source is no finding", full, with(healthy(), "MP3", fileObs{Name: "a.mp3", Found: true, Size: 9, Unreadable: true}), nil},
		{"unreadable cover is no finding", full, with(healthy(), "COVER", fileObs{Name: "c.jpg", Found: true, Size: 9, Unreadable: true}), nil},

		// Repeated file tags: the last occurrence whose file exists (fileObs.Value, set by
		// observeFiles), else the last one.
		{"duplicate mp3: existing one chosen", header + "#MP3:old.mp3\n" + notes(10),
			map[string]fileObs{"MP3": func() fileObs { o := audio("a.mp3", 2); o.Value = "a.mp3"; return o }()},
			[]string{"warning:duplicate-tag"}},
		{"duplicate mp3: none exists, last reported", header + "#MP3:old.mp3\n" + notes(10),
			map[string]fileObs{"MP3": {}}, []string{"error:missing-file", "warning:duplicate-tag"}},

		{"video-only unreadable reported once as error", strings.Replace(header, "a.mp3", "v.mp4", 1) + "#VIDEO:v.mp4\n" + notes(10),
			func() map[string]fileObs {
				v := fileObs{Name: "v.mp4", Found: true, Size: 9, Probed: true, ProbeErr: "x"}
				return map[string]fileObs{"MP3": v, "VIDEO": v}
			}(), []string{"error:unreadable-media"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := codes(analyze(input(tt.txt, tt.files)))
			want := tt.want
			if want == nil {
				want = []string{}
			}
			sort.Strings(want)
			if !reflect.DeepEqual(got, want) {
				t.Errorf("findings = %v, want %v\n%+v", got, want, analyze(input(tt.txt, tt.files)))
			}
		})
	}
}

// The 10 notes of notes(10) end at 1900 ms (plus #GAP).
func TestAnalyzeTiming(t *testing.T) {
	src := func(sec float64) map[string]fileObs { return map[string]fileObs{"MP3": audio("a.mp3", sec)} }
	withVideo := func(audioSec, videoSec float64) map[string]fileObs {
		return map[string]fileObs{"MP3": audio("a.mp3", audioSec), "VIDEO": video("v.mp4", videoSec)}
	}
	tests := []struct {
		name  string
		txt   string
		files map[string]fileObs
		want  []string
	}{
		// notes-past-audio: last note end > song end + 1000 ms.
		{"past audio", header + notes(10), src(0.8), []string{"error:notes-past-audio"}},
		{"past audio boundary", header + notes(10), src(0.9), nil},
		// Notes after #END are never sung, but legal: a warning, not an error.
		{"past #END", header + "#END:850\n" + notes(10), src(2), []string{"warning:notes-past-end"}},
		{"#END boundary", header + "#END:900\n" + notes(10), src(2), nil},
		{"past #END and the audio", header + "#END:500\n" + notes(10), src(0.8), []string{"error:notes-past-audio"}},
		{"estimated duration: past audio is a warning", header + notes(10),
			map[string]fileObs{"MP3": func() fileObs { o := audio("a.mp3", 0.8); o.Media.DurationEstimated = true; return o }()},
			[]string{"warning:notes-past-audio"}},
		// The note that ends last is in P1, not the last note line (P2).
		{"duet: P1 past the audio", header + "P1\n" + padNoEnd(0) + ": 900 100 0 x\nP2\n" + pad(0), src(10),
			[]string{"error:notes-past-audio"}},
		{"#END after the audio does not count", header + "#END:5000\n" + notes(10), src(0.8), []string{"error:notes-past-audio"}},
		{"gap shifts notes", header + "#GAP:1000\n" + notes(10), src(1.8), []string{"error:notes-past-audio"}},
		{"bpm change", header + "B 20 150\n" + notes(10), src(1.7), []string{"error:notes-past-audio"}},
		{"no audio stream: no timing", header + notes(10), with(src(0.1), "MP3", video("a.mp3", 0.1)), []string{"error:no-audio-stream"}},
		{"unknown duration: no timing", header + notes(10),
			map[string]fileObs{"MP3": {Name: "a.mp3", Found: true, Size: 9, Probed: true, Media: mediaInfo{HasAudio: true}}}, nil},
		{"bad bpm: no timing", strings.Replace(header, "300", "x", 1) + notes(10), src(0.1), []string{"error:bad-bpm"}},

		// notes-end-early: last end < 50% of the song AND more than 30 s before its end.
		{"end early", header + notes(10), src(60), []string{"warning:notes-end-early"}},
		{"end early gap boundary", header + notes(10), src(31.9), nil},
		{"end early gap just over", header + notes(10), src(31.95), []string{"warning:notes-end-early"}},
		{"end early ratio boundary", header + "#GAP:38100\n" + notes(10), src(80), nil},
		{"end early ratio just under", header + "#GAP:38100\n" + notes(10), src(80.1), []string{"warning:notes-end-early"}},
		{"#END cuts the outro", header + "#END:3000\n" + notes(10), src(60), nil},

		// #START at or after the end of the audio.
		{"start at audio end", header + "#START:2\n" + notes(10), src(2), []string{"warning:bad-start"}},
		{"start before audio end", header + "#START:1.9\n" + notes(10), src(2), nil},

		// video-too-short: the video ends at song time videoDur - VIDEOGAP.
		{"video boundary", header + "#VIDEO:v.mp4\n" + notes(10), withVideo(30, 20), nil},
		{"video too short", header + "#VIDEO:v.mp4\n" + notes(10), withVideo(30, 19.9), []string{"warning:video-too-short"}},
		{"videogap shortens", header + "#VIDEO:v.mp4\n#VIDEOGAP:0.5\n" + notes(10), withVideo(30, 20.4), []string{"warning:video-too-short"}},
		{"negative videogap lengthens", header + "#VIDEO:v.mp4\n#VIDEOGAP:-0.5\n" + notes(10), withVideo(30, 19.6), nil},
		// The #VIDEOGAP would make the video end 13 s before the song: only the same-file
		// rule prevents the finding.
		{"video same file as audio", strings.Replace(header, "a.mp3", "v.mp4", 1) + "#VIDEO:v.mp4\n#VIDEOGAP:15\n" + notes(10),
			func() map[string]fileObs {
				v := video("v.mp4", 2)
				v.Media.HasAudio = true
				return map[string]fileObs{"MP3": v, "VIDEO": v}
			}(), nil},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := codes(analyze(input(tt.txt, tt.files)))
			want := tt.want
			if want == nil {
				want = []string{}
			}
			sort.Strings(want)
			if !reflect.DeepEqual(got, want) {
				t.Errorf("findings = %v, want %v\n%+v", got, want, analyze(input(tt.txt, tt.files)))
			}
		})
	}
}

func TestAnalyzeEncoding(t *testing.T) {
	got := codes(analyze(songInput{EncodingProblem: "mixed encodings", Damaged: true}))
	if want := []string{"warning:damaged-text", "warning:encoding"}; !reflect.DeepEqual(got, want) {
		t.Errorf("findings = %v, want %v", got, want)
	}
	in := input(header+notes(10), healthyFiles())
	in.EncodingProblem = "mixed encodings"
	fs := analyze(in)
	if got := codes(fs); !reflect.DeepEqual(got, []string{"warning:encoding"}) || !strings.Contains(fs[0].Message, "mixed encodings") {
		t.Errorf("findings = %+v", fs)
	}
}

func TestAnalyzeFirstOccurrenceAndCount(t *testing.T) {
	txt := header + ": 0 2 0 a\n: 20 2 0 b\n: 10 2 0 c\n: 30 2 0 d\n: 5 2 0 e\n" + pad(100)
	fs := analyze(input(txt, noMedia()))
	var f *Finding
	for i := range fs {
		if fs[i].Code == codeNotMonotonic {
			f = &fs[i]
		}
	}
	if f == nil {
		t.Fatalf("no %s in %+v", codeNotMonotonic, fs)
	}
	if f.Line != 7 || !strings.Contains(f.Message, "beat 10") || !strings.Contains(f.Message, "(2 occurrences)") {
		t.Errorf("finding = %+v, want line 7, beat 10, 2 occurrences", f)
	}
}

func TestAnalyzeSortedAndLines(t *testing.T) {
	txt := "#TITLE:T\n#ARTIST:A\n#MP3:a.mp3\n#BPM:300\n#VIDEO:v.mp4\nX\n" + notes(10)
	fs := analyze(input(txt, map[string]fileObs{"MP3": audio("a.mp3", 2), "VIDEO": {}}))
	want := []Finding{
		{Severity: SevError, Code: songtxt.IssueUnknownLine, Line: 6},
		{Severity: SevWarning, Code: codeMissingFile, File: "v.mp4", Line: 5},
	}
	if len(fs) != len(want) {
		t.Fatalf("findings = %+v", fs)
	}
	for i := range want {
		if fs[i].Severity != want[i].Severity || fs[i].Code != want[i].Code || fs[i].File != want[i].File || fs[i].Line != want[i].Line {
			t.Errorf("finding %d = %+v, want %+v", i, fs[i], want[i])
		}
	}
}

// The reported reference of a repeated file tag is the chosen one: the occurrence
// observeFiles found (fileObs.Value), else the last one.
func TestAnalyzeDuplicateFileTag(t *testing.T) {
	txt := header + "#MP3:old.mp3\n" + notes(10) // a.mp3 in line 3, old.mp3 in line 5
	fs := analyze(input(txt, map[string]fileObs{"MP3": {}}))
	for _, f := range fs {
		if f.Code == codeMissingFile && (f.File != "old.mp3" || f.Line != 5) {
			t.Errorf("missing-file = %+v, want the last occurrence (old.mp3, line 5)", f)
		}
		if f.Code == codeDuplicateTag && !strings.Contains(f.Message, "last one whose file exists") {
			t.Errorf("duplicate-tag message = %q", f.Message)
		}
	}
	found := fileObs{Value: "a.mp3", Name: "a.mp3", Found: true, Size: 9, Probed: true, ProbeErr: "x"}
	fs = analyze(input(txt, map[string]fileObs{"MP3": found}))
	if f := fs[0]; f.Code != codeUnreadableMedia || f.Line != 3 || f.Severity != SevError {
		t.Errorf("findings = %+v, want unreadable-media of a.mp3 in line 3", fs)
	}
}

// Overlaps are found against the note that ends last so far, not only the previous one.
func TestAnalyzeOverlapLongNote(t *testing.T) {
	txt := header + ": 0 100 0 a\n: 50 2 0 b\n: 60 2 0 c\n" + pad(200)
	fs := analyze(input(txt, noMedia()))
	if len(fs) != 1 || fs[0].Code != codeOverlapping || fs[0].Line != 6 || !strings.Contains(fs[0].Message, "(2 occurrences)") {
		t.Errorf("findings = %+v, want overlapping-notes in line 6, 2 occurrences", fs)
	}
}

// A P3 note is checked for both singers, with a different previous note each: the
// message must not depend on the order of the checks.
func TestAnalyzeDuetDeterministic(t *testing.T) {
	txt := header + "P1\n: 0 10 0 a\nP2\n: 0 20 0 b\nP3\n: 5 1 0 c\n" + pad(100)
	first := analyze(input(txt, noMedia()))
	if got := codes(first); !reflect.DeepEqual(got, []string{"warning:overlapping-notes"}) {
		t.Fatalf("findings = %v", got)
	}
	for range 100 {
		if again := analyze(input(txt, noMedia())); !reflect.DeepEqual(again, first) {
			t.Fatalf("findings differ between calls:\n%+v\n%+v", first, again)
		}
	}
}

func TestFmtMs(t *testing.T) {
	for ms, want := range map[float64]string{0: "0:00.0", 61234: "1:01.2", 59960: "1:00.0", -500: "-0:00.5", 600000: "10:00.0"} {
		if got := fmtMs(ms); got != want {
			t.Errorf("fmtMs(%v) = %q, want %q", ms, got, want)
		}
	}
}

// A file problem reported by two tags is kept once, with the higher severity, whatever
// the order of the tags.
func TestFindingsDedupe(t *testing.T) {
	var fs findings
	fs.add(SevWarning, codeUnreadableMedia, 5, "v.mp4", "as video")
	fs.add(SevError, codeUnreadableMedia, 3, "v.mp4", "as audio source")
	fs.add(SevWarning, codeUnreadableMedia, 6, "v.mp4", "again")
	fs.add(SevWarning, codeNoVideoStream, 5, "v.mp4", "other code")
	fs.add(SevWarning, codeFewNotes, 1, "", "no file: never merged")
	fs.add(SevWarning, codeFewNotes, 1, "", "no file: never merged")
	got := fs.sorted()
	if len(got) != 4 || got[0].Severity != SevError || got[0].Line != 3 || got[0].Message != "as audio source" {
		t.Errorf("findings = %+v", got)
	}
}

func TestAnalyzeMissingNameTagsFillableFromFolder(t *testing.T) {
	const body = "#MP3:a.mp3\n#BPM:300\n"
	tests := []struct {
		name   string
		txt    string
		folder string
		want   []string // "severity:message"
	}{
		{"not fillable", "#TITLE:T\n" + body + notes(10), "No separator",
			[]string{"error:#ARTIST is missing or empty"}},
		{"fixtxt leaves the txt alone", "#TITLE:Hypa Hypa\n" + body + notes(10), "",
			[]string{"error:#ARTIST is missing or empty"}},
		{"fillable: still an error", "#TITLE:Hypa Hypa\n" + body + notes(10), "Electric Callboy - Hypa Hypa",
			[]string{"error:#ARTIST is missing or empty; fixtxt fills it from the folder name: Electric Callboy"}},
		{"both fillable", "#TITLE:\n" + body + notes(10), "Electric Callboy - Hypa Hypa", []string{
			"error:#ARTIST is missing or empty; fixtxt fills it from the folder name: Electric Callboy",
			"error:#TITLE is missing or empty; fixtxt fills it from the folder name: Hypa Hypa",
		}},
		{"reversed folder name is not fillable", "#TITLE:Hypa Hypa\n" + body + notes(10), "Hypa Hypa - Electric Callboy",
			[]string{"error:#ARTIST is missing or empty"}},
		{"title from the artist", "#ARTIST:Wham!\n" + body + notes(10), "Wham! - Last Christmas - Live",
			[]string{"error:#TITLE is missing or empty; fixtxt fills it from the folder name: Last Christmas - Live"}},
		{"last of a repeated title is used", "#TITLE:Hypa Hypa\n#TITLE:Other\n" + body + notes(10), "Electric Callboy - Hypa Hypa",
			[]string{"error:#ARTIST is missing or empty"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			in := input(tt.txt, healthyFiles())
			in.FolderName = tt.folder
			var got []string
			for _, f := range analyze(in) {
				if f.Code == codeMissingTag {
					got = append(got, string(f.Severity)+":"+f.Message)
				}
			}
			sort.Strings(got)
			if !reflect.DeepEqual(got, tt.want) {
				t.Errorf("missing-tag findings = %q, want %q", got, tt.want)
			}
		})
	}
}
