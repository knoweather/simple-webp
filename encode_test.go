package simple_webp

import (
	"bytes"
	"image"
	"image/color"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestEncodeRoundTrip 用每种 method 编码每个解码后的测试文件，检查结果
// 重新解码后像素一致。
//
// TestEncodeRoundTrip encodes every decoded fixture at each method and checks
// that the result decodes back to the same pixels.
func TestEncodeRoundTrip(t *testing.T) {
	entries, err := os.ReadDir("testdata")
	if err != nil {
		t.Skipf("no testdata: %v", err)
	}
	for _, entry := range entries {
		name := entry.Name()
		if !strings.HasSuffix(name, ".webp") || strings.HasPrefix(name, "anim") {
			continue
		}
		t.Run(strings.TrimSuffix(name, ".webp"), func(t *testing.T) {
			stream, err := os.ReadFile(filepath.Join("testdata", name))
			if err != nil {
				t.Fatal(err)
			}
			w, err := ParseWebpBytes(stream)
			if err != nil {
				t.Fatal(err)
			}
			src, err := w.ToImage()
			if err != nil {
				t.Fatal(err)
			}
			for _, method := range []int{0, 1, 2} {
				encoded, err := EncodeBytes(src, &EncodeOptions{Lossless: true, Method: method})
				if err != nil {
					t.Fatalf("method %d: %v", method, err)
				}
				back, err := ParseWebpBytes(encoded)
				if err != nil {
					t.Fatalf("method %d: parse: %v", method, err)
				}
				img, err := back.ToImage()
				if err != nil {
					t.Fatalf("method %d: decode: %v", method, err)
				}
				compareNRGBA(t, "method "+string(rune('0'+method)), img, src.(*image.NRGBA).Pix)
				if len(encoded) > len(stream)*40 && len(stream) > 1000 {
					t.Logf("method %d grew from %d to %d bytes", method, len(stream), len(encoded))
				}
			}
		})
	}
}

// TestEncodeSinglePixel 检查最小可能图像（1×1）的编码。
//
// TestEncodeSinglePixel checks the smallest possible image.
func TestEncodeSinglePixel(t *testing.T) {
	src := image.NewNRGBA(image.Rect(0, 0, 1, 1))
	src.Pix[0], src.Pix[1], src.Pix[2], src.Pix[3] = 12, 34, 56, 78
	data, err := EncodeBytes(src, nil)
	if err != nil {
		t.Fatal(err)
	}
	w, err := ParseWebpBytes(data)
	if err != nil {
		t.Fatal(err)
	}
	if w.Width != 1 || w.Height != 1 || !w.HasAlpha {
		t.Fatalf("header: %dx%d alpha=%v", w.Width, w.Height, w.HasAlpha)
	}
	img, err := w.ToImage()
	if err != nil {
		t.Fatal(err)
	}
	compareNRGBA(t, "1x1", img, src.Pix)
}

// TestEncodeUniform 检查大幅纯色图像，用以触发 LZ77 的长匹配路径。
//
// TestEncodeUniform checks a large flat image, which exercises long matches.
func TestEncodeUniform(t *testing.T) {
	src := image.NewNRGBA(image.Rect(0, 0, 200, 150))
	for i := 0; i < len(src.Pix); i += 4 {
		src.Pix[i], src.Pix[i+1], src.Pix[i+2], src.Pix[i+3] = 200, 30, 60, 255
	}
	data, err := EncodeBytes(src, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(data) > 200 {
		t.Fatalf("flat image encoded to %d bytes", len(data))
	}
	img, err := Decode(bytes.NewReader(data))
	if err != nil {
		t.Fatal(err)
	}
	compareNRGBA(t, "uniform", img, src.Pix)
}

// TestEncodeOutputDir 在设置 WEBP_ENCODE_DIR 时把编码结果写盘，供外部验证。
//
// TestEncodeOutputDir writes encoded files for external verification when
// WEBP_ENCODE_DIR is set.
func TestEncodeOutputDir(t *testing.T) {
	dir := os.Getenv("WEBP_ENCODE_DIR")
	if dir == "" {
		t.Skip("WEBP_ENCODE_DIR is not set")
	}
	entries, err := os.ReadDir("testdata")
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		name := entry.Name()
		if !strings.HasSuffix(name, ".webp") || strings.HasPrefix(name, "anim") {
			continue
		}
		stream, err := os.ReadFile(filepath.Join("testdata", name))
		if err != nil {
			t.Fatal(err)
		}
		w, err := ParseWebpBytes(stream)
		if err != nil {
			t.Fatal(err)
		}
		src, err := w.ToImage()
		if err != nil {
			t.Fatal(err)
		}
		for _, method := range []int{1, 2} {
			data, err := EncodeBytes(src, &EncodeOptions{Lossless: true, Method: method})
			if err != nil {
				t.Fatal(err)
			}
			out := filepath.Join(dir, strings.TrimSuffix(name, ".webp")+"_m"+string(rune('0'+method))+".webp")
			if err = os.WriteFile(out, data, 0o644); err != nil {
				t.Fatal(err)
			}
		}
	}
}

// TestLossySharpnessOption 校验 Sharpness 的边界、错误与真实接线：写进帧头的
// sharpness 字段必须随选项变化，而不是被静默丢弃。
func TestLossySharpnessOption(t *testing.T) {
	src := image.NewNRGBA(image.Rect(0, 0, 32, 32))
	for y := 0; y < 32; y++ {
		for x := 0; x < 32; x++ {
			src.SetNRGBA(x, y, color.NRGBA{R: uint8(x * 8), G: uint8(y * 8), B: 128, A: 255})
		}
	}
	encode := func(sharp int) ([]byte, error) {
		return EncodeLossyBytes(src, &LossyOptions{Quality: 75, FilterLevel: 40, Sharpness: sharp})
	}
	for _, bad := range []int{-1, 8} {
		if _, err := encode(bad); err == nil {
			t.Fatalf("sharpness %d accepted", bad)
		}
	}
	sharp, err := encode(0)
	if err != nil {
		t.Fatal(err)
	}
	soft, err := encode(7)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Equal(sharp, soft) {
		t.Fatal("sharpness did not reach the VP8 frame header")
	}
	for _, data := range [][]byte{sharp, soft} {
		w, err := ParseWebpBytes(data)
		if err != nil {
			t.Fatal(err)
		}
		if _, err = w.ToImage(); err != nil {
			t.Fatal(err)
		}
	}
}
