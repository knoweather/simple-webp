//go:build arm64 && !purego

package simple_webp

// arm64 的 NEON 内核。NEON 是 ARMv8-A 的基线，不需要运行时探测。
//
// NEON kernels for arm64. NEON is part of the ARMv8-A baseline, so no runtime
// probe is needed.

// upsampleChromaRow 在 arm64 上走 NEON 内核。
//
// upsampleChromaRow uses the NEON kernel on arm64.
func upsampleChromaRow(dst []byte, centerRow, vertRow []byte, cw, w int) {
	upsampleChromaRowWithKernel(dst, centerRow, vertRow, cw, w, chromaRowNEON)
}

// chromaRowNEON 处理从中心行的第 2 个色度样本起、连续 n 个样本的 16n 个
// 输出像素；n 必须是 8 的倍数，且第 n+1 个样本存在（调用方已保证）。
//
// chromaRowNEON handles the 16n output pixels of n consecutive chroma samples
// starting at the centre row's second sample; n must be a multiple of eight
// and sample n+1 must exist, which the caller guarantees.
//
//go:noescape
func chromaRowNEON(dst, centerRow, vertRow *byte, n int)
