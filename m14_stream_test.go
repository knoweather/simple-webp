package simple_webp

import (
	"bytes"
	"image"
	"testing"
	"testing/iotest"
)

// TestDecodeWebpStream 覆盖 M-14 的流式入口：DecodeWebp 从 io.Reader 直接
// 解析解码，结果与 ParseWebpBytes + ToImage 一致；一次只返回一个字节的
// Reader 也能正常工作（证明解析不依赖整块读取）。
//
// TestDecodeWebpStream covers the streaming entry of M-14: DecodeWebp parses
// and decodes straight from an io.Reader with the same result as
// ParseWebpBytes plus ToImage, and a reader that hands out one byte at a time
// works as well (proving the parse does not depend on whole reads).
func TestDecodeWebpStream(t *testing.T) {
	for _, name := range []string{"gradient.webp", "alpha.webp", "anim.webp", "lossy_smooth.webp"} {
		src := readFixture(t, name)
		want, err := ParseWebpBytes(src)
		if err != nil {
			t.Fatalf("%s: parse: %v", name, err)
		}
		wantImg, err := want.ToImage()
		if err != nil {
			t.Fatalf("%s: decode: %v", name, err)
		}

		got, err := DecodeWebp(bytes.NewReader(src), DecodeOptions{})
		if err != nil {
			t.Fatalf("%s: DecodeWebp: %v", name, err)
		}
		if !samePixels(wantImg, got) {
			t.Fatalf("%s: DecodeWebp decoded different pixels", name)
		}

		slow, err := DecodeWebp(iotest.OneByteReader(bytes.NewReader(src)), DecodeOptions{})
		if err != nil {
			t.Fatalf("%s: DecodeWebp over a one byte reader: %v", name, err)
		}
		if !samePixels(wantImg, slow) {
			t.Fatalf("%s: one byte reader decoded different pixels", name)
		}

		// 选项穿透：YCbCr 与 RGBA 布局照旧生效。
		// The options pass through: YCbCr and RGBA layouts still work.
		if name == "lossy_smooth.webp" {
			ycc, err := DecodeWebp(bytes.NewReader(src), DecodeOptions{Format: PixelFormatYCbCr})
			if err != nil {
				t.Fatalf("%s: YCbCr: %v", name, err)
			}
			if _, ok := ycc.(*image.YCbCr); !ok {
				t.Fatalf("%s: YCbCr decode returned %T", name, ycc)
			}
		}
	}

	// 坏流仍然报错 / a broken stream still fails
	if _, err := DecodeWebp(bytes.NewReader([]byte("not a webp at all")), DecodeOptions{}); err == nil {
		t.Fatal("DecodeWebp accepted a non-webp stream")
	}
}

// samePixels 逐像素比较两幅同尺寸图像。
//
// samePixels compares two images of the same size pixel by pixel.
func samePixels(a, b image.Image) bool {
	ab, bb := a.Bounds(), b.Bounds()
	if ab.Dx() != bb.Dx() || ab.Dy() != bb.Dy() {
		return false
	}
	for y := 0; y < ab.Dy(); y++ {
		for x := 0; x < ab.Dx(); x++ {
			if a.At(ab.Min.X+x, ab.Min.Y+y) != b.At(bb.Min.X+x, bb.Min.Y+y) {
				return false
			}
		}
	}
	return true
}

// TestDecodeIntoBuffer 覆盖 M-14 的缓冲复用：DecodeInto 解出的像素与
// ToImage 一致（含带 alpha 的有损图），尺寸不符、nil 与动画都报错，
// 且复用同一块缓冲连续解码结果稳定。
//
// TestDecodeIntoBuffer covers the buffer reuse of M-14: DecodeInto produces
// the same pixels as ToImage (including a lossy image with alpha), rejects a
// size mismatch, a nil target and animations, and decoding repeatedly into
// one buffer is stable.
func TestDecodeIntoBuffer(t *testing.T) {
	for _, name := range []string{"gradient.webp", "alpha.webp", "lossy_alpha.webp", "lossy_smooth.webp"} {
		w, err := ParseWebpBytes(readFixture(t, name))
		if err != nil {
			t.Fatalf("%s: parse: %v", name, err)
		}
		if w.HasAnimation {
			continue
		}
		want, err := w.ToImage()
		if err != nil {
			t.Fatalf("%s: decode: %v", name, err)
		}
		dst := image.NewNRGBA(image.Rect(0, 0, w.Width, w.Height))
		if err := w.DecodeInto(dst); err != nil {
			t.Fatalf("%s: DecodeInto: %v", name, err)
		}
		if !samePixels(want, dst) {
			t.Fatalf("%s: DecodeInto decoded different pixels", name)
		}
		// 复用同一块缓冲再解一遍，结果必须稳定。
		// Decoding again into the same buffer must be stable.
		for i := range dst.Pix {
			dst.Pix[i] = 0
		}
		if err := w.DecodeInto(dst); err != nil {
			t.Fatalf("%s: DecodeInto again: %v", name, err)
		}
		if !samePixels(want, dst) {
			t.Fatalf("%s: reused buffer decoded different pixels", name)
		}

		// 尺寸不符与 nil 目标 / a size mismatch and a nil target
		if err := w.DecodeInto(image.NewNRGBA(image.Rect(0, 0, w.Width+1, w.Height))); err == nil {
			t.Fatalf("%s: DecodeInto accepted a wrong sized buffer", name)
		}
		if err := w.DecodeInto(nil); err == nil {
			t.Fatalf("%s: DecodeInto accepted a nil buffer", name)
		}
	}

	// 动画文件拒绝缓冲解码 / an animation refuses the buffer decode
	anim, err := ParseWebpBytes(readFixture(t, "anim.webp"))
	if err != nil {
		t.Fatal(err)
	}
	if err := anim.DecodeInto(image.NewNRGBA(image.Rect(0, 0, anim.Width, anim.Height))); err == nil {
		t.Fatal("DecodeInto accepted an animation")
	}
}
