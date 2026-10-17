package simple_webp

import (
	"image"

	"github.com/pkg/errors"
)

// Frames decodes every frame of an animated file and returns the composited
// frames at canvas size together with their display duration in milliseconds.
// The pixel budget is forwarded to the sub-decoders.
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

// blendFrame composites one frame onto the canvas at its offset.
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

// blendPixel does a source over destination composite with straight alpha.
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
	// out alpha = sa + da * (1 - sa)
	outA := sa + da*(255-sa)/255
	if outA == 0 {
		dst[0], dst[1], dst[2], dst[3] = 0, 0, 0, 0
		return
	}
	for c := 0; c < 3; c++ {
		sc := int(src[c])
		dc := int(dst[c])
		// premultiplied sum, then back to straight alpha
		v := (sc*sa + dc*da*(255-sa)/255) / outA
		if v > 255 {
			v = 255
		}
		dst[c] = byte(v)
	}
	dst[3] = byte(outA)
}

// clearFrame resets the frame rectangle to the background color, as the
// "dispose to background" disposal method requires.
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

func cloneNRGBA(src *image.NRGBA) *image.NRGBA {
	dst := image.NewNRGBA(src.Bounds())
	copy(dst.Pix, src.Pix)
	return dst
}
