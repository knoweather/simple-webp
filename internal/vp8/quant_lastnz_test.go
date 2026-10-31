package vp8

import (
	"math/rand"
	"testing"
)

// TestQuantBlockRecordsLastNZ 校验量化循环记下的 lastNZ 与「重新扫描系数」的
// 结果一致。
//
// lastNZ 是写出 EOB 位置的唯一依据：记录错了，码流里 EOB 会落在错误的位置，
// 解码端读出的系数个数与编码端不同，画面直接错。量化循环顺手记录它是为了省掉
// 每个块十六个位置的扫描，所以这条等价性必须由测试守住。
//
// TestQuantBlockRecordsLastNZ checks that the position recorded by the
// quantisation loop matches a fresh scan of the coefficients.
//
// lastNZ is the only source of the EOB position: record it wrong and the EOB
// lands elsewhere in the stream, the decoder reads a different number of
// coefficients than the encoder wrote, and the picture breaks. The quantisation
// loop records it on the fly to save a sixteen position scan per block, so this
// equivalence has to be held by a test.
func TestQuantBlockRecordsLastNZ(t *testing.T) {
	rng := rand.New(rand.NewSource(3))
	e := &encoder{}
	// 量化因子取 1、偏置取 0，levels 就等于输入系数，便于构造各种分布。
	// A quantiser of one and a zero bias leave the levels equal to the input,
	// which makes the coefficient patterns easy to control.
	e.d.q[0] = quant{y1: [2]uint16{1, 1}, y2: [2]uint16{1, 1}, uv: [2]uint16{1, 1}}

	check := func(blk *[16]int32, base int) {
		t.Helper()
		e.quantBlock(blk, base, 1, 0, 1, 0)
		want := 0
		for i := 0; i < 16; i++ {
			if e.levels[base+int(zigzag[i])] != 0 {
				want = i + 1
			}
		}
		if got := int(e.lastNZ[base/16]); got != want {
			t.Fatalf("base %d: lastNZ recorded %d, rescan says %d (levels %v)",
				base, got, want, e.levels[base:base+16])
		}
	}

	// 随机稀疏系数，覆盖各种「最后非零位置」。
	// Random sparse coefficients covering every possible last position.
	for iter := 0; iter < 2000; iter++ {
		var blk [16]int32
		for i := range blk {
			if rng.Intn(3) == 0 {
				blk[i] = int32(rng.Intn(21) - 10)
			}
		}
		check(&blk, 16*rng.Intn(16))
	}

	// 边界：全零、只有 DC、只有最后一个扫描位置、全部非零。
	// Boundaries: all zero, DC only, last scan position only, all non-zero.
	var zero [16]int32
	check(&zero, 0)

	var dcOnly [16]int32
	dcOnly[0] = 7
	check(&dcOnly, 16)

	// zigzag[15] == 15，自然下标与扫描位置的最后一格重合。
	// zigzag[15] == 15, where the natural index and the last scan position
	// coincide.
	var lastOnly [16]int32
	lastOnly[int(zigzag[15])] = -3
	check(&lastOnly, 32)

	var all [16]int32
	for i := range all {
		all[i] = int32(i + 1)
	}
	check(&all, 48)
}
