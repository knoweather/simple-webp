package vp8

import (
	"bytes"
	"os"
	"testing"

	xvp8 "golang.org/x/image/vp8"
)

// loadVP8 从 testdata 目录读入 VP8 测试流；文件缺失时跳过基准而不是失败。
// loadVP8 reads a VP8 test stream from the testdata directory; a missing
// file skips the benchmark instead of failing it.
func loadVP8(b *testing.B, name string) []byte {
	b.Helper()
	data, err := os.ReadFile("../../testdata/" + name)
	if err != nil {
		b.Skip(err)
	}
	return data
}

// BenchmarkDecodeFrame 衡量本包解码器解一帧 400x300 真实照片序列的吞吐与分配。
// BenchmarkDecodeFrame measures the throughput and allocations of this
// package's decoder on one 400x300 real-photo frame.
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

// BenchmarkXImageDecodeFrame 用 golang.org/x/image/vp8 解同一帧作为对照基准，
// 便于和 BenchmarkDecodeFrame 直接比较。
// BenchmarkXImageDecodeFrame benchmarks golang.org/x/image/vp8 on the same
// frame as a reference, so it can be compared directly with
// BenchmarkDecodeFrame.
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

// BenchmarkEncodeLossy 测量 16x16 帧内编码器在 400x300 与 VGA 尺寸下的耗时。
//
// BenchmarkEncodeLossy measures the 16x16 intra encoder at 400x300 and VGA.
func BenchmarkEncodeLossy400x300(b *testing.B) {
	img := testImage(400, 300)
	b.SetBytes(int64(400 * 300))
	for i := 0; i < b.N; i++ {
		if _, err := Encode(img, 75); err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkEncodeLossyVGA(b *testing.B) {
	img := testImage(640, 480)
	b.SetBytes(int64(640 * 480))
	for i := 0; i < b.N; i++ {
		if _, err := Encode(img, 75); err != nil {
			b.Fatal(err)
		}
	}
}
