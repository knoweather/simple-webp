package vp8

import (
	"image"
	"math"
)

// 帧内编码器：只使用 16x16 亮度模式与 8x8 色度模式，量化索引固定，环路滤波
// 关闭（滤波强度 0 时规范允许省略滤波）。
//
// The intra frame encoder: 16x16 luma and 8x8 chroma modes only, one fixed
// quantization index and the loop filter disabled (a filter level of zero lets
// the decoder skip filtering entirely).
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
	levels             [400]int32
	qi                 int
	// counts 记录每个 token 概率条目上取 0 与取 1 的次数，用于把默认概率表
	// 调整成贴合本帧统计的表。
	//
	// counts tallies the zero and one decisions at every token probability
	// entry, so the default table can be adapted to this frame's statistics.
	counts  [nPlane][nBand][nContext][nProb][2]uint32
	collect bool
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
	if src.SubsampleRatio != image.YCbCrSubsampleRatio420 {
		return nil, ErrInvalid
	}
	w, h := src.Rect.Dx(), src.Rect.Dy()
	if w <= 0 || h <= 0 || w > 16383 || h > 16383 {
		return nil, ErrInvalid
	}
	e := newEncoder(w, h, src, quality)
	return e.run(), nil
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

	e.hdr.init()
	e.tok.init()
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
	e.encodeAll()
	e.d.tokenProb = e.adaptTokenProb()

	e.collect, e.hdr.dry, e.tok.dry = false, false, false
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
	// 滤波：类型简单、强度 0、锐度 0、不做参考帧/模式偏移。
	// filtering: simple type, level 0, sharpness 0, no reference or mode deltas.
	h.putBit(uniformProb, false)
	h.putUint(uniformProb, 6, 0)
	h.putUint(uniformProb, 3, 0)
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

// encodeMB 编码一个宏块：选模式、变换量化、写模式位与残差 token、再重建。
//
// encodeMB codes one macroblock: mode selection, transform and quantization,
// the mode bits and residual tokens, then reconstruction.
func (e *encoder) encodeMB(mbx, mby int) {
	d := &e.d
	bx, by := mbx*16, mby*16
	cx, cy := mbx*8, mby*8

	bestY := uint8(predDC)
	bestCost := 1 << 30
	for _, m := range [4]uint8{predDC, predVE, predHE, predTM} {
		d.predict16(checkTopLeftPred(mbx, mby, m), bx, by)
		if c := sadPlane(d.y, e.srcY, bx, by, 16); c < bestCost {
			bestCost, bestY = c, m
		}
	}

	bestC := uint8(predDC)
	bestCost = 1 << 30
	for _, m := range [4]uint8{predDC, predVE, predHE, predTM} {
		cm := checkTopLeftPred(mbx, mby, m)
		d.predict8(d.cb, cm, cx, cy)
		d.predict8(d.cr, cm, cx, cy)
		c := sadPlane(d.cb, e.srcCb, cx, cy, 8) + sadPlane(d.cr, e.srcCr, cx, cy, 8)
		if c < bestCost {
			bestCost, bestC = c, m
		}
	}

	d.usePredY16 = true
	d.predY16 = bestY
	d.predC8 = bestC
	d.predict16(checkTopLeftPred(mbx, mby, bestY), bx, by)
	cm := checkTopLeftPred(mbx, mby, bestC)
	d.predict8(d.cb, cm, cx, cy)
	d.predict8(d.cr, cm, cx, cy)

	skip := e.transformMB(bx, by, cx, cy)

	if d.useSkipProb {
		e.hdr.putBit(d.skipProb, skip)
	}
	e.hdr.putBit(145, true)
	switch bestY {
	case predDC:
		e.hdr.putBit(156, false)
		e.hdr.putBit(163, false)
	case predVE:
		e.hdr.putBit(156, false)
		e.hdr.putBit(163, true)
	case predHE:
		e.hdr.putBit(156, true)
		e.hdr.putBit(128, false)
	default:
		e.hdr.putBit(156, true)
		e.hdr.putBit(128, true)
	}
	switch bestC {
	case predDC:
		e.hdr.putBit(142, false)
	case predVE:
		e.hdr.putBit(142, true)
		e.hdr.putBit(114, false)
	case predHE:
		e.hdr.putBit(142, true)
		e.hdr.putBit(114, true)
		e.hdr.putBit(183, false)
	default:
		e.hdr.putBit(142, true)
		e.hdr.putBit(114, true)
		e.hdr.putBit(183, true)
	}

	e.loadCoeffs()
	if !skip {
		e.writeTokens(mbx)
	} else {
		d.leftMB.nzY16 = 0
		d.upMB[mbx].nzY16 = 0
		d.leftMB.nzMask = 0
		d.upMB[mbx].nzMask = 0
	}
	e.setMasks()
	d.reconstructMacroblock(mbx, mby)
}

