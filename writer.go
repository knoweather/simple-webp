package simple_webp

import (
	"bytes"
	"io"
	"math"

	"github.com/pkg/errors"
)

// countingWriter counts the bytes written to the underlying writer.
type countingWriter struct {
	w io.Writer
	n int64
}

func (cw *countingWriter) Write(p []byte) (int, error) {
	n, err := cw.w.Write(p)
	cw.n += int64(n)
	return n, err
}

// WriteTo writes the webp stream of the current state, implementing
// io.WriterTo. Chunks that were consumed while parsing are re-serialized from
// their parsed structs, so modifications made to the structs (or chunks
// added/deleted through AddRawChunk / DeleteRawChunk) are reflected, while the
// chunks that were left raw are copied verbatim. Parsing and writing back a
// file without touching anything reproduces it byte for byte.
//
// The image payload (VP8 / VP8L / ALPH) is never re-encoded, so editing the
// metadata chunks does not touch the pixels at all.
func (w *Webp) WriteTo(out io.Writer) (int64, error) {
	if w.origChunks == nil {
		return 0, errors.New("webp is not parsed")
	}
	names := make([]FourCC, 0, len(w.origChunks))
	payloads := make([][]byte, 0, len(w.origChunks))
	var anmfIdx int
	for i, c := range w.origChunks {
		if w.origDeleted[i] {
			continue
		}
		if !w.origConsumed[i] {
			names = append(names, c.name)
			payloads = append(payloads, c.data)
			continue
		}
		data, err := w.emitConsumed(c, &anmfIdx)
		if err != nil {
			return 0, err
		}
		names = append(names, c.name)
		payloads = append(payloads, data)
	}

	// the RIFF size field counts everything after it: the "WEBP" tag and all
	// chunks with their headers and padding
	total := 4
	for _, p := range payloads {
		total += chunkSize(len(p))
	}
	if int64(total) > math.MaxUint32 {
		return 0, errors.New("webp stream too large")
	}

	var head [12]byte
	copy(head[:4], riffTag)
	by.PutUint32(head[4:8], uint32(total))
	copy(head[8:], webpTag)

	cw := &countingWriter{w: out}
	if _, err := cw.Write(head[:]); err != nil {
		return cw.n, err
	}
	for i := range payloads {
		if err := writeRiffChunk(cw, names[i], payloads[i]); err != nil {
			return cw.n, err
		}
	}
	return cw.n, nil
}

// emitConsumed builds the payload of a consumed chunk from its parsed struct.
func (w *Webp) emitConsumed(c *riffChunk, anmfIdx *int) ([]byte, error) {
	switch c.name {
	case VP8XChunk:
		if w.VP8X == nil {
			return c.data, nil
		}
		return w.VP8X.data(), nil

	case VP8Chunk:
		if w.VP8 == nil {
			return c.data, nil
		}
		return w.VP8.Data, nil

	case VP8LChunk:
		if w.VP8L == nil {
			return c.data, nil
		}
		return w.VP8L.Data, nil

	case ALPHChunk:
		if w.ALPH == nil {
			return c.data, nil
		}
		return append([]byte{w.ALPH.header()}, w.ALPH.Data...), nil

	case ANIMChunk:
		if w.ANIM == nil {
			return c.data, nil
		}
		return w.ANIM.data(), nil

	case ANMFChunk:
		if *anmfIdx >= len(w.ANMFs) {
			return c.data, nil
		}
		frame := w.ANMFs[*anmfIdx]
		*anmfIdx++
		return frame.data(), nil

	case ICCPChunk:
		if w.ICCP == nil {
			return c.data, nil
		}
		return w.ICCP.Data, nil

	case EXIFChunk:
		if w.EXIF == nil {
			return c.data, nil
		}
		return w.EXIF.Data, nil

	case XMPChunk:
		if w.XMP == nil {
			return c.data, nil
		}
		return w.XMP.Data, nil
	}
	// a custom chunk type parsed by the user has no re-serializer here, keep
	// its original raw data
	return c.data, nil
}

// MarshalBytes returns the webp stream of the current state as a byte slice,
// see WriteTo for the details.
func (w *Webp) MarshalBytes() ([]byte, error) {
	var buf bytes.Buffer
	if _, err := w.WriteTo(&buf); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}
