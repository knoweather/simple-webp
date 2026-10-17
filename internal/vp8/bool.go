package vp8

// boolDecoder is the arithmetic (boolean) entropy decoder of section 7.
// It follows the "range minus one" formulation: rngM1 is range-1 and the
// value window holds the next 16 bits, most significant bit first.
type boolDecoder struct {
	buf   []byte
	pos   int
	rngM1 uint32
	bits  uint32
	nBits uint8
	eof   bool
}

// uniformProb codes a bit with 50% probability.
const uniformProb = 128

func (d *boolDecoder) init(buf []byte) {
	d.buf = buf
	d.pos = 0
	d.rngM1 = 254
	d.bits = 0
	d.nBits = 0
	d.eof = false
}

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
	// renormalize: at most seven shifts are needed to get range >= 128
	for d.rngM1 < 127 {
		d.rngM1 = d.rngM1<<1 | 1
		d.bits <<= 1
		d.nBits--
	}
	return bit
}

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

// readInt decodes an n bit magnitude followed by a sign bit.
func (d *boolDecoder) readInt(prob uint8, n uint8) int32 {
	v := d.readUint(prob, n)
	if d.readBit(prob) {
		return -int32(v)
	}
	return int32(v)
}

// readOptionalInt decodes a value whose likely value is zero.
func (d *boolDecoder) readOptionalInt(prob uint8, n uint8) int32 {
	if !d.readBit(prob) {
		return 0
	}
	return d.readInt(prob, n)
}
