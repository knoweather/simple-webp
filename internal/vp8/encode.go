package vp8

import (
	"image"
	"math"

	"github.com/pkg/errors"
)

// 帧内编码器：亮度可以用整块 16x16 模式，也可以用十六个 4x4 模式；色度用
// 8x8 模式。量化索引由质量按 libwebp 的曲线推出，帧头同时写出环路滤波参数
// （强度为 0 时解码端跳过滤波）；打开分段后每个宏块按活动度取所在段的量化
// 偏移。模式由率失真准则选出：每个候选模式都要走一遍预测、变换与量化，再用
// 「重建失真 + 系数 × 比特数」比较。
//
// The intra frame encoder: luma uses either one 16x16 mode or sixteen 4x4
// modes, chroma uses 8x8 modes. The quantiser index comes from the quality
// through libwebp's curve, and the frame header carries the loop filter
// parameters (a level of zero lets the decoder skip filtering); with
// segmentation on, every macroblock takes the quantiser offset of its
// activity segment. The modes are picked by a rate distortion criterion: every
// candidate mode goes through prediction, transform and quantization, and the
// candidates are compared on "reconstruction distortion plus lambda times the
// number of bits".
//
// 重建直接复用解码器：预测与逆变换跑的是同一份代码，所以编码端维护的参考
// 像素与解码端逐位一致，后续宏块的预测不会漂移。
//
// Reconstruction reuses the decoder: prediction and the inverse transforms run
// the same code, so the reference pixels kept by the encoder are bit identical
// to the decoder's and later macroblocks never drift.
type encoder struct {
	d                  decoder
	srcY, srcCb, srcCr *plane
	hdr                boolEncoder
	tok                boolEncoder
	// cost 只用来累计比特数，不产出字节：率失真决策要比较候选模式的码长。
	//
	// cost only tallies the number of bits and produces no bytes: the rate
	// distortion decisions compare the length of the candidate modes.
	cost boolEncoder
	// levels 是当前宏块的量化系数，candLevels 是评估候选模式时的暂存。
	//
	// levels holds the quantized coefficients of the current macroblock and
	// candLevels the scratch space for a candidate mode.
	levels     [400]int32
	candLevels [400]int32
	// lastNZ 记录每个 4x4 块的最后一个非零系数在扫描顺序里的位置加一（全零
	// 记 0），由量化循环顺手填好。下标是系数基址除以十六：0..15 是十六个亮度
	// 块，16..23 是色度的 Cb/Cr 各四块，24 是 Y2。
	//
	// 写出与估算 token 时都要知道 EOB 落在哪，原先每个块都重扫一遍十六个位置；
	// 量化本来就要遍历这些系数，顺手记下即可。
	//
	// lastNZ records one past the scan position of the last non-zero coefficient
	// of every 4x4 block (zero when the block is empty), filled in by the
	// quantisation loop. The index is the coefficient base divided by sixteen:
	// 0..15 are the sixteen luma blocks, 16..23 the four Cb and four Cr blocks,
	// and 24 is Y2.
	//
	// Writing and estimating tokens both need to know where the EOB goes, which
	// used to cost a sixteen position scan per block; quantisation already
	// walks those coefficients, so it records the position on the way.
	lastNZ [25]uint8
	// candLastNZ 与 candLevels 配套：评估候选模式时，lastNZ 会被每个候选
	// 覆写，选中的候选若是较早评估的那个，恢复系数时也必须把 lastNZ 一起
	// 恢复，否则 EOB 的位置会按最后一个候选算，码流与系数对不上。
	//
	// candLastNZ goes with candLevels: evaluating candidates overwrites lastNZ,
	// so when the winner was an earlier candidate its lastNZ has to be restored
	// along with the levels, or the EOB position would follow the last
	// candidate and the stream would not match the coefficients.
	candLastNZ [25]uint8
	// predY4 是选中的十六个 4x4 亮度模式。
	//
	// predY4 holds the sixteen chosen 4x4 luma modes.
	predY4 [4][4]uint8
	qi     int
	// filterLevel、sharpness 与 simpleFilter 决定帧头里的环路滤波参数；强度为
	// 0 时解码端完全跳过滤波。
	//
	// filterLevel, sharpness and simpleFilter pick the loop filter parameters in
	// the frame header; a zero level lets the decoder skip filtering entirely.
	filterLevel  int
	sharpness    int
	simpleFilter bool
	// y4Blocks 统计走了 4x4 模式那条路的宏块数，供测试确认这条路真的被走到。
	//
	// y4Blocks counts the macroblocks that took the 4x4 mode path, so the tests
	// can confirm the path is really taken.
	y4Blocks int
	// decisions 缓存每个宏块选中的模式，第二遍编码直接照着写。
	//
	// decisions caches the modes chosen for every macroblock so the second
	// pass writes them as they are.
	decisions []mbDecision
	// replay 为真时不再搜索模式，直接用 decisions 里记录的。
	//
	// replay skips the mode search and uses the recorded decisions.
	replay bool
	// counts 记录每个 token 概率条目上取 0 与取 1 的次数，用于把默认概率表
	// 调整成贴合本帧统计的表。
	//
	// counts tallies the zero and one decisions at every token probability
	// entry, so the default table can be adapted to this frame's statistics.
	counts [nPlane][nBand][nContext][nProb][2]uint32
	// seg 是逐段量化方案；enabled 为 false 时帧头不写分段，全部宏块用段 0。
	//
	// seg is the per-segment quantization plan; when enabled is false the
	// frame header carries no segmentation and every macroblock uses segment 0.
	seg segPlan
	// collect 为真时统计 token 的取值分布；estimating 为真时写出的比特只
	// 计入代价而不落到码流里。
	//
	// collect tallies the token decisions and estimating routes the bits into
	// the cost accumulator instead of the stream.
	collect    bool
	estimating bool
	// rdScale 是率失真权衡系数的倍率。
	//
	// rdScale scales the rate distortion trade-off factor.
	rdScale float64
}

// mbDecision 记录一个宏块的模式决策：亮度走的是整块 16x16 还是十六个 4x4
// （走哪条只有对应的那一套模式有意义），外加色度模式。
//
// mbDecision records the mode decision of one macroblock: whether the luma took
// the whole 16x16 route or the sixteen 4x4 blocks (only the matching set of
// modes is meaningful), plus the chroma mode.
type mbDecision struct {
	useY16  bool
	predY16 uint8
	predC8  uint8
	predY4  [4][4]uint8
}

// quantBiasNum 是量化取整偏置的百分数。50 是四舍五入，取 40 会略偏向零：靠近
// 量化间隔的系数更容易被压成零，省下的比特多于它带来的失真。实测同一 PSNR 下
// 体积比四舍五入小百分之六到二十，平滑内容最明显。
//
// quantBiasNum is the rounding bias of the quantizer in percent. 50 is round to
// nearest; 40 leans slightly towards zero, so coefficients close to a
// quantisation step collapse to zero and the bits saved outweigh the extra
// distortion. Measured against round to nearest it is six to twenty percent
// smaller at the same PSNR, most of all on smooth content.
const quantBiasNum = 40

// maxCoeffLevel 是系数幅值的上限：CAT6 覆盖到 67+2047，再大就只能截断。
//
// maxCoeffLevel caps the coefficient magnitude: CAT6 reaches 67+2047, anything
// larger has to be clipped.
const maxCoeffLevel = 2048

// 正变换用的定点系数，与逆变换共用同一组常量，保证正反变换尺度一致。
//
// The fixed-point coefficients of the forward transform, shared with the
// inverse so both sides use the same scale.
const (
	fdctC1 = 85627 // 65536 * cos(pi/8) * sqrt(2)
	fdctC2 = 35468 // 65536 * sin(pi/8) * sqrt(2)
)

// Encode 把 4:2:0 的 YCbCr 图像编码为 VP8 关键帧。quality 取 0（最小体积）到
// 100（最高质量）。
//
// Encode codes a 4:2:0 YCbCr image as a VP8 key frame. quality runs from 0
// (smallest) to 100 (best).
func Encode(src *image.YCbCr, quality int) ([]byte, error) {
	return EncodeWithOptions(src, Options{Quality: quality, FilterLevel: -1})
}

