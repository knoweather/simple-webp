//go:build amd64 && !purego

package vp8

// useAVX2 在包初始化时探测一次，之后每次变换只读这个布尔值。
//
// useAVX2 is probed once at package initialisation; every transform then just
// reads the boolean.
var useAVX2 = cpuHasAVX2()

// forceScalar 强制走标量内核，仅供测试使用：它让「AVX2 与标量产出同一串
// 字节」的测试能在同一个二进制里切换两种实现。
//
// forceScalar forces the scalar kernel and exists for the tests only: it lets
// the "AVX2 and scalar produce the same bytes" test switch between the two
// implementations inside one binary.
var forceScalar bool

// fdct4x4 在支持 AVX2 的机器上走汇编内核，否则走标量版本。两者逐位一致。
//
// fdct4x4 uses the assembly kernel where AVX2 is available and the scalar
// version otherwise. The two agree bit for bit.
func fdct4x4(blk *[16]int32) {
	if useAVX2 && !forceScalar {
		fdct4x4AVX2(blk)
		return
	}
	fdct4x4Scalar(blk)
}

// init 在支持 AVX2 的机器上把 16x16 的 SAD 换成汇编内核。检测在包初始化时做
// 一次，之后每次调用只是一次函数指针调用，不在热路径上重复判断 CPU 特性。
//
// init swaps in the assembly kernel for the 16x16 SAD on machines that support
// AVX2. The probe runs once at package initialisation, so every call is just an
// indirect call and the hot path never re-tests the CPU features.
func init() {
	if useAVX2 {
		sadBlock16 = sadBlock16AVX2
	}
}

// sadBlock16AVX2 把 16x16 块的绝对差之和交给汇编内核。
//
// sadBlock16AVX2 hands the 16x16 sum of absolute differences to the assembly
// kernel.
func sadBlock16AVX2(pred, src *plane, x, y int) int {
	return sad16AVX2(&pred.pix[pred.at(x, y)], &src.pix[src.at(x, y)], pred.stride, src.stride)
}

// cpuHasAVX2 用 CPUID 与 XGETBV 判断 AVX2 是否可用；实现在 fdct_amd64.s。
//
// cpuHasAVX2 decides with CPUID and XGETBV whether AVX2 is usable; the
// implementation lives in fdct_amd64.s.
//
//go:noescape
func cpuHasAVX2() bool

// fdct4x4AVX2 是 4x4 正向变换的 AVX2 内核，与 fdct4x4Scalar 逐位一致。
//
// fdct4x4AVX2 is the AVX2 kernel of the 4x4 forward transform, bit-for-bit
// identical to fdct4x4Scalar.
//
//go:noescape
func fdct4x4AVX2(blk *[16]int32)

// sad16AVX2 是 16x16 块绝对差之和的 AVX2 内核，与标量版本结果相同。
//
// sad16AVX2 is the AVX2 kernel for the sum of absolute differences over a
// 16x16 block; it agrees with the scalar version.
//
//go:noescape
func sad16AVX2(ap, bp *byte, aStride, bStride int) int
