package vp8

import "testing"

// TestQuantIndexMatchesLibwebp 校验质量到量化索引的映射与 libwebp 一致。
//
// libwebp 的曲线在 enc/quant_enc.c 的 VP8SetSegmentParams 里：质量先归一化，
// 经分段线性映射成压缩率，再开立方根，127*(1-c) 就是量化索引。单段、不启用
// SNS 时 expn 恒为 1，所以这里可以直接对照。期望值由同一公式算出并取整。
//
// TestQuantIndexMatchesLibwebp checks that the quality to quantiser index
// mapping matches libwebp.
//
// libwebp's curve lives in VP8SetSegmentParams (enc/quant_enc.c): the quality
// is normalised, mapped to a compressibility by a piecewise linear function and
// cube rooted, and 127*(1-c) is the index. With a single segment and SNS off
// expn is always 1, so the values can be compared directly. The expectations
// below come from the same formula.
func TestQuantIndexMatchesLibwebp(t *testing.T) {
	cases := []struct{ quality, want int }{
		{0, 127},
		{1, 103},
		{10, 75},
		{25, 57},
		{49, 39},
		{50, 38},
		{74, 26},
		{75, 26},
		{76, 24},
		{85, 14},
		{90, 9},
		{95, 4},
		{99, 0},
		{100, 0},
	}
	for _, c := range cases {
		if got := quantIndex(c.quality); got != c.want {
			t.Errorf("quantIndex(%d) = %d, want %d (libwebp)", c.quality, got, c.want)
		}
	}

	// 索引必须随质量单调不增，且始终落在 0..127。
	// The index must never grow with quality and must stay within 0..127.
	prev := 128
	for q := 0; q <= 100; q++ {
		qi := quantIndex(q)
		if qi < 0 || qi > 127 {
			t.Fatalf("quantIndex(%d) = %d, outside 0..127", q, qi)
		}
		if qi > prev {
			t.Fatalf("quantIndex(%d) = %d grew from %d", q, qi, prev)
		}
		prev = qi
	}

	// 越界质量被钳到两端。
	// Out of range qualities clamp to the ends.
	if got := quantIndex(-20); got != 127 {
		t.Errorf("quantIndex(-20) = %d, want 127", got)
	}
	if got := quantIndex(300); got != 0 {
		t.Errorf("quantIndex(300) = %d, want 0", got)
	}
}