// Options 调节 VP8 编码器。
//
// Options tunes the VP8 encoder.
type Options struct {
	// Quality 取 0（体积最小）到 100（质量最高）。
	//
	// Quality runs from 0 (smallest) to 100 (best).
	Quality int
	// FilterLevel 是环路滤波强度 0..63。0 关闭滤波，-1 按质量自动选择；强度
	// 越大块效应越弱，但同质量下细节也更软。
	//
	// FilterLevel is the loop filter strength, 0..63. Zero disables the filter,
	// -1 derives the strength from the quality; a stronger filter removes more
	// blocking at the cost of softer detail.
	FilterLevel int
	// SimpleFilter 为 true 时用简单滤波（只处理亮度边缘），否则用普通滤波。
	//
	// SimpleFilter selects the simple filter (luma edges only) instead of the
	// normal one.
	SimpleFilter bool
	// Sharpness 是滤波锐度 0..7，越大越保留细节。
	//
	// Sharpness is the filter sharpness, 0..7, where a larger value keeps more
	// detail.
	Sharpness int
	// Segment 打开逐段量化：按宏块活动度把帧分成四段，平坦的宏块用更粗的
	// 量化、繁忙的保持基础量化。开启后编码器会同时试一遍不分段的版本，
	// 只保留更小的那个，因此体积不会大于关闭时。默认关闭。
	//
	// Segment enables per-segment quantization: the frame is split into four
	// segments by macroblock activity, flat macroblocks getting a coarser
	// quantizer while busy ones keep the base. With it on the encoder also
	// tries one unsegmented pass and keeps whichever stream is smaller, so
	// the size never grows over having it off. It is off by default.
	Segment bool
}

// autoFilterLevel 由质量推导环路滤波强度：质量越低，量化越粗、块效应越强，
// 需要的滤波也越强。
//
// autoFilterLevel derives the loop filter strength from the quality: a lower
// quality means coarser quantization, stronger blocking and a stronger filter.
func autoFilterLevel(quality int) int {
	if quality < 0 {
		quality = 0
	}
	if quality > 100 {
		quality = 100
	}
	return (100 - quality) * 63 / 100
}

// EncodeWithOptions 按给定选项把 4:2:0 的 YCbCr 图象编码为一帧 VP8 码流。
//
// EncodeWithOptions encodes a 4:2:0 YCbCr image as one VP8 frame with the given
// options.
func EncodeWithOptions(src *image.YCbCr, opt Options) ([]byte, error) {
	if src.SubsampleRatio != image.YCbCrSubsampleRatio420 {
		return nil, ErrInvalid
	}
	w, h := src.Rect.Dx(), src.Rect.Dy()
	if w <= 0 || h <= 0 || w > 16383 || h > 16383 {
		return nil, ErrInvalid
	}
	e := newEncoder(w, h, src, opt.Quality)
	level := opt.FilterLevel
	if level < 0 {
		level = autoFilterLevel(opt.Quality)
	}
	if level > 63 {
		level = 63
	}
	e.filterLevel = level
	e.simpleFilter = opt.SimpleFilter
	e.sharpness = clipInt(opt.Sharpness, 0, 7)
	if !opt.Segment {
		return e.run()
	}

	// 逐段量化：按活动度制订方案，分别用「不分段」「常规步长」「双倍步长」
	// 三个方案各编码一遍，只保留最小的那个。分段本身有成本（帧头字段加每
	// 宏块段号），而收益只出现在「低幅值纹理」这类内容上——平坦区域多数
	// 直接 skip，粗量化无利可图。方案不划算时竞赛保证体积不升。
	// Per-segment quantization: the plan is derived from the activity and the
	// frame is encoded three times — unsegmented, with the regular step and
	// with double the step — and the smallest stream wins. Segmentation has a
	// cost of its own (the header fields plus a segment ID per macroblock) and
	// the gain only shows on low amplitude texture, because flat regions are
	// mostly skipped outright and gain nothing from a coarser quantizer. The
	// race keeps the size from growing when a plan does not pay off.
	var best []byte
	attempt := func(enc *encoder, plan segPlan) error {
		enc.seg = plan
		if plan.enabled {
			setupSegmentQuant(&enc.d, enc.qi, plan.delta)
		}
		data, err := enc.run()
		if err != nil {
			return err
		}
		if best == nil || len(data) < len(best) {
			best = data
		}
		return nil
	}
	// 第一次尝试直接复用已按选项配置好的 e，省掉一次整帧缓冲分配。
	// The first attempt reuses e, which is already configured from the
	// options, so one full set of frame buffers is not allocated twice.
	if err := attempt(e, segPlan{}); err != nil {
		return nil, err
	}
	base := planSegments(src, e.d.mbw, e.d.mbh, e.qi)
	if base.enabled {
		enc := newEncoder(w, h, src, opt.Quality)
		enc.filterLevel = level
		enc.simpleFilter = opt.SimpleFilter
		enc.sharpness = e.sharpness
		if err := attempt(enc, base); err != nil {
			return nil, err
		}
		// 双倍步长：平坦侧再粗一档，只有不越出索引上限时才参与竞赛。
		// Double the step: one more notch on the flat side, racing only when
		// it stays inside the index range.
		d2 := 2 * int(base.delta[2])
		if d2 > 0 && e.qi+2*d2 <= 127 {
			wide := base
			wide.delta = [nSegment]int8{0, 0, int8(d2), int8(2 * d2)}
			enc := newEncoder(w, h, src, opt.Quality)
			enc.filterLevel = level
			enc.simpleFilter = opt.SimpleFilter
			enc.sharpness = e.sharpness
			if err := attempt(enc, wide); err != nil {
				return nil, err
			}
		}
	}
	return best, nil
}

// EncodeToTarget 在给定选项的基础上做码率控制：在质量 0..100 之间二分，返回
// 体积最接近 targetBytes 的那一次编码结果。码率控制只调全局量化（若选项打开
// 逐段量化，每次尝试内部会自行竞赛）；目标无法精确命中时返回最接近的一次。
//
// EncodeToTarget adds rate control on top of the given options: it bisects the
// quality over 0..100 and returns the encode whose size is closest to
// targetBytes. Only the global quantiser is adjusted (with the segment option
// on, every attempt races its own plans); when the target cannot be hit
// exactly the closest encode is returned.
func EncodeToTarget(src *image.YCbCr, opt Options, targetBytes int) ([]byte, error) {
	if targetBytes <= 0 {
		return nil, ErrInvalid
	}
	bestErr := int(^uint(0) >> 1)
	var bestData []byte
	lo, hi := 0, 100
	for i := 0; i < 8 && lo <= hi; i++ {
		q := (lo + hi) / 2
		o := opt
		o.Quality = q
		data, err := EncodeWithOptions(src, o)
		if err != nil {
			return nil, err
		}
		if diff := absDiff(len(data), targetBytes); diff < bestErr {
			bestErr = diff
			bestData = data
		}
		if len(data) > targetBytes {
			hi = q - 1
		} else {
			lo = q + 1
		}
	}
	if bestData == nil {
		return nil, ErrInvalid
	}
	return bestData, nil
}

// absDiff 返回两个整数之差的绝对值，供码率控制比较体积偏差。
//
// absDiff returns the absolute difference of two integers, used by the rate
// control to compare size deviations.
func absDiff(a, b int) int {
	if d := a - b; d < 0 {
		return -d
	} else {
		return d
	}
}

