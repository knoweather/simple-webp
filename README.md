# simple-webp

Pure-Go **WebP decode + encode** — VP8 lossy, VP8L lossless, ALPH, animation —
plus a **WebAssembly (WASM)** decoder for the browser and Node.js.
No cgo, no libwebp: the codec is written from scratch and returns standard
`image.Image` (registered with `image.RegisterFormat`), and the only third-party
module is `github.com/pkg/errors` for error wrapping.

<p>
<a href="https://github.com/knoweather/simple-webp/actions/workflows/ci.yml"><img src="https://github.com/knoweather/simple-webp/actions/workflows/ci.yml/badge.svg?branch=main" alt="CI"></a>
<a href="https://goreportcard.com/report/github.com/knoweather/simple-webp"><img src="https://goreportcard.com/badge/github.com/knoweather/simple-webp" alt="Go Report Card"></a>
<a href="https://scorecard.dev/viewer/?uri=github.com/knoweather/simple-webp"><img src="https://img.shields.io/ossf-scorecard/github.com/knoweather/simple-webp?label=openssf%20scorecard" alt="OpenSSF Scorecard"></a>
<a href="https://pkg.go.dev/github.com/knoweather/simple-webp"><img src="https://pkg.go.dev/badge/github.com/knoweather/simple-webp.svg" alt="Go Reference"></a>
<a href="go.mod"><img src="https://img.shields.io/github/go-mod/go-version/knoweather/simple-webp" alt="Go version"></a>
<a href="LICENSE"><img src="https://img.shields.io/github/license/knoweather/simple-webp" alt="License"></a>
<a href="https://github.com/knoweather/simple-webp/tags"><img src="https://img.shields.io/github/v/tag/knoweather/simple-webp?sort=semver" alt="Latest tag"></a>
<img src="https://img.shields.io/badge/CGO-disabled-success" alt="CGO disabled">
<img src="https://img.shields.io/badge/WASM-browser%20%26%20node-blueviolet" alt="WASM">
</p>

**English** | [简体中文](./README.zh-CN.md)

```go
img, err := webp.Decode(r)          // VP8 lossy, VP8L lossless, ALPH
if err != nil {
    return err
}

var out bytes.Buffer
if err := webp.Encode(&out, img, &webp.EncodeOptions{}); err != nil { // lossless
    return err
}
// lossy instead: webp.EncodeLossy(&out, img, &webp.LossyOptions{Quality: 75})
```

- **Byte-exact rewriting**: parse → edit (`SetImage` / `AddFrame` / `AddRawChunk` /
  `DeleteRawChunk`) → write; unknown chunks and untouched pixels survive unchanged,
  metadata chunks (ICCP/EXIF/XMP) are carried through and never re-encoded.
- **Complete codec**: VP8/VP8L/ALPH/animation decoding plus an encoder with loop
  filter, target-size rate control, per-segment quantization, color transform,
  color cache, meta-Huffman groups and animation output.
- **WebAssembly (WASM)**: the decoder compiled to WASM for the browser and
  Node.js — decode WebP to RGBA for `<canvas>` with no libwebp and no cgo
  (`wasm/`).
- **Strict when you want it**: `ParseOptions.Strict` enforces the container spec's
  MUST rules (chunk order, VP8X first, reserved bits, canvas and frame geometry,
  flags matching the payload); the default stays lenient.

