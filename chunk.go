package simple_webp

import (
	"bytes"
	"io"

	"github.com/pkg/errors"
)

// ChunkParse 是自定义 chunk 解析器必须实现的接口，也是本包对外开放的扩展点
// （与 simple-png 的做法一致）：实现方在 ChunkName 中声明接管的 chunk 类型码，
// 由 Parse 解码该 chunk 的载荷。
//
// ChunkParse is the interface a custom chunk parser has to implement, the
// same extension point simple-png exposes: an implementation states in
// ChunkName which chunk type code it takes over and decodes that chunk's
// payload in Parse.
type ChunkParse interface {
	// ChunkName 返回解析器接管的 chunk 类型码。
	// ChunkName returns the chunk type code the parser handles.
	ChunkName() FourCC
	// Parse 解码给定 chunk 的载荷，出错时返回非 nil error。
	// Parse decodes the payload of the given chunk and returns a non-nil
	// error on failure.
	Parse(chunk *RawChunk) error
}

// RawChunk 表示未被原生解码、以原始字节形式保留的 chunk（例如未知的应用私有
// chunk）；重编码容器时它会原样写回。
//
// RawChunk is a chunk that was not decoded natively and is kept in its raw
// form, e.g. an application-specific chunk of unknown type; it is written
// back verbatim when the container is re-encoded.
type RawChunk struct {
	// Name 是 4 字节 chunk 类型码。/ Name is the 4-byte chunk type code.
	Name FourCC
	// Data 是去掉 chunk 头与对齐填充后的载荷。
	// Data is the chunk payload without the header and the pad byte.
	Data []byte
}

// ---------------------------------------------------------------------------
// VP8X：扩展 chunk / VP8X extended chunk
// ---------------------------------------------------------------------------

// VP8X 特性标志位，位位置由 WebP 容器规范定义。
// VP8X feature flags; the bit positions are defined by the container spec.
const (
	FlagICC       uint8 = 0x20
	FlagAlpha     uint8 = 0x10
	FlagEXIF      uint8 = 0x08
	FlagXMP       uint8 = 0x04
	FlagAnimation uint8 = 0x02
)

// VP8X 是扩展 chunk：10 字节载荷声明画布宽高（各按减一存储）以及文件包含哪些
// 扩展特性，其位布局如下（WebP 容器规范）：
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
// 标志字母：I: ICC profile, L: alpha, E: EXIF, X: XMP, A: animation。
//
// VP8X is the extended chunk: its 10-byte payload declares the canvas size
// (each dimension stored minus one) and which extended features the file
// carries. The bit layout above follows the WebP container spec; the flag
// letters are: I: ICC profile, L: alpha, E: EXIF, X: XMP, A: animation.
type VP8X struct {
	Flags        uint8
	CanvasWidth  int
	CanvasHeight int
}

// ChunkName 返回 VP8X 的 chunk 类型码。
// ChunkName returns the VP8X chunk type code.
func (v *VP8X) ChunkName() FourCC { return VP8XChunk }

// Parse 解析 VP8X 载荷：读入标志字节与画布宽高；宽高按减一存储，因此读出后
// 各加一。载荷不足 10 字节视为损坏。
// Parse decodes the VP8X payload: it reads the flag byte and the canvas
// dimensions, which are stored minus one and therefore get incremented.
// A payload shorter than 10 bytes is treated as corrupt.
func (v *VP8X) Parse(c *RawChunk) error {
	if len(c.Data) < 10 {
		return errors.Errorf("invalid vp8x chunk: %d bytes", len(c.Data))
	}
	v.Flags = c.Data[0]
	v.CanvasWidth = int(readUint24(c.Data[4:7])) + 1
	v.CanvasHeight = int(readUint24(c.Data[7:10])) + 1
	return nil
}

// HasICC 报告文件是否内嵌 ICC 色彩配置。/ HasICC reports whether an ICC profile is embedded.
func (v *VP8X) HasICC() bool { return v.Flags&FlagICC != 0 }