// quantIndex 把质量映射到量化索引：质量越高索引越小。
//
// 曲线与 libwebp 一致：先把质量归一化到 0..1，再用分段线性映射把「质量」
// 换算成「压缩率」，最后开立方根（体积大致与量化步长的三次方成正比），
// 所以 127*(1-c) 就是量化索引。libwebp 在中质量段把 75 当作「看起来不错」的
// 基准，线性映射在这里会过于粗暴：同为质量 50，线性映射给出 63，libwebp
// 给出 38，量化步长粗了约 2/3。
//
// quantIndex maps quality onto the quantization index: higher quality means a
// smaller index.
//
// The curve matches libwebp: quality is normalised to 0..1, a piecewise linear
// mapping turns it into a "compressibility", and the cube root of that is
// taken (size scales roughly with the cube of the quantiser step), so
// 127*(1-c) is the index. libwebp calibrates the middle of the range so that
// 75 looks good; a plain linear mapping is far too coarse there: at quality 50
// it yields 63 where libwebp yields 38, a step about two thirds coarser.
func quantIndex(quality int) int {
	if quality < 0 {
		quality = 0
	}
	if quality > 100 {
		quality = 100
	}
	q := float64(quality) / 100
	// 分段线性：0.75 以下压缩率变化慢，以上每一点质量都换更多压缩率。
	// Piecewise linear: below 0.75 the compressibility climbs slowly, above it
	// every bit of quality buys more of it.
	linear := q * (2.0 / 3.0)
	if q >= 0.75 {
		linear = 2*q - 1
	}
	c := math.Cbrt(linear)
	qi := int(127 * (1 - c))
	if qi < 0 {
		qi = 0
	} else if qi > 127 {
		qi = 127
	}
	return qi
}

func newEncoder(w, h int, src *image.YCbCr, quality int) *encoder {
	e := &encoder{}
	d := &e.d
	d.width, d.height = w, h
	d.mbw, d.mbh = (w+15)>>4, (h+15)>>4
	d.y = newPlane(d.mbw*16, d.mbh*16, 4)
	d.cb = newPlane(d.mbw*8, d.mbh*8, 0)
	d.cr = newPlane(d.mbw*8, d.mbh*8, 0)
	d.y.initBorders()
	d.cb.initBorders()
	d.cr.initBorders()
	d.upMB = make([]mbState, d.mbw)
	d.tokenProb = defaultTokenProb
	d.useSkipProb = true
	d.skipProb = uniformProb

	e.qi = quantIndex(quality)
	e.rdScale = rdScaleDefault
	setupSegmentQuant(d, e.qi, [nSegment]int8{})

	e.srcY = newPlane(d.mbw*16, d.mbh*16, 4)
	e.srcCb = newPlane(d.mbw*8, d.mbh*8, 0)
	e.srcCr = newPlane(d.mbw*8, d.mbh*8, 0)
	e.loadSource(src)
	e.decisions = make([]mbDecision, d.mbw*d.mbh)

	e.hdr.init()
	e.tok.init()
	e.cost.costMode = true
	return e
}

// loadSource 把源图像拷进按宏块对齐的平面，右侧与下侧用边缘像素补齐。
//
// loadSource copies the source into macroblock aligned planes, replicating the
// edge pixels on the right and at the bottom.
func (e *encoder) loadSource(src *image.YCbCr) {
	d := &e.d
	baseY := src.Rect.Min.Y*src.YStride + src.Rect.Min.X
	for y := 0; y < d.mbh*16; y++ {
		sy := y
		if sy >= d.height {
			sy = d.height - 1
		}
		row := e.srcY.at(0, y)
		srow := baseY + sy*src.YStride
		for x := 0; x < d.mbw*16; x++ {
			sx := x
			if sx >= d.width {
				sx = d.width - 1
			}
			e.srcY.pix[row+x] = src.Y[srow+sx]
		}
	}

	cw, ch := (d.width+1)/2, (d.height+1)/2
	baseC := (src.Rect.Min.Y/2)*src.CStride + src.Rect.Min.X/2
	for _, pl := range [2]struct {
		dst *plane
		src []byte
	}{{e.srcCb, src.Cb}, {e.srcCr, src.Cr}} {
		for y := 0; y < d.mbh*8; y++ {
			sy := y
			if sy >= ch {
				sy = ch - 1
			}
			row := pl.dst.at(0, y)
			srow := baseC + sy*src.CStride
			for x := 0; x < d.mbw*8; x++ {
				sx := x
				if sx >= cw {
					sx = cw - 1
				}
				pl.dst.pix[row+x] = pl.src[srow+sx]
			}
		}
	}
}

// run 编码全部宏块并组装码流：帧头 10 字节、首分区（控制头与宏块模式）、
// 以及唯一一个残差分区。
//
// run encodes every macroblock and assembles the stream: the ten byte frame
// header, the first partition (control header and macroblock modes) and the
// single residual partition. The 19 bit first-partition length of the frame tag
// cannot represent more than 0x7ffff bytes, so an oversized first partition is
// an error instead of a stream whose tag silently wraps into the neighbouring
// fields.
func (e *encoder) run() ([]byte, error) {
	// 第一遍只统计每个 token 概率条目上的 0/1 次数，不产出字节；据此把默认
	// 概率表调整成本帧自己的统计，第二遍用调整后的表真正编码。模式决策与量化
	// 都与概率无关，所以两遍得到完全相同的系数与重建。
	//
	// The first pass only tallies the zero and one decisions at every token
	// probability entry and emits nothing; the default table is then adapted to
	// this frame's own statistics and the second pass encodes with it. Mode
	// decisions and quantisation do not depend on the probabilities, so both
	// passes produce the same coefficients and the same reconstruction.
	e.collect, e.hdr.dry, e.tok.dry = true, true, true
	e.replay = false
	e.encodeAll()
	e.d.tokenProb = e.adaptTokenProb()

	// 第二遍沿用第一遍选好的模式：模式搜索是最贵的一步，没有必要做两遍。
	// The second pass reuses the modes chosen by the first one: the mode search
	// is the most expensive step and there is no need to do it twice.
	e.collect, e.hdr.dry, e.tok.dry = false, false, false
	e.replay = true
	e.hdr.init()
	e.tok.init()
	e.resetReconstruction()
	e.writeHeader()
	e.encodeAll()

	d := &e.d
	hdr := e.hdr.finish()
	if err := checkFirstPartition(len(hdr)); err != nil {
		return nil, err
	}
	tok := e.tok.finish()
	out := make([]byte, 0, 10+len(hdr)+len(tok))
	tag := uint32(1)<<4 | uint32(len(hdr))<<5
	out = append(out, byte(tag), byte(tag>>8), byte(tag>>16), 0x9d, 0x01, 0x2a)
	out = append(out, byte(d.width), byte(d.width>>8), byte(d.height), byte(d.height>>8))
	out = append(out, hdr...)
	out = append(out, tok...)
	return out, nil
}

// maxFirstPartition 是帧标签里首分区长度字段能表示的最大字节数（19 位）。
//
// maxFirstPartition is the largest first-partition length the 19 bit field of
// the frame tag can hold.
const maxFirstPartition = 1<<19 - 1

// checkFirstPartition 报告首分区长度是否超出帧标签的 19 位字段。超限时若
// 照常左移，长度会溢出到标签的相邻字段，产出的码流会被解码器读成别的
// 分区布局，所以这里直接报错。
//
// checkFirstPartition reports whether the first-partition length overflows the
// 19 bit field of the frame tag. Shifting an oversized length would spill into
// the neighbouring tag fields and a decoder would read a different partition
// layout, so it is an error instead.
func checkFirstPartition(n int) error {
	if n > maxFirstPartition {
		return errors.Wrapf(ErrTooLarge, "first partition is %d bytes, the frame tag allows %d", n, maxFirstPartition)
	}
	return nil
}

// encodeAll 按光栅顺序编码全部宏块。
//
// encodeAll codes every macroblock in raster order.
func (e *encoder) encodeAll() {
	d := &e.d
	for mby := 0; mby < d.mbh; mby++ {
		d.leftMB = mbState{}
		for mbx := 0; mbx < d.mbw; mbx++ {
			e.encodeMB(mbx, mby)
		}
		d.y.fillOverhang(mby*16 + 15)
	}
}

// resetReconstruction 把重建平面与宏块状态清空，供第二遍重新编码。
//
// resetReconstruction clears the reconstruction planes and the macroblock state
// so the second pass can start over.
func (e *encoder) resetReconstruction() {
	d := &e.d
	d.y = newPlane(d.mbw*16, d.mbh*16, 4)
	d.cb = newPlane(d.mbw*8, d.mbh*8, 0)
	d.cr = newPlane(d.mbw*8, d.mbh*8, 0)
	d.y.initBorders()
	d.cb.initBorders()
	d.cr.initBorders()
	d.upMB = make([]mbState, d.mbw)
	d.leftMB = mbState{}
	d.segment = 0
}

