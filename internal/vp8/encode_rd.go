package vp8

import "math"

// 率失真决策：每个候选模式都要走一遍预测、正变换与量化，再用
// 「重建失真 + 权衡系数 × 码长」比较。失真用平方像素误差度量，码长用比特度量，
// 权衡系数与量化步长的平方成正比——步长减半时失真降到四分之一，一个比特也就
// 值得更多的失真。
//
// The rate distortion decisions run every candidate mode through prediction,
// the forward transform and quantization, and compare the candidates on
// "reconstruction distortion plus lambda times the length of the code". The
// distortion is measured in squared pixel errors and the length in bits, with
// lambda growing with the square of the quantization step: halving the step
// quarters the distortion and makes a bit worth more distortion.

// rdScaleDefault 是率失真权衡系数的默认值：一个比特值多少平方像素误差。
// 系数与量化步长的平方成正比，因为步长减半时失真降到四分之一。
//
// rdScaleDefault is the default rate distortion trade-off factor: how much
// squared pixel error a bit is worth. The factor grows with the square of the
// quantization step, because halving the step quarters the distortion.
const rdScaleDefault = 0.0375

// y16Modes 是 16x16 亮度与 8x8 色度可用的四种模式。
//
// y16Modes are the four modes available to a 16x16 luma and an 8x8 chroma
// block.
var y16Modes = [4]uint8{predDC, predVE, predHE, predTM}

// y4Modes 是 4x4 亮度可用的十种模式。
//
// y4Modes are the ten modes available to a 4x4 luma block.
var y4Modes = [10]uint8{predDC, predTM, predVE, predHE, predRD, predVR, predLD, predVL, predHD, predHU}

// rdLambda 返回率失真权衡系数。
//
// rdLambda returns the rate distortion trade-off factor.
func (e *encoder) rdLambda() float64 {
	q := float64(e.d.q[0].y1[1])
	return e.rdScale * q * q
}

// chooseLumaMode 在整块 16x16 与十六个 4x4 两条路里选代价更低的一条。返回
// 是否走 16x16 以及选中的 16x16 模式；走 4x4 时每个块选中的模式存进
// e.predY4。
//
// chooseLumaMode picks the cheaper of one 16x16 mode and sixteen 4x4 modes. It
// reports whether the 16x16 path won and which mode it picked; with 4x4 modes
// the mode chosen for every block lands in e.predY4.
func (e *encoder) chooseLumaMode(mbx, mby, bx, by int) (bool, uint8) {
	d := &e.d
	lambda := e.rdLambda()

	savedLeft, savedUp := d.leftMB, d.upMB[mbx]
	cost16, mode16 := e.evalLuma16(mbx, mby, bx, by, lambda)
	d.leftMB, d.upMB[mbx] = savedLeft, savedUp

	cost4 := e.evalLuma4(mbx, mby, bx, by, lambda)
	d.leftMB, d.upMB[mbx] = savedLeft, savedUp

	if cost16 <= cost4 {
		copy(e.levels[:256], e.candLevels[:256])
		copy(e.levels[384:], e.candLevels[384:])
		return true, mode16
	}
	e.y4Blocks++
	return false, 0
}

// evalLuma16 评估四种 16x16 亮度模式，返回代价最小的那个的代价与模式；最优
// 模式的系数留在 e.candLevels 里。
//
// evalLuma16 evaluates the four 16x16 luma modes and returns the cost and the
// mode of the cheapest one; the coefficients of that mode land in e.candLevels.
func (e *encoder) evalLuma16(mbx, mby, bx, by int, lambda float64) (float64, uint8) {
	d := &e.d
	savedLeft, savedUp := d.leftMB, d.upMB[mbx]
	bestCost := math.Inf(1)
	bestMode := uint8(predDC)
	for _, mode := range y16Modes {
		d.leftMB, d.upMB[mbx] = savedLeft, savedUp
		d.predict16(checkTopLeftPred(mbx, mby, mode), bx, by)
		e.transformLuma16(bx, by)
		e.loadCoeffsLuma16()
		e.setMasksLuma16()
		e.reconstructLuma16(mbx, mby)
		dist := ssePlane(d.y, e.srcY, bx, by, 16)
		rate := e.estimateLuma16Tokens(mbx) + e.estimateY16Mode(mode)
		cost := float64(dist) + lambda*rate
		if cost < bestCost {
			bestCost, bestMode = cost, mode
			copy(e.candLevels[:256], e.levels[:256])
			copy(e.candLevels[384:], e.levels[384:])
		}
	}
	d.leftMB, d.upMB[mbx] = savedLeft, savedUp
	return bestCost, bestMode
}

