package simple_webp

import (
	"image"
	"image/color"
	"testing"
)

// m13Gradient builds a 64x64 NRGBA gradient used by the option-validation tests.
//
// m13Gradient 构造 64×64 的 NRGBA 渐变，供选项校验测试使用。
func m13Gradient(w, h int) *image.NRGBA {
	img := image.NewNRGBA(image.Rect(0, 0, w, h))
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			c := color.NRGBA{
				R: uint8(x * 255 / (w - 1)),
				G: uint8(y * 255 / (h - 1)),
				B: uint8((x + y) / 2),
				A: 255,
			}
			img.SetNRGBA(x, y, c)
		}
	}
	return img
}

// TestEncodeMethodValidation checks that an out-of-range Method is reported as an
// error instead of falling back to the default, and that the supported values work.
//
// TestEncodeMethodValidation 检查越界 Method 报错而非静默回落到默认值，并确认
// 支持的取值可用。
func TestEncodeMethodValidation(t *testing.T) {
	src := m13Gradient(64, 64)
	for _, tc := range []struct {
		opt     *EncodeOptions
		wantErr bool
		what    string
	}{
		{&EncodeOptions{Method: 5}, true, "Method 5"},
		{&EncodeOptions{Method: -1}, true, "Method -1"},
		{&EncodeOptions{Method: 0}, false, "Method 0"},
		{&EncodeOptions{Method: 2}, false, "Method 2"},
		{nil, false, "nil options"},
	} {
		data, err := EncodeBytes(src, tc.opt)
		if tc.wantErr {
			if err == nil {
				t.Fatalf("%s: expected an error, got %d bytes", tc.what, len(data))
			}
			t.Logf("%s: %v", tc.what, err)
			continue
		}
		if err != nil {
			t.Fatalf("%s: unexpected error: %v", tc.what, err)
		}
		if len(data) == 0 {
			t.Fatalf("%s: empty output", tc.what)
		}
	}
}

// TestEncodeLossyQualityRange checks that a Quality outside 0..100 returns an error
// instead of being clamped.
//
// TestEncodeLossyQualityRange 检查 Quality 超出 0..100 返回错误而不是被钳制。
func TestEncodeLossyQualityRange(t *testing.T) {
	src := m13Gradient(64, 64)
	for _, tc := range []struct {
		quality int
		wantErr bool
	}{
		{101, true},
		{-1, true},
		{0, false},
		{100, false},
	} {
		_, err := EncodeLossyBytes(src, &LossyOptions{Quality: tc.quality, QualityZero: true})
		if tc.wantErr {
			if err == nil {
				t.Fatalf("quality %d: expected an error", tc.quality)
			}
			t.Logf("quality %d: %v", tc.quality, err)
			continue
		}
		if err != nil {
			t.Fatalf("quality %d: unexpected error: %v", tc.quality, err)
		}
	}
}

// TestEncodeLossyQualityZeroRoundTrip encodes with QualityZero, which means quality
// 0, and decodes the result back through the package API.
//
// TestEncodeLossyQualityZeroRoundTrip 用 QualityZero（即质量 0）编码，再用本库
// 解回来验证容器与图像都能读。
func TestEncodeLossyQualityZeroRoundTrip(t *testing.T) {
	src := m13Gradient(64, 64)
	data, err := EncodeLossyBytes(src, &LossyOptions{QualityZero: true})
	if err != nil {
		t.Fatalf("quality zero: encode: %v", err)
	}
	w, err := ParseWebpBytes(data)
	if err != nil {
		t.Fatalf("quality zero: parse: %v", err)
	}
	if w.VP8 == nil {
		t.Fatal("quality zero: no VP8 chunk")
	}
	img, err := w.ToImage()
	if err != nil {
		t.Fatalf("quality zero: decode: %v", err)
	}
	if img.Bounds().Dx() != 64 || img.Bounds().Dy() != 64 {
		t.Fatalf("quality zero: decoded %v", img.Bounds())
	}
	t.Logf("quality zero: %d bytes", len(data))
}

// TestEncodeLossyQualityZeroIsSmaller compares quality 0 with quality 95 on the same
// image, so quality 0 has to be reachable and must produce fewer bytes.
//
// TestEncodeLossyQualityZeroIsSmaller 对同一张图比较质量 0 与质量 95：质量 0
// 必须可达，且字节数更少。
func TestEncodeLossyQualityZeroIsSmaller(t *testing.T) {
	src := m13Gradient(64, 64)
	q0, err := EncodeLossyBytes(src, &LossyOptions{QualityZero: true})
	if err != nil {
		t.Fatalf("quality 0: %v", err)
	}
	q95, err := EncodeLossyBytes(src, &LossyOptions{Quality: 95})
	if err != nil {
		t.Fatalf("quality 95: %v", err)
	}
	if len(q0) >= len(q95) {
		t.Fatalf("quality 0 gave %d bytes, quality 95 gave %d", len(q0), len(q95))
	}
	t.Logf("q0 %d bytes, q95 %d bytes", len(q0), len(q95))
}
