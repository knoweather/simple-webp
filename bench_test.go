package simple_webp

import (
	"bytes"
	"image"
	"math"
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

// radarImage 生成用于基准的合成雷达图：一个高斯回波，textured 为真
// 时叠加确定性伪随机噪声。平滑内容压缩比极高（接近 0.1%），纹理内容约 3.6%，
// 分别代表有损编码的最好与最坏情况。
//
// 有损编码的耗时对内容极其敏感——候选剪枝在平滑内容上几乎全中，在纹理内容上
// 基本失效——所以两个都要测，只看一个会得出偏乐观的结论。
//
// radarImage builds the synthetic radar frame used by the benchmarks: one
// Gaussian echo, with deterministic pseudo-random noise added when textured is
// set. The smooth content compresses extremely well (near 0.1%) and the
// textured one to about 3.6%, standing for the best and the worst case of lossy
// encoding.
//
// Lossy encode time is very sensitive to content: candidate pruning almost
// always wins on smooth content and barely helps on textured content, so both
// have to be measured. Looking at one alone gives a flattering answer.
func radarImage(n int, textured bool) *image.NRGBA {
	img := image.NewNRGBA(image.Rect(0, 0, n, n))
	c := float64(n) / 2
	scale := float64(n) / 451.0
	for y := 0; y < n; y++ {
		for x := 0; x < n; x++ {
			fx, fy := float64(x), float64(y)
			d := math.Hypot(fx-c, fy-c*1.05)
			v := 45 * math.Exp(-d*d/(2*(120*scale)*(120*scale)))
			if textured {
				nz := math.Sin(fx*12.9898+fy*78.233) * 43758.5453
				nz -= math.Floor(nz)
				v += nz * 12
			}
			i := img.PixOffset(x, y)
			img.Pix[i] = uint8(math.Min(255, v*5))
			img.Pix[i+1] = uint8(math.Min(255, v*2))
			img.Pix[i+2] = uint8(math.Max(0, math.Min(255, 255-v*4)))
			img.Pix[i+3] = 255
		}
	}
	return img
}

// benchRadarEncode 是 radarImage 两种内容的公共基准体。
//
// benchRadarEncode is the shared benchmark body for the two radarImage
// contents.
func benchRadarEncode(b *testing.B, n int, textured bool) {
	img := radarImage(n, textured)
	b.SetBytes(int64(n * n))
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if _, err := EncodeLossyBytes(img, &LossyOptions{Quality: 75}); err != nil {
			b.Fatal(err)
		}
	}
}

// BenchmarkEncodeLossyRadar451 测 451x451 合成图，q=75。
//
// BenchmarkEncodeLossyRadar451 measures the 451x451 synthetic frame at
// q=75.
func BenchmarkEncodeLossyRadar451(b *testing.B) { benchRadarEncode(b, 451, false) }

// BenchmarkEncodeLossyRadar451Texture 是同尺寸的纹理版本。
//
// BenchmarkEncodeLossyRadar451Texture is the textured variant at the same size.
func BenchmarkEncodeLossyRadar451Texture(b *testing.B) { benchRadarEncode(b, 451, true) }

// BenchmarkEncodeLossyRadar3000 测 3000x3000 的平滑图。单次约两秒，跑基准时
// 请用 -benchtime=3x 之类的参数限制次数。
//
// BenchmarkEncodeLossyRadar3000 measures the 3000x3000 smooth frame. One
// iteration takes about two seconds, so limit the run with something like
// -benchtime=3x.
func BenchmarkEncodeLossyRadar3000(b *testing.B) { benchRadarEncode(b, 3000, false) }

// BenchmarkEncodeLossyRadar3000Texture 是同尺寸的纹理版本，代表最坏情况。
//
// BenchmarkEncodeLossyRadar3000Texture is the textured variant at the same
// size, the worst case.
func BenchmarkEncodeLossyRadar3000Texture(b *testing.B) { benchRadarEncode(b, 3000, true) }
