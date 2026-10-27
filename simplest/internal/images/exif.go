package images

import "image"

// EXIF orientation parsing and application, ported from pi's
// packages/coding-agent/src/utils/exif-orientation.ts (738319e46, refined by
// c6b00676b). Only the TIFF orientation tag (0x0112) is read; anything
// absent or malformed yields orientation 1 (identity).

// exifHasHeader reports whether the "Exif\0\0" signature is present at offset.
func exifHasHeader(data []byte, offset int) bool {
	return offset+6 <= len(data) &&
		data[offset] == 0x45 && data[offset+1] == 0x78 &&
		data[offset+2] == 0x69 && data[offset+3] == 0x66 &&
		data[offset+4] == 0x00 && data[offset+5] == 0x00
}

// exifReadOrientationFromTiff scans the TIFF structure starting at tiffStart
// for the orientation tag in IFD0. All reads are bounds-checked; any
// malformation returns 1.
func exifReadOrientationFromTiff(data []byte, tiffStart int) int {
	if tiffStart < 0 || tiffStart+8 > len(data) {
		return 1
	}

	le := data[tiffStart] == 0x49 && data[tiffStart+1] == 0x49 // "II"
	be := data[tiffStart] == 0x4D && data[tiffStart+1] == 0x4D // "MM"
	if !le && !be {
		return 1
	}

	read16 := func(pos int) int {
		if pos < 0 || pos+2 > len(data) {
			return -1
		}
		if le {
			return int(data[pos]) | int(data[pos+1])<<8
		}
		return int(data[pos])<<8 | int(data[pos+1])
	}
	read32 := func(pos int) int {
		if pos < 0 || pos+4 > len(data) {
			return -1
		}
		if le {
			return int(data[pos]) | int(data[pos+1])<<8 | int(data[pos+2])<<16 | int(data[pos+3])<<24
		}
		return int(data[pos])<<24 | int(data[pos+1])<<16 | int(data[pos+2])<<8 | int(data[pos+3])
	}

	ifdOffset := read32(tiffStart + 4)
	if ifdOffset < 0 {
		return 1
	}
	ifdStart := tiffStart + ifdOffset
	if ifdStart < 0 || ifdStart+2 > len(data) {
		return 1
	}

	entryCount := read16(ifdStart)
	for i := 0; i < entryCount; i++ {
		entryPos := ifdStart + 2 + i*12
		if entryPos < 0 || entryPos+12 > len(data) {
			return 1
		}
		if tag := read16(entryPos); tag == 0x0112 {
			value := read16(entryPos + 8)
			if value >= 1 && value <= 8 {
				return value
			}
			return 1
		}
	}

	return 1
}

// exifFindJpegTiffOffset walks the JPEG marker stream and returns the offset
// of the TIFF header inside the first APP1 segment carrying the "Exif\0\0"
// signature, or -1. Non-EXIF APP1 segments are skipped, as are fill bytes
// (0xFF padding between markers).
func exifFindJpegTiffOffset(data []byte) int {
	offset := 2
	for offset < len(data)-1 {
		if data[offset] != 0xFF {
			return -1
		}
		marker := data[offset+1]
		if marker == 0xFF {
			// Fill byte; keep scanning.
			offset++
			continue
		}

		if marker == 0xE1 {
			segmentStart := offset + 4
			if segmentStart+6 > len(data) {
				return -1
			}
			if exifHasHeader(data, segmentStart) {
				return segmentStart + 6
			}
		}

		if offset+4 > len(data) {
			return -1
		}
		length := int(data[offset+2])<<8 | int(data[offset+3])
		offset += 2 + length
	}

	return -1
}

// exifFindWebpTiffOffset walks the RIFF chunk list and returns the offset of
// the TIFF header inside the "EXIF" chunk, or -1. RIFF chunks are padded to
// even sizes; some encoders also prefix the payload with "Exif\0\0".
func exifFindWebpTiffOffset(data []byte) int {
	offset := 12
	for offset+8 <= len(data) {
		chunkID := string(data[offset : offset+4])
		// Parse as 32-bit unsigned first to avoid negative int for sizes >= 0x80000000.
		chunkSize := int64(uint32(data[offset+4]) | uint32(data[offset+5])<<8 |
			uint32(data[offset+6])<<16 | uint32(data[offset+7])<<24)
		if chunkSize < 0 { // unreachable for uint32, kept for clarity
			return -1
		}
		dataStart := int64(offset + 8)
		if dataStart+chunkSize > int64(len(data)) {
			return -1
		}

		if chunkID == "EXIF" {
			if chunkSize >= 6 && exifHasHeader(data, int(dataStart)) {
				return int(dataStart + 6)
			}
			return int(dataStart)
		}

		// RIFF chunks are padded to even size.
		offset = int(dataStart + chunkSize + chunkSize%2)
	}

	return -1
}

// getExifOrientation returns the EXIF orientation (1-8) of the raw encoded
// image bytes, or 1 when the format is unsupported, the tag is absent, or the
// metadata is malformed.
func getExifOrientation(data []byte) int {
	var tiffOffset int
	switch {
	case len(data) >= 2 && data[0] == 0xFF && data[1] == 0xD8:
		tiffOffset = exifFindJpegTiffOffset(data)
	case len(data) >= 12 && string(data[0:4]) == "RIFF" && string(data[8:12]) == "WEBP":
		tiffOffset = exifFindWebpTiffOffset(data)
	default:
		return 1
	}

	if tiffOffset < 0 {
		return 1
	}
	return exifReadOrientationFromTiff(data, tiffOffset)
}

// applyOrientation returns a new RGBA image with the EXIF orientation
// transform applied to src. The destination pixel (x, y) samples the source
// using the standard destination-from-source mapping below (source dims
// w x h; orientations 5-8 produce h x w output):
//
//	1: (x, y)           5: (y, x)
//	2: (w-1-x, y)       6: (y, h-1-x)
//	3: (w-1-x, h-1-y)   7: (w-1-y, h-1-x)
//	4: (x, h-1-y)       8: (w-1-y, x)
//
// It works for any image.Image, including images with non-zero bounds.
func applyOrientation(src image.Image, orientation int) image.Image {
	if orientation < 1 || orientation > 8 {
		orientation = 1
	}

	b := src.Bounds()
	w, h := b.Dx(), b.Dy()
	dstW, dstH := w, h
	if orientation >= 5 {
		dstW, dstH = h, w
	}

	dst := image.NewRGBA(image.Rect(0, 0, dstW, dstH))
	for y := 0; y < dstH; y++ {
		for x := 0; x < dstW; x++ {
			sx, sy := x, y
			switch orientation {
			case 2:
				sx = w - 1 - x
			case 3:
				sx, sy = w-1-x, h-1-y
			case 4:
				sy = h - 1 - y
			case 5:
				sx, sy = y, x
			case 6:
				sx, sy = y, h-1-x
			case 7:
				sx, sy = w-1-y, h-1-x
			case 8:
				sx, sy = w-1-y, x
			}
			dst.Set(x, y, src.At(b.Min.X+sx, b.Min.Y+sy))
		}
	}
	return dst
}
