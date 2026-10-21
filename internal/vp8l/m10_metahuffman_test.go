package vp8l

import "testing"

// newMixedImage 构造一张混合内容的图：左半只用颜色 0..149，右半只用 150..299，
// 各半用阶 2 的 de Bruijn 序列排列，三像素匹配极少，于是两侧的符号分布截然不同
// 又都必须靠字面量编码，正好检验按块切换码组。
//
// newMixedImage builds a mixed content image: the left half only uses colours
// 0..149, the right half 150..299, each laid out as an order 2 de Bruijn sequence.
// Three pixel matches are rare, so both halves encode mostly literals with very
// different symbol sets, which is exactly what per block Huffman groups address.
func newMixedImage(w, h int) []uint32 {
	left := deBruijn(150, 2)
	right := deBruijn(150, 2)
	pix := make([]uint32, w*h)
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			var v uint32
			if x < w/2 {
				v = uint32(left[(y*(w/2)+x)%len(left)])
			} else {
				v = 150 + uint32(right[(y*(w-w/2)+(x-w/2))%len(right)])
			}
			pix[y*w+x] = 0xff000000 | (v&0xff)<<16 | (v>>8)<<8 | (v & 0xff)
		}
	}
	return pix
}

// TestMetaHuffmanPlan 校验块分组：混合内容会真的分成两组，而且每组都有事件。
//
// TestMetaHuffmanPlan checks the block grouping: mixed content really splits into
// two groups, both with events.
func TestMetaHuffmanPlan(t *testing.T) {
	const w, h = 128, 64
	pix := newMixedImage(w, h)
	plan, ok := planMeta(pix, lz77(pix), w, h)
	if !ok {
		t.Fatal("planMeta gave up on mixed content")
	}
	var counts [2]int
	for _, be := range plan.events {
		counts[plan.groupOfBlock[be.block]]++
	}
	if counts[0] == 0 || counts[1] == 0 {
		t.Fatalf("group events: %v", counts)
	}
}

// TestMetaHuffmanSectionRoundTrip 直接构造一段带 meta 前缀码的码流，校验解码
// 器能按块切换码组并逐像素还原。
//
// TestMetaHuffmanSectionRoundTrip builds a stream with meta prefix codes directly
// and checks that the decoder switches groups per block and restores every pixel.
func TestMetaHuffmanSectionRoundTrip(t *testing.T) {
	const w, h = 128, 64
	pix := newMixedImage(w, h)
	plan, ok := planMeta(pix, lz77(pix), w, h)
	if !ok {
		t.Fatal("planMeta gave up on mixed content")
	}
	e := &encoder{}
	e.w.writeBits(0x2f, 8)
	e.w.writeBits(uint32(w-1), 14)
	e.w.writeBits(uint32(h-1), 14)
	e.w.writeBits(alphaUsed(pix), 1)
	e.w.writeBits(0, 3)
	e.w.writeBits(0, 1) // 不使用变换 / no transforms
	e.writeMetaSection(plan)
	data := e.w.finish()

	got, gw, gh, err := DecodeARGB(data)
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

// TestMetaHuffmanRoundTrip 校验 meta 前缀码：Method 1 下混合内容的码流明显更
// 短，Method 2 也不会因为尝试 meta 而变大，解码都逐像素一致。
//
// TestMetaHuffmanRoundTrip checks the meta prefix codes: with Method 1 the mixed
// content gets a clearly shorter stream, Method 2 never grows by trying them, and
// decoding stays pixel exact.
func TestMetaHuffmanRoundTrip(t *testing.T) {
	const w, h = 128, 64
	pix := newMixedImage(w, h)

	withMeta, err := encodeStream(pix, w, h, 1, false, false, true)
	if err != nil {
		t.Fatal(err)
	}
	plain, err := encodeStream(pix, w, h, 1, false, false, false)
	if err != nil {
		t.Fatal(err)
	}
	if len(withMeta) >= len(plain) {
		t.Fatalf("Method 1: meta stream is %d bytes, plain %d", len(withMeta), len(plain))
	}
	got, _, _, err := DecodeARGB(withMeta)
	if err != nil {
		t.Fatal(err)
	}
	for i := range pix {
		if got[i] != pix[i] {
			t.Fatalf("pixel %d: got %08x want %08x", i, got[i], pix[i])
		}
	}

	// Method 2 打开 meta 后不能变大。
	// With Method 2 the meta attempt must not grow the stream.
	best, err := EncodeARGB(pix, w, h, &Options{Method: 2})
	if err != nil {
		t.Fatal(err)
	}
	off, err := EncodeARGB(pix, w, h, &Options{Method: 2, DisableMetaHuffman: true})
	if err != nil {
		t.Fatal(err)
	}
	if len(best) > len(off) {
		t.Fatalf("EncodeARGB grew the stream: %d > %d", len(best), len(off))
	}
	if _, _, _, err := DecodeARGB(best); err != nil {
		t.Fatal(err)
	}
}
