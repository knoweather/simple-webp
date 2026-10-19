# simple-webp · WebAssembly decoder (read only)

A small WebAssembly module that decodes WebP into RGBA pixels **in the
browser**, with zero C dependencies and no pixel re-encoding. Encoding is
intentionally out of scope — this is a decoder only.

## Build

From the repository root:

```bash
# Windows (PowerShell)
pwsh wasm/build.ps1

# Linux / macOS
./wasm/build.sh
```

This produces `wasm/example/main.wasm` and copies the Go runtime glue
(`wasm_exec.js`) next to it.

To serve the demo locally (a server is required — `file://` blocks WASM
fetching):

```bash
cd wasm/example
python3 -m http.server 8080
# open http://localhost:8080/
```

## JavaScript API

Once `main.wasm` is instantiated with `wasm_exec.js`'s `Go` runtime, three
functions are installed on the global object by `wasm.Register()`:

```js
// bytes: Uint8Array or ArrayBuffer
const res = webpDecode(bytes);
// success: { width, height, hasAlpha, hasAnimation, rgba }
//   rgba is a Uint8ClampedArray-ready Uint8Array, straight alpha, row-major,
//   length === width * height * 4. Animated files decode to the first frame.
// failure: { error: "..." }

const frames = webpDecodeFrames(bytes);
// success: [ { width, height, duration, rgba }, ... ]
//   every frame of an animation, at canvas size; a still image yields one
//   frame with duration 0. duration is the display time in milliseconds.
// failure: { error: "..." }

const info = webpInfo(bytes);
// { width, height, hasAlpha, hasAnimation, hasICC, hasEXIF, hasXMP, frameCount }
//   (no pixel decode). frameCount is 0 for still images.
```

`rgba` is a zero-copy view over Go-managed memory. Use it synchronously — e.g.
`new ImageData(new Uint8ClampedArray(res.rgba), res.width, res.height)` —
and do not retain the reference across further wasm calls, as the backing
memory may be reused.

`webpInfo` reads container metadata, including the **VP8X extended-format
flags** (`hasICC` / `hasEXIF` / `hasXMP`) and the animation `frameCount`,
without touching pixels — handy for deciding whether to call `webpDecodeFrames`.

## Package layout

| File | Purpose |
|---|---|
| `wasm/api.go` | framework-independent decode logic (`Decode`, `DecodeFrames`, `Info`) — unit tested on the host |
| `wasm/wasm.go` | `//go:build js && wasm` `syscall/js` bindings + `Register()` |
| `wasm/main/main.go` | `//go:build js && wasm` entry point (build target) |
| `wasm/api_test.go` | host Go tests (no browser needed) |
| `wasm/example/index.html` | browser demo |
| `wasm/build.ps1` / `wasm/build.sh` | build helpers |
