//go:build !(goexperiment.simd && amd64)

package simple_webp

// yuvRowToNRGBA 在没有 simd 实验的构建里整行走标量参考实现。
//
// yuvRowToNRGBA keeps the whole row on the scalar reference implementation in
// builds without the simd experiment.
func yuvRowToNRGBA(dst, yRow, uRow, vRow []byte) {
	yuvRowScalar(dst, yRow, uRow, vRow)
}
