package simple_webp

import (
	"image"
	"sync"

	"github.com/pkg/errors"

	"github.com/knoweather/simple-webp/internal/vp8"
)

// decodeVP8 用默认像素上限把有损 VP8 关键帧解码成 NRGBA 像素。
//
// decodeVP8 decodes a lossy VP8 key frame into NRGBA pixels using the
// default pixel budget.
func decodeVP8(v *VP8) (*image.NRGBA, error) {
	return decodeVP8Limit(v, defaultMaxPixels)
}

// decodeVP8Limit 用调用者给定的像素上限把有损 VP8 关键帧解码成 NRGBA 像素。
//
// decodeVP8Limit decodes a lossy VP8 key frame into NRGBA pixels with an
// explicit pixel budget supplied by the caller.
func decodeVP8Limit(v *VP8, maxPixels int) (*image.NRGBA, error) {
	img, err := vp8.DecodeLimit(v.Data, maxPixels)
	if err != nil {
		return nil, errors.Wrap(err, "webp: vp8")
	}
	return ycbcrToNRGBA(img), nil
}

// YUV 转换预计算表：对 0..255 的 c，tab[c] = (c*coeff)>>8，热循环里查表
// 代替乘法；系数取自 libwebp 的定点 BT.601 系数。
//
// Yuv conversion precomputed tables: tab[c] = (c*coeff)>>8 for c in 0..255,
// so the hot loop does table lookups instead of multiplies, using libwebp's
// fixed-point BT.601 coefficients.
var (
	yTab  = [256]int{}
	vrTab = [256]int{}
	ugTab = [256]int{}
	vgTab = [256]int{}
	ubTab = [256]int{}
)

// init 在程序启动时填满五张预计算表。
//
// init fills the five precomputed tables at program start.
func init() {
	for i := 0; i < 256; i++ {
		yTab[i] = (i * 19077) >> 8
		vrTab[i] = (i * 26149) >> 8
		ugTab[i] = (i * 6419) >> 8
		vgTab[i] = (i * 13320) >> 8
		ubTab[i] = (i * 33050) >> 8
	}
}

// ycbcrToNRGBA 把 4:2:0 帧转成非预乘 alpha 的 RGB。
//
// 色度平面用参考解码器的 9:3:3:1 "fancy" 滤波器上采样，再用 libwebp 的
// BT.601 有限范围定点形式转换，因此结果与参考解码器一致。每行输出只读
// 平面式 Y/Cb/Cr（此处只读），且写入互不重叠的目标行，所以转换按行带并行。
//
// ycbcrToNRGBA converts a 4:2:0 frame to straight alpha RGB.
//
// The chroma planes are upsampled with the 9:3:3:1 "fancy" filter of the
// reference decoder, and converted with the BT.601 limited range fixed point
// form libwebp uses, so the result matches the reference decoder. Each output
// row only reads the planar Y/Cb/Cr (which are read-only here) and writes a
// disjoint destination row, so the conversion is parallelized across row bands.
func ycbcrToNRGBA(src *image.YCbCr) *image.NRGBA {
	b := src.Bounds()
	w, h := b.Dx(), b.Dy()
	dst := image.NewNRGBA(image.Rect(0, 0, w, h))
	cw, ch := (w+1)/2, (h+1)/2
	if h <= 32 {
		decodeBand(dst, src, cw, ch, w, 0, h, func() {})
		return dst
	}
	const bandH = 16
	var wg sync.WaitGroup
	for y0 := 0; y0 < h; y0 += bandH {
		y1 := y0 + bandH
		if y1 > h {
			y1 = h
		}
		wg.Add(1)
		go decodeBand(dst, src, cw, ch, w, y0, y1, wg.Done)
	}
	wg.Wait()
	return dst
}

