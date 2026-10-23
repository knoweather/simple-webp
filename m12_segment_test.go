package simple_webp

import (
	"bytes"
	"image"
	"image/color"
	"testing"

	xwebp "golang.org/x/image/webp"
)

// textureTestImage 构造一张左半低幅值纹理、右半全幅噪声的图像。低幅值纹理
// 是逐段量化唯一有实质收益的内容：预测滤不掉它，但系数很小，平坦侧粗化后
// 大量系数归零；而纯平坦区在本编码器里直接 skip，粗量化无利可图。
//
// textureTestImage builds an image whose left half carries low amplitude
// texture and whose right half full amplitude noise. Low amplitude texture is
// the only content where per-segment quantization pays for real: prediction
// cannot remove it, yet the coefficients are small, so a coarser quantizer on
// the flat side zeroes many of them; a purely flat region is skipped outright
// by this encoder and gains nothing from coarsening.
func textureTestImage(w, h int) *image.NRGBA {
	img := image.NewNRGBA(image.Rect(0, 0, w, h))
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			var c color.NRGBA
			if x < w/2 {
				v := 128 + (x/3+y/5)%7 - 3
				c = color.NRGBA{R: uint8(v), G: uint8(v), B: uint8(v), A: 255}
			} else {
				c = color.NRGBA{R: uint8(x * 7), G: uint8(y * 13), B: uint8(x * y), A: 255}
			}
			img.SetNRGBA(x, y, c)
		}
	}
	return img
}

// TestLossySegmentRoundTrip 覆盖 M-12：逐段量化在低幅值纹理内容上体积必须
// 严格小于不分段，质量让让不超过 0.75 dB，严格模式可解析，且编码确定。
//
// TestLossySegmentRoundTrip covers M-12: on low amplitude texture the
// segmented encode must be strictly smaller than the unsegmented one, give
// back at most 0.75 dB of quality, parse under strict mode and encode
// deterministically.
func TestLossySegmentRoundTrip(t *testing.T) {
	src := textureTestImage(192, 128)
	plain, err := EncodeLossyBytes(src, &LossyOptions{Quality: 60})
	if err != nil {
		t.Fatalf("plain encode: %v", err)
	}
	seg, err := EncodeLossyBytes(src, &LossyOptions{Quality: 60, Segment: true})
	if err != nil {
		t.Fatalf("segment encode: %v", err)
	}
	if len(seg) >= len(plain) {
		t.Fatalf("segmented encode did not win: %d bytes vs %d", len(seg), len(plain))
	}
	if again, err := EncodeLossyBytes(src, &LossyOptions{Quality: 60, Segment: true}); err != nil {
		t.Fatalf("segment encode again: %v", err)
	} else if !bytes.Equal(again, seg) {
		t.Fatal("segmented encode is not deterministic")
	}

	wp, err := ParseWebpBytesWith(seg, ParseOptions{Strict: true})
	if err != nil {
		t.Fatalf("strict parse of the segmented stream: %v", err)
	}
	img, err := wp.ToImage()
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	psnrSeg := rgbPSNR(src, img)

	wplain, err := ParseWebpBytes(plain)
	if err != nil {
		t.Fatalf("parse plain: %v", err)
	}
	imgPlain, err := wplain.ToImage()
	if err != nil {
		t.Fatalf("decode plain: %v", err)
	}
	psnrPlain := rgbPSNR(src, imgPlain)
	if psnrSeg < psnrPlain-0.75 {
		t.Fatalf("segmented psnr %.2f dB is more than 0.75 dB below plain %.2f dB", psnrSeg, psnrPlain)
	}
	t.Logf("texture 192x128 q=60: plain %d bytes / %.2f dB, segmented %d bytes / %.2f dB",
		len(plain), psnrPlain, len(seg), psnrSeg)
}

