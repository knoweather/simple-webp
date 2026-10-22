package simple_webp

import "github.com/pkg/errors"

// maxCanvasArea 是规范允许的最大画布面积：2^32-1 个像素。
//
// maxCanvasArea is the largest canvas the spec allows: 2^32-1 pixels.
const maxCanvasArea = 1<<32 - 1

// validateStrict 逐条校验 WebP 容器规范里 MUST 级别的约束，供
// ParseOptions.Strict 使用；发现第一条违规即返回带类型码的错误。
//
// validateStrict checks the spec MUST rules one by one for ParseOptions.Strict
// and reports the first violation together with the chunk type code.
func (w *Webp) validateStrict() error {
	// 1. 扩展特性必须由 VP8X 承载：没有 VP8X 却出现扩展 chunk，属于把扩展
	// chunk 混进简单格式，规范不允许。
	// Extended chunks must be introduced by a VP8X; mixing them into the simple
	// format is not allowed.
	if w.VP8X == nil {
		for _, c := range w.origChunks {
			switch c.name {
			case ICCPChunk, EXIFChunk, XMPChunk, ALPHChunk, ANIMChunk, ANMFChunk:
				return errors.Errorf("invalid webp: %q chunk without a VP8X chunk", string(c.name))
			}
		}
	}

	// 2. 重建类 chunk 的顺序：VP8X → ICCP → ANIM → ANMF → ALPH → VP8/VP8L。
	// 规范只要求这七种 chunk 按序出现，元数据与未知类型「MAY appear out of
	// order」，所以它们不参与排序检查。VP8X 至多一个且必须在最前。
	// Reconstructing chunks go VP8X, ICCP, ANIM, ANMF, ALPH, VP8/VP8L. Only
	// those seven have to appear in that order; metadata and unknown chunks
	// "MAY appear out of order", so they take no part in the check. There is at
	// most one VP8X and it comes first.
	prev := -1
	var seenVP8X int
	for _, c := range w.origChunks {
		rank := chunkInsertRank(c.name)
		if rank >= metadataChunkRank {
			continue
		}
		if rank < prev {
			return errors.Errorf("invalid webp: %q chunk is out of the container order", string(c.name))
		}
		prev = rank
		if c.name == VP8XChunk {
			seenVP8X++
			if seenVP8X > 1 {
				return errors.New("invalid webp: more than one VP8X chunk")
			}
		}
	}

	// 3. VP8X 的保留位必须为 0，画布面积不超过 2^32-1。
	// The VP8X reserved bits must be zero and the canvas area must stay within
	// 2^32-1 pixels.
	for _, c := range w.origChunks {
		if c.name != VP8XChunk || len(c.data) < 10 {
			continue
		}
		if c.data[0]&0xc1 != 0 {
			return errors.Errorf("invalid webp: VP8X reserved flag bits are set (0x%02x)", c.data[0])
		}
		if c.data[1] != 0 || c.data[2] != 0 || c.data[3] != 0 {
			return errors.New("invalid webp: VP8X reserved bits are set")
		}
		break
	}
	if w.Width > 0 && w.Height > 0 && int64(w.Width)*int64(w.Height) > maxCanvasArea {
		return errors.Errorf("invalid webp: canvas %dx%d exceeds the maximum area", w.Width, w.Height)
	}

	// 4. 动画标志与 ANIM chunk 必须同时存在。ANIM chunk 存在但标志没置位时，
	// 规范要求「MUST be ignored」，因此不算违规。
	// The animation flag and the ANIM chunk imply each other. An ANIM chunk
	// without the flag "MUST be ignored" according to the spec, so it is not
	// a violation.
	if w.VP8X != nil && w.VP8X.HasAnimation() && w.ANIM == nil {
		return errors.New("invalid webp: animation flag without an ANIM chunk")
	}

	// 5. 帧矩形必须完全落在画布内；VP8L 帧不得再带 ALPH；帧标志的保留位
	// 必须为 0；ALPH 必须排在位流之前。
	// A frame must fit the canvas, a VP8L frame must not carry an ALPH, the
	// reserved bits of the frame flags must be zero, and the ALPH sub-chunk
	// must precede the bitstream.
	for i, f := range w.ANMFs {
		if f.Width <= 0 || f.Height <= 0 || f.X < 0 || f.Y < 0 {
			return errors.Errorf("invalid webp: frame %d has an invalid rectangle %d,%d %dx%d", i, f.X, f.Y, f.Width, f.Height)
		}
		if w.VP8X != nil && (f.X+f.Width > w.VP8X.CanvasWidth || f.Y+f.Height > w.VP8X.CanvasHeight) {
			return errors.Errorf("invalid webp: frame %d at %d,%d %dx%d does not fit the %dx%d canvas",
				i, f.X, f.Y, f.Width, f.Height, w.VP8X.CanvasWidth, w.VP8X.CanvasHeight)
		}
		if f.VP8L != nil && f.ALPH != nil {
			return errors.Errorf("invalid webp: frame %d carries both ALPH and VP8L", i)
		}
		if f.flags&0xfc != 0 {
			return errors.Errorf("invalid webp: frame %d has reserved flag bits set (0x%02x)", i, f.flags)
		}
		seenImage := false
		for _, sub := range f.subs {
			if sub.name == VP8Chunk || sub.name == VP8LChunk {
				seenImage = true
				continue
			}
			if sub.name == ALPHChunk && seenImage {
				return errors.Errorf("invalid webp: frame %d carries ALPH after the image bitstream", i)
			}
		}
	}

	// 6. 顶层图像数据：静态图由「一个可选的 ALPH + 一个位流子块」组成，
	// 动图则由若干 ANMF 帧组成，两者不能混着出现。
	// The top level image data of a still image is made up of one optional
	// ALPH and one bitstream sub-chunk, while an animated image is made up of
	// ANMF frames; the two cannot be mixed.
	var nVP8, nVP8L, nALPH int
	for _, c := range w.origChunks {
		switch c.name {
		case VP8Chunk:
			nVP8++
		case VP8LChunk:
			nVP8L++
		case ALPHChunk:
			nALPH++
		}
	}
	switch {
	case nVP8 > 1:
		return errors.New("invalid webp: more than one VP8 chunk")
	case nVP8L > 1:
		return errors.New("invalid webp: more than one VP8L chunk")
	case nALPH > 1:
		return errors.New("invalid webp: more than one ALPH chunk")
	case nVP8 > 0 && nVP8L > 0:
		return errors.New("invalid webp: both a VP8 and a VP8L chunk")
	}
	if len(w.ANMFs) > 0 && (nVP8 > 0 || nVP8L > 0 || nALPH > 0) {
		return errors.New("invalid webp: image data next to animation frames")
	}
	if nALPH > 0 && nVP8L > 0 {
		return errors.New("invalid webp: an ALPH chunk next to a VP8L chunk")
	}

	// 7. ALPH 标志字节的保留位必须为 0。
	// The reserved bits of the ALPH flag byte must be zero.
	if w.ALPH != nil && w.ALPH.reserved != 0 {
		return errors.Errorf("invalid webp: ALPH reserved bits are set (0x%02x)", w.ALPH.reserved)
	}

	// 8. VP8X 的特性标志必须与文件里实际存在的东西一致。
	// The feature flags of the VP8X must agree with what the file holds.
	if err := w.validateFlags(); err != nil {
		return err
	}

	// 9. 填充字节必须为 0，顶层 chunk 与 ANMF 内的子 chunk 都要查。
	// Padding bytes must be zero, both for the top level chunks and the
	// sub-chunks inside an ANMF frame.
	for _, c := range w.origChunks {
		if len(c.data)&1 == 1 && c.pad != 0 {
			return errors.Errorf("invalid webp: %q chunk has a non-zero padding byte (0x%02x)", string(c.name), c.pad)
		}
	}
	for i, f := range w.ANMFs {
		for _, sub := range f.subs {
			if len(sub.data)&1 == 1 && sub.pad != 0 {
				return errors.Errorf("invalid webp: frame %d sub-chunk %q has a non-zero padding byte (0x%02x)",
					i, string(sub.name), sub.pad)
			}
		}
	}
	return nil
}