// adaptTokenProb 依据第一遍的统计把默认概率表调整成本帧的概率表。
//
// adaptTokenProb turns the default table into this frame's table using the first
// pass tallies.
//
// 每个条目取使编码代价最小的概率，但只有省下的位数明显超过传输新值的成本时
// 才真的改写：写出一个概率约需八位数值加几位更新标志。样本太少的条目保留默认。
//
// Each entry takes the probability that codes this frame's decisions cheapest,
// but is only rewritten when the bits it saves clearly outweigh the cost of
// transmitting the new value: about eight bits for the value plus a few for the
// update flag. Entries with too few samples keep their default.
func (e *encoder) adaptTokenProb() [nPlane][nBand][nContext][nProb]uint8 {
	table := defaultTokenProb
	for i := range e.counts {
		for j := range e.counts[i] {
			for k := range e.counts[i][j] {
				for l := range e.counts[i][j][k] {
					c0 := uint64(e.counts[i][j][k][l][0])
					c1 := uint64(e.counts[i][j][k][l][1])
					n := c0 + c1
					if n < minAdaptSamples {
						continue
					}
					p := uint8((c0*255 + n/2) / n)
					if p < 1 {
						p = 1
					}
					def := defaultTokenProb[i][j][k][l]
					if p == def {
						continue
					}
					if tokenCost(c0, c1, def)-tokenCost(c0, c1, p) <= minAdaptGainBits {
						continue
					}
					table[i][j][k][l] = p
				}
			}
		}
	}
	return table
}

// tokenCost 返回用 prob 作零概率编码 c0 个零与 c1 个一时所需的位数。
//
// tokenCost returns the bits needed to code c0 zeros and c1 ones with prob as
// the probability of a zero.
func tokenCost(c0, c1 uint64, prob uint8) float64 {
	p := float64(prob) / 256
	return -float64(c0)*math.Log2(p) - float64(c1)*math.Log2(1-p)
}

// minAdaptSamples 是改写一个概率条目所需的最少样本数，minAdaptGainBits 是
// 所需的最小净收益（比特）。
//
// minAdaptSamples is the least number of samples needed to rewrite a
// probability entry and minAdaptGainBits the least net gain in bits.
const (
	minAdaptSamples  = 16
	minAdaptGainBits = 12.0
)

// writeHeader 写出首分区里的控制头：色彩标志、分段、滤波、分区数、量化、
// token 概率更新与 skip 概率。
//
// writeHeader emits the control header of the first partition: the colour
// flags, segmentation, filtering, partition count, quantization, the token
// probability updates and the skip probability.
func (e *encoder) writeHeader() {
	h := &e.hdr
	// 色彩空间与像素钳位标志：静态图不使用。
	// colour space and pixel clamp flags: unused for still images.
	h.putBit(uniformProb, false)
	h.putBit(uniformProb, false)
	// 分段：按方案写出启用、更新分段图、更新特性数据（相对增量）与逐段
	// 量化偏移；滤波强度不随段变化；segment ID 树概率非默认时才写出。
	// Segmentation: the plan decides the enable and map update bits, the
	// feature data update (relative deltas) and the per-segment quantizer
	// offsets; the filter strength stays the same for every segment and the
	// segment ID tree probabilities are written only when they differ from
	// the default.
	if e.seg.enabled {
		h.putBit(uniformProb, true)  // 启用分段 / segmentation enabled
		h.putBit(uniformProb, true)  // 更新分段图 / the map is updated
		h.putBit(uniformProb, true)  // 更新特性数据 / the features are updated
		h.putBit(uniformProb, false) // 相对增量 / relative deltas
		for i := range e.seg.delta {
			h.putOptionalInt(uniformProb, 7, int32(e.seg.delta[i]))
		}
		for i := 0; i < nSegment; i++ {
			h.putBit(uniformProb, false) // 不更新滤波强度 / no filter update
		}
		for i := range e.seg.prob {
			if e.seg.prob[i] == 0xff {
				h.putBit(uniformProb, false) // 用默认概率 / default probability
				continue
			}
			h.putBit(uniformProb, true)
			h.putUint(uniformProb, 8, uint32(e.seg.prob[i]))
		}
	} else {
		h.putBit(uniformProb, false) // 不使用分段 / no segmentation
	}
	// 滤波：类型、强度、锐度、不做参考帧/模式偏移。
	// filtering: type, level, sharpness, no reference or mode deltas.
	if e.filterLevel > 0 && !e.simpleFilter {
		h.putBit(uniformProb, false) // 普通滤波 / normal filter
	} else {
		h.putBit(uniformProb, e.filterLevel > 0) // 简单滤波 / simple filter
	}
	h.putUint(uniformProb, 6, uint32(e.filterLevel))
	h.putUint(uniformProb, 3, uint32(e.sharpness))
	h.putBit(uniformProb, false)
	// 单个残差分区。
	// a single residual partition.
	h.putUint(uniformProb, 2, 0)
	// 量化：基础索引加五个零偏移。
	// quantization: the base index plus five zero deltas.
	h.putUint(uniformProb, 7, uint32(e.qi))
	for i := 0; i < 5; i++ {
		h.putOptionalInt(uniformProb, 4, 0)
	}
	// 不刷新熵概率（单帧无影响）。
	// no entropy refresh (irrelevant for a single frame).
	h.putBit(uniformProb, false)
	// token 概率更新：与默认表不同的条目才写出新值。
	// token probability updates: only the entries that differ from the default
	// table carry a new value.
	for i := range tokenUpdateProb {
		for j := range tokenUpdateProb[i] {
			for k := range tokenUpdateProb[i][j] {
				for l := range tokenUpdateProb[i][j][k] {
					up := tokenUpdateProb[i][j][k][l]
					p := e.d.tokenProb[i][j][k][l]
					if p == defaultTokenProb[i][j][k][l] {
						h.putBit(up, false)
						continue
					}
					h.putBit(up, true)
					h.putUint(uniformProb, 8, uint32(p))
				}
			}
		}
	}
	h.putBit(uniformProb, true)
	h.putUint(uniformProb, 8, uint32(e.d.skipProb))
}

// encodeMB 编码一个宏块：按率失真准则选模式、变换量化、写模式位与残差
// token、再重建。
//
// encodeMB codes one macroblock: it picks the modes by the rate distortion
// criterion, transforms and quantizes, writes the mode bits and the residual
// tokens, then reconstructs.
func (e *encoder) encodeMB(mbx, mby int) {
	d := &e.d
	bx, by := mbx*16, mby*16
	cx, cy := mbx*8, mby*8

	// 当前宏块落在哪一段由静态方案决定，两遍编码一致；后续的量化表查找
	// 都按 d.segment 取该段的。
	// The segment of the macroblock comes from the static plan and is the
	// same in both passes; every quantizer lookup below takes the tables of
	// d.segment.
	if e.seg.enabled {
		d.segment = int(e.seg.segOfMB[mby*d.mbw+mbx])
	}

	// 宏块最右 4x4 块的右上预测像素取自宏块上方那一行，必须在预测之前取好。
	// The top right pixels of the rightmost 4x4 block come from the row above
	// the macroblock, so they have to be fetched before the prediction.
	d.loadTopRight(mbx, mby)

	if e.replay {
		// 第二遍沿用第一遍的决策，不再搜索模式。
		// The second pass reuses the decisions of the first pass and searches
		// no modes again.
		dec := &e.decisions[mby*d.mbw+mbx]
		d.usePredY16 = dec.useY16
		d.predY16 = dec.predY16
		d.predC8 = dec.predC8
		d.predY4 = dec.predY4
	} else {
		// 色度模式与亮度模式互不依赖，先定色度。
		// The chroma mode does not depend on the luma mode, so it comes first.
		bestC := e.chooseChromaMode(mbx, mby, cx, cy)
		// 亮度：整块 16x16 与十六个 4x4 两条路里选代价更低的一条。
		// Luma: pick the cheaper of one 16x16 mode and sixteen 4x4 modes.
		useY16, bestY := e.chooseLumaMode(mbx, mby, bx, by)
		d.usePredY16 = useY16
		d.predY16 = bestY
		d.predC8 = bestC
		d.predY4 = e.predY4
		e.decisions[mby*d.mbw+mbx] = mbDecision{
			useY16:  useY16,
			predY16: bestY,
			predC8:  bestC,
			predY4:  e.predY4,
		}
	}

	// 用选中的模式算出系数并重建像素。
	// Compute the coefficients with the chosen modes and reconstruct.
	e.buildMacroblock(mbx, mby, bx, by, cx, cy)

	skip := e.levelsAreZero()

	// 段号位写在 skip 位之前，与解码器 reconstruct 的读取顺序一致。
	// The segment ID bits precede the skip bit, the order the decoder's
	// reconstruct reads them in.
	if e.seg.enabled {
		e.writeSegmentID(int(e.seg.segOfMB[mby*d.mbw+mbx]))
	}
	if d.useSkipProb {
		e.hdr.putBit(d.skipProb, skip)
	}
	if d.usePredY16 {
		e.hdr.putBit(145, true)
		writeY16Mode(&e.hdr, d.predY16)
		for i := 0; i < 4; i++ {
			d.upMB[mbx].pred[i] = d.predY16
			d.leftMB.pred[i] = d.predY16
		}
	} else {
		e.hdr.putBit(145, false)
		e.writeY4Modes(mbx)
	}
	writeChromaMode(&e.hdr, d.predC8)

	if !skip {
		e.writeTokens(mbx)
	} else {
		if d.usePredY16 {
			d.leftMB.nzY16 = 0
			d.upMB[mbx].nzY16 = 0
		}
		d.leftMB.nzMask = 0
		d.upMB[mbx].nzMask = 0
	}
}

