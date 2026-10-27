package images

import (
	"bytes"
	"encoding/base64"
	"image"
	"image/color"
	"image/gif"
	"image/jpeg"
	"image/png"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/AgentDrasil/asgard/simplest/internal/types"
)

// buildTIFFOrientation returns a minimal big- or little-endian TIFF structure
// whose IFD0 carries a single orientation tag with the given value.
func buildTIFFOrientation(orientation int, littleEndian bool) []byte {
	var out []byte
	if littleEndian {
		out = []byte{'I', 'I', 0x2A, 0x00, 0x08, 0x00, 0x00, 0x00, 0x01, 0x00}
	} else {
		out = []byte{'M', 'M', 0x00, 0x2A, 0x00, 0x00, 0x00, 0x08, 0x00, 0x01}
	}
	// Entry: tag 0x0112, type SHORT (3), count 1, value.
	if littleEndian {
		out = append(out, 0x12, 0x01)
		out = append(out, 0x03, 0x00, 0x01, 0x00, 0x00, 0x00)
		out = append(out, byte(orientation), 0x00, 0x00, 0x00)
	} else {
		out = append(out, 0x01, 0x12)
		out = append(out, 0x00, 0x03, 0x00, 0x00, 0x00, 0x01)
		out = append(out, 0x00, byte(orientation), 0x00, 0x00)
	}
	// Next IFD offset (0).
	out = append(out, 0x00, 0x00, 0x00, 0x00)
	return out
}

// buildJPEGWithExif encodes a small JPEG and injects the given APP1 segments
// right after the SOI marker, in order.
func buildJPEGWithExif(t *testing.T, base image.Image, app1Payloads ...[]byte) []byte {
	t.Helper()
	var buf bytes.Buffer
	require.NoError(t, jpeg.Encode(&buf, base, &jpeg.Options{Quality: 95}))
	baseBytes := buf.Bytes()
	require.True(t, bytes.HasPrefix(baseBytes, []byte{0xFF, 0xD8}))

	var out bytes.Buffer
	out.Write(baseBytes[:2]) // SOI
	for _, payload := range app1Payloads {
		out.Write([]byte{0xFF, 0xE1})
		length := len(payload) + 2
		out.Write([]byte{byte(length >> 8), byte(length)})
		out.Write(payload)
	}
	out.Write(baseBytes[2:])
	return out.Bytes()
}

func exifAPP1Payload(orientation int, littleEndian bool) []byte {
	payload := []byte("Exif\x00\x00")
	return append(payload, buildTIFFOrientation(orientation, littleEndian)...)
}

func TestGetExifOrientation_JPEG_TableDriven(t *testing.T) {
	t.Parallel()

	img := createSolidImage(t, 4, 4, color.RGBA{R: 255, G: 0, B: 0, A: 255})

	for _, le := range []bool{true, false} {
		var wantEndian string
		if le {
			wantEndian = "little"
		} else {
			wantEndian = "big"
		}
		for orientation := 1; orientation <= 8; orientation++ {
			data := buildJPEGWithExif(t, img, exifAPP1Payload(orientation, le))
			assert.Equal(t, orientation, getExifOrientation(data), "orientation %d (%s endian)", orientation, wantEndian)
		}
	}
}

func TestGetExifOrientation_NoExif(t *testing.T) {
	t.Parallel()

	img := createSolidImage(t, 4, 4, color.RGBA{R: 0, G: 255, B: 0, A: 255})

	t.Run("plain jpeg without exif", func(t *testing.T) {
		t.Parallel()
		var buf bytes.Buffer
		require.NoError(t, jpeg.Encode(&buf, img, nil))
		assert.Equal(t, 1, getExifOrientation(buf.Bytes()))
	})

	t.Run("non-jpeg non-webp bytes", func(t *testing.T) {
		t.Parallel()
		assert.Equal(t, 1, getExifOrientation([]byte("not an image at all")))
	})
}

