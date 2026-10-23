// Package simple_webp 解析与编辑 WebP（RIFF）容器：读取 RIFF 头与全部 chunk，
// 解码容器级 chunk（VP8X、VP8、VP8L、ALPH、ANIM、ANMF、ICCP、EXIF、XMP），
// 并支持在不重新编码像素的前提下增删元数据 chunk、按原始顺序写回整个流。
// 纯 Go 实现，不依赖 cgo 与 libwebp；浏览器与 Node.js 用的 WebAssembly
// （WASM）解码前端在 wasm 子包里。
//
// Package simple_webp parses and edits WebP (RIFF) containers: it reads the
// RIFF header and every chunk, decodes the container-level chunks (VP8X, VP8,
// VP8L, ALPH, ANIM, ANMF, ICCP, EXIF, XMP), and supports adding or removing
// metadata chunks and writing the stream back in its original order without
// re-encoding the pixels. It is pure Go — no cgo, no libwebp — and the
// WebAssembly (WASM) decoder front end for the browser and Node.js lives in
// the wasm subpackage.
package simple_webp

import (
	"bytes"
	"image"
	"io"
	"sync"

	"github.com/pkg/errors"
)

// 解析器与解码器共用的哨兵错误，调用方可用 errors.Is 判断具体类型。
//
// Sentinel errors returned by the parser and the decoders; callers can test
// them with errors.Is.
var (
	// ErrNotWebP 在流不以 RIFF....WEBP 签名开头时返回。
	// ErrNotWebP is returned when the stream does not start with RIFF....WEBP.
	ErrNotWebP = errors.New("invalid webp: bad RIFF/WEBP signature")
	// ErrChunkNotFound 在 ParseChunk 找不到所请求类型的剩余 chunk 时返回。
	// ErrChunkNotFound is returned by ParseChunk when no chunk of the
	// requested type is left in the stream.
	ErrChunkNotFound = errors.New("chunk not found")
	// ErrUnsupported 对应格式合法但本库尚未实现的 WebP 特性。
	// ErrUnsupported is returned for valid but unimplemented webp features.
	ErrUnsupported = errors.New("unsupported webp feature")
	// ErrImageTooLarge 在图像像素数超出配置的像素预算时返回。
	// ErrImageTooLarge is returned when the image exceeds the configured
	// pixel budget.
	ErrImageTooLarge = errors.New("webp image too large")
)

// Webp 是解析后的 WebP 容器。图像数据 chunk 的负载以原始字节保留，
// 各解析结构体只是对这些字节的视图，因此可以修改元数据并原样写回，
// 无需重新编码像素。
//
// Webp is a parsed WebP container. The chunk payloads of the image data are
// kept as raw bytes and the parsed structs are views onto them, so metadata
// can be edited and written back without re-encoding the pixels.
type Webp struct {
	Width, Height int
	HasAlpha      bool
	HasAnimation  bool

	VP8X  *VP8X
	VP8   *VP8
	VP8L  *VP8L
	ALPH  *ALPH
	ANIM  *ANIM
	ANMFs []*ANMF
	ICCP  *ICCP
	EXIF  *EXIF
	XMP   *XMP

	// OtherChunk 按 chunk 类型存放经 ParseChunk 解析出的自定义 chunk。
	// OtherChunk holds the custom chunks parsed through ParseChunk, grouped
	// by chunk type.
	OtherChunk map[FourCC][]ChunkParse
	// chunks 是基础解析后仍留在流中的原始 chunk，按文件顺序排列。
	// chunks holds the raw chunks that base parsing did not decode
	// natively, in file order.
	chunks []*riffChunk

	// origChunks 按原始顺序保存文件的全部 chunk（包括已消费和已删除的），
	// 使 WriteTo 能忠实重现整个流；两个并行切片记录每个 chunk 的状态。
	// origChunks keeps every chunk of the file in its original order,
	// including the consumed and deleted ones, so WriteTo can reproduce the
	// stream faithfully. The parallel slices record what happened to each
	// chunk.
	origChunks   []*riffChunk
	origConsumed []bool
	origDeleted  []bool

	// maxChunkSize 是本次解析使用的 chunk 载荷上限，来自 ParseOptions；
	// 解析 ANMF 内的嵌套子 chunk 时同样受它约束。
	//
	// maxChunkSize is the chunk payload cap of this parse, taken from
	// ParseOptions; the nested sub-chunks inside an ANMF are bound by it too.
	maxChunkSize int

	sync.RWMutex
	sync.Once

	// img/imgErr 缓存像素解码结果，供 image.go 中的 image.Image 实现使用；
	// 解码失败时缓存错误并原样返回。
	// img/imgErr cache the decoded pixels for the image.Image implementation
	// in image.go; on failure the error is cached and returned as is.
	img    image.Image
	imgErr error
}

