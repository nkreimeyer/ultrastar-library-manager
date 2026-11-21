package fixaudio

import "github.com/nkreimeyer/library-manager/mediatools"

// parseProbe parses ffprobe JSON output the way probeAudio reads a probe.
func parseProbe(data []byte) (audioInfo, error) {
	probe, err := mediatools.ParseProbe(data)
	if err != nil {
		return audioInfo{}, err
	}
	return audioFromProbe(probe)
}
