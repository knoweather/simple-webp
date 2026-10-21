package simple_webp

import (
	"image"

	"github.com/pkg/errors"

	"github.com/knoweather/simple-webp/internal/vp8"
	"github.com/knoweather/simple-webp/internal/vp8l"
)

// PixelFormat 选择解码结果的像素布局：NRGBA（默认）、RGBA，或有损码流的
// YCbCr 平面。
//
// PixelFormat selects the pixel layout of the decoded image: NRGBA (the
// default), RGBA, or the YCbCr planes of a lossy bitstream.
type PixelFormat int

const (
	// PixelFormatNRGBA 返回 *image.NRGBA，即 straight alpha（非预乘）布局，
	// 与 png 解码器处理 RGBA 图像时使用的布局一致。这是默认值。
	//
	// PixelFormatNRGBA returns an *image.NRGBA, the straight alpha layout
	// used by the png decoder for RGBA images. It is the default.
	PixelFormatNRGBA PixelFormat = iota
	// PixelFormatRGBA 返回 *image.RGBA，即预乘 alpha（premultiplied）布局。
	//
	// PixelFormatRGBA returns an *image.RGBA, the premultiplied layout.
	PixelFormatRGBA
	// PixelFormatYCbCr 返回有损码流解码得到的 *image.YCbCr（4:2:0），不做 YUV→RGB
	// 转换。无损码流、带 alpha 平面的图以及动画没有 YCbCr 表示，会返回 ErrUnsupported。
	//
	// PixelFormatYCbCr returns the *image.YCbCr (4:2:0) planes decoded from a
	// lossy bitstream without a YUV to RGB conversion. Lossless bitstreams,
	// images with an alpha plane and animations have no YCbCr representation and
	// return ErrUnsupported.
	PixelFormatYCbCr
)

// defaultMaxPixels 限制解码图像的最大像素数：1<<27 个像素约为 11585×11585
// 的图像，对应 512 MB 的 NRGBA 像素缓冲。
//
// defaultMaxPixels bounds the decoded image size: 1<<27 pixels is an
// 11585 by 11585 image and 512 MB of NRGBA pixels.
const defaultMaxPixels = 1 << 27

// DecodeOptions 调节像素解码行为。
//
// DecodeOptions tunes the pixel decoding.
type DecodeOptions struct {
	// Format 选择返回的像素布局。PixelFormatYCbCr 只适用于没有 alpha 平面的
	// 静态有损码流，其余情况返回 ErrUnsupported。
	//
	// Format selects the returned pixel layout. PixelFormatYCbCr applies only to
	// a still lossy bitstream without an alpha plane; other cases return
	// ErrUnsupported.
	Format PixelFormat
	// Frame 选择动画文件的帧，0 表示第一帧。
	//
	// Frame selects the frame of an animated file; 0 is the first frame.
	Frame int
	// MaxPixels 拒绝像素数超过该值的图像，0 表示使用 defaultMaxPixels。
	//
	// MaxPixels rejects larger images; 0 uses defaultMaxPixels.
	MaxPixels int
}

// ToImage 解码图像并以 image.Image 返回。无损文件走 VP8L 解码器，有损文件走
// VP8 解码器并叠加 ALPH alpha 通道；动画文件返回第一帧。
//
// ToImage decodes the image and returns it as an image.Image. Lossless
// files are decoded by the VP8L decoder, lossy files by the VP8 decoder
// with the ALPH alpha channel applied. For animated files the first
// frame is returned.
func (w *Webp) ToImage() (image.Image, error) {
	return w.ToImageWith(DecodeOptions{})
}

// ToImageWith 按给定选项解码图像。
//
// ToImageWith decodes the image with the given options.
func (w *Webp) ToImageWith(opt DecodeOptions) (image.Image, error) {
	maxPixels := opt.MaxPixels
	if maxPixels <= 0 {
		maxPixels = defaultMaxPixels
	}
	if w.Width <= 0 || w.Height <= 0 || int64(w.Width)*int64(w.Height) > int64(maxPixels) {
		return nil, errors.Wrapf(ErrImageTooLarge, "%dx%d", w.Width, w.Height)
	}

	if opt.Format == PixelFormatYCbCr {
		return w.ycbcrImage(maxPixels)
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

// ycbcrImage 返回有损码流的 YCbCr 平面。动画、无损码流与带 alpha 的图都无法用
// YCbCr 表示，返回 ErrUnsupported。
//
// ycbcrImage returns the YCbCr planes of a lossy bitstream. Animations,
// lossless bitstreams and images with an alpha plane have no YCbCr
// representation and return ErrUnsupported.
func (w *Webp) ycbcrImage(maxPixels int) (image.Image, error) {
	if w.HasAnimation {
		return nil, errors.Wrap(ErrUnsupported, "YCbCr output for an animation")
	}
	if w.VP8L != nil {
		return nil, errors.Wrap(ErrUnsupported, "YCbCr output for a lossless bitstream")
	}
	if w.ALPH != nil {
		return nil, errors.Wrap(ErrUnsupported, "YCbCr output with an alpha plane")
	}
	if w.VP8 == nil {
		return nil, errors.New("webp: no image data")
	}
	return vp8.DecodeLimit(w.VP8.Data, maxPixels)
}

// decodeStill 解码单张静态图：优先无损码流，否则解码有损码流并叠加可选的
// alpha 平面。像素预算会转发给子解码器，即使顶层校验尚未生效，恶意头部
// 也无法申请超过 maxPixels 的像素。
//
// decodeStill decodes one still image: the lossless bitstream, or the
// lossy bitstream with its optional alpha plane. The pixel budget is
// forwarded to the sub-decoders so a hostile header cannot allocate more
// than maxPixels pixels even before the top level gate runs.
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

// toPixelFormat 把解码得到的 NRGBA 图像转换为请求的像素布局。
//
// toPixelFormat converts the decoded NRGBA image to the requested layout.
func toPixelFormat(img *image.NRGBA, format PixelFormat) image.Image {
	if format != PixelFormatRGBA {
		return img
	}
	b := img.Bounds()
	out := image.NewRGBA(b)
	// NRGBA 转 RGBA 是逐像素预乘，image/draw 为此提供了快速路径。
	// NRGBA to RGBA is a per pixel premultiply; image/draw has the fast path.
	drawCopy(out, img)
	return out
}
