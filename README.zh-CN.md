# simple-webp

纯 Go 的 **WebP 解码 + 编码** —— VP8 有损、VP8L 无损、ALPH 透明通道、动画。
无 cgo、不依赖 libwebp：编解码全部自己实现，输出标准 `image.Image`
（已注册 `image.RegisterFormat`），唯一的第三方模块是 `github.com/pkg/errors`（仅用于错误包裹）。

<p>
<a href="https://pkg.go.dev/github.com/knoweather/simple-webp"><img src="https://pkg.go.dev/badge/github.com/knoweather/simple-webp.svg" alt="Go Reference"></a>
<a href="go.mod"><img src="https://img.shields.io/badge/go-1.27%2B-00ADD8?logo=go" alt="Go 1.27+"></a>
<a href="LICENSE"><img src="https://img.shields.io/badge/license-MIT-blue" alt="MIT"></a>
</p>

[English](./README.md) | **简体中文**

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

- **字节级回写**：解析 → 编辑（`SetImage` / `AddFrame` / `AddRawChunk` /
  `DeleteRawChunk`）→ 写回；未知 chunk 与未改动的像素原样保留，ICCP/EXIF/XMP 等
  元数据只是搬运、绝不重编码图像数据。
- **编解码完整**：VP8 / VP8L / ALPH / 动画解码，外加带 loop filter、目标体积码率控制、
  color transform、color cache、meta-Huffman 分组与动画输出的编码器。
- **需要时可严格**：`ParseOptions.Strict` 校验容器规范的 MUST（chunk 顺序、VP8X 必须最前、
  保留位、画布与帧矩形、标志与实际数据一致）；默认仍宽松。

