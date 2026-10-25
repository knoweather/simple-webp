//go:build (!amd64 && !arm64 && !wasm) || purego

package simple_webp

// upsampleChromaRow 在没有向量化内核的构建里整行走标量路径。
//
// upsampleChromaRow keeps the whole row on the scalar path in builds without
// a vector kernel.
func upsampleChromaRow(dst []byte, centerRow, vertRow []byte, cw, w int) {
	upsampleChromaRowWithKernel(dst, centerRow, vertRow, cw, w, nil)
}
