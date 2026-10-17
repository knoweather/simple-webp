package simple_webp

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// formatOf reports the dominant bitstream kind for a parsed WebP.
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

// BenchmarkCmpAll is a micro-benchmark that mirrors bench_c.c against libwebp:
// read the file once, then decode it N times in a tight loop and report the
// average wall-clock decode time and throughput in millions of pixels/second.
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
			// warm up caches / JIT
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
