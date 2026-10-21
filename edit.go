package simple_webp

import (
	"image"
	"sync"

	"github.com/pkg/errors"
)

// ImageOptions 选择 SetImage 的编码方式。
//
// ImageOptions selects how SetImage encodes the pixels.
type ImageOptions struct {
	// Lossy 为 true 时用 VP8 有损编码，否则用 VP8L 无损编码。
	//
	// Lossy selects the VP8 lossy encoder; false selects VP8L lossless.
	Lossy bool
	// Encode 是无损编码选项，Lossy 为 false 时使用。
	//
	// Encode holds the lossless options, used when Lossy is false.
	Encode *EncodeOptions
	// LossyOpt 是有损编码选项，Lossy 为 true 时使用。
	//
	// LossyOpt holds the lossy options, used when Lossy is true.
	LossyOpt *LossyOptions
}

// NewWebp 创建一个空容器，画布尺寸为 width × height。容器没有任何 chunk，
// 需要先用 SetImage 或 AddFrame 放入图像数据再 WriteTo；写一个空容器会报错。
//
// NewWebp creates an empty container with a width times height canvas. It holds
// no chunk yet, so SetImage or AddFrame has to supply the image data before
// WriteTo; writing an empty container is an error.
func NewWebp(width, height int) *Webp {
	return &Webp{
		Width:        width,
		Height:       height,
		origChunks:   []*riffChunk{},
		origConsumed: []bool{},
		origDeleted:  []bool{},
		chunks:       []*riffChunk{},
	}
}

// SetImage 用 m 替换容器里的图像数据：按 opt 选择有损或无损编码，重编码像素，
// 同步画布尺寸与 VP8X，保留已有的 ICCP/EXIF/XMP 元数据，并丢弃解码缓存。
// 动画容器不能直接替换静态图像，请用 RemoveFrame/AddFrame 编辑帧。
//
// SetImage replaces the image data of the container: it re-encodes the pixels
// with the lossy or lossless encoder selected by opt, syncs the canvas size and
// the VP8X chunk, keeps the ICCP/EXIF/XMP metadata and drops the decode cache.
// An animated container cannot take a still image; edit its frames with
// RemoveFrame/AddFrame instead.
func (w *Webp) SetImage(m image.Image, opt *ImageOptions) error {
	if m == nil {
		return errors.New("webp: nil image")
	}
	if len(w.ANMFs) > 0 {
		return errors.New("webp: cannot set a still image on an animated container")
	}
	b := m.Bounds()
	if b.Dx() <= 0 || b.Dy() <= 0 {
		return errors.New("webp: empty image")
	}

	var data []byte
	var err error
	if opt != nil && opt.Lossy {
		data, err = EncodeLossyBytes(m, opt.LossyOpt)
	} else {
		var lossless *EncodeOptions
		if opt != nil {
			lossless = opt.Encode
		}
		data, err = EncodeBytes(m, lossless)
	}
	if err != nil {
		return err
	}
	src, err := ParseWebpBytes(data)
	if err != nil {
		return errors.WithStack(err)
	}

	w.Lock()
	defer w.Unlock()
	w.Width, w.Height = b.Dx(), b.Dy()
	w.VP8, w.VP8L, w.ALPH = src.VP8, src.VP8L, src.ALPH
	w.HasAlpha = src.HasAlpha
	w.syncChunks()
	w.resetCache()
	return nil
}

// AddFrame 把一帧追加到动画容器末尾：帧坐标必须为偶数、帧矩形必须落在画布内，
// 帧内必须有 VP8 或 VP8L 码流。容器里已有静态图像时返回错误，尚未有 ANIM 时
// 会自动补一个默认的 ANIM。
//
// AddFrame appends a frame to an animated container: the frame position must be
// even, the frame rectangle must fit the canvas and the frame must carry a VP8
// or VP8L bitstream. A container that already holds a still image is an error;
// a missing ANIM chunk is created with default values.
func (w *Webp) AddFrame(f *ANMF) error {
	if f == nil {
		return errors.New("webp: nil frame")
	}
	if w.VP8 != nil || w.VP8L != nil {
		return errors.New("webp: cannot add a frame to a still container")
	}
	if f.VP8 == nil && f.VP8L == nil {
		return errors.New("webp: frame has no image data")
	}
	if f.Width <= 0 || f.Height <= 0 {
		return errors.Errorf("webp: frame size %dx%d out of range", f.Width, f.Height)
	}
	if f.X < 0 || f.Y < 0 || f.X%2 != 0 || f.Y%2 != 0 {
		return errors.Errorf("webp: frame position %d,%d must be even and non-negative", f.X, f.Y)
	}
	if w.Width > 0 && w.Height > 0 && (f.X+f.Width > w.Width || f.Y+f.Height > w.Height) {
		return errors.Errorf("webp: frame %d,%d %dx%d does not fit the %dx%d canvas", f.X, f.Y, f.Width, f.Height, w.Width, w.Height)
	}

	w.Lock()
	defer w.Unlock()
	if w.ANIM == nil {
		w.ANIM = &ANIM{}
	}
	w.ANMFs = append(w.ANMFs, f)
	w.HasAnimation = true
	w.syncChunks()
	w.resetCache()
	return nil
}

