//go:build amd64 && !purego

#include "textflag.h"

// YUV→NRGBA 的 AVX2 实现：每次 8 像素，结果与 yuvToRGB / yuvRowScalar 逐位
// 一致。定点系数与移位次数照抄 yTab/vrTab/... 的构造（乘系数后右移 8，
// 累加后再右移 6），因此中间值不会溢出 int32。
//
// AVX2 implementation of YUV to NRGBA: eight pixels per iteration, bit-for-bit
// identical to yuvToRGB / yuvRowScalar. The fixed point coefficients and shifts
// mirror how yTab/vrTab/... are built (multiply, shift right 8, accumulate,
// shift right 6), so no intermediate overflows int32.
//
// 打包顺序：r/g/b 先 packusdw 成 16 位（负值截到 0、超过 255 的留到下一步），
// 再 packuswb 成 8 位（超过 255 的饱和到 255），最后用 pshufb 交织成 RGBA。
//
// Packing: r/g/b are first narrowed to 16 bits with packusdw (negatives clamp
// to 0, values above 255 survive to the next step), then to 8 bits with
// packuswb (saturating at 255), and finally interleaved into RGBA with pshufb.

// func yuvRowAVX2(dst, yRow, uRow, vRow *byte, n int)
TEXT ·yuvRowAVX2(SB), NOSPLIT, $0-40
	MOVQ dst+0(FP), DI
	MOVQ yRow+8(FP), SI
	MOVQ uRow+16(FP), R8
	MOVQ vRow+24(FP), R9
	MOVQ n+32(FP), CX

	TESTQ CX, CX
	JZ    done

	// 常量在循环外广播一次，Y8..Y15 整个循环只读。
	// Broadcast the constants once outside the loop; Y8..Y15 are read-only.
	VPBROADCASTD ·yuvC19077<>(SB), Y8  // Y  系数 / Y coefficient
	VPBROADCASTD ·yuvC26149<>(SB), Y9  // Vr
	VPBROADCASTD ·yuvC6419<>(SB), Y10  // Ug
	VPBROADCASTD ·yuvC13320<>(SB), Y11 // Vg
	VPBROADCASTD ·yuvC33050<>(SB), Y12 // Ub
	VPBROADCASTD ·yuvC14234<>(SB), Y13 // R 的取整偏移 / R rounding offset
	VPBROADCASTD ·yuvC8708<>(SB), Y14  // G 的取整偏移 / G rounding offset
	VPBROADCASTD ·yuvC17685<>(SB), Y15 // B 的取整偏移 / B rounding offset
	VMOVDQU     ·yuvShuf<>(SB), X7     // RGBA 交织掩码 / RGBA interleave mask

loop:
	VPMOVZXBD (SI), Y0 // Y
	VPMOVZXBD (R8), Y1 // U (Cb)
	VPMOVZXBD (R9), Y2 // V (Cr)

	VPMULLD Y8, Y0, Y3
	VPSRAD  $8, Y3, Y3 // yTab[Y]

	// R = (yTab[Y] + vrTab[V] - 14234) >> 6
	VPMULLD Y9, Y2, Y4
	VPSRAD  $8, Y4, Y4
	VPADDD  Y3, Y4, Y5
	VPSUBD  Y13, Y5, Y5
	VPSRAD  $6, Y5, Y5

	// G = (yTab[Y] - ugTab[U] - vgTab[V] + 8708) >> 6
	VPMULLD Y10, Y1, Y4
	VPSRAD  $8, Y4, Y4
	VPSUBD  Y4, Y3, Y4
	VPMULLD Y11, Y2, Y6
	VPSRAD  $8, Y6, Y6
	VPSUBD  Y6, Y4, Y4
	VPADDD  Y14, Y4, Y4
	VPSRAD  $6, Y4, Y4

	// B = (yTab[Y] + ubTab[U] - 17685) >> 6
	VPMULLD Y12, Y1, Y6
	VPSRAD  $8, Y6, Y6
	VPADDD  Y6, Y3, Y6
	VPSUBD  Y15, Y6, Y6
	VPSRAD  $6, Y6, Y6

	// 高 4 像素的 r/g/b 先抽到 X0/X1/X2。
	// Extract the high four pixels' r/g/b into X0/X1/X2 first.
	VEXTRACTI128 $1, Y5, X0
	VEXTRACTI128 $1, Y4, X1
	VEXTRACTI128 $1, Y6, X2

	// 低 4 像素：X3 = RGBA。
	// Low four pixels: X3 = RGBA.
	VPACKUSDW X4, X5, X3 // [r0..r3 | g0..g3] 16 位 / words
	VPCMPEQD  X4, X4, X4
	VPSRLD    $24, X4, X4 // 每个 dword 变成 255 / each dword becomes 255
	VPACKUSDW X4, X6, X5  // [b0..b3 | 255 x4]
	VPACKUSWB X5, X3, X3  // [r | g | b | a] 8 位 / bytes
	VPSHUFB   X7, X3, X3

	// 高 4 像素：X5 = RGBA。
	// High four pixels: X5 = RGBA.
	VPACKUSDW X1, X0, X5
	VPACKUSDW X4, X2, X6
	VPACKUSWB X6, X5, X5
	VPSHUFB   X7, X5, X5

	VINSERTI128 $1, X5, Y3, Y3
	VMOVDQU     Y3, (DI) // 8 像素 × 4 字节 / eight RGBA pixels

	ADDQ $8, SI
	ADDQ $8, R8
	ADDQ $8, R9
	ADDQ $32, DI
	SUBQ $8, CX
	JNZ  loop

done:
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
	SHRL $27, DX  // bit27 OSXSAVE, bit28 AVX
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

// 定点系数与 yuvToRGB 的表构造一致，每个 4 字节广播使用。
// Fixed point coefficients matching yuvToRGB's tables, broadcast from 4 bytes.
DATA ·yuvC19077<>+0(SB)/4, $19077
GLOBL ·yuvC19077<>(SB), RODATA|NOPTR, $4
DATA ·yuvC26149<>+0(SB)/4, $26149
GLOBL ·yuvC26149<>(SB), RODATA|NOPTR, $4
DATA ·yuvC6419<>+0(SB)/4, $6419
GLOBL ·yuvC6419<>(SB), RODATA|NOPTR, $4
DATA ·yuvC13320<>+0(SB)/4, $13320
GLOBL ·yuvC13320<>(SB), RODATA|NOPTR, $4
DATA ·yuvC33050<>+0(SB)/4, $33050
GLOBL ·yuvC33050<>(SB), RODATA|NOPTR, $4
DATA ·yuvC14234<>+0(SB)/4, $14234
GLOBL ·yuvC14234<>(SB), RODATA|NOPTR, $4
DATA ·yuvC8708<>+0(SB)/4, $8708
GLOBL ·yuvC8708<>(SB), RODATA|NOPTR, $4
DATA ·yuvC17685<>+0(SB)/4, $17685
GLOBL ·yuvC17685<>(SB), RODATA|NOPTR, $4

// RGBA 交织掩码：R 在 0..3、G 在 4..7、B 在 8..11、A 在 12..15，
// 展开成 r0 g0 b0 a0 r1 g1 b1 a1 ...
//
// RGBA interleave mask: R at 0..3, G at 4..7, B at 8..11, A at 12..15.
DATA ·yuvShuf<>+0(SB)/8, $0x0c0905010c080400
DATA ·yuvShuf<>+8(SB)/8, $0x0c0b07030c0a0602
GLOBL ·yuvShuf<>(SB), RODATA|NOPTR, $16
