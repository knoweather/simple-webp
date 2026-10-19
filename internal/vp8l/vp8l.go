package vp8l

import (
	"errors"
	"image"
)

// ErrTooLarge 在图像超出格式上限或调用方给定的像素预算时返回。
// ErrTooLarge is returned when the image exceeds the format limits or the
// pixel budget of the call.
var ErrTooLarge = errors.New("vp8l: image too large")

// defaultMaxPixels 是默认解码像素预算：1<<27 像素约等于 11585×11585 的图像，
// 对应 512 MB 的 ARGB 像素缓冲。
// defaultMaxPixels bounds the decoded image: 1<<27 pixels is a 11585 by 11585
// image and 512 MB of ARGB pixels.
const defaultMaxPixels = 1 << 27

// DecodeARGB 解码一条完整的 VP8L 位流（含 5 字节文件头），按扫描线顺序返回
// 0xAARRGGBB 像素；像素预算取默认值 defaultMaxPixels。
// DecodeARGB decodes a complete VP8L bitstream (including its 5-byte header)
// into 0xAARRGGBB pixels in scanline order; the pixel budget is the default
// defaultMaxPixels.
func DecodeARGB(data []byte) (pix []uint32, width, height int, err error) {
	return DecodeARGBLimit(data, defaultMaxPixels)
}

// DecodeARGBLimit 是带显式像素预算的 DecodeARGB：损坏或恶意的文件头最多只能
// 让解码器分配 maxPixels 个像素；maxPixels<=0 表示不限制。
// DecodeARGBLimit is DecodeARGB with an explicit pixel budget: a corrupt or
// hostile header can not make the decoder allocate more than maxPixels pixels.
// A maxPixels <= 0 removes the limit.
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

// Decode 解码一条完整的 VP8L 位流，返回 NRGBA 图像；像素预算取默认值。
// Decode decodes a complete VP8L bitstream into an NRGBA image, using the
// default pixel budget.
func Decode(data []byte) (*image.NRGBA, error) {
	return DecodeLimit(data, defaultMaxPixels)
}

// DecodeLimit 是带显式像素预算的 Decode。
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

// DecodeConfig 只读取 VP8L 位流的 5 字节文件头，返回宽高与是否带 alpha 位；
// 适合在不解码像素的情况下探测尺寸。
// DecodeConfig reads only the 5-byte header of a VP8L bitstream and returns
// the width, height and the alpha-used flag; it is meant for probing the size
// without decoding pixels.
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

// DecodeAlpha 解码 ALPH chunk 使用的无文件头 VP8L 流：alpha 值承载在绿色通道上。
// 为此它先按 width/height 合成 5 字节文件头（alpha 位置 1），再复用 DecodeARGB，
// 最后从每个像素的绿色通道取出 alpha 字节。
// DecodeAlpha decodes a headerless VP8L stream as used by the ALPH chunk: the
// alpha values are carried by the green channel. It synthesizes the 5-byte
// header (with the alpha bit set) from width/height, reuses DecodeARGB, and
// extracts the alpha byte from the green channel of every pixel.
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