> [!NOTE]
> 🚀 **首次发版，灰度测试中。** 有任何问题或建议，欢迎提 [issue](https://github.com/knoweather/simple-webp/issues)！

架构与 [simple-png](https://github.com/knoweather/simple-png) 保持一致：
chunk 化、原始字节保留、可字节级回写。

## 支持范围

| 能力 | 状态 | 说明 |
|---|---|---|
| RIFF/WEBP 容器 | ✅ | `VP8 `/`VP8L`/`VP8X`/`ALPH`/`ANIM`/`ANMF`/`ICCP`/`EXIF`/`XMP `，含 padding |
| 严格解析 | ✅ | `ParseOptions.Strict` 校验规范 MUST（chunk 顺序、VP8X 必须最前、VP8X/ALPH/ANMF 保留位、画布面积、帧矩形、只能有一个图像数据块、VP8X 标志与实际数据一致、VP8L 不带 ALPH、填充字节为 0）；默认仍宽松 |
| VP8L 无损解码 | ✅ | 4 种变换（predictor/color/subtract-green/color-indexing）、meta-Huffman、LZ77、color cache |
| VP8 有损解码 | ✅ | bool 解码器、关键帧头、分区、系数树、16x16/8x8/4x4 预测、WHT+IDCT、loop filter |
| ALPH 透明通道 | ✅ | 未压缩（3 种行滤波）与 VP8L 压缩两种 alpha |
| 动画 | ✅ | `ANIM`/`ANMF`，dispose/blend 合成；`FrameAt` 只合成到指定帧，不克隆中间帧 |
| image 接口 | ✅ | `ToImage()`、`*Webp` 实现 `image.Image`、`image.RegisterFormat("webp", ...)`；有损图可输出 `PixelFormatYCbCr`；`DecodeWebp` 直接从 `io.Reader` 解码，`DecodeInto` 可复用调用方缓冲 |
| 元数据编辑 | ✅ | 追加 `ICCP`/`EXIF`/`XMP ` 后回写，**不重编码像素**，未改动时字节级还原；新 chunk 按类型落到规范位置 |
| 容器编辑 | ✅ | `NewWebp` + `SetImage` 从零构造，`AddFrame`/`RemoveFrame` 编辑动画，`DeleteRawChunk` 删已消费 chunk，`InvalidateCache` 清解码缓存 |
| VP8L 无损编码 | ✅ | 色彩索引 / subtract-green / predictor / **颜色变换** / **颜色缓存** / **meta-Huffman 分组** / LZ77 / Huffman（≤15 位），多种写法取最短 |
| VP8 有损编码 | ✅ | 仅帧内：率失真模式决策、16x16 或 4×4 亮度模式、8×8 色度模式、**环路滤波**、**目标体积码率控制**；支持有损 + alpha（`VP8X`+`ALPH`） |
| 动画编码 | ✅ | `EncodeAnimation` 写 `VP8X`+`ANIM`+`ANMF`，帧可有损或无损，透明帧自带 `ALPH` |

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

`Frames` 会按像素预算拒绝过大的画布（返回 `ErrImageTooLarge`）；因为它一次性返回
全部帧，还会用 `FrameOptions.MaxTotalPixels` 约束「画布像素 × 帧数」总量。
`FramesWith(FrameOptions{...})` 可以同时设置两者，画布在分配任何像素缓冲之前就被检查。
只要某一帧时用 `FrameAt(i, FrameOptions{})`：它顺序合成到第 i 帧就停，不克隆中间画布；
`AnimationInfo()` 一次给出画布尺寸、循环次数、背景色与帧数。

### 严格解析

```go
w, err := simple_webp.ParseWebpWith(r, simple_webp.ParseOptions{Strict: true, VerifySize: true})
```

`Strict` 打开规范 MUST 校验，发现第一条违规就报错；默认（以及 `ParseWebp`）保持宽松，
真实世界里不合规的文件仍能解析。校验覆盖重建类 chunk 的顺序、`VP8X`/`ALPH`/`ANMF`
的保留位、画布面积、帧矩形、「只能有一个图像数据块」以及 `VP8X` 特性标志与文件里
实际数据是否一致。

有两类情况刻意**不**报错，因为规范允许它们：元数据与未知 chunk 的顺序是自由的；
没有置动画标志时，`ANIM` chunk 应当被忽略，而不是判为非法。

### 解码成 YCbCr

```go
img, err := w.ToImageWith(simple_webp.DecodeOptions{Format: simple_webp.PixelFormatYCbCr})
```

有损静态图直接返回解码器的 `*image.YCbCr`，不做 YUV→RGB；无损码流、带 alpha 的图与
动画没有 YCbCr 表示，返回 `ErrUnsupported`。

### 编码时带元数据

```go
err := simple_webp.Encode(out, img, &simple_webp.EncodeOptions{
    ICCP: iccProfile,
    EXIF: exifPacket,
    XMP:  xmpPacket,
})
```

元数据会写成带标志位的 `VP8X` 加对应 chunk，顺序按容器规范；不带元数据时输出与以前
逐字节一致。`LossyOptions` 有同样三个字段。

### 有损 + alpha、动图编码

```go
err := simple_webp.EncodeLossy(out, img, &simple_webp.LossyOptions{
    Quality:     80,
    FilterLevel: 20,   // VP8 环路滤波 0..63；0 = 按质量自动
    TargetBytes: 0,    // >0：挑最接近该体积的质量
    AlphaFilter: simple_webp.ALPHFilterGradient,
})
```

带透明度的输入写成 `VP8X`（alpha 位）+ `ALPH` + `VP8 `，alpha 平面无损。动图从画布
与帧列表写出：

```go
err := simple_webp.EncodeAnimation(out, &simple_webp.Animation{
    CanvasWidth: 320, CanvasHeight: 240,
    LoopCount: 0,
    Frames: []simple_webp.Frame{{Image: frame, Duration: 100, Blend: true}},
}, &simple_webp.AnimationOptions{Lossy: true})
```

### 从零构造 / 编辑容器

```go
w := simple_webp.NewWebp(0, 0)
w.SetImage(img, &simple_webp.ImageOptions{Lossy: true})
_ = w.AddFrame(frame)   // 动图：追加一帧
_ = w.RemoveFrame(0)    // …删掉一帧
w.InvalidateCache()     // 直接改过解析结构体后清解码缓存
out, err := w.MarshalBytes()
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

带透明度的输入会写成扩展格式（`VP8X` 的 alpha 位、`ALPH`、`VP8 `），alpha 平面
无损；只想要无损像素时仍用 `EncodeBytes`。

有损编码器按率失真选模式：每个候选模式都要预测、变换、量化一遍，再按
「重建失真 + 权衡系数 × 码长」打分，码长取系数 token 实际要花的比特数，取代价
最小的一个。宏块的亮度要么用整块 16×16 模式，要么用十六个 4×4 模式，看哪条路
更便宜；色度用四种 8×8 模式之一。帧头现在会写真实的环路滤波参数，`TargetBytes`
打开目标体积码率控制（只调全局量化，逐段量化还没做）。

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
├── strict.go      ParseOptions.Strict 背后的规范 MUST 校验
├── edit.go        NewWebp / SetImage / AddFrame / RemoveFrame / InvalidateCache
├── writer.go      WriteTo / MarshalBytes（原序回写，重算长度与 padding）
├── container_write.go  编码器共用的 RIFF 组装
├── decode.go      ToImage / ToImageWith / 有损无损调度 / YCbCr 输出
├── alpha.go       ALPH 解压、alpha 行滤波逆变换、合成
├── alpha_encode.go  ALPH 载荷：过滤与 VP8L 压缩
├── animation.go   ANMF 帧合成（blend / dispose）、FrameAt / AnimationInfo
├── animation_encode.go  动图编码（VP8X + ANIM + ANMF）
├── vp8.go         VP8 → NRGBA（9:3:3:1 fancy upsampling + BT.601 定点转换）
├── image.go       image.RegisterFormat / Decode / DecodeConfig / image.Image
├── encode.go      image.Image → VP8L → RIFF
├── encode_lossy.go  image.Image → VP8（+ ALPH）→ RIFF
├── internal/vp8/  VP8 有损解码器 + 编码器（bool 编解码、预测、IDCT、loop filter、率失真、码率控制）
├── internal/vp8l/ VP8L 无损解码器 + 编码器（位流、Huffman、LZ77、变换、颜色缓存、颜色变换、meta-Huffman）
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
- 编码产物用本库解码 + 严格容器校验验证；本系列开发环境里没有 libwebp 工具，因此
  没有重新做 libwebp 交叉验证，这里不做该断言。

---

## Benchmark

Windows / amd64，`go test -run XXX -bench . -benchtime 200ms`，本系列容器与编码改动后
重新测量：

| 场景 | simple-webp | 参考实现 (x/image) |
|---|---|---|
| 容器解析（320×240，37 KB） | **17.3 µs**（2.2 GB/s） | — |
| VP8L 无损解码（320×240） | 2.15 ms（35.7 MP/s） | 1.61 ms（47.6 MP/s） |
| VP8 有损解码（400×300） | **1.19 ms**（101 MP/s） | 0.67 ms（179 MP/s） |
| VP8L 无损编码（320×240） | 35.6 ms（2.16 MP/s） | 不支持 |

> 以上数字来自本机一次运行并做了取整；早先与 libwebp 的对比测于另一台机器，本环境
> 没有 `cwebp`/`dwebp`，因此不再复述那组数字。

无损解码仍慢于参考实现（Huffman 逐位解码，尚未加查表快路径）。无损编码现在会试写
颜色变换、颜色缓存与 meta-Huffman 几种写法再取最短，因此比过去慢、输出更小；与
`cwebp` 的体积比这次没有重新测量。

---

## 已知限制

- `EncodeOptions.Lossless` 被接受但忽略：`Encode` 只写 VP8L 无损，有损走 `EncodeLossy`。
  该字段不能直接生效，因为它的零值是 `false`——一旦按它分叉，所有没显式设置的调用方
  都会静默变成有损。
- **有损编码仅帧内**：VP8 编码器按率失真选模式——每个候选模式都要走一遍预测、
  正变换与量化，再按「重建失真 + 系数 token 的比特数」打分取最优。亮度用整块
  16×16 或十六个 4×4 模式（取更便宜的一条），色度用 8×8 模式。环路滤波与目标体积
  码率控制都已实现；码率控制只调全局量化，另可用 `LossyOptions.Segment` 打开
  逐段量化（见「演进方向」）。质量→量化曲线本身已经对齐 libwebp，同一个 `Quality`
  选出的量化步长与对应的 `cwebp -q` 基本一致。
- 有损编码跑两遍：第一遍收集 token 统计，第二遍用据此调整的概率表编码，耗时约为
  单遍的 1.8 倍。模式搜索只在第一遍做，第二遍直接沿用第一遍选好的模式。
- 率失真搜索是编码变慢的主因：每个宏块的每个 4×4 块都要把十种模式各跑一遍
  变换与量化。
- 无损编码现在会试写最多四种写法（颜色变换开/关、颜色缓存、meta-Huffman 分块）并
  取最短，因此比原来的单遍编码慢数倍；现有测试只断言解码像素，不断言字节数。
- meta-Huffman 用两组 16×16 块，按符号直方图做两簇聚类。区域符号分布不同时有效；
  但在 Method 2 的预测器之后通常还是单组更短，这种情况下码流与以前一致。
- `ALPH` 的 `preprocessing=1`（level reduction）按原值输出，未做参考实现的抖动还原；
  cwebp 默认不产生该模式。编码端写 `preprocessing=0`，且不量化 alpha。
- 无损解码的 Huffman 是逐位解码，大图比参考实现慢。
- `DeleteRawChunk` 只返回删掉了几个 chunk，不说明为什么没删：返回 0 表示该类型
  根本不存在，或者删除会让文件变成非法流而未被接受（函数签名返回 int，无法返回 error）。
- **WebP 不含 VP9**：WebP 的两种编码是 VP8（有损，`VP8` chunk）与 VP8L（无损，`VP8L` chunk）。
  VP9 是另一种编码，只用在 WebM/Matroska 容器里，与本库无关。

## 演进方向

有损编码器按设计只做帧内，而码流本身是合规的：VP8 与 VP8L 编码器写出的码流，
用独立解码器（`golang.org/x/image`，libwebp 的 Go 移植版）能解出逐样本一致的结果。
与 `cwebp` 之间的剩余差距按下表排列，按「改动量 ÷ 收益」排序：

质量→量化曲线已经对齐 libwebp（分段线性再开立方根），所以同一个 `Quality`
大致对应 `cwebp -q` 相同的量化步长。在一张 320×240 的平滑测试图上，同一个质量
设置现在比原来的线性映射多约 1.5–3.7 dB PSNR；两条曲线在质量 100 处重合。

`LossyOptions.Segment` 在此之上打开逐段量化：按宏块活动度把帧分成四段，平坦侧
比基础量化粗一档或两档，繁忙区域保持基础步长。编码器会把「不分段」「常规步长」
「双倍步长」三个方案各编码一遍、只留最小的那个，因此这个选项只能把体积改小、
不可能改大。收益随内容而变——平坦区域在本编码器里直接 skip，粗量化无利可图，
收益主要出现在低幅值纹理上（噪声 fixture 上约 5%，纯渐变则没有）。

| # | 项目 | 现状 | 计划 |
|---|---|---|---|
| 1 | **token 概率更新的成本模型** | `adaptTokenProb` 用统一的 12 bit 收益阈值，没查 `tokenUpdateProb`。表里约 40% 的条目更新概率是 255，RFC 6386 §13.5 规定这类条目「从不更新」；给它们写更新标志实际要 ~16 bit，而模型按 ~9 bit 算，可能反而把码流写大 | 按条目实际信号成本定价，对齐 libwebp 的 `enc/frame_enc.c` |
| 2 | **首分区长度溢出** | 帧头里的首分区长度是 19 位字段，既不掩码也不检查范围，超大画布下会静默损坏码流 | 放不下时返回错误，对齐 libwebp 的 `VP8_ENC_ERROR_PARTITION0_OVERFLOW` |
| 3 | **`LossyOptions.Sharpness`** | `internal/vp8` 已按 RFC 正确写入 3 位 `sharpness_level`，但 `LossyOptions` 没有这个字段，外部根本传不进来 | 给 `LossyOptions` 补上该字段（注释方向也要改：和 `cwebp -sharpness` 一样，0 最锐） |
| 4 | **动图编码忽略 `AlphaFilter`** | 静态有损路径按 `LossyOptions.AlphaFilter` 选滤波，动图路径却硬编码成 `ALPHFilterNone` | 动图路径也读 `LossyOpt.AlphaFilter` |
| 5 | **动图 `Duration` 溢出** | 24 bit 字段静默截断（`1<<24` 写成 0，负数回绕） | 编码前校验范围并返回错误 |
| 6 | **VP8L 颜色索引的 alpha** | 规范要求索引图所有 alpha 置 255，编码端写成 0；解码不受影响（逆变换整体替换像素） | 按规范写 255 |
| 7 | **VP8L 重复变换** | 规范要求每种变换只能用一次，解码端没有去重，libwebp 判为损坏 | 解码时拒绝重复的变换类型 |
| 8 | **VP8L 二维邻域距离码** | 解码端实现了完整的 120 项邻域映射，编码端却一律写线性距离码：1 像素距离要 ~10 bit，邻域码只要 1 bit。这是无损体积差距最大的单一来源 | 编码端补上 `DistanceToPlaneCode` 与查找表，对齐 libwebp 的 `src/enc/vp8l_enc.c` |
| 9 | **更多码组与更好的聚类** | meta 前缀码目前只分两组，libwebp 会聚成更多组，也更擅长「照片 + 文字」这类混合内容 | 聚成更多组 |
| 10 | **动图帧间差分** | 每帧都是全量编码：没有帧矩形裁剪，也没有帧间差分 | 加差分与最小化，对齐 libwebp 的 `WebPAnimEncoder` |
| 11 | **多 token 分区** | 目前只有一个残差分区，影响解码并行度而不影响体积 | 写出多个分区 |

另有几项 libwebp 私有能力不在 WebP 码流里，规范并没有要求：`-near_lossless` 源端
预滤波、`-alpha_q` 有损 alpha、`-sns` 空间噪声整形、`-pass` 多遍优化、
`-partition_limit`、`-mt` 多线程。解码侧本库可以从任意 `io.Reader` 流式解析
容器（`DecodeWebp`），也可以解到调用方提供的缓冲里（`DecodeInto`），但还没有
libwebp `WebPIDecoder` 那样「边追加边解码」的增量接口，也没有缩小解码与
区域解码。

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
- `Frames` 在分配画布之前按像素预算检查 VP8X 声明的画布，并用
  `FrameOptions.MaxTotalPixels` 约束「画布 × 帧数」的像素总量：16383×16383 的画布
  配一帧 1×1，会在分配之前被拒绝。
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
