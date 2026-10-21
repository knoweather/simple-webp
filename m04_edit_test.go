package simple_webp

import (
	"image"
	"testing"
)

// newTestImage 构造一张 width×height 的 NRGBA 渐变图，用于容器编辑测试。
//
// newTestImage builds a width times height NRGBA gradient for the editing tests.
func newTestImage(width, height int) *image.NRGBA {
	img := image.NewNRGBA(image.Rect(0, 0, width, height))
	for y := 0; y < height; y++ {
		for x := 0; x < width; x++ {
			o := y*img.Stride + x*4
			img.Pix[o] = byte(x * 255 / width)
			img.Pix[o+1] = byte(y * 255 / height)
			img.Pix[o+2] = byte((x + y) * 3)
			img.Pix[o+3] = 0xff
		}
	}
	return img
}

// TestNewWebpSetImageLossless 校验 NewWebp + SetImage 从零构造无损文件，
// 写回后尺寸与像素都与源图一致。
//
// TestNewWebpSetImageLossless checks NewWebp + SetImage building a lossless file
// from scratch: size and pixels match the source after writing back.
func TestNewWebpSetImageLossless(t *testing.T) {
	src := newTestImage(37, 23)
	w := NewWebp(0, 0)
	if err := w.SetImage(src, nil); err != nil {
		t.Fatal(err)
	}
	out, err := w.MarshalBytes()
	if err != nil {
		t.Fatal(err)
	}
	got, err := ParseWebpBytes(out)
	if err != nil {
		t.Fatal(err)
	}
	if got.Width != 37 || got.Height != 23 {
		t.Fatalf("size %dx%d, want 37x23", got.Width, got.Height)
	}
	img, err := got.ToImage()
	if err != nil {
		t.Fatal(err)
	}
	compareNRGBA(t, "new-webp-lossless", img, src.Pix)
}

// TestEmptyWebpCannotBeWritten 校验空容器写出会报错。
//
// TestEmptyWebpCannotBeWritten checks that writing an empty container is an error.
func TestEmptyWebpCannotBeWritten(t *testing.T) {
	w := NewWebp(16, 16)
	if _, err := w.MarshalBytes(); err == nil {
		t.Fatal("an empty container was serialized")
	}
}

// TestSetImageReplacesPixels 校验在已解析的文件上替换像素：画布跟随新图，
// 原有元数据保留，解码缓存失效。
//
// TestSetImageReplacesPixels checks replacing the pixels of a parsed file: the
// canvas follows the new image, metadata survives, and the decode cache is
// dropped.
func TestSetImageReplacesPixels(t *testing.T) {
	src := readFixture(t, "lossy_sharp.webp")
	base, err := ParseWebpBytes(src)
	if err != nil {
		t.Fatal(err)
	}
	base.EXIF = &EXIF{Data: []byte("Exif\x00\x00m04")}
	if _, err := base.Image(); err != nil {
		t.Fatal(err)
	}
	oldBounds := base.Bounds()

	replacement := newTestImage(64, 48)
	if err := base.SetImage(replacement, nil); err != nil {
		t.Fatal(err)
	}
	if base.Bounds() == oldBounds {
		t.Fatal("bounds did not change after SetImage")
	}
	img, err := base.Image()
	if err != nil {
		t.Fatal(err)
	}
	compareNRGBA(t, "set-image-cache", img, replacement.Pix)

	out, err := base.MarshalBytes()
	if err != nil {
		t.Fatal(err)
	}
	got, err := ParseWebpBytesWith(out, ParseOptions{Strict: true})
	if err != nil {
		t.Fatalf("strict parse: %v", err)
	}
	if got.Width != 64 || got.Height != 48 {
		t.Fatalf("size %dx%d, want 64x48", got.Width, got.Height)
	}
	if got.EXIF == nil || string(got.EXIF.Data) != "Exif\x00\x00m04" {
		t.Fatalf("metadata lost: %+v", got.EXIF)
	}
}

