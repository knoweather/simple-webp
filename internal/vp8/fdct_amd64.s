//go:build amd64 && !purego

#include "textflag.h"

// 4x4 正向 DCT 的 AVX2 内核，结果与 fdct4x4Scalar 逐位一致。
//
// 标量版本的两趟蝶形一共要写十六个中间值到栈上的 tmp，再读回来做第二趟；
// 这里把整个 4x4 块（十六个 int32）放进四个 XMM 寄存器，两趟之间用
// unpack 指令就地转置，全程不碰内存。第一趟按列（垂直），转置后第二趟按行
// （水平）做同样的蝶形，再转置回块布局写回。
//
// AVX2 kernel for the 4x4 forward DCT, bit-for-bit identical to
// fdct4x4Scalar.
//
// The scalar version's two butterfly passes spill sixteen intermediate values
// to a stack tmp and read them back for the second pass. Here the whole 4x4
// block (sixteen int32) lives in four XMM registers and the passes are
// separated by an in-register transpose, so no memory is touched. The first
// pass works on columns, the second on rows after the transpose, and a final
// transpose restores the block layout.
//
// func fdct4x4AVX2(blk *[16]int32)
TEXT ·fdct4x4AVX2(SB), NOSPLIT, $0-8
	MOVQ blk+0(FP), AX

	VMOVDQU  0(AX), X0 // 行 0 / row 0
	VMOVDQU 16(AX), X1
	VMOVDQU 32(AX), X2
	VMOVDQU 48(AX), X3

	VPBROADCASTD ·fdctC1<>(SB), X14
	VPBROADCASTD ·fdctC2<>(SB), X15

	// ---- 第一趟：列变换 / first pass: columns ----
	VPADDD X3, X0, X4  // S = x0 + x3
	VPSUBD X3, X0, X5  // P = x0 - x3
	VPADDD X2, X1, X6  // T = x1 + x2
	VPSUBD X2, X1, X7  // Q = x1 - x2

	VPADDD X6, X4, X8 // O0 = S + T
	VPSUBD X6, X4, X9 // O2 = S - T

	VPMULLD X14, X5, X10 // C1*P
	VPMULLD X15, X7, X11 // C2*Q
	VPADDD  X11, X10, X10
	VPSRAD  $16, X10, X10 // O1 = (C1*P + C2*Q) >> 16

	VPMULLD X15, X5, X12 // C2*P
	VPMULLD X14, X7, X13 // C1*Q
	VPSUBD  X13, X12, X12
	VPSRAD  $16, X12, X12 // O3 = (C2*P - C1*Q) >> 16

	// ---- 转置成行 / transpose into rows ----
	VPUNPCKLDQ X10, X8, X13 // [O0_0 O1_0 O0_1 O1_1]
	VPUNPCKHDQ X10, X8, X14 // [O0_2 O1_2 O0_3 O1_3]
	VPUNPCKLDQ X12, X9, X15 // [O2_0 O3_0 O2_1 O3_1]
	VPUNPCKHDQ X12, X9, X11 // [O2_2 O3_2 O2_3 O3_3]

	VPUNPCKLQDQ X15, X13, X0 // 行 0 / row 0
	VPUNPCKHQDQ X15, X13, X1 // 行 1
	VPUNPCKLQDQ X11, X14, X2 // 行 2
	VPUNPCKHQDQ X11, X14, X3 // 行 3

	VPBROADCASTD ·fdctC1<>(SB), X14
	VPBROADCASTD ·fdctC2<>(SB), X15

	// ---- 第二趟：行变换 / second pass: rows ----
	VPADDD X3, X0, X4
	VPSUBD X3, X0, X5
	VPADDD X2, X1, X6
	VPSUBD X2, X1, X7

	VPADDD X6, X4, X8 // S + T
	VPSUBD X6, X4, X9 // S - T

	VPMULLD X14, X5, X10
	VPMULLD X15, X7, X11
	VPADDD  X11, X10, X10
	VPSRAD  $16, X10, X10

	VPMULLD X15, X5, X12
	VPMULLD X14, X7, X13
	VPSUBD  X13, X12, X12
	VPSRAD  $16, X12, X12

	// 加一后右移一位，与标量的 (v + 1) >> 1 一致。
	// Add one and halve, matching the scalar (v + 1) >> 1.
	VPBROADCASTD ·fdctOne<>(SB), X14
	VPADDD X14, X8, X8
	VPSRAD $1, X8, X8
	VPADDD X14, X10, X10
	VPSRAD $1, X10, X10
	VPADDD X14, X9, X9
	VPSRAD $1, X9, X9
	VPADDD X14, X12, X12
	VPSRAD $1, X12, X12

	// ---- 转置回块布局 / transpose back to block layout ----
	VPUNPCKLDQ X10, X8, X0
	VPUNPCKHDQ X10, X8, X1
	VPUNPCKLDQ X12, X9, X2
	VPUNPCKHDQ X12, X9, X3

	VPUNPCKLQDQ X2, X0, X4
	VPUNPCKHQDQ X2, X0, X5
	VPUNPCKLQDQ X3, X1, X6
	VPUNPCKHQDQ X3, X1, X7

	VMOVDQU X4, 0(AX)
	VMOVDQU X5, 16(AX)
	VMOVDQU X6, 32(AX)
	VMOVDQU X7, 48(AX)

	VZEROUPPER
	RET

