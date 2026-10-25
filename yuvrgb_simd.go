//go:build goexperiment.simd && amd64

package simple_webp

import "simd/archsimd"

// YUV→NRGBA 的 SIMD 内核：4 像素一组，128 位向量，结果与 yuvToRGB /
// yuvRowScalar 逐位一致。系数与移位照抄 yTab/vrTab/... 的构造（乘系数后
// 右移 8，累加后再右移 6），因此中间值不会溢出 int32。
//
// SIMD YUV to NRGBA: four pixels per iteration with 128-bit vectors,
// bit-for-bit identical to yuvToRGB / yuvRowScalar. Coefficients and shifts
// mirror how yTab/vrTab/... are built (multiply, shift right 8, accumulate,
// shift right 6), so no intermediate overflows int32.

// rgbaPairInterleave 把两个向量按字节两两交织：索引 0..15 取第一个向量，
// 16..31 取第二个，只用于低 8 个输出字节。
//
// rgbaPairInterleave interleaves two vectors byte by byte: indices 0..15
// select the first vector and 16..31 the second; only the low eight output
// bytes matter.
var rgbaPairInterleave = [16]byte{0, 16, 1, 17, 2, 18, 3, 19, 0, 16, 1, 17, 2, 18, 3, 19}

// yuvRowToNRGBA 在开了 simd 实验的构建里走 SIMD 内核，不足 4 像素的尾部
// 由标量参考实现处理。
//
// yuvRowToNRGBA uses the SIMD kernel in simd-experiment builds and leaves the
// sub-vector tail to the scalar reference implementation.
func yuvRowToNRGBA(dst, yRow, uRow, vRow []byte) {
	n := len(yRow) &^ 3
	if n > 0 {
		yuvRowSIMD(dst[:n*4], yRow[:n], uRow[:n], vRow[:n])
	}
	if n < len(yRow) {
		yuvRowScalar(dst[n*4:], yRow[n:], uRow[n:], vRow[n:])
	}
}

func yuvRowSIMD(dst, yRow, uRow, vRow []byte) {
	c19077 := archsimd.BroadcastInt32x4(19077)
	c26149 := archsimd.BroadcastInt32x4(26149)
	c6419 := archsimd.BroadcastInt32x4(6419)
	c13320 := archsimd.BroadcastInt32x4(13320)
	c33050 := archsimd.BroadcastInt32x4(33050)
	c14234 := archsimd.BroadcastInt32x4(14234)
	c8708 := archsimd.BroadcastInt32x4(8708)
	c17685 := archsimd.BroadcastInt32x4(17685)
	zero := archsimd.BroadcastInt32x4(0)
	max255 := archsimd.BroadcastInt32x4(255)
	alpha := archsimd.BroadcastUint8x16(255)
	idx := archsimd.LoadUint8x16(rgbaPairInterleave[:])

	for i := 0; i < len(yRow); i += 4 {
		y := loadYUV4(yRow[i:])
		u := loadYUV4(uRow[i:])
		v := loadYUV4(vRow[i:])

		yt := y.Mul(c19077).ShiftAllRight(8)

		r := yt.Add(v.Mul(c26149).ShiftAllRight(8)).Sub(c14234).ShiftAllRight(6)
		g := yt.Sub(u.Mul(c6419).ShiftAllRight(8)).
			Sub(v.Mul(c13320).ShiftAllRight(8)).
			Add(c8708).ShiftAllRight(6)
		b := yt.Add(u.Mul(c33050).ShiftAllRight(8)).Sub(c17685).ShiftAllRight(6)

		r = r.Max(zero).Min(max255)
		g = g.Max(zero).Min(max255)
		b = b.Max(zero).Min(max255)

		rg := r.ConvertToUint32().SaturateToUint8().
			ConcatPermute(g.ConvertToUint32().SaturateToUint8(), idx)
		ba := b.ConvertToUint32().SaturateToUint8().ConcatPermute(alpha, idx)
		out := rg.AsUint16x8().InterleaveLo(ba.AsUint16x8()).AsUint8x16()
		out.Store(dst[i*4:])
	}
}

// loadYUV4 读 4 个字节并零扩展成 4 个 int32 通道；不足 4 字节的部分补零，
// 只有低 4 个通道会被用到。
//
// loadYUV4 reads four bytes and zero-extends them to four int32 lanes; short
// slices are zero-filled and only the low four lanes are used.
func loadYUV4(s []byte) archsimd.Int32x4 {
	v, _ := archsimd.LoadUint8x16Part(s)
	return v.ExtendLo4ToUint32().AsInt32x4()
}
