package vp8l

import (
	"errors"
	"image"
)

// ErrTooLarge is returned when the image exceeds the format limits or the
// pixel budget of the call.
var ErrTooLarge = errors.New("vp8l: image too large")

// defaultMaxPixels bounds the decoded image, 1<<27 pixels is a 11585 by 11585
// image and 512 MB of ARGB pixels.
const defaultMaxPixels = 1 << 27

// DecodeARGB decodes a complete VP8L bitstream (including its 5 byte header)
// into 0xAARRGGBB pixels in scanline order.
func DecodeARGB(data []byte) (pix []uint32, width, height int, err error) {
	return DecodeARGBLimit(data, defaultMaxPixels)
}

// DecodeARGBLimit is DecodeARGB with an explicit pixel budget: a corrupt or
// hostile header can not make the decoder allocate more than maxPixels pixels.
func DecodeARGBLimit(data []byte, maxPixels int) (pix []uint32, width, height int, err error) {
	br := newBitReader(data)
	if br.readBits(8) != 0x2f {
		return nil, 0, 0, errors.New("vp8l: bad signature")
	}
	width = int(br.readBits(14)) + 1
	height = int(br.readBits(14)) + 1
	alphaUsed := br.readBits(1)
	if version := br.readBits(3); version != 0 {
		return nil, 0, 0, errors.New("vp8l: unsupported version")
	}
	if br.err != nil {
		return nil, 0, 0, br.err
	}
	if maxPixels > 0 && int64(width)*int64(height) > int64(maxPixels) {
		return nil, 0, 0, ErrTooLarge
	}

	d := &decoder{br: br}
	pix, err = d.decodeImageStream(width, height, true)
	if err != nil {
		return nil, 0, 0, err
	}
	if alphaUsed == 0 {
		for i := range pix {
			pix[i] |= 0xff000000
		}
	}
	return pix, width, height, nil
}

// Decode decodes a complete VP8L bitstream into an NRGBA image.
func Decode(data []byte) (*image.NRGBA, error) {
	return DecodeLimit(data, defaultMaxPixels)
}

// DecodeLimit is Decode with an explicit pixel budget.
func DecodeLimit(data []byte, maxPixels int) (*image.NRGBA, error) {
	pix, width, height, err := DecodeARGBLimit(data, maxPixels)
	if err != nil {
		return nil, err
	}
	img := image.NewNRGBA(image.Rect(0, 0, width, height))
	for i, p := range pix {
		img.Pix[4*i+0] = byte(p >> 16)
		img.Pix[4*i+1] = byte(p >> 8)
		img.Pix[4*i+2] = byte(p)
		img.Pix[4*i+3] = byte(p >> 24)
	}
	return img, nil
}

// DecodeConfig reads only the 5 byte header of a VP8L bitstream.
func DecodeConfig(data []byte) (width, height int, alphaUsed bool, err error) {
	if len(data) < 5 {
		return 0, 0, false, errors.New("vp8l: truncated header")
	}
	if data[0] != 0x2f {
		return 0, 0, false, errors.New("vp8l: bad signature")
	}
	bits := uint32(data[1]) | uint32(data[2])<<8 | uint32(data[3])<<16 | uint32(data[4])<<24
	width = int(bits&0x3fff) + 1
	height = int(bits>>14&0x3fff) + 1
	alphaUsed = bits>>28&0x1 != 0
	if version := bits >> 29; version != 0 {
		return 0, 0, false, errors.New("vp8l: unsupported version")
	}
	return width, height, alphaUsed, nil
}

// DecodeAlpha decodes a headerless VP8L stream as used by the ALPH chunk: the
// alpha values are carried by the green channel.
func DecodeAlpha(data []byte, width, height int) ([]byte, error) {
	if width <= 0 || height <= 0 || width > maxDimension || height > maxDimension {
		return nil, ErrTooLarge
	}
	stream := make([]byte, 5+len(data))
	stream[0] = 0x2f
	bits := uint32(width-1) | uint32(height-1)<<14 | 1<<28
	stream[1] = byte(bits)
	stream[2] = byte(bits >> 8)
	stream[3] = byte(bits >> 16)
	stream[4] = byte(bits >> 24)
	copy(stream[5:], data)

	pix, _, _, err := DecodeARGB(stream)
	if err != nil {
		return nil, err
	}
	alpha := make([]byte, len(pix))
	for i, p := range pix {
		alpha[i] = byte(p >> 8)
	}
	return alpha, nil
}
