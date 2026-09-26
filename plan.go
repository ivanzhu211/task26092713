package faststart

import (
	"encoding/binary"
	"fmt"
	"math"
)

// relocPlan describes how the output file is assembled.
type relocPlan struct {
	verbatim   bool    // copy input byte for byte
	order      []int   // box indices (into pf.boxes) in output order
	outOffsets []int64 // output offset per input box index
	upgrade    []bool  // per track: rewrite stco as co64
	newMoov    []byte  // full replacement moov box including header
}

// upgradeDelta returns how much the moov payload grows when table t is
// rewritten from stco to co64.
func upgradeDelta(t *chunkTable) int64 {
	newSize := t.size + 4*int64(t.count)
	delta := 4 * int64(t.count)
	if t.headerLen == 8 && newSize >= 1<<32 {
		delta += 8 // header must switch to largesize form
	}
	return delta
}

// moovHeaderLen picks the header form for the rewritten moov box.
func moovHeaderLen(origHeaderLen int64, newTotal int64) int64 {
	if origHeaderLen == 16 || newTotal >= 1<<32 {
		return 16
	}
	return 8
}

// buildPlan computes the relocation plan for pf.
func buildPlan(pf *parsedFile) (*relocPlan, error) {
	moovBox := pf.boxes[pf.moovIdx]

	// Requirement 1: moov already precedes every mdat -> verbatim copy.
	verbatim := true
	for _, b := range pf.boxes {
		if b.Type == [4]byte{'m', 'd', 'a', 't'} && b.Offset < moovBox.Offset {
			verbatim = false
			break
		}
	}
	if verbatim {
		return &relocPlan{verbatim: true}, nil
	}

	// Output order: ftyp (if any), moov, then the rest in original
	// relative order. Nothing is inserted between boxes.
	ftypIdx := -1
	for i, b := range pf.boxes {
		if b.Type == [4]byte{'f', 't', 'y', 'p'} {
			ftypIdx = i
			break
		}
	}
	var order []int
	if ftypIdx >= 0 {
		order = append(order, ftypIdx)
	}
	order = append(order, pf.moovIdx)
	for i := range pf.boxes {
		if i != ftypIdx && i != pf.moovIdx {
			order = append(order, i)
		}
	}

	// Map each chunk to the mdat box (index into pf.boxes) holding it.
	chunkMdat := make([][]int, len(pf.moov.tracks))
	for ti, tk := range pf.moov.tracks {
		chunkMdat[ti] = make([]int, tk.table.count)
		for c := uint32(0); c < tk.table.count; c++ {
			off := tk.table.entries[c]
			idx := -1
			for bi, b := range pf.boxes {
				if b.Type == [4]byte{'m', 'd', 'a', 't'} &&
					int64(off) >= b.Offset+b.HeaderLen &&
					int64(off) < b.Offset+b.Size {
					idx = bi
					break
				}
			}
			if idx < 0 {
				return nil, fmt.Errorf("faststart: track %d chunk %d: %w",
					ti, c, ErrBadOffset)
			}
			chunkMdat[ti][c] = idx
		}
	}

	// Fixpoint: grow the upgrade set until no stco table sees an offset
	// >= 2^32 in the final layout. Upgrades only ever increase moov's
	// size, and moov sits before every mdat in the output, so offsets
	// are monotone in the upgrade set and the loop terminates.
	upgrade := make([]bool, len(pf.moov.tracks))
	var outOffsets []int64
	for {
		var moovDelta int64
		for ti, tk := range pf.moov.tracks {
			if upgrade[ti] && !tk.table.co64 {
				moovDelta += upgradeDelta(tk.table)
			}
		}
		newMoovTotal := moovBox.Size + moovDelta
		hdrLen := moovHeaderLen(moovBox.HeaderLen, newMoovTotal)
		moovDelta += hdrLen - moovBox.HeaderLen
		newMoovTotal = moovBox.Size + moovDelta

		outOffsets = make([]int64, len(pf.boxes))
		pos := int64(0)
		for _, bi := range order {
			outOffsets[bi] = pos
			if bi == pf.moovIdx {
				pos += newMoovTotal
			} else {
				pos += pf.boxes[bi].Size
			}
		}

		changed := false
		for ti, tk := range pf.moov.tracks {
			if tk.table.co64 || upgrade[ti] {
				continue
			}
			for c := uint32(0); c < tk.table.count; c++ {
				m := chunkMdat[ti][c]
				newOff := int64(tk.table.entries[c]) - pf.boxes[m].Offset + outOffsets[m]
				if newOff >= 1<<32 {
					upgrade[ti] = true
					changed = true
					break
				}
			}
		}
		if !changed {
			break
		}
	}

	// Final chunk offsets in the output layout, per track per chunk.
	newEntries := make([][]uint64, len(pf.moov.tracks))
	for ti, tk := range pf.moov.tracks {
		newEntries[ti] = make([]uint64, tk.table.count)
		for c := uint32(0); c < tk.table.count; c++ {
			m := chunkMdat[ti][c]
			newEntries[ti][c] = uint64(int64(tk.table.entries[c]) -
				pf.boxes[m].Offset + outOffsets[m])
		}
	}

	newMoov, err := buildMoov(pf, upgrade, newEntries)
	if err != nil {
		return nil, err
	}
	return &relocPlan{
		order:      order,
		outOffsets: outOffsets,
		upgrade:    upgrade,
		newMoov:    newMoov,
	}, nil
}

