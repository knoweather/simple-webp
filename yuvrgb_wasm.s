//go:build wasm && !purego

#include "textflag.h"

// YUV→NRGBA 的 SIMD128 实现：8 像素一组，结果与 yuvToRGB / yuvRowScalar
// 逐位一致。三个平面各读 8 字节（I64Load 塞进零向量的低 64 位再零扩展成
// 半字），低/高 4 个通道分别做 32 位运算（系数与移位照抄 yTab/vrTab/... 的
// 构造），两次饱和收窄成字节后用 i8x16.swizzle 交织成 RGBA。
//
// SIMD128 YUV to NRGBA: eight pixels per iteration, bit-for-bit identical to
// yuvToRGB / yuvRowScalar. Each plane contributes eight bytes (an I64Load put
// into the low 64 bits of a zero vector, then zero-extended to halfwords);
// the low and the high four lanes run 32-bit arithmetic whose coefficients
// and shifts mirror yTab/vrTab/..., then two saturating narrows and an
// i8x16.swizzle produce RGBA bytes.
//
// 交织索引由调用方传入 16 字节常量：Go 的 wasm 汇编器把 replace_lane 的
// 通道号写错（$1 落到 0 号通道），所以不在内核里现拼。
//
// The interleave indices arrive as a 16-byte constant from the caller: Go's
// wasm assembler encodes replace_lane's lane index wrongly (a $1 lands in
// lane 0), so the kernel does not build the vector on the fly.
//
// func yuvRowWasm(dst, yRow, uRow, vRow, index *byte, n int)
TEXT ·yuvRowWasm(SB), NOSPLIT, $0-48
	I64Load dst+0(FP)
	Set R0
	I64Load yRow+8(FP)
	Set R1
	I64Load uRow+16(FP)
	Set R2
	I64Load vRow+24(FP)
	Set R3
	I64Load index+32(FP)
	Set R5
	I64Load n+40(FP)
	Set R4

	I32Const $0
	I32x4Splat
	Set V13
	I32Const $19077
	I32x4Splat
	Set V8
	I32Const $26149
	I32x4Splat
	Set V9
	I32Const $6419
	I32x4Splat
	Set V10
	I32Const $13320
	I32x4Splat
	Set V11
	I32Const $33050
	I32x4Splat
	Set V12
	I32Const $255
	I32x4Splat
	Set V15
	Get R5
	I32WrapI64
	V128Load $0
	Set V7

	Block
loop:
	Loop
		Get R4
		I64Eqz
		BrIf $1

		Get V13
		Get R1
		I32WrapI64
		I64Load $0
		I64x2ReplaceLane $0
		I16x8ExtendLowI8x16U
		Set V0
		Get V13
		Get R2
		I32WrapI64
		I64Load $0
		I64x2ReplaceLane $0
		I16x8ExtendLowI8x16U
		Set V1
		Get V13
		Get R3
		I32WrapI64
		I64Load $0
		I64x2ReplaceLane $0
		I16x8ExtendLowI8x16U
		Set V2

		// 低 4 像素 / low four pixels
		Get V0
		I32x4ExtendLowI16x8U
		Get V8
		I32x4Mul
		I32Const $8
		I32x4ShrS
		Set V3
		Get V2
		I32x4ExtendLowI16x8U
		Get V9
		I32x4Mul
		I32Const $8
		I32x4ShrS
		Get V3
		I32x4Add
		I32Const $14234
		I32x4Splat
		I32x4Sub
		I32Const $6
		I32x4ShrS
		Set V4

		Get V0
		I32x4ExtendLowI16x8U
		Get V8
		I32x4Mul
		I32Const $8
		I32x4ShrS
		Get V1
		I32x4ExtendLowI16x8U
		Get V10
		I32x4Mul
		I32Const $8
		I32x4ShrS
		I32x4Sub
		Get V2
		I32x4ExtendLowI16x8U
		Get V11
		I32x4Mul
		I32Const $8
		I32x4ShrS
		I32x4Sub
		I32Const $8708
		I32x4Splat
		I32x4Add
		I32Const $6
		I32x4ShrS
		Set V5

		Get V0
		I32x4ExtendLowI16x8U
		Get V8
		I32x4Mul
		I32Const $8
		I32x4ShrS
		Get V1
		I32x4ExtendLowI16x8U
		Get V12
		I32x4Mul
		I32Const $8
		I32x4ShrS
		I32x4Add
		I32Const $17685
		I32x4Splat
		I32x4Sub
		I32Const $6
		I32x4ShrS
		Set V6

		Get R0
		I32WrapI64
		Get V4
		Get V5
		I16x8NarrowI32x4U
		Get V6
		Get V15
		I16x8NarrowI32x4U
		I8x16NarrowI16x8U
		Get V7
		I8x16Swizzle
		V128Store $0

		// 高 4 像素 / high four pixels
		Get V0
		I32x4ExtendHighI16x8U
		Get V8
		I32x4Mul
		I32Const $8
		I32x4ShrS
		Set V3
		Get V2
		I32x4ExtendHighI16x8U
		Get V9
		I32x4Mul
		I32Const $8
		I32x4ShrS
		Get V3
		I32x4Add
		I32Const $14234
		I32x4Splat
		I32x4Sub
		I32Const $6
		I32x4ShrS
		Set V4

		Get V0
		I32x4ExtendHighI16x8U
		Get V8
		I32x4Mul
		I32Const $8
		I32x4ShrS
		Get V1
		I32x4ExtendHighI16x8U
		Get V10
		I32x4Mul
		I32Const $8
		I32x4ShrS
		I32x4Sub
		Get V2
		I32x4ExtendHighI16x8U
		Get V11
		I32x4Mul
		I32Const $8
		I32x4ShrS
		I32x4Sub
		I32Const $8708
		I32x4Splat
		I32x4Add
		I32Const $6
		I32x4ShrS
		Set V5

		Get V0
		I32x4ExtendHighI16x8U
		Get V8
		I32x4Mul
		I32Const $8
		I32x4ShrS
		Get V1
		I32x4ExtendHighI16x8U
		Get V12
		I32x4Mul
		I32Const $8
		I32x4ShrS
		I32x4Add
		I32Const $17685
		I32x4Splat
		I32x4Sub
		I32Const $6
		I32x4ShrS
		Set V6

		Get R0
		I32WrapI64
		I32Const $16
		I32Add
		Get V4
		Get V5
		I16x8NarrowI32x4U
		Get V6
		Get V15
		I16x8NarrowI32x4U
		I8x16NarrowI16x8U
		Get V7
		I8x16Swizzle
		V128Store $0

		Get R0
		I64Const $32
		I64Add
		Set R0
		Get R1
		I64Const $8
		I64Add
		Set R1
		Get R2
		I64Const $8
		I64Add
		Set R2
		Get R3
		I64Const $8
		I64Add
		Set R3
		Get R4
		I64Const $8
		I64Sub
		Set R4
		Br loop
	End
	End
	RET
