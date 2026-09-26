package faststart

import "fmt"

// samplesPerChunk expands a track's stsc table into a per-chunk sample
// count slice and checks the table against the chunk count and the
// sample size table.
func samplesPerChunk(tk *trackInfo) ([]uint32, error) {
	nChunks := tk.table.count
	if len(tk.stsc) == 0 {
		if nChunks == 0 && tk.sizes.Len() == 0 {
			return nil, nil
		}
		return nil, fmt.Errorf("faststart: empty stsc with %d chunks: %w",
			nChunks, ErrInconsistent)
	}
	if tk.stsc[0].firstChunk != 1 {
		return nil, fmt.Errorf("faststart: stsc does not start at chunk 1: %w",
			ErrInconsistent)
	}
	for i := 1; i < len(tk.stsc); i++ {
		if tk.stsc[i].firstChunk <= tk.stsc[i-1].firstChunk {
			return nil, fmt.Errorf("faststart: stsc first_chunk not increasing: %w",
				ErrInconsistent)
		}
	}
	perChunk := make([]uint32, nChunks)
	for i, e := range tk.stsc {
		if e.samplesPerChunk == 0 && nChunks > 0 {
			return nil, fmt.Errorf("faststart: stsc samples_per_chunk is 0: %w",
				ErrInconsistent)
		}
		from := e.firstChunk - 1
		to := nChunks
		if i+1 < len(tk.stsc) {
			to = tk.stsc[i+1].firstChunk - 1
		}
		if from >= to {
			if from >= nChunks {
				return nil, fmt.Errorf("faststart: stsc run starts past last chunk: %w",
					ErrInconsistent)
			}
			continue
		}
		if to > nChunks {
			return nil, fmt.Errorf("faststart: stsc run exceeds chunk count: %w",
				ErrInconsistent)
		}
		for c := from; c < to; c++ {
			perChunk[c] = e.samplesPerChunk
		}
	}
	var total uint64
	for _, n := range perChunk {
		total += uint64(n)
	}
	if total != uint64(tk.sizes.Len()) {
		return nil, fmt.Errorf("faststart: stsc implies %d samples, stsz/stz2 has %d: %w",
			total, tk.sizes.Len(), ErrInconsistent)
	}
	return perChunk, nil
}

// chunkByteSpan returns the total byte size of chunk idx using the
// per-chunk sample counts and the sample size table.
func chunkByteSpan(tk *trackInfo, perChunk []uint32, idx uint32) uint64 {
	var sampleBase uint64
	for c := uint32(0); c < idx; c++ {
		sampleBase += uint64(perChunk[c])
	}
	return tk.sizes.Span(uint32(sampleBase), perChunk[idx])
}

// mdatPayloads returns the sorted payload ranges [start,end) of all mdat
// boxes.
func mdatPayloads(boxes []Box) [][2]int64 {
	var ranges [][2]int64
	for _, b := range boxes {
		if b.Type == [4]byte{'m', 'd', 'a', 't'} {
			ranges = append(ranges, [2]int64{b.Offset + b.HeaderLen, b.Offset + b.Size})
		}
	}
	return ranges
}

// validateChunkOffsets checks that every chunk of every track lies fully
// inside some mdat payload. perChunkByTrack holds the expanded stsc of
// each track.
func validateChunkOffsets(mi *moovInfo, mdats [][2]int64, perChunkByTrack [][]uint32) error {
	for ti, tk := range mi.tracks {
		perChunk := perChunkByTrack[ti]
		for c := uint32(0); c < tk.table.count; c++ {
			start := tk.table.entries[c]
			span := chunkByteSpan(tk, perChunk, c)
			end := start + span
			if end < start {
				return fmt.Errorf("faststart: track %d chunk %d: offset overflow: %w",
					ti, c, ErrBadOffset)
			}
			ok := false
			for _, r := range mdats {
				if int64(start) >= r[0] && int64(end) <= r[1] {
					ok = true
					break
				}
			}
			if !ok {
				return fmt.Errorf("faststart: track %d chunk %d at %d span %d: %w",
					ti, c, start, span, ErrBadOffset)
			}
		}
	}
	return nil
}
