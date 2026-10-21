package simple_webp

import (
	"image"
	"image/color"

	"github.com/pkg/errors"
)

// defaultMaxTotalPixels 限制一次 Frames 调用返回的全部帧像素总数（画布像素 ×
// 帧数）：1<<27 个像素对应 512 MB 的 NRGBA 缓冲，与单张图的预算一致。
//
// defaultMaxTotalPixels bounds the pixels of every frame returned by one Frames
// call (canvas pixels times frame count): 1<<27 pixels is 512 MB of NRGBA
// buffers, the same budget as a single image.
const defaultMaxTotalPixels = 1 << 27

// FrameOptions 调节动图解码。
//
// FrameOptions tunes animation decoding.
type FrameOptions struct {
	// MaxPixels 拒绝画布像素数超过该值的动画，0 表示 defaultMaxPixels。
	// 画布尺寸来自 VP8X，可能远大于实际帧，所以它必须单独受预算约束。
	//
	// MaxPixels rejects animations whose canvas is larger; 0 uses
	// defaultMaxPixels. The canvas comes from VP8X and can be far larger than
	// the frames themselves, so it needs a budget of its own.
	MaxPixels int
	// MaxTotalPixels 拒绝画布像素 × 帧数超过该值的动画，0 表示
	// defaultMaxTotalPixels。Frames 会一次性返回全部帧，总量必须有上限。
	//
	// MaxTotalPixels rejects animations whose canvas times frame count exceeds
	// it; 0 uses defaultMaxTotalPixels. Frames returns every frame at once, so
	// the total has to be bounded.
	MaxTotalPixels int
}

// AnimationInfo 描述一段动画的画布参数与帧数，不涉及像素解码。
//
// AnimationInfo describes the canvas parameters and the frame count of an
// animation without decoding any pixels.
type AnimationInfo struct {
	// Width、Height 是画布的像素尺寸。/ Width and Height are the canvas size in pixels.
	Width, Height int
	// BackgroundColor 是 ANIM 声明的背景色（straight alpha）。
	// BackgroundColor is the background colour ANIM declares (straight alpha).
	BackgroundColor color.NRGBA
	// LoopCount 是最后一帧之后的循环次数，0 表示无限循环。
	// LoopCount is how often playback repeats after the last frame; 0 loops forever.
	LoopCount uint16
	// FrameCount 是 ANMF 帧的数量。/ FrameCount is the number of ANMF frames.
	FrameCount int
}

// frameLimits 校验画布与「画布 × 帧数」预算，返回交给子解码器的像素上限。
// 超限返回 ErrImageTooLarge，任何像素缓冲都不会被分配。
//
// frameLimits checks the canvas and the canvas-times-frames budgets and returns
// the pixel cap for the sub-decoders. Exceeding a budget returns
// ErrImageTooLarge before any pixel buffer is allocated.
func (w *Webp) frameLimits(opt FrameOptions, frames int) (int, error) {
	if len(w.ANMFs) == 0 {
		return 0, errors.New("webp: not an animated file")
	}
	maxPixels := opt.MaxPixels
	if maxPixels <= 0 {
		maxPixels = defaultMaxPixels
	}
	if w.Width <= 0 || w.Height <= 0 {
		return 0, errors.Errorf("webp: bad canvas size %dx%d", w.Width, w.Height)
	}
	canvasPixels := int64(w.Width) * int64(w.Height)
	if canvasPixels > int64(maxPixels) {
		return 0, errors.Wrapf(ErrImageTooLarge, "%dx%d canvas", w.Width, w.Height)
	}
	maxTotal := opt.MaxTotalPixels
	if maxTotal <= 0 {
		maxTotal = defaultMaxTotalPixels
	}
	if canvasPixels*int64(frames) > int64(maxTotal) {
		return 0, errors.Wrapf(ErrImageTooLarge, "%dx%d canvas x %d frames", w.Width, w.Height, frames)
	}
	return maxPixels, nil
}

// Frames 逐帧解码动图，返回按画布尺寸合成的各帧（NRGBA）及其显示时长
// （毫秒）。maxPixels 像素预算同时约束画布尺寸与单帧解码规模，0 表示
// defaultMaxPixels；等价于 FramesWith(FrameOptions{MaxPixels: maxPixels})。
//
// Frames decodes every frame of an animated file and returns the composited
// frames at canvas size together with their display duration in milliseconds.
// The maxPixels budget caps both the canvas size and the per-frame decoding
// size; 0 means defaultMaxPixels. It is FramesWith(FrameOptions{MaxPixels:
// maxPixels}).
func (w *Webp) Frames(maxPixels int) ([]*image.NRGBA, []int, error) {
	return w.FramesWith(FrameOptions{MaxPixels: maxPixels})
}

