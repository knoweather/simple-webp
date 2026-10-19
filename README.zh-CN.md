# simple-webp

> [!NOTE]
> 🚀 **首次发版，灰度测试中。** 有任何问题或建议，欢迎提 [issue](https://github.com/knoweather/simple-webp/issues)！

[English](./README.md) | **简体中文**

纯 Go 的 WebP 解析 / 解码 / 编码库——无 cgo、无第三方运行时依赖。容器解析、
VP8 有损解码、VP8L 无损解码+编码、ALPH 透明通道、动画合成，全部自己实现，
输出标准 `image.Image`。

架构与 [simple-png](https://github.com/knoweather/simple-png) 保持一致：
chunk 化、原始字节保留、可字节级回写。

## 支持范围

| 能力 | 状态 | 说明 |
|---|---|---|
| RIFF/WEBP 容器 | ✅ | `VP8 `/`VP8L`/`VP8X`/`ALPH`/`ANIM`/`ANMF`/`ICCP`/`EXIF`/`XMP `，含 padding |
| VP8L 无损解码 | ✅ | 4 种变换（predictor/color/subtract-green/color-indexing）、meta-Huffman、LZ77、color cache |
| VP8 有损解码 | ✅ | bool 解码器、关键帧头、分区、系数树、16x16/8x8/4x4 预测、WHT+IDCT、loop filter |
| ALPH 透明通道 | ✅ | 未压缩（3 种行滤波）与 VP8L 压缩两种 alpha |
| 动画 | ✅ | `ANIM`/`ANMF`，dispose/blend 合成，返回画布尺寸帧序列 |
| image 接口 | ✅ | `ToImage()`、`*Webp` 实现 `image.Image`、`image.RegisterFormat("webp", ...)` |
| 元数据编辑 | ✅ | 追加 `ICCP`/`EXIF`/`XMP ` 后回写，**不重编码像素**，未改动时字节级还原 |
| VP8L 无损编码 | ✅ | 色彩索引 / subtract-green / predictor / LZ77 / Huffman（≤15 位） |
| VP8 有损编码 | ❌ | 不在范围内（需要模式决策、RD 优化、码率控制） |

## 安装

```bash
go get github.com/knoweather/simple-webp
```

## 用法

### 解析 + 解码

```go
f, _ := os.Open("a.webp")
w, err := simple_webp.ParseWebp(f)
if err != nil {
    panic(err)
}
log.Println(w.Width, w.Height, w.HasAlpha, w.HasAnimation, len(w.ANMFs))

img, err := w.ToImage()   // image.Image（*image.NRGBA）
```

### 标准库直读

`init()` 里注册了格式，`image.Decode` 直接可用：

```go
img, format, err := image.Decode(r)   // format == "webp"
cfg, err := image.DecodeConfig(r)     // 只读头，不碰像素
```

### 直接当 image.Image 用

```go
var img image.Image = w            // Bounds / ColorModel / At（懒解码并缓存）
draw.Draw(dst, dst.Bounds(), w, image.Point{}, draw.Src)
```

### 改元数据、无损回写

```go
w.XMP = &simple_webp.XMP{Data: []byte(xmpPacket)}
w.DeleteRawChunk("EXIF")

var buf bytes.Buffer
_, err := w.WriteTo(&buf)   // 未改动的 chunk 逐字节复制，像素数据不重编码
```

### 动画

```go
frames, durations, err := w.Frames(0)   // 0 = 默认像素预算（1<<27）
for i, f := range frames {
    log.Println(i, durations[i], f.Bounds())
}
```

### 编码

```go
err := simple_webp.Encode(out, img, &simple_webp.EncodeOptions{
    Lossless: true,
    Method:   2, // 0 仅字面量 / 1 +色彩索引+LZ77 / 2 +predictor
})
```

### 自定义 chunk

与 simple-png 相同的扩展点：

```go
type myChunk struct{ Data []byte }

func (c *myChunk) ChunkName() simple_webp.FourCC { return simple_webp.FourCC("ABCD") }
func (c *myChunk) Parse(chunk *simple_webp.RawChunk) error { c.Data = chunk.Data; return nil }
```

---

## 目录结构

```
simple-webp/
├── webp.go        Webp 结构体 / ParseWebp / chunk 消费与原序快照
├── riff.go        RIFF 原语：FourCC、小端长度、padding、读写
├── chunk.go       VP8X / VP8 / VP8L / ALPH / ANIM / ANMF / ICCP / EXIF / XMP
├── writer.go      WriteTo / MarshalBytes（原序回写，重算长度与 padding）
├── decode.go      ToImage / ToImageWith / 有损无损调度
├── alpha.go       ALPH 解压、alpha 行滤波逆变换、合成
├── animation.go   ANMF 帧合成（blend / dispose）
├── vp8.go         VP8 → NRGBA（9:3:3:1 fancy upsampling + BT.601 定点转换）
├── image.go       image.RegisterFormat / Decode / DecodeConfig / image.Image
├── encode.go      image.Image → VP8L → RIFF
├── internal/vp8/  VP8 有损解码器（bool 解码、预测、IDCT、loop filter）
├── internal/vp8l/ VP8L 无损解码器 + 编码器（位流、Huffman、LZ77、变换）
└── testdata/      与 libwebp 对照的 fixture（.webp + .rgba 基准）
```

---

## 正确性

fixture 全部由 libwebp（Pillow）生成，逐像素对照：

- 无损：渐变、随机噪声、调色板、文字、alpha、棋盘、1×1、320×240
- 有损：平滑、锐边、噪声、带 alpha
- 真实数据：6778×6028 卫星图、400×300 照片裁切
- 动画：3 帧

结论：

- **VP8 / VP8L 解码的 YUV / ARGB 平面与参考实现逐字节一致**（含 loop filter、动画合成）
- **最终 RGBA 输出与 libwebp 逐像素一致**（含 fancy upsampling 与 BT.601 定点转换）
- 编码产物可被 libwebp 正确解码，且与源图逐像素一致

---

## Benchmark

Windows / amd64，Ryzen 7 7840HS，`go test -bench . -benchtime 1s`：

| 场景 | simple-webp | 参考实现 (x/image) | libwebp 1.5.0 (C, CT) |
|---|---|---|---|
| 容器解析（320×240，37 KB） | **8.6 µs**（4.3 GB/s） | — | — |
| VP8L 无损解码（320×240） | 3.45 ms（22.3 MP/s） | 2.86 ms（26.9 MP/s） | 快 1.3–3.4× |
| VP8 有损解码（400×300，仅 YUV） | **1.02 ms**（117.8 MP/s） | 1.08 ms（111.4 MP/s） | —（不单独拆 YUV） |
| VP8 有损解码 + RGB 转换（400×300） | 4.52 ms（优化前）→ **1.42 ms**（优化后，CT） | 不支持 | 0.208 ms（单线程约慢 6.8×、10 核约慢 5.2×） |

> libwebp 列来自另一台 CT（concast-go）的单线程基准（libwebp 1.5.0 的
> `WebPDecodeRGBA`），与上方 Windows 表**不是同一台机器**，仅作量级对照。
> 有损解码已做 YUV→RGB 查表 + 16 行带并发优化，故「优化后」数字较原值更低。

无损解码略慢于参考实现（Huffman 逐位解码，尚未加查表快路径）；VP8 核心解码略快。
编码速度约 4 MP/s，体积约为 `cwebp -lossless -m 6` 的 1.5～2 倍（未做 meta-Huffman 与
color transform）。

---

## 已知限制

- **有损编码**未实现，`EncodeOptions.Lossless` 被接受但忽略（只出 VP8L 无损）。
- 编码器未实现 color transform 与 meta-Huffman 分块，压缩率低于 `cwebp`。
- `ALPH` 的 `preprocessing=1`（level reduction）按原值输出，未做参考实现的抖动还原；
  cwebp 默认不产生该模式。
- 无损解码的 Huffman 是逐位解码，大图比参考实现慢约 20%。
- 动画只读不写。
- **WebP 不含 VP9**：WebP 的两种编码是 VP8（有损，`VP8` chunk）与 VP8L（无损，`VP8L` chunk）。
  VP9 是另一种编码，只用在 WebM/Matroska 容器里，与本库无关。

## 安全 / 像素预算

为防止「解压炸弹」（`testdata` 里一个几十字节、却声明 16383×16383 的恶意头），
所有解码路径在**读取宽高之后、分配像素缓冲之前**按像素预算预检：

- `DecodeOptions.MaxPixels`（默认 `1<<27` ≈ 1.34 亿像素，约 512 MB ARGB）会透传到
  `vp8.DecodeLimit` / `vp8l.DecodeARGBLimit`；超过预算返回 `ErrTooLarge` / `ErrImageTooLarge`。
- 单 chunk 体积由 `maxChunkSize` 兜底。
- 已用 `TestToImageWithDefaultBudget` 单测 + 单 worker fuzz 验证：最大声明头被零分配拒绝，
  不会触发 OOM。

调用方可按需收紧预算，例如只信任小图：

```go
img, err := w.ToImageWith(simple_webp.DecodeOptions{MaxPixels: 4096 * 4096})
```

---

## WebAssembly（只读解码）

`wasm/` 提供 WebAssembly **仅解码**前端：把 WebP 字节流转成 RGBA 像素，
可直接画到 `<canvas>` 上——不重编码、不做写入，编码不在范围内。

- 构建：`wasm/build.ps1`（Windows）/ `wasm/build.sh`；产物为
  `wasm/example/main.wasm` 与复制好的 `wasm_exec.js`。
- JS API（由 `wasm.Register()` 安装到全局对象）：
  - `webpDecode(bytes) -> { width, height, hasAlpha, hasAnimation, rgba }`
    （失败返回 `{ error }`）
  - `webpInfo(bytes)   -> { width, height, hasAlpha, hasAnimation }`（不解码像素）
  - `bytes` 可传 `Uint8Array` 或 `ArrayBuffer`；`rgba` 为直通 alpha 的
    `Uint8Array`，须同步消费（Go 托管内存）。
- 完整示例：`wasm/example/index.html` 与 `wasm/README.md`。

---

## 测试

```bash
go test ./...                 # 单元测试 + fixture 逐像素对照
go test -run XXX -bench .     # benchmark
go test -fuzz FuzzParseWebp   # fuzz（目标：不 panic、不 OOM）
```

`internal/vp8` 的平面级测试与 `bench_test.go` 用 `golang.org/x/image` 作为对照 oracle，
它只是测试依赖，不进入运行时。

---

## 参考

- [WebP Container Specification](https://developers.google.com/speed/webp/docs/riff_container)
- [WebP Lossless Bitstream Specification](https://developers.google.com/speed/webp/docs/webp_lossless_bitstream_specification)
- [RFC 6386 - VP8 Data Format and Decoding Guide](https://www.rfc-editor.org/rfc/rfc6386)
- 常量表（系数概率、预测模式概率、反量化表）来自 RFC 6386 / WebP 规范；
  实现与 libwebp 的行为对齐（fancy upsampling、BT.601 定点系数、边界像素约定）。

---

## 鸣谢

感谢 **[腾讯 WorkBuddy](https://cloud.tencent.com/product/workbuddy)**
提供的免费额度的 **Step 5 Preview** 模型支持本项目完成。