// evalLuma4 评估十六个 4x4 亮度块：每个块独立地在十种模式里选代价最小的
// 一个。选中模式的重建留在平面里，下一个块因此能从解码端会看到的像素预测。
//
// evalLuma4 evaluates the sixteen 4x4 luma blocks: every block picks the
// cheapest of the ten modes on its own. The reconstruction of the chosen mode
// stays in the plane, so the next block predicts from the pixels the decoder
// will see.
func (e *encoder) evalLuma4(mbx, mby, bx, by int, lambda float64) float64 {
	d := &e.d
	// 4x4 模式下没有 Y2 块，那一段系数必须清零，否则 skip 判断会看错。
	// The 4x4 path has no Y2 block, so those coefficients have to be cleared or
	// the skip test would look at stale values.
	for i := 384; i < 400; i++ {
		e.levels[i] = 0
	}

	// 模式上下文取自上方与左侧邻块，与解码器跟踪的一样。
	// The mode context comes from the above and left neighbour, exactly as the
	// decoder tracks it.
	var above [4]uint8
	copy(above[:], d.upMB[mbx].pred[:])
	// 非零标记的上下文同样取自上方与左侧。
	// The non-zero flags come from the above and left neighbours as well.
	lnz := unpackMask[d.leftMB.nzMask&0x0f]
	unz := unpackMask[d.upMB[mbx].nzMask&0x0f]

	total := 0.0
	for j := 0; j < 4; j++ {
		left := d.leftMB.pred[j]
		for i := 0; i < 4; i++ {
			n := 4*j + i
			x, y := bx+i*4, by+j*4
			prob := &predProb[above[i]][left]
			ctx := int(lnz[j] + unz[i])
			cost, mode, nz := e.evalLuma4Block(n, x, y, ctx, prob, lambda)
			total += cost
			e.predY4[j][i] = mode
			above[i] = mode
			left = mode
			lnz[j] = nz
			unz[i] = nz
		}
	}
	return total
}

// evalLuma4Block 在十种模式里为第 n 个 4x4 亮度块选代价最小的一个，返回代价、
// 模式与该块是否有非零系数。选中模式的系数与重建像素都留在原地。
//
// evalLuma4Block picks the cheapest of the ten modes for the n-th 4x4 luma
// block and returns its cost, its mode and whether the block carries non-zero
// coefficients. The levels and the reconstructed pixels of the chosen mode stay
// in place.
func (e *encoder) evalLuma4Block(n, x, y, ctx int, prob *[9]uint8, lambda float64) (float64, uint8, uint8) {
	d := &e.d
	// 候选模式都会覆写这块的像素，先存一份备查。
	// Every candidate overwrites the pixels of the block, so keep a copy.
	var saved [16]byte
	for j := 0; j < 4; j++ {
		idx := d.y.at(x, y+j)
		copy(d.y.pix[idx:idx+4], saved[j*4:j*4+4])
	}
	var bestLevels [16]int32
	var bestPix [16]byte
	bestCost := math.Inf(1)
	bestMode := uint8(predDC)
	bestNZ := uint8(0)
	for _, mode := range y4Modes {
		for j := 0; j < 4; j++ {
			idx := d.y.at(x, y+j)
			copy(d.y.pix[idx:idx+4], saved[j*4:j*4+4])
		}
		d.predict4(mode, x, y)
		e.transformLuma4(n, x, y)
		e.loadCoeffsLuma4(n)
		e.setMasksLuma4(n)
		d.addResidual4(x, y, n)
		dist := ssePlane(d.y, e.srcY, x, y, 4)
		rate := e.estimateLuma4Tokens(n, ctx) + e.estimatePredMode(prob, mode)
		cost := float64(dist) + lambda*rate
		if cost < bestCost {
			bestCost, bestMode = cost, mode
			bestNZ = e.blockIsNonZero(n)
			copy(bestLevels[:], e.levels[16*n:16*n+16])
			for j := 0; j < 4; j++ {
				idx := d.y.at(x, y+j)
				copy(bestPix[j*4:j*4+4], d.y.pix[idx:idx+4])
			}
		}
	}
	copy(e.levels[16*n:16*n+16], bestLevels[:])
	for j := 0; j < 4; j++ {
		idx := d.y.at(x, y+j)
		copy(d.y.pix[idx:idx+4], bestPix[j*4:j*4+4])
	}
	return bestCost, bestMode, bestNZ
}

