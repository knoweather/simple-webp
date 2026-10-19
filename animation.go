package simple_webp

import (
	"image"

	"github.com/pkg/errors"
)

// Frames 逐帧解码动图，返回按画布尺寸合成的各帧（NRGBA）及其显示时长
// （毫秒）。每帧先解码为静止图像再混合到画布上，随后克隆整块画布作为输出帧。
// maxPixels 像素预算会透传给子解码器，用于限制单帧解码规模。
//
// Frames decodes every frame of an animated file and returns the composited
// frames at canvas size together with their display duration in milliseconds.
// Each frame is decoded as a still image, blended onto the canvas, and the
// whole canvas is then cloned as the output frame. The maxPixels budget is
// forwarded to the sub-decoders to cap per-frame decoding size.
func (w *Webp) Frames(maxPixels int) ([]*image.NRGBA, []int, error) {
	if len(w.ANMFs) == 0 {
		return nil, nil, errors.New("webp: not an animated file")
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
