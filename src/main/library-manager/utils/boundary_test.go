package utils

import (
	"bytes"
	"errors"
	"reflect"
	"strings"
	"testing"
)

// Boundary tests: each case sits exactly on a comparison in decode.go, image.go or
// songname.go, so moving the boundary by one changes the result.

// TestDecodeTxtBoundaries pins the code point and byte limits of the decoder.
func TestDecodeTxtBoundaries(t *testing.T) {
	tests := []struct {
		name        string
		raw         string
		wantEnc     string
		wantProblem string // substring; "" = no Problem
		wantText    string // substring of the decoded text; "" = not checked
	}{
		{
			// U+0080 (C2 80) is the first non-ASCII code point. CP1252 has no character for
			// it (byte 0x80 is "€"), so a UTF-8 file declaring CP1252 cannot be CP1252.
			name:        "U+0080 in a valid UTF-8 file declaring CP1252",
			raw:         "#ENCODING:CP1252\n#TITLE:a\u0080b\n",
			wantEnc:     EncUTF8,
			wantProblem: "cannot represent",
		},
		{
			name:     "U+007F in a valid UTF-8 file declaring CP1252 is ASCII",
			raw:      "#ENCODING:CP1252\n#TITLE:a\u007fb\n",
			wantEnc:  EncUTF8,
			wantText: "a\u007fb",
		},
		{
			// DC 80 is U+0700, the first code point of the Syriac block: CP1252 "Ü€".
			name:     "isolated U+0700 is CP1252 Ü€",
			raw:      "#TITLE:A\xDC\x80B\n",
			wantEnc:  EncCP1252,
			wantText: "AÜ€B",
		},
		{
			// DF BF is U+07FF, the last code point of the NKo block: CP1252 "ß¿". ("¿"
			// between two letters would look like CP1250, see looksLikeCP1250.)
			name:     "isolated U+07FF is CP1252 ß¿",
			raw:      "#TITLE:A\xDF\xBF B\n",
			wantEnc:  EncCP1252,
			wantText: "Aß¿ B",
		},
		{
			// D0 A0 (U+0420 "Р") would be an accidental pair if it were isolated, but byte
			// 0x80 ("€") before it is not ASCII: it is no isolated sequence and counts as
			// UTF-8 evidence.
			name:        "UTF-8 sequence after byte 0x80",
			raw:         "#TITLE:\x80\xD0\xA0x\n",
			wantEnc:     EncCP1252,
			wantProblem: "mixes UTF-8",
		},
		{
			name:        "UTF-8 sequence before byte 0x80",
			raw:         "#TITLE:x\xD0\xA0\x80\n",
			wantEnc:     EncCP1252,
			wantProblem: "mixes UTF-8",
		},
		{
			name:     "control: isolated D0 A0 in CP1252 text is accidental",
			raw:      "#TITLE:\xE9 x\xD0\xA0x\n",
			wantEnc:  EncCP1252,
			wantText: "é xÐ x",
		},
		{
			// C2 is the lowest UTF-8 lead byte: "Â" + NBSP in CP1252 is the classic
			// mojibake of a UTF-8 NBSP.
			name:        "C2 A0 in CP1252 text is UTF-8 evidence",
			raw:         "#TITLE:\xE9 \xC2\xA0\n",
			wantEnc:     EncCP1252,
			wantProblem: "mixes UTF-8",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			d := DecodeTxt([]byte(tt.raw))
			if d.Encoding != tt.wantEnc {
				t.Errorf("Encoding = %q, want %q (Problem %q)", d.Encoding, tt.wantEnc, d.Problem)
			}
			switch {
			case tt.wantProblem == "" && d.Problem != "":
				t.Errorf("unexpected Problem %q", d.Problem)
			case tt.wantProblem != "" && !strings.Contains(d.Problem, tt.wantProblem):
				t.Errorf("Problem = %q, want it to contain %q", d.Problem, tt.wantProblem)
			}
			if tt.wantText != "" && !strings.Contains(d.Text, tt.wantText) {
				t.Errorf("Text = %q, want it to contain %q", d.Text, tt.wantText)
			}
		})
	}
}

// TestUTF8Sequences checks the isolated/clean flags of utf8Sequences against its
// documented contract, including a sequence right after a byte that starts no sequence.
func TestUTF8Sequences(t *testing.T) {
	tests := []struct {
		raw  string
		want []utf8Seq
	}{
		{"\xC3\xA4", []utf8Seq{{lead: 0xC3, size: 2, isolated: true, clean: true}}},
		// "é" (E9) is no UTF-8 sequence, so the "ä" after it is neither isolated nor clean.
		{"\xE9\xC3\xA4", []utf8Seq{{lead: 0xC3, size: 2}}},
		{"\xC3\xA4\xE9", []utf8Seq{{lead: 0xC3, size: 2}}},
		{"a\xE9\xC3\xA4b", []utf8Seq{{lead: 0xC3, size: 2}}},
		// Adjacent sequences are clean, but not isolated.
		{"a\xC3\xA4\xC3\xB6b", []utf8Seq{
			{lead: 0xC3, size: 2, clean: true},
			{lead: 0xC3, size: 2, clean: true},
		}},
		{"\xC2\x80", []utf8Seq{{lead: 0xC2, size: 2, isolated: true, clean: true}}},
		{"\xC1\x80", nil}, // overlong, never valid
		{"\x80\xD0\xA0", []utf8Seq{{lead: 0xD0, size: 2}}},
		{"\xD0\xA0\x80", []utf8Seq{{lead: 0xD0, size: 2}}},
	}
	for _, tt := range tests {
		if got := utf8Sequences([]byte(tt.raw)); !reflect.DeepEqual(got, tt.want) {
			t.Errorf("utf8Sequences(%q) = %+v, want %+v", tt.raw, got, tt.want)
		}
	}
}