// FramesWith 按给定选项逐帧解码动图，见 Frames。每帧先解码为静止图像再混合到
// 画布上，随后克隆整块画布作为输出帧；画布尺寸与「画布 × 帧数」的总量都会在
// 分配像素缓冲之前按预算预检，超限返回 ErrImageTooLarge。
//
// FramesWith decodes an animated file frame by frame with the given options,
// see Frames. Each frame is decoded as a still image, blended onto the canvas,
// and the whole canvas is then cloned as the output frame. The canvas size and
// the canvas-times-frames total are checked against the budget before any pixel
// buffer is allocated; exceeding it returns ErrImageTooLarge.
func (w *Webp) FramesWith(opt FrameOptions) ([]*image.NRGBA, []int, error) {
	maxPixels, err := w.frameLimits(opt, len(w.ANMFs))
	if err != nil {
		return nil, nil, err
	}

	canvas := image.NewNRGBA(image.Rect(0, 0, w.Width, w.Height))
	frames := make([]*image.NRGBA, 0, len(w.ANMFs))
	durations := make([]int, 0, len(w.ANMFs))

	for _, f := range w.ANMFs {
		img, err := w.decodeStill(f.VP8, f.VP8L, f.ALPH, maxPixels)
		if err != nil {
			return nil, nil, errors.Wrapf(err, "webp: frame at %d,%d", f.X, f.Y)
		}
		blendFrame(canvas, img, f)
		frames = append(frames, cloneNRGBA(canvas))
		durations = append(durations, f.Duration)
		if f.Dispose {
			bg := uint32(0)
			if w.ANIM != nil {
				bg = w.ANIM.BackgroundColor
			}
			clearFrame(canvas, f, bg)
		}
	}
	return frames, durations, nil
}

// FrameAt 只合成到第 i 帧（0 起）并返回该帧与它的显示时长：中间帧不克隆，
// 因此内存只需要一块画布，而不是「帧数 × 画布」。像素预算与 FramesWith 相同，
// 总量按 i+1 帧计算；i 越界返回错误。
//
// FrameAt composites only up to frame i (zero based) and returns that frame and
// its display duration. The intermediate frames are never cloned, so one canvas
// is enough instead of frames times canvas. The budgets are the ones of
// FramesWith, with the total counted over i+1 frames; an out-of-range index is
// an error.
func (w *Webp) FrameAt(i int, opt FrameOptions) (*image.NRGBA, int, error) {
	if i < 0 || i >= len(w.ANMFs) {
		return nil, 0, errors.Errorf("webp: frame %d out of range (%d frames)", i, len(w.ANMFs))
	}
	maxPixels, err := w.frameLimits(opt, i+1)
	if err != nil {
		return nil, 0, err
	}

	canvas := image.NewNRGBA(image.Rect(0, 0, w.Width, w.Height))
	for n := 0; n <= i; n++ {
		f := w.ANMFs[n]
		img, err := w.decodeStill(f.VP8, f.VP8L, f.ALPH, maxPixels)
		if err != nil {
			return nil, 0, errors.Wrapf(err, "webp: frame at %d,%d", f.X, f.Y)
		}
		blendFrame(canvas, img, f)
		if n == i {
			return cloneNRGBA(canvas), f.Duration, nil
		}
		if f.Dispose {
			bg := uint32(0)
			if w.ANIM != nil {
				bg = w.ANIM.BackgroundColor
			}
			clearFrame(canvas, f, bg)
		}
	}
	return nil, 0, errors.Errorf("webp: frame %d out of range (%d frames)", i, len(w.ANMFs))
}

// AnimationInfo 返回动画的画布尺寸、背景色、循环次数与帧数；非动画文件返回错误。
//
// AnimationInfo returns the canvas size, background colour, loop count and frame
// count of the animation; a non-animated file is an error.
func (w *Webp) AnimationInfo() (AnimationInfo, error) {
	if len(w.ANMFs) == 0 {
		return AnimationInfo{}, errors.New("webp: not an animated file")
	}
	info := AnimationInfo{
		Width:      w.Width,
		Height:     w.Height,
		FrameCount: len(w.ANMFs),
	}
	if w.ANIM != nil {
		bg := w.ANIM.BackgroundColor
		info.BackgroundColor = color.NRGBA{
			R: byte(bg >> 16),
			G: byte(bg >> 8),
			B: byte(bg),
			A: byte(bg >> 24),
		}
		info.LoopCount = w.ANIM.LoopCount
	}
	return info, nil
}

