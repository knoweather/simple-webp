package vp8

import (
	"errors"
	"image"
	"testing"
)

// ycbcrFromFunc 用给定函数逐像素生成 4:2:0 YCbCr 图，供分段方案测试使用。
//
// ycbcrFromFunc builds a 4:2:0 YCbCr image from a per-pixel function, for the
// segment plan tests.
func ycbcrFromFunc(w, h int, f func(x, y int) (y8, cb, cr uint8)) *image.YCbCr {
	img := image.NewYCbCr(image.Rect(0, 0, w, h), image.YCbCrSubsampleRatio420)
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			yy, cb, cr := f(x, y)
			img.Y[y*img.YStride+x] = yy
			img.Cb[(y/2)*img.CStride+x/2] = cb
			img.Cr[(y/2)*img.CStride+x/2] = cr
		}
	}
	return img
}

// TestSegmentPlanUniform 均匀图的全部宏块都应落进最平坦那段（偏移 +2d），
// 树概率让常见分支几乎不花比特。
//
// TestSegmentPlanUniform checks that every macroblock of a uniform image lands
// in the flattest segment (offset +2d) and that the tree probabilities make
// the common branch nearly free.
func TestSegmentPlanUniform(t *testing.T) {
	uniform := ycbcrFromFunc(64, 64, func(x, y int) (uint8, uint8, uint8) { return 128, 128, 128 })
	plan := planSegments(uniform, 4, 4, 40)
	if !plan.enabled {
		t.Fatal("the plan is not enabled")
	}
	for i, seg := range plan.segOfMB {
		if seg != 3 {
			t.Fatalf("macroblock %d landed in segment %d, want the flattest 3", i, seg)
		}
	}
	d := segmentDelta(40)
	if plan.delta != [nSegment]int8{0, 0, int8(d), int8(2 * d)} {
		t.Fatalf("deltas %v, want 0/0/+%d/+2*d", plan.delta, d)
	}
	// 全帧同一段：第一位恒为真，第二位恒为假。
	// One segment for the whole frame: the first bit is always true and the
	// second always false.
	if plan.prob[0] != 1 || plan.prob[2] != 1 {
		t.Fatalf("tree probabilities %v, want the common branch cheap", plan.prob)
	}
}

// TestSegmentPlanSpreads 混合内容必须用到多个段，段号数与树概率自洽，
// 且最活跃的段拿最细的量化。
//
// TestSegmentPlanSpreads checks that mixed content uses several segments, that
// the segment counts and the tree probabilities agree, and that the busiest
// segment gets the finest quantizer.
func TestSegmentPlanSpreads(t *testing.T) {
	mixed := ycbcrFromFunc(128, 64, func(x, y int) (uint8, uint8, uint8) {
		if x < 64 {
			return uint8(x * 2), 128, 128 // 左半平滑 / smooth left half
		}
		return uint8(x*y) ^ uint8(x+y), uint8(x * 3), uint8(y * 5) // 右半噪声 / noisy right half
	})
	mbw, mbh := 8, 4
	plan := planSegments(mixed, mbw, mbh, 40)
	if len(plan.segOfMB) != mbw*mbh {
		t.Fatalf("segOfMB has %d entries, want %d", len(plan.segOfMB), mbw*mbh)
	}
	var count [nSegment]int
	for _, seg := range plan.segOfMB {
		if int(seg) >= nSegment {
			t.Fatalf("segment %d out of range", seg)
		}
		count[seg]++
	}
	used := 0
	for _, c := range count {
		if c > 0 {
			used++
		}
	}
	if used < 2 {
		t.Fatalf("only %d segments used, the plan should spread over mixed content", used)
	}
	// 左半（平滑）整体比右半（噪声）平坦：左半的宏块不应落进繁忙段 0/1。
	// The smooth left half is flatter than the noisy right half overall: its
	// macroblocks must not land in the busy segments 0 and 1.
	for mby := 0; mby < mbh; mby++ {
		for mbx := 0; mbx < 4; mbx++ {
			if plan.segOfMB[mby*mbw+mbx] < 2 {
				t.Fatalf("macroblock %d,%d of the smooth half landed in busy segment %d", mbx, mby, plan.segOfMB[mby*mbw+mbx])
			}
		}
	}
	// 繁忙段必须被噪声半用上 / the busy segments have to be used by the noise
	busy := 0
	for mby := 0; mby < mbh; mby++ {
		for mbx := 4; mbx < mbw; mbx++ {
			if plan.segOfMB[mby*mbw+mbx] < 2 {
				busy++
			}
		}
	}
	if busy == 0 {
		t.Fatal("no macroblock of the noisy half landed in a busy segment")
	}
	// 树概率与占比一致：prob[0] ≈ 255*(n0+n1)/N。
	// The tree probabilities follow the shares: prob[0] ~ 255*(n0+n1)/N.
	want0 := 255 * (count[0] + count[1]) / (mbw * mbh)
	if diff := int(plan.prob[0]) - want0; diff > 1 || diff < -1 {
		t.Fatalf("prob[0] = %d, want about %d", plan.prob[0], want0)
	}
	for i, p := range plan.prob {
		if p < 1 || p > 255 {
			t.Fatalf("prob[%d] = %d out of 1..255", i, p)
		}
	}
}

