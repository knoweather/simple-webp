package vp8

import (
	"bytes"
	"os"
	"testing"

	xvp8 "golang.org/x/image/vp8"
)

func loadVP8(b *testing.B, name string) []byte {
	b.Helper()
	data, err := os.ReadFile("../../testdata/" + name)
	if err != nil {
		b.Skip(err)
	}
	return data
}

func BenchmarkDecodeFrame(b *testing.B) {
	data := loadVP8(b, "photo_real.vp8")
	b.SetBytes(400 * 300)
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		if _, err := Decode(data); err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkXImageDecodeFrame(b *testing.B) {
	data := loadVP8(b, "photo_real.vp8")
	b.SetBytes(400 * 300)
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		d := xvp8.NewDecoder()
		d.Init(bytes.NewReader(data), len(data))
		if _, err := d.DecodeFrameHeader(); err != nil {
			b.Fatal(err)
		}
		if _, err := d.DecodeFrame(); err != nil {
			b.Fatal(err)
		}
	}
}
