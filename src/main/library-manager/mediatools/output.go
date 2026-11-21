package mediatools

import (
	"fmt"
	"os/exec"
)

// Default limits of CapOutput. ffmpeg prints the reason a run failed either right at the
// start (the input cannot be opened: "Invalid data found when processing input") or at the
// end (the encoder or muxer gave up), so both ends are kept. A damaged file can make the
// decoder print a line per frame in between, megabytes for one file.
const (
	DefaultHead = 16 << 10
	DefaultTail = 48 << 10
)

// OutputCap is an io.Writer that keeps the first head and the last tail bytes written to
// it and counts the bytes in between. Use it for tool output that is only logged or
// searched for error messages; output that is parsed as data must be read in full.
//
// It is not safe for concurrent use, but exec.Cmd never writes to it concurrently when it
// is both Stdout and Stderr of a command.
type OutputCap struct {
	head, tail int
	h          []byte
	t          []byte // the last bytes written after h was full; at most 2*tail
	total      int64
}

// NewOutputCap returns a writer keeping the first head and the last tail bytes.
func NewOutputCap(head, tail int) *OutputCap {
	return &OutputCap{head: max(0, head), tail: max(0, tail)}
}

// CapOutput returns a writer keeping the first DefaultHead and the last DefaultTail bytes.
func CapOutput() *OutputCap { return NewOutputCap(DefaultHead, DefaultTail) }

// Write keeps what fits and never fails.
func (c *OutputCap) Write(p []byte) (int, error) {
	n := len(p)
	c.total += int64(n)
	if room := c.head - len(c.h); room > 0 {
		k := min(room, len(p))
		c.h = append(c.h, p[:k]...)
		p = p[k:]
	}
	if c.tail == 0 || len(p) == 0 {
		return n, nil
	}
	if len(p) >= c.tail {
		c.t = append(c.t[:0], p[len(p)-c.tail:]...)
		return n, nil
	}
	c.t = append(c.t, p...)
	if len(c.t) > 2*c.tail { // compact now and then instead of on every write
		c.t = append(c.t[:0], c.t[len(c.t)-c.tail:]...)
	}
	return n, nil
}

// kept returns the tail bytes that are kept.
func (c *OutputCap) kept() []byte {
	if len(c.t) > c.tail {
		return c.t[len(c.t)-c.tail:]
	}
	return c.t
}

// Skipped returns the number of bytes that were written but not kept.
func (c *OutputCap) Skipped() int64 {
	return c.total - int64(len(c.h)) - int64(len(c.kept()))
}

// Head returns the first bytes written (at most head), without the skip marker or the tail.
func (c *OutputCap) Head() string { return string(c.h) }

// String returns the kept output. If bytes were skipped, a line
// "... N bytes skipped ..." stands where they were.
func (c *OutputCap) String() string {
	skipped := c.Skipped()
	if skipped == 0 {
		return string(c.h) + string(c.kept())
	}
	return fmt.Sprintf("%s\n... %d bytes skipped ...\n%s", c.h, skipped, c.kept())
}

// CombinedOutput runs cmd like cmd.CombinedOutput, but keeps only the start and the end
// of its output (see CapOutput). Use it where the output is only logged or searched for
// error messages.
func CombinedOutput(cmd *exec.Cmd) ([]byte, error) {
	out := CapOutput()
	cmd.Stdout, cmd.Stderr = out, out
	err := cmd.Run()
	return []byte(out.String()), err
}
