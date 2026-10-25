package simple_webp

// 行级 YUV 到 NRGBA 转换。热循环整行交给 yuvRowToNRGBA：开了
// GOEXPERIMENT=simd 的 amd64/arm64 构建走 simd/archsimd 内核，其余构建走
// 这里的标量参考实现，两条路径必须逐位一致。
//
// Row level YUV to NRGBA conversion. The whole row goes through
// yuvRowToNRGBA: amd64/arm64 builds with GOEXPERIMENT=simd use a
// simd/archsimd kernel, everything else uses the scalar reference
// implementation here. Both paths must agree bit for bit.

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
