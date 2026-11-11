package utils

import (
	"bytes"
	"encoding/binary"
	"errors"
	"fmt"
	"image"
	"image/gif"
	"image/jpeg"
	"image/png"
	"io"
	"math"
	"os"
	"path/filepath"
	"strings"
	"unicode"

	log "github.com/sirupsen/logrus"
	"golang.org/x/text/unicode/norm"

	"github.com/nkreimeyer/ultrastar-songs-sanitizer/fsops"
)

// Image header errors. Every error returned for the content of a file (as opposed to an
// I/O error such as a missing file) wraps exactly one of them, so callers can tell an
// image this package cannot measure apart from a broken one.
var (
	// ErrUnsupportedImage: the file is a known image format whose header this package
	// cannot read (TIFF, AVIF/HEIF, JPEG XL, JPEG 2000, PSD, QOI, and JPEGs that use
	// features image/jpeg lacks, such as arithmetic coding). The file may be fine.
	ErrUnsupportedImage = errors.New("unsupported image format")
	// ErrInvalidImage: the file is empty, truncated, has an unknown signature or an
	// inconsistent header. It is not a usable image.
	ErrInvalidImage = errors.New("invalid image")
)

// ImageHeader describes an image as far as its header tells.
type ImageHeader struct {
	Width, Height int
	// Format is "jpeg", "png", "gif", "bmp" or "webp". With ErrUnsupportedImage it names
	// the unsupported format ("tiff", "heif", ...).
	Format string
}

// headerPeekSize is the number of bytes read to identify the format. It covers the
// complete BMP and WebP headers; JPEG, PNG and GIF are passed on to the standard library.
const headerPeekSize = 32

// GetImageDimensions returns width and height of a JPEG, PNG, GIF, BMP or WebP image.
// Only the image header is read. See ReadImageHeader for the errors.
func GetImageDimensions(filePath string) (int, int, error) {
	h, err := ReadImageHeader(filePath)
	if err != nil {
		return 0, 0, err
	}
	return h.Width, h.Height, nil
}

// ReadImageHeader identifies the image format of filePath by its content (not its
// extension) and returns its dimensions. Errors about the content wrap
// ErrUnsupportedImage or ErrInvalidImage; for ErrUnsupportedImage, the returned header
// carries the detected Format. Width and Height are always positive on success.
func ReadImageHeader(filePath string) (ImageHeader, error) {
	file, err := os.Open(filePath)
	if err != nil {
		return ImageHeader{}, err
	}
	defer file.Close()
	return readImageHeader(file)
}

func readImageHeader(r io.Reader) (ImageHeader, error) {
	buf := make([]byte, headerPeekSize)
	n, err := io.ReadFull(r, buf)
	if err != nil && !errors.Is(err, io.ErrUnexpectedEOF) && !errors.Is(err, io.EOF) {
		return ImageHeader{}, err // a read error, not a content problem
	}
	b := buf[:n]
	if n == 0 {
		return ImageHeader{}, fmt.Errorf("%w: empty file", ErrInvalidImage)
	}

	var h ImageHeader
	switch {
	case bytes.HasPrefix(b, []byte("\xff\xd8\xff")):
		h, err = stdConfig("jpeg", jpeg.DecodeConfig, b, r)
	case bytes.HasPrefix(b, []byte("\x89PNG\r\n\x1a\n")):
		h, err = stdConfig("png", png.DecodeConfig, b, r)
	case bytes.HasPrefix(b, []byte("GIF87a")), bytes.HasPrefix(b, []byte("GIF89a")):
		h, err = stdConfig("gif", gif.DecodeConfig, b, r)
	case bytes.HasPrefix(b, []byte("BM")):
		h, err = bmpHeader(b)
	case bytes.HasPrefix(b, []byte("RIFF")):
		h, err = webpHeader(b)
	default:
		if format := unsupportedFormat(b); format != "" {
			return ImageHeader{Format: format}, fmt.Errorf("%w: %s", ErrUnsupportedImage, format)
		}
		return ImageHeader{}, fmt.Errorf("%w: unknown signature % x", ErrInvalidImage, b[:min(n, 8)])
	}
	if err != nil {
		return ImageHeader{Format: h.Format}, err // the format is set with ErrUnsupportedImage
	}
	if h.Width <= 0 || h.Height <= 0 {
		return ImageHeader{}, fmt.Errorf("%w: %s with dimensions %dx%d", ErrInvalidImage, h.Format, h.Width, h.Height)
	}
	return h, nil
}

