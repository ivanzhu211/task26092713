# mp4faststart

Go library (standard library only) that moves the `moov` box of a
non-fragmented MP4 / ISO BMFF file to the front, so browsers can start
playing before the download finishes. Intended to be called by a video
render pipeline after the renderer writes a file with `moov` at the end.

## API

```go
info, err := faststart.Layout("in.mp4")      // top-level boxes + per-track table summary
err  = faststart.Relocate("in.mp4", "out.mp4") // write moov-first copy
eq, err := faststart.SamplesEqual("a.mp4", "b.mp4") // per-track, per-sample byte compare
```

`Relocate` guarantees:

- If `moov` already precedes every `mdat`, the output is byte-identical.
- Otherwise the layout is `ftyp` (if any), `moov`, then the remaining
  top-level boxes in their original relative order. No boxes inserted.
- Non-`moov` boxes are copied byte for byte; inside `moov` only chunk
  offset tables (`stco`/`co64`) and ancestor container size fields change.
  A `size==0` `moov` is rewritten with an explicit length.
- Every chunk offset in the output points at exactly the same bytes as in
  the input.
- An `stco` table is rewritten as `co64` iff some offset is >= 2^32 in the
  final layout (minimal upgrade set, computed to a fixpoint). Existing
  `co64` tables are never downgraded.
- Running `Relocate` on its own output is a byte-identical no-op.
- Memory grows with the `moov` size only; `mdat` payloads are streamed.
  Sparse (hole-punched) inputs stay sparse on disk.
- On error the destination is never left partially written.

Rejected inputs (error, never panic): fragmented files (`moof`/`mvex`),
chunk offsets outside every `mdat` payload, self-contradictory sample
tables (`stsc` vs `stsz`/`stz2`), truncated files, out-of-range sizes.

Supported: 32-bit / 64-bit (`size==1`) / `size==0` box headers, `uuid`
boxes, multiple `mdat` boxes, `free`/`skip`/unknown top-level boxes,
`stco`/`co64` chunk tables, `stsz` (explicit and constant) and `stz2`
(4/8/16-bit fields) sample size tables.

## Tests

```
go test ./...
```

The main test builds a sparse ~4 GiB file (`ftyp | mdat(A) | moov |
mdat(B)` with a largesize `mdat(B)` header) where relocating pushes the
first track's table past 2^32, its upgrade to `co64` pushes the second
track's table over too, and the third stays `stco`. Physical disk usage
stays under 64 MiB.