// countingReader 包装底层 Reader 并累计已读字节数，用于校验 RIFF 大小字段。
// countingReader wraps the underlying reader and counts the bytes read, used
// to validate the RIFF size field.
type countingReader struct {
	r io.Reader
	n int64
}

// Read 转发到底层 Reader 并把读到的字节数累加到 n。
// Read forwards to the underlying reader and accumulates the byte count in n.
func (c *countingReader) Read(p []byte) (int, error) {
	n, err := c.r.Read(p)
	c.n += int64(n)
	return n, err
}

// ParseOptions 调节容器解析。零值等价于 ParseWebp 的宽松行为。
//
// ParseOptions tunes container parsing. The zero value matches the lenient
// behaviour of ParseWebp.
type ParseOptions struct {
	// Strict 打开规范 MUST 校验：VP8X 必须第一个、重建类 chunk 顺序、
	// VP8X 保留位为 0、动画位必须有 ANIM、帧矩形落在画布内、VP8L 帧不得带
	// ALPH、填充字节为 0。默认关闭，以免拒绝真实世界里的脏文件。
	//
	// Strict enables the spec MUST checks: VP8X first, the order of the
	// reconstructing chunks, zeroed VP8X reserved bits, an ANIM chunk behind
	// the animation flag, frames inside the canvas, no ALPH on a VP8L frame,
	// and zero padding bytes. It is off by default so real world files that
	// violate these rules still parse.
	Strict bool
	// VerifySize 要求 RIFF File Size 与实际流长度一致，也就是 ParseWebp 的
	// verifySize 参数。
	//
	// VerifySize requires the RIFF File Size to match the actual stream length,
	// the verifySize argument of ParseWebp.
	VerifySize bool
	// MaxChunkSize 限制单个 chunk 载荷的字节数，0 表示默认的 1<<30。
	// 解析不可信输入时调小它可以更早拒绝畸形文件；载荷按流式读入，
	// 超限的 chunk 在分配任何内存之前就被拒绝。
	//
	// MaxChunkSize caps the payload of a single chunk in bytes; 0 means the
	// default 1<<30. Turn it down when parsing untrusted input to reject
	// malformed files earlier; payloads are streamed in, so an oversized
	// chunk is refused before any memory is allocated for it.
	MaxChunkSize int
}

// ParseWebp 解析 WebP 流：读取 RIFF 头与全部 chunk，并解码容器级 chunk
// （VP8X、VP8、VP8L、ALPH、ANIM、ANMF、ICCP、EXIF、XMP）。未知类型的 chunk
// 以原始形式保留，见 RawChunks。仅当 verifySize 为 true 时才校验 RIFF
// 大小字段与实际流长度是否一致。
//
// ParseWebp parses a webp stream: it reads the RIFF header, all chunks and
// decodes the container chunks (VP8X, VP8, VP8L, ALPH, ANIM, ANMF, ICCP,
// EXIF, XMP). Chunks of unknown type are kept in raw form, see RawChunks.
//
// The RIFF size field is validated only when verifySize is true.
func ParseWebp(r io.Reader, verifySize ...bool) (*Webp, error) {
	return ParseWebpWith(r, ParseOptions{VerifySize: len(verifySize) > 0 && verifySize[0]})
}

