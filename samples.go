package faststart

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"io"
	"os"
)

// stscEntry is one run of a sample-to-chunk table.
type stscEntry struct {
	firstChunk      uint32 // 1-based
	samplesPerChunk uint32
	sampleDescIndex uint32
}

// trackTables holds the parsed sample tables of one trak. Sample sizes are
// either an explicit table (sizes) or a constant default size (defSize);
// sampleCount is always the declared number of samples.
type trackTables struct {
	chunkTable  *mp4Box // stco or co64
	stsc        []stscEntry
	sizes       []uint32 // nil when defSize applies
	defSize     uint32
	hasSizes    bool
	sampleCount uint64
}

// sizeAt returns the size of sample i.
func (t *trackTables) sizeAt(i uint64) uint32 {
	if t.sizes != nil {
		return t.sizes[i]
	}
	return t.defSize
}

// sample is one resolved sample: absolute file offset and length.
type sample struct {
	offset uint64
	size   uint32
}

// extractTracks pulls the sample tables of every trak out of a parsed moov.
func extractTracks(moov *mp4Box) ([]*trackTables, error) {
	var tracks []*trackTables
	for _, trak := range moov.children {
		if trak.typ != [4]byte{'t', 'r', 'a', 'k'} {
			continue
		}
		t, err := extractTrack(trak)
		if err != nil {
			return nil, err
		}
		tracks = append(tracks, t)
	}
	return tracks, nil
}

func extractTrack(trak *mp4Box) (*trackTables, error) {
	stbl := findPath(trak, "mdia", "minf", "stbl")
	if stbl == nil {
		return nil, fmt.Errorf("faststart: trak without mdia/minf/stbl")
	}
	t := &trackTables{}
	for _, b := range stbl.children {
		switch b.typ {
		case [4]byte{'s', 't', 'c', 'o'}, [4]byte{'c', 'o', '6', '4'}:
			if t.chunkTable != nil {
				return nil, fmt.Errorf("faststart: trak has both stco and co64")
			}
			t.chunkTable = b
		case [4]byte{'s', 't', 's', 'c'}:
			entries, err := parseSTSC(b.payload)
			if err != nil {
				return nil, err
			}
			t.stsc = entries
		case [4]byte{'s', 't', 's', 'z'}:
			def, count, table, err := parseSTSZ(b.payload)
			if err != nil {
				return nil, err
			}
			t.defSize = def
			t.sampleCount = uint64(count)
			t.sizes = table
			t.hasSizes = true
		case [4]byte{'s', 't', 'z', '2'}:
			table, err := parseSTZ2(b.payload)
			if err != nil {
				return nil, err
			}
			t.sizes = table
			t.sampleCount = uint64(len(table))
			t.hasSizes = true
		}
	}
	if t.chunkTable == nil {
		return nil, fmt.Errorf("faststart: trak without stco/co64")
	}
	if t.stsc == nil && len(t.chunkTable.entries) > 0 {
		return nil, fmt.Errorf("faststart: trak without stsc")
	}
	if !t.hasSizes {
		return nil, fmt.Errorf("faststart: trak without stsz/stz2")
	}
	return t, nil
}

// findPath descends container boxes by type, returning the first match.
func findPath(b *mp4Box, types ...string) *mp4Box {
	cur := b
	for _, name := range types {
		var t [4]byte
		copy(t[:], name)
		var next *mp4Box
		for _, c := range cur.children {
			if c.typ == t {
				next = c
				break
			}
		}
		if next == nil {
			return nil
		}
		cur = next
	}
	return cur
}

func parseSTSC(p []byte) ([]stscEntry, error) {
	if len(p) < 8 {
		return nil, fmt.Errorf("faststart: stsc too short")
	}
	count := binary.BigEndian.Uint32(p[4:8])
	if 8+uint64(count)*12 > uint64(len(p)) {
		return nil, fmt.Errorf("faststart: stsc declares %d entries, data too short", count)
	}
	entries := make([]stscEntry, count)
	for i := range entries {
		e := p[8+i*12:]
		entries[i] = stscEntry{
			firstChunk:      binary.BigEndian.Uint32(e[0:4]),
			samplesPerChunk: binary.BigEndian.Uint32(e[4:8]),
			sampleDescIndex: binary.BigEndian.Uint32(e[8:12]),
		}
	}
	return entries, nil
}

