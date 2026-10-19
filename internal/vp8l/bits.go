// Package vp8l 实现 WebP 无损（VP8L）位流解码器，规范见
// https://developers.google.com/speed/webp/docs/webp_lossless_bitstream_specification
//
// Package vp8l implements the WebP lossless (VP8L) bitstream decoder, see
// https://developers.google.com/speed/webp/docs/webp_lossless_bitstream_specification
//
// 纯 Go 实现：不使用 cgo，无任何第三方依赖。
// It is a pure Go implementation: no cgo, no third-party dependency.
package vp8l

import (
	"errors"
	"io"
)

// 位流级哨兵错误：调用方可用 errors.Is 判定失败类别。
// Bitstream-level sentinel errors; callers classify failures with errors.Is.
var (
	errBadBitstream   = errors.New("vp8l: invalid bitstream")
	errBadHuffmanCode = errors.New("vp8l: invalid huffman code")
)

// bitReader 按最低有效位优先读取比特，与 VP8L 位流的位序要求一致。
// 比特缓存在一个 64 位窗口中，因此一次最多 32 位的读取只需一次掩码加一次移位。
//
// bitReader reads bits least significant bit first, as the VP8L bitstream
// requires. Bits are buffered in a 64-bit window so a read of up to 32 bits
// costs a mask and a shift.
type bitReader struct {
	data  []byte
	pos   int
	bits  uint64
	nbits uint
	err   error
}

// newBitReader 返回绑定到 data 的位读取器，data 的生命周期由调用方持有。
// newBitReader returns a bit reader bound to data; the caller retains data.
func newBitReader(data []byte) *bitReader {
	return &bitReader{data: data}
}

// fill 把窗口补满到至少 57 个缓存比特，或者直到输入数据耗尽为止。
// fill tops the window up to at least 57 buffered bits, or until the input is
// exhausted.
func (r *bitReader) fill() {
	for r.nbits <= 56 && r.pos < len(r.data) {
		r.bits |= uint64(r.data[r.pos]) << r.nbits
		r.pos++
		r.nbits += 8
	}
}

// readBits 读取 n（0..32）位并按无符号整数返回；输入不足时记录
// io.ErrUnexpectedEOF 并返回 0，后续读取继续复用该错误。
// readBits reads n (0..32) bits and returns them as an unsigned integer. On
// truncated input it records io.ErrUnexpectedEOF and returns 0; the error is
// sticky and reused by later reads.
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

// bitPos 返回到目前为止消耗的比特总数，供错误消息定位使用。
// bitPos returns the number of bits consumed so far, for error messages.
func (r *bitReader) bitPos() int {
	return r.pos*8 - int(r.nbits)
}

// readBit 读取单个比特，等价于 readBits(1)。
// readBit reads a single bit; it is equivalent to readBits(1).
func (r *bitReader) readBit() uint32 {
	return r.readBits(1)
}
