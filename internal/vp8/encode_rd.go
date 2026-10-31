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

// rdLambda 返回率失真权衡系数。量化步长取当前宏块所在段的 AC 因子，
// 分段量化时各段的权衡随之不同；未分段时 d.segment 恒为 0，行为不变。
//
// rdLambda returns the rate distortion trade-off factor. The quantizer step is
// the AC factor of the current macroblock's segment, so with segmentation
// every segment trades at its own rate; without segmentation d.segment is
// always 0 and the behaviour is unchanged.
func (e *encoder) rdLambda() float64 {
	q := float64(e.d.q[e.d.segment].y1[1])
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
		e.lastNZ = e.candLastNZ
		return true, mode16
	}
	e.y4Blocks++
	return false, 0
}

// sadBlock16 是 16x16 绝对差之和的入口。支持 AVX2 的 amd64 机器在包初始化
// 时把它换成汇编内核，其余平台走标量版本；两者结果相同。函数体有 256 次循环，
// 本来就不会被内联，所以走函数指针不损失什么。
//
// sadBlock16 is the entry point for the sum of absolute differences over a
// 16x16 block. On amd64 machines with AVX2 the package initialisation swaps in
// the assembly kernel and other platforms use the scalar version; both agree.
// The body loops 256 times and would never be inlined anyway, so the indirect
// call costs nothing.
var sadBlock16 = sadBlock16Scalar

// sadBlock16Scalar 返回 16x16 块上预测像素与原始像素的绝对差之和，用作 16x16
// 模式的预筛代价。与 sadBlock4 同一套逻辑，只是块更大；分开写是为了让两处
// 的内层循环边界都能被编译器折叠成常量。
//
// sadBlock16Scalar returns the sum of absolute differences between the
// prediction and the source over a 16x16 block, the pre-filter cost for 16x16
// modes. It is the same logic as sadBlock4 on a bigger block; the two are kept
// apart so the compiler can fold both inner loop bounds into constants.
func sadBlock16Scalar(pred, src *plane, x, y int) int {
	s := 0
	pi := pred.at(x, y)
	si := src.at(x, y)
	for j := 0; j < 16; j++ {
		for i := 0; i < 16; i++ {
			d := int(pred.pix[pi+i]) - int(src.pix[si+i])
			if d < 0 {
				d = -d
			}
			s += d
		}
		pi += pred.stride
		si += src.stride
	}
	return s
}

// y16Candidates 是 16x16 亮度模式进入完整率失真评估的候选数。四种模式里先用
// 绝对差排序，只有最便宜的两个走完变换、量化、重建与码率估计。
//
// 实测这一步省下约百分之八的编码时间，代价是同质量下体积涨千分之二：被跳过的
// 两个模式偶尔会在码率上反超，但绝对差排序基本抓得住真正的赢家。
//
// y16Candidates is how many 16x16 luma modes reach the full rate-distortion
// evaluation. The four modes are ranked by absolute difference and only the two
// cheapest are transformed, quantised, reconstructed and rate estimated.
//
// Measured, this saves about eight percent of the encode time for two tenths of
// a percent in size at the same quality: a skipped mode occasionally wins on
// rate, but ranking by absolute difference catches the real winner almost
// always.
const y16Candidates = 2