// ParseWebpWith 按给定选项解析 WebP 流，见 ParseOptions。
//
// ParseWebpWith parses a webp stream with the given options, see ParseOptions.
func ParseWebpWith(r io.Reader, opt ParseOptions) (*Webp, error) {
	cr := &countingReader{r: r}
	var head [12]byte
	if _, err := io.ReadFull(cr, head[:]); err != nil {
		return nil, errors.WithMessage(err, "invalid webp: can not read RIFF header")
	}
	if string(head[:4]) != string(riffTag) || string(head[8:12]) != string(webpTag) {
		return nil, ErrNotWebP
	}
	declared := by.Uint32(head[4:8])

	// declared is the RIFF File Size: the 4 byte 'WEBP' tag plus every chunk
	// that follows it, so the chunk region is declared-4 bytes long. Data after
	// that point is not part of the container and is ignored. A declared size
	// below 4 is clearly invalid, so the stream is read to EOF as before.
	//
	// declared 是 RIFF File Size：'WEBP' 4 字节加其后全部 chunk，所以 chunk 区长度为
	// declared-4。该点之后的数据不属于容器，一律忽略；declared 小于 4 明显非法，
	// 保持原来的读到 EOF 行为。
	var src io.Reader = cr
	if declared >= 4 {
		src = io.LimitReader(cr, int64(declared)-4)
	}

	var w = &Webp{maxChunkSize: opt.MaxChunkSize}
	for {
		c, err := readRiffChunk(src, w.maxChunkSize)
		if err != nil {
			if errors.Is(err, io.EOF) {
				break
			}
			return nil, err
		}
		w.chunks = append(w.chunks, c)
	}
	if len(w.chunks) == 0 {
		return nil, errors.New("invalid webp: no chunks")
	}
	if opt.VerifySize {
		// The limited read only counted up to the declared size, so drain the
		// trailing bytes before comparing the counters.
		// 限定读取只统计到声明长度，所以先把尾部数据读掉再比对计数。
		if _, err := io.Copy(io.Discard, cr); err != nil {
			return nil, errors.WithMessage(err, "invalid webp: can not read trailing data")
		}
		if got := uint32(cr.n - 8); got != declared {
			return nil, errors.Errorf("invalid webp: RIFF size is %d, stream is %d", declared, got)
		}
	}

	// 在任何 chunk 被消费之前先快照原始 chunk 顺序
	// snapshot the original chunk order before any chunk gets consumed
	w.origChunks = append([]*riffChunk(nil), w.chunks...)
	w.origConsumed = make([]bool, len(w.chunks))
	w.origDeleted = make([]bool, len(w.chunks))
	if err := w.parseBaseChunk(); err != nil {
		return nil, errors.WithStack(err)
	}
	if opt.Strict {
		if err := w.validateStrict(); err != nil {
			return nil, err
		}
	}
	return w, nil
}

// ParseWebpBytes 解析内存中的 WebP 字节流，不校验 RIFF 大小字段
// （等价于调用 ParseWebp 时不传 verifySize）。
//
// ParseWebpBytes parses a webp stream held in memory; the RIFF size field is
// not verified (ParseWebp is called without verifySize).
func ParseWebpBytes(b []byte) (*Webp, error) {
	return ParseWebp(bytes.NewReader(b))
}

// ParseWebpBytesWith 按给定选项解析内存中的 WebP 字节流，见 ParseOptions。
//
// ParseWebpBytesWith parses a webp stream held in memory with the given
// options, see ParseOptions.
func ParseWebpBytesWith(b []byte, opt ParseOptions) (*Webp, error) {
	return ParseWebpWith(bytes.NewReader(b), opt)
}

// parseBaseChunk 消费所有本库能识别的 chunk，并根据解析结果推导
// 图像尺寸与标志位；未被消费的 chunk 留在原始列表中。
// parseBaseChunk consumes every chunk the library understands and derives the
// image dimensions and flags from what was found; chunks it does not consume
// stay in the raw list.
func (w *Webp) parseBaseChunk() error {
	kept := make([]*riffChunk, 0, len(w.chunks))
	for _, c := range w.chunks {
		consumed, err := w.parseOne(c)
		if err != nil {
			return errors.WithStack(err)
		}
		if !consumed {
			kept = append(kept, c)
		}
	}
	w.chunks = kept
	return w.derive()
}

