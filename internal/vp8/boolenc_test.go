package vp8

import (
	"math/rand"
	"testing"
)

// roundTrip 用编码器写出 bits，再用解码器读回，返回首个失配位置（-1 表示全部
// 一致）。
//
// roundTrip encodes bits and decodes them back, returning the first mismatching
// index, or -1 when every bit matches.
func roundTrip(probs []uint8, bits []bool) int {
	var e boolEncoder
	e.init()
	for i, b := range bits {
		e.putBit(probs[i], b)
	}
	stream := e.finish()

	var d boolDecoder
	d.init(stream)
	for i, want := range bits {
		if d.readBit(probs[i]) != want {
			return i
		}
	}
	return -1
}

// TestBoolEncoderSingleBit 覆盖单个比特的两种取值。
//
// TestBoolEncoderSingleBit covers both values of a single bit.
func TestBoolEncoderSingleBit(t *testing.T) {
	for _, bit := range []bool{false, true} {
		probs := []uint8{uniformProb}
		if got := roundTrip(probs, []bool{bit}); got != -1 {
			t.Fatalf("bit %v: mismatch at %d", bit, got)
		}
	}
}

// TestBoolEncoderExtremeProbs 用最偏的概率（1 与 255）交替编码，覆盖 range
// 收缩到极限的情形。
//
// TestBoolEncoderExtremeProbs alternates the most skewed probabilities (1 and
// 255) to cover range shrinking to its limits.
func TestBoolEncoderExtremeProbs(t *testing.T) {
	for _, prob := range []uint8{1, 2, 128, 254, 255} {
		const n = 512
		probs := make([]uint8, n)
		bits := make([]bool, n)
		for i := 0; i < n; i++ {
			probs[i] = prob
			bits[i] = i%3 == 0
		}
		if got := roundTrip(probs, bits); got != -1 {
			t.Fatalf("prob %d: mismatch at bit %d", prob, got)
		}
	}
}

// TestBoolEncoderAllZeros 与 TestBoolEncoderAllOnes 覆盖长时间同值输入，这是
// 进位与 0xff 回溯最容易出错的形态。
//
// TestBoolEncoderAllZeros and TestBoolEncoderAllOnes cover long runs of one
// value, the shape most likely to expose carry and 0xff backtracking bugs.
func TestBoolEncoderAllZeros(t *testing.T) {
	const n = 4096
	probs := make([]uint8, n)
	for i := range probs {
		probs[i] = 200
	}
	if got := roundTrip(probs, make([]bool, n)); got != -1 {
		t.Fatalf("all zero bits: mismatch at %d", got)
	}
}

func TestBoolEncoderAllOnes(t *testing.T) {
	const n = 4096
	probs := make([]uint8, n)
	bits := make([]bool, n)
	for i := range probs {
		probs[i] = 30
		bits[i] = true
	}
	if got := roundTrip(probs, bits); got != -1 {
		t.Fatalf("all one bits: mismatch at %d", got)
	}
}

// TestBoolEncoderRandom 用随机概率与随机比特做大量往返，是最主要的正确性保障。
//
// TestBoolEncoderRandom round-trips random probabilities and random bits at
// scale; it is the main correctness guarantee.
func TestBoolEncoderRandom(t *testing.T) {
	rng := rand.New(rand.NewSource(20261020))
	for iter := 0; iter < 2000; iter++ {
		n := 1 + rng.Intn(600)
		probs := make([]uint8, n)
		bits := make([]bool, n)
		for i := range probs {
			probs[i] = uint8(1 + rng.Intn(255))
			bits[i] = rng.Intn(2) == 1
		}
		if got := roundTrip(probs, bits); got != -1 {
			t.Fatalf("iter %d (n=%d): mismatch at bit %d", iter, n, got)
		}
	}
}

// TestBoolEncoderSkewedRandom 混合极端概率与随机概率，专门制造密集的归一化。
//
// TestBoolEncoderSkewedRandom mixes extreme and random probabilities to force
// dense renormalisation.
func TestBoolEncoderSkewedRandom(t *testing.T) {
	rng := rand.New(rand.NewSource(7))
	for iter := 0; iter < 1000; iter++ {
		n := 1 + rng.Intn(400)
		probs := make([]uint8, n)
		bits := make([]bool, n)
		for i := range probs {
			switch rng.Intn(4) {
			case 0:
				probs[i] = 1
			case 1:
				probs[i] = 255
			default:
				probs[i] = uint8(1 + rng.Intn(255))
			}
			bits[i] = rng.Intn(8) == 0
		}
		if got := roundTrip(probs, bits); got != -1 {
			t.Fatalf("iter %d (n=%d): mismatch at bit %d", iter, n, got)
		}
	}
}

// TestBoolEncoderUintAndInt 校验 putUint / putInt / putOptionalInt 与对应的
// 解码函数互逆。
//
// TestBoolEncoderUintAndInt checks that putUint / putInt / putOptionalInt invert
// their decoding counterparts.
func TestBoolEncoderUintAndInt(t *testing.T) {
	rng := rand.New(rand.NewSource(11))
	for iter := 0; iter < 500; iter++ {
		n := uint8(1 + rng.Intn(16))
		u := uint32(rng.Int63n(int64(1) << n))
		signed := int32(rng.Intn(1<<n)) - int32(1<<(n-1))
		if rng.Intn(3) == 0 {
			signed = 0
		}

		var e boolEncoder
		e.init()
		e.putUint(uniformProb, n, u)
		e.putInt(uniformProb, n, signed)
		e.putOptionalInt(uniformProb, n, signed)
		stream := e.finish()

		var d boolDecoder
		d.init(stream)
		if got := d.readUint(uniformProb, n); got != u {
			t.Fatalf("iter %d: uint %d != %d", iter, got, u)
		}
		if got := d.readInt(uniformProb, n); got != signed {
			t.Fatalf("iter %d: int %d != %d", iter, got, signed)
		}
		if got := d.readOptionalInt(uniformProb, n); got != signed {
			t.Fatalf("iter %d: optional int %d != %d", iter, got, signed)
		}
	}
}

// TestNormShift 校验归一化位移表与解码器的重归一化条件一致。
//
// TestNormShift checks that the normalisation shift agrees with the decoder's
// renormalisation condition.
func TestNormShift(t *testing.T) {
	for rng := uint32(1); rng < 256; rng++ {
		shift := normShift(rng)
		v := rng << uint(shift)
		if v < 128 || v > 255 {
			t.Fatalf("rng=%d shift=%d -> %d, want [128,255]", rng, shift, v)
		}
		if shift > 0 && rng<<uint(shift-1) >= 128 {
			t.Fatalf("rng=%d shift=%d is not minimal", rng, shift)
		}
	}
}
