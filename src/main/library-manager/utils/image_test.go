package utils

import (
	"bytes"
	"encoding/binary"
	"errors"
	"image"
	"image/gif"
	"image/jpeg"
	"image/png"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/nkreimeyer/library-manager/internal/testutil"
)

// --- header builders -------------------------------------------------------------------

func le16(v uint16) []byte       { return binary.LittleEndian.AppendUint16(nil, v) }
func le32(v uint32) []byte       { return binary.LittleEndian.AppendUint32(nil, v) }
func cat(parts ...[]byte) []byte { return bytes.Join(parts, nil) }

// bmpCore returns a BMP with a 12-byte BITMAPCOREHEADER.
func bmpCore(w, h uint16) []byte {
	return cat([]byte("BM"), le32(0), le32(0), le32(26), le32(12), le16(w), le16(h), le16(1), le16(24))
}

// bmpInfo returns a BMP with a DIB header of the given size (40 = BITMAPINFOHEADER).
func bmpInfo(size uint32, w, h int32) []byte {
	b := cat([]byte("BM"), le32(0), le32(0), le32(14+size), le32(size), le32(uint32(w)), le32(uint32(h)), le16(1), le16(24))
	return append(b, make([]byte, int(size)-16)...)
}

func riff(chunk string, data []byte) []byte {
	return cat([]byte("RIFF"), le32(uint32(12+len(data))), []byte("WEBP"), []byte(chunk), le32(uint32(len(data))), data)
}

// webpVP8 returns a lossy WebP: frame tag (key frame), start code, 14-bit dims (the top
// two bits are the scale and must be ignored).
func webpVP8(w, h uint16) []byte {
	return riff("VP8 ", cat([]byte{0x50, 0x02, 0x00}, []byte{0x9d, 0x01, 0x2a}, le16(w|0xc000), le16(h|0x4000), make([]byte, 8)))
}

func webpVP8L(w, h uint32) []byte {
	return riff("VP8L", cat([]byte{0x2f}, le32((w-1)|(h-1)<<14|1<<28), make([]byte, 8)))
}

func webpVP8X(w, h uint32) []byte {
	w, h = w-1, h-1
	return riff("VP8X", cat([]byte{0x10, 0, 0, 0}, []byte{byte(w), byte(w >> 8), byte(w >> 16), byte(h), byte(h >> 8), byte(h >> 16)}))
}

