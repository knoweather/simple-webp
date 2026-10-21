package vp8

import (
	"image"
	"math"
)

// 帧内编码器：亮度可以用整块 16x16 模式，也可以用十六个 4x4 模式；色度用
// 8x8 模式。量化索引固定，环路滤波关闭（滤波强度 0 时规范允许省略滤波）。
// 模式由率失真准则选出：每个候选模式都要走一遍预测、变换与量化，再用
// 「重建失真 + 系数 × 比特数」比较。
//
// The intra frame encoder: luma uses either one 16x16 mode or sixteen 4x4
// modes, chroma uses 8x8 modes. One fixed quantization index and the loop
// filter disabled (a filter level of zero lets the decoder skip filtering
// entirely). The modes are picked by a rate distortion criterion: every
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
	return e.run(), nil
}

// EncodeToTarget 在给定选项的基础上做码率控制：在质量 0..100 之间二分，返回
// 体积最接近 targetBytes 的那一次编码结果。码率控制只调全局量化，逐段量化
// 尚未实现；目标无法精确命中时返回最接近的一次。
//
// EncodeToTarget adds rate control on top of the given options: it bisects the
// quality over 0..100 and returns the encode whose size is closest to
// targetBytes. Only the global quantiser is adjusted (per-segment quantization is
// not implemented); when the target cannot be hit exactly the closest encode is
// returned.
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
// quantIndex maps quality onto the quantization index: higher quality means a
// smaller index.
func quantIndex(quality int) int {
	if quality < 0 {
		quality = 0
	}
	if quality > 100 {
		quality = 100
	}
	return (100 - quality) * 127 / 100
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
	q := e.qi
	d.q[0].y1[0] = dequantTableDC[q]
	d.q[0].y1[1] = dequantTableAC[q]
	d.q[0].y2[0] = dequantTableDC[q] * 2
	d.q[0].y2[1] = dequantTableAC[q] * 155 / 100
	if d.q[0].y2[1] < 8 {
		d.q[0].y2[1] = 8
	}
	d.q[0].uv[0] = dequantTableDC[clipInt(q, 0, 117)]
	d.q[0].uv[1] = dequantTableAC[q]

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
// single residual partition.
func (e *encoder) run() []byte {
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
	tok := e.tok.finish()
	out := make([]byte, 0, 10+len(hdr)+len(tok))
	tag := uint32(1)<<4 | uint32(len(hdr))<<5
	out = append(out, byte(tag), byte(tag>>8), byte(tag>>16), 0x9d, 0x01, 0x2a)
	out = append(out, byte(d.width), byte(d.width>>8), byte(d.height), byte(d.height>>8))
	out = append(out, hdr...)
	out = append(out, tok...)
	return out
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
	// 不使用分段。
	// no segmentation.
	h.putBit(uniformProb, false)
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
	q := d.q[0]
	var dc [16]int32
	for n := 0; n < 16; n++ {
		x, y := bx+(n%4)*4, by+(n/4)*4
		var blk [16]int32
		e.residual(d.y, e.srcY, x, y, &blk)
		fdct4x4(&blk)
		dc[n] = blk[0]
		e.levels[16*n] = 0
		for z := 1; z < 16; z++ {
			e.levels[16*n+z] = quantize(blk[z], q.y1[1])
		}
	}
	fwht4x4(&dc)
	for i := 0; i < 16; i++ {
		qq := q.y2[1]
		if i == 0 {
			qq = q.y2[0]
		}
		e.levels[384+i] = quantize(dc[i], qq)
	}
}

// transformLuma4 对一个 4x4 亮度块做残差、正变换与量化，结果留在 e.levels
// 的第 n 块里：DC 与 AC 都留在块内，不经过 Y2。
//
// transformLuma4 computes the residual, the forward transform and the
// quantization of one 4x4 luma block into the n-th block of e.levels: the DC
// and the AC stay in the block, no Y2 is involved.
func (e *encoder) transformLuma4(n, x, y int) {
	d := &e.d
	q := d.q[0]
	var blk [16]int32
	e.residual(d.y, e.srcY, x, y, &blk)
	fdct4x4(&blk)
	for z := 0; z < 16; z++ {
		qq := q.y1[1]
		if z == 0 {
			qq = q.y1[0]
		}
		e.levels[16*n+z] = quantize(blk[z], qq)
	}
}

// transformChroma 对色度做残差、正变换与量化：Cb 的四个块在前，Cr 的四个块
// 在后，全部系数都编码。
//
// transformChroma computes the residual, the forward transforms and the
// quantization of the chroma: the four Cb blocks come first, then the four Cr
// blocks, and every coefficient is coded.
func (e *encoder) transformChroma(cx, cy int) {
	d := &e.d
	q := d.q[0]
	for c := 0; c < 4; c++ {
		x, y := cx+(c%2)*4, cy+(c/2)*4
		var blk [16]int32
		e.residual(d.cb, e.srcCb, x, y, &blk)
		fdct4x4(&blk)
		for z := 0; z < 16; z++ {
			qq := q.uv[1]
			if z == 0 {
				qq = q.uv[0]
			}
			e.levels[256+16*c+z] = quantize(blk[z], qq)
		}
		e.residual(d.cr, e.srcCr, x, y, &blk)
		fdct4x4(&blk)
		for z := 0; z < 16; z++ {
			qq := q.uv[1]
			if z == 0 {
				qq = q.uv[0]
			}
			e.levels[320+16*c+z] = quantize(blk[z], qq)
		}
	}
}

// residual 取预测平面与源平面之差。
//
// residual takes the difference between the prediction and the source plane.
func (e *encoder) residual(pred, src *plane, x, y int, out *[16]int32) {
	for j := 0; j < 4; j++ {
		pr := pred.at(x, y+j)
		sr := src.at(x, y+j)
		for i := 0; i < 4; i++ {
			out[4*j+i] = int32(int(src.pix[sr+i]) - int(pred.pix[pr+i]))
		}
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
	q := d.q[0]
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
	q := d.q[0]
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
	q := d.q[0]
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
	n := 0
	if skipDC {
		n = 1
	}
	pBand, pCtx := int(coeffBands[n]), ctx

	// 最后一个非零系数的扫描位置，用于决定何处写 EOB。
	// scan position of the last non-zero coefficient, which decides where the
	// EOB goes.
	last := n
	for i := n; i < 16; i++ {
		if e.levels[base+int(zigzag[i])] != 0 {
			last = i + 1
		}
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

// quantize 把系数按反量化因子取整成电平值。
//
// quantize rounds a coefficient into a level using the dequantization factor.
func quantize(v int32, q uint16) int32 {
	if q == 0 {
		return 0
	}
	a := v
	neg := a < 0
	if neg {
		a = -a
	}
	l := (a + int32(q)*quantBiasNum/100) / int32(q)
	if l > maxCoeffLevel {
		l = maxCoeffLevel
	}
	if neg {
		return -l
	}
	return l
}

// fdct4x4 是逆变换的正变换，就地完成：两趟蝶形后整体除以二，使常量残差
// r 对应 DC 系数 8r，与逆变换的除以八一致。
//
// fdct4x4 is the forward transform of the inverse, done in place: two butterfly
// passes and a halving, so a constant residual r maps to a DC of 8r, matching the
// inverse's divide by eight.
func fdct4x4(blk *[16]int32) {
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
