package vp8

import (
	"bytes"
	"image"
	"math"
	"math/rand"
	"testing"

	xwebp "golang.org/x/image/webp"
)

// wrapWebP 把裸 VP8 帧包进最小的 RIFF/WEBP 容器，供只认容器的解码器使用。
//
// wrapWebP wraps a bare VP8 frame in a minimal RIFF/WEBP container for decoders
// that expect one.
func wrapWebP(frame []byte) []byte {
	pad := len(frame) & 1
	out := make([]byte, 0, 20+len(frame)+pad)
	put32 := func(v uint32) {
		out = append(out, byte(v), byte(v>>8), byte(v>>16), byte(v>>24))
	}
	out = append(out, 'R', 'I', 'F', 'F')
	put32(uint32(12 + len(frame) + pad))
	out = append(out, 'W', 'E', 'B', 'P')
	out = append(out, 'V', 'P', '8', ' ')
	put32(uint32(len(frame)))
	out = append(out, frame...)
	if pad != 0 {
		out = append(out, 0)
	}
	return out
}

// testImage 生成一张带渐变与纹理的 4:2:0 图像，避免纯色掩盖变换错误。
//
// testImage builds a 4:2:0 image with a gradient and some texture, so a flat
// colour cannot hide a transform bug.
func testImage(w, h int) *image.YCbCr {
	img := image.NewYCbCr(image.Rect(0, 0, w, h), image.YCbCrSubsampleRatio420)
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			img.Y[y*img.YStride+x] = uint8((x*3 + y*5) & 0xff)
		}
	}
	cw, ch := (w+1)/2, (h+1)/2
	for y := 0; y < ch; y++ {
		for x := 0; x < cw; x++ {
			img.Cb[y*img.CStride+x] = uint8(128 + 40*math.Sin(float64(x)/3))
			img.Cr[y*img.CStride+x] = uint8(128 + 40*math.Cos(float64(y)/4))
		}
	}
	return img
}

// psnr 计算两幅 4:2:0 图像在三个平面上的峰值信噪比。色度也计入，否则色度
// 通道的错误会被完全掩盖。
//
// psnr computes the peak signal to noise ratio over all three planes. Chroma
// counts too, otherwise an error in the chroma channels stays invisible.
func psnr(a, b *image.YCbCr) float64 {
	var se float64
	n := 0
	add := func(x, y byte) {
		d := float64(x) - float64(y)
		se += d * d
		n++
	}
	for y := 0; y < a.Rect.Dy(); y++ {
		for x := 0; x < a.Rect.Dx(); x++ {
			add(a.Y[y*a.YStride+x], b.Y[y*b.YStride+x])
		}
	}
	cw, ch := (a.Rect.Dx()+1)/2, (a.Rect.Dy()+1)/2
	for y := 0; y < ch; y++ {
		for x := 0; x < cw; x++ {
			add(a.Cb[y*a.CStride+x], b.Cb[y*b.CStride+x])
			add(a.Cr[y*a.CStride+x], b.Cr[y*b.CStride+x])
		}
	}
	if se == 0 {
		return math.Inf(1)
	}
	mse := se / float64(n)
	return 10 * math.Log10(255*255/mse)
}

// TestEncodeRoundTrip 编码后用本库解码器解回，检查尺寸与质量。
//
// TestEncodeRoundTrip encodes and decodes with this package's own decoder,
// checking the dimensions and the resulting quality.
func TestEncodeRoundTrip(t *testing.T) {
	for _, tc := range []struct {
		w, h    int
		quality int
		minPSNR float64
	}{
		{16, 16, 90, 34},
		{64, 48, 90, 34},
		{65, 47, 85, 30},
		{120, 90, 75, 26},
		{200, 150, 95, 36},
	} {
		src := testImage(tc.w, tc.h)
		data, err := Encode(src, tc.quality)
		if err != nil {
			t.Fatalf("%dx%d: encode: %v", tc.w, tc.h, err)
		}
		got, err := Decode(data)
		if err != nil {
			t.Fatalf("%dx%d: decode: %v", tc.w, tc.h, err)
		}
		if got.Rect.Dx() != tc.w || got.Rect.Dy() != tc.h {
			t.Fatalf("%dx%d: decoded %dx%d", tc.w, tc.h, got.Rect.Dx(), got.Rect.Dy())
		}
		if p := psnr(src, got); p < tc.minPSNR {
			t.Fatalf("%dx%d q=%d: psnr %.1f dB below %.1f", tc.w, tc.h, tc.quality, p, tc.minPSNR)
		} else {
			t.Logf("%dx%d q=%d: %d bytes, psnr %.1f dB", tc.w, tc.h, tc.quality, len(data), p)
		}
	}
}