// HasAlpha 报告图像是否带 alpha 通道。/ HasAlpha reports whether an alpha channel is present.
func (v *VP8X) HasAlpha() bool { return v.Flags&FlagAlpha != 0 }

// HasEXIF 报告文件是否内嵌 EXIF 元数据。/ HasEXIF reports whether EXIF metadata is embedded.
func (v *VP8X) HasEXIF() bool { return v.Flags&FlagEXIF != 0 }

// HasXMP 报告文件是否内嵌 XMP 元数据。/ HasXMP reports whether XMP metadata is embedded.
func (v *VP8X) HasXMP() bool { return v.Flags&FlagXMP != 0 }

// HasAnimation 报告该文件是否为动画。/ HasAnimation reports whether the file is an animation.
func (v *VP8X) HasAnimation() bool { return v.Flags&FlagAnimation != 0 }

// data 把 VP8X 重新序列化为 10 字节载荷，宽高仍按减一存储，与 Parse 互逆。
// data re-serializes VP8X into its 10-byte payload, storing the canvas size
// minus one again, the inverse of Parse.
func (v *VP8X) data() []byte {
	out := make([]byte, 10)
	out[0] = v.Flags
	writeUint24(out[4:7], uint32(v.CanvasWidth-1))
	writeUint24(out[7:10], uint32(v.CanvasHeight-1))
	return out
}

// ---------------------------------------------------------------------------
// VP8：有损码流 chunk / VP8 lossy bitstream chunk
// ---------------------------------------------------------------------------

// VP8 是有损码流 chunk。载荷原样保留、不作改动：WebP 静态有损图像只有一帧
// VP8 关键帧，这里仅解析帧头，不解码即可拿到图像尺寸。
//
// VP8 is the lossy bitstream chunk. The payload is kept untouched: a WebP
// lossy still image is a single VP8 key frame, so parsing the frame header
// alone already yields the image dimensions without decoding the frame.
type VP8 struct {
	// Data 是整个 VP8 chunk 载荷，含帧头，原样保留。
	// Data is the whole VP8 chunk payload including the frame header, kept verbatim.
	Data []byte

	// Width、Height 是帧头声明的画布尺寸（各 16 位字段的低 14 位）。
	// Width and Height are the canvas dimensions declared by the frame
	// header (the low 14 bits of each 16-bit field).
	Width, Height int
	// Version 是帧头中的编码器版本（bit1-3）。/ Version is the encoder version from the frame header (bits 1-3).
	Version uint8
	// ShowFrame 表示该帧解码后立即显示。/ ShowFrame reports that the frame is displayed right after decoding.
	ShowFrame bool
	// FirstPartSize 是首分区的字节数。/ FirstPartSize is the size of the first partition in bytes.
	FirstPartSize int
}

// ChunkName 返回 VP8 的 chunk 类型码。
// ChunkName returns the VP8 chunk type code.
func (v *VP8) ChunkName() FourCC { return VP8Chunk }

