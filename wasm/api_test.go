package wasm

import (
	"bytes"
	"image"
	"image/color"
	"os"
	"path/filepath"
	"testing"

	"github.com/knoweather/simple-webp"
)

// sampleWebp 用主库的编码器在内存里构造一张小 WebP（编码有意不属于 wasm 包的
// 职责——这里只为测试生成解码输入）。
//
// sampleWebp builds a small WebP in memory using the main library's encoder
// (encoding is intentionally not part of the wasm package — it is only used
// here to produce decode input for the test).
func sampleWebp(t *testing.T) []byte {
	t.Helper()
	img := image.NewNRGBA(image.Rect(0, 0, 3, 3))
	for y := 0; y < 3; y++ {
		for x := 0; x < 3; x++ {
			img.SetNRGBA(x, y, color.NRGBA{R: uint8(x * 80), G: uint8(y * 80), B: 200, A: 255})
		}
	}
	// 放一个半透明像素，确保 alpha 平面确实被用到
	// one semi-transparent pixel so the alpha plane is definitely used
	img.SetNRGBA(0, 0, color.NRGBA{R: 10, G: 20, B: 30, A: 128})
	data, err := simple_webp.EncodeBytes(img, &simple_webp.EncodeOptions{Method: 0})
	if err != nil {
		t.Fatalf("encode: %v", err)
	}
	return data
}

// TestDecode 验证 Decode：尺寸 3x3、RGBA 长度为像素数×4、HasAlpha 为真，
// 且左上角像素 (0,0) 解码为直通 alpha 值 {10,20,30,128}。
//
// TestDecode checks Decode: size 3x3, RGBA length equals pixel count times 4,
// HasAlpha is true, and pixel (0,0) decodes to straight-alpha {10,20,30,128}.
func TestDecode(t *testing.T) {
	data := sampleWebp(t)
	res, err := Decode(data)
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	if res.Width != 3 || res.Height != 3 {
		t.Fatalf("size = %dx%d, want 3x3", res.Width, res.Height)
	}
	if len(res.RGBA) != 3*3*4 {
		t.Fatalf("rgba len = %d, want %d", len(res.RGBA), 3*3*4)
	}
	if !res.HasAlpha {
		t.Fatal("HasAlpha = false, want true")
	}
	// 左上角像素 (0,0)：直通 alpha 值 {10,20,30,128}
	// top-left pixel (0,0): straight alpha {10,20,30,128}
	got := res.RGBA[:4]
	want := []byte{10, 20, 30, 128}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("pixel(0,0) = %v, want %v", got, want)
		}
	}
}

// TestInfo 验证 Info：不解码像素即可读出 3x3 尺寸与 HasAlpha 标志。
//
// TestInfo checks Info: it reports the 3x3 size and the HasAlpha flag
// without decoding pixels.
func TestInfo(t *testing.T) {
	data := sampleWebp(t)
	info, err := Info(data)
	if err != nil {
		t.Fatalf("info: %v", err)
	}
	if info.Width != 3 || info.Height != 3 {
		t.Fatalf("size = %dx%d, want 3x3", info.Width, info.Height)
	}
	if !info.HasAlpha {
		t.Fatal("HasAlpha = false, want true")
	}
}

// loadFixture 读取仓库根的 testdata 文件（测试运行在 wasm/ 包目录，故用 ../ 上跳），
// 文件缺失时跳过对应用例。
//
// loadFixture reads a fixture from the repo-root testdata directory (tests run
// in the wasm/ package dir, hence the ../ hop), skipping the case when absent.
func loadFixture(t *testing.T, name string) []byte {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("..", "testdata", name))
	if err != nil {
		t.Skipf("no fixture %s: %v", name, err)
	}
	return b
}

// TestInfoVP8XAnimated 用 anim.webp（VP8X+ANIM+ANMF）验证 Info 的扩展字段：
// hasAnimation/frameCount 与主库解析一致，且没有虚假的 ICC/EXIF/XMP 标志。
//
// TestInfoVP8XAnimated checks Info's extended fields on anim.webp
// (VP8X+ANIM+ANMF): hasAnimation/frameCount match the library parse and there
// are no spurious ICC/EXIF/XMP flags.
func TestInfoVP8XAnimated(t *testing.T) {
	data := loadFixture(t, "anim.webp")
	info, err := Info(data)
	if err != nil {
		t.Fatalf("info: %v", err)
	}
	w, err := simple_webp.ParseWebpBytes(data)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if w.VP8X == nil {
		t.Skip("anim.webp unexpectedly has no VP8X chunk")
	}
	if !info.HasAnimation {
		t.Fatal("HasAnimation = false, want true")
	}
	if info.FrameCount != len(w.ANMFs) {
		t.Fatalf("FrameCount = %d, want %d", info.FrameCount, len(w.ANMFs))
	}
	if info.FrameCount < 1 {
		t.Fatal("FrameCount < 1 for animated file")
	}
	if info.HasICC || info.HasEXIF || info.HasXMP {
		t.Fatalf("unexpected metadata flags: icc=%v exif=%v xmp=%v",
			info.HasICC, info.HasEXIF, info.HasXMP)
	}
}

