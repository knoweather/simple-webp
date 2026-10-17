package simple_webp

import (
	"encoding/binary"
	"io"

	"github.com/pkg/errors"
)

// webp is a RIFF container: every multi-byte integer is little-endian.
var by = binary.LittleEndian

// FourCC is a 4 byte chunk type code, e.g. "VP8L".
type FourCC string

// The chunk types of the WebP container, see
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

// maxChunkSize is the largest chunk payload accepted while parsing, it keeps
// a corrupt length field from allocating gigabytes.
const maxChunkSize = 1 << 30

// riffChunk is one chunk of the container in raw form: the four character
// code and the payload without the 8 byte header and the padding byte.
type riffChunk struct {
	name FourCC
	data []byte
}

// validFourCC reports whether every byte of the code is printable ASCII.
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
// padding byte that is present when the payload size is odd.
// It returns io.EOF when the stream ends exactly at a chunk boundary.
func readRiffChunk(r io.Reader) (*riffChunk, error) {
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
	if size > maxChunkSize {
		return nil, errors.Errorf("invalid webp: chunk %q too large: %d bytes", string(name), size)
	}
	data := make([]byte, size)
	if _, err := io.ReadFull(r, data); err != nil {
		return nil, errors.WithMessagef(err, "invalid webp: can not read chunk %q data", string(name))
	}
	if size&1 == 1 {
		var pad [1]byte
		if _, err := io.ReadFull(r, pad[:]); err != nil {
			return nil, errors.WithMessagef(err, "invalid webp: can not read chunk %q padding", string(name))
		}
	}
	return &riffChunk{name: name, data: data}, nil
}

// chunkSize returns the number of bytes a chunk occupies in the stream,
// including its 8 byte header and the padding byte.
func chunkSize(dataLen int) int {
	return 8 + dataLen + dataLen&1
}

// writeRiffChunk writes one chunk including the padding byte.
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
func readUint24(b []byte) uint32 {
	return uint32(b[0]) | uint32(b[1])<<8 | uint32(b[2])<<16
}

// writeUint24 writes a 24 bit little-endian unsigned integer into b[:3].
func writeUint24(b []byte, v uint32) {
	b[0] = byte(v)
	b[1] = byte(v >> 8)
	b[2] = byte(v >> 16)
}
