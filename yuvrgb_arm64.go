//go:build arm64 && !purego

package simple_webp

// arm64 上 YUV→NRGBA 仍走标量路径：NEON 版需要 32 位通道的饱和收窄
// （VQMOVN/VQXTN）和钳位（VMIN/VMAX），而 Go 的 arm64 汇编器没有这几条
// 指令，只能用 WORD 硬写机器码，在没有 ARM 硬件验证的情况下不值得冒险。
//
// YUV to NRGBA stays scalar on arm64: the NEON version needs 32-bit saturating
// narrowing (VQMOVN/VQXTN) and clamping (VMIN/VMAX), which Go's arm64
// assembler does not provide; the only alternative is hand-encoded WORD
// instructions, not worth shipping without ARM hardware to verify on.
func yuvRowToNRGBA(dst, yRow, uRow, vRow []byte) {
	yuvRowWithKernel(dst, yRow, uRow, vRow, nil)
}
