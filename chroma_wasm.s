//go:build wasm && !purego

#include "textflag.h"

// 色度上采样的 SIMD128 实现：8 个色度样本 = 16 个输出字节，结果与
// upsampleChromaRowScalar 逐位一致。
//
// SIMD128 chroma upsampling: eight chroma samples make 16 output bytes,
// bit-for-bit identical to upsampleChromaRowScalar.
//
// Go 的 wasm 汇编器只给 v128.load/v128.store 生成了 memarg，8x8 这类带扩展
// 的加载指令会漏掉 memarg，因此这里用 I64Load 读 8 字节、再塞进零向量的
// 低 64 位，最后用 I16x8ExtendLowI8x16U 零扩展成 8 个半字。
//
// Go's wasm assembler emits memargs only for v128.load/v128.store; the
// extending loads such as v128.load8x8_u are missing theirs, so this kernel
// reads eight bytes with I64Load, puts them in the low 64 bits of a zero
// vector and zero-extends them to eight halfwords with I16x8ExtendLowI8x16U.
//
// 偶/奇输出共用 t = 9*c + 3*v；偶像素额外加 3*c[j-1] + v[j-1]，奇像素加
// 3*c[j+1] + v[j+1]，再加 8 后右移 4。最后把奇像素左移 8 位或进偶像素，
// 一个 v128 里就是 e0 o0 e1 o1 ... 的字节序。
//
// The even and the odd output share t = 9*c + 3*v; the even pixel adds
// 3*c[j-1] + v[j-1] and the odd one adds 3*c[j+1] + v[j+1], then both add 8
// and shift right by four. Shifting the odd results left by eight and OR-ing
// them into the even ones makes the register read e0 o0 e1 o1 ... as bytes.
//
// func chromaRowWasm(dst, centerRow, vertRow *byte, n int)
TEXT ·chromaRowWasm(SB), NOSPLIT, $0-32
	I64Load dst+0(FP)
	Set R0
	I64Load centerRow+8(FP)
	Set R1
	I64Load vertRow+16(FP)
	Set R2
	I64Load n+24(FP)
	Set R3

	I32Const $0
	I32x4Splat
	Set V5
	I32Const $9
	I16x8Splat
	Set V8
	I32Const $3
	I16x8Splat
	Set V9
	I32Const $8
	I16x8Splat
	Set V10

	Block
loop:
	Loop
		Get R3
		I64Eqz
		BrIf $1

		// t = 9*c + 3*v
		Get V5
		Get R1
		I32WrapI64
		I64Load $0
		I64x2ReplaceLane $0
		I16x8ExtendLowI8x16U
		Get V8
		I16x8Mul
		Get V5
		Get R2
		I32WrapI64
		I64Load $0
		I64x2ReplaceLane $0
		I16x8ExtendLowI8x16U
		Get V9
		I16x8Mul
		I16x8Add
		Set V11

		// 偶像素 / even pixels
		Get V11
		Get V5
		Get R1
		I64Const $1
		I64Sub
		I32WrapI64
		I64Load $0
		I64x2ReplaceLane $0
		I16x8ExtendLowI8x16U
		Get V9
		I16x8Mul
		I16x8Add
		Get V5
		Get R2
		I64Const $1
		I64Sub
		I32WrapI64
		I64Load $0
		I64x2ReplaceLane $0
		I16x8ExtendLowI8x16U
		I16x8Add
		Get V10
		I16x8Add
		I32Const $4
		I16x8ShrU
		Set V12

		// 奇像素 / odd pixels
		Get V11
		Get V5
		Get R1
		I64Const $1
		I64Add
		I32WrapI64
		I64Load $0
		I64x2ReplaceLane $0
		I16x8ExtendLowI8x16U
		Get V9
		I16x8Mul
		I16x8Add
		Get V5
		Get R2
		I64Const $1
		I64Add
		I32WrapI64
		I64Load $0
		I64x2ReplaceLane $0
		I16x8ExtendLowI8x16U
		I16x8Add
		Get V10
		I16x8Add
		I32Const $4
		I16x8ShrU
		Set V13

		Get R0
		I32WrapI64
		Get V13
		I32Const $8
		I16x8Shl
		Get V12
		V128Or
		V128Store $0

		Get R0
		I64Const $16
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
		I64Sub
		Set R3
		Br loop
	End
	End
	RET
