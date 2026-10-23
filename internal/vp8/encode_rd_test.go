package vp8

import (
	"bytes"
	"image"
	"math"
	"testing"

	xwebp "golang.org/x/image/webp"
)

// TestEncodeUses4x4Modes 确认编码器真的会走 4x4 帧内模式那条路。
//
// TestEncodeUses4x4Modes checks that the encoder really takes the 4x4 intra
// mode path.
func TestEncodeUses4x4Modes(t *testing.T) {
	for _, q := range []int{40, 60, 80, 95} {
		src := testImage(96, 72)
		e := newEncoder(96, 72, src, q)
		if _, err := e.run(); err != nil {
			t.Fatalf("q=%d: run: %v", q, err)
		}
		if e.y4Blocks == 0 {
			t.Fatalf("q=%d: no macroblock used the 4x4 mode path", q)
		}
		t.Logf("q=%d: %d macroblocks took the 4x4 mode path over both passes",
			q, e.y4Blocks)
	}
}

// TestEncode4x4IndependentDecoder 用 x/image/webp 解一份走了 4x4 模式的码流，
// 逐像素比对本库解码器的结果。4x4 模式的二叉树、上下文与非零标记的传播都在
// 这里被独立实现验证。
//
// TestEncode4x4IndependentDecoder decodes a stream that took the 4x4 mode path
// with x/image/webp and compares it pixel by pixel against this package's
// decoder. An independent implementation confirms the mode tree, the contexts
// and the propagation of the non-zero flags.
func TestEncode4x4IndependentDecoder(t *testing.T) {
	for _, tc := range []struct {
		w, h    int
		quality int
	}{
		{96, 72, 40}, {96, 72, 75}, {96, 72, 95},
		{64, 64, 60}, {48, 33, 85}, {129, 97, 70},
	} {
		src := testImage(tc.w, tc.h)
		e := newEncoder(tc.w, tc.h, src, tc.quality)
		data, err := e.run()
		if err != nil {
			t.Fatalf("%dx%d q=%d: run: %v", tc.w, tc.h, tc.quality, err)
		}
		if e.y4Blocks == 0 {
			t.Fatalf("%dx%d q=%d: the stream did not use the 4x4 mode path", tc.w, tc.h, tc.quality)
		}
		ours, err := Decode(data)
		if err != nil {
			t.Fatalf("%dx%d q=%d: decode: %v", tc.w, tc.h, tc.quality, err)
		}
		img, err := xwebp.Decode(bytes.NewReader(wrapWebP(data)))
		if err != nil {
			t.Fatalf("%dx%d q=%d: x/image/webp decode: %v", tc.w, tc.h, tc.quality, err)
		}
		ref, ok := img.(*image.YCbCr)
		if !ok {
			t.Fatalf("%dx%d q=%d: x/image/webp returned %T", tc.w, tc.h, tc.quality, img)
		}
		for y := 0; y < ours.Rect.Dy(); y++ {
			for x := 0; x < ours.Rect.Dx(); x++ {
				if a, b := ours.Y[y*ours.YStride+x], ref.Y[y*ref.YStride+x]; a != b {
					t.Fatalf("%dx%d q=%d: luma mismatch at (%d,%d): %d != %d",
						tc.w, tc.h, tc.quality, x, y, a, b)
				}
			}
		}
		cw, ch := (ours.Rect.Dx()+1)/2, (ours.Rect.Dy()+1)/2
		for y := 0; y < ch; y++ {
			for x := 0; x < cw; x++ {
				if ours.Cb[y*ours.CStride+x] != ref.Cb[y*ref.CStride+x] ||
					ours.Cr[y*ours.CStride+x] != ref.Cr[y*ref.CStride+x] {
					t.Fatalf("%dx%d q=%d: chroma mismatch at (%d,%d)",
						tc.w, tc.h, tc.quality, x, y)
				}
			}
		}
	}
}

// TestRDPicksCheapestY16Mode 确认率失真决策给出的代价就是四个候选模式里最小
// 的那个：测试自己把四个模式各算一遍，再与编码器给出的代价比较。
//
// TestRDPicksCheapestY16Mode checks that the cost the rate distortion decision
// reports is the smallest of the four candidate modes: the test computes the
// four costs itself and compares them with the one the encoder returns.
func TestRDPicksCheapestY16Mode(t *testing.T) {
	src := testImage(64, 64)
	e := newEncoder(64, 64, src, 75)
	d := &e.d
	lambda := e.rdLambda()
	savedLeft, savedUp := d.leftMB, d.upMB[0]

	best := math.Inf(1)
	for _, m := range y16Modes {
		d.leftMB, d.upMB[0] = savedLeft, savedUp
		d.predict16(checkTopLeftPred(0, 0, m), 0, 0)
		e.transformLuma16(0, 0)
		e.loadCoeffsLuma16()
		e.setMasksLuma16()
		e.reconstructLuma16(0, 0)
		dist := ssePlane(d.y, e.srcY, 0, 0, 16)
		rate := e.estimateLuma16Tokens(0) + e.estimateY16Mode(m)
		cost := float64(dist) + lambda*rate
		if cost < best {
			best = cost
		}
	}
	d.leftMB, d.upMB[0] = savedLeft, savedUp

	cost, mode := e.evalLuma16(0, 0, 0, 0, lambda)
	if cost != best {
		t.Fatalf("the encoder reports a cost of %.3f, the cheapest candidate costs %.3f", cost, best)
	}
	t.Logf("cheapest 16x16 mode: %d, cost %.3f", mode, cost)
}

// TestBitCostTable 确认代价表里的每个值就是对应概率下的理论比特数。
//
// TestBitCostTable checks that every entry of the cost table is the number of
// bits the matching probability implies.
func TestBitCostTable(t *testing.T) {
	for p := 1; p < 256; p++ {
		prob := float64(p) / 256
		if got, want := bitCostTable[0][p], -math.Log2(prob); math.Abs(got-want) > 1e-12 {
			t.Fatalf("zero at %d: got %v, want %v", p, got, want)
		}
		if got, want := bitCostTable[1][p], -math.Log2(1-prob); math.Abs(got-want) > 1e-12 {
			t.Fatalf("one at %d: got %v, want %v", p, got, want)
		}
	}
}