// blockIsNonZero 报告第 n 个 4x4 块是否有非零系数。
//
// blockIsNonZero reports whether the n-th 4x4 block carries non-zero
// coefficients.
func (e *encoder) blockIsNonZero(n int) uint8 {
	for z := 0; z < 16; z++ {
		if e.levels[16*n+z] != 0 {
			return 1
		}
	}
	return 0
}

// chooseChromaMode 在四种 8x8 色度模式里选代价最小的一个。
//
// chooseChromaMode picks the cheapest of the four 8x8 chroma modes.
func (e *encoder) chooseChromaMode(mbx, mby, cx, cy int) uint8 {
	d := &e.d
	lambda := e.rdLambda()
	savedLeft, savedUp := d.leftMB, d.upMB[mbx]
	bestCost := math.Inf(1)
	bestMode := uint8(predDC)
	for _, mode := range y16Modes {
		d.leftMB, d.upMB[mbx] = savedLeft, savedUp
		cm := checkTopLeftPred(mbx, mby, mode)
		d.predict8(d.cb, cm, cx, cy)
		d.predict8(d.cr, cm, cx, cy)
		e.transformChroma(cx, cy)
		e.loadCoeffsChroma()
		e.setMasksChroma()
		e.reconstructChroma(mbx, mby)
		dist := ssePlane(d.cb, e.srcCb, cx, cy, 8) + ssePlane(d.cr, e.srcCr, cx, cy, 8)
		rate := e.estimateChromaTokens(mbx) + e.estimateChromaMode(mode)
		cost := float64(dist) + lambda*rate
		if cost < bestCost {
			bestCost, bestMode = cost, mode
			copy(e.candLevels[256:384], e.levels[256:384])
		}
	}
	d.leftMB, d.upMB[mbx] = savedLeft, savedUp
	copy(e.levels[256:384], e.candLevels[256:384])
	return bestMode
}

// reconstructLuma16 在预测之上叠加亮度残差。
//
// reconstructLuma16 adds the luma residual on top of the prediction.
func (e *encoder) reconstructLuma16(mbx, mby int) {
	d := &e.d
	bx, by := mbx*16, mby*16
	for j := 0; j < 4; j++ {
		for i := 0; i < 4; i++ {
			d.addResidual4(bx+i*4, by+j*4, 4*j+i)
		}
	}
}

// reconstructChroma 在预测之上叠加色度残差。
//
// reconstructChroma adds the chroma residual on top of the prediction.
func (e *encoder) reconstructChroma(mbx, mby int) {
	d := &e.d
	cx, cy := mbx*8, mby*8
	if d.nzACMask&0x0f0000 != 0 {
		d.addIDCT8Plane(d.cb, cx, cy, 256)
	} else if d.nzDCMask&0x0f0000 != 0 {
		d.addIDCT8DCPlane(d.cb, cx, cy, 256)
	}
	if d.nzACMask&0xf00000 != 0 {
		d.addIDCT8Plane(d.cr, cx, cy, 320)
	} else if d.nzDCMask&0xf00000 != 0 {
		d.addIDCT8DCPlane(d.cr, cx, cy, 320)
	}
}

