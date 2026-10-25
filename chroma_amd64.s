//go:build amd64 && !purego

#include "textflag.h"

// 色度上采样的 AVX2 内核：一次 8 个色度样本 = 16 个输出像素，结果与
// upsampleChromaRowScalar 逐位一致。偶/奇两个输出由同一组中心样本共享
// 已算好的 t = 9*c + 3*v，只有水平 tap 不同。
//
// AVX2 kernel for chroma upsampling: eight chroma samples (16 output pixels)
// per iteration, bit-for-bit identical to upsampleChromaRowScalar. The even
// and the odd output share t = 9*c + 3*v; only the horizontal tap differs.
//
// 偶/奇结果按 16 位存放在 X1/X2，左移 8 位或进 X1 后内存里正好是
// e0 o0 e1 o1 ...，不需要再打包。
//
// The even/odd results live in X1/X2 as words; shifting the odd one left by
// eight and OR-ing into X1 makes the register's bytes read e0 o0 e1 o1 ...,
// so no further packing is needed.
//
// func chromaRowAVX2(dst, centerRow, vertRow *byte, n int)
TEXT ·chromaRowAVX2(SB), NOSPLIT, $0-32
	MOVQ dst+0(FP), DI
	MOVQ centerRow+8(FP), SI
	MOVQ vertRow+16(FP), R8
	MOVQ n+24(FP), CX

	TESTQ CX, CX
	JZ    done

	VPBROADCASTW ·chromaC9<>(SB), X4
	VPBROADCASTW ·chromaC3<>(SB), X5
	VPBROADCASTW ·chromaC8<>(SB), X6

loop:
	VPMOVZXBW (SI), X0   // c[j..j+7]
	VPMOVZXBW (R8), X1   // v[j..j+7]
	VPMULLW   X4, X0, X0 // 9*c
	VPMULLW   X5, X1, X1 // 3*v
	VPADDW    X1, X0, X0 // t = 9*c + 3*v
	VPMOVZXBW -1(SI), X1 // c[j-1..j+6]
	VPMULLW   X5, X1, X1
	VPADDW    X1, X0, X1
	VPMOVZXBW -1(R8), X2 // v[j-1..j+6]
	VPADDW    X2, X1, X1
	VPADDW    X6, X1, X1
	VPSRLW    $4, X1, X1 // 偶像素 / even pixels
	VPMOVZXBW 1(SI), X2  // c[j+1..j+8]
	VPMULLW   X5, X2, X2
	VPADDW    X2, X0, X2
	VPMOVZXBW 1(R8), X3  // v[j+1..j+8]
	VPADDW    X3, X2, X2
	VPADDW    X6, X2, X2
	VPSRLW    $4, X2, X2 // 奇像素 / odd pixels
	VPSLLW    $8, X2, X2
	VPOR      X2, X1, X1
	VMOVDQU   X1, (DI)   // 16 个输出像素 / sixteen output bytes
	ADDQ      $8, SI
	ADDQ      $8, R8
	ADDQ      $16, DI
	SUBQ      $8, CX
	JNZ       loop

done:
	VZEROUPPER
	RET

// 9:3:3:1 滤波的权重与取整偏移。
// The 9:3:3:1 tap weights and the rounding offset.
DATA ·chromaC9<>+0(SB)/2, $9
GLOBL ·chromaC9<>(SB), RODATA|NOPTR, $2
DATA ·chromaC3<>+0(SB)/2, $3
GLOBL ·chromaC3<>(SB), RODATA|NOPTR, $2
DATA ·chromaC8<>+0(SB)/2, $8
GLOBL ·chromaC8<>(SB), RODATA|NOPTR, $2
