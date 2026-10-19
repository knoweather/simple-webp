package simple_webp

import (
	"image"
	"testing"
)

// fillPseudoRandom 用可复现的伪随机字节填充一片平面。
//
// fillPseudoRandom fills a plane with reproducible pseudo-random bytes.
func fillPseudoRandom(p []byte, seed byte) {
	for i := range p {
		// xorshift 步进，保证可复现且相邻样本差异大，能暴露下标错位。
		// xorshift step: reproducible and with large differences between
		// neighbours, so an index slip shows up.
		seed ^= seed << 1
		seed ^= seed >> 1
		seed ^= seed << 5
		p[i] = seed
	}
}

// TestUpsampleChromaRowMatchesReference 用未重构的 upsampleChroma（逐像素）
// 作为参考实现，逐位校验行级 upsampleChromaRow 的输出。覆盖多种宽度
// （奇/偶、跨 SIMD 边界、1 像素极端情形）与高度，确保钳位与邻居选择一致。
//
// TestUpsampleChromaRowMatchesReference pins the row-level upsampleChromaRow
// bit for bit against the un-refactored per-pixel upsampleChroma. It covers
// many widths (odd/even, across SIMD boundaries, the 1-pixel extreme) and
// heights so clamping and neighbour selection stay consistent.
func TestUpsampleChromaRowMatchesReference(t *testing.T) {
	const h = 9 // 奇数高度：覆盖 k+dk 上下两端的钳位 / odd height: clamps both ends of k+dk
	widths := []int{1, 2, 3, 4, 5, 6, 7, 8, 9, 15, 16, 17, 18, 31, 32, 33, 34, 47, 48, 49, 63, 64, 65, 96, 128, 129}
	for _, w := range widths {
		w := w
		t.Run(image.Rect(0, 0, w, h).String(), func(t *testing.T) {
			cw := (w + 1) / 2
			ch := (h + 1) / 2
			src := image.NewYCbCr(image.Rect(0, 0, w, h), image.YCbCrSubsampleRatio420)
			fillPseudoRandom(src.Y, 0x11)
			fillPseudoRandom(src.Cb, 0x5d)
			fillPseudoRandom(src.Cr, 0xa7)

			planes := []struct {
				name string
				p    []byte
			}{
				{"Cb", src.Cb},
				{"Cr", src.Cr},
			}
			got := make([]byte, w)
			for y := 0; y < h; y++ {
				for _, pl := range planes {
					center, vert := chromaRowPair(pl.p, src.CStride, cw, ch, y)
					upsampleChromaRow(got, center, vert, cw, w)
					for x := 0; x < w; x++ {
						want := upsampleChroma(pl.p, src.CStride, cw, ch, x, y)
						if int(got[x]) != want {
							t.Fatalf("%s y=%d x=%d: got %d, want %d (w=%d)", pl.name, y, x, got[x], want, w)
						}
					}
				}
			}
		})
	}
}
