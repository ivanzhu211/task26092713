package faststart

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"
)

// trackSpec describes one track's sample tables for the test builder.
type trackSpec struct {
	stscRuns [][3]uint32
	sizes    []uint32 // per-sample sizes
	chunks   int
	sizeFmt  int // 0 = stsz, otherwise stz2 field size (4/8/16)
	co64     bool
}

// chunkLayout computes per-chunk sample counts and lengths from stsc runs.
func (ts trackSpec) chunkLayout() (spc []uint32, lengths []int64) {
	spc = make([]uint32, ts.chunks)
	run := 0
	for c := 0; c < ts.chunks; c++ {
		chunkNo := uint32(c) + 1
		for run+1 < len(ts.stscRuns) && ts.stscRuns[run+1][0] <= chunkNo {
			run++
		}
		spc[c] = ts.stscRuns[run][1]
	}
	idx := 0
	for c := 0; c < ts.chunks; c++ {
		var l int64
		for s := uint32(0); s < spc[c]; s++ {
			l += int64(ts.sizes[idx])
			idx++
		}
		lengths = append(lengths, l)
	}
	if idx != len(ts.sizes) {
		panic("trackSpec: sizes/stsc mismatch")
	}
	return spc, lengths
}

// buildMoovAndMdat lays out every track's chunks sequentially in one mdat
// whose payload starts at payloadStart, and returns the moov box and the
// mdat payload. Sample bytes are deterministic per track/chunk/sample.
func buildMoovAndMdat(payloadStart int64, specs []trackSpec) (moovBytes, mdatPayload []byte) {
	var traks [][]byte
	pos := payloadStart
	for ti, ts := range specs {
		_, lengths := ts.chunkLayout()
		var entries32 []uint32
		var entries64 []uint64
		spc, _ := ts.chunkLayout()
		idx := 0
		for ci, l := range lengths {
			if ts.co64 {
				entries64 = append(entries64, uint64(pos))
			} else {
				entries32 = append(entries32, uint32(pos))
			}
			// Write sample bytes.
			for s := uint32(0); s < spc[ci]; s++ {
				n := int(ts.sizes[idx])
				mdatPayload = append(mdatPayload, sampleBytes(byte(ti*16+ci)+byte(idx), n)...)
				idx++
			}
			pos += l
		}
		var ct []byte
		if ts.co64 {
			ct = co64Box(entries64)
		} else {
			ct = stcoBox(entries32)
		}
		var sz []byte
		if ts.sizeFmt == 0 {
			sz = stszBox(ts.sizes)
		} else {
			sz = stz2Box(ts.sizes, ts.sizeFmt)
		}
		traks = append(traks, trakBox(ct, stscBox(ts.stscRuns), sz))
	}
	moovBody := []byte{}
	for _, t := range traks {
		moovBody = append(moovBody, t...)
	}
	return box("moov", moovBody), mdatPayload
}

func writeFile(t *testing.T, dir, name string, parts ...[]byte) string {
	t.Helper()
	p := filepath.Join(dir, name)
	var buf []byte
	for _, b := range parts {
		buf = append(buf, b...)
	}
	if err := os.WriteFile(p, buf, 0o644); err != nil {
		t.Fatal(err)
	}
	return p
}

