package simple_webp

import (
	"image"
	"image/color"
	"math"
	"os"
	"path/filepath"
	"testing"
)

// lossyTestImage 生成一张带渐变与色块的 NRGBA 图像。
//
// lossyTestImage builds an NRGBA image with a gradient and some colour patches.
func lossyTestImage(w, h int) *image.NRGBA {
	img := image.NewNRGBA(image.Rect(0, 0, w, h))
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			var c color.NRGBA
			switch {
			case x < w/3:
				c = color.NRGBA{R: uint8(x * 255 / w), G: 40, B: 200, A: 255}
			case x < 2*w/3:
				c = color.NRGBA{R: 30, G: uint8(y * 255 / h), B: 90, A: 255}
			default:
				c = color.NRGBA{R: uint8((x + y) & 0xff), G: uint8((x * 2) & 0xff), B: uint8((y * 3) & 0xff), A: 255}
			}
			img.SetNRGBA(x, y, c)
		}
	}
	return img
}

// lumaPSNR 计算两幅同尺寸图像在亮度上的峰值信噪比。
//
// lumaPSNR computes the peak signal to noise ratio of the luma channel.
func lumaPSNR(a, b image.Image) float64 {
	ab, bb := a.Bounds(), b.Bounds()
	if ab.Dx() != bb.Dx() || ab.Dy() != bb.Dy() {
		return math.Inf(-1)
	}
	var se float64
	n := 0
	for y := 0; y < ab.Dy(); y++ {
		for x := 0; x < ab.Dx(); x++ {
			ar, ag, abl, _ := a.At(ab.Min.X+x, ab.Min.Y+y).RGBA()
			br, bg, bbl, _ := b.At(bb.Min.X+x, bb.Min.Y+y).RGBA()
			ay := 0.299*float64(ar>>8) + 0.587*float64(ag>>8) + 0.114*float64(abl>>8)
			byl := 0.299*float64(br>>8) + 0.587*float64(bg>>8) + 0.114*float64(bbl>>8)
			d := ay - byl
			se += d * d
			n++
		}
	}
	if se == 0 {
		return math.Inf(1)
	}
	return 10 * math.Log10(255*255/(se/float64(n)))
}

// TestEncodeLossyRoundTrip 编码后再解码，检查容器、尺寸与质量。
//
// TestEncodeLossyRoundTrip encodes then decodes, checking the container, the
// dimensions and the quality.
func TestEncodeLossyRoundTrip(t *testing.T) {
	for _, tc := range []struct {
		w, h    int
		quality int
		minPSNR float64
	}{
		{32, 32, 90, 34},
		{64, 48, 90, 34},
		{97, 61, 85, 31},
		{160, 120, 75, 27},
	} {
		src := lossyTestImage(tc.w, tc.h)
		data, err := EncodeLossyBytes(src, &LossyOptions{Quality: tc.quality})
		if err != nil {
			t.Fatalf("%dx%d: encode: %v", tc.w, tc.h, err)
		}
		w, err := ParseWebpBytes(data)
		if err != nil {
			t.Fatalf("%dx%d: parse: %v", tc.w, tc.h, err)
		}
		if w.Width != tc.w || w.Height != tc.h {
			t.Fatalf("%dx%d: container says %dx%d", tc.w, tc.h, w.Width, w.Height)
		}
		if w.VP8 == nil {
			t.Fatalf("%dx%d: no VP8 chunk", tc.w, tc.h)
		}
		img, err := w.ToImage()
		if err != nil {
			t.Fatalf("%dx%d: decode: %v", tc.w, tc.h, err)
		}
		if img.Bounds().Dx() != tc.w || img.Bounds().Dy() != tc.h {
			t.Fatalf("%dx%d: decoded %v", tc.w, tc.h, img.Bounds())
		}
		if p := lumaPSNR(src, img); p < tc.minPSNR {
			t.Fatalf("%dx%d q=%d: psnr %.1f dB below %.1f", tc.w, tc.h, tc.quality, p, tc.minPSNR)
		} else {
			t.Logf("%dx%d q=%d: %d bytes, psnr %.1f dB", tc.w, tc.h, tc.quality, len(data), p)
		}
	}
}

// TestEncodeLossyFromFixture 把真实有损 fixture 解码后重新编码，验证编码器能
// 处理自然图像，而不是只在合成图上成立。
//
// TestEncodeLossyFromFixture decodes a real lossy fixture and re-encodes it, so
// the encoder is exercised on natural imagery rather than synthetic patterns.
func TestEncodeLossyFromFixture(t *testing.T) {
	for _, name := range []string{"photo.webp", "photo_real.webp", "lossy_sharp.webp", "lossy_smooth.webp", "lossy_noise.webp"} {
		path := filepath.Join("testdata", name)
		raw, err := os.ReadFile(path)
		if err != nil {
			continue
		}
		w, err := ParseWebpBytes(raw)
		if err != nil {
			t.Fatalf("%s: parse: %v", name, err)
		}
		dec, err := w.ToImage()
		if err != nil {
			t.Fatalf("%s: decode: %v", name, err)
		}
		out, err := EncodeLossyBytes(dec, &LossyOptions{Quality: 80})
		if err != nil {
			t.Fatalf("%s: encode: %v", name, err)
		}
		w2, err := ParseWebpBytes(out)
		if err != nil {
			t.Fatalf("%s: reparse: %v", name, err)
		}
		back, err := w2.ToImage()
		if err != nil {
			t.Fatalf("%s: redecode: %v", name, err)
		}
		p := lumaPSNR(dec, back)
		t.Logf("%s: %d -> %d bytes, psnr %.1f dB", name, len(raw), len(out), p)
		if p < 25 {
			t.Fatalf("%s: psnr %.1f dB, want >= 25", name, p)
		}
	}
}

// TestEncodeLossyQualityTradesSize 检查质量越高体积越大。
//
// TestEncodeLossyQualityTradesSize checks that a higher quality yields more bytes.
func TestEncodeLossyQualityTradesSize(t *testing.T) {
	src := lossyTestImage(128, 96)
	low, err := EncodeLossyBytes(src, &LossyOptions{Quality: 30})
	if err != nil {
		t.Fatalf("low: %v", err)
	}
	high, err := EncodeLossyBytes(src, &LossyOptions{Quality: 95})
	if err != nil {
		t.Fatalf("high: %v", err)
	}
	if len(low) >= len(high) {
		t.Fatalf("quality 30 gave %d bytes, quality 95 gave %d", len(low), len(high))
	}
	t.Logf("q30 %d bytes, q95 %d bytes", len(low), len(high))
}

// TestEncodeLossyDefaults 检查未指定选项时使用默认质量。
//
// TestEncodeLossyDefaults checks that the default quality applies when no
// options are given.
func TestEncodeLossyDefaults(t *testing.T) {
	src := lossyTestImage(40, 40)
	a, err := EncodeLossyBytes(src, nil)
	if err != nil {
		t.Fatalf("nil options: %v", err)
	}
	b, err := EncodeLossyBytes(src, &LossyOptions{Quality: defaultLossyQuality})
	if err != nil {
		t.Fatalf("explicit default: %v", err)
	}
	if len(a) != len(b) {
		t.Fatalf("nil options gave %d bytes, explicit default gave %d", len(a), len(b))
	}
}

// TestEncodeLossyNilImage 检查空输入被拒绝。
//
// TestEncodeLossyNilImage checks that a nil image is rejected.
func TestEncodeLossyNilImage(t *testing.T) {
	if _, err := EncodeLossyBytes(nil, nil); err == nil {
		t.Fatal("expected an error for a nil image")
	}
}
