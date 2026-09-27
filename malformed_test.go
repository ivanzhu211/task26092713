package faststart

import (
	"encoding/binary"
	"os"
	"path/filepath"
	"testing"
)

// expectErr runs Relocate and demands an error and no leftover dst file.
func expectErr(t *testing.T, name string, parts ...[]byte) {
	t.Helper()
	dir := t.TempDir()
	src := writeFile(t, dir, name, parts...)
	dst := filepath.Join(dir, name+".out")
	if err := Relocate(src, dst); err == nil {
		t.Fatalf("%s: Relocate succeeded, want error", name)
	}
	if _, err := os.Stat(dst); !os.IsNotExist(err) {
		t.Fatalf("%s: dst file left behind after error", name)
	}
	entries, _ := os.ReadDir(dir)
	for _, e := range entries {
		if e.Name() != name {
			t.Fatalf("%s: stray file %q left behind after error", name, e.Name())
		}
	}
}

// simpleMoov builds a one-track moov whose single chunk starts at chunkOff.
func simpleMoov(chunkOff uint32, sizes []uint32) []byte {
	return container("moov",
		trakBox(stcoBox([]uint32{chunkOff}), stscBox([][3]uint32{{1, uint32(len(sizes)), 1}}), stszBox(sizes)),
	)
}

func TestMalformed(t *testing.T) {
	ftyp := ftypBox()
	mdat := box("mdat", sampleBytes(1, 64))
	payloadStart := uint32(len(ftyp) + 8)

	t.Run("Moof", func(t *testing.T) {
		moof := box("moof", []byte{1, 2, 3, 4})
		expectErr(t, "moof.mp4", ftyp, moof, mdat, simpleMoov(payloadStart, []uint32{4}))
	})

	t.Run("Mvex", func(t *testing.T) {
		moov := container("moov",
			trakBox(stcoBox([]uint32{payloadStart}), stscBox([][3]uint32{{1, 1, 1}}), stszBox([]uint32{4})),
			box("mvex", []byte{0, 0, 0, 0}),
		)
		expectErr(t, "mvex.mp4", ftyp, mdat, moov)
	})

	t.Run("ChunkOutsideMdat", func(t *testing.T) {
		// Chunk offset points past the end of the only mdat.
		expectErr(t, "badchunk.mp4", ftyp, mdat, simpleMoov(payloadStart+0x1000, []uint32{4}))
	})

	t.Run("ChunkBeforeMdat", func(t *testing.T) {
		// Chunk offset points into the ftyp box.
		expectErr(t, "badchunk2.mp4", ftyp, mdat, simpleMoov(4, []uint32{4}))
	})

	t.Run("StscStszMismatch", func(t *testing.T) {
		// stsc covers 1 sample, stsz declares 2.
		moov := container("moov",
			trakBox(stcoBox([]uint32{payloadStart}), stscBox([][3]uint32{{1, 1, 1}}), stszBox([]uint32{4, 4})),
		)
		expectErr(t, "mismatch.mp4", ftyp, mdat, moov)
	})

	t.Run("StscBadFirstChunk", func(t *testing.T) {
		moov := container("moov",
			trakBox(stcoBox([]uint32{payloadStart}), stscBox([][3]uint32{{2, 1, 1}}), stszBox([]uint32{4})),
		)
		expectErr(t, "badstsc.mp4", ftyp, mdat, moov)
	})

	t.Run("Stz2BadFieldSize", func(t *testing.T) {
		stz2 := stz2Box([]uint32{1, 1}, 8)
		stz2[8+7] = 3 // invalid field size
		moov := container("moov",
			trakBox(stcoBox([]uint32{payloadStart}), stscBox([][3]uint32{{1, 2, 1}}), stz2),
		)
		expectErr(t, "badstz2.mp4", ftyp, mdat, moov)
	})

	t.Run("TruncatedBox", func(t *testing.T) {
		// mdat declares a size far beyond EOF.
		bad := box("mdat", sampleBytes(1, 64))
		binary.BigEndian.PutUint32(bad[0:4], uint32(len(bad)*100))
		expectErr(t, "trunc.mp4", ftyp, bad, simpleMoov(payloadStart, []uint32{4}))
	})

	t.Run("TruncatedMidHeader", func(t *testing.T) {
		// File ends in the middle of a box header.
		expectErr(t, "trunc2.mp4", ftyp, mdat, []byte{0, 0, 0})
	})

	t.Run("SizeSmallerThanHeader", func(t *testing.T) {
		bad := make([]byte, 8)
		binary.BigEndian.PutUint32(bad[0:4], 4) // size < header
		copy(bad[4:8], "free")
		expectErr(t, "badsize.mp4", ftyp, bad, mdat, simpleMoov(payloadStart, []uint32{4}))
	})

	t.Run("LargesizeBeyondFile", func(t *testing.T) {
		bad := largeBoxHeader("mdat", 1<<40)
		expectErr(t, "badlarge.mp4", ftyp, bad, simpleMoov(payloadStart, []uint32{4}))
	})

	t.Run("NoMoov", func(t *testing.T) {
		expectErr(t, "nomoov.mp4", ftyp, mdat)
	})

	t.Run("NestedSizeOverflow", func(t *testing.T) {
		// A child box inside moov declares a size exceeding its parent.
		trak := container("trak")
		binary.BigEndian.PutUint32(trak[0:4], uint32(len(trak)+8)) // shrink trak
		moov := container("moov", trak)
		expectErr(t, "nested.mp4", ftyp, mdat, moov)
	})
}

