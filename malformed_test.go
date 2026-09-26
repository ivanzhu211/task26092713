package faststart

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

// validBase returns a small valid ftyp|mdat|moov file image.
func validBase() ([]byte, []muxTrack) {
	ftyp := ftypBox()
	mdatPayloadOff := uint64(len(ftyp)) + 8
	tracks := smallTracks(mdatPayloadOff)
	moov := buildMoovBox(tracks)
	mdat := box("mdat", make([]byte, 256))
	var img []byte
	img = append(img, ftyp...)
	img = append(img, mdat...)
	img = append(img, moov...)
	// Fill sample bytes.
	for ti, tk := range tracks {
		for ci, c := range tk.chunks {
			copy(img[c.off:], chunkBytes(ti, ci, c))
		}
	}
	return img, tracks
}

// expectErr writes img, runs Relocate and Layout, and checks that both
// fail with want and that no partial dst file remains.
func expectErr(t *testing.T, name string, img []byte, want error) {
	t.Helper()
	dir := t.TempDir()
	src := filepath.Join(dir, "bad.mp4")
	if err := os.WriteFile(src, img, 0o644); err != nil {
		t.Fatal(err)
	}
	dst := filepath.Join(dir, "bad-out.mp4")
	if err := Relocate(src, dst); !errors.Is(err, want) {
		t.Fatalf("%s: Relocate err = %v, want %v", name, err, want)
	}
	if _, err := os.Stat(dst); !os.IsNotExist(err) {
		t.Fatalf("%s: dst left behind after error", name)
	}
	if _, err := Layout(src); !errors.Is(err, want) {
		t.Fatalf("%s: Layout err = %v, want %v", name, err, want)
	}
}

