package simple_webp

import (
	"os"
	"testing"
)

// FuzzParseWebp checks that no input makes the parser or the decoders panic.
func FuzzParseWebp(f *testing.F) {
	for _, name := range []string{"tiny.webp", "gradient.webp", "lossy_smooth.webp", "alpha.webp"} {
		if data, err := os.ReadFile("testdata/" + name); err == nil {
			f.Add(data)
		}
	}
	f.Add([]byte("RIFF\x04\x00\x00\x00WEBP"))
	f.Fuzz(func(t *testing.T, data []byte) {
		w, err := ParseWebpBytes(data)
		if err != nil {
			return
		}
		_, _ = w.ToImage()
		_, _, _ = w.Frames(0)
		_, _ = w.MarshalBytes()
	})
}
