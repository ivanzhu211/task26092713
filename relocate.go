package faststart

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
)

const copyBufSize = 4 << 20

// mdatRange is the payload extent of one mdat box.
type mdatRange struct {
	boxIndex int
	start    uint64 // payload start (absolute file offset)
	end      uint64 // payload end
}

// Relocate writes a copy of src to dst with the moov box placed before all
// mdat boxes. If moov is already before every mdat, dst is a byte-identical
// copy of src. dst is written atomically: on any error no partial file is
// left behind.
func Relocate(src, dst string) error {
	absSrc, err1 := filepath.Abs(src)
	absDst, err2 := filepath.Abs(dst)
	if err1 == nil && err2 == nil && absSrc == absDst {
		return fmt.Errorf("faststart: src and dst are the same file")
	}
	f, err := os.Open(src)
	if err != nil {
		return err
	}
	defer f.Close()
	fi, err := f.Stat()
	if err != nil {
		return err
	}
	fileSize := fi.Size()

	boxes, err := scanTopLevel(f, fileSize)
	if err != nil {
		return err
	}
	for _, b := range boxes {
		if b.typ == [4]byte{'m', 'o', 'o', 'f'} {
			return fmt.Errorf("faststart: fragmented MP4 (moof) not supported")
		}
	}
	moovIdx := -1
	var ftypIdx = -1
	var mdats []mdatRange
	for i, b := range boxes {
		switch b.typ {
		case [4]byte{'m', 'o', 'o', 'v'}:
			if moovIdx >= 0 {
				return fmt.Errorf("faststart: multiple moov boxes")
			}
			moovIdx = i
		case [4]byte{'f', 't', 'y', 'p'}:
			if ftypIdx < 0 {
				ftypIdx = i
			}
		case [4]byte{'m', 'd', 'a', 't'}:
			mdats = append(mdats, mdatRange{
				boxIndex: i,
				start:    uint64(b.offset + b.header),
				end:      uint64(b.offset + b.size),
			})
		}
	}
	if moovIdx < 0 {
		return fmt.Errorf("faststart: no moov box")
	}

	// Read and parse moov (memory grows with moov, never with mdat).
	moovData := make([]byte, boxes[moovIdx].size)
	if _, err := f.ReadAt(moovData, boxes[moovIdx].offset); err != nil {
		return fmt.Errorf("faststart: cannot read moov: %w", err)
	}
	moov, err := parseBoxTree(moovData, 0)
	if err != nil {
		return err
	}
	if moov.hasBox("mvex") {
		return fmt.Errorf("faststart: fragmented MP4 (mvex) not supported")
	}
	tracks, err := extractTracks(moov)
	if err != nil {
		return err
	}

	// Validate sample tables and locate every chunk inside an mdat payload.
	chunkMdat := make([][]int, len(tracks))
	for ti, t := range tracks {
		if err := t.validate(); err != nil {
			return err
		}
		lengths := t.chunkLengths()
		chunkMdat[ti] = make([]int, len(lengths))
		for c, cl := range lengths {
			off := t.chunkTable.entries[c]
			mi := findMdat(mdats, off, cl)
			if mi < 0 {
				return fmt.Errorf("faststart: chunk %d of track %d (offset %d, %d bytes) lies outside all mdat payloads", c, ti, off, cl)
			}
			chunkMdat[ti][c] = mi
		}
	}

	// Already faststart? moov before every mdat -> byte-identical copy.
	moovBeforeAll := true
	for _, m := range mdats {
		if boxes[moovIdx].offset > boxes[m.boxIndex].offset {
			moovBeforeAll = false
			break
		}
	}
	if moovBeforeAll {
		return atomicWrite(dst, fileSize, func(w *os.File) error {
			return copyRange(f, w, 0, fileSize)
		})
	}

	// New top-level order: ftyp (if any), moov, then the remaining boxes in
	// their original relative order.
	var order []int
	if ftypIdx >= 0 {
		order = append(order, ftypIdx)
	}
	order = append(order, moovIdx)
	for i := range boxes {
		if i != ftypIdx && i != moovIdx {
			order = append(order, i)
		}
	}

	// Iteratively find the minimal set of stco tables that must be widened
	// to co64: upgrading grows moov, which may push further offsets past
	// 2^32. Offsets only grow, so the loop terminates.
	upgraded := map[*mp4Box]bool{}
	var newBoxOff []int64 // new offset per original box index
	for {
		for _, t := range tracks {
			if upgraded[t.chunkTable] {
				t.chunkTable.co64 = true
				t.chunkTable.typ = [4]byte{'c', 'o', '6', '4'}
			}
		}
		moovSize := moov.computeSizes()
		newBoxOff = layoutOffsets(boxes, order, moovIdx, moovSize)
		changed := false
		for ti, t := range tracks {
			ct := t.chunkTable
			if ct.co64 {
				continue
			}
			for c, e := range ct.entries {
				if newOffset(e, boxes, newBoxOff, mdats, chunkMdat[ti][c]) >= 1<<32 {
					upgraded[ct] = true
					changed = true
					break
				}
			}
		}
		if !changed {
			break
		}
	}

	// Apply the final shifts to the chunk entries.
	for ti, t := range tracks {
		for c := range t.chunkTable.entries {
			t.chunkTable.entries[c] = uint64(newOffset(t.chunkTable.entries[c], boxes, newBoxOff, mdats, chunkMdat[ti][c]))
		}
	}
	moovSize := moov.computeSizes()
	newBoxOff = layoutOffsets(boxes, order, moovIdx, moovSize)
	newMoov := make([]byte, moovSize)
	moov.marshal(newMoov)

	total := int64(0)
	for _, i := range order {
		if i == moovIdx {
			total += moovSize
		} else {
			total += boxes[i].size
		}
	}
	return atomicWrite(dst, total, func(w *os.File) error {
		for _, i := range order {
			if i == moovIdx {
				if _, err := w.Write(newMoov); err != nil {
					return err
				}
				continue
			}
			if err := copyRange(f, w, boxes[i].offset, boxes[i].size); err != nil {
				return err
			}
		}
		return nil
	})
}