// Parse 解析 VP8 帧头：3 字节 tag（bit0 为 0 表示关键帧，bit1-3 为版本，
// bit4 为 show_frame，bit5 起为首分区字节长）、随后 3 字节起始码 0x9d 0x01 0x2a、
// 以及宽高字段（各 16 位，取低 14 位，高 2 位为缩放系数，此处忽略）。
// Data 保留整个载荷；非关键帧、坏起始码或零尺寸都判为损坏。
// Parse decodes the VP8 frame header: the 3-byte tag (bit 0 clear means a
// key frame, bits 1-3 the version, bit 4 show_frame, bits 5+ the size of
// the first partition), the 3-byte start code 0x9d 0x01 0x2a that follows,
// and the dimension fields (16 bits each, the low 14 bits taken, the high
// 2 scale bits ignored here). Data keeps the whole payload; a non-key frame,
// a bad start code or a zero dimension is rejected.
func (v *VP8) Parse(c *RawChunk) error {
	if len(c.Data) < 10 {
		return errors.Errorf("invalid vp8 chunk: %d bytes", len(c.Data))
	}
	tag := uint32(c.Data[0]) | uint32(c.Data[1])<<8 | uint32(c.Data[2])<<16
	if tag&0x1 != 0 {
		return errors.New("invalid vp8 chunk: not a key frame")
	}
	v.Version = uint8(tag >> 1 & 0x7)
	v.ShowFrame = tag>>4&0x1 != 0
	v.FirstPartSize = int(tag >> 5)
	if c.Data[3] != 0x9d || c.Data[4] != 0x01 || c.Data[5] != 0x2a {
		return errors.New("invalid vp8 chunk: bad start code")
	}
	v.Width = int(by.Uint16(c.Data[6:8]) & 0x3fff)
	v.Height = int(by.Uint16(c.Data[8:10]) & 0x3fff)
	if v.Width == 0 || v.Height == 0 {
		return errors.New("invalid vp8 chunk: zero size")
	}
	v.Data = c.Data
	return nil
}

// ---------------------------------------------------------------------------
// VP8L：无损码流 chunk / VP8L lossless bitstream chunk
// ---------------------------------------------------------------------------

// VP8L 是无损码流 chunk，头部布局见无损码流规范
// https://developers.google.com/speed/webp/docs/webp_lossless_bitstream_specification：
// 5 字节头按位排列如下，宽高按减一存储（读出后各加一），版本号必须为 0。
//
// VP8L is the lossless bitstream chunk; its header layout follows the
// lossless bitstream specification at
// https://developers.google.com/speed/webp/docs/webp_lossless_bitstream_specification.
// The 5-byte header is laid out bit-wise as follows; the dimensions are
// stored minus one (and get incremented) and the version number must be 0.
//
//	signature       8 bits (0x2f)
//	width - 1      14 bits
//	height - 1     14 bits
//	alpha_is_used   1 bit
//	version_number  3 bits (must be 0)
type VP8L struct {
	// Data 是整个 VP8L chunk 载荷，含 5 字节头，原样保留。
	// Data is the whole VP8L chunk payload including the 5-byte header, kept verbatim.
	Data []byte

	// Width、Height 是从头部读出并加一后的实际尺寸。
	// Width and Height are the actual dimensions read from the header and incremented.
	Width, Height int
	// AlphaUsed 是头部的 alpha_is_used 标志位。
	// AlphaUsed is the alpha_is_used flag from the header.
	AlphaUsed bool
	// Version 是头部的版本号，非 0 会被 Parse 拒绝。
	// Version is the header version number; anything but 0 is rejected by Parse.
	Version uint8
}

// ChunkName 返回 VP8L 的 chunk 类型码。
// ChunkName returns the VP8L chunk type code.
func (v *VP8L) ChunkName() FourCC { return VP8LChunk }

// Parse 解析 VP8L 的 5 字节头：先校验签名 0x2f，再读出宽高（减一存储，各加一）、
// alpha 使用标志与版本号；版本号非 0 报错。Data 保留整个载荷。
// Parse decodes the 5-byte VP8L header: it checks the 0x2f signature first,
// then reads the dimensions (stored minus one, so incremented), the
// alpha-used flag and the version number; a version other than 0 is
// rejected. Data keeps the whole payload.
func (v *VP8L) Parse(c *RawChunk) error {
	if len(c.Data) < 5 {
		return errors.Errorf("invalid vp8l chunk: %d bytes", len(c.Data))
	}
	if c.Data[0] != 0x2f {
		return errors.Errorf("invalid vp8l chunk: bad signature 0x%02x", c.Data[0])
	}
	bits := by.Uint32(c.Data[1:5])
	v.Width = int(bits&0x3fff) + 1
	v.Height = int(bits>>14&0x3fff) + 1
	v.AlphaUsed = bits>>28&0x1 != 0
	v.Version = uint8(bits >> 29)
	if v.Version != 0 {
		return errors.Errorf("unsupported vp8l version %d", v.Version)
	}
	v.Data = c.Data
	return nil
}

