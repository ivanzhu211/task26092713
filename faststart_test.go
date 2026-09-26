package faststart

import (
	"bytes"
	"encoding/binary"
	"io"
	"os"
	"path/filepath"
	"testing"
)

const (
	testFtypSize  = 32
	testMdatAHead = 16 // largesize
	testMdatBHead = 16 // largesize
	testMdatBPay  = 512
)

// filesEqual streams both files and reports byte equality.
func filesEqual(t *testing.T, a, b string) bool {
	t.Helper()
	fa, err := os.Open(a)
	if err != nil {
		t.Fatal(err)
	}
	defer fa.Close()
	fb, err := os.Open(b)
	if err != nil {
		t.Fatal(err)
	}
	defer fb.Close()
	sa, _ := fa.Stat()
	sb, _ := fb.Stat()
	if sa.Size() != sb.Size() {
		return false
	}
	bufA := make([]byte, copyBufSize)
	bufB := make([]byte, copyBufSize)
	for {
		nA, errA := io.ReadFull(fa, bufA)
		nB, errB := io.ReadFull(fb, bufB)
		if nA != nB || !bytes.Equal(bufA[:nA], bufB[:nB]) {
			return false
		}
		if errA == io.EOF || errA == io.ErrUnexpectedEOF {
			return errB != nil
		}
		if errA != nil || errB != nil {
			t.Fatalf("read error: %v %v", errA, errB)
		}
	}
}

// mainLayout builds the ftyp|mdat(A)|moov|mdat(B) fixture spanning 4 GiB
// and returns the input path plus the track definitions.
func mainLayout(t *testing.T, dir string) (string, []muxTrack) {
	t.Helper()
	const pA = 1<<32 + 1024 // mdat A payload size
	moovOff := int64(testFtypSize + testMdatAHead + pA)

	// Tracks with placeholder offsets to learn the moov size M (offset
	// values do not affect the size of stco tables).
	tracks := []muxTrack{
		{chunks: []muxChunk{
			{sizes: []uint32{4, 7}},
			{sizes: []uint32{10}},
			{sizes: []uint32{5, 6, 9}},
			{sizes: []uint32{8, 4}},
		}},
		{stszConst: true, chunks: []muxChunk{
			{sizes: []uint32{8, 8}},
			{sizes: []uint32{8}},
			{sizes: []uint32{8, 8, 8}},
			{sizes: []uint32{8, 8}},
		}},
		{stz2Field: 4, chunks: []muxChunk{
			{sizes: []uint32{6, 9}},
			{sizes: []uint32{5}},
			{sizes: []uint32{4, 7, 10}},
			{sizes: []uint32{6, 9}},
		}},
	}
	m := int64(len(buildMoovBox(tracks)))

	mdatBOff := moovOff + m
	mdatBPay := mdatBOff + testMdatBHead
	abs1 := uint64(1<<32) - uint64(m) + 64 // track1: crosses 2^32 after the move
	abs2 := uint64(1<<32) - uint64(m) - 8  // track2: crosses only after t1 upgrades
	abs3 := abs2 - 4096                    // track3: always stays below 2^32

	// Fill in the real chunk offsets.
	tracks[0].chunks[0].off = testFtypSize + testMdatAHead + 0
	tracks[0].chunks[1].off = uint64(mdatBPay) + 0
	tracks[0].chunks[2].off = testFtypSize + testMdatAHead + (1 << 20)
	tracks[0].chunks[3].off = abs1
	tracks[1].chunks[0].off = testFtypSize + testMdatAHead + 512
	tracks[1].chunks[1].off = uint64(mdatBPay) + 64
	tracks[1].chunks[2].off = uint64(mdatBPay) + 128
	tracks[1].chunks[3].off = abs2
	tracks[2].chunks[0].off = testFtypSize + testMdatAHead + 1024
	tracks[2].chunks[1].off = uint64(mdatBPay) + 192
	tracks[2].chunks[2].off = uint64(mdatBPay) + 256
	tracks[2].chunks[3].off = abs3

	moov := buildMoovBox(tracks)
	if int64(len(moov)) != m {
		t.Fatalf("moov size changed with real offsets: %d != %d", len(moov), m)
	}

	path := filepath.Join(dir, "main-input.mp4")
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()

	ftypPayload := append([]byte("isom"), be32(0)...)
	ftypPayload = append(ftypPayload, []byte("isomiso2mp41mp42")...)
	writeAt(t, f, 0, box("ftyp", ftypPayload))

	hdr := append(be32(1), []byte("mdat")...)
	writeAt(t, f, testFtypSize, append(hdr, be64(pA+testMdatAHead)...))
	writeAt(t, f, moovOff, moov)
	writeAt(t, f, mdatBOff, append(append(be32(1), []byte("mdat")...), be64(testMdatBPay+testMdatBHead)...))
	writeSamples(t, f, tracks)

	fileSize := mdatBOff + testMdatBHead + testMdatBPay
	if err := f.Truncate(fileSize); err != nil {
		t.Fatal(err)
	}
	return path, tracks
}