// stdConfig reads the header with a standard library decoder, feeding it the bytes that
// were already read.
func stdConfig(format string, decode func(io.Reader) (image.Config, error), peeked []byte, rest io.Reader) (ImageHeader, error) {
	cfg, err := decode(io.MultiReader(bytes.NewReader(peeked), rest))
	var unsupported jpeg.UnsupportedError
	switch {
	case errors.As(err, &unsupported):
		// A JPEG feature image/jpeg lacks (arithmetic coding, 12-bit precision, unusual
		// subsampling): the file may well be fine. png.UnsupportedError is not used here,
		// because it reports headers that no valid PNG has (compression or filter method).
		return ImageHeader{Format: format}, fmt.Errorf("%w: %s header: %w", ErrUnsupportedImage, format, err)
	case err != nil:
		return ImageHeader{}, fmt.Errorf("%w: %s header: %w", ErrInvalidImage, format, err)
	}
	return ImageHeader{Width: cfg.Width, Height: cfg.Height, Format: format}, nil
}

// unsupportedFormat returns the name of a known image format this package cannot read,
// or "" if b does not start with one of their signatures.
func unsupportedFormat(b []byte) string {
	switch {
	case bytes.HasPrefix(b, []byte("II*\x00")), bytes.HasPrefix(b, []byte("MM\x00*")):
		return "tiff"
	case bytes.HasPrefix(b, []byte("\xff\x0a")), bytes.HasPrefix(b, []byte("\x00\x00\x00\x0cJXL \r\n\x87\n")):
		return "jxl"
	case bytes.HasPrefix(b, []byte("\x00\x00\x00\x0cjP  \r\n\x87\n")):
		return "jpeg2000"
	case bytes.HasPrefix(b, []byte("8BPS")):
		return "psd"
	case bytes.HasPrefix(b, []byte("qoif")):
		return "qoi"
	case len(b) >= 12 && string(b[4:8]) == "ftyp":
		switch string(b[8:12]) {
		case "avif", "avis":
			return "avif"
		case "heic", "heix", "hevc", "hevx", "heim", "heis", "mif1", "msf1":
			return "heif"
		}
	}
	return ""
}

// bmpHeader reads a Windows/OS/2 bitmap header: the 14-byte file header ("BM"), then the
// DIB header, whose size selects the layout. BITMAPCOREHEADER (12 bytes) stores uint16
// dimensions; BITMAPINFOHEADER and all later ones (≥ 16 bytes, including OS/2 2.x) store
// int32 dimensions, where a negative height marks a top-down bitmap.
func bmpHeader(b []byte) (ImageHeader, error) {
	const fileHeader = 14
	if len(b) < fileHeader+4 {
		return ImageHeader{}, fmt.Errorf("%w: truncated BMP header", ErrInvalidImage)
	}
	le := binary.LittleEndian
	size := le.Uint32(b[fileHeader:])
	switch {
	case size == 12:
		if len(b) < fileHeader+8 {
			return ImageHeader{}, fmt.Errorf("%w: truncated BMP core header", ErrInvalidImage)
		}
		return ImageHeader{Width: int(le.Uint16(b[18:])), Height: int(le.Uint16(b[20:])), Format: "bmp"}, nil
	case size >= 16:
		if len(b) < fileHeader+12 {
			return ImageHeader{}, fmt.Errorf("%w: truncated BMP info header", ErrInvalidImage)
		}
		w := int64(int32(le.Uint32(b[18:])))
		h := int64(int32(le.Uint32(b[22:])))
		if h < 0 {
			h = -h // top-down bitmap; -MinInt32 still fits in int64
		}
		if w > math.MaxInt32 || h > math.MaxInt32 {
			return ImageHeader{}, fmt.Errorf("%w: BMP dimensions %dx%d out of range", ErrInvalidImage, w, h)
		}
		return ImageHeader{Width: int(w), Height: int(h), Format: "bmp"}, nil
	default:
		return ImageHeader{}, fmt.Errorf("%w: BMP header size %d", ErrInvalidImage, size)
	}
}

