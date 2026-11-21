package mediatools

import (
	"os/exec"
	"runtime"
	"strings"
	"testing"
)

func TestOutputCap(t *testing.T) {
	write := func(c *OutputCap, parts ...string) {
		t.Helper()
		for _, p := range parts {
			if n, err := c.Write([]byte(p)); n != len(p) || err != nil {
				t.Fatalf("Write(%q) = %d, %v", p, n, err)
			}
		}
	}
	tests := []struct {
		name       string
		head, tail int
		parts      []string
		want       string
		wantHead   string
		skipped    int64
	}{
		{"fits", 4, 4, []string{"ab", "cd", "ef"}, "abcdef", "abcd", 0},
		{"exactly full", 2, 3, []string{"abcde"}, "abcde", "ab", 0},
		{"skips the middle", 3, 3, []string{"abc", "defg", "hij"}, "abc\n... 4 bytes skipped ...\nhij", "abc", 4},
		{"one big write", 2, 2, []string{"0123456789"}, "01\n... 6 bytes skipped ...\n89", "01", 6},
		{"many small writes", 1, 3, strings.Split("abcdefghij", ""), "a\n... 6 bytes skipped ...\nhij", "a", 6},
		{"a big write after small ones", 1, 3, []string{"a", "b", "c", "defghij"}, "a\n... 6 bytes skipped ...\nhij", "a", 6},
		{"head only", 5, 0, []string{"abc", "defg", "hij"}, "abcde\n... 5 bytes skipped ...\n", "abcde", 5},
		{"tail only", 0, 2, []string{"abc", "de"}, "\n... 3 bytes skipped ...\nde", "", 3},
		{"nothing", 3, 3, nil, "", "", 0},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c := NewOutputCap(tt.head, tt.tail)
			write(c, tt.parts...)
			if got := c.String(); got != tt.want {
				t.Errorf("String() = %q, want %q", got, tt.want)
			}
			if got := c.Head(); got != tt.wantHead {
				t.Errorf("Head() = %q, want %q", got, tt.wantHead)
			}
			if got := c.Skipped(); got != tt.skipped {
				t.Errorf("Skipped() = %d, want %d", got, tt.skipped)
			}
		})
	}
}

// The tail buffer is compacted, so it stays bounded however much is written.
func TestOutputCapBounded(t *testing.T) {
	c := NewOutputCap(10, 100)
	line := []byte(strings.Repeat("x", 39) + "\n")
	for range 100000 {
		c.Write(line)
	}
	c.Write([]byte("the end\n"))
	if cap(c.t) > 4*100 || len(c.h) != 10 {
		t.Errorf("buffers grew: head %d, tail cap %d", len(c.h), cap(c.t))
	}
	if s := c.String(); !strings.HasSuffix(s, "x\nthe end\n") || !strings.Contains(s, "... 3999898 bytes skipped ...") || len(s) > 200 {
		t.Errorf("String() = %q", s)
	}
}

func TestCapOutputDefaults(t *testing.T) {
	c := CapOutput()
	c.Write([]byte("Invalid data found when processing input\n"))
	c.Write([]byte(strings.Repeat("frame error\n", 100000)))
	c.Write([]byte("Conversion failed!\n"))
	s := c.String()
	if !strings.HasPrefix(s, "Invalid data found") || !strings.HasSuffix(s, "Conversion failed!\n") {
		t.Errorf("start or end lost: %q … %q", s[:50], s[len(s)-50:])
	}
	if len(s) > DefaultHead+DefaultTail+100 {
		t.Errorf("kept %d bytes", len(s))
	}
}

func TestCombinedOutput(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("uses sh")
	}
	out, err := CombinedOutput(exec.Command("sh", "-c", "echo out; echo err >&2; exit 3"))
	if err == nil || !strings.Contains(string(out), "out\n") || !strings.Contains(string(out), "err\n") {
		t.Errorf("CombinedOutput = %q, %v; want both streams and an error", out, err)
	}
}
