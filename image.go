package simple_webp

import (
	"image"
	"image/color"
	"image/draw"
	"io"

	"github.com/pkg/errors"
)

func init() {
	// makes image.Decode and image.DecodeConfig recognize webp files
	image.RegisterFormat("webp", "RIFF????WEBP", Decode, DecodeConfig)
}

// Decode reads a webp stream and returns the decoded image, implementing the
// image.Decode signature.
func Decode(r io.Reader) (image.Image, error) {
	w, err := ParseWebp(r)
	if err != nil {
		return nil, err
	}
	return w.ToImage()
}

// DecodeConfig reads only the container header, it does not decode any pixel
// data.
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
			// skip the payload and its padding byte
			skip := int64(size) + int64(size&1)
			if _, err := io.CopyN(io.Discard, r, skip); err != nil {
				return image.Config{}, errors.WithMessagef(err, "invalid webp: truncated %q chunk", string(name))
			}
		}
	}
}

// Image decodes the image once and caches the result, it backs the
// image.Image implementation of Webp.
func (w *Webp) Image() (image.Image, error) {
	w.Once.Do(func() {
		w.img, w.imgErr = w.ToImage()
	})
	return w.img, w.imgErr
}

// Bounds implements image.Image: the canvas rectangle of the file.
func (w *Webp) Bounds() image.Rectangle {
	return image.Rect(0, 0, w.Width, w.Height)
}

// ColorModel implements image.Image. The decoders produce straight alpha
// NRGBA pixels.
func (w *Webp) ColorModel() color.Model {
	return color.NRGBAModel
}

// At implements image.Image. The image is decoded on the first call and then
// cached.
func (w *Webp) At(x, y int) color.Color {
	img, err := w.Image()
	if err != nil {
		return color.NRGBA{}
	}
	return img.At(x, y)
}

// drawCopy converts a straight alpha NRGBA image into a premultiplied RGBA
// one through image/draw, which has an optimized path for this pair.
func drawCopy(dst *image.RGBA, src *image.NRGBA) {
	draw.Draw(dst, dst.Bounds(), src, image.Point{}, draw.Src)
}
