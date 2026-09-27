# task26092713 — faststart

Go library (standard library only) that relocates the `moov` box of a
non-fragmented ISO BMFF / MP4 file to the front of the file, so browsers
can start playback before the download finishes. Intended to be called by
a video render pipeline after writing the final file.

## API

- `Layout(src string) (*LayoutInfo, error)` — lists top-level boxes (type,
  offset, header length, total size) and, per track, the chunk table type
  (`stco`/`co64`), chunk count and sample count.
- `Relocate(src, dst string) error` — writes a faststart copy of `src` to
  `dst` (atomically; no partial file on error). If `moov` already precedes
  every `mdat`, the output is byte-identical to the input. Otherwise the
  layout becomes `ftyp` (if any), `moov`, then the remaining top-level
  boxes in their original relative order. Only chunk offset tables (and
  ancestor container sizes) are modified inside `moov`; `stco` tables are
  upgraded to `co64` only when a final offset is ≥ 2^32, with the minimal
  upgrade set. Running `Relocate` on its own output is a no-op.
- `SamplesEqual(a, b string) (bool, error)` — compares two files sample by
  sample using each file's own sample tables.

## Properties

- Handles 32-bit sizes, 64-bit largesize, `size == 0` (last box only),
  `uuid` boxes, multiple `mdat` boxes, `free`/`skip`/unknown boxes,
  `stco`/`co64`, `stsz`/`stz2` (incl. 4-bit fields), multi-run `stsc`.
- Memory grows with the size of `moov` only, never with `mdat`; copies are
  streamed and sparse-zero blocks are preserved as holes.
- Fragmented MP4 (`moof`/`mvex`), chunks outside every `mdat` payload,
  inconsistent sample tables, truncated files and size overflows are
  reported as errors (no panic, no partial output).

## Test

```sh
go test ./...
```

The 4 GiB-crossing test uses a sparse file, so actual disk usage stays far
below 64 MiB.
