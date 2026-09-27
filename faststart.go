// Package faststart relocates the moov box of a non-fragmented ISO BMFF
// (MP4) file to the front of the file so that browsers can start playback
// before the whole file is downloaded. It uses only the standard library.
package faststart

import (
	"fmt"
	"os"
)

// BoxInfo describes one top-level box of an MP4 file.
type BoxInfo struct {
	// Type is the 4-character box type ("ftyp", "moov", "mdat", "uuid", ...).
	Type string
	// Offset is the absolute file offset of the box header.
	Offset int64
	// HeaderSize is the length of the box header in bytes
	// (8, 16 for largesize, plus 16 more for uuid usertype).
	HeaderSize int64
	// Size is the total box size in bytes, header included.
	Size int64
}

// TrackInfo summarizes the sample tables of one trak.
type TrackInfo struct {
	// ChunkTableType is "stco" or "co64".
	ChunkTableType string
	// ChunkCount is the number of entries in the chunk offset table.
	ChunkCount int
	// SampleCount is the number of samples declared by stsz/stz2.
	SampleCount int
}

// LayoutInfo is the result of Layout.
type LayoutInfo struct {
	Boxes  []BoxInfo
	Tracks []TrackInfo
}

// Layout lists the top-level boxes of src and, for every track, the chunk
// table type, chunk count and sample count.
func Layout(src string) (*LayoutInfo, error) {
	f, err := os.Open(src)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	fi, err := f.Stat()
	if err != nil {
		return nil, err
	}
	boxes, err := scanTopLevel(f, fi.Size())
	if err != nil {
		return nil, err
	}
	info := &LayoutInfo{}
	moovIdx := -1
	for i, b := range boxes {
		info.Boxes = append(info.Boxes, BoxInfo{
			Type:       string(b.typ[:]),
			Offset:     b.offset,
			HeaderSize: b.header,
			Size:       b.size,
		})
		if b.typ == [4]byte{'m', 'o', 'o', 'v'} && moovIdx < 0 {
			moovIdx = i
		}
	}
	if moovIdx < 0 {
		return info, nil
	}
	data := make([]byte, boxes[moovIdx].size)
	if _, err := f.ReadAt(data, boxes[moovIdx].offset); err != nil {
		return nil, fmt.Errorf("faststart: cannot read moov: %w", err)
	}
	moov, err := parseBoxTree(data, 0)
	if err != nil {
		return nil, err
	}
	tracks, err := extractTracks(moov)
	if err != nil {
		return nil, err
	}
	for _, t := range tracks {
		info.Tracks = append(info.Tracks, TrackInfo{
			ChunkTableType: string(t.chunkTable.typ[:]),
			ChunkCount:     len(t.chunkTable.entries),
			SampleCount:    int(t.sampleCount),
		})
	}
	return info, nil
}