// buildMacroblock 用选中的模式算出宏块的残差系数并重建像素：亮度按整块
// 16x16 或十六个 4x4 块，色度按 8x8。
//
// buildMacroblock computes the residual coefficients of a macroblock with the
// chosen modes and reconstructs the pixels: the luma either as one 16x16 block
// or as sixteen 4x4 blocks, the chroma as 8x8 blocks.
func (e *encoder) buildMacroblock(mbx, mby, bx, by, cx, cy int) {
	d := &e.d
	if d.usePredY16 {
		d.predict16(checkTopLeftPred(mbx, mby, d.predY16), bx, by)
		e.transformLuma16(bx, by)
		e.loadCoeffsLuma16()
		e.setMasksLuma16()
		e.reconstructLuma16(mbx, mby)
	} else {
		// 4x4 模式下没有 Y2 块，那一段系数必须清零，否则 skip 判断会看错。
		// The 4x4 path has no Y2 block, so those coefficients have to be cleared
		// or the skip test would look at stale values.
		for i := 384; i < 400; i++ {
			e.levels[i] = 0
		}
		for j := 0; j < 4; j++ {
			for i := 0; i < 4; i++ {
				n := 4*j + i
				x, y := bx+i*4, by+j*4
				d.predict4(d.predY4[j][i], x, y)
				e.transformLuma4(n, x, y)
				e.loadCoeffsLuma4(n)
				e.setMasksLuma4(n)
				d.addResidual4(x, y, n)
			}
		}
	}
	cm := checkTopLeftPred(mbx, mby, d.predC8)
	d.predict8(d.cb, cm, cx, cy)
	d.predict8(d.cr, cm, cx, cy)
	e.transformChroma(cx, cy)
	e.loadCoeffsChroma()
	e.setMasksChroma()
	e.reconstructChroma(mbx, mby)
}

// levelsAreZero 报告当前宏块的系数是否全为零，也就是可以只做预测。
//
// levelsAreZero reports whether every coefficient of the macroblock was
// quantized to zero, meaning the macroblock needs prediction only.
func (e *encoder) levelsAreZero() bool {
	for _, v := range e.levels {
		if v != 0 {
			return false
		}
	}
	return true
}

// transformLuma16 对 16x16 亮度块做残差、正变换与量化，结果留在 e.levels 中：
// 16 个 4x4 块只保留 AC，DC 汇总进 Y2 做 Walsh-Hadamard。
//
// transformLuma16 computes the residual, the forward transforms and the
// quantization of a 16x16 luma block into e.levels: the sixteen 4x4 blocks keep
// their AC only, the DC values feed the Walsh-Hadamard transform of the Y2
// block.
func (e *encoder) transformLuma16(bx, by int) {
	d := &e.d
	q := d.q[d.segment]
	acq, acBias := int32(q.y1[1]), quantBias(int32(q.y1[1]))
	var dc [16]int32
	for n := 0; n < 16; n++ {
		x, y := bx+(n%4)*4, by+(n/4)*4
		var blk [16]int32
		e.residual(d.y, e.srcY, x, y, &blk)
		fdct4x4(&blk)
		dc[n] = blk[0]
		e.levels[16*n] = 0
		last := 0
		for z := 1; z < 16; z++ {
			lv := quantize(blk[z], acq, acBias)
			e.levels[16*n+z] = lv
			if lv != 0 {
				if p := int(zigzagPos[z]) + 1; p > last {
					last = p
				}
			}
		}
		e.lastNZ[n] = uint8(last)
	}
	fwht4x4(&dc)
	y2dc, y2ac := int32(q.y2[0]), int32(q.y2[1])
	dcBias, acBias2 := quantBias(y2dc), quantBias(y2ac)
	last := 0
	for i := 0; i < 16; i++ {
		qq, bias := y2ac, acBias2
		if i == 0 {
			qq, bias = y2dc, dcBias
		}
		lv := quantize(dc[i], qq, bias)
		e.levels[384+i] = lv
		if lv != 0 {
			if p := int(zigzagPos[i]) + 1; p > last {
				last = p
			}
		}
	}
	e.lastNZ[24] = uint8(last)
}

// transformLuma4 对一个 4x4 亮度块做残差、正变换与量化，结果留在 e.levels
// 的第 n 块里：DC 与 AC 都留在块内，不经过 Y2。
//
// transformLuma4 computes the residual, the forward transform and the
// quantization of one 4x4 luma block into the n-th block of e.levels: the DC
// and the AC stay in the block, no Y2 is involved.
func (e *encoder) transformLuma4(n, x, y int) {
	d := &e.d
	q := d.q[d.segment]
	var blk [16]int32
	e.residual(d.y, e.srcY, x, y, &blk)
	fdct4x4(&blk)
	dcq, acq := int32(q.y1[0]), int32(q.y1[1])
	base := 16 * n
	last := 0
	if lv := quantize(blk[0], dcq, quantBias(dcq)); lv != 0 {
		e.levels[base] = lv
		last = int(zigzagPos[0]) + 1
	} else {
		e.levels[base] = 0
	}
	acBias := quantBias(acq)
	for z := 1; z < 16; z++ {
		lv := quantize(blk[z], acq, acBias)
		e.levels[base+z] = lv
		if lv != 0 {
			if p := int(zigzagPos[z]) + 1; p > last {
				last = p
			}
		}
	}
	e.lastNZ[base/16] = uint8(last)
}

// transformChroma 对色度做残差、正变换与量化：Cb 的四个块在前，Cr 的四个块
// 在后，全部系数都编码。
//
// transformChroma computes the residual, the forward transforms and the
// quantization of the chroma: the four Cb blocks come first, then the four Cr
// blocks, and every coefficient is coded.
func (e *encoder) transformChroma(cx, cy int) {
	d := &e.d
	q := d.q[d.segment]
	dcq, acq := int32(q.uv[0]), int32(q.uv[1])
	dcBias, acBias := quantBias(dcq), quantBias(acq)
	for c := 0; c < 4; c++ {
		x, y := cx+(c%2)*4, cy+(c/2)*4
		var blk [16]int32
		e.residual(d.cb, e.srcCb, x, y, &blk)
		fdct4x4(&blk)
		e.quantBlock(&blk, 256+16*c, dcq, dcBias, acq, acBias)
		e.residual(d.cr, e.srcCr, x, y, &blk)
		fdct4x4(&blk)
		e.quantBlock(&blk, 320+16*c, dcq, dcBias, acq, acBias)
	}
}