// estimateLuma16Tokens 返回写出 16x16 模式的亮度系数所需的比特数。
//
// estimateLuma16Tokens returns the number of bits the luma coefficients of a
// 16x16 mode take.
func (e *encoder) estimateLuma16Tokens(mbx int) float64 {
	d := &e.d
	saved := d.usePredY16
	d.usePredY16 = true
	e.estimating = true
	e.cost.costBits = 0
	e.writeTokensLuma(mbx)
	e.estimating = false
	d.usePredY16 = saved
	bits := e.cost.costBits
	e.cost.costBits = 0
	return bits
}

// estimateLuma4Tokens 返回写出一个 4x4 亮度块的系数所需的比特数。
//
// estimateLuma4Tokens returns the number of bits the coefficients of one 4x4
// luma block take.
func (e *encoder) estimateLuma4Tokens(n, ctx int) float64 {
	e.estimating = true
	e.cost.costBits = 0
	e.writeBlock4(planeY1SansY2, ctx, false, 16*n)
	e.estimating = false
	bits := e.cost.costBits
	e.cost.costBits = 0
	return bits
}

// estimateChromaTokens 返回写出色度系数所需的比特数。
//
// estimateChromaTokens returns the number of bits the chroma coefficients take.
func (e *encoder) estimateChromaTokens(mbx int) float64 {
	e.estimating = true
	e.cost.costBits = 0
	e.writeTokensChroma(mbx)
	e.estimating = false
	bits := e.cost.costBits
	e.cost.costBits = 0
	return bits
}

// estimateY16Mode 返回写出 16x16 亮度模式所需的比特数。
//
// estimateY16Mode returns the number of bits a 16x16 luma mode takes.
func (e *encoder) estimateY16Mode(mode uint8) float64 {
	e.cost.costBits = 0
	writeY16Mode(&e.cost, mode)
	return e.cost.costBits
}

// estimateChromaMode 返回写出 8x8 色度模式所需的比特数。
//
// estimateChromaMode returns the number of bits an 8x8 chroma mode takes.
func (e *encoder) estimateChromaMode(mode uint8) float64 {
	e.cost.costBits = 0
	writeChromaMode(&e.cost, mode)
	return e.cost.costBits
}

// estimatePredMode 返回写出一个 4x4 亮度模式所需的比特数。
//
// estimatePredMode returns the number of bits one 4x4 luma mode takes.
func (e *encoder) estimatePredMode(prob *[9]uint8, mode uint8) float64 {
	e.cost.costBits = 0
	writePredMode(&e.cost, prob, mode)
	return e.cost.costBits
}

// writeY4Modes 写出十六个 4x4 亮度模式：上下文取自上方与左侧邻块的模式，
// 与解码器跟踪的一样。
//
// writeY4Modes emits the sixteen 4x4 luma modes: the context comes from the
// modes of the above and left neighbours, exactly as the decoder tracks it.
func (e *encoder) writeY4Modes(mbx int) {
	d := &e.d
	var above [4]uint8
	copy(above[:], d.upMB[mbx].pred[:])
	for j := 0; j < 4; j++ {
		left := d.leftMB.pred[j]
		for i := 0; i < 4; i++ {
			mode := d.predY4[j][i]
			writePredMode(&e.hdr, &predProb[above[i]][left], mode)
			above[i] = mode
			left = mode
			d.upMB[mbx].pred[i] = mode
		}
		d.leftMB.pred[j] = left
	}
}