func encoded(t testing.TB, enc func(*bytes.Buffer, image.Image) error, w, h int) []byte {
	t.Helper()
	var buf bytes.Buffer
	if err := enc(&buf, image.NewGray(image.Rect(0, 0, w, h))); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func pngBytes(t testing.TB, w, h int) []byte {
	return encoded(t, func(b *bytes.Buffer, m image.Image) error { return png.Encode(b, m) }, w, h)
}

func jpegBytes(t testing.TB, w, h int) []byte {
	return encoded(t, func(b *bytes.Buffer, m image.Image) error { return jpeg.Encode(b, m, nil) }, w, h)
}

func gifBytes(t testing.TB, w, h int) []byte {
	return encoded(t, func(b *bytes.Buffer, m image.Image) error { return gif.Encode(b, m, nil) }, w, h)
}

var tiffLE = []byte("II*\x00\x08\x00\x00\x00")

// --- header tests ----------------------------------------------------------------------

func TestReadImageHeader(t *testing.T) {
	png := pngBytes(t, 7, 5)
	tests := []struct {
		name   string
		data   []byte
		w, h   int
		format string
		want   error // nil, ErrInvalidImage or ErrUnsupportedImage
	}{
		{"png", png, 7, 5, "png", nil},
		{"jpeg", jpegBytes(t, 9, 4), 9, 4, "jpeg", nil},
		{"gif", gifBytes(t, 3, 8), 3, 8, "gif", nil},
		{"bmp core header", bmpCore(300, 200), 300, 200, "bmp", nil},
		{"bmp info header", bmpInfo(40, 640, 480), 640, 480, "bmp", nil},
		{"bmp top-down", bmpInfo(40, 640, -480), 640, 480, "bmp", nil},
		{"bmp v5 header", bmpInfo(124, 1920, 1080), 1920, 1080, "bmp", nil},
		{"bmp os/2 v2 short header", bmpInfo(16, 10, 20), 10, 20, "bmp", nil},
		{"bmp min int32 height", bmpInfo(40, 1, -1<<31), 0, 0, "", ErrInvalidImage},
		{"bmp zero width", bmpInfo(40, 0, 10), 0, 0, "", ErrInvalidImage},
		{"bmp negative width", bmpInfo(40, -5, 10), 0, 0, "", ErrInvalidImage},
		{"bmp zero core height", bmpCore(5, 0), 0, 0, "", ErrInvalidImage},
		{"bmp bad header size", cat(bmpInfo(40, 5, 5)[:14], le32(8), make([]byte, 20)), 0, 0, "", ErrInvalidImage},
		{"bmp truncated", bmpInfo(40, 640, 480)[:22], 0, 0, "", ErrInvalidImage},
		{"bmp truncated core", bmpCore(3, 3)[:20], 0, 0, "", ErrInvalidImage},
		{"bmp signature only", []byte("BM"), 0, 0, "", ErrInvalidImage},
		{"webp lossy", webpVP8(1280, 720), 1280, 720, "webp", nil},
		{"webp lossy max", webpVP8(16383, 16383), 16383, 16383, "webp", nil},
		{"webp lossless", webpVP8L(301, 203), 301, 203, "webp", nil},
		{"webp lossless max", webpVP8L(16384, 16384), 16384, 16384, "webp", nil},
		{"webp extended", webpVP8X(4000, 3000), 4000, 3000, "webp", nil},
		{"webp extended max", webpVP8X(1<<24, 1), 1 << 24, 1, "webp", nil},
		{"webp lossy interframe", func() []byte { b := webpVP8(5, 5); b[20] |= 1; return b }(), 0, 0, "", ErrInvalidImage},
		{"webp lossy no start code", func() []byte { b := webpVP8(5, 5); b[24] = 0; return b }(), 0, 0, "", ErrInvalidImage},
		{"webp lossy zero width", webpVP8(0, 5), 0, 0, "", ErrInvalidImage},
		{"webp lossless bad signature", func() []byte { b := webpVP8L(5, 5); b[20] = 0x2e; return b }(), 0, 0, "", ErrInvalidImage},
		{"webp lossless bad version", func() []byte { b := webpVP8L(5, 5); b[24] |= 0x20; return b }(), 0, 0, "", ErrInvalidImage},
		{"webp unknown chunk", riff("ALPH", make([]byte, 10)), 0, 0, "", ErrInvalidImage},
		{"webp truncated chunk", webpVP8(5, 5)[:25], 0, 0, "", ErrInvalidImage},
		{"webp truncated lossless", webpVP8L(5, 5)[:23], 0, 0, "", ErrInvalidImage},
		{"webp truncated extended", webpVP8X(5, 5)[:28], 0, 0, "", ErrInvalidImage},
		{"webp truncated riff", webpVP8(5, 5)[:16], 0, 0, "", ErrInvalidImage},
		{"riff but avi", cat([]byte("RIFF"), le32(100), []byte("AVI LIST")), 0, 0, "", ErrInvalidImage},
		{"tiff little endian", tiffLE, 0, 0, "tiff", ErrUnsupportedImage},
		{"tiff big endian", []byte("MM\x00*\x00\x00\x00\x08"), 0, 0, "tiff", ErrUnsupportedImage},
		{"heic", cat(le32(0x18000000), []byte("ftypheic"), make([]byte, 8)), 0, 0, "heif", ErrUnsupportedImage},
		{"avif", cat(le32(0x1c000000), []byte("ftypavif"), make([]byte, 8)), 0, 0, "avif", ErrUnsupportedImage},
		{"mp4 is no image", cat(le32(0x18000000), []byte("ftypisom"), make([]byte, 8)), 0, 0, "", ErrInvalidImage},
		{"jpeg xl", []byte{0xff, 0x0a, 0xfa}, 0, 0, "jxl", ErrUnsupportedImage},
		{"psd", []byte("8BPS\x00\x01"), 0, 0, "psd", ErrUnsupportedImage},
		{"empty", nil, 0, 0, "", ErrInvalidImage},
		{"text", []byte("cover"), 0, 0, "", ErrInvalidImage},
		{"truncated png", png[:20], 0, 0, "", ErrInvalidImage},
		{"truncated jpeg", []byte("\xff\xd8\xff\xe0"), 0, 0, "", ErrInvalidImage},
		{"zero size gif", []byte("GIF89a\x00\x00\x00\x00\x00\x00\x00"), 0, 0, "", ErrInvalidImage},
	}
	dir := t.TempDir()
	for i, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			path := filepath.Join(dir, string(rune('a'+i%26))+strings.Repeat("x", i/26)+".img")
			if err := os.WriteFile(path, tt.data, 0o644); err != nil {
				t.Fatal(err)
			}
			h, err := ReadImageHeader(path)
			if tt.want == nil {
				if err != nil {
					t.Fatalf("ReadImageHeader: %v", err)
				}
				if h != (ImageHeader{Width: tt.w, Height: tt.h, Format: tt.format}) {
					t.Errorf("header = %+v, want %dx%d %s", h, tt.w, tt.h, tt.format)
				}
				w, hh, err := GetImageDimensions(path)
				if err != nil || w != tt.w || hh != tt.h {
					t.Errorf("GetImageDimensions = %d, %d, %v", w, hh, err)
				}
				return
			}
			if !errors.Is(err, tt.want) {
				t.Fatalf("err = %v, want %v", err, tt.want)
			}
			other := ErrInvalidImage
			if tt.want == ErrInvalidImage {
				other = ErrUnsupportedImage
			}
			if errors.Is(err, other) {
				t.Errorf("err %v wraps both sentinel errors", err)
			}
			if h.Format != tt.format || h.Width != 0 || h.Height != 0 {
				t.Errorf("header on error = %+v, want format %q only", h, tt.format)
			}
			if w, hh, err := GetImageDimensions(path); err == nil || w != 0 || hh != 0 {
				t.Errorf("GetImageDimensions = %d, %d, %v", w, hh, err)
			}
		})
	}
}

