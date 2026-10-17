package simple_webp

import (
	"bytes"
	"io"

	"github.com/pkg/errors"
)

// ChunkParse is the interface a custom chunk parser has to implement, the
// same extension point simple-png exposes.
type ChunkParse interface {
	ChunkName() FourCC
	Parse(chunk *riffChunk) error
}

// RawChunk is a chunk that was not decoded natively and is kept in its raw
// form, e.g. an unknown application specific chunk.
type RawChunk struct {
	// Name is the 4 byte chunk type code.
	Name FourCC
	// Data is the chunk payload without header and padding.
	Data []byte
}

/*
--------------------------------------------------------------------------------------
VP8X
--------------------------------------------------------------------------------------
*/

// VP8X flags, the bit positions defined by the container specification.
const (
	FlagICC       uint8 = 0x20
	FlagAlpha     uint8 = 0x10
	FlagEXIF      uint8 = 0x08
	FlagXMP       uint8 = 0x04
	FlagAnimation uint8 = 0x02
)

// VP8X
//
//	0                   1                   2                   3
//	0 1 2 3 4 5 6 7 8 9 0 1 2 3 4 5 6 7 8 9 0 1 2 3 4 5 6 7 8 9 0 1
//	+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+
//	|Rsv|I|L|E|X|A|R|                   Reserved                    |
//	+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+
//	|          Canvas Width Minus One               |             ...
//	+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+
//	...  Canvas Height Minus One    |
//	+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+
//
// I: ICC profile, L: alpha, E: EXIF, X: XMP, A: animation.
type VP8X struct {
	Flags        uint8
	CanvasWidth  int
	CanvasHeight int
}

func (v *VP8X) ChunkName() FourCC { return VP8XChunk }

func (v *VP8X) Parse(c *riffChunk) error {
	if len(c.data) < 10 {
		return errors.Errorf("invalid vp8x chunk: %d bytes", len(c.data))
	}
	v.Flags = c.data[0]
	v.CanvasWidth = int(readUint24(c.data[4:7])) + 1
	v.CanvasHeight = int(readUint24(c.data[7:10])) + 1
	return nil
}

func (v *VP8X) HasICC() bool       { return v.Flags&FlagICC != 0 }
func (v *VP8X) HasAlpha() bool     { return v.Flags&FlagAlpha != 0 }
func (v *VP8X) HasEXIF() bool      { return v.Flags&FlagEXIF != 0 }
func (v *VP8X) HasXMP() bool       { return v.Flags&FlagXMP != 0 }
func (v *VP8X) HasAnimation() bool { return v.Flags&FlagAnimation != 0 }

// data serializes the chunk payload.
func (v *VP8X) data() []byte {
	out := make([]byte, 10)
	out[0] = v.Flags
	writeUint24(out[4:7], uint32(v.CanvasWidth-1))
	writeUint24(out[7:10], uint32(v.CanvasHeight-1))
	return out
}

/*
--------------------------------------------------------------------------------------
VP8
--------------------------------------------------------------------------------------
*/

// VP8 is the lossy bitstream chunk. The payload is kept untouched: WebP lossy
// images are single VP8 key frames, the header is parsed here so the
// dimensions are known without decoding the frame.
type VP8 struct {
	Data []byte

	Width, Height int
	Version       uint8
	ShowFrame     bool
	FirstPartSize int
}

func (v *VP8) ChunkName() FourCC { return VP8Chunk }

func (v *VP8) Parse(c *riffChunk) error {
	if len(c.data) < 10 {
		return errors.Errorf("invalid vp8 chunk: %d bytes", len(c.data))
	}
	tag := uint32(c.data[0]) | uint32(c.data[1])<<8 | uint32(c.data[2])<<16
	if tag&0x1 != 0 {
		return errors.New("invalid vp8 chunk: not a key frame")
	}
	v.Version = uint8(tag >> 1 & 0x7)
	v.ShowFrame = tag>>4&0x1 != 0
	v.FirstPartSize = int(tag >> 5)
	if c.data[3] != 0x9d || c.data[4] != 0x01 || c.data[5] != 0x2a {
		return errors.New("invalid vp8 chunk: bad start code")
	}
	v.Width = int(by.Uint16(c.data[6:8]) & 0x3fff)
	v.Height = int(by.Uint16(c.data[8:10]) & 0x3fff)
	if v.Width == 0 || v.Height == 0 {
		return errors.New("invalid vp8 chunk: zero size")
	}
	v.Data = c.data
	return nil
}

