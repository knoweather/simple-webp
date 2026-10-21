package simple_webp

import (
	"image"
	"testing"
)

// newAlphaImage 构造一张带渐隐 alpha 的 NRGBA 图。
//
// newAlphaImage builds an NRGBA image with a fading alpha gradient.
func newAlphaImage(width, height int) *image.NRGBA {
	img := image.NewNRGBA(image.Rect(0, 0, width, height))
	for y := 0; y < height; y++ {
		for x := 0; x < width; x++ {
			o := y*img.Stride + x*4
			img.Pix[o] = byte(x * 255 / width)
			img.Pix[o+1] = byte(y * 255 / height)
			img.Pix[o+2] = byte(128)
			img.Pix[o+3] = byte(x * 255 / width)
		}
	}
	return img
}

// checkAlphaRoundTrip 编码、解析、解码，并校验 alpha 与源图逐字节一致、RGB 的
// 平均绝对误差在给定上限内。
//
// checkAlphaRoundTrip encodes, parses and decodes, then checks that alpha matches
// the source byte for byte and the mean absolute RGB error stays under the limit.
func checkAlphaRoundTrip(t *testing.T, src *image.NRGBA, opt *LossyOptions, wantCompression uint8) {
	t.Helper()
	data, err := EncodeLossyBytes(src, opt)
	if err != nil {
		t.Fatalf("encode: %v", err)
	}
	w, err := ParseWebpBytesWith(data, ParseOptions{Strict: true})
	if err != nil {
		t.Fatalf("strict parse: %v", err)
	}
	if w.VP8X == nil || !w.VP8X.HasAlpha() {
		t.Fatal("VP8X alpha flag missing on a transparent image")
	}
	if w.ALPH == nil {
		t.Fatal("ALPH chunk missing on a transparent image")
	}
	if w.ALPH.Compression != wantCompression {
		t.Fatalf("alpha compression %d, want %d", w.ALPH.Compression, wantCompression)
	}
	img, err := w.ToImage()
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	got := img.(*image.NRGBA)
	want := src
	if got.Bounds() != want.Bounds() {
		t.Fatalf("size %v, want %v", got.Bounds(), want.Bounds())
	}
	var diff, count int
	for y := 0; y < want.Bounds().Dy(); y++ {
		for x := 0; x < want.Bounds().Dx(); x++ {
			so := y*want.Stride + x*4
			do := y*got.Stride + x*4
			if got.Pix[do+3] != want.Pix[so+3] {
				t.Fatalf("alpha mismatch at %d,%d: got %d want %d", x, y, got.Pix[do+3], want.Pix[so+3])
			}
			for c := 0; c < 3; c++ {
				d := int(got.Pix[do+c]) - int(want.Pix[so+c])
				if d < 0 {
					d = -d
				}
				diff += d
				count++
			}
		}
	}
	if mean := diff / count; mean > 30 {
		t.Fatalf("mean RGB error %d is too large for quality 80", mean)
	}
}

// TestLossyAlphaRoundTrip 校验 M-01：带透明度的有损编码产出 VP8X+ALPH+VP8，
// alpha 无损，四种滤波方式都能正确还原。
//
// TestLossyAlphaRoundTrip covers M-01: a transparent lossy encode produces
// VP8X+ALPH+VP8 with lossless alpha, and every filter mode survives the round
// trip.
func TestLossyAlphaRoundTrip(t *testing.T) {
	src := newAlphaImage(64, 48)
	for _, filter := range []uint8{ALPHFilterNone, ALPHFilterHorizontal, ALPHFilterVertical, ALPHFilterGradient} {
		checkAlphaRoundTrip(t, src, &LossyOptions{Quality: 80, AlphaFilter: filter}, ALPHCompressionLossless)
	}
}

// TestLossyAlphaUncompressed 校验 C=0 的原样存储路径。
//
// TestLossyAlphaUncompressed checks the C=0 verbatim path.
func TestLossyAlphaUncompressed(t *testing.T) {
	checkAlphaRoundTrip(t, newAlphaImage(32, 32), &LossyOptions{Quality: 80, AlphaUncompressed: true}, ALPHCompressionNone)
}

// TestLossyOpaqueStaysSimple 校验不透明输入不写 VP8X/ALPH，保持简单格式。
//
// TestLossyOpaqueStaysSimple checks that an opaque input writes no VP8X/ALPH and
// keeps the simple format.
func TestLossyOpaqueStaysSimple(t *testing.T) {
	src := newTestImage(40, 30)
	data, err := EncodeLossyBytes(src, &LossyOptions{Quality: 70})
	if err != nil {
		t.Fatal(err)
	}
	w, err := ParseWebpBytes(data)
	if err != nil {
		t.Fatal(err)
	}
	if w.VP8X != nil || w.ALPH != nil {
		t.Fatalf("opaque input produced an extended container: VP8X=%v ALPH=%v", w.VP8X, w.ALPH)
	}
	if names := chunkNames(t, data); len(names) != 1 || names[0] != VP8Chunk {
		t.Fatalf("chunk order: %v", names)
	}
}

// TestLossyAlphaRejectsBadFilter 校验非法滤波编号被拒绝。
//
// TestLossyAlphaRejectsBadFilter checks that an invalid filter number is rejected.
func TestLossyAlphaRejectsBadFilter(t *testing.T) {
	if _, err := EncodeLossyBytes(newAlphaImage(8, 8), &LossyOptions{AlphaFilter: 9}); err == nil {
		t.Fatal("an invalid alpha filter was accepted")
	}
}