// TestImageHeaderBoundaries checks headers that are exactly as long as the reader needs,
// dimensions at the int32 limit, and that a header one byte too short names what is
// missing.
func TestImageHeaderBoundaries(t *testing.T) {
	const maxInt32 = 1<<31 - 1
	ok := []struct {
		name string
		data []byte
		w, h int
	}{
		{"BMP core header, 22 bytes", bmpCore(640, 480)[:22], 640, 480},
		{"BMP info header, 26 bytes", bmpInfo(40, 640, 480)[:26], 640, 480},
		{"BMP width MaxInt32", bmpInfo(40, maxInt32, 1), maxInt32, 1},
		{"BMP height MaxInt32", bmpInfo(40, 1, maxInt32), 1, maxInt32},
		{"BMP top-down height -MaxInt32", bmpInfo(40, 1, -maxInt32), 1, maxInt32},
		{"WebP VP8, 10 data bytes", webpVP8(640, 480)[:30], 640, 480},
		{"WebP VP8L, 5 data bytes", webpVP8L(640, 480)[:25], 640, 480},
	}
	for _, tt := range ok {
		h, err := readImageHeader(bytes.NewReader(tt.data))
		if err != nil || h.Width != tt.w || h.Height != tt.h {
			t.Errorf("%s: got %dx%d, %v; want %dx%d", tt.name, h.Width, h.Height, err, tt.w, tt.h)
		}
	}

	bad := []struct {
		name    string
		data    []byte
		wantErr string
	}{
		{"BMP height 2^31 (top-down MinInt32)", bmpInfo(40, 1, -maxInt32-1), "out of range"},
		{"BMP core header, 21 bytes", bmpCore(640, 480)[:21], "truncated BMP core header"},
		{"BMP info header, 25 bytes", bmpInfo(40, 640, 480)[:25], "truncated BMP info header"},
		// 18 bytes hold the DIB header size: an unknown one is reported as such.
		{"BMP 18 bytes, header size 5", cat([]byte("BM"), make([]byte, 12), le32(5)), "BMP header size 5"},
		{"BMP 17 bytes", cat([]byte("BM"), make([]byte, 12), le32(5))[:17], "truncated BMP header"},
		// 12 bytes hold the RIFF type: the WebP chunk header is what is missing.
		{"WebP 12 bytes", webpVP8(640, 480)[:12], "truncated WebP header"},
		{"RIFF 12 bytes, not WebP", cat([]byte("RIFF"), le32(4), []byte("WAVE")), "is not WebP"},
		// 20 bytes hold the chunk header: the VP8 data is what is missing.
		{"WebP VP8 20 bytes", webpVP8(640, 480)[:20], "truncated WebP VP8 header"},
		{"WebP VP8 29 bytes", webpVP8(640, 480)[:29], "truncated WebP VP8 header"},
		{"WebP VP8L 24 bytes", webpVP8L(640, 480)[:24], "truncated WebP VP8L header"},
	}
	for _, tt := range bad {
		_, err := readImageHeader(bytes.NewReader(tt.data))
		if !errors.Is(err, ErrInvalidImage) || !strings.Contains(err.Error(), tt.wantErr) {
			t.Errorf("%s: err = %v, want ErrInvalidImage containing %q", tt.name, err, tt.wantErr)
		}
	}
}

// TestTagsFromFolderNameBoundaries: a control character at the very start of the folder
// name is rejected like one anywhere else, and "9" is a digit of a track number.
func TestTagsFromFolderNameBoundaries(t *testing.T) {
	for _, tt := range []struct{ folder, wantReason string }{
		{"\x01Artist - Title", "control characters"},
		{"\tArtist - Title", "control characters"},
		{"9 - Title", "track number"},
		{"09. - Title", "track number"},
		{"19 - Title", "track number"},
	} {
		got := TagsFromFolderName(tt.folder, "", "")
		if got.FillArtist || got.FillTitle || !strings.Contains(got.Reason, tt.wantReason) {
			t.Errorf("TagsFromFolderName(%q) = %+v, want no fill and a reason with %q", tt.folder, got, tt.wantReason)
		}
	}
	// A track-number part next to the known title (fillOther) is no artist either.
	if got := TagsFromFolderName("9 - Title", "", "Title"); got.FillArtist || !strings.Contains(got.Reason, "track number") {
		t.Errorf("TagsFromFolderName(\"9 - Title\", title Title) = %+v, want a track-number reason", got)
	}
}