// parseOne 把单个 chunk 解析进对应结构体，返回该 chunk 是否被消费。
// 单实例 chunk（VP8X、VP8 等）的第二次出现保留为原始 chunk 而不是报错，
// 这样损坏的文件也能尽可能多地解析出内容。
// parseOne parses a single chunk into its struct and reports whether the
// chunk was consumed; a second occurrence of a single-instance chunk is left
// raw instead of failing the parse, so a malformed file still yields as much
// as possible.
func (w *Webp) parseOne(c *riffChunk) (bool, error) {
	var target ChunkParse
	switch c.name {
	case VP8XChunk:
		if w.VP8X != nil {
			return false, nil
		}
		w.VP8X = &VP8X{}
		target = w.VP8X
	case VP8Chunk:
		if w.VP8 != nil {
			return false, nil
		}
		w.VP8 = &VP8{}
		target = w.VP8
	case VP8LChunk:
		if w.VP8L != nil {
			return false, nil
		}
		w.VP8L = &VP8L{}
		target = w.VP8L
	case ALPHChunk:
		if w.ALPH != nil {
			return false, nil
		}
		w.ALPH = &ALPH{}
		target = w.ALPH
	case ANIMChunk:
		if w.ANIM != nil {
			return false, nil
		}
		w.ANIM = &ANIM{}
		target = w.ANIM
	case ANMFChunk:
		frame := &ANMF{maxChunkSize: w.maxChunkSize}
		if err := frame.Parse(&RawChunk{Name: c.name, Data: c.data}); err != nil {
			return false, err
		}
		w.ANMFs = append(w.ANMFs, frame)
		w.markOrigConsumed(c)
		return true, nil
	case ICCPChunk:
		if w.ICCP != nil {
			return false, nil
		}
		w.ICCP = &ICCP{}
		target = w.ICCP
	case EXIFChunk:
		if w.EXIF != nil {
			return false, nil
		}
		w.EXIF = &EXIF{}
		target = w.EXIF
	case XMPChunk:
		if w.XMP != nil {
			return false, nil
		}
		w.XMP = &XMP{}
		target = w.XMP
	default:
		return false, nil
	}
	if err := target.Parse(&RawChunk{Name: c.name, Data: c.data}); err != nil {
		return false, err
	}
	w.markOrigConsumed(c)
	return true, nil
}

// derive 从已解析的 chunk 推导 Width/Height/HasAlpha/HasAnimation：
// 尺寸优先取 VP8X 画布，其次是 VP8L、VP8；存在 ALPH chunk 或 VP8L 的
// AlphaUsed 位时 HasAlpha 置 true；动画标志置起但没有任何 ANMF chunk
// 时报错。找不到任何图像数据 chunk（VP8/VP8L）也报错。
// derive fills Width/Height/HasAlpha/HasAnimation from the parsed chunks:
// the size comes from the VP8X canvas when present, otherwise from VP8L or
// VP8; an ALPH chunk or the VP8L AlphaUsed flag set HasAlpha, and the
// animation flag is rejected unless at least one ANMF chunk exists. It fails
// when no image data chunk (VP8/VP8L) was found.
func (w *Webp) derive() error {
	switch {
	case w.VP8X != nil:
		w.Width, w.Height = w.VP8X.CanvasWidth, w.VP8X.CanvasHeight
		w.HasAlpha = w.VP8X.HasAlpha()
		w.HasAnimation = w.VP8X.HasAnimation()
	case w.VP8L != nil:
		w.Width, w.Height = w.VP8L.Width, w.VP8L.Height
		w.HasAlpha = w.VP8L.AlphaUsed
	case w.VP8 != nil:
		w.Width, w.Height = w.VP8.Width, w.VP8.Height
	default:
		return errors.New("invalid webp: no image data (VP8/VP8L chunk)")
	}
	if w.Width <= 0 || w.Height <= 0 {
		return errors.Errorf("invalid webp: bad image size %dx%d", w.Width, w.Height)
	}
	if w.ALPH != nil {
		w.HasAlpha = true
	}
	if w.VP8L != nil && w.VP8L.AlphaUsed {
		w.HasAlpha = true
	}
	if w.HasAnimation && len(w.ANMFs) == 0 {
		return errors.New("invalid webp: animation flag without ANMF chunk")
	}
	return nil
}

// markOrigConsumed 在原始顺序快照中把该 chunk 标记为已消费。
// markOrigConsumed marks a chunk as consumed in the original order snapshot.
func (w *Webp) markOrigConsumed(c *riffChunk) {
	for i, oc := range w.origChunks {
		if oc == c {
			w.origConsumed[i] = true
			return
		}
	}
}