// quantBlock 把变换后的 4x4 系数量化进 e.levels 的第 base 个位置，同时记下
// 最后一个非零系数的扫描位置，省掉写出 token 时再扫一遍十六个位置。
//
// quantBlock quantizes the transformed 4x4 coefficients into e.levels at base
// and records where the last non-zero coefficient sits in scan order, which
// saves the token writer a sixteen position scan.
func (e *encoder) quantBlock(blk *[16]int32, base int, dcq, dcBias, acq, acBias int32) {
	last := 0
	if lv := quantize(blk[0], dcq, dcBias); lv != 0 {
		e.levels[base] = lv
		last = int(zigzagPos[0]) + 1
	} else {
		e.levels[base] = 0
	}
	for z := 1; z < 16; z++ {
		lv := quantize(blk[z], acq, acBias)
		e.levels[base+z] = lv
		if lv != 0 {
			if p := int(zigzagPos[z]) + 1; p > last {
				last = p
			}
		}
	}
	e.lastNZ[base/16] = uint8(last)
}

// residual 取预测平面与源平面之差。
//
// residual takes the difference between the prediction and the source plane.
func (e *encoder) residual(pred, src *plane, x, y int, out *[16]int32) {
	pr := pred.at(x, y)
	sr := src.at(x, y)
	for j := 0; j < 4; j++ {
		for i := 0; i < 4; i++ {
			out[4*j+i] = int32(int(src.pix[sr+i]) - int(pred.pix[pr+i]))
		}
		pr += pred.stride
		sr += src.stride
	}
}

// loadCoeffsLuma16 把 Y2 与亮度块的系数反量化进解码器的系数缓冲，Y2 走逆 WHT
// 分散到 16 个亮度块。
//
// loadCoeffsLuma16 dequantizes the Y2 and luma levels into the decoder's
// coefficient buffer; the Y2 block goes through the inverse Walsh-Hadamard
// transform, which scatters the DC values into the sixteen luma blocks.
func (e *encoder) loadCoeffsLuma16() {
	d := &e.d
	q := d.q[d.segment]
	for i := 0; i < 16; i++ {
		qq := q.y2[1]
		if i == 0 {
			qq = q.y2[0]
		}
		d.coeff[384+i] = int16(e.levels[384+i] * int32(qq))
	}
	d.inverseWHT()
	for n := 0; n < 16; n++ {
		for z := 1; z < 16; z++ {
			d.coeff[16*n+z] = int16(e.levels[16*n+z] * int32(q.y1[1]))
		}
	}
}

// loadCoeffsLuma4 把第 n 个 4x4 亮度块的系数反量化进解码器的系数缓冲。
//
// loadCoeffsLuma4 dequantizes the levels of the n-th 4x4 luma block into the
// decoder's coefficient buffer.
func (e *encoder) loadCoeffsLuma4(n int) {
	d := &e.d
	q := d.q[d.segment]
	for z := 0; z < 16; z++ {
		qq := q.y1[1]
		if z == 0 {
			qq = q.y1[0]
		}
		d.coeff[16*n+z] = int16(e.levels[16*n+z] * int32(qq))
	}
}

// loadCoeffsChroma 把色度系数反量化进解码器的系数缓冲。
//
// loadCoeffsChroma dequantizes the chroma levels into the decoder's coefficient
// buffer.
func (e *encoder) loadCoeffsChroma() {
	d := &e.d
	q := d.q[d.segment]
	for c := 0; c < 4; c++ {
		for z := 0; z < 16; z++ {
			qq := q.uv[1]
			if z == 0 {
				qq = q.uv[0]
			}
			d.coeff[256+16*c+z] = int16(e.levels[256+16*c+z] * int32(qq))
			d.coeff[320+16*c+z] = int16(e.levels[320+16*c+z] * int32(qq))
		}
	}
}

// setMasksLuma16 算出 16 个亮度块的 DC 与 AC 非零掩码。
//
// setMasksLuma16 derives the DC and AC non-zero masks of the sixteen luma
// blocks.
func (e *encoder) setMasksLuma16() {
	d := &e.d
	var dcMask, acMask uint32
	for n := 0; n < 16; n++ {
		if d.coeff[16*n] != 0 {
			dcMask |= 1 << uint(n)
		}
		for z := 1; z < 16; z++ {
			if d.coeff[16*n+z] != 0 {
				acMask |= 1 << uint(n)
				break
			}
		}
	}
	d.nzDCMask = (d.nzDCMask &^ 0xffff) | dcMask
	d.nzACMask = (d.nzACMask &^ 0xffff) | acMask
}

// setMasksLuma4 算出第 n 个 4x4 亮度块的非零掩码。
//
// setMasksLuma4 derives the non-zero masks of the n-th 4x4 luma block.
func (e *encoder) setMasksLuma4(n int) {
	d := &e.d
	bit := uint32(1) << uint(n)
	dcMask, acMask := d.nzDCMask&^bit, d.nzACMask&^bit
	if d.coeff[16*n] != 0 {
		dcMask |= bit
	}
	for z := 1; z < 16; z++ {
		if d.coeff[16*n+z] != 0 {
			acMask |= bit
			break
		}
	}
	d.nzDCMask, d.nzACMask = dcMask, acMask
}

// setMasksChroma 算出八个色度块的非零掩码。
//
// setMasksChroma derives the non-zero masks of the eight chroma blocks.
func (e *encoder) setMasksChroma() {
	d := &e.d
	var dcMask, acMask uint32
	for c := 0; c < 4; c++ {
		if d.coeff[256+16*c] != 0 {
			dcMask |= 1 << uint(16+c)
		}
		if d.coeff[320+16*c] != 0 {
			dcMask |= 1 << uint(20+c)
		}
		for z := 1; z < 16; z++ {
			if d.coeff[256+16*c+z] != 0 {
				acMask |= 1 << uint(16+c)
				break
			}
		}
		for z := 1; z < 16; z++ {
			if d.coeff[320+16*c+z] != 0 {
				acMask |= 1 << uint(20+c)
				break
			}
		}
	}
	d.nzDCMask = (d.nzDCMask &^ 0x0f0000) | dcMask
	d.nzACMask = (d.nzACMask &^ 0x0f0000) | acMask
}

// writeTokens 按解码器解析的顺序写出系数 token：亮度与色度分开写，因为两者
// 的上下文互不依赖。
//
// writeTokens emits the coefficient tokens in the order the decoder parses them:
// the luma and the chroma are written separately because their contexts do not
// depend on each other.
func (e *encoder) writeTokens(mbx int) {
	e.writeTokensLuma(mbx)
	e.writeTokensChroma(mbx)
}

// writeTokensLuma 写出亮度系数：16x16 模式下先写 Y2 块再写十六个亮度块，
// 4x4 模式下只写十六个亮度块且各带自己的 DC。上下文与解码器一样取自上方与
// 左侧宏块的非零标记。
//
// writeTokensLuma emits the luma coefficients: with a 16x16 mode the Y2 block
// comes first and the sixteen luma blocks follow, with 4x4 modes only the
// sixteen luma blocks are written and each carries its own DC. The contexts
// come from the above and left non-zero flags, exactly as the decoder derives
// them.
func (e *encoder) writeTokensLuma(mbx int) {
	d := &e.d
	coeffBase := 0
	plane := planeY1SansY2
	skipDC := false
	if d.usePredY16 {
		nz := e.writeBlock4(planeY2, int(d.leftMB.nzY16+d.upMB[mbx].nzY16), false, 384)
		d.leftMB.nzY16 = nz
		d.upMB[mbx].nzY16 = nz
		plane = planeY1WithY2
		skipDC = true
	}
	lnz := unpackMask[d.leftMB.nzMask&0x0f]
	unz := unpackMask[d.upMB[mbx].nzMask&0x0f]
	for y := 0; y < 4; y++ {
		nz := lnz[y]
		for x := 0; x < 4; x++ {
			nz = e.writeBlock4(plane, int(nz+unz[x]), skipDC, coeffBase)
			unz[x] = nz
			coeffBase += 16
		}
		lnz[y] = nz
	}
	d.leftMB.nzMask = uint8(packMask(lnz, 0)) | (d.leftMB.nzMask & 0xf0)
	d.upMB[mbx].nzMask = uint8(packMask(unz, 0)) | (d.upMB[mbx].nzMask & 0xf0)
}