// func sad16AVX2(ap, bp *byte, aStride, bStride int) int
//
// 16x16 块的绝对差之和：每行一次 VPSADBW（相邻字节绝对差求和成八个 uint16），
// 结果累加进一个向量寄存器，最后一次性水平求和。16 位累加不会溢出：一行最多
// 16 * 255 = 4080，十六行合计 65280，正好在 uint16 范围内。
//
// Sum of absolute differences over a 16x16 block: one VPSADBW per row (which
// sums absolute byte differences into eight uint16 lanes), accumulated in a
// vector register and reduced once at the end. The 16 bit accumulator cannot
// overflow: one row is at most 16 * 255 = 4080 and sixteen rows sum to 65280,
// inside the uint16 range.
TEXT ·sad16AVX2(SB), NOSPLIT, $0-40
	MOVQ ap+0(FP), SI
	MOVQ bp+8(FP), DI
	MOVQ aStride+16(FP), CX
	MOVQ bStride+24(FP), DX

	VPXOR X2, X2, X2 // 累加器 / accumulator
	MOVQ  $16, R8

loop:
	VMOVDQU (SI), X0
	VMOVDQU (DI), X1
	VPSADBW X1, X0, X0
	VPADDW  X0, X2, X2
	ADDQ    CX, SI
	ADDQ    DX, DI
	DECQ    R8
	JNZ     loop

	// 水平求和八个 uint16：先把高、低两个 64 位半相加，再把低半的四个
	// uint16 折半两次。
	// Reduce the eight uint16 lanes: fold the high and low 64 bit halves
	// together, then halve the remaining four lanes twice.
	VPSHUFD  $0x4e, X2, X3
	VPADDW   X3, X2, X2
	VPSHUFLW $0xb1, X2, X3
	VPADDW   X3, X2, X2
	VPSHUFLW $0x4e, X2, X3
	VPADDW   X3, X2, X2

	VMOVD X2, AX
	ANDQ  $0xffff, AX
	MOVQ  AX, ret+32(FP)
	VZEROUPPER
	RET

// func cpuHasAVX2() bool
TEXT ·cpuHasAVX2(SB), NOSPLIT, $0-1
	MOVL $0, AX
	CPUID
	CMPL AX, $7 // 最大叶号 < 7 时没有 AVX2 位 / no AVX2 bit below leaf 7
	JB   noavx2

	MOVL $1, AX
	XORL CX, CX
	CPUID
	MOVL CX, DX
	SHRL $27, DX // bit27 OSXSAVE, bit28 AVX
	ANDL $3, DX
	CMPL DX, $3
	JNE  noavx2

	XORL CX, CX
	XGETBV
	ANDL $6, AX // XMM(bit1) 与 YMM(bit2) 状态 / XMM and YMM state bits
	CMPL AX, $6
	JNE  noavx2

	MOVL $7, AX
	XORL CX, CX
	CPUID
	MOVL BX, DX
	SHRL $5, DX // EBX bit5 = AVX2
	ANDL $1, DX
	MOVB DX, ret+0(FP)
	RET

noavx2:
	MOVB $0, ret+0(FP)
	RET

// 变换常量，与 encode.go 里的 fdctC1 / fdctC2 一致。
// Transform constants, matching fdctC1 / fdctC2 in encode.go.
DATA ·fdctC1<>+0(SB)/4, $85627
GLOBL ·fdctC1<>(SB), RODATA|NOPTR, $4
DATA ·fdctC2<>+0(SB)/4, $35468
GLOBL ·fdctC2<>(SB), RODATA|NOPTR, $4
DATA ·fdctOne<>+0(SB)/4, $1
GLOBL ·fdctOne<>(SB), RODATA|NOPTR, $4
