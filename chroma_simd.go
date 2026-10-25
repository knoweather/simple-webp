//go:build goexperiment.simd && amd64

package simple_webp

import "simd/archsimd"

// 色度上采样的 SIMD 内核：8 个色度样本（16 个输出字节）一组，128 位向量，
// 结果与 upsampleChromaRowScalar 逐位一致。
//
// SIMD chroma upsampling: eight chroma samples (16 output bytes) per
// iteration with 128-bit vectors, bit-for-bit identical to
// upsampleChromaRowScalar.
//
// 偶/奇输出共用 t = 9*c + 3*v；偶像素额外加 3*c[j-1] + v[j-1]，奇像素加
// 3*c[j+1] + v[j+1]，再加 8 后右移 4。两个结果的低 8 字节按字节交织，
// 一个向量里就是 e0 o0 e1 o1 ... 的输出顺序。
//
// The even and the odd output share t = 9*c + 3*v; the even pixel adds
// 3*c[j-1] + v[j-1] and the odd one adds 3*c[j+1] + v[j+1], then both add 8
// and shift right by four. Byte-interleaving the low eight bytes of the two
// results yields e0 o0 e1 o1 ... in memory order.

// chromaInterleave 把偶、奇两个向量按字节交织成一个 16 字节向量：索引
// 0..15 取第一个向量，16..31 取第二个。
//
// chromaInterleave interleaves the even and the odd vector byte by byte:
// indices 0..15 select the first vector, 16..31 the second.
var chromaInterleave = [16]byte{0, 16, 1, 17, 2, 18, 3, 19, 4, 20, 5, 21, 6, 22, 7, 23}

// upsampleChromaRow 在开了 simd 实验的构建里走 SIMD 内核，两端与零碎尾部
// 仍由标量处理。
//
// upsampleChromaRow uses the SIMD kernel in simd-experiment builds; the row
// ends and the leftover tail stay scalar.
func upsampleChromaRow(dst []byte, centerRow, vertRow []byte, cw, w int) {
	// 只有 j 落在 [1, cw-2] 的样本不会触发水平钳位。
	// Only samples with j in [1, cw-2] avoid horizontal clamping.
	n := 0
	if cw >= 2 {
		n = (cw - 2) &^ 7
	}
	if n == 0 {
		upsampleChromaRowScalar(dst, centerRow, vertRow, cw, 0, w)
		return
	}
	upsampleChromaRowScalar(dst, centerRow, vertRow, cw, 0, 2)
	chromaRowSIMD(dst[2:2+2*n], centerRow, vertRow, n)
	upsampleChromaRowScalar(dst, centerRow, vertRow, cw, 2+2*n, w)
}

// chromaRowSIMD 处理 j = 1..n 这 n 个色度样本；centerRow/vertRow 是整行
// （从 j=0 开始），因此 j-1 与 j+1 的 tap 都在切片内。
//
// chromaRowSIMD processes samples j = 1..n; centerRow/vertRow are whole rows
// starting at j=0, so the j-1 and j+1 taps stay inside the slices.
func chromaRowSIMD(dst, centerRow, vertRow []byte, n int) {
	c9 := archsimd.BroadcastUint16x8(9)
	c3 := archsimd.BroadcastUint16x8(3)
	c8 := archsimd.BroadcastUint16x8(8)
	idx := archsimd.LoadUint8x16(chromaInterleave[:])

	k := 0
	for j := 1; j <= n; j += 8 {
		cm := loadChroma8(centerRow[j:])
		vm := loadChroma8(vertRow[j:])
		t := cm.Mul(c9).Add(vm.Mul(c3))

		cl := loadChroma8(centerRow[j-1:])
		vl := loadChroma8(vertRow[j-1:])
		even := t.Add(cl.Mul(c3)).Add(vl).Add(c8).ShiftAllRight(4)

		cr := loadChroma8(centerRow[j+1:])
		vr := loadChroma8(vertRow[j+1:])
		odd := t.Add(cr.Mul(c3)).Add(vr).Add(c8).ShiftAllRight(4)

		out := even.SaturateToUint8().ConcatPermute(odd.SaturateToUint8(), idx)
		out.Store(dst[k:])
		k += 16
	}
}

// loadChroma8 读 8 个字节并零扩展成 8 个半字；不足 8 字节的部分补零，
// 只有低 8 个通道会被用到。
//
// loadChroma8 reads eight bytes and zero-extends them to eight halfwords;
// short slices are zero-filled, and only the low eight lanes are used.
func loadChroma8(s []byte) archsimd.Uint16x8 {
	v, _ := archsimd.LoadUint8x16Part(s)
	return v.ExtendLo8ToUint16()
}
