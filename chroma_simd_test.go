package simple_webp

import (
	"bytes"
	"math/rand"
	"testing"
)

// TestChromaRowMatchesScalar 在 1..48 个色度样本、两种输出宽度（2*cw-1 与
// 2*cw）下逐位比对向量化路径与标量参考实现，覆盖水平钳位出现在行两端和
// 向量段为空的情况。
//
// TestChromaRowMatchesScalar compares the vectorised path with the scalar
// reference bit for bit for 1..48 chroma samples and both output widths
// (2*cw-1 and 2*cw), covering clamped row ends and an empty vector segment.
func TestChromaRowMatchesScalar(t *testing.T) {
	rng := rand.New(rand.NewSource(1))
	for cw := 1; cw <= 48; cw++ {
		center := make([]byte, cw)
		vert := make([]byte, cw)
		for i := range center {
			center[i] = byte(rng.Intn(256))
			vert[i] = byte(rng.Intn(256))
		}
		for _, w := range []int{2*cw - 1, 2 * cw} {
			want := make([]byte, w)
			upsampleChromaRowScalar(want, center, vert, cw, 0, w)
			got := make([]byte, w)
			upsampleChromaRow(got, center, vert, cw, w)
			if !bytes.Equal(got, want) {
				for i := range want {
					if got[i] != want[i] {
						t.Fatalf("cw=%d w=%d: first mismatch at x=%d: got %d, want %d",
							cw, w, i, got[i], want[i])
					}
				}
			}
		}
	}

	// 极值平面：全 0 与全 255，确认不会出饱和错误。
	// Extreme planes, all zero and all ones, to catch saturation mistakes.
	for _, fill := range []byte{0, 255} {
		const cw = 64
		center := bytes.Repeat([]byte{fill}, cw)
		vert := bytes.Repeat([]byte{fill}, cw)
		want := make([]byte, 2*cw)
		upsampleChromaRowScalar(want, center, vert, cw, 0, 2*cw)
		got := make([]byte, 2*cw)
		upsampleChromaRow(got, center, vert, cw, 2*cw)
		if !bytes.Equal(got, want) {
			t.Fatalf("fill=%d: got %v, want %v", fill, got[:8], want[:8])
		}
	}
}

// BenchmarkChromaRow 对比标量与 AVX2 路径在 1920 宽一行上的吞吐。
//
// BenchmarkChromaRow compares the scalar and AVX2 paths on one 1920-wide row.
func BenchmarkChromaRow(b *testing.B) {
	const w = 1920
	const cw = w / 2
	rng := rand.New(rand.NewSource(2))
	center := make([]byte, cw)
	vert := make([]byte, cw)
	for i := range center {
		center[i] = byte(rng.Intn(256))
		vert[i] = byte(rng.Intn(256))
	}
	dst := make([]byte, w)

	b.Run("scalar", func(b *testing.B) {
		b.SetBytes(w)
		for i := 0; i < b.N; i++ {
			upsampleChromaRowScalar(dst, center, vert, cw, 0, w)
		}
	})
	b.Run("dispatch", func(b *testing.B) {
		b.SetBytes(w)
		for i := 0; i < b.N; i++ {
			upsampleChromaRow(dst, center, vert, cw, w)
		}
	})
}
