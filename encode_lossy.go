package simple_webp

import (
	"bytes"
	"image"

	"image/draw"
	"io"

	"github.com/pkg/errors"

	"github.com/knoweather/simple-webp/internal/vp8"
)

// LossyOptions 调节有损（VP8）编码器的行为。
//
// LossyOptions tunes the lossy (VP8) encoder.
type LossyOptions struct {
	// Quality 取 0（体积最小）到 100（质量最高）；零值等同于 75。
	//
	// Quality runs from 0 (smallest) to 100 (best); the zero value means 75.
	Quality int
}

// defaultLossyQuality 是未指定质量时使用的取值。
//
// defaultLossyQuality is used when no quality is given.
const defaultLossyQuality = 75

// EncodeLossy 把 m 编码为有损 WebP 文件并写入 w。
//
// EncodeLossy writes m as a lossy WebP file.
func EncodeLossy(w io.Writer, m image.Image, opt *LossyOptions) error {
	data, err := EncodeLossyBytes(m, opt)
	if err != nil {
		return err
	}
	_, err = w.Write(data)
	return err
}

// EncodeLossyBytes 把 m 编码为完整的有损 WebP 文件并保留在内存中返回。
//
// EncodeLossyBytes encodes m as a lossy WebP file held in memory.
//
// 有损格式只承载亮度与色度，alpha 通道不参与编码：需要保留透明度时应改用
// EncodeBytes 写出无损图像。
//
// The lossy format carries luma and chroma only, so the alpha channel takes no
// part in the encoding: use EncodeBytes to keep transparency.
func EncodeLossyBytes(m image.Image, opt *LossyOptions) ([]byte, error) {
	if m == nil {
		return nil, errors.New("webp: nil image")
	}
	b := m.Bounds()
	if b.Dx() <= 0 || b.Dy() <= 0 {
		return nil, errors.New("webp: empty image")
	}
	quality := defaultLossyQuality
	if opt != nil && opt.Quality != 0 {
		quality = opt.Quality
	}

	ycc := imageToYCbCr420(m)
	payload, err := vp8.Encode(ycc, quality)
	if err != nil {
		return nil, errors.Wrap(err, "webp: vp8 encode")
	}

	var buf bytes.Buffer
	buf.Write(riffTag)
	var size [4]byte
	by.PutUint32(size[:], uint32(4+chunkSize(len(payload))))
	buf.Write(size[:])
	buf.Write(webpTag)
	if err = writeRiffChunk(&buf, VP8Chunk, payload); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

// imageToYCbCr420 把任意 image.Image 转成 4:2:0 的 YCbCr：亮度逐像素取，色度
// 按 2x2 块平均。借助 image/draw 统一处理所有源布局。
//
// imageToYCbCr420 converts any image.Image into 4:2:0 YCbCr: luma per pixel and
// chroma averaged over each 2x2 block. image/draw normalises the source layout.
func imageToYCbCr420(m image.Image) *image.YCbCr {
	b := m.Bounds()
	w, h := b.Dx(), b.Dy()
	var src *image.NRGBA
	if n, ok := m.(*image.NRGBA); ok && n.Bounds() == image.Rect(0, 0, w, h) {
		src = n
	} else {
		src = image.NewNRGBA(image.Rect(0, 0, w, h))
		draw.Draw(src, src.Bounds(), m, b.Min, draw.Src)
	}

	dst := image.NewYCbCr(image.Rect(0, 0, w, h), image.YCbCrSubsampleRatio420)
	cw, ch := (w+1)/2, (h+1)/2
	cbAcc := make([]int32, cw*ch)
	crAcc := make([]int32, cw*ch)
	cnt := make([]int32, cw*ch)

	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			o := y*src.Stride + x*4
			yy, cb, cr := rgbToYCbCrLimited(src.Pix[o], src.Pix[o+1], src.Pix[o+2])
			dst.Y[y*dst.YStride+x] = yy
			i := (y/2)*cw + x/2
			cbAcc[i] += int32(cb)
			crAcc[i] += int32(cr)
			cnt[i]++
		}
	}
	for i := 0; i < cw*ch; i++ {
		n := cnt[i]
		dst.Cb[(i/cw)*dst.CStride+i%cw] = uint8((cbAcc[i] + n/2) / n)
		dst.Cr[(i/cw)*dst.CStride+i%cw] = uint8((crAcc[i] + n/2) / n)
	}
	return dst
}

// rgbToYCbCrLimited 按 BT.601 的有限范围（亮度 16-235、色度 16-240）把 RGB 转成
// YCbCr，也就是 VP8 解码端使用的范围约定。
//
// rgbToYCbCrLimited converts RGB to YCbCr in the BT.601 limited range (luma
// 16-235, chroma 16-240), the range convention the VP8 decoder assumes.
func rgbToYCbCrLimited(r, g, b uint8) (uint8, uint8, uint8) {
	ri, gi, bi := int32(r), int32(g), int32(b)
	y := 16 + (16829*ri+33039*gi+6416*bi+(1<<15))>>16
	cb := 128 + (-9714*ri-19071*gi+28784*bi+(1<<15))>>16
	cr := 128 + (28784*ri-24103*gi-4681*bi+(1<<15))>>16
	return clampByte(y), clampByte(cb), clampByte(cr)
}

// clampByte 把整数钳制到 [0,255]。
//
// clampByte clamps an integer into [0,255].
func clampByte(v int32) uint8 {
	if v < 0 {
		return 0
	}
	if v > 255 {
		return 255
	}
	return uint8(v)
}
