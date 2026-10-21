package simple_webp

import (
	"bytes"
	"io"
	"math"

	"github.com/pkg/errors"
)

// countingWriter counts the bytes written to the underlying writer.
//
// countingWriter 统计写入底层 writer 的字节数，用于向调用方报告实际写出的长度。
type countingWriter struct {
	w io.Writer
	n int64
}

// Write forwards p to the underlying writer and accumulates the written count.
//
// Write 把 p 转发给底层 writer 并累计已写字节数。
func (cw *countingWriter) Write(p []byte) (int, error) {
	n, err := cw.w.Write(p)
	cw.n += int64(n)
	return n, err
}

// WriteTo writes the webp stream of the current state, implementing
// io.WriterTo. Chunks that were consumed while parsing are re-serialized from
// their parsed structs, so modifications made to the structs (or chunks
// added/deleted through AddRawChunk / DeleteRawChunk) are reflected, while the
// chunks that were left raw are copied verbatim. Parsing and writing back a
// file without touching anything reproduces it byte for byte.
//
// The image payload (VP8 / VP8L / ALPH) is never re-encoded, so editing the
// metadata chunks does not touch the pixels at all.
//
// WriteTo 把当前状态的 webp 流写出，实现 io.WriterTo。解析时被消费（consumed）的 chunk
// 会从解析结构体重新序列化，因此对结构体的修改、以及通过 AddRawChunk / DeleteRawChunk
// 增删的 chunk 都会反映到输出；未被消费、保持原始形态的 chunk 则逐字节原样拷贝。
// 不做任何修改地解析再写回，可逐字节复现原文件。
//
// 图像载荷（VP8 / VP8L / ALPH）永远不会被重新编码，因此编辑元数据 chunk 完全不触及像素数据。
func (w *Webp) WriteTo(out io.Writer) (int64, error) {
	if w.origChunks == nil {
		return 0, errors.New("webp is not parsed")
	}
	if len(w.origChunks) == 0 {
		// 空容器（NewWebp 之后还没放图像数据）写出来是非法文件，直接报错。
		// An empty container (NewWebp without image data yet) would serialize to
		// an invalid file, so it is an error.
		return 0, errors.New("webp: no chunks to write")
	}
	names := make([]FourCC, 0, len(w.origChunks))
	payloads := make([][]byte, 0, len(w.origChunks))
	var anmfIdx int
	for i, c := range w.origChunks {
		if w.origDeleted[i] {
			continue
		}
		if !w.origConsumed[i] {
			names = append(names, c.name)
			payloads = append(payloads, c.data)
			continue
		}
		data, err := w.emitConsumed(c, &anmfIdx)
		if err != nil {
			return 0, err
		}
		names = append(names, c.name)
		payloads = append(payloads, data)
	}

	// 修正扩展特性 chunk 的序列化：直接赋值的 ICCP/EXIF/XMP 即使原文件没有
	// 也要写出，并在存在扩展特性时保证有一个标志位正确的 VP8X。
	// Fix extended-feature serialization: directly assigned ICCP/EXIF/XMP must
	// be written even when the original file had none, and a correctly flagged
	// VP8X is guaranteed whenever an extended feature is present.
	names, payloads = w.finalizeWrite(names, payloads)

	// the RIFF size field counts everything after it: the "WEBP" tag and all
	// chunks with their headers and padding
	// RIFF 大小字段统计其后的全部内容："WEBP" 标签与所有 chunk（含头部和填充）
	total := 4
	for _, p := range payloads {
		total += chunkSize(len(p))
	}
	if int64(total) > math.MaxUint32 {
		return 0, errors.New("webp stream too large")
	}

	var head [12]byte
	copy(head[:4], riffTag)
	by.PutUint32(head[4:8], uint32(total))
	copy(head[8:], webpTag)

	cw := &countingWriter{w: out}
	if _, err := cw.Write(head[:]); err != nil {
		return cw.n, err
	}
	for i := range payloads {
		if err := writeRiffChunk(cw, names[i], payloads[i]); err != nil {
			return cw.n, err
		}
	}
	return cw.n, nil
}