// ---------------------------------------------------------------------------
// ALPH：alpha 通道 chunk / ALPH alpha channel chunk
// ---------------------------------------------------------------------------

// ALPH 的 alpha 通道压缩方法常量。
// ALPH alpha channel compression methods.
const (
	ALPHCompressionNone     uint8 = 0
	ALPHCompressionLossless uint8 = 1
)

// ALPH 的 alpha 通道过滤方法常量。
// ALPH alpha channel filtering methods.
const (
	ALPHFilterNone       uint8 = 0
	ALPHFilterHorizontal uint8 = 1
	ALPHFilterVertical   uint8 = 2
	ALPHFilterGradient   uint8 = 3
)

// ALPH 携带有损图像的 alpha 平面：首字节为标志位，其余为压缩后的 alpha 数据。
//
//	0                   1
//	0 1 2 3 4 5 6 7 8 9 0 1 2 3 4 5
//	+-+-+-+-+-+-+-+-+
//	|C|F|P|Rsv|
//
// C: 压缩方法（0 none, 1 lossless）；F: 过滤方法；P: 预处理（0 none,
// 1 level reduction）。
//
// ALPH carries the alpha plane of a lossy image: one flag byte followed by
// the compressed alpha data. C: compression method (0 none, 1 lossless),
// F: filtering method, P: preprocessing (0 none, 1 level reduction).
type ALPH struct {
	// Compression 是压缩方法，见 ALPHCompression* 常量。
	// Compression is the compression method, see the ALPHCompression* constants.
	Compression uint8
	// Filter 是过滤方法，见 ALPHFilter* 常量。
	// Filter is the filtering method, see the ALPHFilter* constants.
	Filter uint8
	// Preprocessing 是预处理方法（0 无，1 level reduction）。
	// Preprocessing is the preprocessing method (0 none, 1 level reduction).
	Preprocessing uint8
	// Data 是去掉首字节标志后的压缩 alpha 数据。
	// Data is the compressed alpha data without the leading flag byte.
	Data []byte

	// reserved 是标志字节里的保留位（bit 6-7）。规范要求它们为 0，解析时保留
	// 原值，未改动的文件才能逐字节回写。
	// reserved holds the reserved bits of the flag byte (bits 6-7). The spec
	// requires them to be zero; the parsed value is kept so that an untouched
	// file is written back byte for byte.
	reserved uint8
}

// ChunkName 返回 ALPH 的 chunk 类型码。
// ChunkName returns the ALPH chunk type code.
func (a *ALPH) ChunkName() FourCC { return ALPHChunk }

// Parse 解析 ALPH 首字节中的压缩（bit0-1）、过滤（bit2-3）与预处理（bit4-5）
// 方法，其余字节作为压缩 alpha 数据保留；压缩方法大于 1 报错。
// Parse decodes the compression (bits 0-1), filtering (bits 2-3) and
// preprocessing (bits 4-5) methods from the ALPH flag byte and keeps the
// remaining bytes as compressed alpha data; a compression method above 1
// is rejected.
func (a *ALPH) Parse(c *RawChunk) error {
	if len(c.Data) < 1 {
		return errors.New("invalid alph chunk: empty")
	}
	head := c.Data[0]
	a.Compression = head & 0x3
	a.Filter = head >> 2 & 0x3
	a.Preprocessing = head >> 4 & 0x3
	a.reserved = head >> 6 & 0x3
	if a.Compression > ALPHCompressionLossless {
		return errors.Errorf("unsupported alph compression method %d", a.Compression)
	}
	a.Data = c.Data[1:]
	return nil
}

// header 由压缩、过滤与预处理字段重新拼出 ALPH 首字节，与 Parse 互逆。
// header rebuilds the ALPH flag byte from the compression, filtering and
// preprocessing fields, the inverse of Parse.
func (a *ALPH) header() byte {
	return a.Compression | a.Filter<<2 | a.Preprocessing<<4 | a.reserved<<6
}

