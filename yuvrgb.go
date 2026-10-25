package simple_webp

// 行级 YUV 到 NRGBA 转换。热循环整行交给 yuvRowToNRGBA：各架构给一个
// 8 像素一组的 kernel（amd64 是 AVX2、arm64 是 NEON、wasm 是 SIMD128），
// 其它平台与 purego 构建没有 kernel，直接走标量路径。两条路径必须逐位一致。
//
// Row level YUV to NRGBA conversion. The whole row goes through
// yuvRowToNRGBA, which takes an eight-pixel kernel on architectures that have
// one (AVX2 on amd64, NEON on arm64, SIMD128 on wasm) and falls back to the
// scalar path elsewhere and in purego builds. Both paths must agree bit for bit.

// yuvKernel 是一行内 8 像素一组的向量化内核，n 必须是 8 的倍数。
//
// yuvKernel is the vectorised eight-pixel kernel; n must be a multiple of
// eight.
type yuvKernel func(dst, yRow, uRow, vRow *byte, n int)

// yuvRowScalar 逐像素转换一行 YUV，未向量化的参考实现。
//
// yuvRowScalar converts one row of YUV pixel by pixel; it is the
// unvectorised reference implementation.
func yuvRowScalar(dst, yRow, uRow, vRow []byte) {
	for x := range yRow {
		r, g, b := yuvToRGB(int(yRow[x]), int(uRow[x]), int(vRow[x]))
		o := x << 2
		dst[o+0] = r
		dst[o+1] = g
		dst[o+2] = b
		dst[o+3] = 255
	}
}

// yuvRowWithKernel 用 kernel 处理整行里 8 像素的整数段，不足一组的尾部
// 交给标量参考实现；kernel 为 nil 时整行走标量。
//
// yuvRowWithKernel runs the eight-pixel groups of a row through kernel and
// gives the sub-group tail to the scalar reference; a nil kernel keeps the
// whole row scalar.
func yuvRowWithKernel(dst, yRow, uRow, vRow []byte, kernel yuvKernel) {
	n := 0
	if kernel != nil && len(yRow) >= 8 {
		n = len(yRow) &^ 7
		kernel(&dst[0], &yRow[0], &uRow[0], &vRow[0], n)
	}
	if n < len(yRow) {
		yuvRowScalar(dst[n<<2:], yRow[n:], uRow[n:], vRow[n:])
	}
}
