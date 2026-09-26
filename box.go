package faststart

import (
	"encoding/binary"
	"fmt"
	"io"
)

// Box describes one top-level box.
type Box struct {
	Type      [4]byte // box type ("uuid" for extended types)
	Offset    int64   // absolute file offset of the box header
	HeaderLen int64   // header length in bytes (8, 16, or +16 for uuid)
	Size      int64   // total box size including header
}

// TypeString returns the 4-character box type.
func (b Box) TypeString() string { return string(b.Type[:]) }

// readBoxHeader parses one box header at offset off. fileSize resolves
// size==0 (extends to EOF) and detects truncation. The declared size is
// only ever used for bounds checks, never for allocation.
func readBoxHeader(r io.Reader, off, fileSize int64) (Box, error) {
	var hdr [8]byte
	if _, err := io.ReadFull(r, hdr[:]); err != nil {
		return Box{}, fmt.Errorf("faststart: read box header at %d: %w", off, ErrTruncated)
	}
	size32 := binary.BigEndian.Uint32(hdr[0:4])
	var b Box
	copy(b.Type[:], hdr[4:8])
	b.Offset = off
	b.HeaderLen = 8

	var size uint64
	switch size32 {
	case 0:
		// Extends to end of file; only valid for the last top-level box.
		size = uint64(fileSize - off)
	case 1:
		var ext [8]byte
		if _, err := io.ReadFull(r, ext[:]); err != nil {
			return Box{}, fmt.Errorf("faststart: read largesize at %d: %w", off, ErrTruncated)
		}
		size = binary.BigEndian.Uint64(ext[:])
		b.HeaderLen = 16
	default:
		size = uint64(size32)
	}

	if b.Type == [4]byte{'u', 'u', 'i', 'd'} {
		var usertype [16]byte
		if _, err := io.ReadFull(r, usertype[:]); err != nil {
			return Box{}, fmt.Errorf("faststart: read uuid usertype at %d: %w", off, ErrTruncated)
		}
		b.HeaderLen += 16
	}

	if size < uint64(b.HeaderLen) {
		return Box{}, fmt.Errorf("faststart: box %s at %d: size %d smaller than header %d: %w",
			b.TypeString(), off, size, b.HeaderLen, ErrBadSize)
	}
	if size > uint64(fileSize-off) {
		return Box{}, fmt.Errorf("faststart: box %s at %d: size %d exceeds file: %w",
			b.TypeString(), off, size, ErrTruncated)
	}
	b.Size = int64(size)
	return b, nil
}