// parseSTSZ parses a sample-size box. A nonzero default size means every
// sample has that size and no table follows; in that case no table is
// allocated (the declared count may be huge while the box stays tiny).
func parseSTSZ(p []byte) (def, count uint32, table []uint32, err error) {
	if len(p) < 12 {
		return 0, 0, nil, fmt.Errorf("faststart: stsz too short")
	}
	def = binary.BigEndian.Uint32(p[4:8])
	count = binary.BigEndian.Uint32(p[8:12])
	if def != 0 {
		return def, count, nil, nil
	}
	if 12+uint64(count)*4 > uint64(len(p)) {
		return 0, 0, nil, fmt.Errorf("faststart: stsz declares %d entries, data too short", count)
	}
	table = make([]uint32, count)
	for i := range table {
		table[i] = binary.BigEndian.Uint32(p[12+i*4:])
	}
	return 0, count, table, nil
}

func parseSTZ2(p []byte) ([]uint32, error) {
	if len(p) < 12 {
		return nil, fmt.Errorf("faststart: stz2 too short")
	}
	fieldSize := p[7]
	count := binary.BigEndian.Uint32(p[8:12])
	var need uint64
	switch fieldSize {
	case 4:
		need = 12 + (uint64(count)+1)/2
	case 8:
		need = 12 + uint64(count)
	case 16:
		need = 12 + uint64(count)*2
	default:
		return nil, fmt.Errorf("faststart: stz2 unsupported field size %d", fieldSize)
	}
	if need > uint64(len(p)) {
		return nil, fmt.Errorf("faststart: stz2 declares %d entries, data too short", count)
	}
	sizes := make([]uint32, count)
	body := p[12:]
	for i := range sizes {
		switch fieldSize {
		case 4:
			b := body[i/2]
			if i%2 == 0 {
				sizes[i] = uint32(b >> 4)
			} else {
				sizes[i] = uint32(b & 0x0f)
			}
		case 8:
			sizes[i] = uint32(body[i])
		case 16:
			sizes[i] = uint32(binary.BigEndian.Uint16(body[i*2:]))
		}
	}
	return sizes, nil
}

// validate checks the stsc structure and that stsc/stco imply exactly
// sampleCount samples. It runs in O(chunks).
func (t *trackTables) validate() error {
	chunks := len(t.chunkTable.entries)
	if len(t.stsc) == 0 {
		if chunks != 0 {
			return fmt.Errorf("faststart: empty stsc with %d chunks", chunks)
		}
		if t.sampleCount != 0 {
			return fmt.Errorf("faststart: stsz declares %d samples but there are no chunks", t.sampleCount)
		}
		return nil
	}
	if t.stsc[0].firstChunk != 1 {
		return fmt.Errorf("faststart: stsc first run starts at chunk %d, want 1", t.stsc[0].firstChunk)
	}
	for i := 1; i < len(t.stsc); i++ {
		if t.stsc[i].firstChunk <= t.stsc[i-1].firstChunk {
			return fmt.Errorf("faststart: stsc first_chunk not strictly increasing at run %d", i)
		}
		if uint64(t.stsc[i].firstChunk) > uint64(chunks)+1 {
			return fmt.Errorf("faststart: stsc run %d starts beyond last chunk", i)
		}
	}
	var total uint64
	run := 0
	for c := 0; c < chunks; c++ {
		chunkNo := uint32(c) + 1
		for run+1 < len(t.stsc) && t.stsc[run+1].firstChunk <= chunkNo {
			run++
		}
		total += uint64(t.stsc[run].samplesPerChunk)
	}
	if total != t.sampleCount {
		return fmt.Errorf("faststart: stsz declares %d samples but stsc/stco cover %d", t.sampleCount, total)
	}
	return nil
}

// chunkLengths returns the total sample byte length of every chunk. The
// tables must already have been validated by validate.
func (t *trackTables) chunkLengths() []uint64 {
	lengths := make([]uint64, len(t.chunkTable.entries))
	if len(t.stsc) == 0 {
		return lengths
	}
	idx := uint64(0)
	run := 0
	for c := range lengths {
		chunkNo := uint32(c) + 1
		for run+1 < len(t.stsc) && t.stsc[run+1].firstChunk <= chunkNo {
			run++
		}
		spc := t.stsc[run].samplesPerChunk
		if t.sizes == nil {
			lengths[c] = uint64(spc) * uint64(t.defSize)
			idx += uint64(spc)
			continue
		}
		var l uint64
		for s := uint32(0); s < spc && idx < uint64(len(t.sizes)); s++ {
			l += uint64(t.sizes[idx])
			idx++
		}
		lengths[c] = l
	}
	return lengths
}

