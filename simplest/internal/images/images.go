// Package images provides image processing and resizing capabilities to enforce
// per-model input limits (maxWidth, maxHeight, maxBytes, animated GIF flattening).
//
// Format conversions and transparency trade-offs:
// When an image must be re-encoded (animated, BMP, exceeding dimension or byte
// limits), each candidate size first attempts a PNG encoding — which preserves
// alpha/transparency — and falls back to JPEG with a descending quality ladder
// only when PNG does not fit within maxBytes. If nothing fits, dimensions are
// progressively shrunk (x0.75) with the same PNG-then-JPEG strategy, ending in
// a minimal-quality JPEG fallback. Images that fit within all limits pass
// through byte-identical, preserving their original format and alpha channel.
//
// EXIF orientation: JPEG and WebP inputs carry their EXIF orientation tag
// through the pass-through path untouched; when an image is re-encoded, the
// orientation transform is applied to the decoded pixels before resizing so
// the reported and output dimensions match the visually oriented image.
package images

import (
	"bytes"
	"encoding/base64"
	"fmt"
	"image"
	"image/gif"
	"image/jpeg"
	"image/png"
	"math"
	"strings"

	"golang.org/x/image/bmp"
	"golang.org/x/image/draw"
	"golang.org/x/image/webp"

	"github.com/AgentDrasil/asgard/simplest/internal/types"
)

func init() {
	image.RegisterFormat("bmp", "BM", bmp.Decode, bmp.DecodeConfig)
	image.RegisterFormat("webp", "RIFF????WEBP", webp.Decode, webp.DecodeConfig)
}

// Default limits for image processing.
const (
	DefaultMaxWidth       = 2000
	DefaultMaxHeight      = 2000
	DefaultMaxBase64Bytes = int64(4718592) // 4.5 MiB
	DefaultJPEGQuality    = 80
)

// ProcessedImage carries the result of image inspection, normalization, and resizing.
type ProcessedImage struct {
	Data           string   `json:"data"`
	MimeType       string   `json:"mimeType"`
	OriginalWidth  int      `json:"originalWidth"`
	OriginalHeight int      `json:"originalHeight"`
	Width          int      `json:"width"`
	Height         int      `json:"height"`
	WasResized     bool     `json:"wasResized"`
	Notes          []string `json:"notes,omitempty"`
}

func safeDecodeImage(data []byte, mimeType string) (img image.Image, origW, origH int, format string, isAnimated bool, err error) {
	if len(data) == 0 {
		return nil, 0, 0, "", false, fmt.Errorf("empty image data")
	}

	defer func() {
		if r := recover(); r != nil {
			err = fmt.Errorf("image decoding panicked: %v", r)
		}
	}()

	mime := strings.ToLower(strings.TrimSpace(mimeType))

	// GIF check (decode all to detect animations)
	if mime == "image/gif" || bytes.HasPrefix(data, []byte("GIF87a")) || bytes.HasPrefix(data, []byte("GIF89a")) {
		g, gErr := gif.DecodeAll(bytes.NewReader(data))
		if gErr == nil && len(g.Image) > 0 {
			w := g.Config.Width
			h := g.Config.Height
			if w == 0 || h == 0 {
				w = g.Image[0].Bounds().Dx()
				h = g.Image[0].Bounds().Dy()
			}
			return g.Image[0], w, h, "gif", len(g.Image) > 1, nil
		}
	}

	// WebP check
	if mime == "image/webp" || (len(data) >= 12 && string(data[:4]) == "RIFF" && string(data[8:12]) == "WEBP") {
		wImg, wErr := webp.Decode(bytes.NewReader(data))
		if wErr == nil {
			return wImg, wImg.Bounds().Dx(), wImg.Bounds().Dy(), "webp", false, nil
		}
	}

	// BMP check
	if mime == "image/bmp" || (len(data) >= 2 && string(data[:2]) == "BM") {
		bImg, bErr := bmp.Decode(bytes.NewReader(data))
		if bErr == nil {
			return bImg, bImg.Bounds().Dx(), bImg.Bounds().Dy(), "bmp", false, nil
		}
	}

	// Standard image.Decode (handles png, jpeg, etc.)
	stdImg, fmtName, stdErr := image.Decode(bytes.NewReader(data))
	if stdErr == nil {
		return stdImg, stdImg.Bounds().Dx(), stdImg.Bounds().Dy(), fmtName, false, nil
	}

	// Fallback to webp/bmp if format was unknown
	if mime != "image/webp" {
		if wImg, wErr := webp.Decode(bytes.NewReader(data)); wErr == nil {
			return wImg, wImg.Bounds().Dx(), wImg.Bounds().Dy(), "webp", false, nil
		}
	}
	if mime != "image/bmp" {
		if bImg, bErr := bmp.Decode(bytes.NewReader(data)); bErr == nil {
			return bImg, bImg.Bounds().Dx(), bImg.Bounds().Dy(), "bmp", false, nil
		}
	}

	return nil, 0, 0, "", false, fmt.Errorf("unsupported or corrupt image format: %w", stdErr)
}

