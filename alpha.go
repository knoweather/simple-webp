package simple_webp

import (
	"image"

	"github.com/pkg/errors"

	"github.com/knoweather/simple-webp/internal/vp8l"
)

// decodeAlphaPlane decompresses the alpha plane of a lossy image and reverses
// the alpha filtering.
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

// unfilterAlpha reverses the horizontal, vertical or gradient filter of an
// alpha plane.
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

// applyAlpha writes the decoded alpha plane into the image.
func applyAlpha(img *image.NRGBA, alpha *ALPH) error {
	b := img.Bounds()
	w, h := b.Dx(), b.Dy()
	plane, err := decodeAlphaPlane(alpha, w, h)
	if err != nil {
		return err
	}
	// A preprocessing of "level reduction" stores fewer alpha levels; the
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
