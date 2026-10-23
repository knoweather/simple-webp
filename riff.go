package simple_webp

import (
	"bytes"
	"encoding/binary"
	"io"

	"github.com/pkg/errors"
)

// by is the byte order of the RIFF container: every multi-byte integer in a
// WebP stream is little-endian, as mandated by the RIFF specification.
var by = binary.LittleEndian

// FourCC is a 4 byte chunk type code, e.g. "VP8L".
type FourCC string

// Chunk type codes of the WebP container, see
// https://developers.google.com/speed/webp/docs/riff_container
//
// WebP 容器的各 chunk 类型码，见
// https://developers.google.com/speed/webp/docs/riff_container
const (
	RIFFChunk FourCC = "RIFF"
	WEBPChunk FourCC = "WEBP"

	VP8XChunk FourCC = "VP8X"
	VP8Chunk  FourCC = "VP8 "
	VP8LChunk FourCC = "VP8L"
	ALPHChunk FourCC = "ALPH"
	ANIMChunk FourCC = "ANIM"
	ANMFChunk FourCC = "ANMF"
	ICCPChunk FourCC = "ICCP"
	EXIFChunk FourCC = "EXIF"
	XMPChunk  FourCC = "XMP "
)

var (
	riffTag = []byte("RIFF")
	webpTag = []byte("WEBP")
)

// maxChunkSize is the largest chunk payload accepted while parsing; it keeps
// a corrupt length field from allocating gigabytes.
//
// maxChunkSize 是解析时接受的最大 chunk 载荷长度，防止损坏的长度字段触发 GB 级分配。
const maxChunkSize = 1 << 30

// riffChunk is one chunk of the container in raw form: the four character
// code and the payload without the 8 byte header and the padding byte.
//
// riffChunk 是容器中一个 chunk 的原始形态：四字符码加上载荷，不含 8 字节头部与填充字节。
type riffChunk struct {
	name FourCC
	data []byte
	// pad 是载荷长度为奇数时读入的填充字节；规范要求它为 0，严格模式会检查。
	// pad is the padding byte read for an odd payload length; the spec requires
	// zero and strict parsing checks it.
	pad byte
}

// validFourCC reports whether every byte of the code is printable ASCII.
//
// validFourCC 判断码的每个字节是否都是可打印 ASCII（RIFF 要求 chunk 类型为可打印字符）。
func validFourCC(name FourCC) bool {
	if len(name) != 4 {
		return false
	}
	for i := 0; i < 4; i++ {
		if name[i] < 0x20 || name[i] > 0x7e {
			return false
		}
	}
	return true
}

