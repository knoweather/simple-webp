package simple_webp

import (
	"image"
	"image/color"
	"image/draw"
	"io"

	"github.com/pkg/errors"
)

func init() {
	// 让 image.Decode 与 image.DecodeConfig 识别 webp 文件
	// makes image.Decode and image.DecodeConfig recognize webp files
	image.RegisterFormat("webp", "RIFF????WEBP", Decode, DecodeConfig)
}

// Decode 读取 webp 流并返回解码后的图像，实现 image.Decode 签名。
// 动图会返回合成后的画布帧。
//
// Decode reads a webp stream and returns the decoded image, implementing the
// image.Decode signature. For animated files it returns the composited
// canvas frame.
func Decode(r io.Reader) (image.Image, error) {
	w, err := ParseWebp(r)
	if err != nil {
		return nil, err
	}
	return w.ToImage()
}

// DecodeConfig 只读取容器头部（RIFF 头 + 第一个决定尺寸的 chunk），
// 不解码任何像素数据，因此开销很小。VP8X、VP8、VP8L 三种 chunk 都被
// 支持；颜色模型固定返回 NRGBAModel。
//
// DecodeConfig reads only the container header (the RIFF header plus the
// first chunk that determines the size) and decodes no pixel data at all,
// so it is cheap. It supports the VP8X, VP8 and VP8L chunks; the color
// model is always NRGBAModel.
func DecodeConfig(r io.Reader) (image.Config, error) {
	var head [12]byte
	if _, err := io.ReadFull(r, head[:]); err != nil {
		return image.Config{}, errors.WithMessage(err, "invalid webp: can not read RIFF header")
	}
	if string(head[:4]) != string(riffTag) || string(head[8:12]) != string(webpTag) {
		return image.Config{}, ErrNotWebP
	}
	var cfg image.Config
	cfg.ColorModel = color.NRGBAModel

	for {
		var chunkHead [8]byte
		if _, err := io.ReadFull(r, chunkHead[:]); err != nil {
			return image.Config{}, errors.WithMessage(err, "invalid webp: can not read chunk header")
		}
		name := FourCC(chunkHead[:4])
		size := int(by.Uint32(chunkHead[4:8]))
		if size > maxChunkSize {
			return image.Config{}, errors.Errorf("invalid webp: chunk %q too large", string(name))
		}
		switch name {
		case VP8XChunk:
			var data [10]byte
			if size < len(data) {
				return image.Config{}, errors.New("invalid webp: short VP8X chunk")
			}
			if _, err := io.ReadFull(r, data[:]); err != nil {
				return image.Config{}, errors.WithStack(err)
			}
			cfg.Width = int(readUint24(data[4:7])) + 1
			cfg.Height = int(readUint24(data[7:10])) + 1
			return cfg, nil
		case VP8Chunk:
			var data [10]byte
			if size < len(data) {
				return image.Config{}, errors.New("invalid webp: short VP8 chunk")
			}
			if _, err := io.ReadFull(r, data[:]); err != nil {
				return image.Config{}, errors.WithStack(err)
			}
			if data[3] != 0x9d || data[4] != 0x01 || data[5] != 0x2a {
				return image.Config{}, errors.New("invalid webp: bad VP8 start code")
			}
			cfg.Width = int(by.Uint16(data[6:8]) & 0x3fff)
			cfg.Height = int(by.Uint16(data[8:10]) & 0x3fff)
			return cfg, nil
		case VP8LChunk:
			var data [5]byte
			if size < len(data) {
				return image.Config{}, errors.New("invalid webp: short VP8L chunk")
			}
			if _, err := io.ReadFull(r, data[:]); err != nil {
				return image.Config{}, errors.WithStack(err)
			}
			if data[0] != 0x2f {
				return image.Config{}, errors.New("invalid webp: bad VP8L signature")
			}
			bits := by.Uint32(data[1:5])
			cfg.Width = int(bits&0x3fff) + 1
			cfg.Height = int(bits>>14&0x3fff) + 1
			return cfg, nil
		default:
			// 跳过 chunk 载荷及其补齐字节（奇数长度后有一个 padding 字节）
			// skip the payload and its padding byte (odd sizes get one byte of padding)
			skip := int64(size) + int64(size&1)
			if _, err := io.CopyN(io.Discard, r, skip); err != nil {
				return image.Config{}, errors.WithMessagef(err, "invalid webp: truncated %q chunk", string(name))
			}
		}
	}
}

// Image 只解码一次并缓存结果，是 Webp 实现 image.Image 的基础：
// 首次调用触发完整解码，之后（含失败原因）都复用同一结果。
//
// Image decodes the image once and caches the result; it backs the
// image.Image implementation of Webp. The first call triggers the full
// decode; later calls reuse the same result, including any error.
func (w *Webp) Image() (image.Image, error) {
	w.Once.Do(func() {
		w.img, w.imgErr = w.ToImage()
	})
	return w.img, w.imgErr
}

// Bounds 实现 image.Image：返回文件的画布矩形（从原点开始，宽高来自头部）。
//
// Bounds implements image.Image: it returns the canvas rectangle of the file
// (origin at (0,0), size taken from the header).
func (w *Webp) Bounds() image.Rectangle {
	return image.Rect(0, 0, w.Width, w.Height)
}

// ColorModel 实现 image.Image。解码器输出的是 straight alpha（非预乘）
// NRGBA 像素，因此这里固定返回 color.NRGBAModel。
//
// ColorModel implements image.Image. The decoders produce straight alpha
// (non-premultiplied) NRGBA pixels, so color.NRGBAModel is returned
// unconditionally.
func (w *Webp) ColorModel() color.Model {
	return color.NRGBAModel
}

// At 实现 image.Image。图像在首次调用时才解码并缓存；解码失败时
// 返回透明的 color.NRGBA{}。
//
// At implements image.Image. The image is decoded lazily on the first call
// and cached afterwards; if decoding fails, a transparent color.NRGBA{} is
// returned.
func (w *Webp) At(x, y int) color.Color {
	img, err := w.Image()
	if err != nil {
		return color.NRGBA{}
	}
	return img.At(x, y)
}

// drawCopy 通过 image/draw 把 straight alpha 的 NRGBA 图像转换为预乘的
// RGBA 图像；image/draw 对这一对类型有专门优化路径。
//
// drawCopy converts a straight alpha NRGBA image into a premultiplied RGBA
// one through image/draw, which has an optimized path for this pair.
func drawCopy(dst *image.RGBA, src *image.NRGBA) {
	draw.Draw(dst, dst.Bounds(), src, image.Point{}, draw.Src)
}
