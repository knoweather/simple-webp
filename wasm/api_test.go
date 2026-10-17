package wasm

import (
	"image"
	"image/color"
	"testing"

	"github.com/knoweather/simple-webp"
)

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
	// one semi-transparent pixel so the alpha plane is definitely used
	img.SetNRGBA(0, 0, color.NRGBA{R: 10, G: 20, B: 30, A: 128})
	data, err := simple_webp.EncodeBytes(img, &simple_webp.EncodeOptions{Method: 0})
	if err != nil {
		t.Fatalf("encode: %v", err)
	}
	return data
}

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
	// top-left pixel (0,0): straight alpha {10,20,30,128}
	got := res.RGBA[:4]
	want := []byte{10, 20, 30, 128}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("pixel(0,0) = %v, want %v", got, want)
		}
	}
}

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
