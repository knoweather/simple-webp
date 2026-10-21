package simple_webp

import (
	"image"
	"image/draw"
	"io"

	"github.com/pkg/errors"

	"github.com/knoweather/simple-webp/internal/vp8l"
)

// EncodeOptions 调节无损编码器的行为。
//
// EncodeOptions tunes the lossless encoder.
type EncodeOptions struct {
	// Lossless 仅为兼容保留：Encode 始终写出无损 WebP，需要有损输出请改用
	// EncodeLossy。之所以不按该值分叉，是因为它的零值是 false——一旦生效，
	// 所有没有显式设置它的调用方都会静默地从无损变成有损。
	//
	// Lossless is kept for compatibility: Encode always writes lossless webp and
	// EncodeLossy is the lossy entry point. The value is not honoured because
	// its zero value is false, so making it meaningful would silently turn every
	// caller that never set it into a lossy encoder.
	Lossless bool
	// Method 以速度换压缩率：0 只存储字面量，1 追加减绿（subtract green）
	// 变换、颜色索引和 LZ77，2（默认）再应用预测器变换。取值必须是 0、1 或 2；
	// 越界的 Method 不会被静默兜底，而是返回错误。
	//
	// Method trades speed for size: 0 stores literals only, 1 adds the
	// subtract green transform, colour indexing and LZ77, 2 (the default)
	// also applies a predictor transform. Only 0, 1 and 2 are accepted; an
	// out-of-range Method is not silently fallen back to the default but
	// returns an error.
	Method int
	// ICCP 是内嵌的 ICC 色彩配置原始字节；非空时写出带标志位的 VP8X 与 ICCP。
	//
	// ICCP is the embedded ICC profile; when it is non-empty a flagged VP8X and
	// an ICCP chunk are written.
	ICCP []byte
	// EXIF 是内嵌的 EXIF 元数据原始字节；非空时写出带标志位的 VP8X 与 EXIF。
	//
	// EXIF is the embedded EXIF metadata; when it is non-empty a flagged VP8X
	// and an EXIF chunk are written.
	EXIF []byte
	// XMP 是内嵌的 XMP 元数据原始字节；非空时写出带标志位的 VP8X 与 XMP。
	//
	// XMP is the embedded XMP metadata; when it is non-empty a flagged VP8X and
	// an XMP chunk are written.
	XMP []byte
}

// Encode 把 m 编码为无损 WebP 文件并写入 w。
//
// Encode writes m as a lossless WebP file.
func Encode(w io.Writer, m image.Image, opt *EncodeOptions) error {
	data, err := EncodeBytes(m, opt)
	if err != nil {
		return err
	}
	_, err = w.Write(data)
	return err
}

// EncodeBytes 把 m 编码为完整的无损 WebP 文件并保留在内存中返回。
// 非空的 ICCP/EXIF/XMP 直接随文件写出；三者都为空时输出仍是不带 VP8X 的简单容器。
//
// EncodeBytes encodes m as a lossless WebP file held in memory. Non-empty
// ICCP/EXIF/XMP are written straight into the file; when all three are empty
// the output is still the simple container without a VP8X chunk.
func EncodeBytes(m image.Image, opt *EncodeOptions) ([]byte, error) {
	if m == nil {
		return nil, errors.New("webp: nil image")
	}
	b := m.Bounds()
	if b.Dx() <= 0 || b.Dy() <= 0 {
		return nil, errors.New("webp: empty image")
	}
	method := 2
	var iccp, exif, xmp []byte
	if opt != nil {
		if opt.Method < 0 || opt.Method > 2 {
			return nil, errors.Errorf("webp: unsupported Method %d (supported: 0, 1, 2)", opt.Method)
		}
		method = opt.Method
		iccp, exif, xmp = opt.ICCP, opt.EXIF, opt.XMP
	}

	pix := imageToARGB(m)
	payload, err := vp8l.EncodeARGB(pix, b.Dx(), b.Dy(), &vp8l.Options{Method: method})
	if err != nil {
		return nil, errors.Wrap(err, "webp: vp8l encode")
	}

	chunks, err := encodeChunks(VP8LChunk, payload, b.Dx(), b.Dy(), iccp, exif, xmp, nil)
	if err != nil {
		return nil, err
	}
	return marshalWebpFile(chunks)
}

// imageToARGB 把任意 image.Image 转成 0xAARRGGBB 像素切片，借助 image/draw
// 统一处理所有源布局。
//
// imageToARGB converts any image.Image into 0xAARRGGBB pixels, using
// image/draw so every source layout is handled by the standard library.
func imageToARGB(m image.Image) []uint32 {
	b := m.Bounds()
	w, h := b.Dx(), b.Dy()
	var src *image.NRGBA
	if n, ok := m.(*image.NRGBA); ok && n.Bounds() == image.Rect(0, 0, w, h) {
		src = n
	} else {
		src = image.NewNRGBA(image.Rect(0, 0, w, h))
		draw.Draw(src, src.Bounds(), m, b.Min, draw.Src)
	}
	pix := make([]uint32, w*h)
	for i := 0; i < len(pix); i++ {
		o := i * 4
		pix[i] = uint32(src.Pix[o+3])<<24 | uint32(src.Pix[o])<<16 | uint32(src.Pix[o+1])<<8 | uint32(src.Pix[o+2])
	}
	return pix
}