// ---------------------------------------------------------------------------
// ANIM / ANMF：动画 chunk / animation chunks
// ---------------------------------------------------------------------------

// ANIM 保存整段动画的全局参数，6 字节载荷布局如下（WebP 容器规范）：
//
//	Background Color: 32 bits (BGRA)
//	Loop Count:       16 bits
//
// ANIM holds the global parameters of an animation. Its 6-byte payload is
// laid out as follows (WebP container spec): a 32-bit background colour in
// BGRA byte order and a 16-bit loop count.
type ANIM struct {
	// BackgroundColor 按文件的 BGRA 字节序读入，Go 中记作 0xAARRGGBB。
	// BackgroundColor is read in the file's BGRA byte order and held in Go
	// as 0xAARRGGBB.
	BackgroundColor uint32
	// LoopCount 是播放完所有帧后的循环次数，0 表示无限循环。
	// LoopCount is how often the animation is played again after the last
	// frame; 0 means loop forever.
	LoopCount uint16
}

// ChunkName 返回 ANIM 的 chunk 类型码。
// ChunkName returns the ANIM chunk type code.
func (a *ANIM) ChunkName() FourCC { return ANIMChunk }

// Parse 解析 6 字节 ANIM 载荷：4 字节背景色（BGRA 字节序）与 2 字节循环次数。
// Parse decodes the 6-byte ANIM payload: the 32-bit background colour in
// BGRA byte order and the 16-bit loop count.
func (a *ANIM) Parse(c *RawChunk) error {
	if len(c.Data) < 6 {
		return errors.Errorf("invalid anim chunk: %d bytes", len(c.Data))
	}
	a.BackgroundColor = by.Uint32(c.Data[:4])
	a.LoopCount = by.Uint16(c.Data[4:6])
	return nil
}

// data 把 ANIM 重新序列化为 6 字节载荷。/ data re-serializes ANIM into its 6-byte payload.
func (a *ANIM) data() []byte {
	out := make([]byte, 6)
	by.PutUint32(out[:4], a.BackgroundColor)
	by.PutUint16(out[4:6], a.LoopCount)
	return out
}

// ANMF 表示动画中的一帧：16 字节头部给出帧在画布上的位置、尺寸、显示时长与
// 混合/清理标志，其后嵌套本帧的图像数据 chunk（可选的 ALPH，加 VP8 或 VP8L
// 之一）。头部字段布局（WebP 容器规范）见下：
//
//	Frame X:      24 bits (actual x / 2)
//	Frame Y:      24 bits (actual y / 2)
//	Frame Width:  24 bits (width - 1)
//	Frame Height: 24 bits (height - 1)
//	Duration:     24 bits (milliseconds)
//	Flags:         8 bits (bit1 blending, bit0 disposal)
//	Frame Data:   ALPH (optional) + VP8 / VP8L
//
// ANMF is one animation frame: a 16-byte header with the frame position,
// size, duration and blend/disposal flags, followed by the frame's nested
// image chunk (an optional ALPH plus either VP8 or VP8L); the header field
// layout is shown above (WebP container spec).
type ANMF struct {
	// X、Y 是帧左上角在画布上的坐标；文件中按一半存储，读出后乘 2。
	// X and Y are the frame's top-left position on the canvas; they are
	// stored halved in the file and doubled on read.
	X, Y int
	// Width、Height 是帧尺寸，文件中按减一存储，读出后加一。
	// Width and Height are the frame size, stored minus one and incremented on read.
	Width, Height int
	// Duration 是本帧的显示时长，单位毫秒。/ Duration is how long the frame is shown, in milliseconds.
	Duration int
	// Blend 为 true 时帧以 alpha 混合叠加到画布，为 false 时直接覆盖画布对应区域。
	// Blend is true when the frame is alpha blended onto the canvas, false
	// when it overwrites the canvas region.
	Blend bool
	// Dispose 为 true 时本帧显示完后把画布清为背景色。
	// Dispose is true when the canvas is cleared to the background color
	// after the frame was displayed.
	Dispose bool

	// ALPH 是本帧的 alpha 平面，可为 nil。/ ALPH is the frame's alpha plane, may be nil.
	ALPH *ALPH
	// VP8 与 VP8L 恰有一个非 nil，承载本帧的图像码流。
	// Exactly one of VP8 and VP8L is non-nil and carries the frame bitstream.
	VP8  *VP8
	VP8L *VP8L

	// subs 按原始顺序保存本帧的全部子 chunk（已知与未知），供 data 原样重建。
	// subs holds all of the frame's sub-chunks in the order they appeared in the
	// file, known and unknown alike, so data can rebuild the frame unchanged.
	subs []*riffChunk

	// flags 是帧头里的原始标志字节。规范要求其中 6 个保留位为 0；解析时保留
	// 原值，未改动的文件才能逐字节回写。
	// flags is the raw flag byte of the frame header. The spec requires its six
	// reserved bits to be zero; the parsed value is kept so that an untouched
	// file is written back byte for byte.
	flags uint8

	// maxChunkSize 是解析嵌套子 chunk 时的载荷上限；0 表示默认值。
	// maxChunkSize caps the payload of the nested sub-chunks; 0 means the
	// default.
	maxChunkSize int
}

