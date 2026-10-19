package simple_webp

import (
	"bytes"
	"os"
	"testing"

	xwebp "golang.org/x/image/webp"
)

// loadFixture 读取 testdata 下的基准样本文件；文件缺失时跳过当前基准而非失败。
//
// loadFixture reads a benchmark fixture from testdata; a missing file skips
// the current benchmark instead of failing it.
func loadFixture(b *testing.B, name string) []byte {
	b.Helper()
	data, err := os.ReadFile("testdata/" + name)
	if err != nil {
		b.Skipf("no fixture %s: %v", name, err)
	}
	return data
}

// BenchmarkParseOnly 测量容器解析本身：只走 RIFF 结构与头部，不触碰像素数据。
//
// BenchmarkParseOnly measures container parsing alone: it walks the RIFF
// structure and headers without touching pixel data.
func BenchmarkParseOnly(b *testing.B) {
	data := loadFixture(b, "photo.webp")
	b.SetBytes(int64(len(data)))
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		if _, err := ParseWebpBytes(data); err != nil {
			b.Fatal(err)
		}
	}
}

// BenchmarkDecodeLossless 测量完整的 VP8L 无损解码；吞吐按像素数报告。
//
// BenchmarkDecodeLossless measures a full VP8L lossless decode; the
// throughput reported via b.SetBytes is in pixels.
func BenchmarkDecodeLossless(b *testing.B) {
	data := loadFixture(b, "photo.webp")
	w, err := ParseWebpBytes(data)
	if err != nil {
		b.Fatal(err)
	}
	b.SetBytes(int64(w.Width * w.Height))
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if _, err := w.ToImage(); err != nil {
			b.Fatal(err)
		}
	}
}

// BenchmarkDecodeLossy 测量完整的 VP8 有损解码，包含 YUV 到 RGB 的转换。
//
// BenchmarkDecodeLossy measures a full VP8 lossy decode including the YUV
// to RGB conversion.
func BenchmarkDecodeLossy(b *testing.B) {
	data := loadFixture(b, "photo_real.webp")
	w, err := ParseWebpBytes(data)
	if err != nil {
		b.Fatal(err)
	}
	b.SetBytes(int64(w.Width * w.Height))
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if _, err := w.ToImage(); err != nil {
			b.Fatal(err)
		}
	}
}

// BenchmarkXImageLossless 在同一文件上运行参考解码器 golang.org/x/image/webp，
// 作为本包 VP8L 解码速度的对照基线。
//
// BenchmarkXImageLossless runs the reference decoder
// golang.org/x/image/webp on the same file as a baseline for this package's
// VP8L decode speed.
func BenchmarkXImageLossless(b *testing.B) {
	data := loadFixture(b, "photo.webp")
	b.SetBytes(int64(320 * 240))
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if _, err := xwebp.Decode(bytes.NewReader(data)); err != nil {
			b.Fatal(err)
		}
	}
}

// BenchmarkXImageLossy 在有损样本上运行参考解码器，作为 VP8 解码速度的对照基线。
//
// BenchmarkXImageLossy runs the reference decoder on the lossy fixture as a
// baseline for VP8 decode speed.
func BenchmarkXImageLossy(b *testing.B) {
	data := loadFixture(b, "photo_real.webp")
	b.SetBytes(int64(400 * 300))
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if _, err := xwebp.Decode(bytes.NewReader(data)); err != nil {
			b.Fatal(err)
		}
	}
}

// BenchmarkEncodeLossless 测量默认选项下对照片类图像的无损编码。
//
// BenchmarkEncodeLossless measures lossless encoding of a photographic image
// with default options.
func BenchmarkEncodeLossless(b *testing.B) {
	data := loadFixture(b, "photo.webp")
	w, err := ParseWebpBytes(data)
	if err != nil {
		b.Fatal(err)
	}
	img, err := w.ToImage()
	if err != nil {
		b.Fatal(err)
	}
	b.SetBytes(int64(w.Width * w.Height))
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if _, err := EncodeBytes(img, nil); err != nil {
			b.Fatal(err)
		}
	}
}
