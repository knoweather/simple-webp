package simple_webp

import (
	"image"
	"image/color"
	"io"

	"github.com/pkg/errors"

	"github.com/knoweather/simple-webp/internal/vp8"
	"github.com/knoweather/simple-webp/internal/vp8l"
)

// Frame 是 EncodeAnimation 的一帧输入：画布上的位置、显示时长、混合与清理
// 方式，以及这一帧的像素。
//
// Frame is one input frame of EncodeAnimation: its position on the canvas, its
// display duration, its blend and disposal mode, and its pixels.
type Frame struct {
	// Image 是这一帧的像素，尺寸即帧矩形的大小。
	//
	// Image holds the frame pixels; its size is the frame rectangle.
	Image image.Image
	// X、Y 是帧左上角在画布上的坐标，必须是偶数（文件中按一半存储）。
	//
	// X and Y are the frame's top-left position, which must be even (the file
	// stores them halved).
	X, Y int
	// Duration 是这一帧的显示时长，单位毫秒。
	//
	// Duration is how long the frame is shown, in milliseconds.
	Duration int
	// Blend 为 true 时帧与画布按 alpha 混合，为 false 时直接覆盖。
	//
	// Blend composites the frame onto the canvas with alpha; false overwrites.
	Blend bool
	// Dispose 为 true 时本帧显示完后把画布清为背景色。
	//
	// Dispose clears the canvas to the background colour after the frame.
	Dispose bool
}

// Animation 是 EncodeAnimation 的输入：画布参数与帧序列。
//
// Animation is the EncodeAnimation input: the canvas parameters and the frames.
type Animation struct {
	// CanvasWidth、CanvasHeight 是画布尺寸，帧矩形必须落在画布内。
	//
	// CanvasWidth and CanvasHeight are the canvas size; every frame must fit.
	CanvasWidth, CanvasHeight int
	// BackgroundColor 是背景色，写在 ANIM 里。
	//
	// BackgroundColor is written into ANIM.
	BackgroundColor color.NRGBA
	// LoopCount 是循环次数，0 表示无限循环。
	//
	// LoopCount is the number of repeats; 0 loops forever.
	LoopCount uint16
	// Frames 按显示顺序排列，至少一帧。
	//
	// Frames are in display order and hold at least one frame.
	Frames []Frame
}

// AnimationOptions 选择每一帧的编码方式。
//
// AnimationOptions selects how every frame is encoded.
type AnimationOptions struct {
	// Lossy 为 true 时每帧用 VP8 有损编码，否则用 VP8L 无损编码。
	//
	// Lossy encodes every frame with VP8; false uses VP8L lossless.
	Lossy bool
	// Encode 是无损帧的编码选项。/ Encode holds the lossless options.
	Encode *EncodeOptions
	// LossyOpt 是有损帧的编码选项。/ LossyOpt holds the lossy options.
	LossyOpt *LossyOptions
}

// EncodeAnimation 把 a 编码为动图 WebP 并写入 w。
//
// EncodeAnimation writes a as an animated WebP file.
func EncodeAnimation(w io.Writer, a *Animation, opt *AnimationOptions) error {
	data, err := EncodeAnimationBytes(a, opt)
	if err != nil {
		return err
	}
	_, err = w.Write(data)
	return err
}

// EncodeAnimationBytes 把 a 编码为完整的动图 WebP 文件并保留在内存中返回。
// 每帧独立编码（不做帧间差分），带透明度的帧写出 ALPH 子 chunk，容器顺序为
// VP8X、ANIM、ANMF…；画布尺寸、偶数坐标与帧矩形落在画布内都会在编码前校验。
//
// EncodeAnimationBytes encodes a as an animated WebP file held in memory. Every
// frame is encoded on its own (no inter-frame differencing), a transparent frame
// carries an ALPH sub-chunk, and the container order is VP8X, ANIM, ANMF…; the
// canvas size, the even positions and the frame rectangles are validated before
// encoding.
func EncodeAnimationBytes(a *Animation, opt *AnimationOptions) ([]byte, error) {
	if a == nil {
		return nil, errors.New("webp: nil animation")
	}
	if len(a.Frames) == 0 {
		return nil, errors.New("webp: animation has no frames")
	}
	if a.CanvasWidth <= 0 || a.CanvasHeight <= 0 || a.CanvasWidth > 1<<24 || a.CanvasHeight > 1<<24 {
		return nil, errors.Errorf("webp: canvas %dx%d out of range", a.CanvasWidth, a.CanvasHeight)
	}

	frames := make([][]byte, len(a.Frames))
	hasAlpha := false
	for i, f := range a.Frames {
		payload, kind, alpha, frameAlpha, err := encodeAnimationFrame(f, opt)
		if err != nil {
			return nil, errors.Wrapf(err, "webp: frame %d", i)
		}
		if f.X < 0 || f.Y < 0 || f.X%2 != 0 || f.Y%2 != 0 {
			return nil, errors.Errorf("webp: frame %d position %d,%d must be even and non-negative", i, f.X, f.Y)
		}
		b := f.Image.Bounds()
		fw, fh := b.Dx(), b.Dy()
		if fw <= 0 || fh <= 0 {
			return nil, errors.Errorf("webp: frame %d is empty", i)
		}
		if f.X+fw > a.CanvasWidth || f.Y+fh > a.CanvasHeight {
			return nil, errors.Errorf("webp: frame %d at %d,%d %dx%d does not fit the %dx%d canvas",
				i, f.X, f.Y, fw, fh, a.CanvasWidth, a.CanvasHeight)
		}
		frames[i] = buildANMF(f, kind, fw, fh, payload, alpha)
		hasAlpha = hasAlpha || frameAlpha
	}

	var flags uint8 = FlagAnimation
	if hasAlpha {
		flags |= FlagAlpha
	}
	chunks := make([]riffChunk, 0, 2+len(frames))
	vp8x := make([]byte, 10)
	vp8x[0] = flags
	writeUint24(vp8x[4:7], uint32(a.CanvasWidth-1))
	writeUint24(vp8x[7:10], uint32(a.CanvasHeight-1))
	chunks = append(chunks, riffChunk{name: VP8XChunk, data: vp8x})
	bg := uint32(a.BackgroundColor.A)<<24 | uint32(a.BackgroundColor.R)<<16 |
		uint32(a.BackgroundColor.G)<<8 | uint32(a.BackgroundColor.B)
	anim := make([]byte, 6)
	by.PutUint32(anim[:4], bg)
	by.PutUint16(anim[4:6], a.LoopCount)
	chunks = append(chunks, riffChunk{name: ANIMChunk, data: anim})
	for _, frame := range frames {
		chunks = append(chunks, riffChunk{name: ANMFChunk, data: frame})
	}
	return marshalWebpFile(chunks)
}