// webpHeader reads the dimensions of a WebP file ("RIFF" size "WEBP", then the first
// chunk at offset 12): VP8 (lossy key frame: 3-byte frame tag, start code 9d 01 2a, 14-bit
// width and height), VP8L (lossless: signature 0x2f, 14-bit width-1 and height-1) or VP8X
// (extended: 24-bit canvas width-1 and height-1).
func webpHeader(b []byte) (ImageHeader, error) {
	if len(b) < 12 {
		return ImageHeader{}, fmt.Errorf("%w: truncated RIFF header", ErrInvalidImage)
	}
	if string(b[8:12]) != "WEBP" {
		return ImageHeader{}, fmt.Errorf("%w: RIFF file of type %q is not WebP", ErrInvalidImage, b[8:12])
	}
	if len(b) < 20 {
		return ImageHeader{}, fmt.Errorf("%w: truncated WebP header", ErrInvalidImage)
	}
	le := binary.LittleEndian
	chunk, data := string(b[12:16]), b[20:]
	h := ImageHeader{Format: "webp"}
	switch chunk {
	case "VP8 ":
		if len(data) < 10 {
			return ImageHeader{}, fmt.Errorf("%w: truncated WebP VP8 header", ErrInvalidImage)
		}
		if data[0]&1 != 0 {
			return ImageHeader{}, fmt.Errorf("%w: WebP VP8 data does not start with a key frame", ErrInvalidImage)
		}
		if !bytes.Equal(data[3:6], []byte{0x9d, 0x01, 0x2a}) {
			return ImageHeader{}, fmt.Errorf("%w: WebP VP8 start code missing", ErrInvalidImage)
		}
		h.Width = int(le.Uint16(data[6:]) & 0x3fff)
		h.Height = int(le.Uint16(data[8:]) & 0x3fff)
	case "VP8L":
		if len(data) < 5 {
			return ImageHeader{}, fmt.Errorf("%w: truncated WebP VP8L header", ErrInvalidImage)
		}
		if data[0] != 0x2f {
			return ImageHeader{}, fmt.Errorf("%w: WebP VP8L signature missing", ErrInvalidImage)
		}
		bits := le.Uint32(data[1:])
		if bits>>29 != 0 {
			return ImageHeader{}, fmt.Errorf("%w: WebP VP8L version %d", ErrInvalidImage, bits>>29)
		}
		h.Width = int(bits&0x3fff) + 1
		h.Height = int(bits>>14&0x3fff) + 1
	case "VP8X":
		if len(data) < 10 {
			return ImageHeader{}, fmt.Errorf("%w: truncated WebP VP8X header", ErrInvalidImage)
		}
		h.Width = int(uint32(data[4])|uint32(data[5])<<8|uint32(data[6])<<16) + 1
		h.Height = int(uint32(data[7])|uint32(data[8])<<8|uint32(data[9])<<16) + 1
	default:
		return ImageHeader{}, fmt.Errorf("%w: unknown WebP chunk %q", ErrInvalidImage, chunk)
	}
	return h, nil
}

// SquareTolerancePercent: an image counts as square (a cover) if its sides differ by less
// than this percentage of the longer side. 600x598 is square, 600x570 (exactly 5 %) is not.
const SquareTolerancePercent = 5

// IsSquareImage reports whether an image of w x h pixels is square within
// SquareTolerancePercent.
func IsSquareImage(w, h int) bool {
	if w <= 0 || h <= 0 {
		return false
	}
	diff := int64(w) - int64(h)
	if diff < 0 {
		diff = -diff
	}
	return diff*100 < SquareTolerancePercent*int64(max(w, h))
}

// imageKind is the role a file name hints at.
type imageKind int

const (
	kindNone imageKind = iota
	kindCover
	kindBackground
)