// ParseChunk 把第一个类型与 c.ChunkName() 匹配的剩余 chunk 解析进 c。
// 解析成功后该 chunk 被视为已消费并从原始 chunk 列表移除；除非 notSave
// 为 true，解析器实例会记录到 OtherChunk，之后可用 GetOtherChunkByName
// 取回。找不到匹配 chunk 时返回 ErrChunkNotFound。
// ParseChunk parses the first remaining chunk whose type matches
// c.ChunkName() into c. After a successful parse the chunk is consumed and
// removed from the raw chunk list; unless notSave is true, the parser
// instance is recorded in OtherChunk and can be retrieved later with
// GetOtherChunkByName. It returns ErrChunkNotFound when no chunk matches.
func (w *Webp) ParseChunk(c ChunkParse, notSave ...bool) error {
	w.Do(func() {
		w.OtherChunk = make(map[FourCC][]ChunkParse)
	})
	for i, cur := range w.chunks {
		if cur.name != c.ChunkName() {
			continue
		}
		if err := c.Parse(&RawChunk{Name: cur.name, Data: cur.data}); err != nil {
			return errors.WithStack(err)
		}
		w.chunks = append(w.chunks[:i], w.chunks[i+1:]...)
		w.markOrigConsumed(cur)
		if !(len(notSave) > 0 && notSave[0]) {
			w.Lock()
			w.OtherChunk[c.ChunkName()] = append(w.OtherChunk[c.ChunkName()], c)
			w.Unlock()
		}
		return nil
	}
	return ErrChunkNotFound
}

// GetOtherChunkByName 返回为某类 chunk 记录的自定义解析器列表；
// 该类型没有记录时返回 ErrChunkNotFound。
// GetOtherChunkByName returns the custom chunk parsers recorded for a type;
// it returns ErrChunkNotFound when nothing was recorded for that type.
func (w *Webp) GetOtherChunkByName(name FourCC) ([]ChunkParse, error) {
	w.RLock()
	defer w.RUnlock()
	if list, ok := w.OtherChunk[name]; ok {
		return list, nil
	}
	return nil, ErrChunkNotFound
}

// RawChunks 返回基础解析后仍以原始形式保留的所有 chunk，按其在文件中
// 出现的顺序排列。
// RawChunks returns all chunks that are still left after base parsing, in the
// order they appear in the file.
func (w *Webp) RawChunks() []RawChunk {
	out := make([]RawChunk, 0, len(w.chunks))
	for _, c := range w.chunks {
		out = append(out, RawChunk{Name: c.name, Data: c.data})
	}
	return out
}

// RawChunkByName 返回第一个仍存在的指定类型 chunk 的原始负载；
// 不存在时 ok 为 false。
// RawChunkByName returns the raw payload of the first still-present chunk of
// the given type; ok is false when no such chunk remains.
func (w *Webp) RawChunkByName(name FourCC) ([]byte, bool) {
	for _, c := range w.chunks {
		if c.name == name {
			return c.data, true
		}
	}
	return nil, false
}

// clearFlag 在存在 VP8X 时清除一个特性标志位，保持标志与数据一致。
func (w *Webp) clearFlag(flag uint8) {
	if w.VP8X != nil {
		w.VP8X.Flags &^= flag
	}
}

// hasExtendedFeatures 报告流里是否还有必须依赖 VP8X 的扩展特性：已解析的
// 结构体，或者仍以原始形态留在流中的扩展 chunk。
func (w *Webp) hasExtendedFeatures() bool {
	if w.ICCP != nil || w.EXIF != nil || w.XMP != nil || w.ALPH != nil || w.ANIM != nil || len(w.ANMFs) > 0 {
		return true
	}
	for _, c := range w.chunks {
		switch c.name {
		case ICCPChunk, EXIFChunk, XMPChunk, ALPHChunk, ANIMChunk, ANMFChunk:
			return true
		}
	}
	return false
}