func TestSamplesEqualNegative(t *testing.T) {
	dir := t.TempDir()
	ftyp := ftypBox()
	specs := []trackSpec{
		{stscRuns: [][3]uint32{{1, 2, 1}}, sizes: []uint32{4, 4}, chunks: 1},
	}
	payloadStart := int64(len(ftyp) + 8)
	moov, mdatPayload := buildMoovAndMdat(payloadStart, specs)
	mdat := box("mdat", mdatPayload)
	a := writeFile(t, dir, "a.mp4", ftyp, mdat, moov)

	// Same tables, different sample bytes.
	mdatPayload2 := append([]byte(nil), mdatPayload...)
	mdatPayload2[0] ^= 0xff
	b := writeFile(t, dir, "b.mp4", ftyp, box("mdat", mdatPayload2), moov)

	eq, err := SamplesEqual(a, b)
	if err != nil {
		t.Fatal(err)
	}
	if eq {
		t.Fatal("SamplesEqual = true for differing sample bytes")
	}

	// Differing bytes in the last chunk of a multi-chunk track.
	specs3 := []trackSpec{
		{stscRuns: [][3]uint32{{1, 1, 1}}, sizes: []uint32{4, 4}, chunks: 2},
	}
	moov3, mdatPayload4 := buildMoovAndMdat(payloadStart, specs3)
	d := writeFile(t, dir, "d.mp4", ftyp, box("mdat", mdatPayload4), moov3)
	mdatPayload4[len(mdatPayload4)-1] ^= 0xff
	e := writeFile(t, dir, "e.mp4", ftyp, box("mdat", mdatPayload4), moov3)
	eq, err = SamplesEqual(d, e)
	if err != nil {
		t.Fatal(err)
	}
	if eq {
		t.Fatal("SamplesEqual = true for differing last-chunk bytes")
	}

	// Different sample counts.
	specs2 := []trackSpec{
		{stscRuns: [][3]uint32{{1, 3, 1}}, sizes: []uint32{4, 4, 4}, chunks: 1},
	}
	moov2, mdatPayload3 := buildMoovAndMdat(payloadStart, specs2)
	c := writeFile(t, dir, "c.mp4", ftyp, box("mdat", mdatPayload3), moov2)
	eq, err = SamplesEqual(a, c)
	if err != nil {
		t.Fatal(err)
	}
	if eq {
		t.Fatal("SamplesEqual = true for differing sample counts")
	}
}