// writeTokensChroma 写出色度系数：Cb 的四个块在前，Cr 的四个块在后。
//
// writeTokensChroma emits the chroma coefficients: the four Cb blocks come
// first, then the four Cr blocks.
func (e *encoder) writeTokensChroma(mbx int) {
	d := &e.d
	coeffBase := 256
	lnz := unpackMask[d.leftMB.nzMask>>4]
	unz := unpackMask[d.upMB[mbx].nzMask>>4]
	for c := 0; c < 4; c += 2 {
		for y := 0; y < 2; y++ {
			nz := lnz[y+c]
			for x := 0; x < 2; x++ {
				nz = e.writeBlock4(planeUV, int(nz+unz[x+c]), false, coeffBase)
				unz[x+c] = nz
				coeffBase += 16
			}
			lnz[y+c] = nz
		}
	}
	d.leftMB.nzMask = (d.leftMB.nzMask & 0x0f) | uint8(packMask(lnz, 4))
	d.upMB[mbx].nzMask = (d.upMB[mbx].nzMask & 0x0f) | uint8(packMask(unz, 4))
}

// writeBlock4 写出一个 4x4 块的系数 token，返回该块是否有非零系数。skipDC
// 为真时跳过 DC（Y16 宏块的亮度块，DC 已由 Y2 承载）。
//
// writeBlock4 emits the coefficient tokens of one 4x4 block and reports whether
// it has a non-zero coefficient. With skipDC the DC is skipped, as for the luma
// blocks of a Y16 macroblock whose DC lives in the Y2 block.
func (e *encoder) writeBlock4(plane, ctx int, skipDC bool, base int) uint8 {
	// 模式搜索的估算走专用路径：同样的 token 树，但只累加比特数。把它和
	// 真正的写出分开，估算循环里就不用每个 token 都判断一次估算标志、
	// 取一次 sink 指针。
	// The mode search estimates through a dedicated path: the same token tree,
	// but only the bit count is accumulated. Splitting it from the real write
	// keeps the estimation loop free of a flag test and a sink fetch per token.
	if e.estimating {
		return e.estimateBlock4(plane, ctx, skipDC, base)
	}
	n := 0
	if skipDC {
		n = 1
	}
	pBand, pCtx := int(coeffBands[n]), ctx

	// 最后一个非零系数的扫描位置由量化时记好，这里只做下限修正：跳过 DC 时
	// 至少要越过第一个位置。
	// The scan position of the last non-zero coefficient was recorded during
	// quantisation; only the lower bound is fixed up here, since skipping the
	// DC has to step past the first position.
	last := int(e.lastNZ[base/16])
	if last < n {
		last = n
	}
	if last == n {
		e.putToken(plane, pBand, pCtx, 0, false)
		return 0
	}
	e.putToken(plane, pBand, pCtx, 0, true)

	for n < 16 {
		n++
		v := e.levels[base+int(zigzag[n-1])]
		if v == 0 {
			e.putToken(plane, pBand, pCtx, 1, false)
			pBand, pCtx = int(coeffBands[n]), 0
			continue
		}
		e.putToken(plane, pBand, pCtx, 1, true)
		mag := v
		if mag < 0 {
			mag = -mag
		}
		ctx2 := 2
		switch {
		case mag == 1:
			e.putToken(plane, pBand, pCtx, 2, false)
			ctx2 = 1
		case mag == 2:
			e.putToken(plane, pBand, pCtx, 2, true)
			e.putToken(plane, pBand, pCtx, 3, false)
			e.putToken(plane, pBand, pCtx, 4, false)
		case mag <= 4:
			e.putToken(plane, pBand, pCtx, 2, true)
			e.putToken(plane, pBand, pCtx, 3, false)
			e.putToken(plane, pBand, pCtx, 4, true)
			e.putToken(plane, pBand, pCtx, 5, mag-3 == 1)
		case mag <= 6:
			e.putToken(plane, pBand, pCtx, 2, true)
			e.putToken(plane, pBand, pCtx, 3, true)
			e.putToken(plane, pBand, pCtx, 6, false)
			e.putToken(plane, pBand, pCtx, 7, false)
			e.sink().putBit(159, mag-5 == 1)
		case mag <= 10:
			e.putToken(plane, pBand, pCtx, 2, true)
			e.putToken(plane, pBand, pCtx, 3, true)
			e.putToken(plane, pBand, pCtx, 6, false)
			e.putToken(plane, pBand, pCtx, 7, true)
			e.sink().putBit(165, (mag-7)>>1 == 1)
			e.sink().putBit(145, (mag-7)&1 == 1)
		default:
			cat, baseMag, nbits := 0, int32(11), 3
			switch {
			case mag <= 18:
			case mag <= 34:
				cat, baseMag, nbits = 1, 19, 4
			case mag <= 66:
				cat, baseMag, nbits = 2, 35, 5
			default:
				cat, baseMag, nbits = 3, 67, 11
			}
			e.putToken(plane, pBand, pCtx, 2, true)
			e.putToken(plane, pBand, pCtx, 3, true)
			e.putToken(plane, pBand, pCtx, 6, true)
			b1 := cat >> 1
			e.putToken(plane, pBand, pCtx, 8, b1 == 1)
			e.putToken(plane, pBand, pCtx, 9+b1, cat&1 == 1)
			extra := uint32(mag - baseMag)
			for i := 0; i < nbits; i++ {
				e.sink().putBit(cat3456[cat][i], extra>>uint(nbits-1-i)&1 != 0)
			}
		}
		pBand, pCtx = int(coeffBands[n]), ctx2
		e.sink().putBit(uniformProb, v < 0)

		if n == 16 {
			break
		}
		more := n < last
		e.putToken(plane, pBand, pCtx, 0, more)
		if !more {
			break
		}
	}
	return 1
}

// costBit 返回以概率 prob 编码一个判决所需的比特数。
//
// costBit returns the bits needed to code one decision at probability prob.
func costBit(prob uint8, bit bool) float64 {
	if bit {
		return bitCostTable[1][prob]
	}
	return bitCostTable[0][prob]
}

// estimateBlock4 是 writeBlock4 的估算版本：走同一棵 token 树，只把比特数
// 累加到局部变量。模式搜索对每个候选块调用一次，约占编码时间的三成，所以
// 这里刻意重复了 token 树的代码——把估算标志的判断、sink 指针的取用和结构体
// 字段的读写都挪到循环外。
//
// 累加结果加进 e.cost.costBits，调用方按原有约定在调用前清零。
//
// estimateBlock4 is the estimation counterpart of writeBlock4: the same token
// tree with the bit count accumulated in a local. The mode search calls it once
// per candidate block, about a third of the encode time, so the token tree is
// deliberately duplicated here to hoist the estimation flag test, the sink
// fetch and the struct field traffic out of the loop.
//
// The total is added to e.cost.costBits; callers clear it beforehand as before.
func (e *encoder) estimateBlock4(plane, ctx int, skipDC bool, base int) uint8 {
	n := 0
	if skipDC {
		n = 1
	}
	pBand, pCtx := int(coeffBands[n]), ctx
	bits := 0.0
	// 概率表按 (plane, band, ctx) 取一次指针，循环里只做最后一维索引，
	// 省掉每个 token 都要算的三层偏移。
	// The probability table is fetched once per (plane, band, ctx) and the
	// loop only indexes the last dimension, saving a three level offset
	// computation per token.
	p := &defaultTokenProb[plane][pBand][pCtx]

	// 最后一个非零系数的扫描位置由量化时记好，这里只做下限修正。
	// The scan position of the last non-zero coefficient was recorded during
	// quantisation; only the lower bound is fixed up here.
	last := int(e.lastNZ[base/16])
	if last < n {
		last = n
	}
	if last == n {
		e.cost.costBits += costBit(p[0], false)
		return 0
	}
	bits += costBit(p[0], true)

	for n < 16 {
		n++
		v := e.levels[base+int(zigzag[n-1])]
		if v == 0 {
			bits += costBit(p[1], false)
			pBand, pCtx = int(coeffBands[n]), 0
			p = &defaultTokenProb[plane][pBand][pCtx]
			continue
		}
		bits += costBit(p[1], true)
		mag := v
		if mag < 0 {
			mag = -mag
		}
		ctx2 := 2
		switch {
		case mag == 1:
			bits += costBit(p[2], false)
			ctx2 = 1
		case mag == 2:
			bits += costBit(p[2], true)
			bits += costBit(p[3], false)
			bits += costBit(p[4], false)
		case mag <= 4:
			bits += costBit(p[2], true)
			bits += costBit(p[3], false)
			bits += costBit(p[4], true)
			bits += costBit(p[5], mag-3 == 1)
		case mag <= 6:
			bits += costBit(p[2], true)
			bits += costBit(p[3], true)
			bits += costBit(p[6], false)
			bits += costBit(p[7], false)
			bits += costBit(159, mag-5 == 1)
		case mag <= 10:
			bits += costBit(p[2], true)
			bits += costBit(p[3], true)
			bits += costBit(p[6], false)
			bits += costBit(p[7], true)
			bits += costBit(165, (mag-7)>>1 == 1)
			bits += costBit(145, (mag-7)&1 == 1)
		default:
			cat, baseMag, nbits := 0, int32(11), 3
			switch {
			case mag <= 18:
			case mag <= 34:
				cat, baseMag, nbits = 1, 19, 4
			case mag <= 66:
				cat, baseMag, nbits = 2, 35, 5
			default:
				cat, baseMag, nbits = 3, 67, 11
			}
			bits += costBit(p[2], true)
			bits += costBit(p[3], true)
			bits += costBit(p[6], true)
			b1 := cat >> 1
			bits += costBit(p[8], b1 == 1)
			bits += costBit(p[9+b1], cat&1 == 1)
			extra := uint32(mag - baseMag)
			for i := 0; i < nbits; i++ {
				bits += costBit(cat3456[cat][i], extra>>uint(nbits-1-i)&1 != 0)
			}
		}
		pBand, pCtx = int(coeffBands[n]), ctx2
		p = &defaultTokenProb[plane][pBand][pCtx]
		bits += costBit(uniformProb, v < 0)

		if n == 16 {
			break
		}
		more := n < last
		bits += costBit(p[0], more)
		if !more {
			break
		}
	}
	e.cost.costBits += bits
	return 1
}