// DeleteRawChunk 从流中删除所有指定类型的 chunk 并返回删除数量。
// 既适用于原生解码的元数据 chunk（ICCP、EXIF、XMP），也适用于解析时已被
// 消费的 ALPH、ANIM、ANMF、VP8X、VP8、VP8L，以及未知的原始 chunk；删除后
// 调用 WriteTo 写出的流不再包含这些 chunk，VP8X 的对应标志位同步清除，
// 保持标志与数据一致。三条例外都会产出非法流，因此不删除任何东西，返回 0：
// 还有 ANMF 帧时删 ANIM、还有扩展特性时删 VP8X、以及删掉唯一的图像数据。
// DeleteRawChunk removes every chunk of the given type from the stream and
// returns how many chunks were removed. It covers the metadata chunks decoded
// natively (ICCP, EXIF, XMP), the chunks already consumed by parsing (ALPH,
// ANIM, ANMF, VP8X, VP8, VP8L), and unknown raw chunks; a WriteTo afterwards
// writes the stream without those chunks, and the matching VP8X flag bit is
// cleared so the flags stay consistent with the data. Three cases are refused
// because they would produce an invalid stream: deleting ANIM while ANMF frames
// still exist, deleting VP8X while an extended feature still needs it, and
// deleting the only image data chunk. In all three cases nothing is removed and
// the result is 0.
func (w *Webp) DeleteRawChunk(name FourCC) int {
	removed := 0
	gone := make(map[FourCC]bool)
	// 原生解码的元数据 chunk 通过清空解析结构体来删除；若 VP8X 存在，同步
	// 清除其对应该 chunk 的标志位，保持标志位与数据一致。
	// native metadata chunks are dropped by clearing the parsed struct; when a
	// VP8X is present its matching flag bit is cleared too, keeping the flags
	// consistent with the data.
	switch name {
	case ICCPChunk:
		if w.ICCP != nil {
			w.ICCP = nil
			removed++
			w.clearFlag(FlagICC)
			gone[ICCPChunk] = true
		}
	case EXIFChunk:
		if w.EXIF != nil {
			w.EXIF = nil
			removed++
			w.clearFlag(FlagEXIF)
			gone[EXIFChunk] = true
		}
	case XMPChunk:
		if w.XMP != nil {
			w.XMP = nil
			removed++
			w.clearFlag(FlagXMP)
			gone[XMPChunk] = true
		}
	case ALPHChunk:
		if w.ALPH != nil {
			w.ALPH = nil
			removed++
			w.clearFlag(FlagAlpha)
			// alpha 只剩无损码流头里的 alpha_is_used 位
			// alpha now comes only from the lossless header's alpha_is_used bit
			w.HasAlpha = w.VP8L != nil && w.VP8L.AlphaUsed
			gone[ALPHChunk] = true
		}
	case ANIMChunk:
		// 还有帧时删 ANIM 会产出「有帧无 ANIM」的非法流，拒绝删除（返回 0）。
		// deleting ANIM while frames remain would leave frames without ANIM,
		// which is invalid, so the deletion is refused (returns 0).
		if w.ANIM != nil && len(w.ANMFs) == 0 {
			w.ANIM = nil
			removed++
			w.clearFlag(FlagAnimation)
			w.HasAnimation = false
			gone[ANIMChunk] = true
		}
	case ANMFChunk:
		if n := len(w.ANMFs); n > 0 {
			w.ANMFs = nil
			removed += n
			w.clearFlag(FlagAnimation)
			w.HasAnimation = false
			// 没有帧的 ANIM 是非法的，一并删掉
			// an ANIM without frames is invalid, so it goes too
			if w.ANIM != nil {
				w.ANIM = nil
				removed++
				gone[ANIMChunk] = true
			}
			gone[ANMFChunk] = true
		}
	case VP8XChunk:
		// 还有扩展特性时删 VP8X 会让 ICCP/EXIF/XMP/ALPH/ANIM 悬空，拒绝删除。
		// refusing to drop VP8X while an extended feature still needs it.
		if w.VP8X != nil && !w.hasExtendedFeatures() {
			w.VP8X = nil
			removed++
			w.HasAlpha = w.VP8L != nil && w.VP8L.AlphaUsed
			gone[VP8XChunk] = true
		}
	case VP8Chunk:
		// 删掉唯一的图像数据会产出没有像素的容器，拒绝删除。
		// removing the only image data would leave a container without pixels.
		if w.VP8 != nil && (w.VP8L != nil || len(w.ANMFs) > 0) {
			w.VP8 = nil
			removed++
			gone[VP8Chunk] = true
		}
	case VP8LChunk:
		if w.VP8L != nil && (w.VP8 != nil || len(w.ANMFs) > 0) {
			w.VP8L = nil
			removed++
			w.HasAlpha = w.ALPH != nil
			if !w.HasAlpha {
				w.clearFlag(FlagAlpha)
			}
			gone[VP8LChunk] = true
		}
	}

	// 丢弃仍是原始形态的 chunk
	// drop the chunks that are still raw
	rest := make([]*riffChunk, 0, len(w.chunks))
	for _, c := range w.chunks {
		if c.name == name {
			continue
		}
		rest = append(rest, c)
	}
	rawRemoved := len(w.chunks) - len(rest)
	w.chunks = rest
	removed += rawRemoved

	// 同步更新原始顺序快照，让 WriteTo 跳过这些 chunk
	// keep the original order snapshot in sync so WriteTo skips the chunks
	for i, c := range w.origChunks {
		if w.origDeleted[i] {
			continue
		}
		if w.origConsumed[i] {
			// 已消费的 chunk 只在对应结构体被清空时才算删除
			// a consumed chunk counts as deleted only when its struct was cleared
			if gone[c.name] {
				w.origDeleted[i] = true
			}
			continue
		}
		// 未消费的原始 chunk 只在它已从原始列表移除时才算删除
		// an unconsumed raw chunk counts as deleted only when it left the raw list
		if c.name == name && rawRemoved > 0 {
			w.origDeleted[i] = true
		}
	}
	return removed
}

