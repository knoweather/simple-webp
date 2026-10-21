package simple_webp

import (
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
	// Quality 取 0（体积最小）到 100（质量最高）；超出 0..100 返回错误，不静默钳制。
	// 零值默认等同于 75，除非 QualityZero 为 true。
	//
	// Quality runs from 0 (smallest) to 100 (best); a value outside 0..100
	// returns an error instead of being silently clamped. The zero value means
	// the default 75 unless QualityZero is set.
	Quality int
	// QualityZero 让 Quality 的零值表示「质量 0」而不是默认的 75。
	// 不设置时保持旧语义：Quality == 0 等于默认质量。
	//
	// QualityZero makes the zero value of Quality mean quality 0 instead of
	// the default 75. When it is false the old meaning stays: Quality == 0
	// selects the default quality.
	QualityZero bool
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
	// AlphaFilter 选择 ALPH 的行滤波方式：0 不滤波（默认）、1 水平、2 垂直、
	// 3 梯度；其它值返回错误。滤波只影响 alpha 的压缩率，不改变像素值。
	//
	// AlphaFilter selects the ALPH row filter: 0 none (the default), 1
	// horizontal, 2 vertical, 3 gradient; any other value is an error. The
	// filter changes only the compressed size, never the alpha values.
	AlphaFilter uint8
	// AlphaUncompressed 为 true 时 alpha 平面按 C=0 原样存储；默认按 C=1 用
	// VP8L 无损压缩。两种方式都不量化 alpha。
	//
	// AlphaUncompressed stores the alpha plane verbatim (C=0); by default it is
	// compressed losslessly with VP8L (C=1). Neither mode quantises the alpha.
	AlphaUncompressed bool
	// FilterLevel 是 VP8 环路滤波强度 1..63；0（默认）按质量自动选择，
	// -1 关闭滤波。强度越大块效应越弱，细节也更软。
	//
	// FilterLevel is the VP8 loop filter strength, 1..63. Zero (the default)
	// derives it from the quality, -1 disables the filter. A stronger filter
	// removes more blocking and softens detail.
	FilterLevel int
	// SimpleFilter 为 true 时用简单滤波（只处理亮度边缘），否则用普通滤波。
	//
	// SimpleFilter selects the simple filter (luma edges only).
	SimpleFilter bool
	// TargetBytes 打开码率控制：在质量 0..100 之间二分，取最接近该体积的编码
	// 结果（只对 VP8 码流生效，alpha 与元数据不计入）。0 表示按 Quality 编码。
	//
	// TargetBytes enables rate control: the quality is bisected over 0..100 and the
	// encode closest to that size wins (it covers the VP8 bitstream only, alpha
	// and metadata are not counted). 0 encodes at Quality.
	TargetBytes int
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
// 非空的 ICCP/EXIF/XMP 直接随文件写出；三者都为空时输出仍是不带 VP8X 的简单容器。
//
// 带透明度的输入写成扩展格式：VP8X（Alpha 位）+ ALPH + VP8，alpha 平面无损
// 压缩（或按 AlphaUncompressed 原样存储），完全不透明的输入仍走简单格式。
//
// EncodeLossyBytes encodes m as a lossy WebP file held in memory. Non-empty
// ICCP/EXIF/XMP are written straight into the file; when all three are empty
// the output is still the simple container without a VP8X chunk.
//
// An input with transparency is written in the extended format: VP8X (alpha
// flag) + ALPH + VP8, with the alpha plane compressed losslessly (or stored
// verbatim with AlphaUncompressed); a fully opaque input still uses the simple
// format.
//
// A Quality outside 0..100 returns an error rather than being clamped.
func EncodeLossyBytes(m image.Image, opt *LossyOptions) ([]byte, error) {
	if m == nil {
		return nil, errors.New("webp: nil image")
	}
	b := m.Bounds()
	if b.Dx() <= 0 || b.Dy() <= 0 {
		return nil, errors.New("webp: empty image")
	}
	quality := defaultLossyQuality
	var iccp, exif, xmp []byte
	var alphaFilter uint8
	var alphaUncompressed bool
	filterLevel := -1
	var simpleFilter bool
	if opt != nil {
		if opt.QualityZero {
			quality = opt.Quality
		} else if opt.Quality != 0 {
			quality = opt.Quality
		}
		iccp, exif, xmp = opt.ICCP, opt.EXIF, opt.XMP
		alphaFilter = opt.AlphaFilter
		alphaUncompressed = opt.AlphaUncompressed
		simpleFilter = opt.SimpleFilter
		filterLevel = opt.FilterLevel
		if filterLevel == 0 {
			filterLevel = -1 // 0 表示自动 / zero means automatic
		}
	}
	if quality < 0 || quality > 100 {
		return nil, errors.Errorf("webp: quality %d out of range 0..100", quality)
	}
	if alphaFilter > ALPHFilterGradient {
		return nil, errors.Errorf("webp: unsupported alpha filter %d", alphaFilter)
	}
	if filterLevel < -1 || filterLevel > 63 {
		return nil, errors.Errorf("webp: filter level %d out of range -1..63", filterLevel)
	}
	if opt != nil && opt.TargetBytes < 0 {
		return nil, errors.Errorf("webp: target size %d must not be negative", opt.TargetBytes)
	}

	ycc := imageToYCbCr420(m)
	vp8Opt := vp8.Options{
		Quality:      quality,
		FilterLevel:  filterLevel,
		SimpleFilter: simpleFilter,
	}
	var targetBytes int
	if opt != nil {
		targetBytes = opt.TargetBytes
	}
	var payload []byte
	var err error
	if targetBytes > 0 {
		payload, err = vp8.EncodeToTarget(ycc, vp8Opt, targetBytes)
	} else {
		payload, err = vp8.EncodeWithOptions(ycc, vp8Opt)
	}
	if err != nil {
		return nil, errors.Wrap(err, "webp: vp8 encode")
	}

	var alpha []byte
	if plane, hasAlpha := alphaPlane(m); hasAlpha {
		alpha, err = encodeAlphaChunk(plane, b.Dx(), b.Dy(), alphaFilter, alphaUncompressed)
		if err != nil {
			return nil, err
		}
	}

	chunks, err := encodeChunks(VP8Chunk, payload, b.Dx(), b.Dy(), iccp, exif, xmp, alpha)
	if err != nil {
		return nil, err
	}
	return marshalWebpFile(chunks)
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