// encodeAnimationFrame 编码一帧的图像数据：返回子 chunk 类型码、码流载荷、
// 可选的 ALPH 载荷，以及这一帧是否含透明像素。VP8L 自带 alpha 通道，所以
// 只有有损帧才会附加 ALPH。
//
// encodeAnimationFrame encodes the image data of one frame: it returns the
// sub-chunk type code, the payload, an optional ALPH payload and whether the
// frame has transparent pixels. VP8L carries alpha itself, so only a lossy frame
// gets an ALPH sub-chunk.
func encodeAnimationFrame(f Frame, opt *AnimationOptions) ([]byte, FourCC, []byte, bool, error) {
	if f.Image == nil {
		return nil, "", nil, false, errors.New("webp: nil frame image")
	}
	b := f.Image.Bounds()
	width, height := b.Dx(), b.Dy()
	if width <= 0 || height <= 0 {
		return nil, "", nil, false, errors.New("webp: empty frame image")
	}

	plane, hasAlpha := alphaPlane(f.Image)

	var payload []byte
	var kind FourCC
	var alpha []byte
	var err error
	if opt != nil && opt.Lossy {
		quality := defaultLossyQuality
		if opt.LossyOpt != nil {
			if opt.LossyOpt.QualityZero {
				quality = opt.LossyOpt.Quality
			} else if opt.LossyOpt.Quality != 0 {
				quality = opt.LossyOpt.Quality
			}
		}
		if quality < 0 || quality > 100 {
			return nil, "", nil, false, errors.Errorf("webp: quality %d out of range 0..100", quality)
		}
		payload, err = vp8.Encode(imageToYCbCr420(f.Image), quality)
		if err != nil {
			return nil, "", nil, false, errors.Wrap(err, "webp: vp8 encode")
		}
		kind = VP8Chunk
		if hasAlpha {
			alpha, err = encodeAlphaChunk(plane, width, height, ALPHFilterNone, false)
			if err != nil {
				return nil, "", nil, false, err
			}
		}
	} else {
		method := 2
		if opt != nil && opt.Encode != nil {
			if opt.Encode.Method < 0 || opt.Encode.Method > 2 {
				return nil, "", nil, false, errors.Errorf("webp: unsupported Method %d (supported: 0, 1, 2)", opt.Encode.Method)
			}
			method = opt.Encode.Method
		}
		payload, err = vp8l.EncodeARGB(imageToARGB(f.Image), width, height, &vp8l.Options{Method: method})
		if err != nil {
			return nil, "", nil, false, errors.Wrap(err, "webp: vp8l encode")
		}
		kind = VP8LChunk
	}
	return payload, kind, alpha, hasAlpha, nil
}

// buildANMF 拼出 ANMF 的载荷：16 字节帧头、可选的 ALPH 子 chunk、图像码流。
//
// buildANMF assembles the ANMF payload: the 16-byte frame header, an optional
// ALPH sub-chunk and the image bitstream.
func buildANMF(f Frame, kind FourCC, width, height int, payload, alpha []byte) []byte {
	out := make([]byte, 16, 16+len(payload)+len(alpha)+16)
	writeUint24(out[0:3], uint32(f.X/2))
	writeUint24(out[3:6], uint32(f.Y/2))
	writeUint24(out[6:9], uint32(width-1))
	writeUint24(out[9:12], uint32(height-1))
	writeUint24(out[12:15], uint32(f.Duration))
	var flags uint8
	if !f.Blend {
		flags |= 0x2
	}
	if f.Dispose {
		flags |= 0x1
	}
	out[15] = flags
	if alpha != nil {
		out = append(out, marshalRiffChunk(ALPHChunk, alpha)...)
	}
	return append(out, marshalRiffChunk(kind, payload)...)
}
