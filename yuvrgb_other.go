//go:build (!amd64 && !arm64 && !wasm) || purego

package simple_webp

// yuvRowToNRGBA 在没有向量化内核的构建里整行走标量参考实现。
//
// yuvRowToNRGBA keeps the whole row on the scalar reference implementation in
// builds without a vector kernel.
func yuvRowToNRGBA(dst, yRow, uRow, vRow []byte) {
	yuvRowWithKernel(dst, yRow, uRow, vRow, nil)
}