> [!NOTE]
> 🚀 **Initial release, canary testing in progress.** If you hit any problem, please open an [issue](https://github.com/knoweather/simple-webp/issues)!

`webpDecoder` mirrors the architecture of
[simple-png](https://github.com/knoweather/simple-png): chunked, raw bytes
preserved, byte-exact write back.

## Supported

| Capability | Status | Notes |
|---|---|---|
| RIFF/WEBP container | ✅ | `VP8 `/`VP8L`/`VP8X`/`ALPH`/`ANIM`/`ANMF`/`ICCP`/`EXIF`/`XMP `, incl. padding |
| Strict parsing | ✅ | `ParseOptions.Strict` checks the spec MUST rules (chunk order, VP8X first, VP8X/ALPH/ANMF reserved bits, canvas area, frame inside canvas, exactly one image chunk, VP8X flags matching the data, no ALPH on VP8L, zero padding); the default stays lenient |
| VP8L lossless decode | ✅ | 4 transforms (predictor/color/subtract-green/color-indexing), meta-Huffman, LZ77, color cache |
| VP8 lossy decode | ✅ | bool decoder, keyframe header, partitions, coefficient trees, 16x16/8x8/4x4 prediction, WHT+IDCT, loop filter |
| ALPH transparency | ✅ | uncompressed (3 row filters) and VP8L-compressed alpha |
| Animation | ✅ | `ANIM`/`ANMF`, dispose/blend compositing, canvas-sized frame sequence, `FrameAt` decodes one frame without cloning the others |
| image interface | ✅ | `ToImage()`, `*Webp` implements `image.Image`, `image.RegisterFormat("webp", ...)`, `PixelFormatYCbCr` for lossy stills, `DecodeWebp` decodes straight from an `io.Reader`, `DecodeInto` reuses a caller buffer |
| Metadata editing | ✅ | rewrite with `ICCP`/`EXIF`/`XMP ` chunks, **pixels never re-encoded**, byte-exact when untouched; new chunks land where the spec puts their type |
| Container editing | ✅ | `NewWebp` + `SetImage` build a file from scratch, `AddFrame`/`RemoveFrame` edit animations, `DeleteRawChunk` removes consumed chunks, `InvalidateCache` drops the decode cache |
| VP8L lossless encode | ✅ | color-indexing / subtract-green / predictor / **color transform** / **color cache** / **meta-Huffman groups** / LZ77 / Huffman (≤15 bits); the cheaper of the variants is kept |
| VP8 lossy encode | ✅ | intra only: rate-distortion mode decision, 16x16 or 4x4 luma modes, 8x8 chroma modes, **loop filter**, **target-size rate control**, **per-segment quantization** (`LossyOptions.Segment`); lossy + alpha (`VP8X`+`ALPH`) |
| Animation encode | ✅ | `EncodeAnimation` writes `VP8X`+`ANIM`+`ANMF`, lossless or lossy frames, per-frame `ALPH` |

## Install

```bash
go get github.com/knoweather/simple-webp
```

## Usage

### Parse + decode

```go
f, _ := os.Open("a.webp")
w, err := simple_webp.ParseWebp(f)
if err != nil {
    panic(err)
}
log.Println(w.Width, w.Height, w.HasAlpha, w.HasAnimation, len(w.ANMFs))

img, err := w.ToImage() // image.Image (*image.NRGBA)
```

### Standard library

The format is registered in `init()`, so `image.Decode` works out of the box:

```go
img, format, err := image.Decode(r)  // format == "webp"
cfg, err := image.DecodeConfig(r)    // header only, no pixels touched
```

### Use directly as an image.Image

```go
var img image.Image = w // Bounds / ColorModel / At (lazy decode, cached)
draw.Draw(dst, dst.Bounds(), w, image.Point{}, draw.Src)
```

### Edit metadata, lossless write back

```go
w.XMP = &simple_webp.XMP{Data: []byte(xmpPacket)}
w.DeleteRawChunk("EXIF")

var buf bytes.Buffer
_, err := w.WriteTo(&buf) // untouched chunks are copied byte for byte, pixels not re-encoded
```

New chunks go where the container spec puts their type: `VP8X` first, `ICCP`
right after it and before `ANIM`, `ANMF` and the image data, `ANIM` before the
first `ANMF`, `ALPH` before `VP8 `/`VP8L`, and `EXIF`, `XMP` or unknown types at
the end. A single-instance chunk that is already present is refused (a second
copy would make the file invalid), and so is a chunk combination the spec
forbids — `VP8` next to `VP8L`, `ALPH` next to `VP8L`. `DeleteRawChunk` also
removes the chunks parsing already consumed (`ALPH`, `ANIM`, `ANMF`, `VP8X`,
`VP8`, `VP8L`) and clears the matching `VP8X` flags. A removal that would leave
the file invalid — the only image data, `ANIM` while frames remain, `VP8X`
while an extended feature remains — is refused: nothing is deleted and the call
returns 0. Unknown sub-chunks inside an `ANMF` frame keep their original
position, so frame-level application data survives a parse/write cycle. An
`ICCP`/`EXIF`/`XMP`/`ALPH`/`ANIM` assigned directly to its struct is written
even when the source file had none; `WriteTo` returns an error for an odd frame
position, a duration beyond 24 bits, an `ANIM` without frames or an `ALPH` next
to a `VP8L`.

### Decode straight from a stream

```go
img, err := simple_webp.DecodeWebp(resp.Body, simple_webp.DecodeOptions{})
```

`DecodeWebp` parses and decodes from any `io.Reader` — a file, a network
response or a `bytes.Reader` — streaming the container in chunk by chunk, so the
caller never has to hold the whole file in memory first. `(*Webp).DecodeInto`
decodes one still image into an `*image.NRGBA` you already own, which saves the
result allocation when a pipeline decodes image after image; a size mismatch,
a nil buffer or an animation is an error and leaves the buffer untouched.

### Strict parsing

```go
w, err := simple_webp.ParseWebpWith(r, simple_webp.ParseOptions{Strict: true, VerifySize: true})
```

`Strict` enables the spec MUST checks and reports the first violation; the
default (and `ParseWebp`) stays lenient so real world files that break the rules
still parse. The checks cover the order of the reconstructing chunks, the
reserved bits of `VP8X`, `ALPH` and `ANMF`, the canvas area, the frame
rectangles, the "exactly one image chunk" rule and the agreement between the
`VP8X` feature flags and the data in the file.

Two things are deliberately *not* rejected, because the spec allows them:
metadata and unknown chunks may appear out of order, and an `ANIM` chunk
without the animation flag must be ignored rather than treated as an error.

### Decode to YCbCr

```go
img, err := w.ToImageWith(simple_webp.DecodeOptions{Format: simple_webp.PixelFormatYCbCr})
```

Lossy stills hand back the decoder's `*image.YCbCr` without the YUV to RGB
conversion. Lossless bitstreams, alpha planes and animations have no YCbCr form
and return `ErrUnsupported`.

### Encode with metadata

```go
err := simple_webp.Encode(out, img, &simple_webp.EncodeOptions{
    ICCP: iccProfile,
    EXIF: exifPacket,
    XMP:  xmpPacket,
})
```

The metadata turns into a flagged `VP8X` plus the chunks in container order; an
encode without metadata stays byte identical to before. `LossyOptions` carries
the same three fields.

### Lossy + alpha, and animation

```go
err := simple_webp.EncodeLossy(out, img, &simple_webp.LossyOptions{
    Quality:      80,
    FilterLevel:  20,              // VP8 loop filter 0..63; 0 derives it from the quality
    TargetBytes:  0,               // >0: pick the quality closest to a size
    AlphaFilter:  simple_webp.ALPHFilterGradient,
    Segment:      true,            // per-segment quantisation, can only shrink the file
})
```

A transparent input is written as `VP8X`(alpha) + `ALPH` + `VP8 `; the alpha plane
is lossless. Animations are written from a canvas and a frame list:

```go
err := simple_webp.EncodeAnimation(out, &simple_webp.Animation{
    CanvasWidth: 320, CanvasHeight: 240,
    LoopCount: 0,
    Frames: []simple_webp.Frame{{Image: frame, Duration: 100, Blend: true}},
}, &simple_webp.AnimationOptions{Lossy: true})
```

### Build or edit a container

```go
w := simple_webp.NewWebp(0, 0)
w.SetImage(img, &simple_webp.ImageOptions{Lossy: true})
_ = w.AddFrame(frame)          // animations: append a frame
_ = w.RemoveFrame(0)           // …and drop one
w.InvalidateCache()            // after mutating parsed structs directly
out, err := w.MarshalBytes()
```

### Animation

```go
frames, durations, err := w.Frames(0) // 0 = default pixel budget (1<<27)
for i, f := range frames {
    log.Println(i, durations[i], f.Bounds())
}
```

`Frames` rejects a canvas larger than the pixel budget with `ErrImageTooLarge`,
and because it returns every frame at once it also bounds `canvas pixels ×
frames` through `FrameOptions.MaxTotalPixels`; `FramesWith(FrameOptions{...})`
sets both, and the canvas is checked before any pixel buffer is allocated.

### Encode

```go
err := simple_webp.Encode(out, img, &simple_webp.EncodeOptions{
    Lossless: true,
    Method:   2, // 0 literals only / 1 +color-indexing+LZ77 / 2 +predictor
})
```

Lossy output goes through the VP8 encoder:

```go
err := simple_webp.EncodeLossy(out, img, &simple_webp.LossyOptions{
    Quality: 80, // 0 (smallest) .. 100 (best); 0 means the default, 75
})
```

A transparent input makes the lossy encoder write the extended format
(`VP8X` with the alpha flag, `ALPH`, `VP8 `) with a lossless alpha plane; use
`EncodeBytes` when you want lossless pixels instead.

The lossy encoder chooses its modes by rate distortion. Every candidate mode
is predicted, transformed and quantized, and the candidates are scored on the
distortion of the reconstruction plus lambda times the number of bits their
tokens take; the cheapest one wins. The luma of a macroblock is either one
16×16 mode or sixteen 4×4 modes, whichever comes out cheaper, and the chroma
uses one of the four 8×8 modes. On a set of five test images the size at
matched PSNR drops by 6.7% against plain SAD mode selection (a 320×240 photo
−3%, a smooth gradient −14%, a sharp synthetic image −38%, noise −0%). The
search costs about 3.5x the encoding time: 18 ms instead of 5 ms for a
320×240 photo.

### Custom chunks

The same extension point as simple-png:

```go
type myChunk struct{ Data []byte }

func (c *myChunk) ChunkName() simple_webp.FourCC { return simple_webp.FourCC("ABCD") }
func (c *myChunk) Parse(chunk *simple_webp.RawChunk) error { c.Data = chunk.Data; return nil }
```

## WebAssembly (WASM): decode WebP in the browser and Node

`wasm/` is a **decoder only** WebAssembly (WASM) build: a WebP decoder for the
browser and Node.js that turns a WebP byte stream into RGBA pixels ready for
`<canvas>`, with no libwebp, no cgo and no re-encoding. Encoding is out of
scope.

- Build: `wasm/build.ps1` (Windows) or `wasm/build.sh`; output is
  `wasm/example/main.wasm` plus a copied `wasm_exec.js`.
- JS API (installed on the global object by `wasm.Register()`):
  - `webpDecode(bytes) -> { width, height, hasAlpha, hasAnimation, rgba }`
    (returns `{ error }` on failure; first frame for animations)
  - `webpDecodeFrames(bytes) -> [ { width, height, duration, rgba }, … ]`
    (all animation frames at canvas size; a still image yields one frame)
  - `webpInfo(bytes) -> { width, height, hasAlpha, hasAnimation, hasICC,
    hasEXIF, hasXMP, frameCount }` (no pixel decode; VP8X flags + frame count)
  - `bytes` may be a `Uint8Array` or `ArrayBuffer`; `rgba` is a straight-alpha
    `Uint8Array` that must be consumed synchronously (Go-managed memory).
- Decoding covers the full extended format: VP8X alpha (lossless and lossy
  `VP8X+ALPH`), animation (`VP8X+ANIM+ANMF`), and the ICC/EXIF/XMP metadata
  flags. Full demo: `wasm/example/index.html` and `wasm/README.md`.

## Layout

```
webp.go        Webp struct / ParseWebp / chunk consumption & in-order snapshot
riff.go        RIFF primitives: FourCC, little-endian lengths, padding, R/W
chunk.go       VP8X / VP8 / VP8L / ALPH / ANIM / ANMF / ICCP / EXIF / XMP
strict.go      spec MUST validation behind ParseOptions.Strict
edit.go        NewWebp / SetImage / AddFrame / RemoveFrame / InvalidateCache
writer.go      WriteTo / MarshalBytes (in-order write back, lengths & padding recomputed)
container_write.go  RIFF assembly shared by the encoders
decode.go      ToImage / ToImageWith / lossy & lossless dispatch / YCbCr output
alpha.go       ALPH inflate, alpha row-filter inverse transform, compositing
alpha_encode.go  ALPH payload: filtering and VP8L compression of alpha
animation.go   ANMF frame compositing (blend / dispose), FrameAt / AnimationInfo
animation_encode.go  animation writer (VP8X + ANIM + ANMF)
vp8.go         VP8 → NRGBA (9:3:3:1 fancy upsampling + fixed-point BT.601)
image.go       image.RegisterFormat / Decode / DecodeConfig / image.Image
encode.go      image.Image → VP8L → RIFF
encode_lossy.go  image.Image → VP8 (+ ALPH) → RIFF
internal/vp8/  VP8 lossy decoder + encoder (bool coder, prediction, IDCT, loop filter, RD mode decision, rate control, segment quantisation)
internal/vp8l/ VP8L lossless decoder + encoder (bitstream, Huffman, LZ77, transforms, colour cache, colour transform, meta-Huffman)
testdata/      fixtures generated against libwebp (.webp + .rgba references)
```

## Correctness

Fixtures are all generated by libwebp (Pillow) and compared pixel by pixel:

- lossless: gradients, random noise, palette, text, alpha, checkerboard, 1×1, 320×240
- lossy: smooth, sharp edges, noise, with alpha
- real data: 6778×6028 satellite image, 400×300 photo crop
- animation: 3 frames

**VP8/VP8L decode produces YUV/ARGB planes byte-identical to the reference
implementation** (loop filter and animation compositing included), and the
**final RGBA output is pixel-identical to libwebp** (fancy upsampling and
fixed-point BT.601 included). Encoded files are checked by decoding them with
this library and by the strict container validation; a fresh libwebp cross-check
was not possible in the environment this series was developed in, so it is not
claimed here.

## Benchmark

Windows/amd64, `go test -run XXX -bench . -benchtime 200ms`, re-measured after
the container and encoder work in this series:

| Scenario | simple-webp | reference (x/image) |
|---|---|---|
| container parse (320×240, 37 KB) | **17.3 µs** (2.2 GB/s) | — |
| VP8L lossless decode (320×240, 320×240 photo) | 2.15 ms (35.7 MP/s) | 1.61 ms (47.6 MP/s) |
| VP8 lossy decode (400×300) | **1.19 ms** (101 MP/s) | 0.67 ms (179 MP/s) |
| VP8L lossless encode (320×240) | 35.6 ms (2.16 MP/s) | not supported |

> The numbers above come from one run on the machine used for this series and
> are rounded; earlier libwebp comparisons were measured on a different machine
> and are not reproduced here because `cwebp`/`dwebp` are not available in this
> environment.

Lossless decoding is slower than the reference (bit-by-bit Huffman, no
table-driven fast path yet); core VP8 decoding is slower after the loop filter
was added on the encode side, which does not change the decoder. Lossless
encoding now tries the colour-transform, colour-cache and meta-Huffman variants
and keeps the shortest stream, so it is slower than before and its output is
smaller; the size ratio against `cwebp` was not re-measured here.

## Known limitations

- `EncodeOptions.Lossless` is accepted but ignored: `Encode` always writes
  lossless (VP8L) and `EncodeLossy` is the lossy path. The flag cannot be honoured
  as it stands because its zero value is `false`, so every caller that never set
  it would silently switch to lossy
- **Lossy encoding is intra only.** The VP8 encoder picks its modes by rate
  distortion: every candidate goes through prediction, the forward transform
  and quantisation, and the candidates are scored on the quantised distortion
  plus the exact number of bits their tokens would take. The luma uses either
  one 16x16 mode or sixteen 4x4 modes, whichever is cheaper and the chroma uses
  8x8 modes. The loop filter and a target-size rate control are in; the rate
  control moves the global quantiser only, and `LossyOptions.Segment` adds
  per-segment steps on top (opt-in, and the encoder races the segmented and
  unsegmented plans so it can only shrink a file), so the size at a given
  quality still depends on the content. The quality to quantiser curve itself
  follows libwebp's, so a given `Quality` selects about the same step as the
  matching `cwebp -q`
- Lossy encoding runs twice: the first pass collects token statistics and the
  second encodes with probabilities adapted to them, which costs about 1.8x the
  time of a single pass. The mode search runs only in the first pass, the
  second reuses the modes it found. With `Segment` on, the encoder races three
  plans (unsegmented, regular step, double step), so it costs up to three
  times that again
- The rate distortion search is what makes the encoder slow: scoring ten 4x4
  modes for every block of every macroblock means running the transform and
  the quantiser once per candidate
- Lossless encoding now encodes up to four variants (colour transform on or off,
  colour cache, meta-Huffman groups) and keeps the shortest stream, so it costs
  several times the old single-pass encode; the container tests only assert the
  decoded pixels, not the exact bytes
- The meta-Huffman implementation uses two groups of 16×16 blocks clustered by
  their symbol histograms. It wins on content whose regions use different symbol
  sets, but with the Method 2 predictor a single tree set usually wins, in which
  case the stream is left unchanged
- `ALPH` `preprocessing=1` (level reduction) is passed through as-is, without
  the reference dithering; cwebp does not produce that mode by default. The
  encoder writes `preprocessing=0` and does not quantise alpha at all
- Lossless Huffman decodes bit by bit — large images slower than the reference
- `DeleteRawChunk` reports how many chunks it removed but not why a removal was
  refused: 0 means either no chunk of that type was present or the removal would
  have left the file invalid
- `DeleteRawChunk` cannot report an error because its signature returns an int;
  a refused removal therefore looks like "nothing removed"
- **WebP contains no VP9**: WebP's two codecs are VP8 (lossy, `VP8 ` chunk)
  and VP8L (lossless, `VP8L` chunk). VP9 is a different codec used in
  WebM/Matroska containers and unrelated to this library

## Roadmap

The lossy encoder is intra only by design, and the WebP bitstreams themselves are
conformant: the VP8 and VP8L encoders produce streams that an independent
decoder (`golang.org/x/image`, a port of libwebp) reproduces sample for sample.
What is left between this library and `cwebp` is listed below, ordered by
"how much it costs to fix" versus "how much it buys".

The quality to quantiser curve now follows libwebp's — a piecewise linear map
followed by a cube root — so a given `Quality` buys roughly the same step as the
matching `cwebp -q`. On a smooth 320×240 test image the same quality setting now
reaches about 1.5–3.7 dB more PSNR than the old linear rule did; both paths meet
at quality 100.

`LossyOptions.Segment` adds per-segment quantization on top: the frame is split
into four segments by macroblock activity and the flat side is quantised one or
two steps coarser while busy regions keep the base step. The encoder races the
unsegmented, regular-step and double-step plans and keeps the smallest stream,
so the option can only shrink a file, never grow it. The gain is content
dependent — flat regions are skipped outright by this encoder and gain nothing,
so the win shows on low amplitude texture (about 5 % on the noise fixture,
nothing on pure gradients).

| # | Item | Where it stands | Plan |
|---|---|---|---|
| 1 | **Cost model for token probability updates** | `adaptTokenProb` uses a flat 12 bit gain threshold and never looks at `tokenUpdateProb`. About 40 % of the table entries carry an update probability of 255, which RFC 6386 §13.5 marks as "never updated"; signalling those costs ~16 bits, not the ~9 the model assumes, so an update can end up making the stream larger | Price the signalling per entry, as libwebp does in `enc/frame_enc.c` |
| 2 | **First partition size overflow** | The 19 bit first-partition length in the frame tag is neither masked nor range checked, so a very large canvas silently corrupts the stream | Return an error when it does not fit, like libwebp's `VP8_ENC_ERROR_PARTITION0_OVERFLOW` |
| 3 | **`LossyOptions.Sharpness`** | `internal/vp8` writes the RFC's 3 bit `sharpness_level` field correctly, but `LossyOptions` has no such field, so the value can never be set from outside the package | Add the field to `LossyOptions` (and fix the comment: like `cwebp -sharpness`, 0 is the sharpest) |
| 4 | **Animation encoder ignores `AlphaFilter`** | The static lossy path honours `LossyOptions.AlphaFilter`; the animation path hardcodes `ALPHFilterNone` | Read `LossyOpt.AlphaFilter` in the animation encoder too |
| 5 | **Animation `Duration` overflow** | The 24 bit field is written with a silent truncation (`1<<24` becomes 0, a negative value wraps) | Validate the range before encoding and return an error |
| 6 | **VP8L colour indexing alpha** | The spec sets every alpha of the index image to 255; the encoder writes 0. Decoding is unaffected because the inverse transform substitutes the whole pixel | Write 255 |
| 7 | **Duplicate VP8L transforms** | The spec allows each transform type once; the decoder accepts repeats, libwebp rejects them as corrupt | Reject duplicate transform types while decoding |
| 8 | **VP8L two dimensional distance codes** | The decoder implements the full 120 entry neighbourhood map, but the encoder always writes a plain linear distance: a one pixel distance costs ~10 bits where a neighbourhood code costs 1. This is the largest single source of the remaining lossless size gap | Add `DistanceToPlaneCode` and the lookup table to the encoder, as in libwebp's `src/enc/vp8l_enc.c` |
| 9 | **More Huffman groups and better clustering** | The meta prefix codes use two groups; libwebp clusters into more groups and also handles the mixed photo/text case that way | Cluster into more groups |
| 10 | **Animation frame differencing** | Every animation frame is encoded in full: there is no frame rectangle cropping and no inter-frame differencing | Add differencing and minimisation, as in libwebp's `WebPAnimEncoder` |
| 11 | **Multi-token partitions** | The frame still uses a single residual partition, which limits decoder parallelism but not size | Emit more than one partition |

A few further libwebp capabilities are private to it and are not part of the
WebP bitstream, so the spec does not ask for them: `-near_lossless` source
pre-filtering, `-alpha_q` lossy alpha, `-sns` spatial noise shaping, `-pass`
multi-pass optimisation, `-partition_limit` and `-mt` multi-threading. On the
decode side the library streams the container from any `io.Reader`
(`DecodeWebp`) and can decode into a caller buffer (`DecodeInto`), but it has
no append-driven incremental decoder (libwebp's `WebPIDecoder`) and no scaled
or cropped decode yet.

## Safety / pixel budget

To guard against decompression bombs (test data includes a few-dozen-byte
malformed file claiming 16383×16383), every decode path checks the pixel
budget **after reading the header and before allocating pixel buffers**:

- `DecodeOptions.MaxPixels` (default `1<<27` ≈ 134M pixels ≈ 512 MB ARGB) is
  forwarded to `vp8.DecodeLimit` / `vp8l.DecodeARGBLimit`; exceeding it
  returns `ErrTooLarge` / `ErrImageTooLarge`
- a chunk payload is streamed into a growing buffer instead of being allocated
  from its declared length, so a tiny file whose only chunk claims 1 GB fails
  without that allocation; a chunk that promises more data than the stream
  holds is an error rather than a silent end of stream
- `ParseOptions.MaxChunkSize` (default `1<<30`) tightens the per-chunk cap for
  untrusted input; the nested sub-chunks inside an `ANMF` are bound by it too
- data after the RIFF File Size is ignored, as the spec allows; `verifySize`
  still rejects a stream whose size field does not match the actual length
- `Frames` checks the VP8X canvas against the budget before allocating it and
  bounds the total `canvas × frames` pixels, so a 16383×16383 canvas carrying a
  1×1 frame is rejected without the allocation
- covered by `TestToImageWithDefaultBudget` and single-worker fuzzing: the
  worst-case bogus header is rejected with zero allocations, no OOM

Tighten the budget yourself, e.g. trust only small images:

```go
img, err := w.ToImageWith(simple_webp.DecodeOptions{MaxPixels: 4096 * 4096})
```

## Testing

```bash
go test ./...                 # unit tests + pixel-by-pixel fixture comparison
go test -run XXX -bench .     # benchmarks
go test -fuzz FuzzParseWebp   # fuzz (goal: no panic, no OOM)
```

The `internal/vp8` plane-level tests and `bench_test.go` use
`golang.org/x/image` as a comparison oracle — test-only, never a runtime
dependency.

## References

- [WebP Container Specification](https://developers.google.com/speed/webp/docs/riff_container)
- [WebP Lossless Bitstream Specification](https://developers.google.com/speed/webp/docs/webp_lossless_bitstream_specification)
- [RFC 6386 - VP8 Data Format and Decoding Guide](https://www.rfc-editor.org/rfc/rfc6386)
- The constant tables (coefficient probabilities, prediction-mode
  probabilities, dequantization tables) come from RFC 6386 / the WebP specs;
  the implementation is behavior-aligned with libwebp (fancy upsampling,
  fixed-point BT.601 coefficients, edge-pixel conventions).

---

## Acknowledgements

Thanks to **[Tencent WorkBuddy](https://cloud.tencent.com/product/workbuddy)**
for the free quota of the **Step 5 Preview** model that made this project
possible.