func be64(v uint64) []byte {
	var b [8]byte
	binary.BigEndian.PutUint64(b[:], v)
	return b[:]
}

func boxTypes(info *LayoutInfo) []string {
	var out []string
	for _, b := range info.Boxes {
		out = append(out, b.TypeString())
	}
	return out
}

func TestRelocateMainLayout(t *testing.T) {
	dir := t.TempDir()
	input, _ := mainLayout(t, dir)
	output := filepath.Join(dir, "main-output.mp4")
	output2 := filepath.Join(dir, "main-output2.mp4")

	// Input layout: ftyp | mdat(A) | moov | mdat(B), three stco tracks.
	info, err := Layout(input)
	if err != nil {
		t.Fatal(err)
	}
	wantTypes := []string{"ftyp", "mdat", "moov", "mdat"}
	if got := boxTypes(info); !equalStrings(got, wantTypes) {
		t.Fatalf("input boxes = %v, want %v", got, wantTypes)
	}
	if info.Boxes[1].HeaderLen != 16 || info.Boxes[3].HeaderLen != 16 {
		t.Fatalf("mdat boxes should use largesize headers: %+v", info.Boxes)
	}
	for i, tr := range info.Tracks {
		if tr.ChunkTableType != "stco" || tr.ChunkCount != 4 || tr.SampleCount != 8 {
			t.Fatalf("input track %d = %+v", i, tr)
		}
	}

	if err := Relocate(input, output); err != nil {
		t.Fatal(err)
	}

	// Output layout: ftyp | moov | mdat(A) | mdat(B).
	outInfo, err := Layout(output)
	if err != nil {
		t.Fatal(err)
	}
	wantTypes = []string{"ftyp", "moov", "mdat", "mdat"}
	if got := boxTypes(outInfo); !equalStrings(got, wantTypes) {
		t.Fatalf("output boxes = %v, want %v", got, wantTypes)
	}
	wantTables := []string{"co64", "co64", "stco"}
	for i, tr := range outInfo.Tracks {
		if tr.ChunkTableType != wantTables[i] {
			t.Fatalf("output track %d table = %s, want %s", i, tr.ChunkTableType, wantTables[i])
		}
	}

	// White-box: track1 and track2 now reference offsets >= 2^32,
	// track3 stays below; all input offsets were below 2^32.
	inPF, err := parseFile(input)
	if err != nil {
		t.Fatal(err)
	}
	defer inPF.f.Close()
	for ti, tk := range inPF.moov.tracks {
		for _, e := range tk.table.entries {
			if e >= 1<<32 {
				t.Fatalf("input track %d has offset >= 2^32", ti)
			}
		}
	}
	outPF, err := parseFile(output)
	if err != nil {
		t.Fatal(err)
	}
	defer outPF.f.Close()
	for ti, want := range []bool{true, true, false} {
		var max uint64
		for _, e := range outPF.moov.tracks[ti].table.entries {
			if e > max {
				max = e
			}
		}
		if got := max >= 1<<32; got != want {
			t.Fatalf("output track %d max offset %#x, crosses 2^32 = %v, want %v",
				ti, max, got, want)
		}
	}

	eq, err := SamplesEqual(input, output)
	if err != nil {
		t.Fatal(err)
	}
	if !eq {
		t.Fatal("SamplesEqual(input, output) = false")
	}

	// Idempotence: relocating the output again changes nothing.
	if err := Relocate(output, output2); err != nil {
		t.Fatal(err)
	}
	if !filesEqual(t, output, output2) {
		t.Fatal("second Relocate changed the file")
	}

	// Disk budget: everything must stay below 64 MiB physically.
	for _, p := range []string{input, output, output2} {
		if du := diskUsage(t, p); du > 64<<20 {
			t.Fatalf("%s uses %d bytes on disk, over 64 MiB", p, du)
		}
	}
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
