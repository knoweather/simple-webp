package simple_webp

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

// chunkNames 按顺序列出流中顶层 chunk 的类型码，忽略头部与填充字节。
//
// chunkNames lists the top-level chunk type codes of a stream in file order,
// skipping headers and padding bytes.
func chunkNames(t *testing.T, stream []byte) []FourCC {
	t.Helper()
	if len(stream) < 12 || string(stream[8:12]) != "WEBP" {
		t.Fatalf("not a webp stream: %d bytes", len(stream))
	}
	var names []FourCC
	for off := 12; off+8 <= len(stream); {
		size := int(by.Uint32(stream[off+4 : off+8]))
		names = append(names, FourCC(stream[off:off+4]))
		off += 8 + size + size&1
	}
	return names
}

// readFixture 读取 testdata 下的样例，缺失时跳过测试。
//
// readFixture loads a fixture from testdata and skips the test when it is
// missing.
func readFixture(t *testing.T, name string) []byte {
	t.Helper()
	path := filepath.Join("testdata", name)
	if _, err := os.Stat(path); err != nil {
		t.Skipf("no fixture %s", name)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return data
}

// TestSynthesizedVP8XKeepsAlphaAndAnimation 覆盖 F-01：给纯 VP8L 带透明的文件
// 补元数据时，自动创建的 VP8X 必须置上 alpha 位；animation 位同理；而完全没做
// 修改的文件不能凭空长出 VP8X。
//
// TestSynthesizedVP8XKeepsAlphaAndAnimation covers F-01: adding metadata to a
// plain VP8L file with transparency must synthesize a VP8X whose alpha bit is
// set (and the animation bit likewise), while an untouched file must not gain a
// VP8X it never had.
func TestSynthesizedVP8XKeepsAlphaAndAnimation(t *testing.T) {
	src := readFixture(t, "alpha.webp")
	w, err := ParseWebpBytes(src)
	if err != nil {
		t.Fatal(err)
	}
	if w.VP8X != nil {
		t.Skip("fixture already has VP8X")
	}
	if !w.HasAlpha {
		t.Skip("fixture has no alpha")
	}

	// 未做修改时不得凭空生成 VP8X / no VP8X may appear when nothing changed
	untouched, err := w.MarshalBytes()
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(untouched, src) {
		t.Fatalf("untouched round trip mismatch: got %d bytes, want %d", len(untouched), len(src))
	}

	w.EXIF = &EXIF{Data: []byte("Exif\x00\x00fixf01")}
	out, err := w.MarshalBytes()
	if err != nil {
		t.Fatal(err)
	}
	w2, err := ParseWebpBytes(out)
	if err != nil {
		t.Fatal(err)
	}
	if w2.VP8X == nil {
		t.Fatal("no VP8X was synthesized for the added EXIF chunk")
	}
	if !w2.VP8X.HasAlpha() {
		t.Fatalf("FlagAlpha is missing on the synthesized VP8X (flags=0x%02x)", w2.VP8X.Flags)
	}
	if !w2.VP8X.HasEXIF() {
		t.Fatalf("FlagEXIF is missing on the synthesized VP8X (flags=0x%02x)", w2.VP8X.Flags)
	}
	if !w2.HasAlpha {
		t.Fatal("alpha was dropped from the derived flags")
	}
	if names := chunkNames(t, out); len(names) < 3 || names[0] != VP8XChunk || names[1] != VP8LChunk || names[2] != EXIFChunk {
		t.Fatalf("chunk order: %v", names)
	}
}

// TestChunkInsertionOrder 覆盖 F-02：新 chunk 按类型落到各自的位置上——动画
// 文件里 ICCP 必须在 ANIM 之前，静态文件保持 VP8X、ICCP、图像数据的顺序。
//
// TestChunkInsertionOrder covers F-02: a new chunk lands where its type belongs
// — ICCP precedes ANIM in an animated file, while a still file keeps the order
// VP8X, ICCP, image data.
func TestChunkInsertionOrder(t *testing.T) {
	src := readFixture(t, "anim.webp")
	w, err := ParseWebpBytes(src)
	if err != nil {
		t.Fatal(err)
	}
	if w.VP8X == nil || w.ANIM == nil || len(w.ANMFs) == 0 {
		t.Skip("fixture is not an animated VP8X file")
	}
	if err := w.AddRawChunk(ICCPChunk, []byte("profile")); err != nil {
		t.Fatal(err)
	}
	out, err := w.MarshalBytes()
	if err != nil {
		t.Fatal(err)
	}
	names := chunkNames(t, out)
	want := []FourCC{VP8XChunk, ICCPChunk, ANIMChunk, ANMFChunk, ANMFChunk, ANMFChunk}
	if len(names) != len(want) {
		t.Fatalf("animated chunk order: %v", names)
	}
	for i := range want {
		if names[i] != want[i] {
			t.Fatalf("animated chunk order: %v", names)
		}
	}

	// 静态 VP8L：新 ICCP 必须落在自动创建的 VP8X 之后、图像数据之前
	// still VP8L: the new ICCP goes after the synthesized VP8X, before the image data
	src = readFixture(t, "gradient.webp")
	w, err = ParseWebpBytes(src)
	if err != nil {
		t.Fatal(err)
	}
	if w.VP8X != nil {
		t.Skip("fixture already has VP8X")
	}
	if err := w.AddRawChunk(ICCPChunk, []byte("profile")); err != nil {
		t.Fatal(err)
	}
	out, err = w.MarshalBytes()
	if err != nil {
		t.Fatal(err)
	}
	if names = chunkNames(t, out); len(names) != 3 || names[0] != VP8XChunk || names[1] != ICCPChunk || names[2] != VP8LChunk {
		t.Fatalf("still chunk order: %v", names)
	}
}

// TestANMFEmitsAssignedAlpha 覆盖 F-03 的配套语义：原始流里没有、解析后手工
// 赋值的帧内 ALPH 仍必须被写出来，不能被「按原始顺序重放」吞掉。
//
// TestANMFEmitsAssignedAlpha covers the counterpart of F-03: an ALPH assigned to
// a parsed frame, which the original stream never had, must still be written
// instead of being swallowed by the replay of the original sub-chunk order.
func TestANMFEmitsAssignedAlpha(t *testing.T) {
	w, err := ParseWebpBytes(readFixture(t, "anim.webp"))
	if err != nil {
		t.Fatal(err)
	}
	if len(w.ANMFs) == 0 || w.ANMFs[0].VP8 == nil || w.ANMFs[0].ALPH != nil {
		t.Skip("fixture does not have a plain VP8 frame")
	}
	alpha := []byte{0x11, 0x22, 0x33}
	w.ANMFs[0].ALPH = &ALPH{Compression: ALPHCompressionNone, Data: alpha}
	out, err := w.MarshalBytes()
	if err != nil {
		t.Fatal(err)
	}
	w2, err := ParseWebpBytes(out)
	if err != nil {
		t.Fatalf("re-parse: %v", err)
	}
	if w2.ANMFs[0].ALPH == nil {
		t.Fatal("the assigned ALPH was not written into the frame")
	}
	if !bytes.Equal(w2.ANMFs[0].ALPH.Data, alpha) {
		t.Fatalf("ALPH data mismatch: got %x want %x", w2.ANMFs[0].ALPH.Data, alpha)
	}
}

// TestDeleteConsumedChunks 覆盖 F-05：DeleteRawChunk 能删掉解析时已被消费的
// ANMF / ANIM / ALPH / VP8X，并同步 VP8X 标志位；会产出非法流的删除不生效。
//
// TestDeleteConsumedChunks covers F-05: DeleteRawChunk also removes the chunks
// parsing consumed (ANMF, ANIM, ALPH, VP8X) and keeps the VP8X flags in sync; a
// removal that would leave the file invalid does nothing.
func TestDeleteConsumedChunks(t *testing.T) {
	src := readFixture(t, "anim.webp")
	w, err := ParseWebpBytes(src)
	if err != nil {
		t.Fatal(err)
	}
	if len(w.ANMFs) == 0 || w.ANIM == nil {
		t.Skip("fixture is not animated")
	}
	frames := len(w.ANMFs)

	// 还有帧时删 ANIM 会被拒绝 / deleting ANIM while frames remain is refused
	if n := w.DeleteRawChunk(ANIMChunk); n != 0 {
		t.Fatalf("ANIM deleted while %d frames remain: %d", frames, n)
	}
	if n := w.DeleteRawChunk(ANMFChunk); n != frames+1 {
		t.Fatalf("deleted %d chunks, want %d (frames plus ANIM)", n, frames+1)
	}
	out, err := w.MarshalBytes()
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range chunkNames(t, out) {
		if name == ANMFChunk || name == ANIMChunk {
			t.Fatalf("%q still present after delete: %v", string(name), chunkNames(t, out))
		}
	}
	if w.HasAnimation {
		t.Fatal("HasAnimation is still set after deleting every frame")
	}
	if w.VP8X != nil && w.VP8X.HasAnimation() {
		t.Fatalf("FlagAnimation not cleared (flags=0x%02x)", w.VP8X.Flags)
	}

	// 有损 + alpha：ANIM 无关，ALPH 删除后 alpha 位必须清掉
	// lossy + alpha: deleting ALPH clears the alpha flag
	src = readFixture(t, "lossy_alpha.webp")
	w, err = ParseWebpBytes(src)
	if err != nil {
		t.Fatal(err)
	}
	if w.ALPH == nil || w.VP8 == nil || w.VP8X == nil {
		t.Skip("fixture is not VP8X+ALPH+VP8")
	}
	if n := w.DeleteRawChunk(VP8XChunk); n != 0 {
		t.Fatalf("VP8X deleted while ALPH remains: %d", n)
	}
	if n := w.DeleteRawChunk(ALPHChunk); n != 1 {
		t.Fatalf("ALPH deleted %d chunks, want 1", n)
	}
	if w.VP8X == nil || w.VP8X.HasAlpha() {
		t.Fatalf("FlagAlpha not cleared (flags=0x%02x)", w.VP8X.Flags)
	}
	// 唯一的图像数据不能删 / the only image data cannot be deleted
	if n := w.DeleteRawChunk(VP8Chunk); n != 0 {
		t.Fatalf("the only image data was deleted: %d", n)
	}
	out, err = w.MarshalBytes()
	if err != nil {
		t.Fatal(err)
	}
	w2, err := ParseWebpBytes(out)
	if err != nil {
		t.Fatalf("re-parse after deleting ALPH: %v", err)
	}
	if w2.ALPH != nil || w2.HasAlpha {
		t.Fatal("alpha is still advertised after deleting ALPH")
	}
	// 无扩展特性后 VP8X 可以删掉，退化为简单格式
	// with no extended feature left, the VP8X itself can go
	if n := w2.DeleteRawChunk(VP8XChunk); n != 1 {
		t.Fatalf("VP8X deleted %d chunks, want 1", n)
	}
	out, err = w2.MarshalBytes()
	if err != nil {
		t.Fatal(err)
	}
	names := chunkNames(t, out)
	if len(names) != 1 || names[0] != VP8Chunk {
		t.Fatalf("simple format expected, got %v", names)
	}
	if _, err := ParseWebpBytes(out); err != nil {
		t.Fatalf("the reduced stream does not parse: %v", err)
	}

	// 纯 VP8L 的唯一图像数据同样不能删 / same refusal for a plain VP8L file
	w, err = ParseWebpBytes(readFixture(t, "gradient.webp"))
	if err != nil {
		t.Fatal(err)
	}
	if n := w.DeleteRawChunk(VP8LChunk); n != 0 {
		t.Fatalf("the only VP8L image data was deleted: %d", n)
	}
}

// TestFramesRejectsOversizedCanvas 覆盖 F-06：画布尺寸必须受像素预算约束，
// 不能因为帧很小就放行一个巨大的 VP8X 画布。
//
// TestFramesRejectsOversizedCanvas covers F-06: the canvas size is subject to
// the pixel budget, so tiny frames cannot smuggle in a huge VP8X canvas.
func TestFramesRejectsOversizedCanvas(t *testing.T) {
	frame := make([]byte, 16)
	writeUint24(frame[6:9], 0)  // 宽 1 / width 1
	writeUint24(frame[9:12], 0) // 高 1 / height 1
	frame = append(frame, chunkBytes("VP8 ", vp8Payload(1, 1))...)
	stream := buildWebp(
		chunkBytes("VP8X", vp8xPayload(FlagAnimation, 16383, 16383)),
		chunkBytes("ANIM", make([]byte, 6)),
		chunkBytes("ANMF", frame),
	)
	w, err := ParseWebpBytes(stream)
	if err != nil {
		t.Fatal(err)
	}

	runtime.GC()
	var before, after runtime.MemStats
	runtime.ReadMemStats(&before)
	if _, _, err := w.Frames(1 << 20); !errors.Is(err, ErrImageTooLarge) {
		t.Fatalf("frames of a 16383x16383 canvas: %v", err)
	}
	runtime.ReadMemStats(&after)
	if grew := after.TotalAlloc - before.TotalAlloc; grew > 32<<20 {
		t.Fatalf("frames allocated %d bytes before rejecting the canvas", grew)
	}
	// 0 走默认预算，同样必须拒绝 / 0 means the default budget, also refused
	if _, _, err := w.Frames(0); !errors.Is(err, ErrImageTooLarge) {
		t.Fatalf("default budget: %v", err)
	}
}

// TestFramesTotalBudget 覆盖 F-06 的总量预算：画布像素 × 帧数必须受
// FrameOptions.MaxTotalPixels 约束，边界之内仍能正常解码。
//
// TestFramesTotalBudget covers the F-06 total budget: canvas pixels times frame
// count is bounded by FrameOptions.MaxTotalPixels, while a value at the limit
// still decodes.
func TestFramesTotalBudget(t *testing.T) {
	w, err := ParseWebpBytes(readFixture(t, "anim.webp"))
	if err != nil {
		t.Fatal(err)
	}
	if len(w.ANMFs) == 0 {
		t.Skip("fixture is not animated")
	}
	total := int64(w.Width) * int64(w.Height) * int64(len(w.ANMFs))
	if total <= 1 {
		t.Skipf("fixture total is %d pixels", total)
	}
	if _, _, err := w.FramesWith(FrameOptions{MaxPixels: 1 << 20, MaxTotalPixels: int(total) - 1}); !errors.Is(err, ErrImageTooLarge) {
		t.Fatalf("total budget just below %d pixels: %v", total, err)
	}
	frames, durations, err := w.FramesWith(FrameOptions{MaxPixels: 1 << 20, MaxTotalPixels: int(total)})
	if err != nil {
		t.Fatalf("total budget at the limit: %v", err)
	}
	if len(frames) != len(w.ANMFs) || len(durations) != len(w.ANMFs) {
		t.Fatalf("decoded %d frames and %d durations, want %d", len(frames), len(durations), len(w.ANMFs))
	}
	// Frames(maxPixels) 与 FramesWith 走同一条路径
	// Frames(maxPixels) takes the same path as FramesWith
	if _, _, err := w.Frames(1 << 20); err != nil {
		t.Fatalf("Frames: %v", err)
	}
}

// TestANMFKeepsUnknownSubChunks 覆盖 F-03：帧内的未知子 chunk 必须按原始顺序
// 原样写回，且整个流逐字节相等。
//
// TestANMFKeepsUnknownSubChunks covers F-03: an unknown sub-chunk inside a frame
// must be written back in its original position, so the whole stream round-trips
// byte for byte.
func TestANMFKeepsUnknownSubChunks(t *testing.T) {
	frame := make([]byte, 16)
	writeUint24(frame[6:9], 31)  // 宽 32 / width 32
	writeUint24(frame[9:12], 15) // 高 16 / height 16
	writeUint24(frame[12:15], 100)
	frame = append(frame, chunkBytes("XYZW", []byte("keepme"))...)
	frame = append(frame, chunkBytes("VP8 ", vp8Payload(32, 16))...)

	anim := make([]byte, 6)
	stream := buildWebp(
		chunkBytes("VP8X", vp8xPayload(FlagAnimation, 32, 16)),
		chunkBytes("ANIM", anim),
		chunkBytes("ANMF", frame),
	)

	w, err := ParseWebpBytes(stream)
	if err != nil {
		t.Fatal(err)
	}
	if len(w.ANMFs) != 1 {
		t.Fatalf("frames: %d", len(w.ANMFs))
	}
	out, err := w.MarshalBytes()
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(out, stream) {
		t.Fatalf("unknown sub-chunk lost or reordered: got %d bytes, want %d", len(out), len(stream))
	}
}

// TestParseIgnoresTrailingData 覆盖 F-04：RIFF 声明长度之后的尾部数据不影响
// 解析，严格校验模式下仍然报大小不符。
//
// TestParseIgnoresTrailingData covers F-04: data after the RIFF File Size does
// not break parsing, while strict verification still reports the mismatch.
func TestParseIgnoresTrailingData(t *testing.T) {
	src := readFixture(t, "gradient.webp")
	for _, n := range []int{1, 3, 8, 16} {
		padded := append(append([]byte{}, src...), make([]byte, n)...)
		w, err := ParseWebpBytes(padded)
		if err != nil {
			t.Fatalf("%d trailing bytes: %v", n, err)
		}
		if w.Width <= 0 || w.Height <= 0 {
			t.Fatalf("%d trailing bytes: size %dx%d", n, w.Width, w.Height)
		}
		out, err := w.MarshalBytes()
		if err != nil {
			t.Fatalf("%d trailing bytes: marshal: %v", n, err)
		}
		if !bytes.Equal(out, src) {
			t.Fatalf("%d trailing bytes: trailing data leaked into the output", n)
		}
		if _, err := ParseWebp(bytes.NewReader(padded), true); err == nil {
			t.Fatalf("%d trailing bytes: strict verification accepted the stream", n)
		}
	}
}

// TestTruncatedChunkIsAnError 覆盖 F-07 的配套语义：chunk 头声明了长度而数据
// 不足时必须报错，不能被当成「流恰好在 chunk 边界结束」而静默接受。
//
// TestTruncatedChunkIsAnError covers the counterpart of F-07: a chunk header
// that promises more data than the stream holds must fail, not be mistaken for
// a stream that ends cleanly on a chunk boundary.
func TestTruncatedChunkIsAnError(t *testing.T) {
	truncated := []byte("EXIF")
	truncated = append(truncated, 100, 0, 0, 0)
	truncated = append(truncated, "abc"...)
	stream := buildWebp(chunkBytes("VP8 ", vp8Payload(8, 8)), truncated)
	if _, err := ParseWebpBytes(stream); err == nil {
		t.Fatal("expected an error for a chunk whose payload is truncated")
	}
}

// TestParseTruncatedChunkDoesNotPreallocate 覆盖 F-07：chunk 头声明的长度远超
// 实际数据时，解析必须直接失败，且不得按声明长度分配内存。
//
// TestParseTruncatedChunkDoesNotPreallocate covers F-07: a chunk header that
// declares far more data than the stream holds must fail without allocating
// memory for the declared size.
func TestParseTruncatedChunkDoesNotPreallocate(t *testing.T) {
	head := make([]byte, 20)
	copy(head, "RIFF")
	by.PutUint32(head[4:8], uint32(4+8+(1<<30)))
	copy(head[8:], "WEBP")
	copy(head[12:], "VP8L")
	by.PutUint32(head[16:20], 1<<30) // 1 GB, with no payload behind it

	runtime.GC()
	var before, after runtime.MemStats
	runtime.ReadMemStats(&before)
	if _, err := ParseWebpBytes(head); err == nil {
		t.Fatal("expected an error for a truncated 1 GB chunk")
	}
	runtime.ReadMemStats(&after)
	if grew := after.TotalAlloc - before.TotalAlloc; grew > 32<<20 {
		t.Fatalf("parse allocated %d bytes for a truncated chunk", grew)
	}
}

// TestANMFRejectsDuplicateSubChunks 覆盖 F-08：帧内重复的 ALPH/VP8/VP8L 以及
// VP8 与 VP8L 并存都必须是错误。
//
// TestANMFRejectsDuplicateSubChunks covers F-08: a duplicated ALPH/VP8/VP8L
// sub-chunk, or VP8 and VP8L together, must be an error.
func TestANMFRejectsDuplicateSubChunks(t *testing.T) {
	head := make([]byte, 16)
	writeUint24(head[6:9], 31)
	writeUint24(head[9:12], 15)
	alp := chunkBytes("ALPH", []byte{0x01, 0x02})
	vp8 := chunkBytes("VP8 ", vp8Payload(32, 16))
	vp8lPayload := make([]byte, 5)
	vp8lPayload[0] = 0x2f
	by.PutUint32(vp8lPayload[1:5], uint32(31)|uint32(15)<<14)
	vp8l := chunkBytes("VP8L", vp8lPayload)

	cases := map[string][][]byte{
		"duplicate ALPH": {alp, alp, vp8},
		"duplicate VP8":  {vp8, vp8},
		"duplicate VP8L": {vp8l, vp8l},
		"VP8 and VP8L":   {vp8, vp8l},
		"VP8L and VP8":   {vp8l, vp8},
	}
	for name, subs := range cases {
		frame := append([]byte{}, head...)
		for _, sub := range subs {
			frame = append(frame, sub...)
		}
		stream := buildWebp(
			chunkBytes("VP8X", vp8xPayload(FlagAnimation, 32, 16)),
			chunkBytes("ANIM", make([]byte, 6)),
			chunkBytes("ANMF", frame),
		)
		if _, err := ParseWebpBytes(stream); err == nil {
			t.Errorf("%s: expected an error, got none", name)
		}
	}
}
