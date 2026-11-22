package mediacache

import (
	"context"
	"errors"

	"github.com/nkreimeyer/library-manager/mediatools"
)

// KindProbe is the kind of the results of Probe.
const KindProbe = "probe/1"

// probeResult is a cached mediatools.Probe result: the info with its Log (which has no
// JSON name in ProbeInfo), or the failure of ffprobe on the file.
type probeResult struct {
	Info  *mediatools.ProbeInfo `json:"info,omitempty"`
	Log   string                `json:"log,omitempty"`
	Error *probeFailure         `json:"error,omitempty"`
}

type probeFailure struct {
	Message string `json:"message"` // ProbeError.Err.Error(), e.g. "exit status 1"
	Stderr  string `json:"stderr"`
}

// Probe is mediatools.Probe through the cache c (nil = no cache). A successful probe and a
// *mediatools.ProbeError that is about the file (mediatools.FileFailure: ffprobe exited
// with an error status and named no system error; such a file stays unreadable until it
// changes) are cached. A probe killed by a signal or failing on a read error of the file
// system (a NAS connection that dropped for a moment) is not, and neither is a successful
// probe whose messages name a system error (its result may be partial). A cached ProbeError has the same message, Stderr and info Log as
// the original; its Err is a plain error with the exit error's message. A probe that could
// not run (mediatools.ErrNotRun), was stopped (ctx) or whose output could not be parsed is
// never cached. If ctx is done, the result is ctx.Err(), as from mediatools.Probe, even for
// a cached file.
func Probe(ctx context.Context, c *Cache, path string) (mediatools.ProbeInfo, error) {
	if err := ctx.Err(); err != nil {
		return mediatools.ProbeInfo{}, err
	}
	var r probeResult
	st, ok := c.Lookup(path, KindProbe, &r)
	if ok {
		if r.Error != nil {
			return mediatools.ProbeInfo{Log: r.Log}, &mediatools.ProbeError{Err: errors.New(r.Error.Message), Stderr: r.Error.Stderr}
		}
		if r.Info != nil {
			info := *r.Info
			info.Log = r.Log
			return info, nil
		}
		// An empty record (not written by this code): probe again.
	}
	info, err := mediatools.Probe(ctx, path)
	var probeErr *mediatools.ProbeError
	switch {
	case ctx.Err() != nil:
	case err == nil && !mediatools.SystemError(info.Log, path):
		stored := info
		c.Store(st, KindProbe, probeResult{Info: &stored, Log: info.Log})
	case errors.As(err, &probeErr) && probeErr.Err != nil && mediatools.FileFailure(probeErr.Err, probeErr.Stderr, path):
		c.Store(st, KindProbe, probeResult{Log: info.Log,
			Error: &probeFailure{Message: probeErr.Err.Error(), Stderr: probeErr.Stderr}})
	}
	return info, err
}