// encodeSegmentedForTest 强制走分段路径编码一遍（绕过「只保留更小」的
// 择优），供帧头字段与量化表演算的测试使用。
//
// encodeSegmentedForTest encodes one frame through the segmented path
// directly, bypassing the "keep the smaller" race, for the tests that inspect
// the frame header fields and the quantizer tables.
func encodeSegmentedForTest(t *testing.T, src *image.YCbCr, quality int) []byte {
	t.Helper()
	e := newEncoder(src.Rect.Dx(), src.Rect.Dy(), src, quality)
	e.filterLevel = autoFilterLevel(quality)
	e.seg = planSegments(src, e.d.mbw, e.d.mbh, e.qi)
	if !e.seg.enabled {
		t.Fatalf("q=%d: the plan refused to segment", quality)
	}
	setupSegmentQuant(&e.d, e.qi, e.seg.delta)
	data, err := e.run()
	if err != nil {
		t.Fatalf("q=%d: run: %v", quality, err)
	}
	return data
}

// TestSegmentSetupQuantMatchesParseQuant 校验编码端的分段量化表与解码端
// parseQuant 的相对增量推导一致：把带分段的码流解出的表应与
// setupSegmentQuant 的结果逐项相等。
//
// TestSegmentSetupQuantMatchesParseQuant checks that the encoder's per-segment
// quantizer tables agree with the relative delta derivation of the decoder's
// parseQuant: the tables parsed out of a segmented stream must equal what
// setupSegmentQuant produces.
func TestSegmentSetupQuantMatchesParseQuant(t *testing.T) {
	mixed := ycbcrFromFunc(96, 64, func(x, y int) (uint8, uint8, uint8) {
		if x < 48 {
			return uint8(x * 2), 128, 128
		}
		return uint8(x*y) ^ uint8(x+y), uint8(x * 3), uint8(y * 5)
	})
	qi := quantIndex(60)
	plan := planSegments(mixed, 6, 4, qi)

	data := encodeSegmentedForTest(t, mixed, 60)
	var d decoder
	if err := d.parseHeaders(data); err != nil {
		t.Fatalf("parse headers: %v", err)
	}
	if !d.seg.useSegment {
		t.Fatal("the stream carries no segmentation")
	}
	if !d.seg.updateMap {
		t.Fatal("the segment map is not updated")
	}
	for i := range plan.delta {
		if d.seg.quantizer[i] != plan.delta[i] {
			t.Fatalf("segment %d: parsed delta %d, want %d", i, d.seg.quantizer[i], plan.delta[i])
		}
	}
	for i := range plan.prob {
		if d.seg.prob[i] != plan.prob[i] {
			t.Fatalf("tree prob %d: parsed %d, want %d", i, d.seg.prob[i], plan.prob[i])
		}
	}
	// 逐段反量化表与 setupSegmentQuant 的推导一致。
	// The per-segment dequantization tables match setupSegmentQuant.
	var want decoder
	setupSegmentQuant(&want, qi, plan.delta)
	for i := 0; i < nSegment; i++ {
		if d.q[i] != want.q[i] {
			t.Fatalf("segment %d: parsed quant %+v, want %+v", i, d.q[i], want.q[i])
		}
	}
}

// TestSegmentRoundTripPixels 逐段量化的码流必须解回与码流声明一致的像素：
// 与本库解码器往返，且与参考实现（golang.org/x/image 由根包测试覆盖）
// 结构一致——这里验证帧头字段、段号分布与量化表自洽后解出的图没有损坏。
//
// TestSegmentRoundTripPixels checks that a segmented stream decodes back to
// sound pixels: the frame header fields, the segment distribution and the
// quantizer tables have to be self-consistent, which a wrong segment ID or
// offset would immediately break.
func TestSegmentRoundTripPixels(t *testing.T) {
	mixed := ycbcrFromFunc(128, 96, func(x, y int) (uint8, uint8, uint8) {
		if x < 64 {
			return uint8(x * 2), 128, 128
		}
		return uint8(x*y) ^ uint8(x+y), uint8(x * 3), uint8(y * 5)
	})
	for _, q := range []int{20, 50, 80, 98} {
		data := encodeSegmentedForTest(t, mixed, q)
		got, err := Decode(data)
		if err != nil {
			t.Fatalf("q=%d: decode: %v", q, err)
		}
		if got.Rect.Dx() != 128 || got.Rect.Dy() != 96 {
			t.Fatalf("q=%d: size %v", q, got.Rect)
		}
		if p := psnr(mixed, got); p < 25 {
			t.Fatalf("q=%d: round trip psnr %.1f dB below 25", q, p)
		}
	}
}

// TestCheckFirstPartition 校验：首分区长度超出帧标签的 19 位字段时报错，
// 边界值放行。
//
// TestCheckFirstPartition checks that a first-partition length above the 19
// bit field of the frame tag is an error, the boundary value passes.
func TestCheckFirstPartition(t *testing.T) {
	if err := checkFirstPartition(0); err != nil {
		t.Fatalf("empty first partition: %v", err)
	}
	if err := checkFirstPartition(maxFirstPartition); err != nil {
		t.Fatalf("boundary length %d: %v", maxFirstPartition, err)
	}
	err := checkFirstPartition(maxFirstPartition + 1)
	if err == nil {
		t.Fatalf("length %d accepted", maxFirstPartition+1)
	}
	if !errors.Is(err, ErrTooLarge) {
		t.Fatalf("error %v does not wrap ErrTooLarge", err)
	}
}
