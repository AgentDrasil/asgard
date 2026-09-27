package images

import (
	"bytes"
	"encoding/base64"
	"image"
	"image/color"
	"image/draw"
	"image/gif"
	"image/jpeg"
	"image/png"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"golang.org/x/image/bmp"

	"github.com/AgentDrasil/asgard/simplest/internal/types"
)

// sampleWebPB64 is a valid 150x100 lossy WebP image.
const sampleWebPB64 = "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"

func createSolidImage(t *testing.T, w, h int, c color.Color) image.Image {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	draw.Draw(img, img.Bounds(), &image.Uniform{C: c}, image.Point{}, draw.Src)
	return img
}

func createPNGBytes(t *testing.T, w, h int) []byte {
	t.Helper()
	img := createSolidImage(t, w, h, color.RGBA{R: 255, G: 0, B: 0, A: 255})
	var buf bytes.Buffer
	err := png.Encode(&buf, img)
	require.NoError(t, err)
	return buf.Bytes()
}

func createJPEGBytes(t *testing.T, w, h int) []byte {
	t.Helper()
	img := createSolidImage(t, w, h, color.RGBA{R: 0, G: 255, B: 0, A: 255})
	var buf bytes.Buffer
	err := jpeg.Encode(&buf, img, &jpeg.Options{Quality: 90})
	require.NoError(t, err)
	return buf.Bytes()
}

func createBMPBytes(t *testing.T, w, h int) []byte {
	t.Helper()
	img := createSolidImage(t, w, h, color.RGBA{R: 0, G: 0, B: 255, A: 255})
	var buf bytes.Buffer
	err := bmp.Encode(&buf, img)
	require.NoError(t, err)
	return buf.Bytes()
}

func createAnimatedGIFBytes(t *testing.T, w, h, numFrames int) []byte {
	t.Helper()
	g := &gif.GIF{}
	palette := color.Palette{
		color.RGBA{R: 0, G: 0, B: 0, A: 255},
		color.RGBA{R: 255, G: 255, B: 255, A: 255},
	}
	for i := 0; i < numFrames; i++ {
		pal := image.NewPaletted(image.Rect(0, 0, w, h), palette)
		for y := 0; y < h; y++ {
			for x := 0; x < w; x++ {
				pal.SetColorIndex(x, y, uint8(i%2))
			}
		}
		g.Image = append(g.Image, pal)
		g.Delay = append(g.Delay, 10)
	}
	var buf bytes.Buffer
	err := gif.EncodeAll(&buf, g)
	require.NoError(t, err)
	return buf.Bytes()
}

func TestProcessImage_Passthrough_TableDriven(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		genData  func(t *testing.T) []byte
		mimeType string
		wantMime string
	}{
		{
			name: "small png within limits passes through",
			genData: func(t *testing.T) []byte {
				return createPNGBytes(t, 100, 100)
			},
			mimeType: "image/png",
			wantMime: "image/png",
		},
		{
			name: "small jpeg within limits passes through",
			genData: func(t *testing.T) []byte {
				return createJPEGBytes(t, 200, 150)
			},
			mimeType: "image/jpeg",
			wantMime: "image/jpeg",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			data := tt.genData(t)
			res, err := ProcessImage(data, tt.mimeType, nil)
			require.NoError(t, err)
			assert.False(t, res.WasResized)
			assert.Equal(t, tt.wantMime, res.MimeType)
			assert.Equal(t, base64.StdEncoding.EncodeToString(data), res.Data)
			assert.Empty(t, res.Notes)
		})
	}
}

func TestProcessImage_DecodeWebPAndBMP_TableDriven(t *testing.T) {
	t.Parallel()

	webpBytes, err := base64.StdEncoding.DecodeString(sampleWebPB64)
	require.NoError(t, err)

	tests := []struct {
		name          string
		data          []byte
		mime          string
		limits        *types.ModelImageInputLimits
		wantOrigW     int
		wantOrigH     int
		wantMaxW      int
		wantMaxH      int
		expectResized bool
	}{
		{
			name:          "webp within limits passes through",
			data:          webpBytes,
			mime:          "image/webp",
			limits:        nil,
			wantOrigW:     150,
			wantOrigH:     100,
			wantMaxW:      150,
			wantMaxH:      100,
			expectResized: false,
		},
		{
			name: "webp scaled to custom limits",
			data: webpBytes,
			mime: "image/webp",
			limits: &types.ModelImageInputLimits{
				Resize: &types.ModelImageResizeOptions{
					MaxWidth:  75,
					MaxHeight: 50,
				},
			},
			wantOrigW:     150,
			wantOrigH:     100,
			wantMaxW:      75,
			wantMaxH:      50,
			expectResized: true,
		},
		{
			name:          "bmp normalized and handled",
			data:          createBMPBytes(t, 60, 40),
			mime:          "image/bmp",
			limits:        nil,
			wantOrigW:     60,
			wantOrigH:     40,
			wantMaxW:      60,
			wantMaxH:      40,
			expectResized: false,
		},
		{
			name: "bmp scaled down to limits",
			data: createBMPBytes(t, 200, 100),
			mime: "image/bmp",
			limits: &types.ModelImageInputLimits{
				Resize: &types.ModelImageResizeOptions{
					MaxWidth:  100,
					MaxHeight: 50,
				},
			},
			wantOrigW:     200,
			wantOrigH:     100,
			wantMaxW:      100,
			wantMaxH:      50,
			expectResized: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			res, err := ProcessImage(tt.data, tt.mime, tt.limits)
			require.NoError(t, err)
			assert.Equal(t, tt.wantOrigW, res.OriginalWidth)
			assert.Equal(t, tt.wantOrigH, res.OriginalHeight)
			assert.LessOrEqual(t, res.Width, tt.wantMaxW)
			assert.LessOrEqual(t, res.Height, tt.wantMaxH)
			assert.Equal(t, tt.expectResized, res.WasResized)
			assert.NotEmpty(t, res.Data)
		})
	}
}

