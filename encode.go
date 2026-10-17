package simple_webp

import (
	"bytes"
	"image"
	"image/draw"
	"io"

	"github.com/pkg/errors"

	"github.com/knoweather/simple-webp/internal/vp8l"
)

// EncodeOptions tunes the lossless encoder.
type EncodeOptions struct {
	// Lossless is informational: this library only writes lossless webp, so
	// the value is accepted and ignored.
	Lossless bool
	// Method trades speed for size: 0 stores literals only, 1 adds the
	// subtract green transform, colour indexing and LZ77, 2 (the default)
	// also applies a predictor transform.
	Method int
}

// Encode writes m as a lossless WebP file.
func Encode(w io.Writer, m image.Image, opt *EncodeOptions) error {
	data, err := EncodeBytes(m, opt)
	if err != nil {
		return err
	}
	_, err = w.Write(data)
	return err
}

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
