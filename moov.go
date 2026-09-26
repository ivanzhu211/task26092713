package faststart

import (
	"encoding/binary"
	"fmt"
	"io"
)

// innerBox is a box inside the moov payload. Offsets are relative to the
// start of the moov payload.
type innerBox struct {
	typ          [4]byte
	off          int64 // offset of box start within payload
	size         int64 // total size including header
	headerLen    int64
	sizeFieldOff int64 // offset of the size field within payload
	sizeLarge    bool  // true if the size field is 64-bit (largesize form)
}

// stscEntry is one row of the sample-to-chunk table.
type stscEntry struct {
	firstChunk      uint32
	samplesPerChunk uint32
}

// trackInfo holds the parsed sample tables of one trak.
type trackInfo struct {
	table   *chunkTable
	stsc    []stscEntry
	sizes   sizeTable
	hasStsc bool
	hasStsz bool
}

// sizeTable is a compact per-sample size table. A constant-size stsz is
// stored as (value, count) so memory never depends on a declared count
// alone.
type sizeTable struct {
	count    uint32
	constant bool
	value    uint32   // valid when constant
	vals     []uint32 // valid when !constant, len == count
}

func (s sizeTable) Len() uint32 { return s.count }

func (s sizeTable) At(i uint32) uint32 {
	if s.constant {
		return s.value
	}
	return s.vals[i]
}

// Span returns the total size of n consecutive samples starting at
// index start. It is O(1) for constant-size tables.
func (s sizeTable) Span(start, n uint32) uint64 {
	if s.constant {
		return uint64(n) * uint64(s.value)
	}
	var sum uint64
	for i := start; i < start+n; i++ {
		sum += uint64(s.vals[i])
	}
	return sum
}

// moovInfo is the parsed moov box.
type moovInfo struct {
	box     Box
	payload []byte // moov payload (everything after the moov header)
	tracks  []*trackInfo
}

// readMoovPayload reads the moov payload into memory. It never
// pre-allocates from the declared box size; the buffer grows as bytes
// actually arrive.
func readMoovPayload(rs io.ReadSeeker, b Box) ([]byte, error) {
	if _, err := rs.Seek(b.Offset+b.HeaderLen, io.SeekStart); err != nil {
		return nil, err
	}
	payload, err := io.ReadAll(&io.LimitedReader{R: rs, N: b.Size - b.HeaderLen})
	if err != nil {
		return nil, err
	}
	if int64(len(payload)) != b.Size-b.HeaderLen {
		return nil, fmt.Errorf("faststart: moov payload short: %w", ErrTruncated)
	}
	return payload, nil
}

// walkBoxes parses the box sequence in buf[start:end]. Every box must be
// well formed and bounded by end.
func walkBoxes(buf []byte, start, end int64, fn func(innerBox) error) error {
	off := start
	for off < end {
		if end-off < 8 {
			return fmt.Errorf("faststart: dangling %d bytes at payload offset %d: %w",
				end-off, off, ErrTruncated)
		}
		size32 := binary.BigEndian.Uint32(buf[off : off+8])
		var ib innerBox
		copy(ib.typ[:], buf[off+4:off+8])
		ib.off = off
		ib.headerLen = 8
		ib.sizeFieldOff = off
		var size uint64
		switch size32 {
		case 0:
			return fmt.Errorf("faststart: size==0 box %s inside moov at %d: %w",
				string(ib.typ[:]), off, ErrInconsistent)
		case 1:
			if end-off < 16 {
				return fmt.Errorf("faststart: truncated largesize header at %d: %w", off, ErrTruncated)
			}
			size = binary.BigEndian.Uint64(buf[off+8 : off+16])
			ib.headerLen = 16
			ib.sizeLarge = true
		default:
			size = uint64(size32)
		}
		if ib.typ == [4]byte{'u', 'u', 'i', 'd'} {
			if end-off < ib.headerLen+16 {
				return fmt.Errorf("faststart: truncated uuid header at %d: %w", off, ErrTruncated)
			}
			ib.headerLen += 16
		}
		if size < uint64(ib.headerLen) || size > uint64(end-off) {
			return fmt.Errorf("faststart: box %s at payload %d: bad size %d: %w",
				string(ib.typ[:]), off, size, ErrInconsistent)
		}
		ib.size = int64(size)
		if err := fn(ib); err != nil {
			return err
		}
		off += ib.size
	}
	if off != end {
		return fmt.Errorf("faststart: boxes overrun parent: %w", ErrInconsistent)
	}
	return nil
}