// Filename hints. Tags in square brackets are the UltraStar convention ("Song [CO].jpg",
// "Song [BG].jpg") and are strong hints. The words are weak hints: they only count as
// whole words (letters only; digits, punctuation, spaces and camelCase humps separate
// words, so "cover-art", "Cover2" and "SongCover" match, "discovery" and "covers" do not),
// and only beyond the words of the artist, title and folder name: a hint word counts if
// the file name has it more often than any of these names ("Take Cover.jpg" for the song
// "Take Cover" is no hint, "Take Cover cover.jpg" is). Counting single words, not whole
// names, also covers file names that repeat only part of a name ("Cover Me.png" for the
// title "Cover Me [DUET]", "Artist_-_Take_Cover_BG.png").
// "bgm" (background music) is no hint, and "back" alone is none either ("Back in Black").
var (
	coverTags       = []string{"[co]"}
	backgroundTags  = []string{"[bg]"}
	coverWords      = map[string]bool{"cover": true}
	backgroundWords = map[string]bool{"background": true, "bg": true}
)

// nameHint returns the role the file name hints at and whether the hint is strong. An
// image with conflicting hints of the same strength hints at nothing (kindNone with
// conflict set) and is never chosen, neither by hint nor by shape. A strong hint overrides
// a conflicting weak one. songNames are the folder name, artist, title and the like (see
// songNames): their words are only hints where the file name has them more often.
func nameHint(name string, songNames []string) (kind imageKind, strong, conflict bool) {
	base := normalizeName(strings.TrimSuffix(name, filepath.Ext(name)))
	co, bg := containsAny(base, coverTags), containsAny(base, backgroundTags)
	switch {
	case co && bg:
		return kindNone, true, true
	case co:
		return kindCover, true, false
	case bg:
		return kindBackground, true, false
	}

	counts := wordCounts(strings.TrimSuffix(name, filepath.Ext(name)))
	allowed := map[string]int{} // per word: the most times one song name has it
	for _, n := range songNames {
		for w, c := range wordCounts(n) {
			allowed[w] = max(allowed[w], c)
		}
	}
	for w, c := range counts {
		if c > allowed[w] {
			co = co || coverWords[w]
			bg = bg || backgroundWords[w]
		}
	}
	switch {
	case co && bg:
		return kindNone, false, true
	case co:
		return kindCover, false, false
	case bg:
		return kindBackground, false, false
	}
	return kindNone, false, false
}

// wordCounts counts the lower-cased words of s (see splitWords), compared in NFC.
func wordCounts(s string) map[string]int {
	counts := map[string]int{}
	for _, w := range splitWords(norm.NFC.String(s)) {
		counts[strings.ToLower(w)]++
	}
	return counts
}

func containsAny(s string, subs []string) bool {
	for _, sub := range subs {
		if strings.Contains(s, sub) {
			return true
		}
	}
	return false
}

// splitWords splits s into runs of letters, also splitting at a lower→upper case change
// ("SongCover" → "Song", "Cover"). Everything that is not a letter separates words.
func splitWords(s string) []string {
	var words []string
	var cur []rune
	var prev rune
	flush := func() {
		if len(cur) > 0 {
			words = append(words, string(cur))
			cur = cur[:0]
		}
	}
	for _, r := range s {
		switch {
		case !unicode.IsLetter(r):
			flush()
		case unicode.IsUpper(r) && unicode.IsLower(prev):
			flush()
			cur = append(cur, r)
		default:
			cur = append(cur, r)
		}
		prev = r
	}
	flush()
	return words
}

// imageCandidate is an image file of a song folder with what is known about it.
type imageCandidate struct {
	name             string
	kind             imageKind
	strong, conflict bool
	w, h             int
	readable         bool // dimensions known
	displayable      bool // unreadable, but in a format UltraStar can show (see showableUnsupported)
}

// showableUnsupported lists the formats this package cannot measure but UltraStar Deluxe
// can show (it loads images with SDL2_image, which reads TIFF and, through libjpeg-turbo,
// JPEGs image/jpeg cannot read, such as arithmetic-coded ones). A hinted image in one of
// these formats still counts for its hint; a hinted image that is corrupt (ErrInvalidImage)
// never does, because a broken cover is worse than none. Without a hint, an unreadable
// image is never chosen: its shape is unknown.
var showableUnsupported = map[string]bool{"tiff": true, "jpeg": true}

