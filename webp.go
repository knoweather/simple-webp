// Package simple_webp 解析与编辑 WebP（RIFF）容器：读取 RIFF 头与全部 chunk，
// 解码容器级 chunk（VP8X、VP8、VP8L、ALPH、ANIM、ANMF、ICCP、EXIF、XMP），
// 并支持在不重新编码像素的前提下增删元数据 chunk、按原始顺序写回整个流。
//
// Package simple_webp parses and edits WebP (RIFF) containers: it reads the
// RIFF header and every chunk, decodes the container-level chunks (VP8X, VP8,
// VP8L, ALPH, ANIM, ANMF, ICCP, EXIF, XMP), and supports adding or removing
// metadata chunks and writing the stream back in its original order without
// re-encoding the pixels.
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
	cr := &countingReader{r: r}
	var head [12]byte
	if _, err := io.ReadFull(cr, head[:]); err != nil {
		return nil, errors.WithMessage(err, "invalid webp: can not read RIFF header")
	}
	if string(head[:4]) != string(riffTag) || string(head[8:12]) != string(webpTag) {
		return nil, ErrNotWebP
	}
	declared := by.Uint32(head[4:8])

	var w = &Webp{}
	for {
		c, err := readRiffChunk(cr)
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
	if len(verifySize) > 0 && verifySize[0] {
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
		frame := &ANMF{}
		if err := frame.Parse(c); err != nil {
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
	if err := target.Parse(c); err != nil {
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
		if err := c.Parse(cur); err != nil {
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

// DeleteRawChunk 从流中删除所有指定类型的 chunk 并返回删除数量。
// 既适用于原生解码的元数据 chunk（ICCP、EXIF、XMP），也适用于未知的
// 原始 chunk；删除后调用 WriteTo 写出的流不再包含这些 chunk。
// DeleteRawChunk removes every chunk of the given type from the stream and
// returns how many chunks were removed. It works for the metadata chunks that
// are decoded natively (ICCP, EXIF, XMP) as well as for unknown raw chunks; a
// WriteTo afterwards writes the stream without those chunks.
func (w *Webp) DeleteRawChunk(name FourCC) int {
	removed := 0
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
			if w.VP8X != nil {
				w.VP8X.Flags &^= FlagICC
			}
		}
	case EXIFChunk:
		if w.EXIF != nil {
			w.EXIF = nil
			removed++
			if w.VP8X != nil {
				w.VP8X.Flags &^= FlagEXIF
			}
		}
	case XMPChunk:
		if w.XMP != nil {
			w.XMP = nil
			removed++
			if w.VP8X != nil {
				w.VP8X.Flags &^= FlagXMP
			}
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
		if c.name != name || w.origDeleted[i] {
			continue
		}
		// 已消费的 chunk 仅在其结构体被清空时才跳过，
		// 未消费的 chunk 仅当它已从原始列表移除时才跳过
		// a consumed chunk is skipped only when its struct is gone, an
		// unconsumed one only when it was removed from the raw list
		if w.origConsumed[i] {
			switch name {
			case ICCPChunk, EXIFChunk, XMPChunk:
				w.origDeleted[i] = true
			}
			continue
		}
		if rawRemoved > 0 {
			w.origDeleted[i] = true
		}
	}
	return removed
}

// AddRawChunk 把一个新的原始 chunk 插入流中，位置在第一个图像 chunk
// （VP8、VP8L 或 ANMF）之前；没有图像 chunk 时追加到末尾。可用于给已解析
// 的文件补充 EXIF 或 XMP 等 chunk。name 必须是合法 FourCC，data 不得超过
// maxChunkSize，且 Webp 必须来自成功的 ParseWebp。
// AddRawChunk inserts a new raw chunk into the stream before the first image
// chunk (VP8, VP8L or ANMF); if there is no image chunk it is appended. Use it
// e.g. to add an EXIF or XMP chunk to an already parsed file. name must be a
// valid FourCC, data must fit in maxChunkSize and the Webp must come from a
// successful ParseWebp.
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
	c := &riffChunk{name: name, data: data}
	insertAt := len(w.origChunks)
	for i, oc := range w.origChunks {
		switch oc.name {
		case VP8Chunk, VP8LChunk, ANMFChunk:
			insertAt = i
		}
		if insertAt != len(w.origChunks) {
			break
		}
	}
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
