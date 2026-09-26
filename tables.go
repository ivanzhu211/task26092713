package faststart

// sizeFieldRef points at the size field of an ancestor container box
// inside the moov payload; it is patched when a nested stco grows into a
// co64.
type sizeFieldRef struct {
	off   int64 // payload-relative offset of the size field
	large bool  // true for a 64-bit largesize field
}

// chunkTable locates one stco/co64 box inside the moov payload and caches
// its entries. Offsets are relative to the start of the moov payload.
type chunkTable struct {
	co64      bool   // true if the table is co64
	boxOff    int64  // payload-relative offset of the stco/co64 box
	headerLen int64  // box header length (8 or 16)
	size      int64  // total box size including header
	entryBase int64  // payload-relative offset of the first entry
	count     uint32 // entry_count
	entries   []uint64
	// ancestors are the size fields of enclosing stbl/minf/mdia/trak
	// boxes, innermost first.
	ancestors []sizeFieldRef
}