/*
--------------------------------------------------------------------------------------
VP8L
--------------------------------------------------------------------------------------
*/

// VP8L is the lossless bitstream chunk, see
// https://developers.google.com/speed/webp/docs/webp_lossless_bitstream_specification
//
// The 5 byte header is:
//
//	signature       8 bits (0x2f)
//	width - 1      14 bits
//	height - 1     14 bits
//	alpha_is_used   1 bit
//	version_number  3 bits (must be 0)
type VP8L struct {
	Data []byte

	Width, Height int
	AlphaUsed     bool
	Version       uint8
}

func (v *VP8L) ChunkName() FourCC { return VP8LChunk }

func (v *VP8L) Parse(c *riffChunk) error {
	if len(c.data) < 5 {
		return errors.Errorf("invalid vp8l chunk: %d bytes", len(c.data))
	}
	if c.data[0] != 0x2f {
		return errors.Errorf("invalid vp8l chunk: bad signature 0x%02x", c.data[0])
	}
	bits := by.Uint32(c.data[1:5])
	v.Width = int(bits&0x3fff) + 1
	v.Height = int(bits>>14&0x3fff) + 1
	v.AlphaUsed = bits>>28&0x1 != 0
	v.Version = uint8(bits >> 29)
	if v.Version != 0 {
		return errors.Errorf("unsupported vp8l version %d", v.Version)
	}
	v.Data = c.data
	return nil
}

/*
--------------------------------------------------------------------------------------
ALPH
--------------------------------------------------------------------------------------
*/

// ALPH alpha channel compression methods.
const (
	ALPHCompressionNone     uint8 = 0
	ALPHCompressionLossless uint8 = 1
)

// ALPH alpha channel filtering methods.
const (
	ALPHFilterNone       uint8 = 0
	ALPHFilterHorizontal uint8 = 1
	ALPHFilterVertical   uint8 = 2
	ALPHFilterGradient   uint8 = 3
)

// ALPH carries the alpha plane of a lossy image.
//
//	0                   1
//	0 1 2 3 4 5 6 7 8 9 0 1 2 3 4 5
//	+-+-+-+-+-+-+-+-+
//	|C|F|P|Rsv|
//
// C: compression method (0 none, 1 lossless), F: filtering method,
// P: preprocessing (0 none, 1 level reduction).
type ALPH struct {
	Compression   uint8
	Filter        uint8
	Preprocessing uint8
	Data          []byte
}

func (a *ALPH) ChunkName() FourCC { return ALPHChunk }

func (a *ALPH) Parse(c *riffChunk) error {
	if len(c.data) < 1 {
		return errors.New("invalid alph chunk: empty")
	}
	head := c.data[0]
	a.Compression = head & 0x3
	a.Filter = head >> 2 & 0x3
	a.Preprocessing = head >> 4 & 0x3
	if a.Compression > ALPHCompressionLossless {
		return errors.Errorf("unsupported alph compression method %d", a.Compression)
	}
	a.Data = c.data[1:]
	return nil
}

func (a *ALPH) header() byte {
	return a.Compression | a.Filter<<2 | a.Preprocessing<<4
}

/*
--------------------------------------------------------------------------------------
ANIM / ANMF
--------------------------------------------------------------------------------------
*/

// ANIM holds the global animation parameters.
//
//	Background Color: 32 bits (BGRA)
//	Loop Count:       16 bits
type ANIM struct {
	// BackgroundColor is stored as 0xAARRGGBB in the file's BGRA byte order.
	BackgroundColor uint32
	LoopCount       uint16
}

func (a *ANIM) ChunkName() FourCC { return ANIMChunk }

func (a *ANIM) Parse(c *riffChunk) error {
	if len(c.data) < 6 {
		return errors.Errorf("invalid anim chunk: %d bytes", len(c.data))
	}
	a.BackgroundColor = by.Uint32(c.data[:4])
	a.LoopCount = by.Uint16(c.data[4:6])
	return nil
}

func (a *ANIM) data() []byte {
	out := make([]byte, 6)
	by.PutUint32(out[:4], a.BackgroundColor)
	by.PutUint16(out[4:6], a.LoopCount)
	return out
}

