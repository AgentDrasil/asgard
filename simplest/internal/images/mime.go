package images

import (
	"encoding/binary"
)

// Port of pi 005af57d8: packages/coding-agent/src/utils/mime.ts
// (detectSupportedImageMimeType). Sniffs at most the first 4100 bytes and
// returns a supported image MIME type, or "" when the content is not a
// supported image (in which case callers should read the file as text).

const imageTypeSniffBytes = 4100

var pngSignature = []byte{0x89, 0x50, 0x4e, 0x47, 0x0d, 0x0a, 0x1a, 0x0a}

// DetectMimeType reports the supported image MIME type of data based on its
// content, or "" if data does not look like a supported image.
func DetectMimeType(data []byte) string {
	if len(data) > imageTypeSniffBytes {
		data = data[:imageTypeSniffBytes]
	}

	if startsWithBytes(data, []byte{0xff, 0xd8, 0xff}) {
		if len(data) > 3 && data[3] == 0xf7 {
			return "" // JPEG-LS / SOF55: not supported
		}
		return "image/jpeg"
	}
	if startsWithBytes(data, pngSignature) {
		if isPNG(data) && !isAnimatedPNG(data) {
			return "image/png"
		}
		return ""
	}
	if startsWithASCII(data, 0, "GIF87a") || startsWithASCII(data, 0, "GIF89a") {
		return "image/gif"
	}
	if startsWithASCII(data, 0, "RIFF") && startsWithASCII(data, 8, "WEBP") {
		return "image/webp"
	}
	if startsWithASCII(data, 0, "BM") && isBMP(data) {
		return "image/bmp"
	}
	return ""
}

func isPNG(data []byte) bool {
	return len(data) >= 16 &&
		binary.BigEndian.Uint32(data[8:12]) == 13 &&
		startsWithASCII(data, 12, "IHDR")
}

func isAnimatedPNG(data []byte) bool {
	offset := len(pngSignature)
	for offset+8 <= len(data) {
		chunkLength := binary.BigEndian.Uint32(data[offset : offset+4])
		chunkTypeOffset := offset + 4
		if startsWithASCII(data, chunkTypeOffset, "acTL") {
			return true
		}
		if startsWithASCII(data, chunkTypeOffset, "IDAT") {
			return false
		}
		nextOffset := offset + 8 + int(chunkLength) + 4
		if nextOffset <= offset || nextOffset > len(data) {
			// A chunk overrunning the sniff window: we cannot prove the file
			// is animated, so treat it as a normal image upstream would.
			return false
		}
		offset = nextOffset
	}
	return false
}

func isBMP(data []byte) bool {
	if len(data) < 26 {
		return false
	}

	declaredFileSize := binary.LittleEndian.Uint32(data[2:6])
	pixelDataOffset := binary.LittleEndian.Uint32(data[10:14])
	dibHeaderSize := binary.LittleEndian.Uint32(data[14:18])
	if declaredFileSize != 0 && declaredFileSize < 26 {
		return false
	}
	if uint64(pixelDataOffset) < 14+uint64(dibHeaderSize) {
		return false
	}
	if declaredFileSize != 0 && pixelDataOffset >= declaredFileSize {
		return false
	}

	var colorPlanes, bitsPerPixel uint16
	switch {
	case dibHeaderSize == 12:
		colorPlanes = binary.LittleEndian.Uint16(data[22:24])
		bitsPerPixel = binary.LittleEndian.Uint16(data[24:26])
	case dibHeaderSize >= 40 && dibHeaderSize <= 124:
		if len(data) < 30 {
			return false
		}
		colorPlanes = binary.LittleEndian.Uint16(data[26:28])
		bitsPerPixel = binary.LittleEndian.Uint16(data[28:30])
	default:
		return false
	}

	if colorPlanes != 1 {
		return false
	}
	switch bitsPerPixel {
	case 1, 4, 8, 16, 24, 32:
		return true
	default:
		return false
	}
}

func startsWithBytes(data, prefix []byte) bool {
	return len(data) >= len(prefix) && string(data[:len(prefix)]) == string(prefix)
}

func startsWithASCII(data []byte, offset int, text string) bool {
	return offset+len(text) <= len(data) && string(data[offset:offset+len(text)]) == text
}
