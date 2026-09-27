package faststart

import (
	"bytes"
	"encoding/binary"
	"io"
	"os"
	"path/filepath"
	"testing"
)

// stszDefBox builds an stsz box with a default sample size (no table).
func stszDefBox(size uint32, count int) []byte {
	p := make([]byte, 12)
	binary.BigEndian.PutUint32(p[4:8], size)
	binary.BigEndian.PutUint32(p[8:12], uint32(count))
	return box("stsz", p)
}

// chunkOffsets returns the absolute chunk offsets of a track whose chunks
// have the given lengths, starting at base.
func chunkOffsets(base uint64, lengths []uint64) []uint32 {
	var out []uint32
	pos := base
	for _, l := range lengths {
		out = append(out, uint32(pos))
		pos += l
	}
	return out
}

// TestRelocateCross4G builds:
//
//	ftyp | mdat(A, largesize, ~4GiB sparse) | moov | mdat(B, largesize)
//
// with three tracks, all stco, multi-run stsc. After relocation track 1's
// offsets cross 2^32 (upgrade to co64), the growth of moov pushes track 2's
// offsets across as well, and track 3 stays stco.
func TestRelocateCross4G(t *testing.T) {
	dir := t.TempDir()
	ftyp := ftypBox()

	// Sample-per-chunk layout, all sample sizes 1.
	runs1 := [][3]uint32{{1, 2, 1}, {3, 1, 1}}
	lengths1 := []uint64{2, 2, 1, 1, 1, 1, 1, 1} // 8 chunks, 10 samples
	lengths2 := []uint64{2, 2, 1, 1}             // 4 chunks, 6 samples
	lengths3 := []uint64{2, 2, 1, 1}             // 4 chunks, 6 samples

	// First pass: build moov with dummy entries to learn its size M0.
	moov0 := container("moov",
		trakBox(stcoBox(make([]uint32, 8)), stscBox(runs1), stszDefBox(1, 10)),
		trakBox(stcoBox(make([]uint32, 4)), stscBox(runs1), stz2Box([]uint32{1, 1, 1, 1, 1, 1}, 8)),
		trakBox(stcoBox(make([]uint32, 4)), stscBox(runs1), stz2Box([]uint32{1, 1, 1, 1, 1, 1}, 16)),
	)
	M0 := int64(len(moov0))

	const fourG = uint64(1) << 32
	// Entry bases chosen so that:
	//  - track1 max entry + M0        >= 2^32  (upgraded in round 1, +32B)
	//  - track2 max entry + M0 + 32   >= 2^32  (upgraded in round 2, +16B)
	//  - track3 max entry + M0 + 48   <  2^32  (stays stco)
	base1 := fourG - uint64(M0) - 9  // max entry = 2^32 - M0
	base2 := fourG - uint64(M0) - 21 // max entry = 2^32 - M0 - 16
	base3 := fourG - uint64(M0) - 69 // max entry = 2^32 - M0 - 64
	entries1 := chunkOffsets(base1, lengths1)
	entries2 := chunkOffsets(base2, lengths2)
	entries3 := chunkOffsets(base3, lengths3)

	moov := container("moov",
		trakBox(stcoBox(entries1), stscBox(runs1), stszDefBox(1, 10)),
		trakBox(stcoBox(entries2), stscBox(runs1), stz2Box([]uint32{1, 1, 1, 1, 1, 1}, 8)),
		trakBox(stcoBox(entries3), stscBox(runs1), stz2Box([]uint32{1, 1, 1, 1, 1, 1}, 16)),
	)
	if int64(len(moov)) != M0 {
		t.Fatalf("moov size changed between passes: %d != %d", len(moov), M0)
	}

	// mdatA: largesize, sparse, big enough to hold all chunk data.
	aSize := int64(fourG) + M0 + 1024
	// mdatB: largesize, tiny payload.
	bPayload := sampleBytes(0x77, 16)
	bSize := int64(16 + len(bPayload))

	src := filepath.Join(dir, "big.mp4")
	f, err := os.Create(src)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.Write(ftyp); err != nil {
		t.Fatal(err)
	}
	if _, err := f.Write(largeBoxHeader("mdat", aSize)); err != nil {
		t.Fatal(err)
	}
	// Write the sample bytes of every chunk at their absolute offsets.
	writeSamples := func(entries []uint32, lengths []uint64, seed byte) {
		idx := 0
		for ci, l := range lengths {
			off := int64(entries[ci])
			for s := uint64(0); s < l; s++ {
				if _, err := f.WriteAt(sampleBytes(seed+byte(idx), 1), off); err != nil {
					t.Fatal(err)
				}
				off++
				idx++
			}
		}
	}
	writeSamples(entries1, lengths1, 0x10)
	writeSamples(entries2, lengths2, 0x40)
	writeSamples(entries3, lengths3, 0x70)
	moovOff := int64(len(ftyp)) + aSize
	if _, err := f.WriteAt(moov, moovOff); err != nil {
		t.Fatal(err)
	}
	if _, err := f.WriteAt(largeBoxHeader("mdat", bSize), moovOff+int64(len(moov))); err != nil {
		t.Fatal(err)
	}
	if _, err := f.WriteAt(bPayload, moovOff+int64(len(moov))+16); err != nil {
		t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}

	// Input layout sanity check.
	li, err := Layout(src)
	if err != nil {
		t.Fatal(err)
	}
	var types []string
	for _, b := range li.Boxes {
		types = append(types, b.Type)
	}
	if !equalStrings(types, []string{"ftyp", "mdat", "moov", "mdat"}) {
		t.Fatalf("input layout = %v", types)
	}
	if li.Boxes[1].HeaderSize != 16 || li.Boxes[3].HeaderSize != 16 {
		t.Fatalf("mdat headers = %d, %d; want largesize 16", li.Boxes[1].HeaderSize, li.Boxes[3].HeaderSize)
	}
	for i, tr := range li.Tracks {
		if tr.ChunkTableType != "stco" {
			t.Fatalf("input track %d table = %s, want stco", i, tr.ChunkTableType)
		}
	}
	if li.Tracks[0].ChunkCount != 8 || li.Tracks[0].SampleCount != 10 {
		t.Fatalf("track0 = %+v", li.Tracks[0])
	}

	// Relocate.
	dst := filepath.Join(dir, "big.out.mp4")
	if err := Relocate(src, dst); err != nil {
		t.Fatal(err)
	}

	lo, err := Layout(dst)
	if err != nil {
		t.Fatal(err)
	}
	types = types[:0]
	for _, b := range lo.Boxes {
		types = append(types, b.Type)
	}
	if !equalStrings(types, []string{"ftyp", "moov", "mdat", "mdat"}) {
		t.Fatalf("output layout = %v", types)
	}
	// Exactly the first two tables are upgraded.
	if lo.Tracks[0].ChunkTableType != "co64" || lo.Tracks[1].ChunkTableType != "co64" || lo.Tracks[2].ChunkTableType != "stco" {
		t.Fatalf("output tables = %s, %s, %s; want co64, co64, stco",
			lo.Tracks[0].ChunkTableType, lo.Tracks[1].ChunkTableType, lo.Tracks[2].ChunkTableType)
	}
	// moov grew by exactly 4*(8+4) bytes.
	if lo.Boxes[1].Size != M0+48 {
		t.Fatalf("output moov size = %d, want %d", lo.Boxes[1].Size, M0+48)
	}

	// Every chunk still points at the same bytes.
	eq, err := SamplesEqual(src, dst)
	if err != nil {
		t.Fatal(err)
	}
	if !eq {
		t.Fatal("SamplesEqual(src, dst) = false")
	}

	// Idempotent.
	dst2 := filepath.Join(dir, "big.out2.mp4")
	if err := Relocate(dst, dst2); err != nil {
		t.Fatal(err)
	}
	same, err := filesIdentical(dst, dst2)
	if err != nil {
		t.Fatal(err)
	}
	if !same {
		t.Fatal("second Relocate changed the file")
	}
}

// filesIdentical streams both files and reports byte equality.
func filesIdentical(a, b string) (bool, error) {
	fa, err := os.Open(a)
	if err != nil {
		return false, err
	}
	defer fa.Close()
	fb, err := os.Open(b)
	if err != nil {
		return false, err
	}
	defer fb.Close()
	ia, err := fa.Stat()
	if err != nil {
		return false, err
	}
	ib, err := fb.Stat()
	if err != nil {
		return false, err
	}
	if ia.Size() != ib.Size() {
		return false, nil
	}
	ba := make([]byte, 4<<20)
	bb := make([]byte, 4<<20)
	for off := int64(0); off < ia.Size(); {
		n := int64(len(ba))
		if rem := ia.Size() - off; rem < n {
			n = rem
		}
		if _, err := fa.ReadAt(ba[:n], off); err != nil && err != io.EOF {
			return false, err
		}
		if _, err := fb.ReadAt(bb[:n], off); err != nil && err != io.EOF {
			return false, err
		}
		if !bytes.Equal(ba[:n], bb[:n]) {
			return false, nil
		}
		off += n
	}
	return true, nil
}
