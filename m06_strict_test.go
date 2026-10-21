package simple_webp

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// strictANMF 构造一个 ANMF chunk：头部给出坐标、尺寸、时长与标志，subs 为嵌套
// 子 chunk 的完整字节。
//
// strictANMF builds an ANMF chunk: the header carries the position, size,
// duration and flags, and subs holds the fully serialized sub-chunks.
func strictANMF(x, y, w, h, duration int, flags byte, subs ...[]byte) []byte {
	head := make([]byte, 16)
	writeUint24(head[0:3], uint32(x/2))
	writeUint24(head[3:6], uint32(y/2))
	writeUint24(head[6:9], uint32(w-1))
	writeUint24(head[9:12], uint32(h-1))
	writeUint24(head[12:15], uint32(duration))
	head[15] = flags
	for _, sub := range subs {
		head = append(head, sub...)
	}
	return chunkBytes("ANMF", head)
}

// strictVP8L 构造一个最小但合法的 VP8L 载荷（5 字节头）。
//
// strictVP8L builds a minimal but valid VP8L payload (the 5-byte header).
func strictVP8L(w, h int) []byte {
	out := make([]byte, 5)
	out[0] = 0x2f
	by.PutUint32(out[1:5], uint32(w-1)|uint32(h-1)<<14)
	return out
}

// chunkBytesPad 构造一个 chunk，但把奇数长度时的填充字节写成指定值。
//
// chunkBytesPad builds a chunk with a chosen padding byte for odd payloads.
func chunkBytesPad(name string, data []byte, pad byte) []byte {
	out := chunkBytes(name, data)
	if len(data)&1 == 1 {
		out[len(out)-1] = pad
	}
	return out
}

// TestStrictAcceptsFixtures 校验所有 testdata 样例都能通过严格模式。
//
// TestStrictAcceptsFixtures checks that every testdata fixture parses in
// strict mode.
func TestStrictAcceptsFixtures(t *testing.T) {
	entries, err := os.ReadDir("testdata")
	if err != nil {
		t.Skip("no testdata")
	}
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".webp") {
			continue
		}
		data, err := os.ReadFile(filepath.Join("testdata", e.Name()))
		if err != nil {
			t.Fatal(err)
		}
		if _, err := ParseWebpBytesWith(data, ParseOptions{Strict: true}); err != nil {
			t.Errorf("%s: %v", e.Name(), err)
		}
	}
}

// TestStrictRejectsViolations 覆盖 M-06 的每条 MUST：严格模式报错，宽松模式
// 仍能解析同一个流。
//
// TestStrictRejectsViolations covers each M-06 MUST rule: strict mode reports
// an error while lenient mode still parses the same stream.
func TestStrictRejectsViolations(t *testing.T) {
	vp8 := func(w, h int) []byte { return chunkBytes("VP8 ", vp8Payload(w, h)) }
	anim := chunkBytes("ANIM", make([]byte, 6))
	alph := chunkBytes("ALPH", []byte{0x00, 0x01, 0x02})
	als := func(b []byte) []byte { return b }

	reservedFlags := vp8xPayload(FlagAlpha|0x01, 8, 8)
	reservedBits := vp8xPayload(FlagAlpha, 8, 8)
	reservedBits[1] = 1

	cases := map[string][]byte{
		"VP8X not first": buildWebp(
			vp8(8, 8),
			chunkBytes("VP8X", vp8xPayload(FlagAlpha, 8, 8)),
		),
		"ICCP after ANIM": buildWebp(
			chunkBytes("VP8X", vp8xPayload(FlagAnimation, 32, 16)),
			anim,
			chunkBytes("ICCP", []byte("profile")),
			strictANMF(0, 0, 32, 16, 100, 0, vp8(32, 16)),
		),
		"VP8X reserved flag bits": buildWebp(
			chunkBytes("VP8X", reservedFlags),
			als(vp8(8, 8)),
		),
		"VP8X reserved payload bits": buildWebp(
			chunkBytes("VP8X", reservedBits),
			vp8(8, 8),
		),
		"canvas area too large": buildWebp(
			chunkBytes("VP8X", vp8xPayload(FlagAlpha, 65536, 65536)),
			vp8(8, 8),
		),
		"animation flag without ANIM": buildWebp(
			chunkBytes("VP8X", vp8xPayload(FlagAnimation, 32, 16)),
			strictANMF(0, 0, 32, 16, 100, 0, vp8(32, 16)),
		),
		"ANIM without the flag": buildWebp(
			chunkBytes("VP8X", vp8xPayload(FlagAlpha, 32, 16)),
			anim,
			strictANMF(0, 0, 32, 16, 100, 0, vp8(32, 16)),
		),
		"frame outside the canvas": buildWebp(
			chunkBytes("VP8X", vp8xPayload(FlagAnimation, 32, 32)),
			anim,
			strictANMF(28, 0, 32, 16, 100, 0, vp8(32, 16)),
		),
		"VP8L frame with ALPH": buildWebp(
			chunkBytes("VP8X", vp8xPayload(FlagAnimation, 32, 16)),
			anim,
			strictANMF(0, 0, 32, 16, 100, 0, alph, chunkBytes("VP8L", strictVP8L(32, 16))),
		),
		"non-zero padding": buildWebp(
			chunkBytes("VP8X", vp8xPayload(FlagAlpha, 8, 8)),
			vp8(8, 8),
			chunkBytesPad("XYZW", []byte("odd"), 0xaa),
		),
	}

	for name, stream := range cases {
		if _, err := ParseWebpBytesWith(stream, ParseOptions{Strict: true}); err == nil {
			t.Errorf("%s: strict mode accepted the stream", name)
			continue
		}
		if _, err := ParseWebpBytes(stream); err != nil {
			t.Errorf("%s: lenient mode rejected the stream: %v", name, err)
		}
	}
}

// TestParseWebpWithVerifySize 校验 ParseOptions.VerifySize 与旧的 verifySize
// 参数等价。
//
// TestParseWebpWithVerifySize checks that ParseOptions.VerifySize matches the
// old verifySize argument.
func TestParseWebpWithVerifySize(t *testing.T) {
	stream := buildWebp(chunkBytes("VP8 ", vp8Payload(8, 8)))
	bad := append([]byte(nil), stream...)
	by.PutUint32(bad[4:8], 1234)
	if _, err := ParseWebpWith(bytes.NewReader(bad), ParseOptions{VerifySize: true}); err == nil {
		t.Fatal("expected a RIFF size mismatch")
	}
	if _, err := ParseWebpWith(bytes.NewReader(stream), ParseOptions{VerifySize: true}); err != nil {
		t.Fatalf("valid stream rejected: %v", err)
	}
}
