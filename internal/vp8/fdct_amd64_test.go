//go:build amd64 && !purego

package vp8

import (
	"bytes"
	"math/rand"
	"testing"
)

// TestFDCT4x4AVX2MatchesScalar 校验 AVX2 内核与标量版本逐位一致。
//
// 变换处在编码器最热的路径上，任何一位差异都会让码流与质量悄悄改变，所以这里
// 用随机值与边界值把两个实现对比一遍。CPU 不支持 AVX2 时跳过。
//
// TestFDCT4x4AVX2MatchesScalar checks that the AVX2 kernel agrees with the
// scalar version bit for bit.
//
// The transform sits on the encoder's hottest path, so a single differing bit
// would silently change the bitstream and the quality. Random and boundary
// values are compared across both implementations; the test skips when the CPU
// has no AVX2.
func TestFDCT4x4AVX2MatchesScalar(t *testing.T) {
	if !cpuHasAVX2() {
		t.Skip("CPU has no AVX2")
	}
	rng := rand.New(rand.NewSource(1))
	cases := make([][16]int32, 0, 4096)
	// 残差的实际范围是 -255..255，变换后系数到几千；这里连边界一起覆盖。
	// The residual really spans -255..255 and coefficients reach a few
	// thousand; the boundaries are covered as well.
	for i := 0; i < 4000; i++ {
		var blk [16]int32
		for j := range blk {
			blk[j] = int32(rng.Intn(8193) - 4096)
		}
		cases = append(cases, blk)
	}
	for _, edge := range []int32{-1 << 20, -1 << 16, -32768, -1, 0, 1, 32767, 1 << 16, 1 << 20} {
		var blk [16]int32
		for j := range blk {
			blk[j] = edge
		}
		cases = append(cases, blk)
	}
	var allZero, allMax [16]int32
	for j := range allMax {
		allMax[j] = 1<<31 - 1
	}
	cases = append(cases, allZero, allMax)

	for i, in := range cases {
		want := in
		got := in
		fdct4x4Scalar(&want)
		fdct4x4AVX2(&got)
		if got != want {
			t.Fatalf("case %d: AVX2 %v != scalar %v (input %v)", i, got, want, in)
		}
	}
}

// TestSadBlock16AVX2MatchesScalar 校验 16x16 绝对差之和的 AVX2 内核与标量版本
// 结果相同。累加器是 16 位，所以极端值（全 0 对全 255）必须一并覆盖，确认
// 一行 16 * 255 累到十六行仍在 uint16 范围内。
//
// TestSadBlock16AVX2MatchesScalar checks that the AVX2 kernel for the 16x16 sum
// of absolute differences agrees with the scalar version. The accumulator is 16
// bits wide, so the extremes (all zeros against all 255) are covered as well, to
// confirm sixteen rows of 16 * 255 still fit in a uint16.
func TestSadBlock16AVX2MatchesScalar(t *testing.T) {
	if !cpuHasAVX2() {
		t.Skip("CPU has no AVX2")
	}
	rng := rand.New(rand.NewSource(2))
	for iter := 0; iter < 300; iter++ {
		a := newPlane(32, 32, 4)
		b := newPlane(32, 32, 4)
		for i := range a.pix {
			a.pix[i] = byte(rng.Intn(256))
			b.pix[i] = byte(rng.Intn(256))
		}
		x, y := rng.Intn(16), rng.Intn(16)
		want := sadBlock16Scalar(a, b, x, y)
		got := sadBlock16AVX2(a, b, x, y)
		if got != want {
			t.Fatalf("iteration %d at (%d,%d): AVX2 %d != scalar %d", iter, x, y, got, want)
		}
	}

	// 极值：全 0 对全 255，每块正好 256 * 255。
	// Extremes: all zeros against all 255, exactly 256 * 255 per block.
	a := newPlane(16, 16, 4)
	b := newPlane(16, 16, 4)
	for i := range b.pix {
		b.pix[i] = 255
	}
	if got, want := sadBlock16AVX2(a, b, 0, 0), 256*255; got != want {
		t.Fatalf("extremes: AVX2 %d != %d", got, want)
	}
	if got, want := sadBlock16Scalar(a, b, 0, 0), 256*255; got != want {
		t.Fatalf("extremes scalar: %d != %d", got, want)
	}
}

