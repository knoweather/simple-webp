# simple-webp

> [!NOTE]
> 🚀 **首次发版，灰度测试中。** 有任何问题或建议，欢迎提 [issue](https://github.com/knoweather/simple-webp/issues)！

[English](./README.md) | **简体中文**

纯 Go 的 WebP 解析 / 解码 / 编码库——无 cgo；唯一的第三方模块是 `github.com/pkg/errors`（仅用于错误包裹），其余均为标准库。容器解析、
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
| VP8 有损编码 | ✅ | 仅帧内：率失真模式决策、16x16 或 4×4 亮度模式、8×8 色度模式，整帧单一量化索引，不做环路滤波与码率控制 |

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

新 chunk 按容器规范给该类型规定的位置插入：`VP8X` 最前，`ICCP` 紧随其后、排在
`ANIM`、`ANMF` 与图像数据之前，`ANIM` 在第一个 `ANMF` 之前，`ALPH` 在 `VP8 `/`VP8L`
之前，`EXIF`、`XMP` 与未知类型排在末尾。`DeleteRawChunk` 也能删掉解析时已被消费的
`ALPH`、`ANIM`、`ANMF`、`VP8X`、`VP8`、`VP8L`，并同步清除 `VP8X` 的标志位；会让
文件变成非法流的删除（唯一的图像数据、还有帧时删 `ANIM`、还有扩展特性时删 `VP8X`）
一律不生效：什么都不删，返回 0。`ANMF` 帧内的未知子 chunk 保持原位置，帧级应用
数据经解析、写回后不丢。

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

有损输出走 VP8 编码器：

```go
err := simple_webp.EncodeLossy(out, img, &simple_webp.LossyOptions{
    Quality: 80, // 0（体积最小）.. 100（质量最高）；0 表示取默认值 75
})
```

有损格式只承载亮度与色度，alpha 通道会被丢弃；需要保留透明度时用
`EncodeBytes` 走无损。

有损编码器按率失真选模式：每个候选模式都要预测、变换、量化一遍，再按
「重建失真 + 权衡系数 × 码长」打分，码长取系数 token 实际要花的比特数，取代价
最小的一个。宏块的亮度要么用整块 16×16 模式，要么用十六个 4×4 模式，看哪条路
更便宜；色度用四种 8×8 模式之一。相对只按 SAD 选模，五个测试图在同 PSNR 下的
体积合计下降 6.7%（320×240 照片 −3%、平滑渐变 −14%、锐利合成图 −38%、噪声
−0%）。代价是编码慢约 3.5 倍：320×240 照片从 5 ms 变成 18 ms。

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
├── internal/vp8/  VP8 有损解码器 + 编码器（bool 编解码、预测、IDCT、loop filter、率失真模式决策）
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
| VP8 有损编码（400×300） | 33.2 ms（3.6 MP/s） | 不支持 | — |
| VP8 有损编码（640×480） | 84.4 ms（3.6 MP/s） | 不支持 | — |

> libwebp 列来自另一台 CT（concast-go）的单线程基准（libwebp 1.5.0 的
> `WebPDecodeRGBA`），与上方 Windows 表**不是同一台机器**，仅作量级对照。
> 有损解码已做 YUV→RGB 查表 + 16 行带并发优化，故「优化后」数字较原值更低。

无损解码略慢于参考实现（Huffman 逐位解码，尚未加查表快路径）；VP8 核心解码略快。
编码速度约 4 MP/s，体积约为 `cwebp -lossless -m 6` 的 1.5～2 倍（未做 meta-Huffman 与
color transform）。

---

## 已知限制

- `EncodeOptions.Lossless` 被接受但忽略：`Encode` 只写 VP8L 无损，有损走 `EncodeLossy`。
  该字段不能直接生效，因为它的零值是 `false`——一旦按它分叉，所有没显式设置的调用方
  都会静默变成有损。
- **有损编码仅帧内**：VP8 编码器按率失真选模式——每个候选模式都要走一遍预测、
  正变换与量化，再按「重建失真 + 系数 token 的比特数」打分取最优。亮度用整块
  16×16 或十六个 4×4 模式（取更便宜的一条），色度用 8×8 模式，整帧固定量化
  索引；不做环路滤波，也没有码率控制。输出是任何 VP8 解码器都能解的合法码流，
  但同 PSNR 下体积仍大于 `cwebp` 1.5.0（320×240 照片实测约为其 1.2~1.4 倍，
  该数字测于本次率失真改造之前，改造把它再收窄约 7%），差距来自缺少环路滤波与码率控制。
- 有损编码跑两遍：第一遍收集 token 统计，第二遍用据此调整的概率表编码，耗时约为
  单遍的 1.8 倍。模式搜索只在第一遍做，第二遍直接沿用第一遍选好的模式。
- 率失真搜索是编码变慢的主因：每个宏块的每个 4×4 块都要把十种模式各跑一遍
  变换与量化。320×240 照片的编码耗时从 5 ms 涨到 18 ms，整个编码器约慢 3.5 倍。
- 编码器未实现 color transform 与 meta-Huffman 分块，压缩率低于 `cwebp`。
- `ALPH` 的 `preprocessing=1`（level reduction）按原值输出，未做参考实现的抖动还原；
  cwebp 默认不产生该模式。
- 无损解码的 Huffman 是逐位解码，大图比参考实现慢约 20%。
- 动画只读不写。
- `DeleteRawChunk` 只返回删掉了几个 chunk，不说明为什么没删：返回 0 表示该类型
  根本不存在，或者删除会让文件变成非法流而未被接受。
- **WebP 不含 VP9**：WebP 的两种编码是 VP8（有损，`VP8` chunk）与 VP8L（无损，`VP8L` chunk）。
  VP9 是另一种编码，只用在 WebM/Matroska 容器里，与本库无关。

## 演进方向

有损编码器按设计只做帧内。率失真模式决策与 4×4 帧内模式都已完成，与 `cwebp`
之间的剩余差距在：

- **码率控制**：量化索引整帧固定，输出多大完全取决于图像在该质量下需要多少
  比特。要做目标码率模式，就得逐宏块调整量化器，帧头里的逐段增量正好可以
  承载这件事。
- **环路滤波**：刻意不做。本库的解码器在整帧解完后才滤波，滤波无法改善预测，
  它带来的是主观平滑度，而不是同体积下的 PSNR。

## 安全 / 像素预算

为防止「解压炸弹」（`testdata` 里一个几十字节、却声明 16383×16383 的恶意头），
所有解码路径在**读取宽高之后、分配像素缓冲之前**按像素预算预检：

- `DecodeOptions.MaxPixels`（默认 `1<<27` ≈ 1.34 亿像素，约 512 MB ARGB）会透传到
  `vp8.DecodeLimit` / `vp8l.DecodeARGBLimit`；超过预算返回 `ErrTooLarge` / `ErrImageTooLarge`。
- chunk 载荷按实际到达的字节流式读入，不再按声明长度预分配：几十字节、只含一个声明
  1 GB 的 chunk 头的文件会直接失败，不会有那次分配；声明长度大于实际数据时按截断报错，
  而不是当成流正常结束。
- RIFF File Size 之后的数据一律忽略（规范允许）；`verifySize` 仍会拒绝大小字段与
  实际长度不符的流。
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
    （失败返回 `{ error }`；动画取第一帧）
  - `webpDecodeFrames(bytes) -> [ { width, height, duration, rgba }, … ]`
    （全部动画帧，按画布尺寸；静态图返回一帧）
  - `webpInfo(bytes) -> { width, height, hasAlpha, hasAnimation, hasICC,
    hasEXIF, hasXMP, frameCount }`（不解码像素；VP8X 标志位 + 帧数）
  - `bytes` 可传 `Uint8Array` 或 `ArrayBuffer`；`rgba` 为直通 alpha 的
    `Uint8Array`，须同步消费（Go 托管内存）。
- 解码覆盖完整扩展格式：VP8X alpha（无损与有损 `VP8X+ALPH`）、动画（`VP8X+ANIM+ANMF`），
  以及 ICC/EXIF/XMP 元数据标志。完整示例：`wasm/example/index.html` 与 `wasm/README.md`。

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
