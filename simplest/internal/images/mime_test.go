package images

import (
	"encoding/binary"
	"testing"

	"github.com/stretchr/testify/assert"
)

func pngChunk(ctype string, payload []byte) []byte {
	buf := make([]byte, 12+len(payload))
	binary.BigEndian.PutUint32(buf[0:4], uint32(len(payload)))
	copy(buf[4:8], ctype)
	copy(buf[8:], payload)
	crc := uint32(0)
	binary.BigEndian.PutUint32(buf[8+len(payload):], crc)
	return buf
}

func buildPNG(chunks ...[]byte) []byte {
	out := []byte{0x89, 0x50, 0x4e, 0x47, 0x0d, 0x0a, 0x1a, 0x0a}
	for _, c := range chunks {
		out = append(out, c...)
	}
	return out
}

func ihdrChunk() []byte {
	payload := make([]byte, 13)
	binary.BigEndian.PutUint32(payload[0:4], 4)
	binary.BigEndian.PutUint32(payload[4:8], 4)
	payload[8] = 8
	return pngChunk("IHDR", payload)
}

func bmpFile(dibHeaderSize uint32, planes, bpp uint16, fileSize uint32, pixelOffset uint32, totalLen int) []byte {
	if totalLen < 30 {
		totalLen = 30
	}
	buf := make([]byte, totalLen)
	buf[0], buf[1] = 'B', 'M'
	binary.LittleEndian.PutUint32(buf[2:6], fileSize)
	binary.LittleEndian.PutUint32(buf[10:14], pixelOffset)
	binary.LittleEndian.PutUint32(buf[14:18], dibHeaderSize)
	if dibHeaderSize == 12 {
		binary.LittleEndian.PutUint16(buf[22:24], planes)
		binary.LittleEndian.PutUint16(buf[24:26], bpp)
	} else {
		binary.LittleEndian.PutUint16(buf[26:28], planes)
		binary.LittleEndian.PutUint16(buf[28:30], bpp)
	}
	return buf
}

func TestDetectMimeType(t *testing.T) {
	t.Parallel()

	idat := pngChunk("IDAT", []byte{1, 2, 3})
	actl := pngChunk("acTL", []byte{0, 0, 0, 1})

	tests := []struct {
		name string
		data []byte
		want string
	}{
		{"jpeg", []byte{0xff, 0xd8, 0xff, 0xe0, 0x00}, "image/jpeg"},
		{"jpeg-ls rejected", []byte{0xff, 0xd8, 0xff, 0xf7, 0x00}, ""},
		{"jpeg truncated to 3 bytes", []byte{0xff, 0xd8, 0xff}, "image/jpeg"},
		{"png", buildPNG(ihdrChunk(), idat), "image/png"},
		{"apng rejected", buildPNG(ihdrChunk(), actl, idat), ""},
		{"gif87a", append([]byte("GIF87a"), 0x00), "image/gif"},
		{"gif89a", append([]byte("GIF89a"), 0x00), "image/gif"},
		{"webp", append(append([]byte("RIFF"), 0, 0, 0, 0), []byte("WEBPVP8 ")...), "image/webp"},
		{"bmp 12-byte dib", bmpFile(12, 1, 24, 0, 26, 26), "image/bmp"},
		{"bmp 40-byte dib", bmpFile(40, 1, 32, 100, 54, 60), "image/bmp"},
		{"bmp declared size too small", bmpFile(40, 1, 24, 10, 54, 60), ""},
		{"bmp pixel offset beyond declared size", bmpFile(40, 1, 24, 100, 100, 60), ""},
		{"bmp pixel offset before header end", bmpFile(40, 1, 24, 0, 40, 60), ""},
		{"bmp planes != 1", bmpFile(40, 2, 24, 0, 54, 60), ""},
		{"bmp bad bpp", bmpFile(40, 1, 3, 0, 54, 60), ""},
		{"bmp bad dib header size", bmpFile(30, 1, 24, 0, 54, 60), ""},
		{"bmp truncated (no bpp field)", append([]byte("BM"), make([]byte, 20)...), ""},
		{"bm only no structure", []byte("BMxx"), ""},
		{"truncated png signature only", []byte{0x89, 0x50, 0x4e, 0x47}, ""},
		{"truncated png no IHDR", buildPNG(pngChunk("IDAT", []byte{1})), ""},
		{"truncated bmp", append([]byte("BM"), make([]byte, 10)...), ""},
		{"plain text", []byte("hello world, this is text"), ""},
		{"empty", nil, ""},
		{"fake riff no webp", append([]byte("RIFF"), 0, 0, 0, 0), ""},
	}
	for _, tc := range tests {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			assert.Equal(t, tc.want, DetectMimeType(tc.data))
		})
	}
}

func TestDetectMimeType_APNGEdgeChunkWindow(t *testing.T) {
	t.Parallel()

	idat := pngChunk("IDAT", []byte{1, 2, 3})
	actl := pngChunk("acTL", []byte{0, 0, 0, 1})

	// A chunk whose declared length overruns the sniff window means we cannot
	// prove the file is animated; matching upstream, scanning stops there
	// (treated as not animated), so this stays image/png.
	hugeLen := pngChunk("abcD", nil)
	binary.BigEndian.PutUint32(hugeLen[0:4], 0xffffffff)
	data := buildPNG(ihdrChunk(), hugeLen)
	data = append(data, make([]byte, 4200)...)
	assert.Equal(t, "image/png", DetectMimeType(data[:4100]))

	// acTL before IDAT inside the window: animated => not an image.
	apng := buildPNG(ihdrChunk(), actl, idat)
	assert.Equal(t, "", DetectMimeType(apng))

	// acTL after IDAT: not animated => image/png.
	lateAcTL := buildPNG(ihdrChunk(), idat, actl)
	assert.Equal(t, "image/png", DetectMimeType(lateAcTL))
}