func TestGetExifOrientation_SkipsNonExifAPP1(t *testing.T) {
	t.Parallel()

	img := createSolidImage(t, 4, 4, color.RGBA{R: 0, G: 0, B: 255, A: 255})
	nonExifAPP1 := []byte("http://example.com\x00")
	data := buildJPEGWithExif(t, img, nonExifAPP1, exifAPP1Payload(6, true))
	assert.Equal(t, 6, getExifOrientation(data))
}

func TestGetExifOrientation_JpegFillBytesBeforeAPP1(t *testing.T) {
	t.Parallel()

	img := createSolidImage(t, 4, 4, color.RGBA{R: 255, G: 0, B: 0, A: 255})
	var buf bytes.Buffer
	require.NoError(t, jpeg.Encode(&buf, img, &jpeg.Options{Quality: 95}))
	baseBytes := buf.Bytes()

	// Inject 0xFF fill bytes between SOI and the APP1 segment.
	var out bytes.Buffer
	out.Write(baseBytes[:2])
	out.Write([]byte{0xFF, 0xFF, 0xFF})
	payload := exifAPP1Payload(7, true)
	out.Write([]byte{0xFF, 0xE1})
	length := len(payload) + 2
	out.Write([]byte{byte(length >> 8), byte(length)})
	out.Write(payload)
	out.Write(baseBytes[2:])

	assert.Equal(t, 7, getExifOrientation(out.Bytes()))
}

func TestGetExifOrientation_IFDWithoutOrientationTag(t *testing.T) {
	t.Parallel()

	img := createSolidImage(t, 4, 4, color.RGBA{R: 255, G: 0, B: 0, A: 255})
	// Valid little-endian TIFF with one unrelated tag (0x010F Make), no 0x0112.
	tiff := []byte{'I', 'I', 0x2A, 0x00, 0x08, 0x00, 0x00, 0x00, 0x01, 0x00,
		0x0F, 0x01, 0x02, 0x00, 0x02, 0x00, 0x00, 0x00, 0x05, 0x00, 0x00, 0x00,
		0x00, 0x00, 0x00, 0x00}
	data := buildJPEGWithExif(t, img, append([]byte("Exif\x00\x00"), tiff...))
	assert.Equal(t, 1, getExifOrientation(data))
}

func TestGetExifOrientation_Malformed_ReturnsOne(t *testing.T) {
	t.Parallel()

	img := createSolidImage(t, 4, 4, color.RGBA{R: 255, G: 0, B: 0, A: 255})

	tests := []struct {
		name    string
		payload []byte
	}{
		{
			name:    "truncated tiff header",
			payload: append([]byte("Exif\x00\x00"), buildTIFFOrientation(6, true)[:5]...),
		},
		{
			name:    "invalid byte order",
			payload: append([]byte("Exif\x00\x00"), []byte{'X', 'X', 0, 0, 0, 0, 0, 0, 0, 0}...),
		},
		{
			name:    "ifd offset beyond data",
			payload: append([]byte("Exif\x00\x00"), []byte{'I', 'I', 0x2A, 0x00, 0xFF, 0xFF, 0x00, 0x00, 0x00, 0x00}...),
		},
		{
			name:    "orientation value out of range",
			payload: exifAPP1Payload(9, true),
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			data := buildJPEGWithExif(t, img, tt.payload)
			assert.Equal(t, 1, getExifOrientation(data))
		})
	}
}

// buildWebPWithExifChunk builds a minimal RIFF/WEBP container holding a dummy
// VP8L chunk plus an EXIF chunk with the given payload.
func buildWebPWithExifChunk(exifPayload []byte) []byte {
	var body bytes.Buffer
	body.WriteString("WEBP")

	// VP8L chunk (odd size to exercise even-size padding).
	vp8l := []byte{0x10, 0x00, 0x00, 0x00, 0x00} // 5 bytes
	body.WriteString("VP8L")
	body.Write([]byte{byte(len(vp8l)), 0x00, 0x00, 0x00})
	body.Write(vp8l)
	body.Write([]byte{0x00}) // pad byte

	body.WriteString("EXIF")
	body.Write([]byte{byte(len(exifPayload)), 0x00, 0x00, 0x00})
	body.Write(exifPayload)

	var out bytes.Buffer
	out.WriteString("RIFF")
	bodyLen := body.Len()
	out.Write([]byte{byte(bodyLen), byte(bodyLen >> 8), byte(bodyLen >> 16), byte(bodyLen >> 24)})
	out.Write(body.Bytes())
	return out.Bytes()
}