// TestSetImageOnAnimatedRejected 校验动画容器不能直接换成静态图。
//
// TestSetImageOnAnimatedRejected checks that an animated container rejects a
// still image replacement.
func TestSetImageOnAnimatedRejected(t *testing.T) {
	w, err := ParseWebpBytes(readFixture(t, "anim.webp"))
	if err != nil {
		t.Fatal(err)
	}
	if err := w.SetImage(newTestImage(8, 8), nil); err == nil {
		t.Fatal("SetImage accepted an animated container")
	}
}

// TestRemoveAndAddFrame 校验帧编辑：删掉中间帧后输出只剩其余帧、时长正确，
// 追加一帧后又能按严格模式解析。
//
// TestRemoveAndAddFrame checks frame editing: dropping the middle frame leaves
// the others with their durations, and appending a frame yields a stream that
// still parses in strict mode.
func TestRemoveAndAddFrame(t *testing.T) {
	w, err := ParseWebpBytes(readFixture(t, "anim.webp"))
	if err != nil {
		t.Fatal(err)
	}
	if len(w.ANMFs) < 3 {
		t.Skipf("only %d frames", len(w.ANMFs))
	}
	durations := []int{w.ANMFs[0].Duration, w.ANMFs[1].Duration, w.ANMFs[2].Duration}
	if err := w.RemoveFrame(1); err != nil {
		t.Fatal(err)
	}
	out, err := w.MarshalBytes()
	if err != nil {
		t.Fatal(err)
	}
	got, err := ParseWebpBytesWith(out, ParseOptions{Strict: true})
	if err != nil {
		t.Fatalf("strict parse: %v", err)
	}
	if len(got.ANMFs) != len(w.ANMFs) {
		t.Fatalf("%d frames in the output, want %d", len(got.ANMFs), len(w.ANMFs))
	}
	if got.ANMFs[0].Duration != durations[0] || got.ANMFs[1].Duration != durations[2] {
		t.Fatalf("durations %d,%d, want %d,%d",
			got.ANMFs[0].Duration, got.ANMFs[1].Duration, durations[0], durations[2])
	}

	// 追加一帧：码流取自一张真实的有损图，帧头按画布尺寸声明。
	// Append a frame: the bitstream comes from a real lossy image, the ANMF
	// header declares the canvas size.
	still, err := ParseWebpBytes(readFixture(t, "lossy_sharp.webp"))
	if err != nil || still.VP8 == nil {
		t.Fatalf("fixture: %v", err)
	}
	frame := &ANMF{
		X: 0, Y: 0,
		Width: w.Width, Height: w.Height,
		Duration: 33,
		VP8:      still.VP8,
	}
	if err := w.AddFrame(frame); err != nil {
		t.Fatal(err)
	}
	out, err = w.MarshalBytes()
	if err != nil {
		t.Fatal(err)
	}
	got, err = ParseWebpBytesWith(out, ParseOptions{Strict: true})
	if err != nil {
		t.Fatalf("strict parse after AddFrame: %v", err)
	}
	if len(got.ANMFs) != len(w.ANMFs) {
		t.Fatalf("%d frames after AddFrame, want %d", len(got.ANMFs), len(w.ANMFs))
	}
	if last := got.ANMFs[len(got.ANMFs)-1]; last.Duration != 33 {
		t.Fatalf("appended frame duration %d, want 33", last.Duration)
	}
}

// TestFramePositionValidation 校验 AddFrame 拒绝奇数坐标与越界矩形。
//
// TestFramePositionValidation checks that AddFrame rejects an odd position and a
// frame outside the canvas.
func TestFramePositionValidation(t *testing.T) {
	still, err := ParseWebpBytes(readFixture(t, "lossy_sharp.webp"))
	if err != nil || still.VP8 == nil {
		t.Fatalf("fixture: %v", err)
	}
	w := NewWebp(32, 32)
	if err := w.AddFrame(&ANMF{X: 1, Y: 0, Width: 32, Height: 32, VP8: still.VP8}); err == nil {
		t.Fatal("odd frame position was accepted")
	}
	if err := w.AddFrame(&ANMF{X: 0, Y: 0, Width: 32, Height: 32}); err == nil {
		t.Fatal("a frame without image data was accepted")
	}
	if err := w.AddFrame(&ANMF{X: 0, Y: 0, Width: 64, Height: 64, VP8: still.VP8}); err == nil {
		t.Fatal("a frame outside the canvas was accepted")
	}
}
