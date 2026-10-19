package vp8

// boolDecoder 是 RFC 6386 第 7 节的算术（布尔）熵解码器。
// 采用“range 减一”表示法：rngM1 保存 range-1，值窗口中存放接下来的
// 16 个比特，最高位在前。
//
// boolDecoder is the arithmetic (boolean) entropy decoder of section 7 of
// RFC 6386. It follows the "range minus one" formulation: rngM1 holds
// range-1, and the value window holds the next 16 bits, most significant
// bit first.
type boolDecoder struct {
	buf   []byte
	pos   int
	rngM1 uint32
	bits  uint32
	nBits uint8
	eof   bool
}

// uniformProb 表示 50% 概率（即等概率位）所用的概率值。
// uniformProb codes a bit with 50% probability.
const uniformProb = 128

// init 将解码器复位到流起始状态：range-1 置为 254，值窗口清空。
// init resets the decoder to the start-of-stream state: rngM1 becomes 254
// and the value window is emptied.
func (d *boolDecoder) init(buf []byte) {
	d.buf = buf
	d.pos = 0
	d.rngM1 = 254
	d.bits = 0
	d.nBits = 0
	d.eof = false
}

// refill 把下一个字节追加到值窗口中已有比特的下方。
// refill appends the next byte below the bits already in the window.
func (d *boolDecoder) refill() {
	if d.pos >= len(d.buf) {
		d.eof = true
		return
	}
	d.bits |= uint32(d.buf[d.pos]) << (8 - d.nBits)
	d.pos++
	d.nBits += 8
}

// readBit 以给定的“取零概率”解码一个比特。
// readBit decodes one bit with the given probability of being zero.
func (d *boolDecoder) readBit(prob uint8) bool {
	if d.nBits < 8 {
		d.refill()
	}
	split := (d.rngM1*uint32(prob))>>8 + 1
	bit := d.bits >= split<<8
	if bit {
		d.rngM1 -= split
		d.bits -= split << 8
	} else {
		d.rngM1 = split - 1
	}
	// 重归一化：最多左移 7 次即可让 range 恢复到 >= 128。
	// Renormalize: at most seven shifts are needed to get range >= 128.
	for d.rngM1 < 127 {
		d.rngM1 = d.rngM1<<1 | 1
		d.bits <<= 1
		d.nBits--
	}
	return bit
}

// readUint 解码一个 n 比特无符号整数，最高位在前。
// readUint decodes an n bit unsigned integer, most significant bit first.
func (d *boolDecoder) readUint(prob uint8, n uint8) uint32 {
	var v uint32
	for ; n > 0; n-- {
		v <<= 1
		if d.readBit(prob) {
			v |= 1
		}
	}
	return v
}

// readInt 解码一个 n 比特幅值，后跟一个符号位。
// readInt decodes an n bit magnitude followed by a sign bit.
func (d *boolDecoder) readInt(prob uint8, n uint8) int32 {
	v := d.readUint(prob, n)
	if d.readBit(prob) {
		return -int32(v)
	}
	return int32(v)
}

// readOptionalInt 解码一个“很可能为零”的值：先解码一个存在位，
// 为 0 时直接返回，否则再解码完整的 int。
// readOptionalInt decodes a value that is likely zero: it decodes a
// presence bit first and returns 0 when that bit is clear; otherwise it
// decodes a full int.
func (d *boolDecoder) readOptionalInt(prob uint8, n uint8) int32 {
	if !d.readBit(prob) {
		return 0
	}
	return d.readInt(prob, n)
}