func TestProcessImage_ResizeLargeDimensions_TableDriven(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		origW      int
		origH      int
		maxW       int
		maxH       int
		wantWidth  int
		wantHeight int
	}{
		{
			name:       "4000x3000 downscaled to default 2000x2000 keeps aspect ratio",
			origW:      4000,
			origH:      3000,
			maxW:       2000,
			maxH:       2000,
			wantWidth:  2000,
			wantHeight: 1500,
		},
		{
			name:       "1500x3000 downscaled to default 2000x2000 keeps aspect ratio",
			origW:      1500,
			origH:      3000,
			maxW:       2000,
			maxH:       2000,
			wantWidth:  1000,
			wantHeight: 2000,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			data := createJPEGBytes(t, tt.origW, tt.origH)
			limits := &types.ModelImageInputLimits{
				Resize: &types.ModelImageResizeOptions{
					MaxWidth:  tt.maxW,
					MaxHeight: tt.maxH,
				},
			}

			res, err := ProcessImage(data, "image/jpeg", limits)
			require.NoError(t, err)
			assert.True(t, res.WasResized)
			assert.Equal(t, tt.origW, res.OriginalWidth)
			assert.Equal(t, tt.origH, res.OriginalHeight)
			assert.Equal(t, tt.wantWidth, res.Width)
			assert.Equal(t, tt.wantHeight, res.Height)

			hasResizeNote := false
			for _, n := range res.Notes {
				if strings.Contains(n, "resized to") {
					hasResizeNote = true
				}
			}
			assert.True(t, hasResizeNote, "expected resize note in notes: %v", res.Notes)
		})
	}
}

func TestProcessImage_MaxBytesControl_TableDriven(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		maxBytes int64
	}{
		{
			name:     "strict maxBytes 8000",
			maxBytes: 8000,
		},
		{
			name:     "moderate maxBytes 15000",
			maxBytes: 15000,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			// Create a 500x500 image with gradients to produce more bytes than a solid block
			img := image.NewRGBA(image.Rect(0, 0, 500, 500))
			for y := 0; y < 500; y++ {
				for x := 0; x < 500; x++ {
					img.Set(x, y, color.RGBA{
						R: uint8((x * 255) / 500),
						G: uint8((y * 255) / 500),
						B: uint8(((x + y) * 255) / 1000),
						A: 255,
					})
				}
			}
			var buf bytes.Buffer
			err := png.Encode(&buf, img)
			require.NoError(t, err)

			limits := &types.ModelImageInputLimits{
				Resize: &types.ModelImageResizeOptions{
					MaxWidth:    1000,
					MaxHeight:   1000,
					MaxBytes:    tt.maxBytes,
					JPEGQuality: 80,
				},
			}

			res, err := ProcessImage(buf.Bytes(), "image/png", limits)
			require.NoError(t, err)
			assert.NotEmpty(t, res.Data)
			assert.LessOrEqual(t, int64(len(res.Data)), tt.maxBytes)
			assert.Equal(t, "image/jpeg", res.MimeType)
		})
	}
}

func TestProcessImage_AnimatedGIF(t *testing.T) {
	t.Parallel()

	data := createAnimatedGIFBytes(t, 80, 60, 3)

	res, err := ProcessImage(data, "image/gif", nil)
	require.NoError(t, err)
	assert.NotEmpty(t, res.Data)
	assert.Equal(t, 80, res.Width)
	assert.Equal(t, 60, res.Height)

	foundAnimNote := false
	for _, n := range res.Notes {
		if strings.Contains(n, "[Animated GIF: first frame retained]") {
			foundAnimNote = true
			break
		}
	}
	assert.True(t, foundAnimNote, "expected animated GIF note, got: %v", res.Notes)
}

func TestProcessImage_CorruptData_TableDriven(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		data []byte
		mime string
	}{
		{
			name: "empty bytes",
			data: []byte{},
			mime: "image/png",
		},
		{
			name: "corrupt png header",
			data: []byte{0x89, 'P', 'N', 'G', 0x00, 0x00},
			mime: "image/png",
		},
		{
			name: "random garbage bytes",
			data: []byte("definitely not an image of any kind"),
			mime: "image/jpeg",
		},
		{
			name: "truncated bmp header",
			data: []byte("BM123"),
			mime: "image/bmp",
		},
		{
			name: "truncated webp header",
			data: []byte("RIFF1234WEBP"),
			mime: "image/webp",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			res, err := ProcessImage(tt.data, tt.mime, nil)
			assert.Error(t, err)
			assert.Nil(t, res)
		})
	}
}
