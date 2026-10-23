package simple_webp

import (
	"image"
	"io"

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

// DecodeWebp 从任意 io.Reader 流式解析并解码一个 WebP 文件，等价于
// ParseWebp(r) 后接 ToImageWith(opt)。容器按 chunk 逐个流入，调用方不需要
// 事先把整个文件读进内存——文件、网络流、bytes.Reader 都可以直接喂进来。
//
// DecodeWebp parses and decodes a webp file from any io.Reader, the streaming
// counterpart of ParseWebp(r) followed by ToImageWith(opt). The container
// streams in chunk by chunk, so the caller never needs the whole file in
// memory: files, network streams and bytes.Reader all feed straight in.
func DecodeWebp(r io.Reader, opt DecodeOptions) (image.Image, error) {
	w, err := ParseWebp(r)
	if err != nil {
		return nil, err
	}
	return w.ToImageWith(opt)
}

// DecodeInto 把一张静态图解到调用方提供的 NRGBA 缓冲里：除色度上采样的暂存
// 行外不再分配图像内存，连续解码多张图时可以复用同一块缓冲。dst 的边界必须
// 恰好是 image.Rect(0, 0, w.Width, w.Height)；尺寸不符、nil 目标或动画文件
// 都会返回错误，dst 内容不被修改。像素预算取默认值（1<<27 个像素）。
//
// DecodeInto decodes one still image into an NRGBA buffer supplied by the
// caller: apart from the chroma upsampling scratch rows no image memory is
// allocated, so a pipeline that decodes image after image can reuse one
// buffer. The bounds of dst must be exactly image.Rect(0, 0, w.Width,
// w.Height); a size mismatch, a nil target or an animated file is an error and
// leaves dst untouched. The pixel budget is the default one (1<<27 pixels).
func (w *Webp) DecodeInto(dst *image.NRGBA) error {
	if dst == nil {
		return errors.New("webp: nil image")
	}
	b := dst.Bounds()
	if b.Min.X != 0 || b.Min.Y != 0 || b.Dx() != w.Width || b.Dy() != w.Height {
		return errors.Errorf("webp: destination is %v, image is %dx%d", b, w.Width, w.Height)
	}
	if w.HasAnimation {
		return errors.Wrap(ErrUnsupported, "DecodeInto for an animation")
	}
	switch {
	case w.VP8L != nil:
		pix, width, height, err := vp8l.DecodeARGBLimit(w.VP8L.Data, defaultMaxPixels)
		if err != nil {
			return errors.Wrap(err, "webp: vp8l")
		}
		if width != w.Width || height != w.Height {
			return errors.Errorf("webp: bitstream is %dx%d, container says %dx%d", width, height, w.Width, w.Height)
		}
		for i, p := range pix {
			o := i * 4
			dst.Pix[o+0] = byte(p >> 16)
			dst.Pix[o+1] = byte(p >> 8)
			dst.Pix[o+2] = byte(p)
			dst.Pix[o+3] = byte(p >> 24)
		}
	case w.VP8 != nil:
		img, err := vp8.DecodeLimit(w.VP8.Data, defaultMaxPixels)
		if err != nil {
			return errors.Wrap(err, "webp: vp8")
		}
		// 位流自己的宽高必须与容器一致，否则转换会静默不写任何像素。
		// The bitstream's own size has to match the container, otherwise the
		// conversion would silently write nothing.
		if img.Rect.Dx() != w.Width || img.Rect.Dy() != w.Height {
			return errors.Errorf("webp: bitstream is %dx%d, container says %dx%d", img.Rect.Dx(), img.Rect.Dy(), w.Width, w.Height)
		}
		ycbcrToNRGBAInto(dst, img)
		if w.ALPH != nil {
			if err = applyAlpha(dst, w.ALPH); err != nil {
				return err
			}
		}
	default:
		return errors.New("webp: no image data")
	}
	return nil
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
