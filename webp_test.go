package simple_webp

import (
	"bytes"
	"os"
	"testing"
)

// chunkBytes builds one RIFF chunk with header and padding.
func chunkBytes(name string, data []byte) []byte {
	out := make([]byte, 8, 8+len(data)+1)
	copy(out[:4], name)
	by.PutUint32(out[4:8], uint32(len(data)))
	out = append(out, data...)
	if len(data)&1 == 1 {
		out = append(out, 0)
	}
	return out
}

// buildWebp assembles a RIFF/WEBP stream out of already serialized chunks.
func buildWebp(chunks ...[]byte) []byte {
	body := []byte("WEBP")
	for _, c := range chunks {
		body = append(body, c...)
	}
	out := make([]byte, 8, 8+len(body))
	copy(out, "RIFF")
	by.PutUint32(out[4:8], uint32(len(body)))
	return append(out, body...)
}

// vp8xPayload builds the 10 byte VP8X payload.
func vp8xPayload(flags uint8, w, h int) []byte {
	out := make([]byte, 10)
	out[0] = flags
	writeUint24(out[4:7], uint32(w-1))
	writeUint24(out[7:10], uint32(h-1))
	return out
}

// vp8Payload builds a minimal but well formed VP8 key frame header.
func vp8Payload(w, h int) []byte {
	out := make([]byte, 10)
	tag := uint32(0x10) // show_frame, key frame, version 0, empty first partition
	out[0], out[1], out[2] = byte(tag), byte(tag>>8), byte(tag>>16)
	out[3], out[4], out[5] = 0x9d, 0x01, 0x2a
	by.PutUint16(out[6:8], uint16(w))
	by.PutUint16(out[8:10], uint16(h))
	return out
}

func TestParseSyntheticContainer(t *testing.T) {
	vp8 := vp8Payload(64, 32)
	exif := []byte("Exif\x00\x00odd") // odd length, exercises the padding byte
	xmp := []byte("<x:xmpmeta/>")
	unknown := []byte{0x01, 0x02, 0x03}

	stream := buildWebp(
		chunkBytes("VP8X", vp8xPayload(FlagAlpha|FlagEXIF|FlagXMP, 64, 32)),
		chunkBytes("ICCP", []byte("profile")),
		chunkBytes("ALPH", append([]byte{0x01}, make([]byte, 16)...)),
		chunkBytes("VP8 ", vp8),
		chunkBytes("EXIF", exif),
		chunkBytes("XMP ", xmp),
		chunkBytes("ZZZZ", unknown),
	)

	w, err := ParseWebp(bytes.NewReader(stream), true)
	if err != nil {
		t.Fatal(err)
	}
	if w.Width != 64 || w.Height != 32 {
		t.Fatalf("size: got %dx%d", w.Width, w.Height)
	}
	if !w.HasAlpha || w.HasAnimation {
		t.Fatalf("flags: alpha=%v animation=%v", w.HasAlpha, w.HasAnimation)
	}
	if w.VP8 == nil || w.VP8.Width != 64 || w.VP8.Height != 32 {
		t.Fatalf("vp8: %+v", w.VP8)
	}
	if w.ALPH == nil || w.ALPH.Compression != ALPHCompressionLossless || len(w.ALPH.Data) != 16 {
		t.Fatalf("alph: %+v", w.ALPH)
	}
	if w.ICCP == nil || string(w.ICCP.Data) != "profile" {
		t.Fatalf("iccp: %+v", w.ICCP)
	}
	if w.EXIF == nil || !bytes.Equal(w.EXIF.Data, exif) {
		t.Fatalf("exif: %+v", w.EXIF)
	}
	if w.XMP == nil || !bytes.Equal(w.XMP.Data, xmp) {
		t.Fatalf("xmp: %+v", w.XMP)
	}
	raw := w.RawChunks()
	if len(raw) != 1 || raw[0].Name != FourCC("ZZZZ") || !bytes.Equal(raw[0].Data, unknown) {
		t.Fatalf("raw chunks: %+v", raw)
	}

	// an untouched parse must round-trip byte for byte
	out, err := w.MarshalBytes()
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(out, stream) {
		t.Fatalf("round trip mismatch:\n got %d bytes\nwant %d bytes", len(out), len(stream))
	}
}

func TestParseLosslessContainer(t *testing.T) {
	// VP8L header: signature, 14 bit width-1, 14 bit height-1, alpha bit
	payload := make([]byte, 5)
	payload[0] = 0x2f
	bits := uint32(99) | uint32(49)<<14 | 1<<28
	by.PutUint32(payload[1:5], bits)

	stream := buildWebp(chunkBytes("VP8L", payload))
	w, err := ParseWebpBytes(stream)
	if err != nil {
		t.Fatal(err)
	}
	if w.Width != 100 || w.Height != 50 {
		t.Fatalf("size: got %dx%d", w.Width, w.Height)
	}
	if !w.HasAlpha || !w.VP8L.AlphaUsed {
		t.Fatal("alpha flag not detected")
	}
	if w.VP8X != nil {
		t.Fatal("simple lossless file must not have VP8X")
	}
}