// metadataChunkRank 是元数据与未知 chunk 的排位。它们的顺序规范允许自由
// （"Metadata and unknown chunks MAY appear out of order"），排在所有重建类
// chunk 之后。
//
// metadataChunkRank is the rank of the metadata and unknown chunks. Their
// order is free according to the spec ("Metadata and unknown chunks MAY
// appear out of order"); they rank after every reconstructing chunk.
const metadataChunkRank = 6

// chunkInsertRank 返回一种 chunk 在容器顺序中的排位，新 chunk 依排位插入。
func chunkInsertRank(name FourCC) int {
	switch name {
	case VP8XChunk:
		return 0
	case ICCPChunk:
		return 1
	case ANIMChunk:
		return 2
	case ANMFChunk:
		return 3
	case ALPHChunk:
		return 4
	case VP8Chunk, VP8LChunk:
		return 5
	default:
		return metadataChunkRank
	}
}

// chunkInsertIndex 返回把排位为 r 的新 chunk 插入 names 时的下标：跳过所有
// 排位不大于 r 的 chunk；排位为 metadataChunkRank 的 chunk 追加到尾部。
func chunkInsertIndex(names []FourCC, r int) int {
	if r >= metadataChunkRank {
		return len(names)
	}
	for i, n := range names {
		if chunkInsertRank(n) > r {
			return i
		}
	}
	return len(names)
}

// chunkPresent 报告流里是否仍有一个未被删除的指定类型 chunk。
//
// chunkPresent reports whether the stream still holds a chunk of the given
// type that has not been deleted.
func (w *Webp) chunkPresent(name FourCC) bool {
	for i, c := range w.origChunks {
		if !w.origDeleted[i] && c.name == name {
			return true
		}
	}
	return false
}

