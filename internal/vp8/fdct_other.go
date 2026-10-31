//go:build !amd64 || purego

package vp8

// fdct4x4 在没有 AVX2 内核的平台上直接走标量版本。
//
// fdct4x4 falls back to the scalar version on platforms without an AVX2 kernel.
func fdct4x4(blk *[16]int32) {
	fdct4x4Scalar(blk)
}
