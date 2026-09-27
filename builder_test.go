package faststart

import (
	"encoding/binary"
)

// box builds a 32-bit-size box.
func box(typ string, payload []byte) []byte {
	out := make([]byte, 8+len(payload))
	binary.BigEndian.PutUint32(out[0:4], uint32(len(out)))
	copy(out[4:8], typ)
	copy(out[8:], payload)
	return out
}

// largeBoxHeader builds a 16-byte largesize header for a box whose payload
// will be written separately.
func largeBoxHeader(typ string, totalSize int64) []byte {
	out := make([]byte, 16)
	binary.BigEndian.PutUint32(out[0:4], 1)
	copy(out[4:8], typ)
	binary.BigEndian.PutUint64(out[8:16], uint64(totalSize))
	return out
}

// uuidBox builds a uuid box with the given 16-byte usertype.
func uuidBox(id [16]byte, payload []byte) []byte {
	out := make([]byte, 24+len(payload))
	binary.BigEndian.PutUint32(out[0:4], uint32(len(out)))
	copy(out[4:8], "uuid")
	copy(out[8:24], id[:])
	copy(out[24:], payload)
	return out
}

func container(typ string, children ...[]byte) []byte {
	var body []byte
	for _, c := range children {
		body = append(body, c...)
	}
	return box(typ, body)
}

func stcoBox(entries []uint32) []byte {
	p := make([]byte, 8+4*len(entries))
	binary.BigEndian.PutUint32(p[4:8], uint32(len(entries)))
	for i, e := range entries {
		binary.BigEndian.PutUint32(p[8+i*4:], e)
	}
	return box("stco", p)
}

func co64Box(entries []uint64) []byte {
	p := make([]byte, 8+8*len(entries))
	binary.BigEndian.PutUint32(p[4:8], uint32(len(entries)))
	for i, e := range entries {
		binary.BigEndian.PutUint64(p[8+i*8:], e)
	}
	return box("co64", p)
}

// stscBox builds a sample-to-chunk box from (firstChunk, samplesPerChunk,
// sampleDescIndex) runs.
func stscBox(runs [][3]uint32) []byte {
	p := make([]byte, 8+12*len(runs))
	binary.BigEndian.PutUint32(p[4:8], uint32(len(runs)))
	for i, r := range runs {
		binary.BigEndian.PutUint32(p[8+i*12:], r[0])
		binary.BigEndian.PutUint32(p[8+i*12+4:], r[1])
		binary.BigEndian.PutUint32(p[8+i*12+8:], r[2])
	}
	return box("stsc", p)
}

// stszBox builds a sample-size box with an explicit table.
func stszBox(sizes []uint32) []byte {
	p := make([]byte, 12+4*len(sizes))
	binary.BigEndian.PutUint32(p[4:8], 0) // no default size
	binary.BigEndian.PutUint32(p[8:12], uint32(len(sizes)))
	for i, s := range sizes {
		binary.BigEndian.PutUint32(p[12+i*4:], s)
	}
	return box("stsz", p)
}

// stz2Box builds a compact sample-size box. fieldSize is 4, 8 or 16.
func stz2Box(sizes []uint32, fieldSize int) []byte {
	var body []byte
	switch fieldSize {
	case 4:
		body = make([]byte, (len(sizes)+1)/2)
		for i, s := range sizes {
			if i%2 == 0 {
				body[i/2] |= byte(s) << 4
			} else {
				body[i/2] |= byte(s)
			}
		}
	case 8:
		body = make([]byte, len(sizes))
		for i, s := range sizes {
			body[i] = byte(s)
		}
	case 16:
		body = make([]byte, 2*len(sizes))
		for i, s := range sizes {
			binary.BigEndian.PutUint16(body[i*2:], uint16(s))
		}
	}
	p := make([]byte, 12+len(body))
	p[7] = byte(fieldSize)
	binary.BigEndian.PutUint32(p[8:12], uint32(len(sizes)))
	copy(p[12:], body)
	return box("stz2", p)
}

// trakBox wraps stbl children into trak > mdia > minf > stbl.
func trakBox(stblChildren ...[]byte) []byte {
	stbl := container("stbl", stblChildren...)
	return container("trak", container("mdia", container("minf", stbl)))
}

func ftypBox() []byte {
	p := make([]byte, 16)
	copy(p[0:4], "isom")
	binary.BigEndian.PutUint32(p[4:8], 512)
	copy(p[8:12], "isom")
	copy(p[12:16], "iso2")
	return box("ftyp", p)
}

// sampleBytes returns deterministic pseudo-random bytes for sample data.
func sampleBytes(seed byte, n int) []byte {
	out := make([]byte, n)
	for i := range out {
		out[i] = seed + byte(i)*31 + byte(i>>3)
	}
	return out
}
