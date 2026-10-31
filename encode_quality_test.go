package simple_webp

import (
	"image"
	"math"
	"testing"
)

// qualityTestImage 生成质量回归用的合成内容。三种内容覆盖不同的压缩特性：
// gradient 是极端平坦（预测几乎完美，走单候选剪枝），edges 是锐利边缘（预测
// 也准，但残差集中在边界），noise 是高频纹理（预测很差，必须跑完整候选表）。
//
// qualityTestImage builds the synthetic content used by the quality guard. The
// three kinds cover different compression characteristics: gradient is
// extremely flat (the prediction is nearly perfect and takes the single
// candidate), edges has sharp boundaries (also well predicted, but the residual
// concentrates on the edges), and noise is high frequency texture (badly
// predicted, so it runs the whole candidate list).
func qualityTestImage(kind string, w, h int) *image.NRGBA {
	img := image.NewNRGBA(image.Rect(0, 0, w, h))
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			var r, g, b float64
			switch kind {
			case "noise":
				nz := math.Sin(float64(x)*12.9898+float64(y)*78.233) * 43758.5453
				nz -= math.Floor(nz)
				r = nz*180 + 40
				nz2 := math.Sin(float64(x)*39.3468+float64(y)*11.135) * 24634.6345
				nz2 -= math.Floor(nz2)
				g = nz2*160 + 50
				b = 255 - nz*140
			case "gradient":
				r = float64(x) / float64(w) * 255
				g = float64(y) / float64(h) * 255
				b = 128 + 60*math.Sin(float64(x+y)/40)
			case "edges":
				if (x/16+y/16)%2 == 0 {
					r, g, b = 235, 235, 235
				} else {
					r, g, b = 20, 40, 90
				}
			}
			i := img.PixOffset(x, y)
			img.Pix[i] = uint8(math.Max(0, math.Min(255, r)))
			img.Pix[i+1] = uint8(math.Max(0, math.Min(255, g)))
			img.Pix[i+2] = uint8(math.Max(0, math.Min(255, b)))
			img.Pix[i+3] = 255
		}
	}
	return img
}

// TestEncodeQualityGuard 锁定有损编码在合成内容上的体积上限与质量下限。
//
// 4x4 亮度的候选剪枝（luma4Candidates / luma4FlatSAD）与估算路径的改写都会
// 改变模式选择或码流，这个测试防止它们悄悄退化成「体积明显变大」或「质量
// 明显变差」。阈值取实测值上浮 1%、PSNR 下浮 0.2 dB，足以吸收常量微调，又能
// 拦住真正的回退。
//
// 数值来自 2026-10-31 在 amd64 上的实测（候选剪枝与 AVX2 变换落地后）。改动
// 编码器时若体积与质量都变好，请同步收紧这些阈值。
//
// TestEncodeQualityGuard pins the size ceiling and the quality floor of the
// lossy encoder on synthetic content.
//
// The 4x4 and 16x16 luma candidate pruning (luma4Candidates / luma4FlatSAD /
// y16Candidates), the rewritten estimation path and the AVX2 transform all
// change mode selection or the bitstream; this test keeps them from silently
// degrading into "visibly larger files" or "visibly worse quality". The
// thresholds are the measured values plus one percent on size and minus 0.2 dB
// on PSNR, loose enough for constant tweaks and tight enough to catch a real
// regression.
//
// The numbers are the measurements taken on amd64 on 2026-10-31, after the
// pruning and the AVX2 transform landed. When a change improves both size and
// quality, tighten them.
func TestEncodeQualityGuard(t *testing.T) {
	cases := []struct {
		kind    string
		w, h    int
		q       int
		maxSize int
		minPSNR float64
	}{
		{"gradient", 640, 480, 50, 3005, 42.63},
		{"gradient", 640, 480, 75, 3793, 44.26},
		{"edges", 640, 480, 75, 6017, 32.35},
		{"noise", 640, 480, 75, 155938, 15.54},
		{"noise", 400, 300, 50, 49668, 15.37},
		{"noise", 400, 300, 90, 89759, 15.60},
	}
	for _, c := range cases {
		src := qualityTestImage(c.kind, c.w, c.h)
		data, err := EncodeLossyBytes(src, &LossyOptions{Quality: c.q})
		if err != nil {
			t.Fatalf("%s %dx%d q=%d: %v", c.kind, c.w, c.h, c.q, err)
		}
		if len(data) > c.maxSize {
			t.Errorf("%s %dx%d q=%d: %d bytes exceeds the %d byte ceiling",
				c.kind, c.w, c.h, c.q, len(data), c.maxSize)
		}
		wp, err := ParseWebpBytes(data)
		if err != nil {
			t.Fatalf("%s %dx%d q=%d: parse: %v", c.kind, c.w, c.h, c.q, err)
		}
		got, err := wp.ToImage()
		if err != nil {
			t.Fatalf("%s %dx%d q=%d: decode: %v", c.kind, c.w, c.h, c.q, err)
		}
		if psnr := rgbPSNR(src, got); psnr < c.minPSNR {
			t.Errorf("%s %dx%d q=%d: psnr %.2f dB is below the %.2f dB floor",
				c.kind, c.w, c.h, c.q, psnr, c.minPSNR)
		}
	}
}