// ANMF is one animation frame.
//
//	Frame X:      24 bits (actual x / 2)
//	Frame Y:      24 bits (actual y / 2)
//	Frame Width:  24 bits (width - 1)
//	Frame Height: 24 bits (height - 1)
//	Duration:     24 bits (milliseconds)
//	Flags:         8 bits (bit1 blending, bit0 disposal)
//	Frame Data:   ALPH (optional) + VP8 / VP8L
type ANMF struct {
	X, Y          int
	Width, Height int
	Duration      int
	// Blend is true when the frame is alpha blended onto the canvas, false
	// when it overwrites the canvas region.
	Blend bool
	// Dispose is true when the canvas is cleared to the background color
	// after the frame was displayed.
	Dispose bool

	ALPH *ALPH
	VP8  *VP8
	VP8L *VP8L
}

func (a *ANMF) ChunkName() FourCC { return ANMFChunk }

func (a *ANMF) Parse(c *riffChunk) error {
	if len(c.data) < 16 {
		return errors.Errorf("invalid anmf chunk: %d bytes", len(c.data))
	}
	a.X = int(readUint24(c.data[0:3])) * 2
	a.Y = int(readUint24(c.data[3:6])) * 2
	a.Width = int(readUint24(c.data[6:9])) + 1
	a.Height = int(readUint24(c.data[9:12])) + 1
	a.Duration = int(readUint24(c.data[12:15]))
	flags := c.data[15]
	a.Blend = flags&0x2 == 0
	a.Dispose = flags&0x1 != 0

	r := bytes.NewReader(c.data[16:])
	for {
		sub, err := readRiffChunk(r)
		if err != nil {
			if errors.Is(err, io.EOF) {
				break
			}
			return errors.WithStack(err)
		}
		switch sub.name {
		case ALPHChunk:
			a.ALPH = &ALPH{}
			if err = a.ALPH.Parse(sub); err != nil {
				return err
			}
		case VP8Chunk:
			a.VP8 = &VP8{}
			if err = a.VP8.Parse(sub); err != nil {
				return err
			}
		case VP8LChunk:
			a.VP8L = &VP8L{}
			if err = a.VP8L.Parse(sub); err != nil {
				return err
			}
		}
	}
	if a.VP8 == nil && a.VP8L == nil {
		return errors.New("invalid anmf chunk: no image data")
	}
	return nil
}

func (a *ANMF) data() []byte {
	out := make([]byte, 16, 16+64)
	writeUint24(out[0:3], uint32(a.X/2))
	writeUint24(out[3:6], uint32(a.Y/2))
	writeUint24(out[6:9], uint32(a.Width-1))
	writeUint24(out[9:12], uint32(a.Height-1))
	writeUint24(out[12:15], uint32(a.Duration))
	var flags uint8
	if !a.Blend {
		flags |= 0x2
	}
	if a.Dispose {
		flags |= 0x1
	}
	out[15] = flags
	if a.ALPH != nil {
		out = append(out, marshalRiffChunk(ALPHChunk, append([]byte{a.ALPH.header()}, a.ALPH.Data...))...)
	}
	if a.VP8L != nil {
		out = append(out, marshalRiffChunk(VP8LChunk, a.VP8L.Data)...)
	}
	if a.VP8 != nil {
		out = append(out, marshalRiffChunk(VP8Chunk, a.VP8.Data)...)
	}
	return out
}

/*
--------------------------------------------------------------------------------------
Metadata chunks
--------------------------------------------------------------------------------------
*/

// ICCP is the embedded ICC color profile.
type ICCP struct {
	Data []byte
}

func (i *ICCP) ChunkName() FourCC { return ICCPChunk }

func (i *ICCP) Parse(c *riffChunk) error {
	i.Data = c.data
	return nil
}

// EXIF is the embedded EXIF metadata.
type EXIF struct {
	Data []byte
}

func (e *EXIF) ChunkName() FourCC { return EXIFChunk }

func (e *EXIF) Parse(c *riffChunk) error {
	e.Data = c.data
	return nil
}

// XMP is the embedded XMP metadata.
type XMP struct {
	Data []byte
}

func (x *XMP) ChunkName() FourCC { return XMPChunk }

func (x *XMP) Parse(c *riffChunk) error {
	x.Data = c.data
	return nil
}