// putToken 统计并写出一个 token 树节点的判决。概率取自当前帧的概率表，因此
// 第二遍会自动用上调整后的值。
//
// putToken tallies and emits one token tree decision. The probability comes from
// the current frame's table, so the second pass picks up the adapted values.
//
// 估算码长时用规范里的默认概率表而不是本帧的表：两遍编码因此会选出同样的
// 模式与系数，第一遍统计到的分布与第二遍写出的码流完全对应。
//
// The length estimate uses the default table from the specification rather than
// the frame's own table, so both passes pick the same modes and coefficients
// and the statistics of the first pass match the stream of the second.
func (e *encoder) putToken(plane, band, ctx, node int, bit bool) {
	if e.estimating {
		prob := defaultTokenProb[plane][band][ctx][node]
		if bit {
			e.cost.costBits += bitCostTable[1][prob]
		} else {
			e.cost.costBits += bitCostTable[0][prob]
		}
		return
	}
	if e.collect {
		var b int
		if bit {
			b = 1
		}
		e.counts[plane][band][ctx][node][b]++
	}
	e.tok.putBit(e.d.tokenProb[plane][band][ctx][node], bit)
}

// sink 返回当前写 token 的目标：正常编码时是真正的输出缓冲，估算时是只累计
// 比特数的累加器。
//
// sink returns where the token bits go: the real output buffer while encoding
// and the accumulator that only tallies the bits while estimating.
func (e *encoder) sink() *boolEncoder {
	if e.estimating {
		return &e.cost
	}
	return &e.tok
}

// quantize 把系数按反量化因子取整成电平值。bias 是取整偏置，由调用方按块
// 预计算成 q*quantBiasNum/100——把它提出来是为了让每个系数只剩一次除法，
// 而不是每系数都算一遍乘法和百分比除法。
//
// quantize rounds a coefficient into a level using the dequantization factor.
// bias is the rounding offset, precomputed per block by the caller as
// q*quantBiasNum/100; hoisting it leaves one division per coefficient instead
// of a multiply and a percentage division as well.
func quantize(v, q, bias int32) int32 {
	if q == 0 {
		return 0
	}
	a := v
	neg := a < 0
	if neg {
		a = -a
	}
	l := (a + bias) / q
	if l > maxCoeffLevel {
		l = maxCoeffLevel
	}
	if neg {
		return -l
	}
	return l
}

// quantBias 返回反量化因子 q 对应的取整偏置。
//
// quantBias returns the rounding offset for the dequantization factor q.
func quantBias(q int32) int32 {
	return q * quantBiasNum / 100
}

// fdct4x4Scalar 是逆变换的正变换，就地完成：两趟蝶形后整体除以二，使常量残差
// r 对应 DC 系数 8r，与逆变换的除以八一致。
//
// 平台相关的 fdct4x4 在支持 AVX2 时转发到汇编内核（见 fdct_amd64.go），两者
// 结果逐位一致。注意这里必须是**直接调用**而不是函数变量：调用点传的是
// 栈上的临时块（`var blk [16]int32`），一旦经由函数变量转发，编译器无法证明
// 被调函数不保存指针，就会把整块挤到堆上，每块多一次 64 字节分配。
//
// fdct4x4Scalar is the forward transform of the inverse, done in place: two
// butterfly passes and a halving, so a constant residual r maps to a DC of 8r,
// matching the inverse's divide by eight.
//
// The platform specific fdct4x4 forwards to the assembly kernel when AVX2 is
// available (see fdct_amd64.go) and both agree bit for bit. The forward has to
// be a direct call rather than a function variable: the call site passes a
// stack temporary (`var blk [16]int32`), and going through a function variable
// stops the compiler from proving the callee keeps no pointer, which pushes the
// whole block to the heap and adds a 64 byte allocation per block.
func fdct4x4Scalar(blk *[16]int32) {
	var tmp [16]int32
	for i := 0; i < 4; i++ {
		x0, x1, x2, x3 := blk[i], blk[4+i], blk[8+i], blk[12+i]
		s, p := x0+x3, x0-x3
		t, q := x1+x2, x1-x2
		tmp[i] = s + t
		tmp[4+i] = (fdctC1*p + fdctC2*q) >> 16
		tmp[8+i] = s - t
		tmp[12+i] = (fdctC2*p - fdctC1*q) >> 16
	}
	for i := 0; i < 4; i++ {
		y0, y1, y2, y3 := tmp[4*i], tmp[4*i+1], tmp[4*i+2], tmp[4*i+3]
		s, p := y0+y3, y0-y3
		t, q := y1+y2, y1-y2
		blk[4*i] = (s + t + 1) >> 1
		blk[4*i+1] = ((fdctC1*p+fdctC2*q)>>16 + 1) >> 1
		blk[4*i+2] = (s - t + 1) >> 1
		blk[4*i+3] = ((fdctC2*p-fdctC1*q)>>16 + 1) >> 1
	}
}

// fwht4x4 是 inverseWHT 的正变换：两趟 Hadamard 蝶形后整体除以二。
//
// fwht4x4 is the forward transform of inverseWHT: two Hadamard butterfly passes
// and a halving.
func fwht4x4(dc *[16]int32) {
	var tmp [16]int32
	for i := 0; i < 4; i++ {
		x0, x1, x2, x3 := dc[i], dc[4+i], dc[8+i], dc[12+i]
		a0, a1, a2, a3 := x0+x3, x1+x2, x1-x2, x0-x3
		tmp[i] = a0 + a1
		tmp[4+i] = a3 + a2
		tmp[8+i] = a0 - a1
		tmp[12+i] = a3 - a2
	}
	for i := 0; i < 4; i++ {
		y0, y1, y2, y3 := tmp[4*i], tmp[4*i+1], tmp[4*i+2], tmp[4*i+3]
		a0, a1, a2, a3 := y0+y3, y1+y2, y1-y2, y0-y3
		dc[4*i] = (a0 + a1 + 1) >> 1
		dc[4*i+1] = (a3 + a2 + 1) >> 1
		dc[4*i+2] = (a0 - a1 + 1) >> 1
		dc[4*i+3] = (a3 - a2 + 1) >> 1
	}
}
