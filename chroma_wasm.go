//go:build wasm && !purego

package simple_webp

// wasm 的 SIMD128 内核。Go 的 wasm 后端把汇编函数编译成原生的
// WebAssembly 指令，所以这里写的是栈式指令配 V0..V15 的 v128 局部变量，
// 没有运行时特性探测：SIMD128 由宿主（浏览器/Node）决定是否支持。
//
// SIMD128 kernels for wasm. Go's wasm backend compiles assembly functions to
// native WebAssembly instructions, so this file is written in stack machine
// form with V0..V15 as v128 locals. There is no runtime feature probe here:
// whether SIMD128 exists is up to the host (browser or Node).

// upsampleChromaRow 在 wasm 上走 SIMD128 内核。
//
// upsampleChromaRow uses the SIMD128 kernel on wasm.
func upsampleChromaRow(dst []byte, centerRow, vertRow []byte, cw, w int) {
	upsampleChromaRowWithKernel(dst, centerRow, vertRow, cw, w, chromaRowWasm)
}

// chromaRowWasm 处理从中心行的第 2 个色度样本起、连续 n 个样本的 16n 个
// 输出像素；n 必须是 8 的倍数，且第 n+1 个样本存在（调用方已保证）。
//
// chromaRowWasm handles the 16n output pixels of n consecutive chroma samples
// starting at the centre row's second sample; n must be a multiple of eight
// and sample n+1 must exist, which the caller guarantees.
//
//go:noescape
func chromaRowWasm(dst, centerRow, vertRow *byte, n int)
