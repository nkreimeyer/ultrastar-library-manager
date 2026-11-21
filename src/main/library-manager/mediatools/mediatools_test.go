package mediatools

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/nkreimeyer/library-manager/internal/testutil"
)

func TestParseVersion(t *testing.T) {
	tests := []struct {
		line   string
		ok     bool
		want   Version
		tooOld bool
	}{
		{"ffmpeg version 7.1.1 Copyright (c) 2000-2025", true, Version{Raw: "7.1.1", Major: 7, Minor: 1}, false},
		{"ffmpeg version n9.1 Copyright (c) 2000-2026", true, Version{Raw: "n9.1", Major: 9, Minor: 1}, false},
		{"ffmpeg version 6.1.1-3ubuntu5 Copyright", true, Version{Raw: "6.1.1-3ubuntu5", Major: 6, Minor: 1}, false},
		{"ffmpeg version 7.1-full_build-www.gyan.dev Copyright", true, Version{Raw: "7.1-full_build-www.gyan.dev", Major: 7, Minor: 1}, false},
		{"ffmpeg version 5.1.6-0+deb12u1 Copyright", true, Version{Raw: "5.1.6-0+deb12u1", Major: 5, Minor: 1}, false},
		{"ffmpeg version 5.0.3 Copyright", true, Version{Raw: "5.0.3", Major: 5, Minor: 0}, true},
		{"ffmpeg version 4.4.2-0ubuntu0.22.04.1 Copyright", true, Version{Raw: "4.4.2-0ubuntu0.22.04.1", Major: 4, Minor: 4}, true},
		{"ffmpeg version 10.0 Copyright", true, Version{Raw: "10.0", Major: 10}, false},
		{"ffmpeg version 7.1", true, Version{Raw: "7.1", Major: 7, Minor: 1}, false}, // nothing after the version
		{"ffmpeg version N-118000-g1234abcd Copyright", true, Version{Raw: "N-118000-g1234abcd", Git: true}, false},
		{"ffmpeg version 2024-03-07-git-97beb63a66-full_build-www.gyan.dev Copyright", true,
			Version{Raw: "2024-03-07-git-97beb63a66-full_build-www.gyan.dev", Git: true, Date: "2024-03-07"}, false},
		{"ffmpeg version 2022-07-22-git-abc Copyright", true, Version{Raw: "2022-07-22-git-abc", Git: true, Date: "2022-07-22"}, false},
		{"ffmpeg version 2021-05-01-git-abc Copyright", true, Version{Raw: "2021-05-01-git-abc", Git: true, Date: "2021-05-01"}, true},
		{"ffmpeg version git-2020-01-01 Copyright", false, Version{Raw: "git-2020-01-01"}, false},
		{"ffprobe version 7.1 Copyright", false, Version{}, false}, // other tool's output
		{"ffmpeg version", false, Version{}, false},
		{"", false, Version{}, false},
	}
	for _, tt := range tests {
		got, ok := ParseVersion("ffmpeg", []byte(tt.line+"\nbuilt with gcc\n"))
		if ok != tt.ok || got != tt.want {
			t.Errorf("ParseVersion(%q) = %+v, %v; want %+v, %v", tt.line, got, ok, tt.want, tt.ok)
		}
		if ok && got.TooOld() != tt.tooOld {
			t.Errorf("%q: TooOld = %v, want %v", tt.line, got.TooOld(), tt.tooOld)
		}
	}
}

func versionScript(name, v string) string {
	return "#!/bin/sh\necho '" + name + " version " + v + " Copyright (c) the FFmpeg developers'\n"
}

