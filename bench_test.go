package simple_webp

import (
	"bytes"
	"os"
	"testing"

	xwebp "golang.org/x/image/webp"
)

func loadFixture(b *testing.B, name string) []byte {
	b.Helper()
	data, err := os.ReadFile("testdata/" + name)
	if err != nil {
		b.Skipf("no fixture %s: %v", name, err)
	}
	return data
}

// BenchmarkParseOnly measures the container parse, without touching pixels.
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

// BenchmarkDecodeLossless measures a full VP8L decode.
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

// BenchmarkDecodeLossy measures a full VP8 decode including the YUV to RGB
// conversion.
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

// BenchmarkXImageLossless is the reference decoder on the same file.
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

// BenchmarkXImageLossy is the reference decoder on the lossy file.
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

// BenchmarkEncodeLossless measures the encoder on a photographic image.
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