func TestReadImageHeaderMissingFile(t *testing.T) {
	_, err := ReadImageHeader(filepath.Join(t.TempDir(), "gone.png"))
	if !errors.Is(err, fs.ErrNotExist) || errors.Is(err, ErrInvalidImage) || errors.Is(err, ErrUnsupportedImage) {
		t.Errorf("err = %v, want a plain not-exist error", err)
	}
}

// TestReadImageHeaderFFmpeg checks the hand-written readers against files written by real
// encoders.
func TestReadImageHeaderFFmpeg(t *testing.T) {
	testutil.RequireTool(t, "ffmpeg")
	dir := t.TempDir()
	tests := []struct {
		file, input, pixFmt string
		args                []string
		w, h                int
		format              string
	}{
		// ",format=rgb24": without a format filter, the color source of ffmpeg 6.1 makes
		// yuv420p frames and rounds odd sizes down to even ones (301x203 became 300x202).
		{"a.bmp", "color=c=red:s=301x203,format=rgb24", "bgr24", nil, 301, 203, "bmp"},
		{"b.bmp", "color=c=red:s=64x48", "pal8", nil, 64, 48, "bmp"},
		{"lossy.webp", "color=c=red:s=301x203,format=rgb24", "yuv420p", []string{"-c:v", "libwebp"}, 301, 203, "webp"},
		{"lossless.webp", "color=c=red:s=1921x1081,format=rgb24", "", []string{"-c:v", "libwebp", "-lossless", "1"}, 1921, 1081, "webp"},
		// Several different frames make libwebp_anim write an extended (VP8X) file.
		{"anim.webp", "testsrc=s=641x361:r=5", "", []string{"-frames:v", "3", "-c:v", "libwebp_anim"}, 641, 361, "webp"},
		{"c.tiff", "color=c=red:s=10x10", "rgb24", nil, 0, 0, "tiff"},
	}
	for _, tt := range tests {
		t.Run(tt.file, func(t *testing.T) {
			path := filepath.Join(dir, tt.file)
			args := []string{"-f", "lavfi", "-i", tt.input}
			if !slices.Contains(tt.args, "-frames:v") {
				args = append(args, "-frames:v", "1")
			}
			if tt.pixFmt != "" {
				args = append(args, "-pix_fmt", tt.pixFmt)
			}
			args = append(append(args, tt.args...), path)
			testutil.GenerateMedia(t, args...)
			if tt.file == "anim.webp" {
				if b, _ := os.ReadFile(path); len(b) < 16 || string(b[12:16]) != "VP8X" {
					t.Fatalf("ffmpeg did not write a VP8X file: % x", b[:min(len(b), 16)])
				}
			}
			h, err := ReadImageHeader(path)
			if tt.format == "tiff" {
				if !errors.Is(err, ErrUnsupportedImage) || h.Format != "tiff" {
					t.Errorf("tiff: %+v, %v", h, err)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if h != (ImageHeader{Width: tt.w, Height: tt.h, Format: tt.format}) {
				t.Errorf("header = %+v, want %dx%d %s", h, tt.w, tt.h, tt.format)
			}
		})
	}
}

func FuzzReadImageHeader(f *testing.F) {
	for _, seed := range [][]byte{
		pngBytes(f, 3, 3), jpegBytes(f, 3, 3), gifBytes(f, 3, 3), bmpCore(3, 3), bmpInfo(40, 3, -3),
		webpVP8(3, 3), webpVP8L(3, 3), webpVP8X(3, 3), tiffLE, []byte("RIFF"), []byte("BM"), nil,
	} {
		f.Add(seed)
	}
	f.Fuzz(func(t *testing.T, data []byte) {
		h, err := readImageHeader(bytes.NewReader(data))
		if err == nil {
			if h.Width <= 0 || h.Height <= 0 || h.Format == "" {
				t.Errorf("success with header %+v", h)
			}
			return
		}
		if errors.Is(err, ErrInvalidImage) == errors.Is(err, ErrUnsupportedImage) {
			t.Errorf("err %v must wrap exactly one of ErrInvalidImage and ErrUnsupportedImage", err)
		}
	})
}

// --- cover and background --------------------------------------------------------------

func TestIsSquareImage(t *testing.T) {
	for _, tt := range []struct {
		w, h int
		want bool
	}{
		{600, 600, true},
		{600, 598, true},
		{598, 600, true},
		{600, 571, true},  // 4.8 %
		{600, 570, false}, // exactly 5 %
		{570, 600, false},
		{1000, 951, true},
		{1000, 950, false},
		{4, 4, true},
		{4, 3, false},
		{1920, 1080, false},
		{1080, 1920, false},
		{0, 0, false},
		{-5, -5, false},
		{1 << 30, 1<<30 - 1, true},
	} {
		if got := IsSquareImage(tt.w, tt.h); got != tt.want {
			t.Errorf("IsSquareImage(%d, %d) = %v, want %v", tt.w, tt.h, got, tt.want)
		}
	}
}

func TestNameHint(t *testing.T) {
	song := []string{"artist - take cover", "take cover", "artist"}
	tests := []struct {
		name     string
		strip    []string
		kind     imageKind
		strong   bool
		conflict bool
	}{
		{"Song [CO].jpg", nil, kindCover, true, false},
		{"song [co].JPG", nil, kindCover, true, false},
		{"Song [BG].jpg", nil, kindBackground, true, false},
		{"Song[bg].png", nil, kindBackground, true, false},
		{"Song [CO][BG].jpg", nil, kindNone, true, true},
		{"cover.jpg", nil, kindCover, false, false},
		{"Cover.png", nil, kindCover, false, false},
		{"song cover.jpg", nil, kindCover, false, false},
		{"cover-art.jpg", nil, kindCover, false, false},
		{"cover_2.jpg", nil, kindCover, false, false},
		{"Cover2.jpg", nil, kindCover, false, false},
		{"SongCover.jpg", nil, kindCover, false, false},
		{"COVER.JPG", nil, kindCover, false, false},
		{"discovery.jpg", nil, kindNone, false, false},
		{"DISCOVERY.jpg", nil, kindNone, false, false},
		{"covers.jpg", nil, kindNone, false, false},
		{"undercover.jpg", nil, kindNone, false, false},
		{"background.jpg", nil, kindBackground, false, false},
		{"Song Background.png", nil, kindBackground, false, false},
		{"song bg.jpg", nil, kindBackground, false, false},
		{"song_BG.jpg", nil, kindBackground, false, false},
		{"SongBG.jpg", nil, kindBackground, false, false},
		{"bg1.jpg", nil, kindBackground, false, false},
		{"bgm.jpg", nil, kindNone, false, false},
		{"Back in Black.jpg", nil, kindNone, false, false},
		{"back.jpg", nil, kindNone, false, false},
		{"cover background.jpg", nil, kindNone, false, true},
		// A strong hint overrides a conflicting weak one.
		{"cover [BG].jpg", nil, kindBackground, true, false},
		{"background [CO].jpg", nil, kindCover, true, false},
		// Words of the artist, title and folder name are no hints.
		{"Artist - Take Cover.jpg", song, kindNone, false, false},
		{"Artist - Take Cover [BG].jpg", song, kindBackground, true, false},
		{"Artist - Take Cover bg.jpg", song, kindBackground, false, false},
		{"ARTIST - TAKE COVER cover.jpg", song, kindCover, false, false},
		{"Take Cover.jpg", song, kindNone, false, false},
		{"Arti\u0301st - Take Cover.jpg", []string{normalizeName("Art\u00edst - take cover")}, kindNone, false, false},
		// Only whole words count: the folder "A" must not cut "background" apart.
		{"background.jpg", []string{"a"}, kindBackground, false, false},
		{"A cover.jpg", []string{"a"}, kindCover, false, false},
		{"bgcover.jpg", []string{"cover"}, kindNone, false, false},
		// Song names are compared word by word, not as a whole: the file name may repeat
		// only part of them, or with other separators.
		{"Bruce Springsteen - Cover Me.png", []string{"Bruce Springsteen - Cover Me [DUET]", "Cover Me [DUET]"}, kindNone, false, false},
		{"Artist_-_Take_Cover_BG.png", []string{"Song", "Artist", "Take Cover"}, kindBackground, false, false},
		{"Artist_-_Take_Cover.png", []string{"Song", "Artist", "Take Cover"}, kindNone, false, false},
		{"Under Cover.png", []string{"The Strokes - Under Cover of Darkness (Radio Edit)"}, kindNone, false, false},
		{"Cover Me cover.png", []string{"Cover Me [DUET]"}, kindCover, false, false},
		{"BG - Background.png", []string{"Background"}, kindBackground, false, false},
	}
	for _, tt := range tests {
		kind, strong, conflict := nameHint(tt.name, tt.strip)
		if kind != tt.kind || strong != tt.strong || conflict != tt.conflict {
			t.Errorf("nameHint(%q, %q) = %v, %v, %v, want %v, %v, %v",
				tt.name, tt.strip, kind, strong, conflict, tt.kind, tt.strong, tt.conflict)
		}
	}
}

func TestFindCoverAndBackgroundImage(t *testing.T) {
	sq := func(n int) []byte { return pngBytes(t, n, n) }
	img := func(w, h int) []byte { return pngBytes(t, w, h) }
	wide, tall := img(64, 36), img(36, 64)
	tests := []struct {
		name      string
		files     map[string][]byte
		txt       string // song txt of the folder, "" for none
		cover, bg string
	}{
		{"none", nil, "", "", ""},
		{"no images", map[string][]byte{"song.mp3": []byte("a")}, "", "", ""},
		{"by shape", map[string][]byte{"a.png": wide, "b.png": sq(10)}, "", "b.png", "a.png"},
		{"first by name", map[string][]byte{"b.png": sq(10), "a.png": sq(20), "d.png": wide, "c.png": img(80, 30)}, "", "a.png", "c.png"},
		{"near square is a cover", map[string][]byte{"a.png": img(600, 598)}, "", "a.png", ""},
		{"5 percent off is not square", map[string][]byte{"a.png": img(600, 570)}, "", "", "a.png"},
		{"near square portrait", map[string][]byte{"a.png": img(598, 600)}, "", "a.png", ""},
		{"landscape before portrait", map[string][]byte{"a.png": tall, "b.png": wide}, "", "", "b.png"},
		{"portrait if nothing else", map[string][]byte{"a.png": tall}, "", "", "a.png"},
		{"strong hints win over shape", map[string][]byte{
			"a.png": sq(10), "b.png": wide, "Song [CO].png": wide, "Song [BG].png": sq(10),
		}, "", "Song [CO].png", "Song [BG].png"},
		{"weak hints win over shape", map[string][]byte{
			"a.png": sq(10), "b.png": wide, "cover.png": wide, "song bg.png": sq(10),
		}, "", "cover.png", "song bg.png"},
		{"strong hint before weak hint", map[string][]byte{
			"a cover.png": sq(10), "b [CO].png": sq(10), "a background.png": wide, "b [BG].png": wide,
		}, "", "b [CO].png", "b [BG].png"},
		{"hinted images are never chosen for the other kind", map[string][]byte{
			"background.png": sq(10), "cover.png": wide,
		}, "", "cover.png", "background.png"},
		{"hint excludes from the other kind's shape choice", map[string][]byte{
			"a [BG].png": sq(10), "b.png": sq(5), "c [CO].png": []byte("corrupt"), "d cover.png": tall,
		}, "", "d cover.png", "a [BG].png"},
		{"background hint excludes from the cover shape choice", map[string][]byte{
			"a [BG].png": sq(10), "b.png": sq(5),
		}, "", "b.png", "a [BG].png"},
		{"cover hinted background candidate", map[string][]byte{"a cover.png": wide}, "", "a cover.png", ""},
		{"both hints: never chosen", map[string][]byte{"x [CO] [BG].png": sq(10), "y cover background.png": wide}, "", "", ""},
		{"both hints: shape fallback skips it", map[string][]byte{"a [CO][BG].png": sq(10), "b.png": sq(10)}, "", "b.png", ""},
		{"title words are no hints", map[string][]byte{
			"Artist - Take Cover.png": wide, "Artist - Take Cover sq.png": sq(10),
		}, "#ARTIST:Artist\n#TITLE:Take Cover\n#MP3:a.mp3\n", "Artist - Take Cover sq.png", "Artist - Take Cover.png"},
		{"folder name words are no hints", map[string][]byte{
			"Song - Background Noise.png": sq(10),
		}, "", "Song - Background Noise.png", ""},
		// Regressions: file names that repeat only part of the title or folder name.
		{"title with a qualifier", map[string][]byte{
			"Bruce Springsteen - Cover Me [CO].png": sq(10), "Bruce Springsteen - Cover Me.png": wide,
		}, "#ARTIST:Bruce Springsteen\n#TITLE:Cover Me [DUET]\n#MP3:a.mp3\n", "Bruce Springsteen - Cover Me [CO].png", "Bruce Springsteen - Cover Me.png"},
		{"folder name with a qualifier", map[string][]byte{
			"The Strokes - Under Cover of Darkness [CO].png": sq(10), "The Strokes - Under Cover of Darkness.png": wide,
		}, "", "The Strokes - Under Cover of Darkness [CO].png", "The Strokes - Under Cover of Darkness.png"},
		{"underscores", map[string][]byte{
			"Artist_-_Take_Cover_BG.png": wide, "Artist_-_Take_Cover.png": sq(10),
		}, "#ARTIST:Artist\n#TITLE:Take Cover\n#MP3:a.mp3\n", "Artist_-_Take_Cover.png", "Artist_-_Take_Cover_BG.png"},
		{"corrupt hinted image is never chosen", map[string][]byte{
			"cover.jpg": []byte("not an image"), "a.png": sq(10), "[BG].png": []byte("x"), "b.png": wide,
		}, "", "a.png", "b.png"},
		{"unsupported but showable hinted image counts", map[string][]byte{
			"cover.tiff": tiffLE, "a.png": sq(10), "Song [BG].tif": tiffLE, "b.png": wide,
		}, "", "cover.tiff", "Song [BG].tif"},
		{"unsupported, not showable hinted image does not count", map[string][]byte{
			"cover.jpg": cat(le32(0x18000000), []byte("ftypheic"), make([]byte, 8)), "a.png": sq(10),
		}, "", "a.png", ""},
		{"unhinted unreadable images are never chosen", map[string][]byte{"a.tiff": tiffLE, "b.jpg": []byte("x")}, "", "", ""},
		{"webp and bmp", map[string][]byte{"a.webp": webpVP8(500, 500), "b.bmp": bmpInfo(40, 1920, -1080)}, "", "a.webp", "b.bmp"},
		{"jfif", map[string][]byte{"a.jfif": jpegBytes(t, 20, 20)}, "", "a.jfif", ""},
		{"non-image files and hidden files are ignored", map[string][]byte{
			"._cover.png": sq(10), "cover.txt": sq(10), ".bg.png": wide, "x.png": sq(3),
		}, "", "x.png", ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			folder := "Song"
			switch tt.name {
			case "folder name words are no hints":
				folder = "Song - Background Noise"
			case "folder name with a qualifier":
				folder = "The Strokes - Under Cover of Darkness (Radio Edit)"
			}
			dir := filepath.Join(t.TempDir(), folder)
			if err := os.Mkdir(dir, 0o755); err != nil {
				t.Fatal(err)
			}
			for name, data := range tt.files {
				if err := os.WriteFile(filepath.Join(dir, name), data, 0o644); err != nil {
					t.Fatal(err)
				}
			}
			if tt.txt != "" {
				testutil.WriteFile(t, filepath.Join(dir, "song.txt"), tt.txt)
			}
			cover, bg := FindCoverImage(nil, dir), FindBackgroundImage(nil, dir)
			if cover != tt.cover || bg != tt.bg {
				t.Errorf("cover, background = %q, %q, want %q, %q", cover, bg, tt.cover, tt.bg)
			}
			if cover != "" && cover == bg {
				t.Errorf("%q is both cover and background", cover)
			}
		})
	}
	if got := FindCoverImage(nil, filepath.Join(t.TempDir(), "missing")); got != "" {
		t.Errorf("FindCoverImage on a missing dir = %q", got)
	}
}