// validateFlags 校验 VP8X 的特性标志与文件里实际存在的 chunk 一致：标志置位
// 就必须能找到对应的 chunk。
//
// validateFlags checks that the feature flags of the VP8X agree with the
// chunks the file actually holds: a flag that is set must have its chunk.
func (w *Webp) validateFlags() error {
	if w.VP8X == nil {
		return nil
	}
	var hasICCP, hasEXIF, hasXMP, hasAlpha bool
	for _, c := range w.origChunks {
		switch c.name {
		case ICCPChunk:
			hasICCP = true
		case EXIFChunk:
			hasEXIF = true
		case XMPChunk:
			hasXMP = true
		case ALPHChunk:
			hasAlpha = true
		}
	}
	if w.VP8L != nil && w.VP8L.AlphaUsed {
		hasAlpha = true
	}
	for _, f := range w.ANMFs {
		// 帧的透明度可以来自 ALPH 子 chunk，也可以来自 VP8L 码流自己的
		// alpha_is_used 位。
		// A frame carries its transparency either in an ALPH sub-chunk or in
		// the alpha_is_used bit of its own VP8L bitstream.
		if f.ALPH != nil {
			hasAlpha = true
		}
		if f.VP8L != nil && f.VP8L.AlphaUsed {
			hasAlpha = true
		}
	}
	if w.VP8X.HasICC() && !hasICCP {
		return errors.New("invalid webp: the VP8X ICC flag is set but there is no ICCP chunk")
	}
	if w.VP8X.HasEXIF() && !hasEXIF {
		return errors.New("invalid webp: the VP8X EXIF flag is set but there is no EXIF chunk")
	}
	if w.VP8X.HasXMP() && !hasXMP {
		return errors.New("invalid webp: the VP8X XMP flag is set but there is no XMP chunk")
	}
	if w.VP8X.HasAlpha() && !hasAlpha {
		return errors.New("invalid webp: the VP8X alpha flag is set but the image has no transparency")
	}
	return nil
}