// TestDecodeVP8XAlpha 用 lossy_alpha.webp（VP8X+ALPH+VP8，有损带 alpha）验证
// Info 读出不带 VP8X 时会跳过；若是 VP8X 则确认 hasAlpha 为真且解码像素完整。
//
// TestDecodeVP8XAlpha uses lossy_alpha.webp (VP8X+ALPH+VP8, lossy with alpha)
// to verify Info and Decode: skip if it is not a VP8X container, otherwise assert
// hasAlpha is true and the decoded RGBA is complete.
func TestDecodeVP8XAlpha(t *testing.T) {
	data := loadFixture(t, "lossy_alpha.webp")
	w, err := simple_webp.ParseWebpBytes(data)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if w.VP8X == nil {
		t.Skip("lossy_alpha.webp is not a VP8X container")
	}
	info, err := Info(data)
	if err != nil {
		t.Fatalf("info: %v", err)
	}
	if !info.HasAlpha {
		t.Fatal("lossy_alpha.webp: HasAlpha = false, want true")
	}
	if info.HasAnimation {
		t.Fatal("lossy_alpha.webp: HasAnimation = true, want false")
	}
	res, err := Decode(data)
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(res.RGBA) != info.Width*info.Height*4 {
		t.Fatalf("rgba len = %d, want %d", len(res.RGBA), info.Width*info.Height*4)
	}
}

// TestDecodeFramesAnimated 验证 DecodeFrames 对动图返回的帧数、时长、尺寸与像素
// 都和主库 Frames() 一致。
//
// TestDecodeFramesAnimated checks that DecodeFrames on an animated file returns
// the same frame count, durations, sizes and pixels as the library Frames().
func TestDecodeFramesAnimated(t *testing.T) {
	data := loadFixture(t, "anim.webp")
	w, err := simple_webp.ParseWebpBytes(data)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if !w.HasAnimation {
		t.Skip("anim.webp is not animated")
	}
	imgFrames, durs, err := w.Frames(0)
	if err != nil {
		t.Fatalf("library frames: %v", err)
	}
	frames, err := DecodeFrames(data)
	if err != nil {
		t.Fatalf("decode frames: %v", err)
	}
	if len(frames) != len(imgFrames) {
		t.Fatalf("frame count = %d, want %d", len(frames), len(imgFrames))
	}
	for i, f := range frames {
		if f.Duration != durs[i] {
			t.Fatalf("frame %d duration = %d, want %d", i, f.Duration, durs[i])
		}
		if f.Width != w.Width || f.Height != w.Height {
			t.Fatalf("frame %d size = %dx%d, want %dx%d", i, f.Width, f.Height, w.Width, w.Height)
		}
		if len(f.RGBA) != f.Width*f.Height*4 {
			t.Fatalf("frame %d rgba len = %d, want %d", i, len(f.RGBA), f.Width*f.Height*4)
		}
		if !bytes.Equal(f.RGBA, imgFrames[i].Pix) {
			t.Fatalf("frame %d pixels differ from library decode", i)
		}
	}
}

// TestDecodeFramesStill 验证静图经 DecodeFrames 返回单帧、时长为 0，且像素与
// Decode 一致。
//
// TestDecodeFramesStill checks that a still image through DecodeFrames yields a
// single frame with duration 0 whose pixels match Decode.
func TestDecodeFramesStill(t *testing.T) {
	data := sampleWebp(t)
	frames, err := DecodeFrames(data)
	if err != nil {
		t.Fatalf("decode frames: %v", err)
	}
	if len(frames) != 1 {
		t.Fatalf("still: %d frames, want 1", len(frames))
	}
	if frames[0].Duration != 0 {
		t.Fatalf("still: duration = %d, want 0", frames[0].Duration)
	}
	res, err := Decode(data)
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	if !bytes.Equal(frames[0].RGBA, res.RGBA) {
		t.Fatal("still frame pixels differ from Decode")
	}
}
