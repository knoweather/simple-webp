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

	// 2. 重建类 chunk 的顺序：VP8X → ICCP → ANIM → ANMF → ALPH → VP8/VP8L，
	// 元数据与未知类型排在图像数据之后；VP8X 至多一个且必须在最前。
	// Reconstructing chunks go VP8X, ICCP, ANIM, ANMF, ALPH, VP8/VP8L, then the
	// metadata and unknown chunks; there is at most one VP8X and it comes first.
	prev := -1
	var seenVP8X int
	for _, c := range w.origChunks {
		rank := chunkInsertRank(c.name)
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

	// 4. animation 标志与 ANIM chunk 必须同时存在。
	// The animation flag and the ANIM chunk imply each other.
	if w.VP8X != nil && w.VP8X.HasAnimation() && w.ANIM == nil {
		return errors.New("invalid webp: animation flag without an ANIM chunk")
	}
	if w.ANIM != nil && w.VP8X != nil && !w.VP8X.HasAnimation() {
		return errors.New("invalid webp: ANIM chunk without the animation flag")
	}

	// 5. 帧矩形必须完全落在画布内；VP8L 帧不得再带 ALPH。
	// A frame must fit the canvas, and a VP8L frame must not carry an ALPH.
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
	}

	// 6. 填充字节必须为 0，顶层 chunk 与 ANMF 内的子 chunk 都要查。
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
