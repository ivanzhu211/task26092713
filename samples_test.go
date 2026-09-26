package faststart

import (
	"os"
	"path/filepath"
	"testing"
)

func writeImg(t *testing.T, dir, name string, img []byte) string {
	t.Helper()
	p := filepath.Join(dir, name)
	if err := os.WriteFile(p, img, 0o644); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestSamplesEqual(t *testing.T) {
	dir := t.TempDir()
	base, _ := validBase()
	a := writeImg(t, dir, "a.mp4", base)

	eq, err := SamplesEqual(a, a)
	if err != nil || !eq {
		t.Fatalf("self compare = %v, %v", eq, err)
	}

	// One sample byte flipped (inside track 0 chunk 0).
	base2, tracks := validBase()
	idx := int(tracks[0].chunks[0].off) + 1
	base = base2
	flipped := append([]byte{}, base...)
	flipped[idx] ^= 0xff
	b := writeImg(t, dir, "b.mp4", flipped)
	eq, err = SamplesEqual(a, b)
	if err != nil {
		t.Fatal(err)
	}
	if eq {
		t.Fatal("flipped sample byte not detected")
	}

	// Flipping a byte inside moov (not sample data) keeps samples equal.
	moovIdx := len(base) - 100
	tampered := append([]byte{}, base...)
	tampered[moovIdx] ^= 0xff
	// Only safe if that byte is not inside a table we parse; instead
	// tamper an stsc sample_description_index which we ignore.
	stscIdx := -1
	for i := 0; i+4 < len(base); i++ {
		if string(base[i:i+4]) == "stsc" {
			stscIdx = i
			break
		}
	}
	if stscIdx < 0 {
		t.Fatal("stsc not found")
	}
	tampered2 := append([]byte{}, base...)
	tampered2[stscIdx+20] ^= 0xff // sample_description_index of run 1 (ignored)
	c := writeImg(t, dir, "c.mp4", tampered2)
	eq, err = SamplesEqual(a, c)
	if err != nil {
		t.Fatal(err)
	}
	if !eq {
		t.Fatal("non-sample moov byte changed the comparison")
	}

	_ = moovIdx
}
