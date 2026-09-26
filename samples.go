package faststart

import (
	"bytes"
	"io"
)

// SamplesEqual reports whether the files at aPath and bPath contain the
// same sample bytes, compared track by track and sample by sample using
// each file's own sample tables.
func SamplesEqual(aPath, bPath string) (bool, error) {
	pa, err := parseFile(aPath)
	if err != nil {
		return false, err
	}
	defer pa.f.Close()
	pb, err := parseFile(bPath)
	if err != nil {
		return false, err
	}
	defer pb.f.Close()

	if len(pa.moov.tracks) != len(pb.moov.tracks) {
		return false, nil
	}
	for ti := range pa.moov.tracks {
		ta := pa.moov.tracks[ti]
		tb := pb.moov.tracks[ti]
		if ta.sizes.Len() != tb.sizes.Len() {
			return false, nil
		}
		eq, err := compareTrack(pa, pb, ti)
		if err != nil || !eq {
			return eq, err
		}
	}
	return true, nil
}

// compareTrack compares every sample of track ti between the two files.
func compareTrack(pa, pb *parsedFile, ti int) (bool, error) {
	ta := pa.moov.tracks[ti]
	tb := pb.moov.tracks[ti]
	ca := &sampleCursor{tk: ta, perChunk: pa.perChunk[ti]}
	cb := &sampleCursor{tk: tb, perChunk: pb.perChunk[ti]}
	for {
		offA, sizeA, okA := ca.next()
		offB, sizeB, okB := cb.next()
		if okA != okB {
			return false, nil
		}
		if !okA {
			return true, nil
		}
		if sizeA != sizeB {
			return false, nil
		}
		eq, err := compareRange(pa.f, int64(offA), pb.f, int64(offB), int64(sizeA))
		if err != nil || !eq {
			return eq, err
		}
	}
}

// sampleCursor walks a track's samples in chunk order.
type sampleCursor struct {
	tk          *trackInfo
	perChunk    []uint32
	chunk       uint32 // current chunk index
	sampleInChk uint32 // sample index within current chunk
	sampleBase  uint32 // index of first sample of current chunk
	inChunkOff  uint64 // byte offset of current sample within the chunk
}

// next returns the file offset and size of the next sample.
func (c *sampleCursor) next() (off uint64, size uint32, ok bool) {
	for c.chunk < c.tk.table.count {
		if c.sampleInChk < c.perChunk[c.chunk] {
			idx := c.sampleBase + c.sampleInChk
			sz := c.tk.sizes.At(idx)
			off = c.tk.table.entries[c.chunk] + c.inChunkOff
			c.inChunkOff += uint64(sz)
			c.sampleInChk++
			return off, sz, true
		}
		c.sampleBase += c.perChunk[c.chunk]
		c.chunk++
		c.sampleInChk = 0
		c.inChunkOff = 0
	}
	return 0, 0, false
}

// compareRange compares n bytes at offA in ra with n bytes at offB in rb.
func compareRange(ra io.ReadSeeker, offA int64, rb io.ReadSeeker, offB int64, n int64) (bool, error) {
	if _, err := ra.Seek(offA, io.SeekStart); err != nil {
		return false, err
	}
	if _, err := rb.Seek(offB, io.SeekStart); err != nil {
		return false, err
	}
	ba := make([]byte, 0, copyBufSize)
	bb := make([]byte, 0, copyBufSize)
	remaining := n
	for remaining > 0 {
		chunk := int64(copyBufSize)
		if remaining < chunk {
			chunk = remaining
		}
		ba = ba[:chunk]
		bb = bb[:chunk]
		if _, err := io.ReadFull(ra, ba); err != nil {
			return false, err
		}
		if _, err := io.ReadFull(rb, bb); err != nil {
			return false, err
		}
		if !bytes.Equal(ba, bb) {
			return false, nil
		}
		remaining -= chunk
	}
	return true, nil
}