func TestMalformed(t *testing.T) {
	base, _ := validBase()

	t.Run("truncated", func(t *testing.T) {
		expectErr(t, "truncated", base[:len(base)-10], ErrTruncated)
	})

	t.Run("dangling bytes", func(t *testing.T) {
		expectErr(t, "dangling", append(append([]byte{}, base...), 1, 2, 3, 4), ErrTruncated)
	})

	t.Run("size smaller than header", func(t *testing.T) {
		img := append([]byte{}, base...)
		copy(img[0:4], be32(4)) // ftyp size 4 < header 8
		expectErr(t, "size<header", img, ErrBadSize)
	})

	t.Run("size==0 not last", func(t *testing.T) {
		img := append([]byte{}, base...)
		copy(img[0:4], be32(0)) // ftyp swallows the rest
		expectErr(t, "size0-not-last", img, ErrNoMoov)
	})

	t.Run("moof", func(t *testing.T) {
		img := append([]byte{}, base...)
		img = append(img, box("moof", []byte{0, 0, 0, 0})...)
		// Fix: moof must come before moov to be seen; append is fine
		// since scan walks all boxes, but moov is found first. Rebuild:
		// ftyp | moof | mdat | moov
		ftypLen := len(ftypBox())
		var rebuilt []byte
		rebuilt = append(rebuilt, img[:ftypLen]...)
		rebuilt = append(rebuilt, box("moof", []byte{0, 0, 0, 0})...)
		rebuilt = append(rebuilt, img[ftypLen:len(base)]...)
		expectErr(t, "moof", rebuilt, ErrFragmented)
	})

	t.Run("mvex", func(t *testing.T) {
		_, tracks := validBase()
		ftyp := ftypBox()
		mdat := box("mdat", make([]byte, 256))
		// moov(mvex + original moov payload)
		orig := buildMoovBox(tracks)
		moov2 := box("moov", append(box("mvex", []byte{0, 0, 0, 0}), orig[8:]...))
		var img []byte
		img = append(img, ftyp...)
		img = append(img, mdat...)
		img = append(img, moov2...)
		for ti, tk := range tracks {
			for ci, c := range tk.chunks {
				copy(img[c.off:], chunkBytes(ti, ci, c))
			}
		}
		expectErr(t, "mvex", img, ErrFragmented)
	})

	t.Run("no moov", func(t *testing.T) {
		var img []byte
		img = append(img, ftypBox()...)
		img = append(img, box("mdat", make([]byte, 64))...)
		expectErr(t, "no-moov", img, ErrNoMoov)
	})

	t.Run("chunk offset outside mdat", func(t *testing.T) {
		img := append([]byte{}, base...)
		// Point track 0 chunk 0 into the moov box.
		idx := bytes.Index(img, []byte("stco"))
		if idx < 0 {
			t.Fatal("stco not found")
		}
		entryOff := idx + 4 + 8 // type + version/flags + count
		copy(img[entryOff:entryOff+4], be32(uint32(len(base))-40))
		expectErr(t, "bad-offset", img, ErrBadOffset)
	})

	t.Run("stsc stsz mismatch", func(t *testing.T) {
		img := append([]byte{}, base...)
		idx := bytes.Index(img, []byte("stsc"))
		if idx < 0 {
			t.Fatal("stsc not found")
		}
		// Last run's samples_per_chunk += 1.
		count := int(be32to(img[idx+8 : idx+12]))
		last := idx + 12 + (count-1)*12 + 4
		v := be32to(img[last : last+4])
		copy(img[last:last+4], be32(v+1))
		expectErr(t, "stsc-mismatch", img, ErrInconsistent)
	})

	t.Run("stz2 bad field size", func(t *testing.T) {
		img := append([]byte{}, base...)
		idx := bytes.Index(img, []byte("stz2"))
		if idx < 0 {
			t.Fatal("stz2 not found")
		}
		img[idx+11] = 7 // field_size 7 is invalid
		expectErr(t, "stz2-field", img, ErrInconsistent)
	})

	t.Run("stco count exceeds box", func(t *testing.T) {
		img := append([]byte{}, base...)
		idx := bytes.Index(img, []byte("stco"))
		if idx < 0 {
			t.Fatal("stco not found")
		}
		copy(img[idx+8:idx+12], be32(0x10000))
		expectErr(t, "stco-count", img, ErrInconsistent)
	})

	t.Run("missing stsc", func(t *testing.T) {
		_, tracks := validBase()
		// Build a moov without stsc by hand.
		var moovPayload []byte
		for _, tk := range tracks {
			var stbl []byte
			stbl = append(stbl, box("stco", stcoPayload(tk))...)
			stbl = append(stbl, box("stsz", stszLikePayload(tk))...)
			moovPayload = append(moovPayload, box("trak", box("mdia", box("minf", box("stbl", stbl))))...)
		}
		var img []byte
		img = append(img, ftypBox()...)
		img = append(img, box("mdat", make([]byte, 256))...)
		img = append(img, box("moov", moovPayload)...)
		expectErr(t, "missing-stsc", img, ErrInconsistent)
	})

	t.Run("huge constant stsz", func(t *testing.T) {
		// A constant-size stsz may declare any sample_count; the
		// library must not allocate proportional to it. The implied
		// chunk span lands outside every mdat, so this is ErrBadOffset.
		ftyp := ftypBox()
		mdatPayloadOff := uint64(len(ftyp)) + 8
		stco := box("stco", append(append([]byte{0, 0, 0, 0}, be32(1)...), be32(uint32(mdatPayloadOff))...))
		stsc := box("stsc", append(append([]byte{0, 0, 0, 0}, be32(1)...),
			append(append(be32(1), be32(0xFFFFFF00)...), be32(1)...)...))
		stsz := box("stsz", append(append([]byte{0, 0, 0, 0}, be32(8)...), be32(0xFFFFFF00)...))
		stbl := box("stbl", append(append(append([]byte{}, stco...), stsc...), stsz...))
		moov := box("moov", box("trak", box("mdia", box("minf", stbl))))
		var img []byte
		img = append(img, ftyp...)
		img = append(img, box("mdat", make([]byte, 256))...)
		img = append(img, moov...)
		expectErr(t, "huge-stsz", img, ErrBadOffset)
	})
}

func be32to(b []byte) uint32 {
	return uint32(b[0])<<24 | uint32(b[1])<<16 | uint32(b[2])<<8 | uint32(b[3])
}
