package simple_webp

import (
	"image"

	"github.com/pkg/errors"

	"github.com/knoweather/simple-webp/internal/vp8l"
)

// decodeAlphaPlane 解压有损 WebP 图像的 alpha 平面数据，并逆向还原 alpha 过滤。
// 无压缩模式下 alpha.Data 必须至少含 width*height 字节；无损模式委托给
// vp8l 解码器，二者均会随后逆向应用过滤器。
//
// decodeAlphaPlane decompresses the alpha plane of a lossy WebP image and
// reverses the alpha filtering. In uncompressed mode alpha.Data must hold at
// least width*height bytes; in lossless mode decoding is delegated to the
// vp8l decoder. Both paths unfilter the plane afterwards.
func decodeAlphaPlane(alpha *ALPH, width, height int) ([]byte, error) {
	if width <= 0 || height <= 0 {
		return nil, errors.New("webp: invalid alpha size")
	}
	switch alpha.Compression {
	case ALPHCompressionNone:
		if len(alpha.Data) < width*height {
			return nil, errors.Errorf("webp: alpha plane is %d bytes, want %d", len(alpha.Data), width*height)
		}
		plane := make([]byte, width*height)
		copy(plane, alpha.Data)
		unfilterAlpha(plane, width, height, alpha.Filter)
		return plane, nil
	case ALPHCompressionLossless:
		plane, err := vp8l.DecodeAlpha(alpha.Data, width, height)
		if err != nil {
			return nil, errors.Wrap(err, "webp: alpha plane")
		}
		unfilterAlpha(plane, width, height, alpha.Filter)
		return plane, nil
	}
	return nil, errors.Errorf("webp: unsupported alpha compression %d", alpha.Compression)
}

// unfilterAlpha 逆向 alpha 平面的水平、垂直或梯度过滤，
// 就地把预测差分值还原为真实 alpha 值（模 256 加法）。
//
// unfilterAlpha reverses the horizontal, vertical or gradient filter of an
// alpha plane in place, turning predictive deltas back into real alpha values
// (addition modulo 256).
func unfilterAlpha(plane []byte, width, height int, filter uint8) {
	switch filter {
	case ALPHFilterHorizontal:
		for y := 0; y < height; y++ {
			row := plane[y*width : (y+1)*width]
			for x := 1; x < width; x++ {
				row[x] += row[x-1]
			}
		}
	case ALPHFilterVertical:
		for y := 1; y < height; y++ {
			row := plane[y*width : (y+1)*width]
			prev := plane[(y-1)*width : y*width]
			for x := 0; x < width; x++ {
				row[x] += prev[x]
			}
		}
	case ALPHFilterGradient:
		for y := 0; y < height; y++ {
			row := plane[y*width : (y+1)*width]
			if y == 0 {
				for x := 1; x < width; x++ {
					row[x] += row[x-1]
				}
				continue
			}
			prev := plane[(y-1)*width : y*width]
			row[0] += prev[0]
			for x := 1; x < width; x++ {
				p := int(row[x-1]) + int(prev[x]) - int(prev[x-1])
				if p < 0 {
					p = 0
				} else if p > 255 {
					p = 255
				}
				row[x] += byte(p)
			}
		}
	}
}

// applyAlpha 解码 alpha 平面并把每个像素的 alpha 值写入 NRGBA 图像
// （每个像素的第 4 个字节，即 A 通道）。
//
// applyAlpha decodes the alpha plane and writes each pixel's alpha value
// into the NRGBA image (the 4th byte of each pixel, the A channel).
func applyAlpha(img *image.NRGBA, alpha *ALPH) error {
	b := img.Bounds()
	w, h := b.Dx(), b.Dy()
	plane, err := decodeAlphaPlane(alpha, w, h)
	if err != nil {
		return err
	}
	// "电平缩减"预处理只存储较少的 alpha 级别，参考解码器会用抖动
	// 还原；这里直接保留原始值，与抖动结果的偏差在一个量化步长内。
	// A "level reduction" preprocessing stores fewer alpha levels; the
	// reference decoder dithers them back. The values are kept as they are,
	// which is within one quantization step of the dithered result.
	for y := 0; y < h; y++ {
		row := img.Pix[y*img.Stride : y*img.Stride+w*4]
		for x := 0; x < w; x++ {
			row[x*4+3] = plane[y*w+x]
		}
	}
	return nil
}