func TestGetExifOrientation_WebP_TableDriven(t *testing.T) {
	t.Parallel()

	tiff := buildTIFFOrientation(8, true)

	t.Run("with Exif prefix inside chunk", func(t *testing.T) {
		t.Parallel()
		payload := append([]byte("Exif\x00\x00"), tiff...)
		assert.Equal(t, 8, getExifOrientation(buildWebPWithExifChunk(payload)))
	})

	t.Run("raw tiff without Exif prefix", func(t *testing.T) {
		t.Parallel()
		assert.Equal(t, 8, getExifOrientation(buildWebPWithExifChunk(tiff)))
	})

	t.Run("truncated exif payload returns 1", func(t *testing.T) {
		t.Parallel()
		payload := []byte("Exif\x00\x00")
		data := buildWebPWithExifChunk(payload[:2]) // truncated, malformed
		assert.Equal(t, 1, getExifOrientation(data))
	})
}

// makeDistinctImage returns a w x h RGBA image whose pixel color encodes its
// coordinates: R = x*10+1, G = y*10+2, B = 7, A = 255.
func makeDistinctImage(w, h int) *image.RGBA {
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			img.SetRGBA(x, y, color.RGBA{R: uint8(x*10 + 1), G: uint8(y*10 + 2), B: 7, A: 255})
		}
	}
	return img
}

func TestApplyOrientation_TableDriven(t *testing.T) {
	t.Parallel()

	const w, h = 2, 3
	src := makeDistinctImage(w, h)

	// Expected source coordinate per destination pixel, per the standard
	// destination-from-source mapping (dst dims h x w for 5-8).
	mapping := map[int]func(x, y int) (int, int){
		1: func(x, y int) (int, int) { return x, y },
		2: func(x, y int) (int, int) { return w - 1 - x, y },
		3: func(x, y int) (int, int) { return w - 1 - x, h - 1 - y },
		4: func(x, y int) (int, int) { return x, h - 1 - y },
		5: func(x, y int) (int, int) { return y, x },
		6: func(x, y int) (int, int) { return y, h - 1 - x },
		7: func(x, y int) (int, int) { return w - 1 - y, h - 1 - x },
		8: func(x, y int) (int, int) { return w - 1 - y, x },
	}

	for orientation := 1; orientation <= 8; orientation++ {
		got := applyOrientation(src, orientation)
		wantW, wantH := w, h
		if orientation >= 5 {
			wantW, wantH = h, w
		}
		assert.Equal(t, wantW, got.Bounds().Dx(), "orientation %d width", orientation)
		assert.Equal(t, wantH, got.Bounds().Dy(), "orientation %d height", orientation)

		for y := 0; y < wantH; y++ {
			for x := 0; x < wantW; x++ {
				sx, sy := mapping[orientation](x, y)
				want := color.RGBA{R: uint8(sx*10 + 1), G: uint8(sy*10 + 2), B: 7, A: 255}
				assert.Equal(t, want, got.At(x, y), "orientation %d at dst(%d,%d)", orientation, x, y)
			}
		}
	}
}