func resizeBilinear(src image.Image, targetW, targetH int) image.Image {
	dst := image.NewRGBA(image.Rect(0, 0, targetW, targetH))
	draw.BiLinear.Scale(dst, dst.Bounds(), src, src.Bounds(), draw.Src, nil)
	return dst
}

// ProcessImage decodes the input image, validates its limits, scales dimensions keeping
// aspect ratio if necessary, down-samples GIF animations to the first frame, and compresses
// payload bytes to satisfy maxBytes.
func ProcessImage(data []byte, mimeType string, limits *types.ModelImageInputLimits) (*ProcessedImage, error) {
	origImg, origW, origH, format, isAnimated, err := safeDecodeImage(data, mimeType)
	if err != nil {
		return nil, err
	}

	maxWidth := DefaultMaxWidth
	maxHeight := DefaultMaxHeight
	maxBytes := DefaultMaxBase64Bytes
	jpegQuality := DefaultJPEGQuality

	if limits != nil && limits.Resize != nil {
		if limits.Resize.MaxWidth > 0 {
			maxWidth = limits.Resize.MaxWidth
		}
		if limits.Resize.MaxHeight > 0 {
			maxHeight = limits.Resize.MaxHeight
		}
		if limits.Resize.MaxBytes > 0 {
			maxBytes = limits.Resize.MaxBytes
		}
		if limits.Resize.JPEGQuality > 0 {
			jpegQuality = limits.Resize.JPEGQuality
		}
	}

	// Normalize MIME type from format if empty
	normMime := mimeType
	if normMime == "" {
		switch format {
		case "jpeg", "jpg":
			normMime = "image/jpeg"
		case "png":
			normMime = "image/png"
		case "gif":
			normMime = "image/gif"
		case "webp":
			normMime = "image/webp"
		case "bmp":
			normMime = "image/bmp"
		default:
			normMime = "image/" + format
		}
	}

	origB64Len := int64(base64.StdEncoding.EncodedLen(len(data)))

	// Check if already in limits and no normalization needed (BMP needs normalization; animated GIF needs first frame)
	needsNormalization := isAnimated || format == "bmp"
	exceedsDimensions := origW > maxWidth || origH > maxHeight
	exceedsBytes := origB64Len > maxBytes

	if !needsNormalization && !exceedsDimensions && !exceedsBytes {
		return &ProcessedImage{
			Data:           base64.StdEncoding.EncodeToString(data),
			MimeType:       normMime,
			OriginalWidth:  origW,
			OriginalHeight: origH,
			Width:          origW,
			Height:         origH,
			WasResized:     false,
			Notes:          nil,
		}, nil
	}

	// The image must be re-encoded; apply the EXIF orientation transform to the
	// decoded image before computing target dimensions (pass-through images are
	// never touched, keeping their bytes identical even when orientation != 1).
	orientation := getExifOrientation(data)
	if orientation != 1 {
		origImg = applyOrientation(origImg, orientation)
		origW = origImg.Bounds().Dx()
		origH = origImg.Bounds().Dy()
		// Oriented dims may now exceed the limits even when the raw dims fit;
		// recompute before calculating target dimensions.
		exceedsDimensions = origW > maxWidth || origH > maxHeight
	}

	// Calculate target dimensions
	targetW := origW
	targetH := origH
	wasResized := false

	if exceedsDimensions {
		scaleW := float64(maxWidth) / float64(origW)
		scaleH := float64(maxHeight) / float64(origH)
		scale := math.Min(scaleW, scaleH)
		if scale < 1.0 {
			targetW = int(math.Round(float64(origW) * scale))
			targetH = int(math.Round(float64(origH) * scale))
			if targetW < 1 {
				targetW = 1
			}
			if targetH < 1 {
				targetH = 1
			}
			wasResized = true
		}
	}

	currentImg := origImg
	if targetW != origW || targetH != origH {
		currentImg = resizeBilinear(origImg, targetW, targetH)
	}

	// JPEG quality ladder: step down from jpegQuality to >= 40 in steps of 20.
	qualities := []int{jpegQuality}
	for q := ((jpegQuality - 1) / 20) * 20; q >= 40; q -= 20 {
		if q < jpegQuality {
			qualities = append(qualities, q)
		}
	}

	// tryEncode attempts PNG first at this size — preserving alpha/transparency —
	// then JPEG at each quality, returning the first encoding whose base64
	// length fits within maxBytes.
	tryEncode := func(img image.Image) (outBytes []byte, outMime string, ok bool) {
		var buf bytes.Buffer
		if err := png.Encode(&buf, img); err == nil && int64(base64.StdEncoding.EncodedLen(buf.Len())) <= maxBytes {
			return buf.Bytes(), "image/png", true
		}
		for _, q := range qualities {
			buf.Reset()
			if err := jpeg.Encode(&buf, img, &jpeg.Options{Quality: q}); err == nil && int64(base64.StdEncoding.EncodedLen(buf.Len())) <= maxBytes {
				return buf.Bytes(), "image/jpeg", true
			}
		}
		return nil, "", false
	}

	outBytes, outMime, fits := tryEncode(currentImg)

	// If nothing fits, iteratively shrink dimensions until an encoding fits
	curW := targetW
	curH := targetH
	for !fits && (curW > 1 || curH > 1) {
		curW = int(float64(curW) * 0.75)
		curH = int(float64(curH) * 0.75)
		if curW < 1 {
			curW = 1
		}
		if curH < 1 {
			curH = 1
		}
		currentImg = resizeBilinear(origImg, curW, curH)
		targetW = curW
		targetH = curH
		wasResized = true

		outBytes, outMime, fits = tryEncode(currentImg)
	}

	// Final fallback: JPEG at minimum quality.
	if !fits {
		var buf bytes.Buffer
		_ = jpeg.Encode(&buf, currentImg, &jpeg.Options{Quality: 40})
		outBytes = buf.Bytes()
		outMime = "image/jpeg"
	}

	var notes []string
	if isAnimated {
		notes = append(notes, "[Animated GIF: first frame retained]")
	}
	if wasResized {
		notes = append(notes, fmt.Sprintf("resized to %dx%d", targetW, targetH))
	}

	return &ProcessedImage{
		Data:           base64.StdEncoding.EncodeToString(outBytes),
		MimeType:       outMime,
		OriginalWidth:  origW,
		OriginalHeight: origH,
		Width:          targetW,
		Height:         targetH,
		WasResized:     wasResized,
		Notes:          notes,
	}, nil
}