// imageCandidates returns the visible image files of dir, sorted by name, with their
// name hints and dimensions.
func imageCandidates(ops *fsops.Ops, dir string) []imageCandidate {
	names := mediaEntries(dir, IsImageFile)
	if len(names) == 0 {
		return nil
	}
	strip := songNames(ops, dir)
	cands := make([]imageCandidate, 0, len(names))
	for _, name := range names {
		c := imageCandidate{name: name}
		c.kind, c.strong, c.conflict = nameHint(name, strip)
		hdr, err := ReadImageHeader(filepath.Join(dir, name))
		switch {
		case err == nil:
			c.w, c.h, c.readable = hdr.Width, hdr.Height, true
		case errors.Is(err, ErrUnsupportedImage) && showableUnsupported[hdr.Format]:
			c.displayable = true
			log.Debugf("Cannot read the dimensions of %s in %s: %v", name, dir, err)
		default:
			log.Debugf("Could not get image dimensions of %s in %s: %v", name, dir, err)
		}
		cands = append(cands, c)
	}
	return cands
}

// songNames returns the folder name, #ARTIST, #TITLE and "#ARTIST #TITLE" of the song
// in dir, read through ops (see ReadSongTags). Their words are no file name hints (see
// nameHint).
func songNames(ops *fsops.Ops, dir string) []string {
	names := []string{filepath.Base(dir)}
	if tags, err := ReadSongTags(ops, dir); err == nil {
		artist, title := strings.TrimSpace(tags["ARTIST"]), strings.TrimSpace(tags["TITLE"])
		names = append(names, artist, title, artist+" "+title)
	}
	return names
}

// pickImage returns the first usable candidate hinted as kind (strong hints before weak
// ones), else the first readable candidate without any hint that the first shape
// predicate accepts, then the second, and so on.
func pickImage(cands []imageCandidate, kind imageKind, shapes ...func(w, h int) bool) string {
	for _, strong := range []bool{true, false} {
		for _, c := range cands {
			if c.kind == kind && c.strong == strong && (c.readable || c.displayable) {
				return c.name
			}
		}
	}
	for _, shape := range shapes {
		for _, c := range cands {
			// Images hinted as the other kind or with conflicting hints are never chosen by
			// shape; neither are images whose shape is unknown.
			if c.readable && c.kind == kindNone && !c.conflict && shape(c.w, c.h) {
				return c.name
			}
		}
	}
	return ""
}

// FindCoverImage returns the image of rootDir to use as cover, or "" if there is none.
// Images are considered in name order: first one with a strong cover hint ("[CO]"), then
// one with a weak hint (the word "cover"), then the first square image (IsSquareImage)
// without any hint. Images hinted as background are never the cover. See nameHint for the
// hints and showableUnsupported for hinted images whose dimensions cannot be read.
// FindCoverImage and FindBackgroundImage never return the same image. The song txt (whose
// #ARTIST/#TITLE words are no hints) is read through ops, which may be nil.
func FindCoverImage(ops *fsops.Ops, rootDir string) string {
	name := pickImage(imageCandidates(ops, rootDir), kindCover, IsSquareImage)
	logPick(rootDir, "cover", name)
	return name
}

// FindBackgroundImage returns the image of rootDir to use as background, or "" if there is
// none. Images are considered in name order: first one with a strong background hint
// ("[BG]"), then one with a weak hint (the words "background" or "bg"), then the first
// non-square landscape image without any hint, then the first non-square portrait image
// without any hint. Images hinted as cover are never the background. See FindCoverImage
// for ops.
func FindBackgroundImage(ops *fsops.Ops, rootDir string) string {
	name := pickImage(imageCandidates(ops, rootDir), kindBackground,
		func(w, h int) bool { return !IsSquareImage(w, h) && w > h },
		func(w, h int) bool { return !IsSquareImage(w, h) })
	logPick(rootDir, "background", name)
	return name
}

func logPick(dir, kind, name string) {
	if name == "" {
		log.Debugf("No %s image found in %s", kind, dir)
		return
	}
	log.Debugf("Found %s image %s in %s", kind, name, dir)
}