func TestApplyOrientation_NonZeroBounds(t *testing.T) {
	t.Parallel()

	src := image.NewRGBA(image.Rect(5, 3, 7, 6)) // 2x3, min (5,3)
	src.SetRGBA(5, 3, color.RGBA{R: 1, G: 2, B: 3, A: 255})
	src.SetRGBA(6, 3, color.RGBA{R: 11, G: 2, B: 3, A: 255})
	src.SetRGBA(5, 4, color.RGBA{R: 1, G: 12, B: 3, A: 255})
	src.SetRGBA(6, 4, color.RGBA{R: 11, G: 12, B: 3, A: 255})
	src.SetRGBA(5, 5, color.RGBA{R: 1, G: 22, B: 3, A: 255})
	src.SetRGBA(6, 5, color.RGBA{R: 11, G: 22, B: 3, A: 255})

	got := applyOrientation(src, 5) // transpose: dst(x,y) = src(y,x)
	assert.Equal(t, 3, got.Bounds().Dx())
	assert.Equal(t, 2, got.Bounds().Dy())
	assert.Equal(t, color.RGBA{R: 1, G: 2, B: 3, A: 255}, got.At(0, 0))
	assert.Equal(t, color.RGBA{R: 1, G: 22, B: 3, A: 255}, got.At(2, 0))
	assert.Equal(t, color.RGBA{R: 11, G: 2, B: 3, A: 255}, got.At(0, 1))
}

func TestProcessImage_EXIFOrientation6_ReencodeSwapsDims(t *testing.T) {
	t.Parallel()

	// 60 wide x 90 tall: top half red, bottom half blue.
	src := image.NewRGBA(image.Rect(0, 0, 60, 90))
	for y := 0; y < 90; y++ {
		for x := 0; x < 60; x++ {
			if y < 45 {
				src.Set(x, y, color.RGBA{R: 255, G: 0, B: 0, A: 255})
			} else {
				src.Set(x, y, color.RGBA{R: 0, G: 0, B: 255, A: 255})
			}
		}
	}
	data := buildJPEGWithExif(t, src, exifAPP1Payload(6, true))

	// Oriented dims are 90x60; scaled to fit 60x60 -> 60x40.
	limits := &types.ModelImageInputLimits{
		Resize: &types.ModelImageResizeOptions{MaxWidth: 60, MaxHeight: 60},
	}
	res, err := ProcessImage(data, "image/jpeg", limits)
	require.NoError(t, err)
	assert.True(t, res.WasResized)
	// Output dims are swapped relative to the raw 60x90 source.
	assert.Equal(t, 60, res.Width)
	assert.Equal(t, 40, res.Height)

	raw, err := base64.StdEncoding.DecodeString(res.Data)
	require.NoError(t, err)
	decoded, _, err := image.Decode(bytes.NewReader(raw))
	require.NoError(t, err)
	// Orientation 6 (rotate 90 CW): dst(0,0) samples src(0, h-1) = bottom-left = blue.
	r, g, b, _ := decoded.At(0, 0).RGBA()
	assert.Less(t, r, uint32(64), "red channel should be low at dst(0,0)")
	assert.Less(t, g, uint32(64))
	assert.Greater(t, b, uint32(192<<8), "blue channel should dominate at dst(0,0)")
}

func TestProcessImage_EXIFOrientation_PassThroughUntouched(t *testing.T) {
	t.Parallel()

	img := createSolidImage(t, 50, 40, color.RGBA{R: 200, G: 30, B: 40, A: 255})
	data := buildJPEGWithExif(t, img, exifAPP1Payload(6, true))

	res, err := ProcessImage(data, "image/jpeg", nil)
	require.NoError(t, err)
	assert.False(t, res.WasResized)
	assert.Empty(t, res.Notes)
	// Pass-through must be byte-identical: orientation must not trigger re-encoding.
	assert.Equal(t, base64.StdEncoding.EncodeToString(data), res.Data)
}