// decodeBand 把 src 的行区间 [y0, y1) 转换进 dst。各带的目标行互不重叠、
// 只读源平面，因此并发执行也没有数据竞争。
//
// decodeBand converts the rows [y0, y1) of src into dst. Bands are disjoint in
// their destination rows and only read the source planes, so running them
// concurrently is data-race free.
func decodeBand(dst *image.NRGBA, src *image.YCbCr, cw, ch, w, y0, y1 int, done func()) {
	defer done()
	for y := y0; y < y1; y++ {
		yRow := src.Y[y*src.YStride : y*src.YStride+w]
		dstRow := dst.Pix[y*dst.Stride : y*dst.Stride+w*4]
		for x := 0; x < w; x++ {
			u := upsampleChroma(src.Cb, src.CStride, cw, ch, x, y)
			v := upsampleChroma(src.Cr, src.CStride, cw, ch, x, y)
			r, g, bl := yuvToRGB(int(yRow[x]), u, v)
			o := x * 4
			dstRow[o+0] = r
			dstRow[o+1] = g
			dstRow[o+2] = bl
			dstRow[o+3] = 255
		}
	}
}

// chromaAt 读取一个色度样本，坐标超出平面时截断到边缘。
//
// chromaAt reads one chroma sample, clamping the coordinates to the plane.
func chromaAt(p []byte, stride, cw, ch, cx, cy int) int {
	if cx < 0 {
		cx = 0
	} else if cx >= cw {
		cx = cw - 1
	}
	if cy < 0 {
		cy = 0
	} else if cy >= ch {
		cy = ch - 1
	}
	return int(p[cy*stride+cx])
}

// upsampleChroma 为亮度像素 (x, y) 插值出一个色度样本。
//
// 2x2 亮度块的色度样本与它的左上角像素共位，因此块内偏移 (dx, dy) 的像素
// 按 9/16 该样本 + 3/16 水平邻居 + 3/16 垂直邻居 + 1/16 对角邻居混合。
//
// upsampleChroma interpolates one chroma sample for the luma pixel (x, y).
//
// The chroma sample of a 2x2 luma block is co-sited with its top left pixel,
// so the pixel at offset (dx, dy) inside the block blends 9/16 of that sample
// with 3/16 of its horizontal neighbour, 3/16 of its vertical neighbour and
// 1/16 of the diagonal one.
func upsampleChroma(p []byte, stride, cw, ch, x, y int) int {
	j, k := x>>1, y>>1
	dj, dk := 1, 1
	if x&1 == 0 {
		dj = -1
	}
	if y&1 == 0 {
		dk = -1
	}
	center := chromaAt(p, stride, cw, ch, j, k)
	h := chromaAt(p, stride, cw, ch, j+dj, k)
	v := chromaAt(p, stride, cw, ch, j, k+dk)
	d := chromaAt(p, stride, cw, ch, j+dj, k+dk)
	return (9*center + 3*h + 3*v + d + 8) >> 4
}

// yuvToRGB 用与参考解码器相同的定点系数和取整，把一组 BT.601 有限范围
// YUV 转成 RGB。>>8 的乘积已在 yTab/vrTab/... 表里预计算，只剩最终的
// >>6 和取整偏移。
//
// yuvToRGB converts one BT.601 limited range YUV triple with the same fixed
// point coefficients and rounding as the reference decoder. The >>8 products
// are precomputed in the yTab/vrTab/... tables, so only the final >>6 and the
// rounding offsets remain.
func yuvToRGB(y, u, v int) (byte, byte, byte) {
	r := (yTab[y] + vrTab[v] - 14234) >> 6
	g := (yTab[y] - ugTab[u] - vgTab[v] + 8708) >> 6
	b := (yTab[y] + ubTab[u] - 17685) >> 6
	return clip8(r), clip8(g), clip8(b)
}

// clip8 把 v 截断到 [0, 255] 范围。
//
// clip8 clamps v to the [0, 255] range.
func clip8(v int) byte {
	if v < 0 {
		return 0
	}
	if v > 255 {
		return 255
	}
	return byte(v)
}
