// Package vp8l implements the WebP lossless bitstream decoder, see
// https://developers.google.com/speed/webp/docs/webp_lossless_bitstream_specification
//
// It is a pure Go implementation: no cgo, no third party dependency.
package vp8l

import (
	"errors"
	"io"
)

var (
	errBadBitstream   = errors.New("vp8l: invalid bitstream")
	errBadHuffmanCode = errors.New("vp8l: invalid huffman code")
)

// bitReader reads bits least significant bit first, as the VP8L bitstream
// requires. Bits are buffered in a 64 bit window so a read of up to 32 bits
// costs a mask and a shift.
type bitReader struct {
	data  []byte
	pos   int
	bits  uint64
	nbits uint
	err   error
}

func newBitReader(data []byte) *bitReader {
	return &bitReader{data: data}
}

// fill tops the window up to at least 57 buffered bits, or until the input is
// exhausted.
func (r *bitReader) fill() {
	for r.nbits <= 56 && r.pos < len(r.data) {
		r.bits |= uint64(r.data[r.pos]) << r.nbits
		r.pos++
		r.nbits += 8
	}
}

func (r *bitReader) readBits(n uint) uint32 {
	if n == 0 {
		return 0
	}
	if r.nbits < n {
		r.fill()
		if r.nbits < n {
			r.err = io.ErrUnexpectedEOF
			return 0
		}
	}
	v := uint32(r.bits & (uint64(1)<<n - 1))
	r.bits >>= n
	r.nbits -= n
	return v
}

// bitPos returns the number of bits consumed so far, for error messages.
func (r *bitReader) bitPos() int {
	return r.pos*8 - int(r.nbits)
}

func (r *bitReader) readBit() uint32 {
	return r.readBits(1)
}
