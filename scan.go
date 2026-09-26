package faststart

import (
	"fmt"
	"io"
)

// scanTopLevel walks all top-level boxes of the file. It rejects
// fragmented files (top-level moof) and enforces that a size==0 box is
// the last box in the file.
func scanTopLevel(rs io.ReadSeeker, fileSize int64) ([]Box, error) {
	var boxes []Box
	off := int64(0)
	for off < fileSize {
		if _, err := rs.Seek(off, io.SeekStart); err != nil {
			return nil, err
		}
		b, err := readBoxHeader(rs, off, fileSize)
		if err != nil {
			return nil, err
		}
		if b.Type == [4]byte{'m', 'o', 'o', 'f'} {
			return nil, fmt.Errorf("faststart: moof at %d: %w", off, ErrFragmented)
		}
		boxes = append(boxes, b)
		off += b.Size
	}
	if off != fileSize {
		return nil, fmt.Errorf("faststart: top-level boxes end at %d, file size %d: %w",
			off, fileSize, ErrTruncated)
	}
	return boxes, nil
}

// fileSizeOf returns the size of the file behind rs.
func fileSizeOf(rs io.ReadSeeker) (int64, error) {
	cur, err := rs.Seek(0, io.SeekCurrent)
	if err != nil {
		return 0, err
	}
	end, err := rs.Seek(0, io.SeekEnd)
	if err != nil {
		return 0, err
	}
	if _, err := rs.Seek(cur, io.SeekStart); err != nil {
		return 0, err
	}
	return end, nil
}