// ChunkName 返回 ANMF 的 chunk 类型码。
// ChunkName returns the ANMF chunk type code.
func (a *ANMF) ChunkName() FourCC { return ANMFChunk }

// Parse 解析 ANMF：位置 x/y 读出后乘 2、宽高读出后加一，时长单位为毫秒；
// 载荷剩余部分按嵌套 RIFF 流处理，依次解出其中的 ALPH、VP8、VP8L 子 chunk；
// 既无 VP8 也无 VP8L 时报错。
// Parse decodes an ANMF chunk: the position gets doubled and the size
// incremented on read, the duration is in milliseconds; the rest of the
// payload is treated as a nested RIFF stream from which the ALPH, VP8 and
// VP8L sub-chunks are parsed. A frame without any image data is rejected.
func (a *ANMF) Parse(c *RawChunk) error {
	if len(c.Data) < 16 {
		return errors.Errorf("invalid anmf chunk: %d bytes", len(c.Data))
	}
	a.X = int(readUint24(c.Data[0:3])) * 2
	a.Y = int(readUint24(c.Data[3:6])) * 2
	a.Width = int(readUint24(c.Data[6:9])) + 1
	a.Height = int(readUint24(c.Data[9:12])) + 1
	a.Duration = int(readUint24(c.Data[12:15]))
	flags := c.Data[15]
	a.flags = flags
	a.Blend = flags&0x2 == 0
	a.Dispose = flags&0x1 != 0

	r := bytes.NewReader(c.Data[16:])
	for {
		// 逐个读出嵌套的子 chunk 直到流结束；未知类型的子 chunk 只记录不解析。
		// Read the nested sub-chunks until the stream ends; unknown types are
		// recorded but not decoded.
		sub, err := readRiffChunk(r, a.maxChunkSize)
		if err != nil {
			if errors.Is(err, io.EOF) {
				break
			}
			return errors.WithStack(err)
		}
		// 记下读出的顺序与原始载荷，写回时按这个顺序原样输出未知类型。
		// Record the order and the raw payload so writing emits the sub-chunks
		// in the same order and unknown ones byte for byte.
		a.subs = append(a.subs, sub)
		switch sub.name {
		case ALPHChunk:
			// 同一帧内的子 chunk 只允许出现一次；VP8 与 VP8L 更是互斥，
			// 重复或冲突的帧无法确定该用哪份码流，直接报错而不是覆盖。
			// A sub-chunk may appear only once per frame and VP8 and VP8L are
			// mutually exclusive; a duplicate or conflicting frame cannot be
			// resolved to one bitstream, so it is rejected instead of overwritten.
			if a.ALPH != nil {
				return errors.Errorf("invalid anmf chunk: duplicate %q sub-chunk", string(sub.name))
			}
			a.ALPH = &ALPH{}
			if err = a.ALPH.Parse(&RawChunk{Name: sub.name, Data: sub.data}); err != nil {
				return err
			}
		case VP8Chunk:
			if a.VP8 != nil {
				return errors.Errorf("invalid anmf chunk: duplicate %q sub-chunk", string(sub.name))
			}
			if a.VP8L != nil {
				return errors.Errorf("invalid anmf chunk: both %q and %q sub-chunks", string(sub.name), string(VP8LChunk))
			}
			a.VP8 = &VP8{}
			if err = a.VP8.Parse(&RawChunk{Name: sub.name, Data: sub.data}); err != nil {
				return err
			}
		case VP8LChunk:
			if a.VP8L != nil {
				return errors.Errorf("invalid anmf chunk: duplicate %q sub-chunk", string(sub.name))
			}
			if a.VP8 != nil {
				return errors.Errorf("invalid anmf chunk: both %q and %q sub-chunks", string(sub.name), string(VP8Chunk))
			}
			a.VP8L = &VP8L{}
			if err = a.VP8L.Parse(&RawChunk{Name: sub.name, Data: sub.data}); err != nil {
				return err
			}
		}
	}
	if a.VP8 == nil && a.VP8L == nil {
		return errors.New("invalid anmf chunk: no image data")
	}
	return nil
}

