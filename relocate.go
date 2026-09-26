package faststart

import (
	"io"
	"os"
	"path/filepath"
)

// copyBufSize caps the streaming buffer; memory never grows with mdat.
const copyBufSize = 1 << 20

// Relocate writes a copy of the file at srcPath to dstPath with the moov
// box placed immediately after ftyp (if present). If moov already
// precedes every mdat, dstPath is a byte-identical copy of srcPath.
// On error dstPath is never left partially written.
func Relocate(srcPath, dstPath string) error {
	pf, err := parseFile(srcPath)
	if err != nil {
		return err
	}
	defer pf.f.Close()

	plan, err := buildPlan(pf)
	if err != nil {
		return err
	}

	dir := filepath.Dir(dstPath)
	tmp, err := os.CreateTemp(dir, ".faststart-*")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	ok := false
	defer func() {
		if !ok {
			tmp.Close()
			os.Remove(tmpName)
		}
	}()

	if err := writeOutput(pf, plan, tmp); err != nil {
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Rename(tmpName, dstPath); err != nil {
		return err
	}
	ok = true
	return nil
}

// writeOutput streams the relocated file into w. Runs of zero bytes are
// skipped with Seek so sparse inputs stay sparse on disk; the resulting
// byte stream is identical either way.
func writeOutput(pf *parsedFile, plan *relocPlan, w *os.File) error {
	buf := make([]byte, copyBufSize)
	var total int64
	copyRange := func(off, n int64) error {
		if _, err := pf.f.Seek(off, io.SeekStart); err != nil {
			return err
		}
		remaining := n
		for remaining > 0 {
			chunk := int64(copyBufSize)
			if remaining < chunk {
				chunk = remaining
			}
			if _, err := io.ReadFull(pf.f, buf[:chunk]); err != nil {
				return err
			}
			if isZero(buf[:chunk]) {
				if _, err := w.Seek(chunk, io.SeekCurrent); err != nil {
					return err
				}
			} else {
				if _, err := w.Write(buf[:chunk]); err != nil {
					return err
				}
			}
			remaining -= chunk
			total += chunk
		}
		return nil
	}

	if plan.verbatim {
		if err := copyRange(0, pf.size); err != nil {
			return err
		}
	} else {
		for _, bi := range plan.order {
			if bi == pf.moovIdx {
				if _, err := w.Write(plan.newMoov); err != nil {
					return err
				}
				total += int64(len(plan.newMoov))
				continue
			}
			b := pf.boxes[bi]
			if err := copyRange(b.Offset, b.Size); err != nil {
				return err
			}
		}
	}
	// The file may end in a skipped zero run; fix the final length.
	return w.Truncate(total)
}

// isZero reports whether p is all zero bytes.
func isZero(p []byte) bool {
	for _, b := range p {
		if b != 0 {
			return false
		}
	}
	return true
}