// blendFrame 按 ANMF 中的偏移把一帧合成到画布上：超出画布边界的像素被裁掉；
// f.Blend 为 false 时直接覆盖（不混合），为 true 时按 straight alpha 做
// source-over 混合。
//
// blendFrame composites one frame onto the canvas at its ANMF offset: pixels
// outside the canvas are clipped away. When f.Blend is false the pixels are
// copied verbatim (no blending); when true they are composited with a
// straight-alpha source-over blend.
func blendFrame(canvas, frame *image.NRGBA, f *ANMF) {
	cw, ch := canvas.Bounds().Dx(), canvas.Bounds().Dy()
	fw := frame.Bounds().Dx()
	fh := frame.Bounds().Dy()
	for y := 0; y < fh; y++ {
		cy := f.Y + y
		if cy < 0 || cy >= ch {
			continue
		}
		for x := 0; x < fw; x++ {
			cx := f.X + x
			if cx < 0 || cx >= cw {
				continue
			}
			src := frame.Pix[y*frame.Stride+x*4:]
			dst := canvas.Pix[cy*canvas.Stride+cx*4:]
			if !f.Blend {
				copy(dst[:4], src[:4])
				continue
			}
			blendPixel(dst, src)
		}
	}
}

// blendPixel 就地执行 straight alpha 的 source-over 合成：dst = src over dst。
// sa==255 时整像素直接覆盖；sa==0 时保留目标像素；结果 alpha 为 0 时把
// RGB 一并清零，避免残留脏色。
//
// blendPixel performs an in-place straight-alpha source-over composite:
// dst = src over dst. When sa==255 the destination pixel is overwritten
// outright; when sa==0 it is kept; if the resulting alpha is 0 the RGB
// channels are zeroed too, so no stale color is left behind.
func blendPixel(dst, src []byte) {
	sa := int(src[3])
	if sa == 255 {
		copy(dst[:4], src[:4])
		return
	}
	if sa == 0 {
		return
	}
	da := int(dst[3])
	// 输出 alpha = sa + da * (1 - sa)
	// out alpha = sa + da * (1 - sa)
	outA := sa + da*(255-sa)/255
	if outA == 0 {
		dst[0], dst[1], dst[2], dst[3] = 0, 0, 0, 0
		return
	}
	for c := 0; c < 3; c++ {
		sc := int(src[c])
		dc := int(dst[c])
		// 先按预乘 alpha 求和，再除以输出 alpha 转回 straight alpha
		// premultiplied sum, then back to straight alpha
		v := (sc*sa + dc*da*(255-sa)/255) / outA
		if v > 255 {
			v = 255
		}
		dst[c] = byte(v)
	}
	dst[3] = byte(outA)
}

// clearFrame 按 "dispose to background" 处置方式，把帧矩形区域重置为
// 背景色（ARGB 打包，a 在最高字节）。与混合一样，越界的像素会被裁掉。
//
// clearFrame resets the frame rectangle to the background color, as the
// "dispose to background" disposal method requires. The background is packed
// ARGB (alpha in the top byte); out-of-bounds pixels are clipped just like in
// blending.
func clearFrame(canvas *image.NRGBA, f *ANMF, bg uint32) {
	r := byte(bg >> 16)
	g := byte(bg >> 8)
	b := byte(bg)
	a := byte(bg >> 24)
	cw, ch := canvas.Bounds().Dx(), canvas.Bounds().Dy()
	for y := 0; y < f.Height; y++ {
		cy := f.Y + y
		if cy < 0 || cy >= ch {
			continue
		}
		for x := 0; x < f.Width; x++ {
			cx := f.X + x
			if cx < 0 || cx >= cw {
				continue
			}
			off := cy*canvas.Stride + cx*4
			canvas.Pix[off], canvas.Pix[off+1], canvas.Pix[off+2], canvas.Pix[off+3] = r, g, b, a
		}
	}
}

// cloneNRGBA 返回与 src 同边界的新图像，像素内容整块拷贝，
// 调用方随后修改 src 不会影响返回值。
//
// cloneNRGBA returns a new image with the same bounds as src and a full pixel
// copy, so later mutation of src never affects the returned value.
func cloneNRGBA(src *image.NRGBA) *image.NRGBA {
	dst := image.NewNRGBA(src.Bounds())
	copy(dst.Pix, src.Pix)
	return dst
}
