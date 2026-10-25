//go:build wasm && !purego

package simple_webp

// wasm 的 SIMD128 内核，写法与 chroma_wasm.go 相同：栈式指令 + v128 局部
// 变量，没有运行时探测。
//
// SIMD128 kernels for wasm, written like chroma_wasm.go: stack machine
// instructions over v128 locals, with no runtime probe.

// yuvRGBAIndex 是 i8x16.swizzle 用的交织索引：R 在 0..3、G 在 4..7、
// B 在 8..11、A 在 12..15。作为常量从内存传入内核，原因见 yuvrgb_wasm.s。
//
// yuvRGBAIndex is the i8x16.swizzle interleave index: R at 0..3, G at 4..7,
// B at 8..11, A at 12..15. It is passed to the kernel as a constant from
// memory; see yuvrgb_wasm.s for why.
var yuvRGBAIndex = [16]byte{0, 4, 8, 12, 1, 5, 9, 12, 2, 6, 10, 12, 3, 7, 11, 12}

// yuvRowToNRGBA 在 wasm 上走 SIMD128 内核。
//
// yuvRowToNRGBA uses the SIMD128 kernel on wasm.
func yuvRowToNRGBA(dst, yRow, uRow, vRow []byte) {
	yuvRowWithKernel(dst, yRow, uRow, vRow, wasmYUVKernel)
}

func wasmYUVKernel(dst, yRow, uRow, vRow *byte, n int) {
	yuvRowWasm(dst, yRow, uRow, vRow, &yuvRGBAIndex[0], n)
}

// yuvRowWasm 一次处理 8 个像素，n 必须是 8 的倍数。
//
// yuvRowWasm handles eight pixels per iteration; n must be a multiple of
// eight.
//
//go:noescape
func yuvRowWasm(dst, yRow, uRow, vRow, index *byte, n int)