// AddRawChunk 把一个新的原始 chunk 按类型排位插入流中：跳过所有排位不大于
// 自身的已有 chunk，即 VP8X 最前，ICCP 在 VP8X 之后、ANIM/ANMF/图像数据之前，
// ANIM 在第一个 ANMF 之前，ALPH 在 ANIM/ANMF 之后、VP8/VP8L 之前，VP8/VP8L
// 之后是 EXIF、XMP 等尾部 chunk；排位 6 一律追加到末尾。可用于给已解析的文件
// 补充 EXIF 或 XMP 等 chunk。name 必须是合法 FourCC，data 不得超过
// maxChunkSize，且 Webp 必须来自成功的 ParseWebp。
//
// 单实例 chunk（VP8X/VP8/VP8L/ALPH/ANIM/ICCP/EXIF/XMP）在流中已存在时拒绝
// 重复插入——两份会让文件非法；ANMF 可以追加，但载荷必须是一帧合法的帧
// （手工构造帧请用 AddFrame）。
//
// AddRawChunk inserts a new raw chunk at the position its type dictates: it
// skips every existing chunk whose rank is not greater than its own, so VP8X
// comes first, ICCP follows VP8X and precedes ANIM, ANMF and the image data,
// ANIM precedes the first ANMF, ALPH follows ANIM/ANMF and precedes VP8/VP8L,
// and EXIF, XMP or unknown types land at the end. Use it e.g. to add an EXIF or
// XMP chunk to an already parsed file. name must be a valid FourCC, data must
// fit in maxChunkSize and the Webp must come from a successful ParseWebp.
//
// A single-instance chunk (VP8X/VP8/VP8L/ALPH/ANIM/ICCP/EXIF/XMP) that is
// already present is refused, because a second copy makes the file invalid; an
// ANMF may be appended, but its payload has to be a valid frame (use AddFrame
// to build one).
func (w *Webp) AddRawChunk(name FourCC, data []byte) error {
	if !validFourCC(name) {
		return errors.Errorf("invalid chunk type %q", string(name))
	}
	if len(data) > maxChunkSize {
		return errors.New("chunk data too large")
	}
	if w.origChunks == nil {
		return errors.New("webp is not parsed")
	}
	// 单实例 chunk 重复插入会产出非法流（两个图像数据块、两个 ICCP……），
	// 这里直接拒绝；结构可以廉价校验的类型顺带校验载荷长度，避免写进一个
	// 解析不回来的 chunk。图像码流与元数据的内容不在这里校验（那需要跑
	// 解码器），调用方负责给合法载荷。
	// A duplicate of a single-instance chunk would produce an invalid stream
	// (two image data chunks, two ICCP chunks, ...), so it is refused; the
	// types whose structure is cheap to check also get a payload length check,
	// so a chunk that would not parse back is refused up front. The content of
	// the image bitstreams and of the metadata is not validated here (that
	// would need a decoder); the caller supplies valid payloads.
	switch name {
	case VP8XChunk, VP8Chunk, VP8LChunk, ALPHChunk, ANIMChunk, ICCPChunk, EXIFChunk, XMPChunk:
		if w.chunkPresent(name) {
			return errors.Errorf("webp: a %q chunk is already present", string(name))
		}
		// VP8L 自带 alpha，再挂 ALPH 是非法组合；VP8 与 VP8L 也只能有一个。
		// A VP8L carries its own alpha, so an extra ALPH is invalid; VP8 and
		// VP8L are mutually exclusive as well.
		switch {
		case name == ALPHChunk && (w.VP8L != nil || w.chunkPresent(VP8LChunk)):
			return errors.New("webp: an ALPH chunk cannot be added next to a VP8L bitstream")
		case name == VP8Chunk && (w.VP8L != nil || w.chunkPresent(VP8LChunk)):
			return errors.New("webp: a VP8 chunk cannot be added next to a VP8L bitstream")
		case name == VP8LChunk && (w.VP8 != nil || w.chunkPresent(VP8Chunk)):
			return errors.New("webp: a VP8L chunk cannot be added next to a VP8 bitstream")
		}
		switch name {
		case VP8XChunk:
			if len(data) != 10 {
				return errors.Errorf("webp: a VP8X payload is 10 bytes, got %d", len(data))
			}
		case ANIMChunk:
			if len(data) != 6 {
				return errors.Errorf("webp: an ANIM payload is 6 bytes, got %d", len(data))
			}
		case ALPHChunk:
			if len(data) == 0 {
				return errors.New("webp: an ALPH payload needs its flag byte")
			}
		}
	case ANMFChunk:
		frame := ANMF{maxChunkSize: w.maxChunkSize}
		if err := frame.Parse(&RawChunk{Name: name, Data: data}); err != nil {
			return errors.Wrapf(err, "webp: the %q payload is not a valid frame (use AddFrame instead)", string(name))
		}
	}
	c := &riffChunk{name: name, data: data}
	names := make([]FourCC, len(w.origChunks))
	for i, oc := range w.origChunks {
		names[i] = oc.name
	}
	insertAt := chunkInsertIndex(names, chunkInsertRank(name))
	n := len(w.origChunks) + 1
	chunks := make([]*riffChunk, 0, n)
	consumed := make([]bool, 0, n)
	deleted := make([]bool, 0, n)
	for i := range w.origChunks {
		if i == insertAt {
			chunks = append(chunks, c)
			consumed = append(consumed, false)
			deleted = append(deleted, false)
		}
		chunks = append(chunks, w.origChunks[i])
		consumed = append(consumed, w.origConsumed[i])
		deleted = append(deleted, w.origDeleted[i])
	}
	if insertAt == len(w.origChunks) {
		chunks = append(chunks, c)
		consumed = append(consumed, false)
		deleted = append(deleted, false)
	}
	w.origChunks, w.origConsumed, w.origDeleted = chunks, consumed, deleted
	w.rebuildChunks()
	return nil
}

// rebuildChunks 从原始顺序快照重新推导仍需保留的原始 chunk 列表，
// 跳过已消费或已删除的条目。
// rebuildChunks re-derives the remaining raw chunk list from the original
// order snapshot, skipping the entries that were consumed or deleted.
func (w *Webp) rebuildChunks() {
	rest := make([]*riffChunk, 0, len(w.origChunks))
	for i, c := range w.origChunks {
		if w.origConsumed[i] || w.origDeleted[i] {
			continue
		}
		rest = append(rest, c)
	}
	w.chunks = rest
}
