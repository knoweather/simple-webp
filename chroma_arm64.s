//go:build arm64 && !purego

#include "textflag.h"

// 色度上采样的 NEON 实现：8 个色度样本 = 16 个输出字节，结果与
// upsampleChromaRowScalar 逐位一致。
//
// NEON chroma upsampling: eight chroma samples make 16 output bytes,
// bit-for-bit identical to upsampleChromaRowScalar.
//
// Go 的 arm64 汇编器没有 VMOVL/VMLAL（宽化), 所以用 VZIP1 配零向量把字节
// 零扩展成半字，9x/3x 用移位加加法代替乘法；VLD1 只接受 (Rn) 形式，水平
// 邻居的指针在循环里先算好。偶/奇输出共用 t = 9*c + 3*v，最后把奇像素左移
// 8 位或进偶像素，一个 v128 里就是 e0 o0 e1 o1 ... 的字节序。
//
// Go's arm64 assembler has no VMOVL/VMLAL (widening), so VZIP1 against a zero
// vector zero-extends bytes to halfwords and 9x/3x are built from shifts and
// adds; VLD1 only accepts the (Rn) form, so the horizontal neighbour pointers
// are computed in the loop. The even and odd outputs share t = 9*c + 3*v;
// shifting the odd results left by eight and OR-ing them into the even ones
// makes the register read e0 o0 e1 o1 ... as bytes.
//
// func chromaRowNEON(dst, centerRow, vertRow *byte, n int)
TEXT ·chromaRowNEON(SB), NOSPLIT, $0-32
	MOVD dst+0(FP), R0
	MOVD centerRow+8(FP), R1
	MOVD vertRow+16(FP), R2
	MOVD n+24(FP), R3

	VEOR V31.B16, V31.B16, V31.B16
	MOVD $9, R8
	VDUP R8, V8.H8
	MOVD $3, R8
	VDUP R8, V9.H8
	MOVD $8, R8
	VDUP R8, V10.H8

loop:
	SUB  $1, R1, R4 // c[j-1]
	ADD  $1, R1, R5 // c[j+1]
	SUB  $1, R2, R6 // v[j-1]
	ADD  $1, R2, R7 // v[j+1]

	VLD1  (R1), [V0.B8]
	VZIP1 V31.B16, V0.B16, V0.B16
	VLD1  (R2), [V1.B8]
	VZIP1 V31.B16, V1.B16, V1.B16

	VMUL  V8.H8, V0.H8, V2.H8 // 9c
	VMUL  V9.H8, V1.H8, V3.H8 // 3v
	VADD  V3.H8, V2.H8, V2.H8 // t = 9c + 3v

	// 偶像素 / even pixels
	VLD1  (R4), [V4.B8]
	VZIP1 V31.B16, V4.B16, V4.B16
	VMUL  V9.H8, V4.H8, V4.H8
	VADD  V4.H8, V2.H8, V4.H8
	VLD1  (R6), [V5.B8]
	VZIP1 V31.B16, V5.B16, V5.B16
	VADD  V5.H8, V4.H8, V4.H8
	VADD  V10.H8, V4.H8, V4.H8
	VUSHR $4, V4.H8, V4.H8

	// 奇像素 / odd pixels
	VLD1  (R5), [V6.B8]
	VZIP1 V31.B16, V6.B16, V6.B16
	VMUL  V9.H8, V6.H8, V6.H8
	VADD  V6.H8, V2.H8, V6.H8
	VLD1  (R7), [V7.B8]
	VZIP1 V31.B16, V7.B16, V7.B16
	VADD  V7.H8, V6.H8, V6.H8
	VADD  V10.H8, V6.H8, V6.H8
	VUSHR $4, V6.H8, V6.H8

	VSHL  $8, V6.H8, V6.H8
	VORR  V6.B16, V4.B16, V4.B16
	VST1  [V4.B16], (R0)

	ADD  $8, R1
	ADD  $8, R2
	ADD  $16, R0
	SUB  $8, R3
	CBNZ R3, loop
	RET
