package fixvideos

import (
	"context"

	"github.com/nkreimeyer/library-manager/mediatools"
)

// File-based forms of the probe-based helpers, as the tests use them: each probes file
// once, like processVideoFile, and returns the probe error where the helper has none.

func probeOf(file string) mediatools.ProbeInfo {
	probe, _ := mediatools.Probe(context.Background(), file)
	return probe
}

func getNeedsReEncode(file, codec string) bool {
	return needsReEncode(nil, file, codec, probeOf(file))
}

func getVideoBitrate(file string) (int, error) {
	probe, err := mediatools.Probe(context.Background(), file)
	if err != nil {
		return 0, err
	}
	return videoBitrate(nil, file, probe)
}

func estimatedSizeOfReEncodedVideo(file string) (int64, error) {
	probe, err := mediatools.Probe(context.Background(), file)
	if err != nil {
		return 0, err
	}
	return estimatedReEncodedSize(probe)
}

func getVideoFramerate(file string) (float64, error) {
	probe, err := mediatools.Probe(context.Background(), file)
	if err != nil {
		return 0, err
	}
	return videoFramerate(probe)
}

func getVideoDuration(file string) (float64, error) {
	probe, err := mediatools.Probe(context.Background(), file)
	if err != nil {
		return 0, err
	}
	return formatDuration(probe)
}

func getVideoResolution(file string) (int, int, error) {
	probe, err := mediatools.Probe(context.Background(), file)
	if err != nil {
		return 0, 0, err
	}
	return videoResolution(probe)
}

func videoStreamDuration(file string) (float64, error) {
	probe, err := mediatools.Probe(context.Background(), file)
	if err != nil {
		return 0, err
	}
	return videoDuration(probe), nil
}

// firstStream returns the first stream of ffprobe JSON output for one selected stream
// ("-select_streams v:0"), which carries no codec_type.
func firstStream(data []byte) (mediatools.Stream, bool, error) {
	probe, err := mediatools.ParseProbe(data)
	if err != nil || len(probe.Streams) == 0 {
		return mediatools.Stream{}, false, err
	}
	return probe.Streams[0], true, nil
}

// parseStreamBitrate applies the shared bitrate rule to the first stream of the JSON.
func parseStreamBitrate(data []byte) (int, bool) {
	probe, err := mediatools.ParseProbe(data)
	if err != nil || len(probe.Streams) == 0 {
		return 0, false
	}
	b := mediatools.VideoBitrate(probe, probe.Streams[0])
	return int(b), b > 0
}

func parseVideoStreamDuration(data []byte) (float64, error) {
	st, _, err := firstStream(data)
	if err != nil {
		return 0, err
	}
	return streamDuration(st), nil
}

func parseMarkerProbe(data []byte) (markerProbe, error) {
	probe, err := mediatools.ParseProbe(data)
	if err != nil {
		return markerProbe{}, err
	}
	return markerFromProbe(probe), nil
}

// planFile probes file and plans it, as processVideoFile does.
func (p *processor) planFile(dir, file string) (encodePlan, bool) {
	probe, err := mediatools.Probe(context.Background(), file)
	return p.plan(dir, file, probe, err)
}