// sampleIter lazily walks the samples of a track in file order. The tables
// must have been validated first.
type sampleIter struct {
	t     *trackTables
	chunk int
	run   int
	idx   uint64 // next sample index
	left  uint32 // samples left in current chunk
	off   uint64 // offset of next sample within current chunk
}

func (t *trackTables) iter() *sampleIter {
	return &sampleIter{t: t}
}

func (it *sampleIter) next() (sample, bool) {
	for {
		if it.left > 0 {
			sz := it.t.sizeAt(it.idx)
			s := sample{offset: it.off, size: sz}
			it.off += uint64(sz)
			it.idx++
			it.left--
			return s, true
		}
		if it.chunk >= len(it.t.chunkTable.entries) {
			return sample{}, false
		}
		chunkNo := uint32(it.chunk) + 1
		for it.run+1 < len(it.t.stsc) && it.t.stsc[it.run+1].firstChunk <= chunkNo {
			it.run++
		}
		it.left = it.t.stsc[it.run].samplesPerChunk
		it.off = it.t.chunkTable.entries[it.chunk]
		it.chunk++
	}
}

// SamplesEqual reports whether a and b contain the same samples: for every
// track, the samples addressed by each file's own sample tables must be
// byte-identical.
func SamplesEqual(a, b string) (bool, error) {
	fa, ta, err := openAndParse(a)
	if err != nil {
		return false, err
	}
	defer fa.Close()
	fb, tb, err := openAndParse(b)
	if err != nil {
		return false, err
	}
	defer fb.Close()
	if len(ta) != len(tb) {
		return false, nil
	}
	for i := range ta {
		ia, ib := ta[i].iter(), tb[i].iter()
		for {
			sa, oka := ia.next()
			sb, okb := ib.next()
			if oka != okb {
				return false, nil
			}
			if !oka {
				break
			}
			if sa.size != sb.size {
				return false, nil
			}
			eq, err := rangesEqual(fa, int64(sa.offset), fb, int64(sb.offset), int64(sa.size))
			if err != nil {
				return false, err
			}
			if !eq {
				return false, nil
			}
		}
	}
	return true, nil
}

// openAndParse opens a file, scans its top-level boxes, parses the moov
// track tables and validates them. The caller must close the returned file.
func openAndParse(path string) (*os.File, []*trackTables, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, nil, err
	}
	fi, err := f.Stat()
	if err != nil {
		f.Close()
		return nil, nil, err
	}
	boxes, err := scanTopLevel(f, fi.Size())
	if err != nil {
		f.Close()
		return nil, nil, err
	}
	var moovBox *topBox
	for i := range boxes {
		if boxes[i].typ == [4]byte{'m', 'o', 'o', 'v'} {
			moovBox = &boxes[i]
			break
		}
	}
	if moovBox == nil {
		f.Close()
		return nil, nil, fmt.Errorf("faststart: %s: no moov box", path)
	}
	data := make([]byte, moovBox.size)
	if _, err := f.ReadAt(data, moovBox.offset); err != nil {
		f.Close()
		return nil, nil, fmt.Errorf("faststart: %s: cannot read moov: %w", path, err)
	}
	moov, err := parseBoxTree(data, 0)
	if err != nil {
		f.Close()
		return nil, nil, err
	}
	tracks, err := extractTracks(moov)
	if err != nil {
		f.Close()
		return nil, nil, err
	}
	for _, t := range tracks {
		if err := t.validate(); err != nil {
			f.Close()
			return nil, nil, err
		}
	}
	return f, tracks, nil
}

// rangesEqual compares n bytes at offset oa of ra with offset ob of rb.
func rangesEqual(ra io.ReaderAt, oa int64, rb io.ReaderAt, ob int64, n int64) (bool, error) {
	bufA := make([]byte, 64*1024)
	bufB := make([]byte, 64*1024)
	for n > 0 {
		chunk := int64(len(bufA))
		if chunk > n {
			chunk = n
		}
		if _, err := ra.ReadAt(bufA[:chunk], oa); err != nil {
			return false, err
		}
		if _, err := rb.ReadAt(bufB[:chunk], ob); err != nil {
			return false, err
		}
		if !bytes.Equal(bufA[:chunk], bufB[:chunk]) {
			return false, nil
		}
		oa += chunk
		ob += chunk
		n -= chunk
	}
	return true, nil
}