// emitConsumed builds the payload of a consumed chunk from its parsed struct,
// falling back to the original raw data when the struct field is nil (chunk
// present in the file but never filled in).
//
// emitConsumed 依据解析结构体重建被消费 chunk 的载荷；若对应字段为 nil（文件中存在
// 该 chunk 但从未填入），则回退为原始字节。
func (w *Webp) emitConsumed(c *riffChunk, anmfIdx *int) ([]byte, error) {
	switch c.name {
	case VP8XChunk:
		if w.VP8X == nil {
			return c.data, nil
		}
		return w.VP8X.data(), nil

	case VP8Chunk:
		if w.VP8 == nil {
			return c.data, nil
		}
		return w.VP8.Data, nil

	case VP8LChunk:
		if w.VP8L == nil {
			return c.data, nil
		}
		return w.VP8L.Data, nil

	case ALPHChunk:
		if w.ALPH == nil {
			return c.data, nil
		}
		return append([]byte{w.ALPH.header()}, w.ALPH.Data...), nil

	case ANIMChunk:
		if w.ANIM == nil {
			return c.data, nil
		}
		return w.ANIM.data(), nil

	case ANMFChunk:
		if *anmfIdx >= len(w.ANMFs) {
			return c.data, nil
		}
		frame := w.ANMFs[*anmfIdx]
		*anmfIdx++
		return frame.data(), nil

	case ICCPChunk:
		if w.ICCP == nil {
			return c.data, nil
		}
		return w.ICCP.Data, nil

	case EXIFChunk:
		if w.EXIF == nil {
			return c.data, nil
		}
		return w.EXIF.Data, nil

	case XMPChunk:
		if w.XMP == nil {
			return c.data, nil
		}
		return w.XMP.Data, nil
	}
	// a custom chunk type parsed by the user has no re-serializer here, keep
	// its original raw data
	// 用户自行解析的自定义 chunk 在这里没有对应的序列化器，保留其原始数据
	return c.data, nil
}