func TestParseAnimatedContainer(t *testing.T) {
	anim := make([]byte, 6)
	by.PutUint32(anim[:4], 0x11223344)
	by.PutUint16(anim[4:6], 3)

	frame := make([]byte, 16)
	writeUint24(frame[0:3], 5)  // x = 10
	writeUint24(frame[3:6], 7)  // y = 14
	writeUint24(frame[6:9], 31) // width 32
	writeUint24(frame[9:12], 15)
	writeUint24(frame[12:15], 120) // 120 ms
	frame[15] = 0x2                // no blend, no dispose
	frame = append(frame, chunkBytes("VP8 ", vp8Payload(32, 16))...)

	stream := buildWebp(
		chunkBytes("VP8X", vp8xPayload(FlagAnimation|FlagAlpha, 100, 80)),
		chunkBytes("ANIM", anim),
		chunkBytes("ANMF", frame),
	)
	w, err := ParseWebpBytes(stream)
	if err != nil {
		t.Fatal(err)
	}
	if !w.HasAnimation || len(w.ANMFs) != 1 {
		t.Fatalf("animation: %v frames=%d", w.HasAnimation, len(w.ANMFs))
	}
	f := w.ANMFs[0]
	if f.X != 10 || f.Y != 14 || f.Width != 32 || f.Height != 16 || f.Duration != 120 {
		t.Fatalf("frame: %+v", f)
	}
	if f.Blend || f.Dispose {
		t.Fatalf("frame flags: blend=%v dispose=%v", f.Blend, f.Dispose)
	}
	if f.VP8 == nil || f.VP8.Width != 32 {
		t.Fatalf("frame vp8: %+v", f.VP8)
	}
	if w.ANIM.LoopCount != 3 || w.ANIM.BackgroundColor != 0x11223344 {
		t.Fatalf("anim: %+v", w.ANIM)
	}

	out, err := w.MarshalBytes()
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(out, stream) {
		t.Fatal("animated round trip mismatch")
	}
}

type customChunk struct {
	Data []byte
}

func (c *customChunk) ChunkName() FourCC { return FourCC("ABCD") }

func (c *customChunk) Parse(chunk *riffChunk) error {
	c.Data = chunk.data
	return nil
}

func TestCustomChunk(t *testing.T) {
	payload := []byte("hello custom")
	stream := buildWebp(
		chunkBytes("ABCD", payload),
		chunkBytes("VP8 ", vp8Payload(8, 8)),
	)
	w, err := ParseWebpBytes(stream)
	if err != nil {
		t.Fatal(err)
	}
	c := &customChunk{}
	if err = w.ParseChunk(c); err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(c.Data, payload) {
		t.Fatalf("custom chunk data: %q", c.Data)
	}
	list, err := w.GetOtherChunkByName("ABCD")
	if err != nil || len(list) != 1 {
		t.Fatalf("other chunk registry: %v %v", list, err)
	}
	if len(w.RawChunks()) != 0 {
		t.Fatal("custom chunk should be consumed")
	}
}

func TestAddDeleteRawChunk(t *testing.T) {
	stream := buildWebp(chunkBytes("VP8 ", vp8Payload(8, 8)))
	w, err := ParseWebpBytes(stream)
	if err != nil {
		t.Fatal(err)
	}
	if err = w.AddRawChunk("EXIF", []byte("new exif")); err != nil {
		t.Fatal(err)
	}
	if err = w.AddRawChunk("XMP ", []byte("new xmp")); err != nil {
		t.Fatal(err)
	}
	out, err := w.MarshalBytes()
	if err != nil {
		t.Fatal(err)
	}
	w2, err := ParseWebpBytes(out)
	if err != nil {
		t.Fatal(err)
	}
	if w2.EXIF == nil || string(w2.EXIF.Data) != "new exif" {
		t.Fatalf("exif after add: %+v", w2.EXIF)
	}
	if w2.XMP == nil || string(w2.XMP.Data) != "new xmp" {
		t.Fatalf("xmp after add: %+v", w2.XMP)
	}
	// the added chunks must precede the image data
	if bytes.Index(out, []byte("new exif")) > bytes.Index(out, []byte("VP8 ")) {
		t.Fatal("EXIF chunk must be written before the image data")
	}

	if n := w2.DeleteRawChunk("EXIF"); n != 1 {
		t.Fatalf("deleted %d chunks, want 1", n)
	}
	out2, err := w2.MarshalBytes()
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(out2, []byte("new exif")) {
		t.Fatal("deleted chunk still present")
	}
}

func TestBadSignature(t *testing.T) {
	if _, err := ParseWebpBytes([]byte("RIFF\x04\x00\x00\x00WAVE")); err == nil {
		t.Fatal("expected an error for a non webp stream")
	}
	if _, err := ParseWebpBytes([]byte("not a riff stream")); err == nil {
		t.Fatal("expected an error for a truncated stream")
	}
}

func TestVerifySize(t *testing.T) {
	stream := buildWebp(chunkBytes("VP8 ", vp8Payload(8, 8)))
	bad := append([]byte(nil), stream...)
	by.PutUint32(bad[4:8], 1234)
	if _, err := ParseWebpBytes(bad); err != nil {
		t.Fatalf("size is not verified by default: %v", err)
	}
	if _, err := ParseWebp(bytes.NewReader(bad), true); err == nil {
		t.Fatal("expected a RIFF size mismatch error")
	}
}

func TestRealFileRoundTrip(t *testing.T) {
	path := os.Getenv("WEBP_TEST_FILE")
	if path == "" {
		t.Skip("WEBP_TEST_FILE is not set")
	}
	stream, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	w, err := ParseWebp(bytes.NewReader(stream), true)
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("%s: %dx%d alpha=%v anim=%v frames=%d lossy=%v lossless=%v",
		path, w.Width, w.Height, w.HasAlpha, w.HasAnimation, len(w.ANMFs), w.VP8 != nil, w.VP8L != nil)
	out, err := w.MarshalBytes()
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(out, stream) {
		t.Fatalf("round trip mismatch: got %d bytes want %d", len(out), len(stream))
	}
}
