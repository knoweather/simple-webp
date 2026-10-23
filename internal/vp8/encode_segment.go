package vp8

import (
	"image"
	"sort"
)

// segPlan 是逐段量化方案：每个宏块分到哪一段、逐段的量化索引偏移，以及
// segment ID 解码树的三个概率。enabled 为 false 时帧头不写分段，行为与
// 单一全局量化完全一致。
//
// segPlan is the per-segment quantization plan: which segment every macroblock
// belongs to, the quantizer index offset of every segment and the three
// probabilities of the segment ID decode tree. When enabled is false the frame
// header carries no segmentation and the frame is coded with one global
// quantizer, exactly as before.
type segPlan struct {
	enabled bool
	// segOfMB 按光栅顺序存放每个宏块的段号，长度 mbw*mbh。
	//
	// segOfMB holds the segment of every macroblock in raster order, mbw*mbh
	// entries long.
	segOfMB []uint8
	// delta 是逐段量化索引偏移（相对基础索引），规范里是 7 位有符号数。
	// 方案只粗化平坦侧：段 0/1 保持基础量化，段 2 粗一档、段 3 粗两档。
	// 繁忙区域一步不粗化，质量不受影响；体积收益全部来自平坦区域。
	//
	// delta is the per-segment quantizer index offset relative to the base
	// index, a 7 bit signed value in the bitstream. The plan coarsens the flat
	// side only: segments 0 and 1 keep the base quantizer, segment 2 is one
	// step coarser and segment 3 two steps. Busy regions are never coarsened,
	// so their quality is untouched and the size gain comes from the flat
	// regions alone.
	delta [nSegment]int8
	// prob 是 segment ID 解码树的三个概率；0xff 表示取规范默认值。
	//
	// prob holds the three probabilities of the segment ID decode tree; 0xff
	// means the default value of the specification.
	prob [nSegmentProb]uint8
}

// segmentDelta 由基础量化索引推导逐段偏移的步长 d：段 2 取 +d、段 3 取
// +2d，段 0/1 保持基础量化。质量越高（索引越小）步长越小；索引接近上限时
// 收缩步长，避免 +2d 被钳掉后分段失去意义。
//
// segmentDelta derives the step d of the per-segment offsets from the base
// quantizer index: segment 2 takes +d and segment 3 +2d, while segments 0 and
// 1 keep the base quantizer. A higher quality (a smaller index) gets a smaller
// step; near the top of the index range the step shrinks so that +2d is not
// clipped away entirely.
func segmentDelta(qi int) int {
	d := 3 + qi/32
	if qi+2*d > 127 {
		d = (127 - qi) / 2
	}
	if d < 0 {
		d = 0
	}
	return d
}