func readFile(t *testing.T, p string) []byte {
	t.Helper()
	b, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

// boxTypes returns the top-level box types of a file.
func boxTypes(t *testing.T, p string) []string {
	t.Helper()
	li, err := Layout(p)
	if err != nil {
		t.Fatal(err)
	}
	var types []string
	for _, b := range li.Boxes {
		types = append(types, b.Type)
	}
	return types
}

func equalStrings(a, b []string) bool {
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

// leafPayloads collects the payloads of all leaf boxes of the given types
// found anywhere inside moov.
func leafPayloads(t *testing.T, moovBytes []byte, types ...string) map[string][][]byte {
	t.Helper()
	moov, err := parseBoxTree(moovBytes, 0)
	if err != nil {
		t.Fatal(err)
	}
	out := map[string][][]byte{}
	moov.walkBoxes(func(b *mp4Box) {
		typ := string(b.typ[:])
		for _, want := range types {
			if typ == want {
				out[typ] = append(out[typ], b.payload)
			}
		}
	})
	return out
}

func TestRelocateBasic(t *testing.T) {
	dir := t.TempDir()
	ftyp := ftypBox()
	free := box("free", []byte{1, 2, 3, 4})
	skip := box("skip", []byte{9, 9})
	uuid := uuidBox([16]byte{0xaa, 0xbb}, []byte{7, 7, 7})

	specs := []trackSpec{
		{stscRuns: [][3]uint32{{1, 2, 1}, {3, 1, 1}}, sizes: []uint32{3, 5, 2, 6, 4, 7}, chunks: 4},
		{stscRuns: [][3]uint32{{1, 1, 1}, {3, 2, 1}}, sizes: []uint32{1, 2, 3, 4}, chunks: 3, sizeFmt: 4},
	}
	// Prefix: ftyp | free | mdat(header)
	payloadStart := int64(len(ftyp) + len(free) + 8)
	moov, mdatPayload := buildMoovAndMdat(payloadStart, specs)
	// Add a uuid leaf inside moov; it must survive the rebuild untouched.
	innerUUID := uuidBox([16]byte{0xde, 0xad, 0xbe, 0xef}, []byte{5, 6, 7})
	moov = container("moov", moov[8:], innerUUID)
	mdat := box("mdat", mdatPayload)

	src := writeFile(t, dir, "in.mp4", ftyp, free, mdat, moov, skip, uuid)

	// Layout of the input.
	li, err := Layout(src)
	if err != nil {
		t.Fatal(err)
	}
	wantTypes := []string{"ftyp", "free", "mdat", "moov", "skip", "uuid"}
	var gotTypes []string
	for _, b := range li.Boxes {
		gotTypes = append(gotTypes, b.Type)
	}
	if !equalStrings(gotTypes, wantTypes) {
		t.Fatalf("input layout types = %v, want %v", gotTypes, wantTypes)
	}
	if li.Boxes[5].HeaderSize != 24 {
		t.Fatalf("uuid header size = %d, want 24", li.Boxes[5].HeaderSize)
	}
	if len(li.Tracks) != 2 {
		t.Fatalf("tracks = %d, want 2", len(li.Tracks))
	}
	if li.Tracks[0].ChunkTableType != "stco" || li.Tracks[0].ChunkCount != 4 || li.Tracks[0].SampleCount != 6 {
		t.Fatalf("track0 = %+v", li.Tracks[0])
	}
	if li.Tracks[1].ChunkTableType != "stco" || li.Tracks[1].ChunkCount != 3 || li.Tracks[1].SampleCount != 4 {
		t.Fatalf("track1 = %+v", li.Tracks[1])
	}

	dst := filepath.Join(dir, "out.mp4")
	if err := Relocate(src, dst); err != nil {
		t.Fatal(err)
	}

	// Order: ftyp, moov, then the rest in original relative order.
	wantOrder := []string{"ftyp", "moov", "free", "mdat", "skip", "uuid"}
	if got := boxTypes(t, dst); !equalStrings(got, wantOrder) {
		t.Fatalf("output order = %v, want %v", got, wantOrder)
	}

	// Samples survive the move.
	eq, err := SamplesEqual(src, dst)
	if err != nil {
		t.Fatal(err)
	}
	if !eq {
		t.Fatal("SamplesEqual(src, dst) = false")
	}

	// Non-moov boxes are byte-identical.
	inBoxes := splitBoxes(t, readFile(t, src))
	outBoxes := splitBoxes(t, readFile(t, dst))
	for _, typ := range []string{"ftyp", "free", "mdat", "skip", "uuid"} {
		if !bytes.Equal(inBoxes[typ], outBoxes[typ]) {
			t.Fatalf("box %s changed", typ)
		}
	}

	// Inside moov, only chunk tables may change: stsc/stsz/stz2 payloads
	// must be identical.
	inMoov := leafPayloads(t, inBoxes["moov"], "stsc", "stsz", "stz2")
	outMoov := leafPayloads(t, outBoxes["moov"], "stsc", "stsz", "stz2")
	for typ := range inMoov {
		if len(inMoov[typ]) != len(outMoov[typ]) {
			t.Fatalf("%s count changed", typ)
		}
		for i := range inMoov[typ] {
			if !bytes.Equal(inMoov[typ][i], outMoov[typ][i]) {
				t.Fatalf("%s payload %d changed", typ, i)
			}
		}
	}

	// The uuid leaf inside moov is preserved byte-identically.
	if !bytes.Contains(splitBoxes(t, readFile(t, dst))["moov"], innerUUID) {
		t.Fatal("uuid box inside moov was not preserved")
	}

	// Idempotent: relocating the output again is a no-op byte-wise.
	dst2 := filepath.Join(dir, "out2.mp4")
	if err := Relocate(dst, dst2); err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(readFile(t, dst), readFile(t, dst2)) {
		t.Fatal("second Relocate changed the file")
	}
}

// splitBoxes splits a whole file into its top-level boxes by type
// (last one wins for duplicate types).
func splitBoxes(t *testing.T, data []byte) map[string][]byte {
	t.Helper()
	out := map[string][]byte{}
	for off := 0; off < len(data); {
		h, err := parseHeaderBytes(data[off:])
		if err != nil {
			t.Fatal(err)
		}
		out[string(h.typ[:])] = data[off : off+int(h.size)]
		off += int(h.size)
	}
	return out
}

func TestRelocateAlreadyFaststart(t *testing.T) {
	dir := t.TempDir()
	ftyp := ftypBox()
	specs := []trackSpec{
		{stscRuns: [][3]uint32{{1, 1, 1}}, sizes: []uint32{5, 5}, chunks: 2},
	}
	payloadStart := int64(len(ftyp))
	// moov comes first, so mdat payload starts after moov; build moov with
	// placeholder offsets, then rebuild with the real payload start.
	moov, _ := buildMoovAndMdat(0, specs)
	payloadStart += int64(len(moov) + 8)
	moov, mdatPayload := buildMoovAndMdat(payloadStart, specs)
	mdat := box("mdat", mdatPayload)
	src := writeFile(t, dir, "in.mp4", ftyp, moov, mdat)

	dst := filepath.Join(dir, "out.mp4")
	if err := Relocate(src, dst); err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(readFile(t, src), readFile(t, dst)) {
		t.Fatal("already-faststart file was modified")
	}
}

func TestRelocateMoovSizeZero(t *testing.T) {
	dir := t.TempDir()
	ftyp := ftypBox()
	specs := []trackSpec{
		{stscRuns: [][3]uint32{{1, 2, 1}}, sizes: []uint32{3, 3}, chunks: 1},
	}
	payloadStart := int64(len(ftyp) + 8)
	moov, mdatPayload := buildMoovAndMdat(payloadStart, specs)
	// Rewrite the moov size field as 0 (extends to EOF; moov is last).
	moov[0], moov[1], moov[2], moov[3] = 0, 0, 0, 0
	mdat := box("mdat", mdatPayload)
	src := writeFile(t, dir, "in.mp4", ftyp, mdat, moov)

	dst := filepath.Join(dir, "out.mp4")
	if err := Relocate(src, dst); err != nil {
		t.Fatal(err)
	}
	li, err := Layout(dst)
	if err != nil {
		t.Fatal(err)
	}
	if li.Boxes[1].Type != "moov" {
		t.Fatalf("box1 = %s, want moov", li.Boxes[1].Type)
	}
	if li.Boxes[1].Size != int64(len(moov)) {
		t.Fatalf("moov size = %d, want explicit %d", li.Boxes[1].Size, len(moov))
	}
	eq, err := SamplesEqual(src, dst)
	if err != nil {
		t.Fatal(err)
	}
	if !eq {
		t.Fatal("SamplesEqual = false")
	}
}

func TestRelocateCo64StaysCo64(t *testing.T) {
	dir := t.TempDir()
	ftyp := ftypBox()
	specs := []trackSpec{
		{stscRuns: [][3]uint32{{1, 1, 1}}, sizes: []uint32{9, 8, 7}, chunks: 3, co64: true},
	}
	payloadStart := int64(len(ftyp) + 8)
	moov, mdatPayload := buildMoovAndMdat(payloadStart, specs)
	mdat := box("mdat", mdatPayload)
	src := writeFile(t, dir, "in.mp4", ftyp, mdat, moov)

	dst := filepath.Join(dir, "out.mp4")
	if err := Relocate(src, dst); err != nil {
		t.Fatal(err)
	}
	li, err := Layout(dst)
	if err != nil {
		t.Fatal(err)
	}
	if li.Tracks[0].ChunkTableType != "co64" {
		t.Fatalf("chunk table = %s, want co64 (no downgrade)", li.Tracks[0].ChunkTableType)
	}
	eq, err := SamplesEqual(src, dst)
	if err != nil {
		t.Fatal(err)
	}
	if !eq {
		t.Fatal("SamplesEqual = false")
	}
}
