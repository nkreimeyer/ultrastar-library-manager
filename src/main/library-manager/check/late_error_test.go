package check

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
)

// A system error that ffmpeg prints after more than maxStderr of per-frame decode errors
// must still keep the result out of the cache: the decision looks at the tail too.
func TestLateSystemErrorIsNoFileResult(t *testing.T) {
	const firstLine = "[h264 @ 0x1] error while decoding MB 1 2"
	for _, exit := range []string{"0", "1"} {
		t.Run("exit "+exit, func(t *testing.T) {
			// About 150 KiB of decode errors, then a read error of the file system.
			fakeTools(t, map[string]string{"ffmpeg": `i=0
while [ $i -lt 2000 ]; do
  echo "[h264 @ 0x1] error while decoding MB 1 2, bytestream -12345678901234567890" >&2
  i=$((i+1))
done
echo "Error during demuxing: Input/output error" >&2
exit ` + exit + "\n"})
			path := filepath.Join(t.TempDir(), "v.mp4")
			decErr, decWarn, fileResult, err := decodeCheck(context.Background(), path)
			if err != nil {
				t.Fatal(err)
			}
			if fileResult {
				t.Error("decodeCheck: fileResult = true despite the late Input/output error")
			}
			if msg := decErr + decWarn; !strings.HasPrefix(msg, strings.ReplaceAll(firstLine, " @ 0x1", "")) {
				t.Errorf("decodeCheck message = %q, want the first lines", msg)
			}
			if _, _, fileResult := measureDuration(context.Background(), path); fileResult {
				t.Error("measureDuration: fileResult = true despite the late Input/output error")
			}
		})
	}
}