// MarshalBytes returns the webp stream of the current state as a byte slice,
// see WriteTo for the details.
//
// MarshalBytes 以字节切片返回当前状态的 webp 流，细节见 WriteTo。
func (w *Webp) MarshalBytes() ([]byte, error) {
	var buf bytes.Buffer
	if _, err := w.WriteTo(&buf); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

// finalizeWrite 修正扩展特性 chunk 的序列化，解决三个真实使用痛点：
//
//  1. 直接给 w.ICCP / w.EXIF / w.XMP 赋值（原文件并不含对应 chunk）时，写回
//     会丢失这些数据——WriteTo 只遍历解析时已有的 origChunks，新赋的结构体
//     永远不会被 emitConsumed 命中。本函数把这类结构体补写成 chunk。
//  2. 纯 VP8L（或任何不含 VP8X）的文件在补加 metadata 时必须先有一个 VP8X
//     chunk 并置上对应标志位（ICCP/EXIF/XMP 都只能藏在 VP8X 之后，且依赖其
//     标志位被读取器索引）。本函数在缺 VP8X 时自动创建，并以当前状态置位。
//  3. 补写的 chunk 按类型排位决定插入点（见 chunkInsertRank）：VP8X 最前，
//     ICCP 紧随 VP8X 且排在 ANIM/ANMF/图像数据之前，EXIF 与 XMP 落在图像
//     数据之后的尾部。
//
// 标志位处理是「只置位、不清除」：仅把确实存在的扩展特性对应的位打开，绝不
// 关闭原本合法的标志位，从而不影响未做修改的流。删除 metadata 时由
// DeleteRawChunk 负责清除对应标志位。
//
// finalizeWrite fixes the serialization of extended-feature chunks, addressing
// three real pain points: (1) a directly assigned w.ICCP/w.EXIF/w.XMP that the
// original file did not contain was silently dropped by WriteTo, which only
// walks the chunks parsed from the source; this function writes those structs
// back. (2) a file without a VP8X chunk (e.g. a plain VP8L) must gain a VP8X
// with the right flag bits before metadata can be embedded — ICCP/EXIF/XMP live
// only after VP8X and are indexed through its flags. This function creates a
// VP8X when missing and sets its flags from the current state. Flag handling is
// "set, never clear": it only turns on bits for features that are truly present,
// so it never disturbs a previously valid stream. DeleteRawChunk clears the bits
// when metadata is removed. (3) a synthesized chunk goes to the position its
// type dictates (see chunkInsertRank): VP8X first, ICCP right after VP8X and
// before ANIM/ANMF and the image data, EXIF and XMP after the image data at the
// end.
func (w *Webp) finalizeWrite(names []FourCC, payloads [][]byte) ([]FourCC, [][]byte) {
	present := make(map[FourCC]bool, len(names))
	for _, n := range names {
		present[n] = true
	}

	// 置位规则：已存在于流中的 chunk 的标志位一律保持原样（这样未做修改的解析
	// 结果仍字节级往返）；只对本次新写入的 chunk 补齐标志位。
	// Flag rule: flag bits of chunks already in the stream are never touched (so
	// an untouched parse still round-trips byte for byte); only chunks written
	// anew get their flag bits set.

	// 计算需要新写入的扩展 chunk（结构体已赋值，但输出里还没有对应 chunk）。
	// Compute the extended chunks to synthesize (struct assigned but no chunk yet).
	var synth []FourCC
	var newFlags uint8
	for _, name := range []FourCC{ICCPChunk, EXIFChunk, XMPChunk} {
		if present[name] {
			continue
		}
		switch name {
		case ICCPChunk:
			if w.ICCP != nil {
				synth = append(synth, name)
				newFlags |= FlagICC
			}
		case EXIFChunk:
			if w.EXIF != nil {
				synth = append(synth, name)
				newFlags |= FlagEXIF
			}
		case XMPChunk:
			if w.XMP != nil {
				synth = append(synth, name)
				newFlags |= FlagXMP
			}
		}
	}

	// 统计输出中实际存在的扩展特性（已赋值结构体，或流中已有的 chunk——后者包括
	// 通过 AddRawChunk 直接插入的情形）。
	// Tally the extended features present in the output (assigned structs or
	// chunks already in the stream — the latter covers AddRawChunk inserts).
	var presentFlags uint8
	if w.ICCP != nil || present[ICCPChunk] {
		presentFlags |= FlagICC
	}
	if w.EXIF != nil || present[EXIFChunk] {
		presentFlags |= FlagEXIF
	}
	if w.XMP != nil || present[XMPChunk] {
		presentFlags |= FlagXMP
	}
	if w.HasAlpha || (w.VP8L != nil && w.VP8L.AlphaUsed) || w.ALPH != nil || present[ALPHChunk] {
		presentFlags |= FlagAlpha
	}
	if w.ANIM != nil || w.HasAnimation || len(w.ANMFs) > 0 || present[ANIMChunk] {
		presentFlags |= FlagAnimation
	}

	// 只在需要新写入时才动 VP8X：缺则创建，已存在则仅补置新标志位。
	// Only touch VP8X when something new is being written: create it if absent,
	// otherwise just OR in the freshly-added flag bits.
	if w.VP8X == nil {
		// 缺 VP8X 且输出里确实需要它时才创建：有新写入的扩展 chunk，或流中已有
		// 只能藏在 VP8X 之后的扩展 chunk（ICCP/EXIF/XMP/ANIM/ALPH，包括通过
		// AddRawChunk 插入的）。创建时用当前状态整体置位（含 alpha、animation）。
		// 只有alpha 的纯 VP8L 文件不在此列，否则会凭空多出一个 VP8X，破坏字节级
		// 往返。
		// Create it when VP8X is missing and the output actually needs one: either
		// an extended chunk is being written now, or the stream already carries
		// one that can only live behind a VP8X (ICCP/EXIF/XMP/ANIM/ALPH, including
		// AddRawChunk inserts). The flags of the whole current state (alpha and
		// animation included) go into the new chunk. A plain alpha-only VP8L file
		// is not in this set, so it keeps round-tripping byte for byte.
		needsVP8X := len(synth) > 0 ||
			present[ICCPChunk] || present[EXIFChunk] || present[XMPChunk] ||
			present[ANIMChunk] || present[ALPHChunk]
		if needsVP8X {
			w.VP8X = &VP8X{Flags: presentFlags, CanvasWidth: w.Width, CanvasHeight: w.Height}
		}
	} else if newFlags != 0 {
		w.VP8X.Flags |= newFlags
		// VP8X 已在输出中：循环阶段已用旧标志位把它序列化进 payloads，
		// 这里需就地刷新为更新后的载荷。
		// VP8X is already in the output: the loop already serialized it with
		// the old flags, so refresh its payload here.
		if present[VP8XChunk] {
			for i, n := range names {
				if n == VP8XChunk {
					payloads[i] = w.VP8X.data()
					break
				}
			}
		}
	}

	// 若新创建了 VP8X，把它插到最前（必须在 ICCP/EXIF/XMP 之前）。
	// If a VP8X was just created, prepend it (it must precede ICCP/EXIF/XMP).
	if w.VP8X != nil && !present[VP8XChunk] {
		synth = append([]FourCC{VP8XChunk}, synth...)
	}
	if len(synth) == 0 {
		return names, payloads
	}

	// 构造合成 chunk 的载荷。
	// Build the payloads for the synthesized chunks.
	synthData := make([][]byte, len(synth))
	for i, name := range synth {
		switch name {
		case VP8XChunk:
			synthData[i] = w.VP8X.data()
		case ICCPChunk:
			synthData[i] = w.ICCP.Data
		case EXIFChunk:
			synthData[i] = w.EXIF.Data
		case XMPChunk:
			synthData[i] = w.XMP.Data
		}
	}

	// 逐个按排位插入：VP8X 最前，ICCP 紧随其后，EXIF/XMP 落在图像数据之后的
	// 尾部。下标先照当前列表算出，再改动 names/payloads；synthData 取同一个 i。
	// Insert the chunks one at a time by rank: VP8X first, ICCP right after it,
	// EXIF/XMP at the end after the image data. The index is computed from the
	// lists as they stand before they are modified, and synthData uses the same i.
	for i, name := range synth {
		idx := chunkInsertIndex(names, chunkInsertRank(name))
		names = append(names, "")
		copy(names[idx+1:], names[idx:])
		names[idx] = name
		payloads = append(payloads, nil)
		copy(payloads[idx+1:], payloads[idx:])
		payloads[idx] = synthData[i]
	}
	return names, payloads
}