func TestProcessImage_EXIFOrientation_RecomputesDimensionLimits(t *testing.T) {
	t.Parallel()

	// 900x700 raw; orientation 6/8 orients it to 700x900, which exceeds
	// MaxHeight=768 even though the raw dims fit within 1024x768.
	// The byte limit forces the re-encode path that applies the orientation.
	src := image.NewRGBA(image.Rect(0, 0, 900, 700))
	for y := 0; y < 700; y++ {
		for x := 0; x < 900; x++ {
			src.SetRGBA(x, y, color.RGBA{
				R: uint8((x * 255) / 900),
				G: uint8((y * 255) / 700),
				B: 90,
				A: 255,
			})
		}
	}

	for _, orientation := range []int{6, 8} {
		data := buildJPEGWithExif(t, src, exifAPP1Payload(orientation, true))
		origB64 := base64.StdEncoding.EncodeToString(data)
		require.Greater(t, int64(len(origB64)), int64(20000), "test setup: original base64 must exceed MaxBytes")

		limits := &types.ModelImageInputLimits{
			Resize: &types.ModelImageResizeOptions{MaxWidth: 1024, MaxHeight: 768, MaxBytes: 20000},
		}
		res, err := ProcessImage(data, "image/jpeg", limits)
		require.NoError(t, err, "orientation %d", orientation)
		assert.True(t, res.WasResized, "orientation %d", orientation)
		// Oriented dims are 700x900; must be clamped to the asymmetric limits.
		assert.LessOrEqual(t, res.Width, 1024, "orientation %d", orientation)
		assert.LessOrEqual(t, res.Height, 768, "orientation %d", orientation)
		// Aspect ratio of the oriented image must be preserved (700/900 ~= 0.778).
		gotRatio := float64(res.Width) / float64(res.Height)
		assert.InDelta(t, 700.0/900.0, gotRatio, 0.02, "orientation %d", orientation)
	}
}

func TestProcessImage_TransparentGIF_ReencodesToPNG(t *testing.T) {
	t.Parallel()

	// Transparent GIF with one opaque pixel.
	palette := color.Palette{
		color.RGBA{R: 0, G: 0, B: 0, A: 0}, // transparent
		color.RGBA{R: 255, G: 0, B: 0, A: 255},
	}
	frame := image.NewPaletted(image.Rect(0, 0, 64, 48), palette)
	frame.SetColorIndex(0, 0, 1)
	var buf bytes.Buffer
	require.NoError(t, gif.Encode(&buf, frame, nil))

	limits := &types.ModelImageInputLimits{
		Resize: &types.ModelImageResizeOptions{MaxWidth: 32, MaxHeight: 48},
	}
	res, err := ProcessImage(buf.Bytes(), "image/gif", limits)
	require.NoError(t, err)
	assert.True(t, res.WasResized)
	assert.Equal(t, "image/png", res.MimeType)

	raw, err := base64.StdEncoding.DecodeString(res.Data)
	require.NoError(t, err)
	decoded, err := png.Decode(bytes.NewReader(raw))
	require.NoError(t, err)
	// A pixel away from the opaque corner must remain transparent.
	_, _, _, a := decoded.At(32, 24).RGBA()
	assert.Zero(t, a, "pixel should stay transparent in the re-encoded PNG")
}

func TestProcessImage_TransparentPNG_OverLimitFitsAsPNG(t *testing.T) {
	t.Parallel()

	// Transparent PNG; sized so it exceeds MaxBytes but its JPEG-quality
	// re-encode would also be an option — PNG must win when it fits.
	src := image.NewRGBA(image.Rect(0, 0, 8, 8))
	for y := 0; y < 8; y++ {
		for x := 0; x < 8; x++ {
			src.SetRGBA(x, y, color.RGBA{R: 10, G: 20, B: 30, A: 0})
		}
	}
	var buf bytes.Buffer
	require.NoError(t, png.Encode(&buf, src))

	limits := &types.ModelImageInputLimits{
		Resize: &types.ModelImageResizeOptions{MaxWidth: 4, MaxHeight: 4, MaxBytes: 4000},
	}
	res, err := ProcessImage(buf.Bytes(), "image/png", limits)
	require.NoError(t, err)
	assert.True(t, res.WasResized)
	assert.Equal(t, "image/png", res.MimeType)

	raw, err := base64.StdEncoding.DecodeString(res.Data)
	require.NoError(t, err)
	decoded, err := png.Decode(bytes.NewReader(raw))
	require.NoError(t, err)
	_, _, _, a := decoded.At(2, 2).RGBA()
	assert.Zero(t, a, "pixel should stay transparent in the re-encoded PNG")
}
