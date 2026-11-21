package check

import "github.com/nkreimeyer/library-manager/mediatools"

// parseProbe parses ffprobe JSON output the way probeMedia reads a probe.
func parseProbe(data []byte) (mediaInfo, error) {
	probe, err := mediatools.ParseProbe(data)
	if err != nil {
		return mediaInfo{}, err
	}
	return fromProbe(probe), nil
}
