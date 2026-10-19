package simple_webp

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// formatOf 返回已解析 WebP 的主导码流类别：无损、有损、动画，或未知。
//
// formatOf reports the dominant bitstream kind of a parsed WebP: lossless,
// lossy, animation, or unknown.
func formatOf(w *Webp) string {
	switch {
	case w.VP8L != nil:
		return "lossless"
	case w.VP8 != nil:
		if w.HasAnimation {
			return "anim"
		}
		return "lossy"
	default:
		return "?"
	}
}

// BenchmarkCmpAll 是与 libwebp 的 bench_c.c 对齐的微基准：文件只读一次，
// 随后在紧凑循环里解码 N 次，报告平均墙钟解码耗时与每秒百万像素吞吐。
// 运行方式：go test -bench=BenchmarkCmpAll -benchtime=200x -v
//
// BenchmarkCmpAll is a micro-benchmark that mirrors libwebp's bench_c.c:
// the file is read once, then decoded N times in a tight loop, reporting
// the average wall-clock decode time and throughput in megapixels/second.
// Run with: go test -bench=BenchmarkCmpAll -benchtime=200x -v
func BenchmarkCmpAll(b *testing.B) {
	files, err := filepath.Glob("testdata/*.webp")
	if err != nil || len(files) == 0 {
		b.Skip("no fixtures")
	}
	for _, f := range files {
		f := f
		b.Run(f, func(b *testing.B) {
			data, err := os.ReadFile(f)
			if err != nil {
				b.Skip(err)
			}
			w, err := ParseWebpBytes(data)
			if err != nil {
				b.Skip(err)
			}
			// 预热缓存与 JIT。
			// Warm up caches and the JIT.
			for k := 0; k < 3; k++ {
				if _, err := w.ToImage(); err != nil {
					b.Skip(err)
				}
			}
			b.ResetTimer()
			start := time.Now()
			for i := 0; i < b.N; i++ {
				if _, err := w.ToImage(); err != nil {
					b.Fatal(err)
				}
			}
			el := time.Since(start)
			avgMs := float64(el.Microseconds()) / 1000.0 / float64(b.N)
			mps := float64(w.Width*w.Height) / (avgMs * 1000.0)
			fmt.Printf("%-30s %5dx%-5d %-9s %8.3f ms %9.1f MP/s (go)\n",
				filepath.Base(f), w.Width, w.Height, formatOf(w), avgMs, mps)
		})
	}
}
