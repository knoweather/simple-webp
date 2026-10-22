package vp8l

import (
	"math/rand"
	"testing"
)

// TestDistanceCodeFitsTheCodeSpace 校验最大后向引用距离落在距离码空间内。
//
// 距离码是「距离 + 120」，而规范里距离码的上限是 1048576（前缀码 39）。搜索
// 窗口一旦越过这个上限，距离码就既没有对应的前缀码，也会让距离频次数组越界。
//
// TestDistanceCodeFitsTheCodeSpace checks that the largest backward reference
// distance stays inside the distance code space.
//
// A distance code is "distance + 120" while the format caps the distance code
// at 1048576 (prefix code 39). A search window that reaches past that limit
// produces a distance code that has no prefix code and runs past the distance
// frequency array.
func TestDistanceCodeFitsTheCodeSpace(t *testing.T) {
	if encoderWindow+120 != maxDistanceCode {
		t.Fatalf("encoderWindow %d does not leave room for the 120 offset", encoderWindow)
	}
	prefix, _, nbits := encodePrefix(distanceCode(encoderWindow))
	if prefix > 39 {
		t.Fatalf("the largest distance maps to prefix code %d, the space ends at 39", prefix)
	}
	if nbits != 18 {
		t.Fatalf("the largest distance needs %d extra bits, the format allows 18", nbits)
	}
}

// TestLongDistanceRoundTrip 校验跨越整个搜索窗口的长距离后向引用能编解码还原。
//
// 这条要造一张超过 1M 像素的图：距离码越界的 bug 只在距离接近 1M 时才出现，
// 小图碰不到。默认跳过，-short 下不跑。
//
// TestLongDistanceRoundTrip checks that a backward reference reaching across
// the whole search window survives a round trip.
//
// The image has to be larger than a million pixels: the distance code only
// overflows for distances close to 1M, which small images never reach. The
// test is skipped by default and does not run with -short.
func TestLongDistanceRoundTrip(t *testing.T) {
	if testing.Short() {
		t.Skip("the long distance round trip needs a million pixel image")
	}
	const width, height = 1024, 1025
	n := width * height
	pix := make([]uint32, n)
	rng := rand.New(rand.NewSource(99))
	for i := range pix {
		pix[i] = rng.Uint32() & 0xffffff00
	}
	// 把同一段内容放到相隔整个搜索窗口（1<<20 像素）的两处，逼出长距离引用。
	// The same content is placed twice, one search window apart, to force a
	// long backward reference.
	const dist = 1 << 20
	marker := [4]uint32{0xff112233, 0xff445566, 0xff778899, 0xffaabbcc}
	for i := 0; i < len(marker); i++ {
		pix[i] = marker[i]
		pix[dist+i] = marker[i]
	}

	stream, err := EncodeARGB(pix, width, height, &Options{Method: 1})
	if err != nil {
		t.Fatalf("encode: %v", err)
	}
	got, w, h, err := DecodeARGB(stream)
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	if w != width || h != height {
		t.Fatalf("size %dx%d, want %dx%d", w, h, width, height)
	}
	for i := range pix {
		if got[i] != pix[i] {
			t.Fatalf("pixel %d differs: got %#08x want %#08x", i, got[i], pix[i])
		}
	}
}
