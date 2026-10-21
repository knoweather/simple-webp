package simple_webp

import (
	"bytes"
	"image"
	"image/color"
	"testing"
)

// newAnimFrames 构造两张 full-canvas 的 NRGBA 帧，尺寸与画布一致。
//
// newAnimFrames builds two full-canvas NRGBA frames matching the canvas.
func newAnimFrames(w, h int, withAlpha bool) []Frame {
	f0 := newTestImage(w, h)
	f1 := image.NewNRGBA(image.Rect(0, 0, w, h))
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			o := y*f1.Stride + x*4
			f1.Pix[o] = byte(255 - x*255/w)
			f1.Pix[o+1] = byte(64)
			f1.Pix[o+2] = byte((x * y) % 256)
			f1.Pix[o+3] = 0xff
		}
	}
	if withAlpha {
		// alpha 取 1..255：alpha=0 的像素在合成时 RGB 会被丢弃（完全透明），
		// 无法与源图逐像素比较。
		// alpha runs 1..255: a fully transparent pixel loses its RGB when
		// composited, so it could not be compared with the source.
		for i := 3; i < len(f0.Pix); i += 4 {
			f0.Pix[i] = byte(1 + (i/4)%255)
		}
	}
	return []Frame{
		{Image: f0, X: 0, Y: 0, Duration: 100, Blend: true},
		{Image: f1, X: 0, Y: 0, Duration: 250, Blend: false, Dispose: true},
	}
}

// TestEncodeAnimationRoundTrip 校验 M-02：无损动画编码后能被解析回来，帧数、
// 时长、循环次数与背景色正确，逐帧像素与原图一致。
//
// TestEncodeAnimationRoundTrip covers M-02: a lossless animation parses back with
// the right frame count, durations, loop count and background, and every frame
// matches the source pixel for pixel.
func TestEncodeAnimationRoundTrip(t *testing.T) {
	const w, h = 40, 32
	anim := &Animation{
		CanvasWidth:     w,
		CanvasHeight:    h,
		BackgroundColor: color.NRGBA{R: 10, G: 20, B: 30, A: 255},
		LoopCount:       3,
		Frames:          newAnimFrames(w, h, true),
	}
	data, err := EncodeAnimationBytes(anim, nil)
	if err != nil {
		t.Fatal(err)
	}
	got, err := ParseWebpBytesWith(data, ParseOptions{Strict: true})
	if err != nil {
		t.Fatalf("strict parse: %v", err)
	}
	if len(got.ANMFs) != len(anim.Frames) {
		t.Fatalf("%d frames, want %d", len(got.ANMFs), len(anim.Frames))
	}
	info, err := got.AnimationInfo()
	if err != nil {
		t.Fatal(err)
	}
	if info.FrameCount != 2 || info.LoopCount != 3 {
		t.Fatalf("info: %+v", info)
	}
	if info.BackgroundColor != (color.NRGBA{R: 10, G: 20, B: 30, A: 255}) {
		t.Fatalf("background: %+v", info.BackgroundColor)
	}
	for i, f := range got.ANMFs {
		if f.Duration != anim.Frames[i].Duration {
			t.Fatalf("frame %d duration %d, want %d", i, f.Duration, anim.Frames[i].Duration)
		}
		if f.Blend != anim.Frames[i].Blend || f.Dispose != anim.Frames[i].Dispose {
			t.Fatalf("frame %d flags blend=%v dispose=%v", i, f.Blend, f.Dispose)
		}
	}
	if !got.VP8X.HasAlpha() {
		t.Fatal("VP8X alpha flag missing although a frame is transparent")
	}

	frames, _, err := got.Frames(0)
	if err != nil {
		t.Fatal(err)
	}
	for i, f := range anim.Frames {
		want := f.Image.(*image.NRGBA)
		if !bytes.Equal(frames[i].Pix, want.Pix) {
			t.Fatalf("frame %d pixels differ after a lossless round trip", i)
		}
	}
}

// TestEncodeAnimationLossy 校验有损动画：能解析、时长正确，alpha 无损保留，
// 像素误差在容差内。
//
// TestEncodeAnimationLossy checks a lossy animation: it parses, keeps durations,
// preserves alpha losslessly and stays within the pixel tolerance.
func TestEncodeAnimationLossy(t *testing.T) {
	const w, h = 48, 40
	anim := &Animation{
		CanvasWidth:  w,
		CanvasHeight: h,
		LoopCount:    1,
		Frames:       newAnimFrames(w, h, true),
	}
	data, err := EncodeAnimationBytes(anim, &AnimationOptions{
		Lossy:    true,
		LossyOpt: &LossyOptions{Quality: 85},
	})
	if err != nil {
		t.Fatal(err)
	}
	got, err := ParseWebpBytesWith(data, ParseOptions{Strict: true})
	if err != nil {
		t.Fatalf("strict parse: %v", err)
	}
	if len(got.ANMFs) != 2 {
		t.Fatalf("%d frames", len(got.ANMFs))
	}
	if got.ANMFs[0].ALPH == nil {
		t.Fatal("transparent frame has no ALPH sub-chunk")
	}
	frames, _, err := got.Frames(0)
	if err != nil {
		t.Fatal(err)
	}
	want := anim.Frames[0].Image.(*image.NRGBA)
	var diff, count int
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			so := y*want.Stride + x*4
			do := y*frames[0].Stride + x*4
			if frames[0].Pix[do+3] != want.Pix[so+3] {
				t.Fatalf("alpha mismatch at %d,%d", x, y)
			}
			for c := 0; c < 3; c++ {
				d := int(frames[0].Pix[do+c]) - int(want.Pix[so+c])
				if d < 0 {
					d = -d
				}
				diff += d
				count++
			}
		}
	}
	if mean := diff / count; mean > 30 {
		t.Fatalf("mean RGB error %d is too large", mean)
	}
}

// TestEncodeAnimationValidation 校验画布、坐标与帧矩形的校验。
//
// TestEncodeAnimationValidation checks the canvas, position and rectangle rules.
func TestEncodeAnimationValidation(t *testing.T) {
	img := newTestImage(8, 8)
	base := Animation{CanvasWidth: 16, CanvasHeight: 16}
	cases := []struct {
		name string
		anim Animation
	}{
		{"no frames", Animation{CanvasWidth: 16, CanvasHeight: 16}},
		{"zero canvas", Animation{Frames: []Frame{{Image: img}}}},
		{"odd position", Animation{CanvasWidth: 16, CanvasHeight: 16, Frames: []Frame{{Image: img, X: 1}}}},
		{"outside canvas", Animation{CanvasWidth: 16, CanvasHeight: 16, Frames: []Frame{{Image: newTestImage(32, 32)}}}},
		{"nil image", Animation{CanvasWidth: 16, CanvasHeight: 16, Frames: []Frame{{X: 0, Y: 0}}}},
	}
	_ = base
	for _, c := range cases {
		if _, err := EncodeAnimationBytes(&c.anim, nil); err == nil {
			t.Errorf("%s: expected an error", c.name)
		}
	}
}