// TestEncodeIndependentDecoder 用 x/image/webp 解码同一份码流，逐像素比对本库
// 解码器的结果。两个独立实现给出相同像素，说明码流本身合法。
//
// TestEncodeIndependentDecoder decodes the same stream with x/image/webp and
// compares pixel by pixel against this package's decoder. Two independent
// implementations agreeing means the stream itself is well formed.
func TestEncodeIndependentDecoder(t *testing.T) {
	src := testImage(96, 72)
	data, err := Encode(src, 88)
	if err != nil {
		t.Fatalf("encode: %v", err)
	}
	ours, err := Decode(data)
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	img, err := xwebp.Decode(bytes.NewReader(wrapWebP(data)))
	if err != nil {
		t.Fatalf("x/image/webp decode: %v", err)
	}
	ref, ok := img.(*image.YCbCr)
	if !ok {
		t.Fatalf("x/image/webp returned %T", img)
	}
	if ref.Rect.Dx() != ours.Rect.Dx() || ref.Rect.Dy() != ours.Rect.Dy() {
		t.Fatalf("size mismatch: %v vs %v", ref.Rect, ours.Rect)
	}
	for y := 0; y < ours.Rect.Dy(); y++ {
		for x := 0; x < ours.Rect.Dx(); x++ {
			a := ours.Y[y*ours.YStride+x]
			b := ref.Y[y*ref.YStride+x]
			if a != b {
				t.Fatalf("luma mismatch at (%d,%d): %d != %d", x, y, a, b)
			}
		}
	}
	cw, ch := (ours.Rect.Dx()+1)/2, (ours.Rect.Dy()+1)/2
	for y := 0; y < ch; y++ {
		for x := 0; x < cw; x++ {
			if ours.Cb[y*ours.CStride+x] != ref.Cb[y*ref.CStride+x] ||
				ours.Cr[y*ours.CStride+x] != ref.Cr[y*ref.CStride+x] {
				t.Fatalf("chroma mismatch at (%d,%d)", x, y)
			}
		}
	}
}

// TestEncodeHighQuality 在最高质量下检查重建与源几乎一致。
//
// TestEncodeHighQuality checks that the reconstruction nearly matches the
// source at the highest quality.
func TestEncodeHighQuality(t *testing.T) {
	src := testImage(64, 64)
	data, err := Encode(src, 100)
	if err != nil {
		t.Fatalf("encode: %v", err)
	}
	got, err := Decode(data)
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	if p := psnr(src, got); p < 40 {
		t.Fatalf("quality 100: psnr %.1f dB, want >= 40", p)
	} else {
		t.Logf("quality 100: %d bytes, psnr %.1f dB", len(data), p)
	}
}

// TestEncodeFlatImage 用纯色图像检查整帧都走 skip 的情形。
//
// TestEncodeFlatImage covers a frame where every macroblock is skipped.
func TestEncodeFlatImage(t *testing.T) {
	src := image.NewYCbCr(image.Rect(0, 0, 32, 32), image.YCbCrSubsampleRatio420)
	for i := range src.Y {
		src.Y[i] = 100
	}
	for i := range src.Cb {
		src.Cb[i] = 128
		src.Cr[i] = 128
	}
	data, err := Encode(src, 80)
	if err != nil {
		t.Fatalf("encode: %v", err)
	}
	got, err := Decode(data)
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	for y := 0; y < 32; y++ {
		for x := 0; x < 32; x++ {
			if v := got.Y[y*got.YStride+x]; v != 100 {
				t.Fatalf("pixel (%d,%d) = %d, want 100", x, y, v)
			}
		}
	}
}

// TestEncodeRejectsWrongSubsampling 确认非 4:2:0 输入被拒绝。
//
// TestEncodeRejectsWrongSubsampling checks that non 4:2:0 input is rejected.
func TestEncodeRejectsWrongSubsampling(t *testing.T) {
	img := image.NewYCbCr(image.Rect(0, 0, 16, 16), image.YCbCrSubsampleRatio444)
	if _, err := Encode(img, 80); err == nil {
		t.Fatal("expected an error for 4:4:4 input")
	}
}