// planSegments 按宏块活动度制订逐段量化方案：活动度高的宏块分到保持基础
// 量化的段（0/1），平坦的分到粗量化段（+d、+2d）。活动度取 16x16 亮度块内
// 水平与垂直方向相邻像素绝对差之和。分段阈值取活动度分布的四分位数，
// 因此各段宏块数大致均衡；segment ID 树的概率按各段实际占比设置，
// 使段号的编码代价接近其信息熵。
//
// planSegments derives the per-segment plan from the macroblock activity:
// busy macroblocks land in the segments that keep the base quantizer (0 and 1)
// and flat ones in the coarse segments (+d and +2d). The activity is the sum
// of the absolute differences of horizontally and vertically adjacent luma
// pixels inside a 16x16 block. The thresholds are the quartiles of the
// activity distribution, so the segments hold roughly equal macroblock counts;
// the segment ID tree probabilities follow the actual segment shares, which
// prices a segment ID close to its entropy.
func planSegments(src *image.YCbCr, mbw, mbh, qi int) segPlan {
	n := mbw * mbh
	if n == 0 {
		return segPlan{}
	}
	d := segmentDelta(qi)
	if d == 0 {
		// 最高质量附近没有可用的偏移空间，退回单一量化。
		// At the top of the quality range there is no room for offsets, so the
		// plan falls back to a single quantizer.
		return segPlan{}
	}

	// 活动度：从源图直接统计，不依赖编码结果，因此两遍编码之间保持稳定。
	// 取 16x16 块内水平与垂直相邻像素绝对差之和；每块像素数相同，所以用和
	// 而不是均值不影响四分位阈值。
	// The activity comes straight from the source, not from any encode, so it
	// stays stable across the two encoding passes. It is the sum of the
	// absolute differences of horizontally and vertically adjacent luma pixels
	// inside a 16x16 block; every block covers the same pixel count, so the
	// sum and the mean give the same quartile thresholds.
	act := make([]int, n)
	dw, dh := src.Rect.Dx(), src.Rect.Dy()
	for mby := 0; mby < mbh; mby++ {
		for mbx := 0; mbx < mbw; mbx++ {
			var sum int
			for y := 0; y < 16; y++ {
				sy := mby*16 + y
				if sy >= dh {
					sy = dh - 1
				}
				row := (src.Rect.Min.Y+sy)*src.YStride + src.Rect.Min.X
				prevRow := row
				if y > 0 {
					// y>0 时 sy>=1，所以上一行总是存在。
					// With y>0, sy>=1, so the row above always exists.
					prevRow = (src.Rect.Min.Y+sy-1)*src.YStride + src.Rect.Min.X
				}
				for x := 0; x < 16; x++ {
					sx := mbx*16 + x
					if sx >= dw {
						sx = dw - 1
					}
					cur := int(src.Y[row+sx])
					if x > 0 {
						sum += absInt(cur - int(src.Y[row+sx-1]))
					}
					if y > 0 {
						sum += absInt(cur - int(src.Y[prevRow+sx]))
					}
				}
			}
			act[mby*mbw+mbx] = sum
		}
	}

	// 四分位阈值 / the quartile thresholds
	sorted := make([]int, n)
	copy(sorted, act)
	sort.Ints(sorted)
	t1, t2, t3 := sorted[n/4], sorted[n/2], sorted[(3*n)/4]

	plan := segPlan{enabled: true, segOfMB: make([]uint8, n)}
	var count [nSegment]int
	for i, a := range act {
		var seg uint8
		switch {
		case a > t3:
			seg = 0 // 最活跃 / busiest
		case a > t2:
			seg = 1
		case a > t1:
			seg = 2
		default:
			seg = 3 // 最平坦 / flattest
		}
		plan.segOfMB[i] = seg
		count[seg]++
	}
	// 只粗化平坦侧：段 0/1 保持基础量化，段 2 粗一档，段 3 粗两档。
	// Only the flat side is coarsened: segments 0 and 1 keep the base
	// quantizer, segment 2 is one step coarser and segment 3 two steps.
	plan.delta = [nSegment]int8{0, 0, int8(d), int8(2 * d)}

	// 树概率按各段占比折算成「取零概率」。分支从未走到时取 255，让常见分支
	// 几乎不花比特、罕见分支贵得走不到。
	// The tree probabilities follow the segment shares as probabilities of a
	// zero. A branch that is never taken gets 255, which makes the common
	// branch nearly free and the rare one too expensive to ever take.
	n01, n23 := count[0]+count[1], count[2]+count[3]
	switch {
	case n01 == 0:
		// 段 0/1 从未用到：第一位恒真，第二位不会出现。
		// Segments 0 and 1 are never used: the first bit is always true and
		// the second one never appears.
		plan.prob[0], plan.prob[1] = 1, 0xff
		plan.prob[2] = clampProb(255 * count[2] / n23)
	case n23 == 0:
		plan.prob[0], plan.prob[1] = 255, clampProb(255*count[0]/n01)
		plan.prob[2] = 0xff
	default:
		plan.prob[0] = clampProb(255 * n01 / n)
		plan.prob[1] = clampProb(255 * count[0] / n01)
		plan.prob[2] = clampProb(255 * count[2] / n23)
	}
	return plan
}

// clampProb 把概率钳到 1..255：0 会让对应分支永远不可能取零，钳到 1 保持
// 「几乎不可能」的语义。
//
// clampProb clamps a probability into 1..255: zero would make the matching
// branch impossible, so one keeps the "almost impossible" meaning.
func clampProb(v int) uint8 {
	if v < 1 {
		return 1
	}
	if v > 255 {
		return 255
	}
	return uint8(v)
}

// setupSegmentQuant 按基础量化索引与逐段偏移填充解码器的四张反量化表，
// 推导方式与解码端 parseQuant 的相对增量模式完全一致（含 y2 AC 的
// 155/100 缩放、下限 8，以及色度 DC 的 117 钳位）。
//
// setupSegmentQuant fills the four dequantization tables of the decoder from
// the base quantizer index and the per-segment offsets, deriving them exactly
// as the decoder's parseQuant does in relative delta mode (including the
// 155/100 scaling and the minimum of 8 for the y2 AC factor and the 117 clamp
// of the chroma DC factor).
func setupSegmentQuant(d *decoder, baseQI int, delta [nSegment]int8) {
	for i := 0; i < nSegment; i++ {
		q := clipInt(baseQI+int(delta[i]), 0, 127)
		d.q[i].y1[0] = dequantTableDC[q]
		d.q[i].y1[1] = dequantTableAC[q]
		d.q[i].y2[0] = dequantTableDC[q] * 2
		d.q[i].y2[1] = dequantTableAC[q] * 155 / 100
		if d.q[i].y2[1] < 8 {
			d.q[i].y2[1] = 8
		}
		d.q[i].uv[0] = dequantTableDC[clipInt(q, 0, 117)]
		d.q[i].uv[1] = dequantTableAC[q]
	}
}

// writeSegmentID 把一个宏块的段号写进首分区，树形结构与解码端 reconstruct
// 的读取一一对应：第一位为假时由第二位在段 0/1 间抉择，为真时由第三位在
// 段 2/3 间抉择。概率取本帧方案里的值。
//
// writeSegmentID emits the segment ID of one macroblock into the first
// partition, the tree mirroring what the decoder's reconstruct reads: a false
// first bit lets the second bit choose between segments 0 and 1, a true one
// lets the third bit choose between 2 and 3. The probabilities are the ones of
// this frame's plan.
func (e *encoder) writeSegmentID(seg int) {
	h := &e.hdr
	if seg < 2 {
		h.putBit(e.seg.prob[0], false)
		h.putBit(e.seg.prob[1], seg == 1)
		return
	}
	h.putBit(e.seg.prob[0], true)
	h.putBit(e.seg.prob[2], seg == 2)
}