// RemoveFrame 删除第 i 帧（0 起）；删掉最后一帧时同时清掉 ANIM 与 animation
// 标志位，输出会退化成没有图像数据的容器，因此需要再 AddFrame 或 SetImage。
//
// RemoveFrame drops frame i (zero based); removing the last frame also clears the
// ANIM chunk and the animation flag, leaving a container without image data, so
// a later AddFrame or SetImage has to supply some.
func (w *Webp) RemoveFrame(i int) error {
	if i < 0 || i >= len(w.ANMFs) {
		return errors.Errorf("webp: frame %d out of range (%d frames)", i, len(w.ANMFs))
	}
	w.Lock()
	defer w.Unlock()
	w.ANMFs = append(w.ANMFs[:i], w.ANMFs[i+1:]...)
	if len(w.ANMFs) == 0 {
		w.ANIM = nil
		w.HasAnimation = false
	}
	w.syncChunks()
	w.resetCache()
	return nil
}

// InvalidateCache 丢弃 Image/At 的解码缓存；直接修改像素结构体后调用它，
// 下一次 Image/At 会重新解码。
//
// InvalidateCache drops the decode cache of Image/At; call it after mutating the
// pixel structs directly so the next Image/At decodes again.
func (w *Webp) InvalidateCache() {
	w.Lock()
	defer w.Unlock()
	w.resetCache()
}

// resetCache 重置解码缓存，调用方需持有写锁。
//
// resetCache resets the decode cache; the caller holds the write lock.
func (w *Webp) resetCache() {
	w.Once = sync.Once{}
	w.img = nil
	w.imgErr = nil
}

// syncChunks 依据当前结构体状态重建 origChunks：所有 chunk 都按 chunkInsertRank
// 的顺序重新排列并标记为已消费，WriteTo 于是完全从结构体重新序列化。
//
// syncChunks rebuilds origChunks from the current struct state: every chunk is
// ordered by chunkInsertRank and marked consumed, so WriteTo re-serializes the
// whole stream from the structs.
func (w *Webp) syncChunks() {
	w.origChunks = nil
	w.origConsumed = nil
	w.origDeleted = nil
	add := func(name FourCC) {
		w.origChunks = append(w.origChunks, &riffChunk{name: name})
		w.origConsumed = append(w.origConsumed, true)
		w.origDeleted = append(w.origDeleted, false)
	}

	var flags uint8
	if w.ICCP != nil {
		flags |= FlagICC
	}
	if w.EXIF != nil {
		flags |= FlagEXIF
	}
	if w.XMP != nil {
		flags |= FlagXMP
	}
	if w.ALPH != nil || (w.VP8L != nil && w.VP8L.AlphaUsed) {
		flags |= FlagAlpha
	}
	if w.ANIM != nil || len(w.ANMFs) > 0 {
		flags |= FlagAnimation
	}
	w.HasAnimation = len(w.ANMFs) > 0 || w.ANIM != nil
	if flags != 0 || w.VP8X != nil {
		w.VP8X = &VP8X{Flags: flags, CanvasWidth: w.Width, CanvasHeight: w.Height}
		add(VP8XChunk)
	}
	if w.ICCP != nil {
		add(ICCPChunk)
	}
	if w.ANIM != nil {
		add(ANIMChunk)
	}
	for range w.ANMFs {
		add(ANMFChunk)
	}
	if w.ALPH != nil {
		add(ALPHChunk)
	}
	if w.VP8L != nil {
		add(VP8LChunk)
	}
	if w.VP8 != nil {
		add(VP8Chunk)
	}
	if w.EXIF != nil {
		add(EXIFChunk)
	}
	if w.XMP != nil {
		add(XMPChunk)
	}
	w.chunks = nil
}