func TestDetect(t *testing.T) {
	t.Run("missing", func(t *testing.T) {
		testutil.FakeTools(t, nil)
		tools := Detect(context.Background())
		if len(tools) != 2 || tools[0].Name != "ffmpeg" || tools[1].Name != "ffprobe" {
			t.Fatalf("tools = %+v", tools)
		}
		for _, tool := range tools {
			if tool.Found || tool.OK || tool.Path != "" || !strings.Contains(tool.Problem, "not found") {
				t.Errorf("%+v, want not found", tool)
			}
		}
		if AllOK(tools) {
			t.Error("AllOK = true")
		}
	})
	t.Run("ok", func(t *testing.T) {
		testutil.FakeTools(t, map[string]string{"ffmpeg": versionScript("ffmpeg", "7.1.1"), "ffprobe": versionScript("ffprobe", "N-1-gabc")})
		tools := Detect(context.Background())
		for _, tool := range tools {
			if !tool.Found || !tool.Known || !tool.OK || tool.Problem != "" || tool.Path == "" {
				t.Errorf("%+v, want ok", tool)
			}
		}
		if tools[0].Version != "7.1.1" || tools[1].Version != "N-1-gabc" {
			t.Errorf("versions = %q, %q", tools[0].Version, tools[1].Version)
		}
		if !AllOK(tools) {
			t.Error("AllOK = false")
		}
	})
	t.Run("too old, unreadable, broken", func(t *testing.T) {
		testutil.FakeTools(t, map[string]string{"ffmpeg": versionScript("ffmpeg", "4.4.2"), "ffprobe": "#!/bin/sh\necho hello\n"})
		tools := Detect(context.Background())
		if f := tools[0]; !f.Found || !f.Known || f.OK || !strings.Contains(f.Problem, "too old") {
			t.Errorf("ffmpeg %+v, want too old", f)
		}
		if p := tools[1]; !p.Found || p.Known || !p.OK || !strings.Contains(p.Problem, "could not be read") {
			t.Errorf("ffprobe %+v, want found with unknown version", p)
		}
		if AllOK(tools) {
			t.Error("AllOK = true")
		}
	})
	t.Run("fails to run", func(t *testing.T) {
		testutil.FakeTools(t, map[string]string{"ffmpeg": "#!/bin/sh\nexit 3\n", "ffprobe": "#!/nonexistent/interpreter\n"})
		for _, tool := range Detect(context.Background()) {
			if !tool.Found || tool.OK || !strings.Contains(tool.Problem, "-version failed") {
				t.Errorf("%+v, want failed", tool)
			}
		}
	})
	t.Run("timeout", func(t *testing.T) {
		old := Timeout
		Timeout = 100 * time.Millisecond
		t.Cleanup(func() { Timeout = old })
		testutil.FakeTools(t, map[string]string{"ffmpeg": "#!/bin/sh\nPATH=/usr/bin:/bin\nexec sleep 10\n"})
		start := time.Now()
		f := Detect(context.Background())[0]
		if f.OK || !strings.Contains(f.Problem, "-version failed") {
			t.Errorf("%+v, want failed", f)
		}
		if time.Since(start) > 5*time.Second {
			t.Errorf("took %v, the timeout did not stop the tool", time.Since(start))
		}
	})
}

func TestInstallHint(t *testing.T) {
	for goos, want := range map[string]string{"windows": "winget", "darwin": "brew", "linux": "apt"} {
		if h := InstallHint(goos); !strings.Contains(h, want) {
			t.Errorf("InstallHint(%s) = %q, want %q in it", goos, h, want)
		}
	}
}

func TestRequire(t *testing.T) {
	t.Run("ok, also with an unreadable version", func(t *testing.T) {
		testutil.FakeTools(t, map[string]string{"ffmpeg": versionScript("ffmpeg", "5.1.6"), "ffprobe": "#!/bin/sh\necho hello\n"})
		if err := Require(context.Background(), Names...); err != nil {
			t.Errorf("Require = %v", err)
		}
		if tool := Lookup(context.Background(), "ffmpeg"); !tool.OK || tool.TooOld() || tool.Err() != nil {
			t.Errorf("Lookup = %+v, want ok", tool)
		}
	})
	t.Run("too old, missing", func(t *testing.T) {
		testutil.FakeTools(t, map[string]string{"ffmpeg": versionScript("ffmpeg", "4.4.2")})
		err := Require(context.Background(), Names...)
		want := "ffmpeg: version 4.4.2 is too old, 5.1 or newer is needed; ffprobe was not found on PATH"
		if err == nil || err.Error() != want {
			t.Errorf("Require = %v, want %q", err, want)
		}
		if tool := Lookup(context.Background(), "ffmpeg"); !tool.TooOld() {
			t.Errorf("Lookup = %+v, want too old", tool)
		}
		if tool := Lookup(context.Background(), "ffprobe"); tool.TooOld() || tool.Found {
			t.Errorf("Lookup = %+v, want missing (not too old)", tool)
		}
		if err := Require(context.Background()); err != nil {
			t.Errorf("Require() = %v, want nil for no tools", err)
		}
	})
	t.Run("fails to run", func(t *testing.T) {
		testutil.FakeTools(t, map[string]string{"ffprobe": "#!/bin/sh\nexit 3\n"})
		if err := Require(context.Background(), "ffprobe"); err == nil || !strings.HasPrefix(err.Error(), "ffprobe -version failed: ") {
			t.Errorf("Require = %v, want the failure", err)
		}
		if tool := Lookup(context.Background(), "ffprobe"); tool.TooOld() {
			t.Errorf("Lookup = %+v, a tool that does not run is not too old", tool)
		}
	})
}
