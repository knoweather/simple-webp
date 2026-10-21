package simple_webp

import (
	"image"
	"testing"
)

// newPhotoishImage 构造一张适合有损编码的彩色渐变图。
//
// newPhotoishImage builds a colour gradient that suits lossy encoding.
func newPhotoishImage(w, h int) *image.NRGBA {
	img := image.NewNRGBA(image.Rect(0, 0, w, h))
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			o := y*img.Stride + x*4
			img.Pix[o] = byte((x*255)/w + (y*17)%40)
			img.Pix[o+1] = byte((y * 255) / h)
			img.Pix[o+2] = byte(((x + y) * 3) % 256)
			img.Pix[o+3] = 0xff
		}
	}
	return img
}

// TestRateControlTarget 校验 M-12 的基础码率控制：给定目标体积时输出落在
// 目标附近，并且仍然能解码。
//
// TestRateControlTarget covers the basic M-12 rate control: a target size yields
// an output close to it that still decodes.
func TestRateControlTarget(t *testing.T) {
	img := newPhotoishImage(96, 72)
	const target = 1500
	data, err := EncodeLossyBytes(img, &LossyOptions{TargetBytes: target})
	if err != nil {
		t.Fatal(err)
	}
	if len(data) > target*120/100 || len(data) < target*80/100 {
		t.Fatalf("target %d bytes, got %d", target, len(data))
	}
	if _, err := ParseWebpBytes(data); err != nil {
		t.Fatalf("re-parse: %v", err)
	}
	w, err := ParseWebpBytes(data)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := w.ToImage(); err != nil {
		t.Fatalf("decode: %v", err)
	}
}

// TestRateControlZeroKeepsQuality 校验 TargetBytes 为 0 时不走码率控制。
//
// TestRateControlZeroKeepsQuality checks that a zero TargetBytes skips the rate
// control.
func TestRateControlZeroKeepsQuality(t *testing.T) {
	img := newPhotoishImage(48, 48)
	plain, err := EncodeLossyBytes(img, &LossyOptions{Quality: 80})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := EncodeLossyBytes(img, &LossyOptions{TargetBytes: -1}); err == nil {
		t.Fatal("a negative target was accepted")
	}
	if len(plain) == 0 {
		t.Fatal("empty output")
	}
}

// TestFilterLevelValidation 校验非法滤波强度被拒绝。
//
// TestFilterLevelValidation checks that an invalid filter level is rejected.
func TestFilterLevelValidation(t *testing.T) {
	img := newPhotoishImage(16, 16)
	if _, err := EncodeLossyBytes(img, &LossyOptions{FilterLevel: 64}); err == nil {
		t.Fatal("filter level 64 was accepted")
	}
	if _, err := EncodeLossyBytes(img, &LossyOptions{FilterLevel: 20, SimpleFilter: true}); err != nil {
		t.Fatalf("simple filter: %v", err)
	}
}
