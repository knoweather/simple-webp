//go:build amd64 && !purego

package simple_webp

// useAVX2 在包初始化时探测：CPUID 报告 AVX2，且 XCR0 表明操作系统已为
// SSE/AVX 保存状态。这里只信 CPU 特性位、不看 GOAMD64，因为同一份二进制
// 要在任意 x86-64 机器上运行。
//
// useAVX2 is probed at package init: CPUID must report AVX2 and XCR0 must show
// the OS has enabled the SSE/AVX state. The probe deliberately ignores
// GOAMD64, because a single binary has to run on any x86-64 machine.
var useAVX2 = cpuHasAVX2()

// yuvRowToNRGBA 在支持 AVX2 的机器上走汇编内核，否则整行走标量。
//
// yuvRowToNRGBA uses the assembly kernel where AVX2 is available and keeps
// the whole row scalar otherwise.
func yuvRowToNRGBA(dst, yRow, uRow, vRow []byte) {
	var k yuvKernel
	if useAVX2 {
		k = yuvRowAVX2
	}
	yuvRowWithKernel(dst, yRow, uRow, vRow, k)
}

// cpuHasAVX2 实现在 yuvrgb_amd64.s，用 CPUID + XGETBV 判断。
//
// cpuHasAVX2 is implemented in yuvrgb_amd64.s and decides with CPUID + XGETBV.
//
//go:noescape
func cpuHasAVX2() bool

// yuvRowAVX2 是 8 像素一组的 AVX2 内核，n 必须是 8 的倍数。
//
// yuvRowAVX2 is the AVX2 kernel, eight pixels per iteration; n must be a
// multiple of eight.
//
//go:noescape
func yuvRowAVX2(dst, yRow, uRow, vRow *byte, n int)
