package simple_webp

import (
	"bytes"
	"image"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestEncodeRoundTrip encodes every decoded fixture at each method and checks
// that the result decodes back to the same pixels.
func TestEncodeRoundTrip(t *testing.T) {
	entries, err := os.ReadDir("testdata")
	if err != nil {
		t.Skipf("no testdata: %v", err)
	}
	for _, entry := range entries {
		name := entry.Name()
		if !strings.HasSuffix(name, ".webp") || strings.HasPrefix(name, "anim") {
			continue
		}
		t.Run(strings.TrimSuffix(name, ".webp"), func(t *testing.T) {
			stream, err := os.ReadFile(filepath.Join("testdata", name))
			if err != nil {
				t.Fatal(err)
			}
			w, err := ParseWebpBytes(stream)
			if err != nil {
				t.Fatal(err)
			}
			src, err := w.ToImage()
			if err != nil {
				t.Fatal(err)
			}
			for _, method := range []int{0, 1, 2} {
				encoded, err := EncodeBytes(src, &EncodeOptions{Lossless: true, Method: method})
				if err != nil {
					t.Fatalf("method %d: %v", method, err)
				}
				back, err := ParseWebpBytes(encoded)
				if err != nil {
					t.Fatalf("method %d: parse: %v", method, err)
				}
				img, err := back.ToImage()
				if err != nil {
					t.Fatalf("method %d: decode: %v", method, err)
				}
				compareNRGBA(t, "method "+string(rune('0'+method)), img, src.(*image.NRGBA).Pix)
				if len(encoded) > len(stream)*40 && len(stream) > 1000 {
					t.Logf("method %d grew from %d to %d bytes", method, len(stream), len(encoded))
				}
			}
		})
	}
}

// TestEncodeSinglePixel checks the smallest possible image.
func TestEncodeSinglePixel(t *testing.T) {
	src := image.NewNRGBA(image.Rect(0, 0, 1, 1))
	src.Pix[0], src.Pix[1], src.Pix[2], src.Pix[3] = 12, 34, 56, 78
	data, err := EncodeBytes(src, nil)
	if err != nil {
		t.Fatal(err)
	}
	w, err := ParseWebpBytes(data)
	if err != nil {
		t.Fatal(err)
	}
	if w.Width != 1 || w.Height != 1 || !w.HasAlpha {
		t.Fatalf("header: %dx%d alpha=%v", w.Width, w.Height, w.HasAlpha)
	}
	img, err := w.ToImage()
	if err != nil {
		t.Fatal(err)
	}
	compareNRGBA(t, "1x1", img, src.Pix)
}

// TestEncodeUniform checks a large flat image, which exercises long matches.
func TestEncodeUniform(t *testing.T) {
	src := image.NewNRGBA(image.Rect(0, 0, 200, 150))
	for i := 0; i < len(src.Pix); i += 4 {
		src.Pix[i], src.Pix[i+1], src.Pix[i+2], src.Pix[i+3] = 200, 30, 60, 255
	}
	data, err := EncodeBytes(src, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(data) > 200 {
		t.Fatalf("flat image encoded to %d bytes", len(data))
	}
	img, err := Decode(bytes.NewReader(data))
	if err != nil {
		t.Fatal(err)
	}
	compareNRGBA(t, "uniform", img, src.Pix)
}

// TestEncodeOutputDir writes encoded files for external verification when
// WEBP_ENCODE_DIR is set.
func TestEncodeOutputDir(t *testing.T) {
	dir := os.Getenv("WEBP_ENCODE_DIR")
	if dir == "" {
		t.Skip("WEBP_ENCODE_DIR is not set")
	}
	entries, err := os.ReadDir("testdata")
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		name := entry.Name()
		if !strings.HasSuffix(name, ".webp") || strings.HasPrefix(name, "anim") {
			continue
		}
		stream, err := os.ReadFile(filepath.Join("testdata", name))
		if err != nil {
			t.Fatal(err)
		}
		w, err := ParseWebpBytes(stream)
		if err != nil {
			t.Fatal(err)
		}
		src, err := w.ToImage()
		if err != nil {
			t.Fatal(err)
		}
		for _, method := range []int{1, 2} {
			data, err := EncodeBytes(src, &EncodeOptions{Lossless: true, Method: method})
			if err != nil {
				t.Fatal(err)
			}
			out := filepath.Join(dir, strings.TrimSuffix(name, ".webp")+"_m"+string(rune('0'+method))+".webp")
			if err = os.WriteFile(out, data, 0o644); err != nil {
				t.Fatal(err)
			}
		}
	}
}
