package fixaudio

import (
	"testing"

	"github.com/nkreimeyer/library-manager/mediatools"
)

// parseProbe parses ffprobe JSON output the way probeAudio reads a probe.
func parseProbe(data []byte) (audioInfo, error) {
	probe, err := mediatools.ParseProbe(data)
	if err != nil {
		return audioInfo{}, err
	}
	return audioFromProbe(probe)
}

// Only the container's marker or the processed (first audio) stream's marker means the
// file was normalized; a marker on another audio stream does not.
func TestNormalizedMarkerOfProcessedStream(t *testing.T) {
	stream := func(tags string) string {
		return `{"codec_type":"audio","codec_name":"vorbis","channels":2,"sample_fmt":"fltp","tags":{` + tags + `}}`
	}
	tag := `"SANITIZER_NORMALIZED":"-3.0dB"`
	for _, tt := range []struct {
		name, json string
		want       bool
	}{
		{"none", `{"streams":[` + stream("") + `,` + stream("") + `]}`, false},
		{"container", `{"streams":[` + stream("") + `],"format":{"tags":{` + tag + `}}}`, true},
		{"first stream", `{"streams":[` + stream(tag) + `,` + stream("") + `]}`, true},
		{"first stream, lower case", `{"streams":[` + stream(`"sanitizer_normalized":"x"`) + `]}`, true},
		{"second stream only", `{"streams":[` + stream("") + `,` + stream(tag) + `]}`, false},
		{"a video stream before the audio", `{"streams":[{"codec_type":"video","tags":{` + tag + `}},` + stream("") + `]}`, false},
	} {
		probe, err := mediatools.ParseProbe([]byte(tt.json))
		if err != nil {
			t.Fatal(err)
		}
		info, err := audioFromProbe(probe)
		if err != nil || info.Normalized != tt.want {
			t.Errorf("%s: Normalized = %v, %v; want %v", tt.name, info.Normalized, err, tt.want)
		}
	}
}
