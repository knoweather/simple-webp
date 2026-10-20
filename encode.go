package simple_webp

import (
	"bytes"
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
	// 变换、颜色索引和 LZ77，2（默认）再应用预测器变换。
	//
	// Method trades speed for size: 0 stores literals only, 1 adds the
	// subtract green transform, colour indexing and LZ77, 2 (the default)
	// also applies a predictor transform.
	Method int
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
//
// EncodeBytes encodes m as a lossless WebP file held in memory.
func EncodeBytes(m image.Image, opt *EncodeOptions) ([]byte, error) {
	if m == nil {
		return nil, errors.New("webp: nil image")
	}
	b := m.Bounds()
	if b.Dx() <= 0 || b.Dy() <= 0 {
		return nil, errors.New("webp: empty image")
	}
	method := 2
	if opt != nil && opt.Method >= 0 && opt.Method <= 2 {
		method = opt.Method
	}

	pix := imageToARGB(m)
	payload, err := vp8l.EncodeARGB(pix, b.Dx(), b.Dy(), &vp8l.Options{Method: method})
	if err != nil {
		return nil, errors.Wrap(err, "webp: vp8l encode")
	}

	var buf bytes.Buffer
	buf.Write(riffTag)
	var size [4]byte
	by.PutUint32(size[:], uint32(4+chunkSize(len(payload))))
	buf.Write(size[:])
	buf.Write(webpTag)
	if err = writeRiffChunk(&buf, VP8LChunk, payload); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
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
