//go:build amd64 && !purego

package simple_webp

// upsampleChromaRow 在支持 AVX2 的机器上走汇编内核，否则整行走标量。
//
// upsampleChromaRow uses the assembly kernel where AVX2 is available and
// keeps the whole row scalar otherwise.
//
// centerRow 是中心色度行（k=y>>1），vertRow 是垂直邻居色度行
// （k+dk 已由调用方按平面高度钳位）。水平方向的 tap 越界时钳位到
// [0, cw-1]，与 chromaAt 的行为一致。
//
// centerRow is the centre chroma row (k=y>>1); vertRow is the vertical
// neighbour row (k+dk already clamped to the plane height by the caller).
// Horizontal taps out of range are clamped to [0, cw-1], matching chromaAt.
func upsampleChromaRow(dst []byte, centerRow, vertRow []byte, cw, w int) {
	var k chromaKernel
	if useAVX2 {
		k = chromaRowAVX2
	}
	upsampleChromaRowWithKernel(dst, centerRow, vertRow, cw, w, k)
}

// chromaRowAVX2 处理从中心行的第 2 个色度样本起、连续 n 个样本的 16n 个
// 输出像素；n 必须是 8 的倍数，且第 n+1 个样本存在（调用方已保证）。
//
// chromaRowAVX2 handles the 16n output pixels of n consecutive chroma samples
// starting at the centre row's second sample; n must be a multiple of eight
// and sample n+1 must exist, which the caller guarantees.
//
//go:noescape
func chromaRowAVX2(dst, centerRow, vertRow *byte, n int)