// readRiffChunk reads one chunk: fourcc, uint32 payload size, payload and the
// padding byte that is present when the payload size is odd. A payload larger
// than maxChunk is rejected before anything is allocated; a non-positive
// maxChunk falls back to the default maxChunkSize.
// It returns io.EOF when the stream ends exactly at a chunk boundary.
//
// readRiffChunk 读取一个 chunk：四字符码、uint32 载荷长度、载荷，以及载荷长度为奇数时
// 存在的 1 字节填充。超过 maxChunk 的载荷在分配任何内存之前就被拒绝；maxChunk 非正
// 时回退到默认的 maxChunkSize。若流恰好结束在 chunk 边界上，则返回 io.EOF。
func readRiffChunk(r io.Reader, maxChunk int) (*riffChunk, error) {
	if maxChunk <= 0 {
		maxChunk = maxChunkSize
	}
	var head [8]byte
	if _, err := io.ReadFull(r, head[:]); err != nil {
		if errors.Is(err, io.EOF) {
			return nil, io.EOF
		}
		return nil, errors.WithMessage(err, "invalid webp: can not read chunk header")
	}
	name := FourCC(head[:4])
	if !validFourCC(name) {
		return nil, errors.Errorf("invalid webp: bad chunk type %q", string(name))
	}
	size := by.Uint32(head[4:8])
	// 用 int64 比较，避免 MaxChunkSize 被调用方设成超过 32 位时截断。
	// Compare as int64 so a MaxChunkSize above 32 bits set by the caller does
	// not wrap around.
	if int64(size) > int64(maxChunk) {
		return nil, errors.Errorf("invalid webp: chunk %q too large: %d bytes", string(name), size)
	}
	// 载荷流式读入：内存只随实际到达的数据增长，声明长度再大也不会预分配。
	// io.CopyN 在数据不足时返回 io.EOF，这里统一换成 io.ErrUnexpectedEOF，
	// 否则读循环会把它当成「流在 chunk 边界上正常结束」而静默吞掉截断。
	// Stream the payload in, so the buffer grows with the data that actually
	// arrives instead of the declared length. io.CopyN reports io.EOF when the
	// stream is short, which is turned into io.ErrUnexpectedEOF: the parse loop
	// treats io.EOF as a clean end of stream and would swallow the truncation.
	buf := bytes.NewBuffer(nil)
	n, err := io.CopyN(buf, r, int64(size))
	if n != int64(size) {
		if err == nil || errors.Is(err, io.EOF) {
			err = io.ErrUnexpectedEOF
		}
		return nil, errors.WithMessagef(err, "invalid webp: can not read chunk %q data", string(name))
	}
	data := buf.Bytes()
	if size == 0 {
		data = []byte{}
	}
	var pad byte
	if size&1 == 1 {
		var p [1]byte
		if _, err := io.ReadFull(r, p[:]); err != nil {
			return nil, errors.WithMessagef(err, "invalid webp: can not read chunk %q padding", string(name))
		}
		pad = p[0]
	}
	return &riffChunk{name: name, data: data, pad: pad}, nil
}

// chunkSize returns the number of bytes a chunk occupies in the stream,
// including its 8 byte header and the padding byte.
//
// chunkSize 返回一个 chunk 在流中占据的字节数，含 8 字节头部与（长度为奇数时的）填充字节。
func chunkSize(dataLen int) int {
	return 8 + dataLen + dataLen&1
}

// writeRiffChunk writes one chunk including the padding byte.
//
// writeRiffChunk 写出一个 chunk，含头部与填充字节。
func writeRiffChunk(w io.Writer, name FourCC, data []byte) error {
	if !validFourCC(name) {
		return errors.Errorf("invalid chunk type %q", string(name))
	}
	if len(data) > maxChunkSize {
		return errors.Errorf("chunk %q data too large: %d bytes", string(name), len(data))
	}
	var head [8]byte
	copy(head[:4], string(name))
	by.PutUint32(head[4:8], uint32(len(data)))
	if _, err := w.Write(head[:]); err != nil {
		return err
	}
	if _, err := w.Write(data); err != nil {
		return err
	}
	if len(data)&1 == 1 {
		var pad [1]byte
		if _, err := w.Write(pad[:]); err != nil {
			return err
		}
	}
	return nil
}

// marshalRiffChunk builds the bytes of one chunk including header and padding.
//
// marshalRiffChunk 拼出一个 chunk 的完整字节：头部、载荷与填充字节。
func marshalRiffChunk(name FourCC, data []byte) []byte {
	out := make([]byte, 0, chunkSize(len(data)))
	var head [8]byte
	copy(head[:4], string(name))
	by.PutUint32(head[4:8], uint32(len(data)))
	out = append(out, head[:]...)
	out = append(out, data...)
	if len(data)&1 == 1 {
		out = append(out, 0)
	}
	return out
}

// readUint24 reads a 24 bit little-endian unsigned integer.
//
// readUint24 按小端序读出 24 位无符号整数（ANIM/ANMF 帧定时字段使用）。
func readUint24(b []byte) uint32 {
	return uint32(b[0]) | uint32(b[1])<<8 | uint32(b[2])<<16
}

// writeUint24 writes a 24 bit little-endian unsigned integer into b[:3].
//
// writeUint24 按小端序把 24 位无符号整数写入 b[:3]。
func writeUint24(b []byte, v uint32) {
	b[0] = byte(v)
	b[1] = byte(v >> 8)
	b[2] = byte(v >> 16)
}
