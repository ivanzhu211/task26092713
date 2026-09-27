package faststart

import (
	"encoding/binary"
	"fmt"
)

// mp4Box is the in-memory representation of a box inside moov.
type mp4Box struct {
	typ       [4]byte
	largesize bool // original header used the 64-bit largesize form
	uuid      *[16]byte
	children  []*mp4Box // non-nil for container boxes
	payload   []byte    // raw payload for leaf boxes

	// Chunk offset table (stco/co64) data. entries holds the 32- or 64-bit
	// chunk offsets; co64 selects the on-disk entry width.
	isChunkTable bool
	co64         bool
	entries      []uint64

	outSize   int64 // computed by computeSizes
	outHeader int64 // computed by computeSizes
}

// containerTypes are the box types inside moov that we descend into. Every
// other box is kept as an opaque leaf so its bytes survive untouched.
var containerTypes = map[[4]byte]bool{
	{'m', 'o', 'o', 'v'}: true,
	{'t', 'r', 'a', 'k'}: true,
	{'m', 'd', 'i', 'a'}: true,
	{'m', 'i', 'n', 'f'}: true,
	{'s', 't', 'b', 'l'}: true,
}

const maxBoxDepth = 64

// parseBoxTree parses one box (header included) from data, which must hold
// exactly the box bytes.
func parseBoxTree(data []byte, depth int) (*mp4Box, error) {
	if depth > maxBoxDepth {
		return nil, fmt.Errorf("faststart: moov nesting deeper than %d levels", maxBoxDepth)
	}
	h, err := parseHeaderBytes(data)
	if err != nil {
		return nil, err
	}
	if h.size != int64(len(data)) {
		return nil, fmt.Errorf("faststart: box %q size %d does not match containing data %d", h.typ, h.size, len(data))
	}
	b := &mp4Box{typ: h.typ, largesize: h.header == 16 || h.header == 32}
	if h.uuid {
		var id [16]byte
		copy(id[:], data[h.header-16:h.header])
		b.uuid = &id
	}
	body := data[h.header:]
	switch {
	case h.typ == [4]byte{'s', 't', 'c', 'o'} || h.typ == [4]byte{'c', 'o', '6', '4'}:
		if err := b.parseChunkTable(body); err != nil {
			return nil, err
		}
	case containerTypes[h.typ]:
		off := int64(0)
		for off < int64(len(body)) {
			ch, err := parseHeaderBytes(body[off:])
			if err != nil {
				return nil, fmt.Errorf("faststart: bad child of %q: %w", h.typ, err)
			}
			if ch.size <= 0 || off+ch.size > int64(len(body)) {
				return nil, fmt.Errorf("faststart: child %q of %q at +%d has invalid size %d", ch.typ, h.typ, off, ch.size)
			}
			child, err := parseBoxTree(body[off:off+ch.size], depth+1)
			if err != nil {
				return nil, err
			}
			b.children = append(b.children, child)
			off += ch.size
		}
	default:
		b.payload = body
	}
	return b, nil
}

// parseHeaderBytes parses a box header from the front of buf.
func parseHeaderBytes(buf []byte) (boxHeader, error) {
	var h boxHeader
	if len(buf) < 8 {
		return h, fmt.Errorf("faststart: truncated box header (%d bytes)", len(buf))
	}
	size32 := binary.BigEndian.Uint32(buf[0:4])
	copy(h.typ[:], buf[4:8])
	h.header = 8
	if size32 == 1 {
		if len(buf) < 16 {
			return h, fmt.Errorf("faststart: truncated largesize header")
		}
		large := binary.BigEndian.Uint64(buf[8:16])
		if large > uint64(len(buf)) {
			return h, fmt.Errorf("faststart: box %q declares size %d beyond available data %d", h.typ, large, len(buf))
		}
		h.size = int64(large)
		h.header = 16
	} else {
		h.size = int64(size32)
	}
	if h.typ == [4]byte{'u', 'u', 'i', 'd'} {
		if len(buf) < int(h.header)+16 {
			return h, fmt.Errorf("faststart: truncated uuid header")
		}
		h.uuid = true
		h.header += 16
	}
	if h.size == 0 {
		// Only legal for the last top-level box; inside moov we treat it as
		// "rest of the buffer" so the root moov with size==0 parses.
		h.size = int64(len(buf))
	}
	if h.size < h.header {
		return h, fmt.Errorf("faststart: box %q has size %d smaller than header %d", h.typ, h.size, h.header)
	}
	return h, nil
}