// parseMoov parses the moov payload and extracts every trak's tables.
func parseMoov(b Box, payload []byte) (*moovInfo, error) {
	mi := &moovInfo{box: b, payload: payload}
	err := walkBoxes(payload, 0, int64(len(payload)), func(ib innerBox) error {
		switch ib.typ {
		case [4]byte{'m', 'v', 'e', 'x'}:
			return ErrFragmented
		case [4]byte{'t', 'r', 'a', 'k'}:
			tk, err := parseTrak(payload, ib)
			if err != nil {
				return err
			}
			mi.tracks = append(mi.tracks, tk)
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return mi, nil
}

// parseTrak descends trak/mdia/minf/stbl and reads the sample tables.
func parseTrak(payload []byte, trak innerBox) (*trackInfo, error) {
	tk := &trackInfo{}
	content := func(parent innerBox, name [4]byte, fn func(innerBox) error) error {
		return walkBoxes(payload, parent.off+parent.headerLen, parent.off+parent.size,
			func(ib innerBox) error {
				if ib.typ == name {
					return fn(ib)
				}
				return nil
			})
	}
	var chain []innerBox
	err := content(trak, [4]byte{'m', 'd', 'i', 'a'}, func(mdia innerBox) error {
		return content(mdia, [4]byte{'m', 'i', 'n', 'f'}, func(minf innerBox) error {
			return content(minf, [4]byte{'s', 't', 'b', 'l'}, func(stbl innerBox) error {
				chain = []innerBox{trak, mdia, minf, stbl}
				return nil
			})
		})
	})
	if err != nil {
		return nil, err
	}
	if chain == nil {
		return nil, fmt.Errorf("faststart: trak without stbl: %w", ErrInconsistent)
	}
	stblBox := chain[3]
	ancestors := make([]sizeFieldRef, len(chain))
	for i := len(chain) - 1; i >= 0; i-- {
		ancestors[len(chain)-1-i] = sizeFieldRef{off: chain[i].sizeFieldOff, large: chain[i].sizeLarge}
	}
	err = walkBoxes(payload, stblBox.off+stblBox.headerLen, stblBox.off+stblBox.size,
		func(ib innerBox) error {
			switch ib.typ {
			case [4]byte{'s', 't', 'c', 'o'}:
				t, err := parseChunkTable(payload, ib, false)
				if err != nil {
					return err
				}
				t.ancestors = ancestors
				tk.table = t
			case [4]byte{'c', 'o', '6', '4'}:
				t, err := parseChunkTable(payload, ib, true)
				if err != nil {
					return err
				}
				t.ancestors = ancestors
				tk.table = t
			case [4]byte{'s', 't', 's', 'c'}:
				entries, err := parseStsc(payload, ib)
				if err != nil {
					return err
				}
				tk.stsc = entries
				tk.hasStsc = true
			case [4]byte{'s', 't', 's', 'z'}:
				sizes, err := parseStsz(payload, ib)
				if err != nil {
					return err
				}
				tk.sizes = sizes
				tk.hasStsz = true
			case [4]byte{'s', 't', 'z', '2'}:
				sizes, err := parseStz2(payload, ib)
				if err != nil {
					return err
				}
				tk.sizes = sizes
				tk.hasStsz = true
			}
			return nil
		})
	if err != nil {
		return nil, err
	}
	if tk.table == nil || !tk.hasStsc || !tk.hasStsz {
		return nil, fmt.Errorf("faststart: trak missing stco/co64, stsc or stsz/stz2: %w",
			ErrInconsistent)
	}
	return tk, nil
}

// parseChunkTable reads an stco/co64 box and caches its entries.
func parseChunkTable(payload []byte, ib innerBox, co64 bool) (*chunkTable, error) {
	base := ib.off + ib.headerLen
	end := ib.off + ib.size
	if end-base < 8 {
		return nil, fmt.Errorf("faststart: chunk table too small: %w", ErrInconsistent)
	}
	count := binary.BigEndian.Uint32(payload[base+4 : base+8])
	entrySize := int64(4)
	if co64 {
		entrySize = 8
	}
	if int64(count) > (end-base-8)/entrySize {
		return nil, fmt.Errorf("faststart: chunk table entry_count %d exceeds box: %w",
			count, ErrInconsistent)
	}
	t := &chunkTable{
		co64:      co64,
		boxOff:    ib.off,
		headerLen: ib.headerLen,
		size:      ib.size,
		entryBase: base + 8,
		count:     count,
		entries:   make([]uint64, count),
	}
	for i := uint32(0); i < count; i++ {
		p := t.entryBase + int64(i)*entrySize
		if co64 {
			t.entries[i] = binary.BigEndian.Uint64(payload[p : p+8])
		} else {
			t.entries[i] = uint64(binary.BigEndian.Uint32(payload[p : p+4]))
		}
	}
	return t, nil
}

// parseStsc reads an stsc box.
func parseStsc(payload []byte, ib innerBox) ([]stscEntry, error) {
	base := ib.off + ib.headerLen
	end := ib.off + ib.size
	if end-base < 8 {
		return nil, fmt.Errorf("faststart: stsc too small: %w", ErrInconsistent)
	}
	count := binary.BigEndian.Uint32(payload[base+4 : base+8])
	if int64(count) > (end-base-8)/12 {
		return nil, fmt.Errorf("faststart: stsc entry_count %d exceeds box: %w",
			count, ErrInconsistent)
	}
	entries := make([]stscEntry, count)
	for i := uint32(0); i < count; i++ {
		p := base + 8 + int64(i)*12
		entries[i] = stscEntry{
			firstChunk:      binary.BigEndian.Uint32(payload[p : p+4]),
			samplesPerChunk: binary.BigEndian.Uint32(payload[p+4 : p+8]),
		}
	}
	return entries, nil
}

// parseStsz reads an stsz box and expands it to per-sample sizes.
func parseStsz(payload []byte, ib innerBox) (sizeTable, error) {
	base := ib.off + ib.headerLen
	end := ib.off + ib.size
	if end-base < 12 {
		return sizeTable{}, fmt.Errorf("faststart: stsz too small: %w", ErrInconsistent)
	}
	sampleSize := binary.BigEndian.Uint32(payload[base+4 : base+8])
	count := binary.BigEndian.Uint32(payload[base+8 : base+12])
	if sampleSize != 0 {
		if end-base != 12 {
			return sizeTable{}, fmt.Errorf("faststart: stsz with default size has entries: %w",
				ErrInconsistent)
		}
		return sizeTable{count: count, constant: true, value: sampleSize}, nil
	}
	if int64(count) > (end-base-12)/4 {
		return sizeTable{}, fmt.Errorf("faststart: stsz sample_count %d exceeds box: %w",
			count, ErrInconsistent)
	}
	sizes := make([]uint32, count)
	for i := uint32(0); i < count; i++ {
		p := base + 12 + int64(i)*4
		sizes[i] = binary.BigEndian.Uint32(payload[p : p+4])
	}
	return sizeTable{count: count, vals: sizes}, nil
}

// parseStz2 reads an stz2 box (4, 8 or 16 bit fields) and expands it to
// per-sample sizes.
func parseStz2(payload []byte, ib innerBox) (sizeTable, error) {
	base := ib.off + ib.headerLen
	end := ib.off + ib.size
	if end-base < 12 {
		return sizeTable{}, fmt.Errorf("faststart: stz2 too small: %w", ErrInconsistent)
	}
	fieldSize := payload[base+7]
	count := binary.BigEndian.Uint32(payload[base+8 : base+12])
	sizes := make([]uint32, count)
	switch fieldSize {
	case 4:
		need := (int64(count) + 1) / 2
		if need > end-base-12 {
			return sizeTable{}, fmt.Errorf("faststart: stz2 sample_count %d exceeds box: %w",
				count, ErrInconsistent)
		}
		for i := uint32(0); i < count; i++ {
			b := payload[base+12+int64(i)/2]
			if i%2 == 0 {
				sizes[i] = uint32(b >> 4)
			} else {
				sizes[i] = uint32(b & 0x0f)
			}
		}
	case 8:
		if int64(count) > end-base-12 {
			return sizeTable{}, fmt.Errorf("faststart: stz2 sample_count %d exceeds box: %w",
				count, ErrInconsistent)
		}
		for i := uint32(0); i < count; i++ {
			sizes[i] = uint32(payload[base+12+int64(i)])
		}
	case 16:
		if int64(count) > (end-base-12)/2 {
			return sizeTable{}, fmt.Errorf("faststart: stz2 sample_count %d exceeds box: %w",
				count, ErrInconsistent)
		}
		for i := uint32(0); i < count; i++ {
			p := base + 12 + int64(i)*2
			sizes[i] = uint32(binary.BigEndian.Uint16(payload[p : p+2]))
		}
	default:
		return sizeTable{}, fmt.Errorf("faststart: stz2 field_size %d: %w", fieldSize, ErrInconsistent)
	}
	return sizeTable{count: count, vals: sizes}, nil
}
