package vp8l

import "testing"

// TestColorForwardInverse 直接校验前向与逆向颜色变换互逆。
//
// TestColorForwardInverse checks that the forward and inverse colour transforms
// undo each other.
func TestColorForwardInverse(t *testing.T) {
	const w, h = 33, 21
	pix := make([]uint32, w*h)
	for i := range pix {
		g := byte(i * 7)
		r := byte(i*3 + 100)
		b := byte(i * 11)
		pix[i] = 0xff000000 | uint32(r)<<16 | uint32(g)<<8 | uint32(b)
	}
	res, coeffs := forwardColor(pix, w, h, colorTransformBits)
	back := append([]uint32(nil), res...)
	inverseColor(back, w, h, colorTransformBits, coeffs)
	for i := range pix {
		if back[i] != pix[i] {
			t.Fatalf("pixel %d: got %08x want %08x", i, back[i], pix[i])
		}
	}
}

// TestColorTransformRoundTrip 校验颜色变换：红蓝与绿色强相关的图像用颜色变换
// 后码流更短，解码仍逐像素一致。
//
// TestColorTransformRoundTrip checks the colour transform: an image whose red and
// blue correlate with green gets a shorter stream with the transform, and
// decoding stays pixel exact.
func TestColorTransformRoundTrip(t *testing.T) {
	const w, h = 64, 48
	pix := make([]uint32, w*h)
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			// 加一点扰动让不同颜色超过调色板阈值（256），从而真的走颜色变换；
			// 同时保持红、蓝与绿色的强相关。
			// A little noise pushes the colour count above the palette threshold
			// (256) so the colour transform is used, while red and blue stay
			// strongly correlated with green.
			g := byte(x*3 + y*2 + (x*y)%7)
			r := byte(int(g)*2 + (x*y)%5)
			b := byte(int(g)/2 + (x+y)%3)
			pix[y*w+x] = 0xff000000 | uint32(r)<<16 | uint32(g)<<8 | uint32(b)
		}
	}

	plain, err := encodeStream(pix, w, h, 2, true, false, false)
	if err != nil {
		t.Fatal(err)
	}
	withColor, err := encodeStream(pix, w, h, 2, true, true, false)
	if err != nil {
		t.Fatal(err)
	}
	if len(withColor) >= len(plain) {
		t.Fatalf("colour transform stream is %d bytes, plain %d", len(withColor), len(plain))
	}
	best, err := EncodeARGB(pix, w, h, &Options{Method: 2})
	if err != nil {
		t.Fatal(err)
	}
	if len(best) != len(withColor) {
		t.Fatalf("EncodeARGB kept %d bytes, the colour variant is %d", len(best), len(withColor))
	}
	got, gw, gh, err := DecodeARGB(best)
	if err != nil {
		t.Fatal(err)
	}
	if gw != w || gh != h {
		t.Fatalf("decoded %dx%d, want %dx%d", gw, gh, w, h)
	}
	for i := range pix {
		if got[i] != pix[i] {
			t.Fatalf("pixel %d: got %08x want %08x", i, got[i], pix[i])
		}
	}
}

// TestColorTransformNeverGrows 校验没有相关性的图像不会被颜色变换拖大：
// EncodeARGB 取两种写法中更短的一个。
//
// TestColorTransformNeverGrows checks that an image without correlation is never
// grown by the colour transform: EncodeARGB keeps the shorter of the two.
func TestColorTransformNeverGrows(t *testing.T) {
	const w, h = 32, 32
	pix := make([]uint32, w*h)
	state := uint32(12345)
	for i := range pix {
		state = state*1664525 + 1013904223
		pix[i] = 0xff000000 | state&0x00ffffff
	}
	plain, err := encodeStream(pix, w, h, 2, true, false, false)
	if err != nil {
		t.Fatal(err)
	}
	best, err := EncodeARGB(pix, w, h, &Options{Method: 2})
	if err != nil {
		t.Fatal(err)
	}
	if len(best) > len(plain) {
		t.Fatalf("EncodeARGB grew the stream: %d > %d", len(best), len(plain))
	}
	if _, _, _, err := DecodeARGB(best); err != nil {
		t.Fatal(err)
	}
}
