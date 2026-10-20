package vp8

import "math"

// boolEncoder 是 boolDecoder 的逆运算，输出 RFC 6386 第 7 节的布尔编码码流。
//
// 编码器保存完整的 range（解码器存的是 range-1），状态以 24 位区间下界 low 为
// 中心：解码器的值窗口只保留 16 位前瞻，区间大小按 range<<8 计，所以 low 的有效
// 宽度维持在 24 位，每凑满 8 位就定稿一个字节。
//
// count 记录自上次定稿以来累积的归一化位数，初值 -24 对应“窗口还空着 24 位”。
// 一旦 count 越过 0，说明 low 的高 8 位已经稳定，可以写出；若该字节的进位位
// 为 1，则需要向已写出的字节回传进位——连续的 0xff 会被进位翻成 0x00，必须
// 逐字节回溯。
//
// boolEncoder is the inverse of boolDecoder and emits the boolean-coded stream
// of section 7 of RFC 6386.
//
// The encoder keeps the full range (the decoder stores range-1) and centres its
// state on low, the 24 bit low end of the interval: the decoder keeps only 16
// bits of lookahead and measures the interval as range<<8, so low is held at 24
// significant bits and one byte is finalised each time eight bits accumulate.
//
// count tracks the normalisation bits accumulated since the last finalised
// byte, starting at -24 for the empty window. Once count reaches zero the top
// byte of low is settled and can be written; if that byte's carry bit is set the
// carry has to travel back into the bytes already written, and a run of 0xff
// bytes flips to 0x00 as it passes, so the search walks backwards byte by byte.
type boolEncoder struct {
	low   uint32 // 区间下界，24 位有效 / interval low end, 24 significant bits
	rng   uint32 // [1, 255]
	count int    // 距上次定稿累积的位数，初值 -24 / bits since the last byte, starts at -24
	buf   []byte
	// dry 为真时只走状态机而不产出字节，用于统计遍。
	// dry runs the state machine without producing bytes, for the statistics
	// pass.
	dry bool
	// costMode 为真时 putBit 不产出字节，只把该比特的编码代价累加到 costBits，
	// 用于率失真决策时估算一段码流的位数。
	//
	// With costMode set, putBit produces no bytes and only adds the cost of
	// coding the bit to costBits, which estimates the length of a stretch of
	// the stream for the rate distortion decisions.
	costMode bool
	costBits float64
}

// init 复位编码器。range 与解码器一致取 255（即 rngM1=254），窗口为空。
//
// init resets the encoder. range starts at 255, matching the decoder's
// rngM1 = 254, with an empty window.
func (e *boolEncoder) init() {
	e.low = 0
	e.rng = 255
	e.count = -24
	e.buf = e.buf[:0]
}

// putBit 以“取零概率” prob 编码一个比特。
//
// putBit encodes one bit with prob as the probability of a zero.
func (e *boolEncoder) putBit(prob uint8, bit bool) {
	if e.costMode {
		if bit {
			e.costBits += bitCostTable[1][prob]
		} else {
			e.costBits += bitCostTable[0][prob]
		}
		return
	}
	if e.dry {
		return
	}
	split := 1 + (e.rng-1)*uint32(prob)>>8
	rng := split
	low := e.low
	if bit {
		low += split
		rng = e.rng - split
	}

	shift := normShift(rng)
	rng <<= uint(shift)
	count := e.count + shift
	if count >= 0 {
		// offset 是本次移位中越过字节边界的位数，恒在 [1,7]：shift 不超过 7，
		// 而 count 要越过 0 至少要 offset 位。
		// offset is how many bits of this shift crossed the byte boundary. It is
		// always in [1,7]: shift never exceeds 7, and reaching count >= 0 takes at
		// least offset bits.
		offset := shift - count
		if low<<uint(offset-1)&0x80000000 != 0 {
			e.carry()
		}
		e.buf = append(e.buf, byte(low>>uint(24-offset)))
		low <<= uint(offset)
		low &= 0xffffff
		shift = count
		count -= 8
	}
	low <<= uint(shift)

	e.low = low
	e.rng = rng
	e.count = count
}

// carry 把进位回传进已写出的字节：末尾连续的 0xff 全部翻成 0x00，再给第一个
// 非 0xff 字节加一。
//
// carry propagates a carry into the bytes already written: the trailing run of
// 0xff flips to 0x00 and the first byte before it is incremented.
func (e *boolEncoder) carry() {
	i := len(e.buf) - 1
	for i >= 0 && e.buf[i] == 0xff {
		e.buf[i] = 0
		i--
	}
	if i >= 0 {
		e.buf[i]++
	}
}

// putUint 编码一个 n 位无符号整数，最高位在前。
//
// putUint encodes an n bit unsigned integer, most significant bit first.
func (e *boolEncoder) putUint(prob uint8, n uint8, v uint32) {
	for i := int(n) - 1; i >= 0; i-- {
		e.putBit(prob, v&(1<<uint(i)) != 0)
	}
}

// putInt 编码一个 n 位幅值，后跟一个符号位。
//
// putInt encodes an n bit magnitude followed by a sign bit.
func (e *boolEncoder) putInt(prob uint8, n uint8, v int32) {
	if v < 0 {
		e.putUint(prob, n, uint32(-v))
		e.putBit(prob, true)
		return
	}
	e.putUint(prob, n, uint32(v))
	e.putBit(prob, false)
}

// putOptionalInt 编码一个“很可能为零”的值：先写存在位，为零时仅此一位。
//
// putOptionalInt encodes a value that is likely zero: a presence bit first, and
// nothing else when that bit is clear.
func (e *boolEncoder) putOptionalInt(prob uint8, n uint8, v int32) {
	if v == 0 {
		e.putBit(prob, false)
		return
	}
	e.putBit(prob, true)
	e.putInt(prob, n, v)
}

// finish 用 32 个等概率零位冲净窗口，返回完整码流。这些位不承载信息，只用于
// 把 low 中剩余的有效位推出去，使解码器末几次决策仍有码可读。
//
// finish flushes the window with 32 equiprobable zero bits and returns the
// complete stream. Those bits carry no information: they only push the remaining
// significant bits of low out, so the decoder's last decisions still have stream
// to read.
func (e *boolEncoder) finish() []byte {
	for i := 0; i < 32; i++ {
		e.putBit(uniformProb, false)
	}
	return e.buf
}

// normShift 返回把 range 归一到 [128,255] 所需的左移位数。
//
// normShift returns the number of left shifts that bring range into [128,255].
func normShift(rng uint32) int {
	if rng >= 128 {
		return 0
	}
	n := 0
	for rng < 128 {
		rng <<= 1
		n++
	}
	return n
}

// bitCostTable 预先算好每个概率下编码零与一的代价（比特数）。率失真决策会
// 反复估算码长，每次都调用 math.Log2 太慢，查表快得多。
//
// bitCostTable holds the cost in bits of coding a zero and a one for every
// probability. The rate distortion decisions estimate the length of the code
// over and over, and calling math.Log2 every time is too slow.
var bitCostTable [2][256]float64

func init() {
	for p := 0; p < 256; p++ {
		prob := float64(p) / 256
		zero := 1e9
		one := 1e9
		if prob > 0 {
			zero = -math.Log2(prob)
		}
		if prob < 1 {
			one = -math.Log2(1 - prob)
		}
		bitCostTable[0][p] = zero
		bitCostTable[1][p] = one
	}
}
