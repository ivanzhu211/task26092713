package faststart

import (
	"os"
	"path/filepath"
	"testing"
)

// smallTracks returns two tiny tracks whose chunks all live in one mdat
// payload starting at payloadOff.
func smallTracks(payloadOff uint64) []muxTrack {
	return []muxTrack{
		{chunks: []muxChunk{
			{off: payloadOff + 0, sizes: []uint32{4, 7}},
			{off: payloadOff + 64, sizes: []uint32{10}},
		}},
		{stz2Field: 8, chunks: []muxChunk{
			{off: payloadOff + 128, sizes: []uint32{6, 9}},
			{off: payloadOff + 192, sizes: []uint32{5}},
		}},
	}
}

// assemble writes the given boxes back to back plus all sample data.
func assemble(t *testing.T, path string, boxes [][]byte, tracks []muxTrack, fileSize int64) {
	t.Helper()
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	off := int64(0)
	for _, b := range boxes {
		writeAt(t, f, off, b)
		off += int64(len(b))
	}
	writeSamples(t, f, tracks)
	if fileSize < off {
		fileSize = off
	}
	if err := f.Truncate(fileSize); err != nil {
		t.Fatal(err)
	}
}

func ftypBox() []byte {
	p := append([]byte("isom"), be32(0)...)
	return box("ftyp", append(p, []byte("isomiso2")...))
}

func TestRelocateVerbatim(t *testing.T) {
	dir := t.TempDir()
	moovOff := int64(len(ftypBox()))
	tracks := smallTracks(0) // offsets patched below
	moovLen := int64(len(buildMoovBox(tracks)))
	payloadOff := uint64(moovOff + moovLen + 8)
	tracks = smallTracks(payloadOff)
	moov := buildMoovBox(tracks)
	mdat := box("mdat", make([]byte, 256))

	src := filepath.Join(dir, "front.mp4")
	assemble(t, src, [][]byte{ftypBox(), moov, mdat}, tracks, 0)
	dst := filepath.Join(dir, "front-out.mp4")
	if err := Relocate(src, dst); err != nil {
		t.Fatal(err)
	}
	if !filesEqual(t, src, dst) {
		t.Fatal("moov already front: output differs from input")
	}
}

func TestRelocateSize0Moov(t *testing.T) {
	dir := t.TempDir()
	// ftyp | mdat | moov(size==0, extends to EOF)
	ftyp := ftypBox()
	mdatPayloadOff := uint64(len(ftyp)) + 8
	tracks := smallTracks(mdatPayloadOff)
	moov := buildMoovBox(tracks)
	moov0 := append(be32(0), moov[4:]...) // size==0 header
	mdat := box("mdat", make([]byte, 256))

	src := filepath.Join(dir, "size0.mp4")
	assemble(t, src, [][]byte{ftyp, mdat, moov0}, tracks, 0)
	dst := filepath.Join(dir, "size0-out.mp4")
	if err := Relocate(src, dst); err != nil {
		t.Fatal(err)
	}
	info, err := Layout(dst)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"ftyp", "moov", "mdat"}
	if got := boxTypes(info); !equalStrings(got, want) {
		t.Fatalf("boxes = %v, want %v", got, want)
	}
	// The relocated moov must carry an explicit size.
	pf, err := parseFile(dst)
	if err != nil {
		t.Fatal(err)
	}
	defer pf.f.Close()
	mb := pf.boxes[pf.moovIdx]
	if mb.Size != int64(len(moov)) {
		t.Fatalf("moov size = %d, want explicit %d", mb.Size, len(moov))
	}
	if eq, err := SamplesEqual(src, dst); err != nil || !eq {
		t.Fatalf("SamplesEqual = %v, %v", eq, err)
	}
}

func TestRelocateKeepsCo64AndBoxes(t *testing.T) {
	dir := t.TempDir()
	// free | uuid | ftyp | mdat(A) | moov | skip | mdat(B, size==0)
	free := box("free", []byte{1, 2, 3, 4})
	uuidBox := box("uuid", append(make([]byte, 16), 9, 9, 9, 9))
	ftyp := ftypBox()
	skip := box("skip", []byte{5, 6})

	mdatAOff := int64(len(free) + len(uuidBox) + len(ftyp))
	mdatAPayload := uint64(mdatAOff) + 8

	tracks := []muxTrack{
		{co64: true, chunks: []muxChunk{
			{off: mdatAPayload + 0, sizes: []uint32{4, 7}},
			{off: 0, sizes: []uint32{10}}, // patched below, lives in mdat B
		}},
		{stz2Field: 16, chunks: []muxChunk{
			{off: mdatAPayload + 64, sizes: []uint32{6, 9}},
			{off: 16, sizes: []uint32{5}}, // patched below
		}},
	}
	moovLen := int64(len(buildMoovBox(tracks)))
	mdatBOff := mdatAOff + 8 + 256 + moovLen + int64(len(skip))
	mdatBPayload := uint64(mdatBOff) + 8
	tracks[0].chunks[1].off = mdatBPayload + 32
	tracks[1].chunks[1].off = mdatBPayload + 96
	moov := buildMoovBox(tracks)

	mdatA := box("mdat", make([]byte, 256))
	mdatB := append(be32(0), []byte("mdat")...) // size==0, extends to EOF

	src := filepath.Join(dir, "mix.mp4")
	assemble(t, src,
		[][]byte{free, uuidBox, ftyp, mdatA, moov, skip, mdatB},
		tracks, mdatBOff+8+256)

	dst := filepath.Join(dir, "mix-out.mp4")
	if err := Relocate(src, dst); err != nil {
		t.Fatal(err)
	}
	info, err := Layout(dst)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"ftyp", "moov", "free", "uuid", "mdat", "skip", "mdat"}
	if got := boxTypes(info); !equalStrings(got, want) {
		t.Fatalf("boxes = %v, want %v", got, want)
	}
	if info.Tracks[0].ChunkTableType != "co64" {
		t.Fatalf("co64 input downgraded to %s", info.Tracks[0].ChunkTableType)
	}
	if info.Tracks[1].ChunkTableType != "stco" {
		t.Fatalf("track1 table = %s, want stco", info.Tracks[1].ChunkTableType)
	}
	if eq, err := SamplesEqual(src, dst); err != nil || !eq {
		t.Fatalf("SamplesEqual = %v, %v", eq, err)
	}
	// Non-moov boxes must be byte-identical; check free and uuid bytes.
	outPF, err := parseFile(dst)
	if err != nil {
		t.Fatal(err)
	}
	defer outPF.f.Close()
	for i, b := range outPF.boxes {
		if b.TypeString() == "free" || b.TypeString() == "uuid" {
			buf := make([]byte, b.Size)
			if _, err := outPF.f.ReadAt(buf, b.Offset); err != nil {
				t.Fatal(err)
			}
			srcBox := [][]byte{free, uuidBox}[i-2]
			if !filesEqualBytes(buf, srcBox) {
				t.Fatalf("box %s changed", b.TypeString())
			}
		}
	}
	// Idempotence.
	dst2 := filepath.Join(dir, "mix-out2.mp4")
	if err := Relocate(dst, dst2); err != nil {
		t.Fatal(err)
	}
	if !filesEqual(t, dst, dst2) {
		t.Fatal("second Relocate changed the file")
	}
}

func filesEqualBytes(a, b []byte) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