// buildMoov produces the rewritten moov box: every chunk table gets the
// relocated offsets, upgraded tables are re-emitted as co64, ancestor
// size fields are bumped, and the moov header carries an explicit size.
func buildMoov(pf *parsedFile, upgrade []bool, newEntries [][]uint64) ([]byte, error) {
	mi := pf.moov
	payload := make([]byte, len(mi.payload))
	copy(payload, mi.payload)

	// Rewrite entries of tables that keep their wire form in place.
	for ti, tk := range mi.tracks {
		t := tk.table
		if upgrade[ti] && !t.co64 {
			continue // re-emitted as co64 below
		}
		for i := uint32(0); i < t.count; i++ {
			e := newEntries[ti][i]
			p := t.entryBase + int64(i)*4
			if t.co64 {
				p = t.entryBase + int64(i)*8
				binary.BigEndian.PutUint64(payload[p:p+8], e)
			} else {
				if e >= 1<<32 {
					return nil, fmt.Errorf("faststart: stco entry overflows 32 bits: %w",
						ErrInconsistent)
				}
				binary.BigEndian.PutUint32(payload[p:p+4], uint32(e))
			}
		}
	}

	// Collect upgraded tables (by track index); ancestor patches are
	// applied at original payload offsets before splicing.
	var upgraded []int
	var totalDelta int64
	for ti, tk := range mi.tracks {
		if !upgrade[ti] || tk.table.co64 {
			continue
		}
		t := tk.table
		delta := upgradeDelta(t)
		upgraded = append(upgraded, ti)
		totalDelta += delta
		for _, ref := range t.ancestors {
			if ref.large {
				old := binary.BigEndian.Uint64(payload[ref.off : ref.off+8])
				binary.BigEndian.PutUint64(payload[ref.off:ref.off+8], old+uint64(delta))
			} else {
				old := binary.BigEndian.Uint32(payload[ref.off : ref.off+4])
				if uint64(old)+uint64(delta) > math.MaxUint32 {
					return nil, fmt.Errorf("faststart: container size overflows 32 bits: %w",
						ErrInconsistent)
				}
				binary.BigEndian.PutUint32(payload[ref.off:ref.off+4], old+uint32(delta))
			}
		}
	}

	// Splice: copy patched segments, replacing each upgraded stco box
	// with its co64 form. Tables are nested in disjoint traks, so their
	// box ranges never overlap; sort by offset for the walk.
	for i := 0; i < len(upgraded); i++ {
		for j := i + 1; j < len(upgraded); j++ {
			if mi.tracks[upgraded[j]].table.boxOff < mi.tracks[upgraded[i]].table.boxOff {
				upgraded[i], upgraded[j] = upgraded[j], upgraded[i]
			}
		}
	}
	var out []byte
	cursor := int64(0)
	emit := func(b []byte) { out = append(out, b...) }
	for _, ti := range upgraded {
		t := mi.tracks[ti].table
		emit(payload[cursor:t.boxOff])
		emit(buildCo64Box(payload, t, newEntries[ti]))
		cursor = t.boxOff + t.size
	}
	emit(payload[cursor:])

	newTotal := mi.box.HeaderLen + int64(len(mi.payload)) + totalDelta
	hdrLen := moovHeaderLen(mi.box.HeaderLen, newTotal)
	newTotal = hdrLen + int64(len(out))
	header := make([]byte, hdrLen)
	if hdrLen == 16 {
		binary.BigEndian.PutUint32(header[0:4], 1)
		copy(header[4:8], "moov")
		binary.BigEndian.PutUint64(header[8:16], uint64(newTotal))
	} else {
		if newTotal >= 1<<32 {
			return nil, fmt.Errorf("faststart: moov size overflow: %w", ErrInconsistent)
		}
		binary.BigEndian.PutUint32(header[0:4], uint32(newTotal))
		copy(header[4:8], "moov")
	}
	return append(header, out...), nil
}

// buildCo64Box re-emits an stco table as a co64 box with the relocated
// entries widened to 64 bits.
func buildCo64Box(payload []byte, t *chunkTable, entries []uint64) []byte {
	hdrLen := t.headerLen
	if hdrLen == 8 && t.size+4*int64(t.count) >= 1<<32 {
		hdrLen = 16
	}
	finalSize := hdrLen + (t.size - t.headerLen) + 4*int64(t.count)
	out := make([]byte, 0, hdrLen+8+8*int64(t.count))
	header := make([]byte, hdrLen)
	if hdrLen == 16 {
		binary.BigEndian.PutUint32(header[0:4], 1)
		copy(header[4:8], "co64")
		binary.BigEndian.PutUint64(header[8:16], uint64(finalSize))
	} else {
		binary.BigEndian.PutUint32(header[0:4], uint32(finalSize))
		copy(header[4:8], "co64")
	}
	out = append(out, header...)
	// version/flags and entry_count carry over unchanged.
	vf := t.boxOff + t.headerLen
	out = append(out, payload[vf:vf+8]...)
	var ent [8]byte
	for _, e := range entries {
		binary.BigEndian.PutUint64(ent[:], e)
		out = append(out, ent[:]...)
	}
	return out
}
