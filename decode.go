package simple_webp

import (
	"image"

	"github.com/pkg/errors"

	"github.com/knoweather/simple-webp/internal/vp8l"
)

// PixelFormat selects the pixel layout of the decoded image.
type PixelFormat int

const (
	// PixelFormatNRGBA returns an *image.NRGBA, the straight alpha layout
	// used by the png decoder for RGBA images. It is the default.
	PixelFormatNRGBA PixelFormat = iota
	// PixelFormatRGBA returns an *image.RGBA, the premultiplied layout.
	PixelFormatRGBA
)

// defaultMaxPixels bounds the decoded image size, 1<<27 pixels is a 11585 by
// 11585 image and 512 MB of NRGBA pixels.
const defaultMaxPixels = 1 << 27

// DecodeOptions tunes the pixel decoding.
type DecodeOptions struct {
	// Format selects the returned pixel layout.
	Format PixelFormat
	// Frame selects the frame of an animated file, 0 is the first frame.
	Frame int
	// MaxPixels rejects larger images, 0 uses defaultMaxPixels.
	MaxPixels int
}

// ToImage decodes the image and returns it as an image.Image. Lossless files
// are decoded by the VP8L decoder, lossy files by the VP8 decoder with the
// ALPH alpha channel applied. For animated files the first frame is returned.
func (w *Webp) ToImage() (image.Image, error) {
	return w.ToImageWith(DecodeOptions{})
}

// ToImageWith decodes the image with the given options.
func (w *Webp) ToImageWith(opt DecodeOptions) (image.Image, error) {
	maxPixels := opt.MaxPixels
	if maxPixels <= 0 {
		maxPixels = defaultMaxPixels
	}
	if w.Width <= 0 || w.Height <= 0 || w.Width*w.Height > maxPixels {
		return nil, errors.Wrapf(ErrImageTooLarge, "%dx%d", w.Width, w.Height)
	}

	if w.HasAnimation {
		frames, _, err := w.Frames(maxPixels)
		if err != nil {
			return nil, err
		}
		if opt.Frame < 0 || opt.Frame >= len(frames) {
			return nil, errors.Errorf("webp: frame %d out of range (%d frames)", opt.Frame, len(frames))
		}
		return toPixelFormat(frames[opt.Frame], opt.Format), nil
	}

	img, err := w.decodeStill(w.VP8, w.VP8L, w.ALPH, maxPixels)
	if err != nil {
		return nil, err
	}
	return toPixelFormat(img, opt.Format), nil
}

// decodeStill decodes one still image: the lossless bitstream, or the lossy
// bitstream with its optional alpha plane. The pixel budget is forwarded to
// the sub-decoders so a hostile header can not allocate more than maxPixels
// pixels even before the top level gate runs.
func (w *Webp) decodeStill(lossy *VP8, lossless *VP8L, alpha *ALPH, maxPixels int) (*image.NRGBA, error) {
	switch {
	case lossless != nil:
		return vp8l.DecodeLimit(lossless.Data, maxPixels)
	case lossy != nil:
		img, err := decodeVP8Limit(lossy, maxPixels)
		if err != nil {
			return nil, err
		}
		if alpha != nil {
			if err = applyAlpha(img, alpha); err != nil {
				return nil, err
			}
		}
		return img, nil
	}
	return nil, errors.New("webp: no image data")
}

// toPixelFormat converts the decoded NRGBA image to the requested layout.
func toPixelFormat(img *image.NRGBA, format PixelFormat) image.Image {
	if format != PixelFormatRGBA {
		return img
	}
	b := img.Bounds()
	out := image.NewRGBA(b)
	// NRGBA to RGBA is a per pixel premultiply, image/draw has the fast path
	drawCopy(out, img)
	return out
}