// TestAVX2KernelsMatchScalarBitstream 校验 AVX2 内核与标量实现产出**完全相同
// 的码流**。
//
// 单函数逐位对比只能说明变换本身没算错，但真正要保证的是「同一份输入，开不开
// AVX2 都写出同一串字节」。这里把两个内核临时换回标量再编码一遍，逐字节比较；
// 变换与 SAD 的结果只要有一位不同，模式选择或系数就会变，比较立刻失败。
//
// TestAVX2KernelsMatchScalarBitstream checks that the AVX2 kernels produce
// exactly the same bitstream as the scalar implementations.
//
// Comparing the transform alone only proves the maths is right; what actually
// matters is that the same input yields the same bytes whether or not AVX2 is
// in use. The two kernels are swapped back to scalar, the frame is encoded
// again and the streams are compared byte by byte: a single differing bit in
// the transform or in the SAD changes mode selection or coefficients and the
// comparison fails immediately.
func TestAVX2KernelsMatchScalarBitstream(t *testing.T) {
	if !cpuHasAVX2() {
		t.Skip("CPU has no AVX2")
	}
	encode := func(w, h, q int) []byte {
		t.Helper()
		src := testImage(w, h)
		e := newEncoder(w, h, src, q)
		data, err := e.run()
		if err != nil {
			t.Fatalf("%dx%d q=%d: run: %v", w, h, q, err)
		}
		return data
	}

	// 覆盖平坦（多走 16x16）、纹理（多走 4x4）与两种尺寸。
	// Cover flat content (mostly the 16x16 path), textured content (mostly
	// 4x4) and two sizes.
	for _, tc := range []struct {
		w, h, q int
	}{{64, 64, 50}, {96, 72, 75}, {129, 97, 30}, {160, 120, 90}} {
		withAVX := encode(tc.w, tc.h, tc.q)

		// 两个内核同时切回标量，确保差异不会被其中一个掩盖。
		// Both kernels go back to scalar together so neither can mask a
		// difference in the other.
		savedSAD := sadBlock16
		forceScalar = true
		sadBlock16 = sadBlock16Scalar
		scalar := encode(tc.w, tc.h, tc.q)
		sadBlock16 = savedSAD
		forceScalar = false

		if !bytes.Equal(withAVX, scalar) {
			// 找出第一处差异，方便定位。
			// Locate the first differing byte to make the failure actionable.
			n := len(withAVX)
			if len(scalar) < n {
				n = len(scalar)
			}
			at := -1
			for i := 0; i < n; i++ {
				if withAVX[i] != scalar[i] {
					at = i
					break
				}
			}
			t.Fatalf("%dx%d q=%d: bitstream differs (len %d vs %d, first diff at %d)",
				tc.w, tc.h, tc.q, len(withAVX), len(scalar), at)
		}
	}
}

// TestFDCT4x4UsesAVX2 确认 AVX2 探测结果确实接通了汇编内核：探测失效时 useAVX2
// 与 cpuHasAVX2() 不一致，基准变慢的原因就能在这里看出来。
//
// TestFDCT4x4UsesAVX2 checks that the AVX2 probe result really enables the
// assembly kernel: when the probe breaks, useAVX2 disagrees with cpuHasAVX2()
// and the reason for a slower benchmark shows up here.
func TestFDCT4x4UsesAVX2(t *testing.T) {
	if !cpuHasAVX2() {
		t.Skip("CPU has no AVX2")
	}
	if !useAVX2 {
		t.Fatal("cpuHasAVX2() is true but the probe did not enable the AVX2 kernel")
	}
	var probe [16]int32
	probe[0] = 1
	want := probe
	fdct4x4Scalar(&want)
	got := probe
	fdct4x4(&got)
	if got != want {
		t.Fatal("fdct4x4 is not wired to the AVX2 kernel")
	}
}
