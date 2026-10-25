//go:build !(goexperiment.simd && amd64)

package simple_webp

// upsampleChromaRow 在没有 simd 实验的构建里整行走标量路径。
//
// upsampleChromaRow keeps the whole row on the scalar path in builds without
// the simd experiment.
func upsampleChromaRow(dst []byte, centerRow, vertRow []byte, cw, w int) {
	upsampleChromaRowScalar(dst, centerRow, vertRow, cw, 0, w)
}