// data 把这一帧重新序列化：16 字节头部后嵌套子 chunk。经过 Parse 的帧按 subs 记录
// 的原始顺序逐个写出，未知类型与对应结构体为 nil 的子 chunk 用原始载荷原样写回；
// 原始流里没有、但已被赋值的新结构体（例如解析后补上的 ALPH）追加在最后，顺序为
// ALPH、VP8L、VP8——未经 Parse 的手工构造帧同样走这条固定顺序。
//
// 帧头的每个字段都有格式上限，超出就无法表示：坐标以 x/2 存储且为奇数时
// 会静默丢位、时长与宽高是 24 位字段。这些情况一律返回错误，而不是悄悄
// 写出一个语义被改变的文件。
//
// data re-serializes the frame: the 16-byte header followed by the sub-chunks.
// A parsed frame is written in the order recorded in subs, with unknown types and
// sub-chunks whose struct is nil emitted from their original payload; a struct
// assigned after parsing (an ALPH added later, for instance) has no place in the
// original stream, so it is appended in the fixed order ALPH, VP8L, VP8 — the
// same order a manually built frame without subs is written in.
//
// Every field of the frame header has a format limit beyond which it cannot be
// represented: the position is stored as x/2 so an odd value would lose a bit
// silently, and the duration and the dimensions are 24 bit fields. Those cases
// are errors rather than a file whose meaning quietly changed.
func (a *ANMF) data() ([]byte, error) {
	if a.X < 0 || a.Y < 0 || a.X%2 != 0 || a.Y%2 != 0 {
		return nil, errors.Errorf("webp: frame position %d,%d must be even and non-negative", a.X, a.Y)
	}
	if a.X >= 1<<25 || a.Y >= 1<<25 {
		return nil, errors.Errorf("webp: frame position %d,%d out of range", a.X, a.Y)
	}
	if a.Width <= 0 || a.Height <= 0 || a.Width > 1<<24 || a.Height > 1<<24 {
		return nil, errors.Errorf("webp: frame size %dx%d out of range", a.Width, a.Height)
	}
	if a.Duration < 0 || a.Duration >= 1<<24 {
		return nil, errors.Errorf("webp: frame duration %d out of range 0..%d", a.Duration, 1<<24-1)
	}
	out := make([]byte, 16, 16+64)
	writeUint24(out[0:3], uint32(a.X/2))
	writeUint24(out[3:6], uint32(a.Y/2))
	writeUint24(out[6:9], uint32(a.Width-1))
	writeUint24(out[9:12], uint32(a.Height-1))
	writeUint24(out[12:15], uint32(a.Duration))
	var flags uint8 = a.flags
	if a.Blend {
		flags &^= 0x2
	} else {
		flags |= 0x2
	}
	if a.Dispose {
		flags |= 0x1
	} else {
		flags &^= 0x1
	}
	out[15] = flags
	var sawALPH, sawVP8L, sawVP8 bool
	for _, sub := range a.subs {
		switch {
		case sub.name == ALPHChunk && a.ALPH != nil:
			out = append(out, marshalRiffChunk(ALPHChunk, append([]byte{a.ALPH.header()}, a.ALPH.Data...))...)
			sawALPH = true
		case sub.name == VP8Chunk && a.VP8 != nil:
			out = append(out, marshalRiffChunk(VP8Chunk, a.VP8.Data)...)
			sawVP8 = true
		case sub.name == VP8LChunk && a.VP8L != nil:
			out = append(out, marshalRiffChunk(VP8LChunk, a.VP8L.Data)...)
			sawVP8L = true
		default:
			out = append(out, marshalRiffChunk(sub.name, sub.data)...)
		}
	}
	if a.ALPH != nil && !sawALPH {
		out = append(out, marshalRiffChunk(ALPHChunk, append([]byte{a.ALPH.header()}, a.ALPH.Data...))...)
	}
	if a.VP8L != nil && !sawVP8L {
		out = append(out, marshalRiffChunk(VP8LChunk, a.VP8L.Data)...)
	}
	if a.VP8 != nil && !sawVP8 {
		out = append(out, marshalRiffChunk(VP8Chunk, a.VP8.Data)...)
	}
	return out, nil
}