// writeY16Mode 写出 16x16 亮度模式（规范 11.2 节的二叉树）。
//
// writeY16Mode emits the 16x16 luma mode through the binary tree of section
// 11.2.
func writeY16Mode(sink *boolEncoder, mode uint8) {
	switch mode {
	case predDC:
		sink.putBit(156, false)
		sink.putBit(163, false)
	case predVE:
		sink.putBit(156, false)
		sink.putBit(163, true)
	case predHE:
		sink.putBit(156, true)
		sink.putBit(128, false)
	default:
		sink.putBit(156, true)
		sink.putBit(128, true)
	}
}

// writeChromaMode 写出 8x8 色度模式（规范 11.4 节的二叉树）。
//
// writeChromaMode emits the 8x8 chroma mode through the binary tree of section
// 11.4.
func writeChromaMode(sink *boolEncoder, mode uint8) {
	switch mode {
	case predDC:
		sink.putBit(142, false)
	case predVE:
		sink.putBit(142, true)
		sink.putBit(114, false)
	case predHE:
		sink.putBit(142, true)
		sink.putBit(114, true)
		sink.putBit(183, false)
	default:
		sink.putBit(142, true)
		sink.putBit(114, true)
		sink.putBit(183, true)
	}
}

// writePredMode 写出一个 4x4 亮度模式（规范 11.5 节的二叉树）：概率按上方与
// 左侧邻块的模式索引。
//
// writePredMode emits one 4x4 luma mode through the binary tree of section
// 11.5; the probabilities are indexed by the modes of the above and left
// neighbours.
func writePredMode(sink *boolEncoder, prob *[9]uint8, mode uint8) {
	switch mode {
	case predDC:
		sink.putBit(prob[0], false)
	case predTM:
		sink.putBit(prob[0], true)
		sink.putBit(prob[1], false)
	case predVE:
		sink.putBit(prob[0], true)
		sink.putBit(prob[1], true)
		sink.putBit(prob[2], false)
	case predHE:
		sink.putBit(prob[0], true)
		sink.putBit(prob[1], true)
		sink.putBit(prob[2], true)
		sink.putBit(prob[3], false)
		sink.putBit(prob[4], false)
	case predRD:
		sink.putBit(prob[0], true)
		sink.putBit(prob[1], true)
		sink.putBit(prob[2], true)
		sink.putBit(prob[3], false)
		sink.putBit(prob[4], true)
		sink.putBit(prob[5], false)
	case predVR:
		sink.putBit(prob[0], true)
		sink.putBit(prob[1], true)
		sink.putBit(prob[2], true)
		sink.putBit(prob[3], false)
		sink.putBit(prob[4], true)
		sink.putBit(prob[5], true)
	case predLD:
		sink.putBit(prob[0], true)
		sink.putBit(prob[1], true)
		sink.putBit(prob[2], true)
		sink.putBit(prob[3], true)
		sink.putBit(prob[6], false)
	case predVL:
		sink.putBit(prob[0], true)
		sink.putBit(prob[1], true)
		sink.putBit(prob[2], true)
		sink.putBit(prob[3], true)
		sink.putBit(prob[6], true)
		sink.putBit(prob[7], false)
	case predHD:
		sink.putBit(prob[0], true)
		sink.putBit(prob[1], true)
		sink.putBit(prob[2], true)
		sink.putBit(prob[3], true)
		sink.putBit(prob[6], true)
		sink.putBit(prob[7], true)
		sink.putBit(prob[8], false)
	default:
		sink.putBit(prob[0], true)
		sink.putBit(prob[1], true)
		sink.putBit(prob[2], true)
		sink.putBit(prob[3], true)
		sink.putBit(prob[6], true)
		sink.putBit(prob[7], true)
		sink.putBit(prob[8], true)
	}
}

// ssePlane 返回 n x n 块内两个平面的平方误差之和。
//
// ssePlane returns the sum of the squared differences between two planes over
// an n x n block.
func ssePlane(a, b *plane, x, y, n int) int {
	s := 0
	for j := 0; j < n; j++ {
		ai := a.at(x, y+j)
		bi := b.at(x, y+j)
		for i := 0; i < n; i++ {
			d := int(a.pix[ai+i]) - int(b.pix[bi+i])
			s += d * d
		}
	}
	return s
}