// parseChunkTable parses the payload of an stco or co64 box.
func (b *mp4Box) parseChunkTable(body []byte) error {
	if len(body) < 8 {
		return fmt.Errorf("faststart: %s box too short (%d bytes)", b.typ, len(body))
	}
	count := binary.BigEndian.Uint32(body[4:8])
	width := 4
	b.co64 = b.typ == [4]byte{'c', 'o', '6', '4'}
	if b.co64 {
		width = 8
	}
	need := 8 + uint64(count)*uint64(width)
	if need > uint64(len(body)) {
		return fmt.Errorf("faststart: %s declares %d entries but only %d bytes available", b.typ, count, len(body)-8)
	}
	b.isChunkTable = true
	b.entries = make([]uint64, count)
	p := body[8:]
	for i := range b.entries {
		if width == 4 {
			b.entries[i] = uint64(binary.BigEndian.Uint32(p[i*4:]))
		} else {
			b.entries[i] = binary.BigEndian.Uint64(p[i*8:])
		}
	}
	return nil
}

// computeSizes assigns outSize/outHeader bottom-up and returns this box's
// total size. The original header form (32-bit vs largesize) is preserved
// unless the size no longer fits in 32 bits.
func (b *mp4Box) computeSizes() int64 {
	var body int64
	switch {
	case b.isChunkTable:
		body = 8
		if b.co64 {
			body += int64(len(b.entries)) * 8
		} else {
			body += int64(len(b.entries)) * 4
		}
	case b.children != nil:
		for _, c := range b.children {
			body += c.computeSizes()
		}
	default:
		body = int64(len(b.payload))
	}
	header := int64(8)
	if b.largesize {
		header = 16
	}
	if b.uuid != nil {
		header += 16
	}
	size := header + body
	if !b.largesize && size >= 1<<32 {
		// Does not fit in 32 bits: widen the header.
		header += 8
		size += 8
	}
	b.outHeader = header
	b.outSize = size
	return size
}

// marshal serializes the box into out, which must have room for outSize
// bytes. computeSizes must have been called first.
func (b *mp4Box) marshal(out []byte) {
	header := b.outHeader
	if b.uuid != nil {
		header -= 16
	}
	if header == 16 {
		binary.BigEndian.PutUint32(out[0:4], 1)
		copy(out[4:8], b.typ[:])
		binary.BigEndian.PutUint64(out[8:16], uint64(b.outSize))
	} else {
		binary.BigEndian.PutUint32(out[0:4], uint32(b.outSize))
		copy(out[4:8], b.typ[:])
	}
	pos := header
	if b.uuid != nil {
		copy(out[pos:pos+16], b.uuid[:])
		pos += 16
	}
	switch {
	case b.isChunkTable:
		binary.BigEndian.PutUint32(out[pos:pos+4], 0) // version/flags
		binary.BigEndian.PutUint32(out[pos+4:pos+8], uint32(len(b.entries)))
		p := out[pos+8:]
		if b.co64 {
			for i, e := range b.entries {
				binary.BigEndian.PutUint64(p[i*8:], e)
			}
		} else {
			for i, e := range b.entries {
				binary.BigEndian.PutUint32(p[i*4:], uint32(e))
			}
		}
	case b.children != nil:
		for _, c := range b.children {
			c.marshal(out[pos : pos+c.outSize])
			pos += c.outSize
		}
	default:
		copy(out[pos:], b.payload)
	}
}

// walkBoxes calls fn for every box in the tree, depth-first.
func (b *mp4Box) walkBoxes(fn func(*mp4Box)) {
	fn(b)
	for _, c := range b.children {
		c.walkBoxes(fn)
	}
}

// hasBox reports whether the tree contains a box of the given type.
func (b *mp4Box) hasBox(typ string) bool {
	var t [4]byte
	copy(t[:], typ)
	found := false
	b.walkBoxes(func(x *mp4Box) {
		if x.typ == t {
			found = true
		}
	})
	return found
}
