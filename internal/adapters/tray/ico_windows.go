package tray

import (
	"bytes"
	"encoding/binary"
)

// ico wraps PNG images in an .ico file, one entry per size. Windows has
// read PNG entries since Vista.
func ico(pngs ...[]byte) []byte {
	var b bytes.Buffer
	le := func(v any) { _ = binary.Write(&b, binary.LittleEndian, v) }
	le([3]uint16{0, 1, uint16(len(pngs))}) //nolint:gosec // a handful of images
	offset := 6 + 16*len(pngs)
	for _, p := range pngs {
		w, h := pngSize(p)
		le([4]uint8{w, h, 0, 0})
		le([2]uint16{1, 32})
		le([2]uint32{uint32(len(p)), uint32(offset)}) //nolint:gosec // embedded icons, a few KB
		offset += len(p)
	}
	for _, p := range pngs {
		b.Write(p)
	}
	return b.Bytes()
}

// pngSize reads a PNG's width and height from its header, as an .ico entry
// stores them: one byte each, 0 meaning 256.
func pngSize(p []byte) (w, h uint8) {
	if len(p) < 24 {
		return 0, 0
	}
	return uint8(binary.BigEndian.Uint32(p[16:20])), uint8(binary.BigEndian.Uint32(p[20:24])) //nolint:gosec // 0 for 256 is the format
}
