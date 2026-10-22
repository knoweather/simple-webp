package simple_webp

import (
	"image"
	"image/draw"

	"github.com/pkg/errors"

	"github.com/knoweather/simple-webp/internal/vp8l"
)

// alphaPlane 提取图像的 alpha 平面（行优先，一字节一像素），并报告是否存在
// 非不透明像素。全不透明的图像不需要 ALPH chunk。
//
// alphaPlane extracts the alpha plane of the image (row major, one byte per
// pixel) and reports whether any pixel is not fully opaque. A fully opaque image
// needs no ALPH chunk.
func alphaPlane(m image.Image) ([]byte, bool) {
	b := m.Bounds()
	w, h := b.Dx(), b.Dy()
	var src *image.NRGBA
	if n, ok := m.(*image.NRGBA); ok && n.Bounds() == image.Rect(0, 0, w, h) {
		src = n
	} else {
		src = image.NewNRGBA(image.Rect(0, 0, w, h))
		draw.Draw(src, src.Bounds(), m, b.Min, draw.Src)
	}

	plane := make([]byte, w*h)
	hasAlpha := false
	for y := 0; y < h; y++ {
		row := src.Pix[y*src.Stride:]
		for x := 0; x < w; x++ {
			a := row[x*4+3]
			plane[y*w+x] = a
			if a != 0xff {
				hasAlpha = true
			}
		}
	}
	return plane, hasAlpha
}

// filterAlpha 对 alpha 平面施加预测差分（unfilterAlpha 的逆过程），让后续熵
// 编码看到更小的残差。四种方式与解码端一一对应。
//
// 规范对首行/首列有专门规定：水平滤波时第 0 列的像素（除左上角）减去正上方
// 的像素，垂直滤波时第 0 行的像素（除左上角）减去左侧的像素。
//
// filterAlpha applies the predictive differencing the decoder reverses in
// unfilterAlpha, so the entropy coder sees smaller residuals. The four methods
// mirror the decoder.
//
// The spec has dedicated rules for the first row and column: with the
// horizontal filter the pixels in column 0 (except the top left one) subtract
// the pixel just above, and with the vertical filter the pixels in row 0
// (except the top left one) subtract the pixel on the left.
func filterAlpha(plane []byte, width, height int, filter uint8) {
	switch filter {
	case ALPHFilterHorizontal:
		// 逐行从右往左做差分，保证用到的左邻仍是原值；行也从下往上遍历，
		// 保证第 0 列用到的上一行仍是原值。
		// Each row is differenced from right to left so that the left
		// neighbour is still the original value, and the rows are walked
		// bottom up so that the row above column 0 is still original too.
		for y := height - 1; y >= 0; y-- {
			row := plane[y*width : (y+1)*width]
			for x := width - 1; x >= 1; x-- {
				row[x] -= row[x-1]
			}
			if y > 0 {
				row[0] -= plane[(y-1)*width]
			}
		}
	case ALPHFilterVertical:
		// 第 1 行及以下由上邻预测，从下往上遍历保证上邻仍是原值。
		// Rows 1 and below are predicted from the pixel above; walking the
		// rows bottom up keeps that neighbour original.
		for y := height - 1; y >= 1; y-- {
			row := plane[y*width : (y+1)*width]
			prev := plane[(y-1)*width : y*width]
			for x := 0; x < width; x++ {
				row[x] -= prev[x]
			}
		}
		// 第 0 行由左邻预测，从右往左做差分；左上角保持不变。
		// Row 0 is predicted from the pixel on the left, differenced from
		// right to left; the top left pixel is left alone.
		row := plane[:width]
		for x := width - 1; x >= 1; x-- {
			row[x] -= row[x-1]
		}
	case ALPHFilterGradient:
		for y := height - 1; y >= 0; y-- {
			row := plane[y*width : (y+1)*width]
			if y == 0 {
				for x := width - 1; x >= 1; x-- {
					row[x] -= row[x-1]
				}
				continue
			}
			prev := plane[(y-1)*width : y*width]
			for x := width - 1; x >= 1; x-- {
				p := int(row[x-1]) + int(prev[x]) - int(prev[x-1])
				if p < 0 {
					p = 0
				} else if p > 255 {
					p = 255
				}
				row[x] -= byte(p)
			}
			row[0] -= prev[0]
		}
	}
}

// encodeAlphaChunk 构造 ALPH 载荷：一个标志字节（压缩方式、过滤方式，预处理
// 恒为 0）加过滤后的 alpha 数据。alpha=false 时按 C=1 用 VP8L 无损压缩，
// 否则按 C=0 原样存储。
//
// encodeAlphaChunk builds the ALPH payload: one flag byte (compression method and
// filter, preprocessing always 0) followed by the filtered alpha data. With
// uncompressed=false the data is compressed losslessly (C=1) through VP8L,
// otherwise stored verbatim (C=0).
func encodeAlphaChunk(plane []byte, width, height int, filter uint8, uncompressed bool) ([]byte, error) {
	if filter > ALPHFilterGradient {
		return nil, errors.Errorf("webp: unsupported alpha filter %d", filter)
	}
	filtered := make([]byte, len(plane))
	copy(filtered, plane)
	filterAlpha(filtered, width, height, filter)

	head := filter << 2 // compression 0 (none), preprocessing 0
	var data []byte
	if uncompressed {
		data = filtered
	} else {
		// alpha 放进绿色通道，其余通道补满，与解码端 DecodeAlpha 的取法一致。
		// The alpha value goes into the green channel and the other channels are
		// filled, matching what DecodeAlpha reads back.
		argb := make([]uint32, len(filtered))
		for i, a := range filtered {
			argb[i] = 0xff000000 | uint32(a)<<8
		}
		stream, err := vp8l.EncodeARGB(argb, width, height, &vp8l.Options{Method: 1})
		if err != nil {
			return nil, errors.Wrap(err, "webp: alpha encode")
		}
		if len(stream) < 5 {
			return nil, errors.New("webp: alpha stream too short")
		}
		// ALPH 里的 VP8L 码流不带 5 字节流头，解码端会自行补上。
		// The VP8L stream inside ALPH carries no 5-byte header; the decoder
		// prepends one.
		data = stream[5:]
		head |= ALPHCompressionLossless
	}

	out := make([]byte, 0, 1+len(data))
	out = append(out, head)
	out = append(out, data...)
	return out, nil
}
