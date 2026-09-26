package faststart

import (
	"fmt"
	"os"
)

// TrackInfo summarizes one trak's chunk and sample tables.
type TrackInfo struct {
	ChunkTableType string // "stco" or "co64"
	ChunkCount     uint32 // number of chunk offsets
	SampleCount    uint32 // total number of samples
}

// LayoutInfo is the result of Layout.
type LayoutInfo struct {
	Boxes  []Box
	Tracks []TrackInfo
}

// parsedFile bundles the scan/parse results shared by all operations.
type parsedFile struct {
	f        *os.File
	size     int64
	boxes    []Box
	moovIdx  int // index of moov in boxes, -1 if absent
	moov     *moovInfo
	mdats    [][2]int64
	perChunk [][]uint32 // expanded stsc per track
}

// parseFile opens path, scans the top-level boxes and parses moov with
// full validation. The caller must close pf.f.
func parseFile(path string) (*parsedFile, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	pf, err := parseFileHandle(f)
	if err != nil {
		f.Close()
		return nil, err
	}
	return pf, nil
}

func parseFileHandle(f *os.File) (*parsedFile, error) {
	size, err := fileSizeOf(f)
	if err != nil {
		return nil, err
	}
	boxes, err := scanTopLevel(f, size)
	if err != nil {
		return nil, err
	}
	pf := &parsedFile{f: f, size: size, boxes: boxes, moovIdx: -1}
	for i, b := range boxes {
		if b.Type == [4]byte{'m', 'o', 'o', 'v'} {
			if pf.moovIdx >= 0 {
				return nil, fmt.Errorf("faststart: multiple moov boxes: %w", ErrInconsistent)
			}
			pf.moovIdx = i
		}
	}
	if pf.moovIdx < 0 {
		return nil, ErrNoMoov
	}
	payload, err := readMoovPayload(f, boxes[pf.moovIdx])
	if err != nil {
		return nil, err
	}
	mi, err := parseMoov(boxes[pf.moovIdx], payload)
	if err != nil {
		return nil, err
	}
	pf.moov = mi
	pf.mdats = mdatPayloads(boxes)
	pf.perChunk = make([][]uint32, len(mi.tracks))
	for i, tk := range mi.tracks {
		perChunk, err := samplesPerChunk(tk)
		if err != nil {
			return nil, err
		}
		pf.perChunk[i] = perChunk
	}
	if err := validateChunkOffsets(mi, pf.mdats, pf.perChunk); err != nil {
		return nil, err
	}
	return pf, nil
}

// Layout lists the top-level boxes of the file at srcPath and, for each
// trak, the chunk table type, chunk count and sample count.
func Layout(srcPath string) (*LayoutInfo, error) {
	pf, err := parseFile(srcPath)
	if err != nil {
		return nil, err
	}
	defer pf.f.Close()
	info := &LayoutInfo{Boxes: pf.boxes}
	for _, tk := range pf.moov.tracks {
		typ := "stco"
		if tk.table.co64 {
			typ = "co64"
		}
		info.Tracks = append(info.Tracks, TrackInfo{
			ChunkTableType: typ,
			ChunkCount:     tk.table.count,
			SampleCount:    tk.sizes.Len(),
		})
	}
	return info, nil
}
