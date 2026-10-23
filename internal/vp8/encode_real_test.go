package vp8

import (
	"bytes"
	"image"
	"os"
	"testing"

	xwebp "golang.org/x/image/webp"
)

// TestEncodeRealContent 拿真实素材（锐边、平滑渐变、噪声、真实照片裁切）过一遍
// 编码，再用 x/image/webp 解同一份码流逐像素核对。真实内容比合成图案更容易
// 触发模式上下文与非零标记传播上的问题。
//
// TestEncodeRealContent encodes real material (sharp edges, a smooth
// gradient, noise, a real photo crop) and checks the stream against
// x/image/webp pixel by pixel. Real content exercises the mode contexts and
// the propagation of the non-zero flags more than synthetic patterns do.
func TestEncodeRealContent(t *testing.T) {
	for _, name := range []string{"lossy_sharp", "lossy_smooth", "lossy_noise", "photo_real"} {
		raw, err := os.ReadFile("../../testdata/" + name + ".vp8")
		if err != nil {
			t.Skip(err)
		}
		src, err := Decode(raw)
		if err != nil {
			t.Fatal(err)
		}
		for _, q := range []int{40, 70, 90} {
			e := newEncoder(src.Rect.Dx(), src.Rect.Dy(), src, q)
			data, err := e.run()
			if err != nil {
				t.Fatalf("q=%d: run: %v", q, err)
			}
			got, err := Decode(data)
			if err != nil {
				t.Fatalf("%s q=%d: decode: %v", name, q, err)
			}
			img, err := xwebp.Decode(bytes.NewReader(wrapWebP(data)))
			if err != nil {
				t.Fatalf("%s q=%d: x/image/webp decode: %v", name, q, err)
			}
			ref, ok := img.(*image.YCbCr)
			if !ok {
				t.Fatalf("%s q=%d: x/image/webp returned %T", name, q, img)
			}
			for y := 0; y < got.Rect.Dy(); y++ {
				for x := 0; x < got.Rect.Dx(); x++ {
					if a, b := got.Y[y*got.YStride+x], ref.Y[y*ref.YStride+x]; a != b {
						t.Fatalf("%s q=%d: luma mismatch at (%d,%d): %d != %d",
							name, q, x, y, a, b)
					}
				}
			}
			cw, ch := (got.Rect.Dx()+1)/2, (got.Rect.Dy()+1)/2
			for y := 0; y < ch; y++ {
				for x := 0; x < cw; x++ {
					if got.Cb[y*got.CStride+x] != ref.Cb[y*ref.CStride+x] ||
						got.Cr[y*got.CStride+x] != ref.Cr[y*ref.CStride+x] {
						t.Fatalf("%s q=%d: chroma mismatch at (%d,%d)", name, q, x, y)
					}
				}
			}
			t.Logf("%-12s q=%-3d %6d bytes, psnr %.2f dB, %d macroblocks on the 4x4 path",
				name, q, len(data), psnr(src, got), e.y4Blocks)
		}
	}
}
