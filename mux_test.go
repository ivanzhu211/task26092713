package faststart

import (
	"encoding/binary"
	"os"
	"syscall"
	"testing"
)

// muxChunk is one chunk of a synthetic track.
type muxChunk struct {
	off   uint64   // absolute file offset in the input layout
	sizes []uint32 // per-sample sizes
}

// muxTrack describes a synthetic trak.
type muxTrack struct {
	co64      bool // write a co64 table instead of stco
	stz2Field int  // 0 => stsz, otherwise stz2 with this field size
	stszConst bool // use stsz default-size form (all sizes equal)
	chunks    []muxChunk
}

func be32(v uint32) []byte {
	var b [4]byte
	binary.BigEndian.PutUint32(b[:], v)
	return b[:]
}

func box(typ string, payload []byte) []byte {
	out := append(be32(uint32(len(payload))+8), typ...)
	return append(out, payload...)
}

func boxLarge(typ string, payload []byte) []byte {
	out := append(be32(1), typ...)
	var b [8]byte
	binary.BigEndian.PutUint64(b[:], uint64(len(payload))+16)
	out = append(out, b[:]...)
	return append(out, payload...)
}

// sampleByte generates deterministic non-zero sample content.
func sampleByte(track, chunk, sample, i int) byte {
	return byte((track*37+chunk*11+sample*7+i)%251 + 1)
}

func chunkBytes(track, chunkIdx int, c muxChunk) []byte {
	var out []byte
	for s, sz := range c.sizes {
		for i := 0; i < int(sz); i++ {
			out = append(out, sampleByte(track, chunkIdx, s, i))
		}
	}
	return out
}

func stscPayload(tk muxTrack) []byte {
	type run struct{ first, spc uint32 }
	var runs []run
	for i, c := range tk.chunks {
		spc := uint32(len(c.sizes))
		if len(runs) > 0 && runs[len(runs)-1].spc == spc {
			continue
		}
		runs = append(runs, run{uint32(i) + 1, spc})
	}
	p := append([]byte{0, 0, 0, 0}, be32(uint32(len(runs)))...)
	for _, r := range runs {
		p = append(p, be32(r.first)...)
		p = append(p, be32(r.spc)...)
		p = append(p, be32(1)...)
	}
	return p
}

func stszLikePayload(tk muxTrack) []byte {
	var sizes []uint32
	for _, c := range tk.chunks {
		sizes = append(sizes, c.sizes...)
	}
	if tk.stz2Field != 0 {
		p := []byte{0, 0, 0, 0, 0, 0, 0, byte(tk.stz2Field)}
		p = append(p, be32(uint32(len(sizes)))...)
		switch tk.stz2Field {
		case 4:
			for i := 0; i < len(sizes); i += 2 {
				hi := byte(sizes[i])
				var lo byte
				if i+1 < len(sizes) {
					lo = byte(sizes[i+1])
				}
				p = append(p, hi<<4|lo)
			}
		case 8:
			for _, s := range sizes {
				p = append(p, byte(s))
			}
		case 16:
			for _, s := range sizes {
				var b [2]byte
				binary.BigEndian.PutUint16(b[:], uint16(s))
				p = append(p, b[:]...)
			}
		}
		return p
	}
	if tk.stszConst {
		return append(append([]byte{0, 0, 0, 0}, be32(sizes[0])...), be32(uint32(len(sizes)))...)
	}
	p := append(append([]byte{0, 0, 0, 0}, be32(0)...), be32(uint32(len(sizes)))...)
	for _, s := range sizes {
		p = append(p, be32(s)...)
	}
	return p
}

func stcoPayload(tk muxTrack) []byte {
	p := append([]byte{0, 0, 0, 0}, be32(uint32(len(tk.chunks)))...)
	for _, c := range tk.chunks {
		p = append(p, be32(uint32(c.off))...)
	}
	return p
}

func co64Payload(tk muxTrack) []byte {
	p := append([]byte{0, 0, 0, 0}, be32(uint32(len(tk.chunks)))...)
	for _, c := range tk.chunks {
		var b [8]byte
		binary.BigEndian.PutUint64(b[:], c.off)
		p = append(p, b[:]...)
	}
	return p
}

// buildMoovBox assembles a moov box from synthetic tracks.
func buildMoovBox(tracks []muxTrack) []byte {
	var moovPayload []byte
	for _, tk := range tracks {
		var stbl []byte
		if tk.co64 {
			stbl = append(stbl, box("co64", co64Payload(tk))...)
		} else {
			stbl = append(stbl, box("stco", stcoPayload(tk))...)
		}
		stbl = append(stbl, box("stsc", stscPayload(tk))...)
		if tk.stz2Field != 0 {
			stbl = append(stbl, box("stz2", stszLikePayload(tk))...)
		} else {
			stbl = append(stbl, box("stsz", stszLikePayload(tk))...)
		}
		trak := box("trak", box("mdia", box("minf", box("stbl", stbl))))
		moovPayload = append(moovPayload, trak...)
	}
	return box("moov", moovPayload)
}

// writeAt writes p at absolute offset off.
func writeAt(t *testing.T, f *os.File, off int64, p []byte) {
	t.Helper()
	if _, err := f.WriteAt(p, off); err != nil {
		t.Fatal(err)
	}
}

// writeSamples writes the deterministic sample bytes of every chunk.
func writeSamples(t *testing.T, f *os.File, tracks []muxTrack) {
	t.Helper()
	for ti, tk := range tracks {
		for ci, c := range tk.chunks {
			writeAt(t, f, int64(c.off), chunkBytes(ti, ci, c))
		}
	}
}

// diskUsage returns the number of bytes physically allocated for path.
func diskUsage(t *testing.T, path string) int64 {
	t.Helper()
	fi, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if s, ok := fi.Sys().(*syscall.Stat_t); ok {
		return s.Blocks * 512
	}
	return fi.Size()
}