// ---------------------------------------------------------------------------
// 元数据 chunk / metadata chunks
// ---------------------------------------------------------------------------

// ICCP 携带内嵌的 ICC 色彩配置文件。/ ICCP carries the embedded ICC color profile.
type ICCP struct {
	// Data 是原始 ICC 配置字节。/ Data is the raw ICC profile bytes.
	Data []byte
}

// ChunkName 返回 ICCP 的 chunk 类型码。/ ChunkName returns the ICCP chunk type code.
func (i *ICCP) ChunkName() FourCC { return ICCPChunk }

// Parse 原样保留 ICCP 载荷，不作解释。/ Parse keeps the ICCP payload verbatim without interpreting it.
func (i *ICCP) Parse(c *RawChunk) error {
	i.Data = c.Data
	return nil
}

// EXIF 携带内嵌的 EXIF 元数据。/ EXIF carries the embedded EXIF metadata.
type EXIF struct {
	// Data 是原始 EXIF 字节。/ Data is the raw EXIF bytes.
	Data []byte
}

// ChunkName 返回 EXIF 的 chunk 类型码。/ ChunkName returns the EXIF chunk type code.
func (e *EXIF) ChunkName() FourCC { return EXIFChunk }

// Parse 原样保留 EXIF 载荷，不作解释。/ Parse keeps the EXIF payload verbatim without interpreting it.
func (e *EXIF) Parse(c *RawChunk) error {
	e.Data = c.Data
	return nil
}

// XMP 携带内嵌的 XMP 元数据。/ XMP carries the embedded XMP metadata.
type XMP struct {
	// Data 是原始 XMP 字节。/ Data is the raw XMP bytes.
	Data []byte
}

// ChunkName 返回 XMP 的 chunk 类型码。/ ChunkName returns the XMP chunk type code.
func (x *XMP) ChunkName() FourCC { return XMPChunk }

// Parse 原样保留 XMP 载荷，不作解释。/ Parse keeps the XMP payload verbatim without interpreting it.
func (x *XMP) Parse(c *RawChunk) error {
	x.Data = c.Data
	return nil
}
