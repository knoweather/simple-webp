package simple_webp

import (
	"bytes"
	"math/rand"
	"testing"
)

// TestYUVRowMatchesScalar 逐位比对向量化路径与标量参考实现，覆盖 1..257 的
// 行宽（含不足 8 像素的尾部）以及 0、255 这两个钳位边界。
//
// TestYUVRowMatchesScalar compares the vectorised path with the scalar
// reference bit for bit, over row widths 1..257 (including sub-group tails)
// and the clamping boundaries 0 and 255.
func TestYUVRowMatchesScalar(t *testing.T) {
	const maxW = 257
	rng := rand.New(rand.NewSource(1))
	y := make([]byte, maxW)
	u := make([]byte, maxW)
	v := make([]byte, maxW)
	for i := range y {
		y[i] = byte(rng.Intn(256))
		u[i] = byte(rng.Intn(256))
		v[i] = byte(rng.Intn(256))
	}
	// 边界样本：全 0、全 255、以及交替极值。
	// Boundary samples: all zero, all ones and alternating extremes.
	for _, fill := range []byte{0, 255} {
		for i := range y {
			y[i], u[i], v[i] = fill, fill, fill
		}
		checkRow(t, y, u, v)
	}
	for i := range y {
		if i&1 == 0 {
			y[i], u[i], v[i] = 0, 0, 0
		} else {
			y[i], u[i], v[i] = 255, 255, 255
		}
	}
	checkRow(t, y, u, v)

	for i := range y {
		y[i] = byte(rng.Intn(256))
		u[i] = byte(rng.Intn(256))
		v[i] = byte(rng.Intn(256))
	}
	for _, w := range []int{1, 7, 8, 9, 15, 16, 17, 63, 64, 65, maxW} {
		checkRow(t, y[:w], u[:w], v[:w])
	}
}

func checkRow(t *testing.T, y, u, v []byte) {
	t.Helper()
	want := make([]byte, len(y)*4)
	yuvRowScalar(want, y, u, v)
	got := make([]byte, len(y)*4)
	yuvRowToNRGBA(got, y, u, v)
	if !bytes.Equal(got, want) {
		for i := range want {
			if got[i] != want[i] {
				t.Fatalf("len=%d: first mismatch at byte %d (pixel %d, channel %d): got %d, want %d",
					len(y), i, i/4, i%4, got[i], want[i])
			}
		}
	}
}

// BenchmarkYUVRow 对比标量与 AVX2 路径在 1920 宽一行上的吞吐。
//
// BenchmarkYUVRow compares the scalar and AVX2 paths on one 1920-wide row.
func BenchmarkYUVRow(b *testing.B) {
	const w = 1920
	rng := rand.New(rand.NewSource(2))
	y := make([]byte, w)
	u := make([]byte, w)
	v := make([]byte, w)
	for i := range y {
		y[i] = byte(rng.Intn(256))
		u[i] = byte(rng.Intn(256))
		v[i] = byte(rng.Intn(256))
	}
	dst := make([]byte, w*4)

	b.Run("scalar", func(b *testing.B) {
		b.SetBytes(w)
		for i := 0; i < b.N; i++ {
			yuvRowScalar(dst, y, u, v)
		}
	})
	b.Run("dispatch", func(b *testing.B) {
		b.SetBytes(w)
		for i := 0; i < b.N; i++ {
			yuvRowToNRGBA(dst, y, u, v)
		}
	})
}