// findMdat returns the index of the mdat whose payload fully contains the
// span [off, off+length), or -1.
func findMdat(mdats []mdatRange, off uint64, length uint64) int {
	for i, m := range mdats {
		if off >= m.start && off <= m.end && length <= m.end-off {
			return i
		}
	}
	return -1
}

// layoutOffsets computes the new absolute offset of every top-level box
// given the output order and the new moov size.
func layoutOffsets(boxes []topBox, order []int, moovIdx int, moovSize int64) []int64 {
	offs := make([]int64, len(boxes))
	pos := int64(0)
	for _, i := range order {
		offs[i] = pos
		if i == moovIdx {
			pos += moovSize
		} else {
			pos += boxes[i].size
		}
	}
	return offs
}

// newOffset maps an old chunk offset into the output file.
func newOffset(old uint64, boxes []topBox, newBoxOff []int64, mdats []mdatRange, mdatIdx int) int64 {
	m := mdats[mdatIdx]
	delta := newBoxOff[m.boxIndex] - boxes[m.boxIndex].offset
	return int64(old) + delta
}

// copyRange copies n bytes from r at offset off to w, skipping all-zero
// blocks with a seek so sparse files stay sparse.
func copyRange(r io.ReaderAt, w io.WriteSeeker, off int64, n int64) error {
	buf := make([]byte, copyBufSize)
	for n > 0 {
		chunk := int64(len(buf))
		if chunk > n {
			chunk = n
		}
		if _, err := r.ReadAt(buf[:chunk], off); err != nil {
			return fmt.Errorf("faststart: read error during copy: %w", err)
		}
		if isZero(buf[:chunk]) {
			if _, err := w.Seek(chunk, io.SeekCurrent); err != nil {
				return err
			}
		} else {
			if _, err := w.Write(buf[:chunk]); err != nil {
				return err
			}
		}
		off += chunk
		n -= chunk
	}
	return nil
}

func isZero(b []byte) bool {
	for _, v := range b {
		if v != 0 {
			return false
		}
	}
	return true
}

// atomicWrite writes to a temporary file next to dst and renames it into
// place. The file is truncated to size after writing (zero blocks skipped
// during the copy may otherwise leave the file short). On any error the
// temporary file is removed.
func atomicWrite(dst string, size int64, write func(w *os.File) error) error {
	dir := filepath.Dir(dst)
	tmp, err := os.CreateTemp(dir, ".faststart-*")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	cleanup := func(err error) error {
		tmp.Close()
		os.Remove(tmpName)
		return err
	}
	if err := write(tmp); err != nil {
		return cleanup(err)
	}
	if err := tmp.Truncate(size); err != nil {
		return cleanup(err)
	}
	if err := tmp.Sync(); err != nil {
		return cleanup(err)
	}
	if err := tmp.Close(); err != nil {
		os.Remove(tmpName)
		return err
	}
	if err := os.Rename(tmpName, dst); err != nil {
		os.Remove(tmpName)
		return err
	}
	return nil
}
