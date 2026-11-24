package gitx

import (
	"bytes"
	"encoding/binary"
	"hash/crc32"
	"image"
	_ "image/jpeg" // registers the decoder image.Decode uses
	_ "image/png"  // registers the decoder image.Decode uses
)

// maxVerifiedPixels bounds what parseBinaryFormat decodes: a small compressed
// image can expand to gigabytes. A larger image stays unverified.
const maxVerifiedPixels = 16 << 20

// parseBinaryFormat names the image format blob decodes as, with nothing after
// the image's end marker, or "" when it does not. That catches a script
// appended to an image. It cannot catch data hidden inside one: metadata can
// hold anything, and decoders skip extra bytes in the image data. A match says
// the file is a real image, not that it is harmless.
func parseBinaryFormat(blob []byte) string {
	format := ""
	switch {
	case validPNG(blob):
		format = "png"
	case validJPEG(blob):
		format = "jpeg"
	default:
		return ""
	}
	config, _, err := image.DecodeConfig(bytes.NewReader(blob))
	if err != nil || config.Width <= 0 || config.Height <= 0 || int64(config.Width)*int64(config.Height) > maxVerifiedPixels {
		return ""
	}
	if _, _, err = image.Decode(bytes.NewReader(blob)); err != nil {
		return ""
	}
	return format
}

// validPNG walks the chunks, checking each CRC, up to an IEND at the last byte.
func validPNG(b []byte) bool {
	if !bytes.HasPrefix(b, []byte("\x89PNG\r\n\x1a\n")) {
		return false
	}
	rest := b[8:]
	for first := true; len(rest) >= 12; first = false {
		size := uint64(binary.BigEndian.Uint32(rest))
		if size > uint64(len(rest))-12 {
			return false
		}
		end := 8 + int(size)
		kind := string(rest[4:8])
		if (first && kind != "IHDR") || crc32.ChecksumIEEE(rest[4:end]) != binary.BigEndian.Uint32(rest[end:]) {
			return false
		}
		rest = rest[end+4:]
		if kind == "IEND" {
			return len(rest) == 0
		}
	}
	return false
}

// validJPEG walks the marker segments and entropy-coded scans up to an EOI at
// the last byte.
func validJPEG(b []byte) bool {
	if len(b) < 4 || b[0] != 0xff || b[1] != 0xd8 {
		return false
	}
	restart := func(marker byte) bool { return marker >= 0xd0 && marker <= 0xd7 }
	for i := 2; i+1 < len(b); {
		if b[i] != 0xff {
			return false
		}
		for i+1 < len(b) && b[i+1] == 0xff { // fill bytes
			i++
		}
		if i+1 >= len(b) {
			return false
		}
		marker := b[i+1]
		i += 2
		switch {
		case marker == 0xd9:
			return i == len(b)
		case marker == 0x00 || marker == 0xd8:
			return false
		case marker == 0x01 || restart(marker):
			continue
		}
		if i+2 > len(b) {
			return false
		}
		size := int(binary.BigEndian.Uint16(b[i:]))
		if size < 2 || i+size > len(b) {
			return false
		}
		i += size
		if marker == 0xda {
			for i+1 < len(b) && (b[i] != 0xff || b[i+1] == 0x00 || restart(b[i+1])) {
				i++
			}
		}
	}
	return false
}
