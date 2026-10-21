package vp8l

import (
	"bytes"
	"math/rand"
	"testing"
)

// deBruijn 返回字母表大小 k、阶数 n 的 de Bruijn 序列（FKM 算法）。序列里每个
// 长度 n 的组合恰好出现一次。
//
// deBruijn returns a de Bruijn sequence of order n over an alphabet of size k
// (FKM algorithm); every length n combination appears exactly once.
func deBruijn(k, n int) []int {
	a := make([]int, k*n)
	var seq []int
	var db func(t, p int)
	db = func(t, p int) {
		if t > n {
			if n%p == 0 {
				seq = append(seq, a[1:p+1]...)
			}
			return
		}
		a[t] = a[t-p]
		db(t+1, p)
		for j := a[t-p] + 1; j < k; j++ {
			a[t] = j
			db(t+1, t)
		}
	}
	db(1, 1)
	return seq
}

// TestColorCacheRoundTrip 校验颜色缓存：颜色反复出现而相邻组合不重复时，LZ77
// 抓不到匹配，缓存让码流更小，解码仍逐像素一致。
//
// TestColorCacheRoundTrip checks the colour cache: when colours repeat while
// adjacent pairs do not, LZ77 finds no match and the cache shrinks the stream,
// with decoding still pixel exact.
func TestColorCacheRoundTrip(t *testing.T) {
	const w, h = 64, 64
	// 300 种颜色：超过调色板阈值（256），所以不走色彩索引；阶 2 的 de Bruijn
	// 序列让每个二像素组合只出现一次，于是没有任何三像素匹配可供 LZ77 使用，
	// 剩下的重复色正好交给颜色缓存。
	// 300 colours: above the palette threshold (256), so colour indexing is out;
	// an order 2 de Bruijn sequence makes every two pixel pair unique, so LZ77 has
	// no three pixel match to use and the recurring colours are left to the cache.
	seq := deBruijn(300, 2)
	if len(seq) < w*h {
		t.Fatalf("de Bruijn sequence is %d long, want at least %d", len(seq), w*h)
	}
	palette := make([]uint32, 300)
	for i := range palette {
		palette[i] = 0xff000000 | uint32(i)*0x000811
	}
	pix := make([]uint32, w*h)
	for i := range pix {
		pix[i] = palette[seq[i]]
	}

	withCache, err := EncodeARGB(pix, w, h, &Options{Method: 1})
	if err != nil {
		t.Fatal(err)
	}
	noCache, err := EncodeARGB(pix, w, h, &Options{Method: 1, DisableColorCache: true})
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Equal(withCache, noCache) {
		t.Fatal("the colour cache did not change the bitstream")
	}
	if len(withCache) >= len(noCache) {
		t.Fatalf("cache stream is %d bytes, without cache %d", len(withCache), len(noCache))
	}
	got, gw, gh, err := DecodeARGB(withCache)
	if err != nil {
		t.Fatal(err)
	}
	if gw != w || gh != h {
		t.Fatalf("decoded %dx%d, want %dx%d", gw, gh, w, h)
	}
	for i := range pix {
		if got[i] != pix[i] {
			t.Fatalf("pixel %d: got %08x want %08x", i, got[i], pix[i])
		}
	}
}

// TestColorCacheDisabledOnNoise 校验重复率低的图像不会启用缓存（输出与显式
// 关闭缓存时完全一致）。
//
// TestColorCacheDisabledOnNoise checks that an image with little repetition does
// not switch the cache on: the output equals the explicitly disabled stream.
func TestColorCacheDisabledOnNoise(t *testing.T) {
	const w, h = 48, 48
	rng := rand.New(rand.NewSource(1))
	pix := make([]uint32, w*h)
	for i := range pix {
		pix[i] = 0xff000000 | rng.Uint32()&0x00ffffff
	}
	auto, err := EncodeARGB(pix, w, h, &Options{Method: 1})
	if err != nil {
		t.Fatal(err)
	}
	off, err := EncodeARGB(pix, w, h, &Options{Method: 1, DisableColorCache: true})
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(auto, off) {
		t.Fatalf("noise image used the colour cache: %d vs %d bytes", len(auto), len(off))
	}
}

// TestColorCacheEvents 直接校验缓存决策：重复出现的字面量走缓存引用，并且
// 缓存引用本身不写入缓存（与解码端语义一致）。
//
// TestColorCacheEvents checks the cache decisions directly: a repeated literal
// becomes a cache reference, and a cache reference inserts nothing, matching the
// decoder.
func TestColorCacheEvents(t *testing.T) {
	pix := make([]uint32, 512)
	for i := range pix {
		pix[i] = []uint32{0xff112233, 0xff445566}[i%2]
	}
	tokens := make([]token, len(pix))
	for i, p := range pix {
		tokens[i] = token{literal: p}
	}
	events := buildEvents(pix, tokens, 8)
	if len(events) != len(pix) {
		t.Fatalf("%d events", len(events))
	}
	if events[0].kind != evLiteral || events[1].kind != evLiteral {
		t.Fatalf("first two events: %+v %+v", events[0], events[1])
	}
	for i := 2; i < len(pix); i++ {
		if events[i].kind != evCacheRef {
			t.Fatalf("event %d is not a cache reference: %+v", i, events[i])
		}
	}
	if got := chooseCacheBits(pix, tokens); got != 8 {
		t.Fatalf("chooseCacheBits returned %d for a fully cached image", got)
	}
}