// TestLossySegmentOnFixtures 在真实有损 fixture 上验证逐段量化不会帮倒忙：
// 体积不劣于不分段（竞赛保证），且与参考解码器的差距不劣于不分段的差距
// （解码器间的固有差异来自上采样，与分段无关）。
//
// TestLossySegmentOnFixtures checks on real lossy fixtures that per-segment
// quantization does no harm: the size never grows over the unsegmented encode
// (the race guarantees it) and the disagreement with the reference decoder is
// no worse than the unsegmented one's (the inherent gap between the two
// decoders comes from upsampling and has nothing to do with segmentation).
func TestLossySegmentOnFixtures(t *testing.T) {
	for _, name := range []string{"lossy_smooth.webp", "lossy_noise.webp", "lossy_sharp.webp"} {
		src, err := ParseWebpBytes(readFixture(t, name))
		if err != nil {
			t.Fatalf("%s: parse: %v", name, err)
		}
		img, err := src.ToImage()
		if err != nil {
			t.Fatalf("%s: decode: %v", name, err)
		}
		nrgba, ok := img.(*image.NRGBA)
		if !ok {
			t.Fatalf("%s: decoded %T, want *image.NRGBA", name, img)
		}
		plain, err := EncodeLossyBytes(nrgba, &LossyOptions{Quality: 70})
		if err != nil {
			t.Fatalf("%s: plain encode: %v", name, err)
		}
		seg, err := EncodeLossyBytes(nrgba, &LossyOptions{Quality: 70, Segment: true})
		if err != nil {
			t.Fatalf("%s: segment encode: %v", name, err)
		}
		if len(seg) > len(plain) {
			t.Fatalf("%s: segmented encode is larger: %d vs %d bytes", name, len(seg), len(plain))
		}

		// 与参考解码器的差距：分段流不得明显差于不分段流。
		// The gap against the reference decoder: the segmented stream must
		// not disagree noticeably more than the unsegmented one.
		gap := func(data []byte) float64 {
			ref, err := xwebp.Decode(bytes.NewReader(data))
			if err != nil {
				t.Fatalf("%s: reference decode: %v", name, err)
			}
			w, err := ParseWebpBytes(data)
			if err != nil {
				t.Fatalf("%s: parse: %v", name, err)
			}
			got, err := w.ToImage()
			if err != nil {
				t.Fatalf("%s: decode: %v", name, err)
			}
			return rgbPSNR(ref, got)
		}
		if g := gap(seg); g < gap(plain)-1 {
			t.Fatalf("%s: segmented stream disagrees with the reference decoder by %.1f dB more", name, gap(plain)-g)
		}
		if _, err := ParseWebpBytesWith(seg, ParseOptions{Strict: true}); err != nil {
			t.Fatalf("%s: strict parse of the segmented stream: %v", name, err)
		}
		t.Logf("%s q=70: plain %d bytes, segmented %d bytes", name, len(plain), len(seg))
	}
}

// TestLossySegmentWithRateControl 验证逐段量化与目标体积码率控制同时打开时
// 仍能产出可解析、质量达标的码流。
//
// TestLossySegmentWithRateControl checks that per-segment quantization and the
// target size rate control work together: the stream still parses and the
// quality holds up.
func TestLossySegmentWithRateControl(t *testing.T) {
	src := textureTestImage(128, 96)
	decodePSNR := func(data []byte) float64 {
		w, err := ParseWebpBytesWith(data, ParseOptions{Strict: true})
		if err != nil {
			t.Fatalf("strict parse: %v", err)
		}
		img, err := w.ToImage()
		if err != nil {
			t.Fatalf("decode: %v", err)
		}
		return rgbPSNR(src, img)
	}
	seg, err := EncodeLossyBytes(src, &LossyOptions{Quality: 75, Segment: true, TargetBytes: 8000})
	if err != nil {
		t.Fatalf("encode: %v", err)
	}
	plain, err := EncodeLossyBytes(src, &LossyOptions{Quality: 75, TargetBytes: 8000})
	if err != nil {
		t.Fatalf("plain encode: %v", err)
	}
	psnrSeg, psnrPlain := decodePSNR(seg), decodePSNR(plain)
	if psnrSeg < psnrPlain-1 {
		t.Fatalf("segmented psnr %.1f dB is more than 1 dB below plain %.1f dB", psnrSeg, psnrPlain)
	}
	if len(seg) > len(plain) {
		t.Fatalf("segmented encode is larger: %d vs %d bytes", len(seg), len(plain))
	}
	t.Logf("target 8000 bytes: plain %d bytes / %.1f dB, segmented %d bytes / %.1f dB",
		len(plain), psnrPlain, len(seg), psnrSeg)
}