// TestEncodeOddSizes 覆盖非 16 倍数的尺寸与极小图像：这些会把宏块补齐、色度
// 半采样和边缘复制同时推到边界。
//
// TestEncodeOddSizes covers sizes that are not multiples of 16 and very small
// images, which push macroblock padding, chroma half sampling and edge
// replication to their limits at once.
func TestEncodeOddSizes(t *testing.T) {
	for _, sz := range [][2]int{
		{1, 1}, {1, 17}, {17, 1}, {2, 2}, {15, 15}, {16, 16}, {17, 17},
		{31, 33}, {33, 31}, {48, 17}, {17, 48}, {63, 65}, {100, 3},
	} {
		src := testImage(sz[0], sz[1])
		data, err := Encode(src, 85)
		if err != nil {
			t.Fatalf("%dx%d: encode: %v", sz[0], sz[1], err)
		}
		got, err := Decode(data)
		if err != nil {
			t.Fatalf("%dx%d: decode: %v", sz[0], sz[1], err)
		}
		if got.Rect.Dx() != sz[0] || got.Rect.Dy() != sz[1] {
			t.Fatalf("%dx%d: decoded %dx%d", sz[0], sz[1], got.Rect.Dx(), got.Rect.Dy())
		}
		if p := psnr(src, got); p < 25 {
			t.Fatalf("%dx%d: psnr %.1f dB below 25", sz[0], sz[1], p)
		}
	}
}

// TestEncodeRandomContent 用随机内容与随机尺寸反复往返，确认编码器不会产生
// 解码器无法解析的码流。
//
// TestEncodeRandomContent round-trips random content at random sizes, checking
// that the encoder never emits a stream the decoder cannot parse.
func TestEncodeRandomContent(t *testing.T) {
	rng := rand.New(rand.NewSource(4242))
	for i := 0; i < 60; i++ {
		w, h := 1+rng.Intn(80), 1+rng.Intn(80)
		src := image.NewYCbCr(image.Rect(0, 0, w, h), image.YCbCrSubsampleRatio420)
		for j := range src.Y {
			src.Y[j] = uint8(rng.Intn(256))
		}
		for j := range src.Cb {
			src.Cb[j] = uint8(rng.Intn(256))
			src.Cr[j] = uint8(rng.Intn(256))
		}
		q := rng.Intn(101)
		data, err := Encode(src, q)
		if err != nil {
			t.Fatalf("%dx%d q=%d: encode: %v", w, h, q, err)
		}
		got, err := Decode(data)
		if err != nil {
			t.Fatalf("%dx%d q=%d: decode: %v", w, h, q, err)
		}
		if got.Rect.Dx() != w || got.Rect.Dy() != h {
			t.Fatalf("%dx%d: decoded %dx%d", w, h, got.Rect.Dx(), got.Rect.Dy())
		}
	}
}

// TestEncodeReconstructionMatchesDecode 校验编码器内部维护的重建与真正解码出
// 来的像素逐采样一致。这是整个编码器的核心不变量：一旦两者分叉，后续宏块的
// 预测就会建立在错误的参考像素上，码流本身仍然自洽，任何解码器都看不出问题。
//
// TestEncodeReconstructionMatchesDecode checks that the reconstruction the
// encoder keeps matches the decoded samples exactly. That is the encoder's core
// invariant: once the two diverge, later macroblocks predict from the wrong
// reference pixels, and the stream stays self consistent, so no decoder can
// notice.
func TestEncodeReconstructionMatchesDecode(t *testing.T) {
	for _, q := range []int{0, 10, 25, 50, 70, 80, 90, 100} {
		w, h := 64, 48
		src := testImage(w, h)
		e := newEncoder(w, h, src, q)
		data := e.run()
		dec, err := Decode(data)
		if err != nil {
			t.Fatalf("q=%d: decode: %v", q, err)
		}
		for y := 0; y < h; y++ {
			for x := 0; x < w; x++ {
				if got, want := e.d.y.pix[e.d.y.at(x, y)], dec.Y[y*dec.YStride+x]; got != want {
					t.Fatalf("q=%d: luma (%d,%d) encoder %d != decoded %d", q, x, y, got, want)
				}
			}
		}
		cw, ch := (w+1)/2, (h+1)/2
		for y := 0; y < ch; y++ {
			for x := 0; x < cw; x++ {
				if got, want := e.d.cb.pix[e.d.cb.at(x, y)], dec.Cb[y*dec.CStride+x]; got != want {
					t.Fatalf("q=%d: Cb (%d,%d) encoder %d != decoded %d", q, x, y, got, want)
				}
				if got, want := e.d.cr.pix[e.d.cr.at(x, y)], dec.Cr[y*dec.CStride+x]; got != want {
					t.Fatalf("q=%d: Cr (%d,%d) encoder %d != decoded %d", q, x, y, got, want)
				}
			}
		}
	}
}
