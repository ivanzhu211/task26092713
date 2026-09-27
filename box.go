package faststart

import (
	"encoding/binary"
	"fmt"
	"io"
)

// boxHeader is a parsed ISO BMFF box header.
type boxHeader struct {
	typ    [4]byte
	size   int64 // total size including header
	header int64 // header length in bytes
	uuid   bool
}

// readBoxHeader reads the box header at offset off. fileSize is used only to
// detect truncation and to resolve size==0 (box extends to end of file).
func readBoxHeader(r io.ReaderAt, off int64, fileSize int64) (boxHeader, error) {
	var h boxHeader
	if off < 0 || off > fileSize {
		return h, fmt.Errorf("faststart: box offset %d out of file bounds (size %d)", off, fileSize)
	}
	var buf [16]byte
	if _, err := r.ReadAt(buf[:8], off); err != nil {
		return h, fmt.Errorf("faststart: truncated file: cannot read box header at %d", off)
	}
	size32 := binary.BigEndian.Uint32(buf[0:4])
	copy(h.typ[:], buf[4:8])
	h.header = 8
	if size32 == 1 {
		if _, err := r.ReadAt(buf[:8], off+8); err != nil {
			return h, fmt.Errorf("faststart: truncated file: cannot read largesize at %d", off)
		}
		large := binary.BigEndian.Uint64(buf[0:8])
		if large > uint64(fileSize) { // also guards against huge values
			return h, fmt.Errorf("faststart: box %q at %d declares size %d beyond file size %d", h.typ, off, large, fileSize)
		}
		h.size = int64(large)
		h.header = 16
	} else {
		h.size = int64(size32)
	}
	if h.typ == [4]byte{'u', 'u', 'i', 'd'} {
		h.uuid = true
		h.header += 16
	}
	if h.size == 0 {
		// Box extends to end of file.
		h.size = fileSize - off
	}
	if h.size < h.header {
		return h, fmt.Errorf("faststart: box %q at %d has size %d smaller than header %d", h.typ, off, h.size, h.header)
	}
	if off+h.size > fileSize {
		return h, fmt.Errorf("faststart: truncated file: box %q at %d ends at %d beyond file size %d", h.typ, off, off+h.size, fileSize)
	}
	return h, nil
}

// topBox is a top-level box of the file.
type topBox struct {
	boxHeader
	offset int64
}

// scanTopLevel lists all top-level boxes of a file of the given size.
func scanTopLevel(r io.ReaderAt, fileSize int64) ([]topBox, error) {
	var boxes []topBox
	for off := int64(0); off < fileSize; {
		h, err := readBoxHeader(r, off, fileSize)
		if err != nil {
			return nil, err
		}
		if h.size <= 0 {
			return nil, fmt.Errorf("faststart: box %q at %d has non-positive size", h.typ, off)
		}
		boxes = append(boxes, topBox{boxHeader: h, offset: off})
		off += h.size
	}
	return boxes, nil
}