// transformMB 对宏块做残差、正变换与量化，结果留在 e.levels 中；全部为零时
// 返回 true，表示该宏块可以只做预测。
//
// transformMB computes the residual, the forward transforms and quantization
// into e.levels; it returns true when everything quantized to zero, meaning the
// macroblock needs prediction only.
func (e *encoder) transformMB(bx, by, cx, cy int) bool {
	d := &e.d
	q := d.q[0]
	for i := range e.levels {
		e.levels[i] = 0
	}

	// 亮度：16 个 4x4 块只保留 AC，DC 汇总进 Y2 做 Walsh-Hadamard。
	// luma: the sixteen 4x4 blocks keep their AC only, the DC values feed the
	// Walsh-Hadamard transform of the Y2 block.
	var dc [16]int32
	for n := 0; n < 16; n++ {
		x, y := bx+(n%4)*4, by+(n/4)*4
		var blk [16]int32
		e.residual(d.y, e.srcY, x, y, &blk)
		fdct4x4(&blk)
		dc[n] = blk[0]
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

	// 色度：Cb 的四个块在前，Cr 的四个块在后，全部系数都编码。
	// chroma: the four Cb blocks then the four Cr blocks, every coefficient
	// coded.
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

	for _, v := range e.levels {
		if v != 0 {
			return false
		}
	}
	return true
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

// loadCoeffs 把量化后的系数反量化进解码器的系数缓冲，Y2 走逆 WHT 分散到
// 16 个亮度块。
//
// loadCoeffs dequantizes the levels into the decoder's coefficient buffer; the
// Y2 block goes through the inverse Walsh-Hadamard transform, which scatters the
// DC values into the sixteen luma blocks.
func (e *encoder) loadCoeffs() {
	d := &e.d
	q := d.q[0]
	for i := range d.coeff {
		d.coeff[i] = 0
	}
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

// setMasks 依据反量化后的系数算出 DC 与 AC 非零掩码，供逆变换选择完整 IDCT
// 或仅 DC 的快路径。
//
// setMasks derives the non-zero DC and AC masks from the dequantized
// coefficients, which the inverse transform uses to choose between the full
// IDCT and the DC-only fast path.
func (e *encoder) setMasks() {
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
	d.nzDCMask = dcMask
	d.nzACMask = acMask
}

// writeTokens 按解码器解析的顺序写出系数 token：Y2、16 个亮度块、Cb、Cr。
// 上下文与解码器一样取自上方与左侧宏块的非零标记。
//
// writeTokens emits the coefficient tokens in the order the decoder parses them:
// Y2, the sixteen luma blocks, Cb, Cr. The contexts come from the above and left
// non-zero flags, exactly as the decoder derives them.
func (e *encoder) writeTokens(mbx int) {
	d := &e.d
	nz := e.writeBlock4(planeY2, int(d.leftMB.nzY16+d.upMB[mbx].nzY16), false, 384)
	d.leftMB.nzY16 = nz
	d.upMB[mbx].nzY16 = nz

	lnz := unpackMask[d.leftMB.nzMask&0x0f]
	unz := unpackMask[d.upMB[mbx].nzMask&0x0f]
	coeffBase := 0
	for y := 0; y < 4; y++ {
		nz := lnz[y]
		for x := 0; x < 4; x++ {
			nz = e.writeBlock4(planeY1WithY2, int(nz+unz[x]), true, coeffBase)
			unz[x] = nz
			coeffBase += 16
		}
		lnz[y] = nz
	}
	lnzMask := packMask(lnz, 0)
	unzMask := packMask(unz, 0)

	// 色度的系数缓冲按 Cb 四块、Cr 四块顺序连续排列，基址随之递增。
	// The chroma coefficient buffer runs through the four Cb blocks and then the
	// four Cr blocks, so the base advances with every block.
	lnz = unpackMask[d.leftMB.nzMask>>4]
	unz = unpackMask[d.upMB[mbx].nzMask>>4]
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
	lnzMask |= packMask(lnz, 4)
	unzMask |= packMask(unz, 4)
	d.leftMB.nzMask = uint8(lnzMask)
	d.upMB[mbx].nzMask = uint8(unzMask)
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
			e.tok.putBit(159, mag-5 == 1)
		case mag <= 10:
			e.putToken(plane, pBand, pCtx, 2, true)
			e.putToken(plane, pBand, pCtx, 3, true)
			e.putToken(plane, pBand, pCtx, 6, false)
			e.putToken(plane, pBand, pCtx, 7, true)
			e.tok.putBit(165, (mag-7)>>1 == 1)
			e.tok.putBit(145, (mag-7)&1 == 1)
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
				e.tok.putBit(cat3456[cat][i], extra>>uint(nbits-1-i)&1 != 0)
			}
		}
		pBand, pCtx = int(coeffBands[n]), ctx2
		e.tok.putBit(uniformProb, v < 0)

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
func (e *encoder) putToken(plane, band, ctx, node int, bit bool) {
	if e.collect {
		var b int
		if bit {
			b = 1
		}
		e.counts[plane][band][ctx][node][b]++
	}
	e.tok.putBit(e.d.tokenProb[plane][band][ctx][node], bit)
}

// sadPlane 返回 n x n 块内预测与源的绝对差之和。
//
// sadPlane returns the sum of absolute differences between the prediction and
// the source over an n x n block.
//
// 这里是编码器里唯一适合向量化的热点，但实测收益不足以抵消代价，故保持标量：
// 在 VGA 图像的编码总耗时中它约占 18%，熵编码（putBit）占 20%、预测占 22%，
// 后两者是串行比特流与逐像素访存，向量化帮不上忙；即便 SAD 提速八倍，整帧也只
// 快约 18%。而 simd 包要求调用方用 GOEXPERIMENT=simd 构建，否则连编译都不通过，
// 且该包尚未承诺稳定——让所有使用方承担这个构建约束，不划算。
//
// This is the encoder's only hot spot that suits vectorisation, but the measured
// gain does not cover the cost, so it stays scalar: it accounts for about 18% of
// the encode time of a VGA image, while the entropy coder (putBit) takes 20% and
// prediction 22% -- both are a serial bit stream and per-pixel memory traffic
// that vectorisation cannot help. Even an eightfold faster SAD would only make
// the frame about 18% quicker. Against that, the simd package requires callers
// to build with GOEXPERIMENT=simd or nothing compiles at all, and it is not yet
// covered by the compatibility promise, so imposing that build constraint on
// every user is not worth it.
func sadPlane(a, b *plane, x, y, n int) int {
	s := 0
	for j := 0; j < n; j++ {
		ai := a.at(x, y+j)
		bi := b.at(x, y+j)
		for i := 0; i < n; i++ {
			d := int(a.pix[ai+i]) - int(b.pix[bi+i])
			if d < 0 {
				d = -d
			}
			s += d
		}
	}
	return s
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