// evalLuma16 评估四种 16x16 亮度模式，返回代价最小的那个的代价与模式；最优
// 模式的系数留在 e.candLevels 里。
//
// 四种模式先按预测的绝对差排序，只把前 y16Candidates 个送进完整评估。
//
// evalLuma16 evaluates the four 16x16 luma modes and returns the cost and the
// mode of the cheapest one; the coefficients of that mode land in e.candLevels.
//
// The four modes are first ranked by the absolute error of their prediction and
// only the best y16Candidates reach the full evaluation.
func (e *encoder) evalLuma16(mbx, mby, bx, by int, lambda float64) (float64, uint8) {
	d := &e.d
	savedLeft, savedUp := d.leftMB, d.upMB[mbx]

	// 预筛：predict16 只读块外邻居，连续调用互不影响。
	// Pre-filter: predict16 only reads outside the block, so the predictions
	// can be run back to back.
	var sad [4]int
	for k, mode := range y16Modes {
		d.leftMB, d.upMB[mbx] = savedLeft, savedUp
		d.predict16(checkTopLeftPred(mbx, mby, mode), bx, by)
		sad[k] = sadBlock16(d.y, e.srcY, bx, by)
	}

	// 选出绝对差最小的两个模式，顺序保留「先小后大」。
	// Pick the two cheapest modes, keeping the cheaper one first.
	first, second := 0, 1
	if sad[1] < sad[0] {
		first, second = 1, 0
	}
	for k := 2; k < len(sad); k++ {
		if sad[k] < sad[first] {
			second, first = first, k
		} else if sad[k] < sad[second] {
			second = k
		}
	}
	cand := [y16Candidates]int{first, second}

	bestCost := math.Inf(1)
	bestMode := uint8(predDC)
	for c := 0; c < y16Candidates; c++ {
		mode := y16Modes[cand[c]]
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
			e.candLastNZ = e.lastNZ
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
	e.lastNZ[24] = 0

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

// luma4Candidates 是 4x4 亮度块进入完整率失真评估的候选模式数。十种模式
// 先用预测残差的绝对值和排序，只有最便宜的这么多个会走完变换、量化、重建
// 与码率估计。取 4 是实测的平衡点：再少体积明显变大，再多则省不下时间。
//
// luma4Candidates is how many 4x4 luma modes reach the full rate-distortion
// evaluation. The ten modes are first ranked by the absolute error of their
// prediction and only the cheapest few are transformed, quantised,
// reconstructed and rate estimated. Four is the measured balance: fewer grows
// the output, more saves little time.
const luma4Candidates = 4

// luma4DecisiveNum/Den 与 luma4FlatSAD 共同构成「第一名已经赢定」的判据：
// 第二名的绝对差达到第一名的这么多倍，且第一名的绝对差本身已经很小（平均
// 每像素不超过两档）时，其余模式不可能再追回来，只评估第一名即可。
//
// 只看相对差距是不够的：随机噪声上各模式的绝对差差距同样很大，但那是误差
// 都很大的前提下的大差距，此时率失真的最优模式与绝对差的最小模式并不同一，
// 剪到单候选会让体积明显变大。加上绝对阈值后，只有「预测本来就准」的平坦块
// 与锐边块走单候选，纹理块仍然跑完整个候选表。
//
// luma4DecisiveNum/Den and luma4FlatSAD together form the "the winner is
// already decided" test: the runner-up's error is this many times the best
// one's and the best one's error is already small (two levels per pixel on
// average), so the remaining modes cannot catch up and only the best is
// evaluated.
//
// The relative gap alone is not enough: random noise also shows a wide gap
// between modes, but only because every mode errs badly there, and the
// rate-distortion optimum is not the minimum-error mode. Pruning to a single
// candidate visibly grows the output on such content. The absolute threshold
// keeps the single candidate for flat and hard-edged blocks, where the
// prediction is accurate to begin with, and lets textured blocks run the whole
// list.
const (
	luma4DecisiveNum = 13
	luma4DecisiveDen = 10
	luma4FlatSAD     = 32
)

// sadUnavailable 标记「这个模式没有参加预筛」。它必须同时满足两点：远大于
// 任何真实的绝对差之和（4x4 块最多 16 × 255 = 4080），并且乘以
// luma4DecisiveNum 之后仍在 int32 范围内——32 位平台上 int 就是 32 位，
// 用 math.MaxInt32 做哨兵会在 `sad[1] * 13` 处溢出，让判据得出错误结论。
//
// sadUnavailable marks "this mode did not take part in the pre-filter". It has
// to be far above any real sum of absolute differences (a 4x4 block tops out at
// 16 * 255 = 4080) and still fit in int32 after multiplying by
// luma4DecisiveNum: on 32 bit platforms int is 32 bits wide, and using
// math.MaxInt32 as the sentinel overflows at `sad[1] * 13` and makes the test
// draw the wrong conclusion.
const sadUnavailable = 1 << 20

// sadBlock4 返回 4x4 块上预测像素与原始像素的绝对差之和，用作模式预筛的
// 快速代价。它不做平方，也不看码率，只用来排序。
//
// sadBlock4 returns the sum of absolute differences between the prediction and
// the source over a 4x4 block, the cheap cost used to rank modes. It squares
// nothing and ignores the rate; it only orders candidates.
func sadBlock4(pred, src *plane, x, y int) int {
	s := 0
	pi := pred.at(x, y)
	si := src.at(x, y)
	for j := 0; j < 4; j++ {
		for i := 0; i < 4; i++ {
			d := int(pred.pix[pi+i]) - int(src.pix[si+i])
			if d < 0 {
				d = -d
			}
			s += d
		}
		pi += pred.stride
		si += src.stride
	}
	return s
}

// topLuma4Modes 返回 sad 里代价最低的 k 个模式下标，按代价升序排列。k 大于
// 元素数时返回全部。选择排序的常数小，十元素上比排序整个数组便宜。
//
// topLuma4Modes returns the indices of the k cheapest modes in sad, cheapest
// first. When k exceeds the number of entries everything is returned. Selection
// sort has a small constant and beats sorting all ten entries.
func topLuma4Modes(sad *[10]int, k int) [10]int {
	var idx [10]int
	for i := range idx {
		idx[i] = i
	}
	if k > len(sad) {
		k = len(sad)
	}
	for i := 0; i < k; i++ {
		min := i
		for j := i + 1; j < len(sad); j++ {
			if sad[idx[j]] < sad[idx[min]] {
				min = j
			}
		}
		idx[i], idx[min] = idx[min], idx[i]
	}
	return idx
}

// evalLuma4Block 为第 n 个 4x4 亮度块选代价最小的模式，返回代价、模式与该块
// 是否有非零系数。选中模式的系数与重建像素都留在原地。
//
// 十种模式全跑一遍完整评估是本编码器最贵的一步（约占编码总时间的七成），
// 而其中大多数模式的预测残差一开始就明显更大。这里先用绝对差之和排序，
// 只把前 luma4Candidates 个送进完整评估。
//
// evalLuma4Block picks the cheapest mode for the n-th 4x4 luma block and
// returns its cost, its mode and whether the block carries non-zero
// coefficients. The levels and the reconstructed pixels of the chosen mode stay
// in place.
//
// Evaluating all ten modes in full is the most expensive step of the encoder
// (about seventy percent of the encode time), while most of them already lose
// on the prediction residual alone. The modes are ranked by their sum of
// absolute differences and only the best luma4Candidates reach the full
// evaluation.
func (e *encoder) evalLuma4Block(n, x, y, ctx int, prob *[9]uint8, lambda float64) (float64, uint8, uint8) {
	d := &e.d
	// 每个预测器都写满块内十六个像素，所以候选之间不需要清零：predict4 的
	// 输出本来就覆盖上一轮留下的重建像素，addResidual4 与 ssePlane 读到的
	// 一定是本次预测的值。
	//
	// Every predictor writes all sixteen pixels of the block, so candidates need
	// no clearing: predict4's output already overwrites whatever reconstruction
	// the previous candidate left behind, and addResidual4 and ssePlane always
	// read this candidate's prediction.

	// 预筛：predict4 只读块外邻居，连续调用互不影响，因此可以直接在同一块
	// 上把十种预测都算一遍。
	//
	// 分成两级：先只算四个主模式（DC/TM/VE/HE）。平坦块与锐边块的最优模式
	// 几乎总在其中，且预测本身就准，算完这四个就能定下来；只有主模式都不准
	// （纹理块）才继续算六个方向模式。这样纹理块保留完整的候选表，而占多数
	// 的平坦块省掉六次预测与六次绝对差求和。
	//
	// Pre-filter: predict4 only reads outside the block, so the ten
	// predictions can be run back to back on the same block.
	//
	// It runs in two levels: the four primary modes first (DC/TM/VE/HE). Flat
	// and hard-edged blocks almost always win with one of them and their
	// prediction is already accurate, so those four settle it; only blocks
	// where even the primary modes miss (textured ones) go on to the six
	// directional modes. Textured blocks keep the full candidate list while the
	// majority of flat blocks save six predictions and six error sums.
	var sad [10]int
	bestSad := math.MaxInt32
	for k := 0; k < 4; k++ {
		d.predict4(y4Modes[k], x, y)
		sad[k] = sadBlock4(d.y, e.srcY, x, y)
		if sad[k] < bestSad {
			bestSad = sad[k]
		}
	}
	if bestSad <= luma4FlatSAD {
		for k := 4; k < len(sad); k++ {
			sad[k] = sadUnavailable
		}
	} else {
		for k := 4; k < len(sad); k++ {
			d.predict4(y4Modes[k], x, y)
			sad[k] = sadBlock4(d.y, e.srcY, x, y)
		}
	}
	cand := topLuma4Modes(&sad, luma4Candidates)
	// 自适应剪枝：预测本来就准、且第一名明显领先时，其余模式追不回来，只
	// 评估第一名；否则把候选表跑完。
	// Adaptive pruning: when the prediction is accurate to begin with and the
	// best mode clearly leads, the others cannot catch up and only the best is
	// evaluated; otherwise the whole candidate list runs.
	nCand := luma4Candidates
	if sad[cand[0]] <= luma4FlatSAD && sad[cand[1]]*luma4DecisiveNum >= sad[cand[0]]*luma4DecisiveDen {
		nCand = 1
	}

	var bestLevels [16]int32
	var bestPix [16]byte
	bestLastNZ := uint8(0)
	bestCost := math.Inf(1)
	bestMode := uint8(predDC)
	bestNZ := uint8(0)
	for c := 0; c < nCand; c++ {
		mode := y4Modes[cand[c]]
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
			bestLastNZ = e.lastNZ[n]
			copy(bestLevels[:], e.levels[16*n:16*n+16])
			for j := 0; j < 4; j++ {
				idx := d.y.at(x, y+j)
				copy(bestPix[j*4:j*4+4], d.y.pix[idx:idx+4])
			}
		}
	}
	copy(e.levels[16*n:16*n+16], bestLevels[:])
	e.lastNZ[n] = bestLastNZ
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
			copy(e.candLastNZ[16:24], e.lastNZ[16:24])
		}
	}
	d.leftMB, d.upMB[mbx] = savedLeft, savedUp
	copy(e.levels[256:384], e.candLevels[256:384])
	copy(e.lastNZ[16:24], e.candLastNZ[16:24])
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
	ai := a.at(x, y)
	bi := b.at(x, y)
	for j := 0; j < n; j++ {
		for i := 0; i < n; i++ {
			d := int(a.pix[ai+i]) - int(b.pix[bi+i])
			s += d * d
		}
		ai += a.stride
		bi += b.stride
	}
	return s
}
