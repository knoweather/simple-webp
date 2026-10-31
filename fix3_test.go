package simple_webp

import (
	"testing"
)
// TestAddRawChunkRejectsDuplicates 校验：单实例 chunk 重复插入被拒绝，
// ANMF 的载荷必须是一帧合法的帧，合法帧与未知类型仍然可以插入。
//
// TestAddRawChunkRejectsDuplicates checks that a duplicate of a single-instance
// chunk is refused, an ANMF payload has to be a valid frame, while a valid
// frame and unknown chunk types still go in.
func TestAddRawChunkRejectsDuplicates(t *testing.T) {
	// 已有 VP8L 的文件：VP8L / VP8 / ALPH 都插不进去（重复或互斥）。
	// A file with a VP8L: VP8L, VP8 and ALPH are all refused (duplicate or
	// mutually exclusive).
	for _, name := range []FourCC{VP8LChunk, VP8Chunk, ALPHChunk} {
		w, err := ParseWebpBytes(readFixture(t, "gradient.webp"))
		if err != nil {
			t.Fatal(err)
		}
		if err := w.AddRawChunk(name, []byte("x")); err == nil {
			t.Fatalf("%s next to a VP8L was accepted", name)
		}
	}

	// 已有 VP8 的文件：VP8 / VP8L 插不进去；单实例扩展 chunk 第一次成功、
	// 第二次被拒。
	// A file with a VP8: VP8 and VP8L are refused; a single-instance extended
	// chunk goes in once and is refused the second time.
	w, err := ParseWebpBytes(readFixture(t, "lossy_smooth.webp"))
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []FourCC{VP8Chunk, VP8LChunk} {
		if err := w.AddRawChunk(name, []byte("x")); err == nil {
			t.Fatalf("%s next to a VP8 was accepted", name)
		}
	}
	for _, name := range []FourCC{VP8XChunk, ANIMChunk, ICCPChunk, EXIFChunk, XMPChunk, ALPHChunk} {
		w, err := ParseWebpBytes(readFixture(t, "lossy_smooth.webp"))
		if err != nil {
			t.Fatal(err)
		}
		// VP8X 10 字节、ANIM 6 字节、ALPH 至少 1 字节，其余载荷任意。
		// A VP8X payload is 10 bytes, an ANIM payload 6 and an ALPH at least 1;
		// the metadata payloads are free form.
		first := []byte("first")
		switch name {
		case VP8XChunk:
			first = make([]byte, 10)
		case ANIMChunk:
			first = make([]byte, 6)
		case ALPHChunk:
			first = []byte{0}
		}
		if err := w.AddRawChunk(name, first); err != nil {
			t.Fatalf("%s: first insert refused: %v", name, err)
		}
		if err := w.AddRawChunk(name, first); err == nil {
			t.Fatalf("%s: duplicate insert accepted", name)
		}
	}

	// ANMF：非法载荷被拒，合法帧被接受。
	// ANMF: an invalid payload is refused, a valid frame is accepted.
	w, err = ParseWebpBytes(readFixture(t, "anim.webp"))
	if err != nil {
		t.Fatal(err)
	}
	if err := w.AddRawChunk(ANMFChunk, []byte("x")); err == nil {
		t.Fatal("an ANMF payload that is not a frame was accepted")
	}
	frame := make([]byte, 16)
	writeUint24(frame[6:9], 15)
	writeUint24(frame[9:12], 15)
	frame = append(frame, chunkBytes("VP8 ", vp8Payload(16, 16))...)
	if err := w.AddRawChunk(ANMFChunk, frame); err != nil {
		t.Fatalf("a valid ANMF payload was refused: %v", err)
	}
	out, err := w.MarshalBytes()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := ParseWebpBytesWith(out, ParseOptions{Strict: true}); err != nil {
		t.Fatalf("the stream with the added frame does not parse strictly: %v", err)
	}

	// 未知类型可以重复插入 / unknown types may repeat
	w2, err := ParseWebpBytes(readFixture(t, "gradient.webp"))
	if err != nil {
		t.Fatal(err)
	}
	if err := w2.AddRawChunk("XYZW", []byte("a")); err != nil {
		t.Fatalf("unknown chunk refused: %v", err)
	}
	if err := w2.AddRawChunk("XYZW", []byte("b")); err != nil {
		t.Fatalf("second unknown chunk refused: %v", err)
	}

	// ALPH 挂在 VP8L 上同样非法，插入阶段就拒绝。
	// An ALPH next to a VP8L is invalid too, refused at insert time.
	w3, err := ParseWebpBytes(readFixture(t, "gradient.webp"))
	if err != nil {
		t.Fatal(err)
	}
	if err := w3.AddRawChunk(ALPHChunk, []byte{0, 1, 2}); err == nil {
		t.Fatal("an ALPH was accepted next to a VP8L")
	}
}

// TestAssignedALPHAndANIMAreWritten 校验：直接赋值的 ALPH/ANIM 必须写出，
// VP8X 的对应标志位同步，输出能通过严格模式；会产生非法流的组合返回错误。
//
// TestAssignedALPHAndANIMAreWritten checks that an assigned ALPH/ANIM has to
// be written with the matching VP8X flags and the output has to pass strict
// mode; combinations that would produce an invalid stream are errors.
func TestAssignedALPHAndANIMAreWritten(t *testing.T) {
	alpha := &ALPH{Compression: ALPHCompressionNone, Filter: ALPHFilterNone, Data: []byte{1, 2, 3, 4}}

	// A：纯 VP8 文件（无 VP8X）赋值 ALPH → 写出 ALPH + 带 alpha 位的 VP8X。
	// A: a plain VP8 file (no VP8X) with an assigned ALPH gets the ALPH and a
	// VP8X whose alpha bit is set.
	w, err := ParseWebpBytes(readFixture(t, "lossy_smooth.webp"))
	if err != nil {
		t.Fatal(err)
	}
	w.ALPH = alpha
	out, err := w.MarshalBytes()
	if err != nil {
		t.Fatalf("A: marshal: %v", err)
	}
	w2, err := ParseWebpBytesWith(out, ParseOptions{Strict: true})
	if err != nil {
		t.Fatalf("A: strict parse: %v", err)
	}
	if w2.ALPH == nil || w2.VP8X == nil || !w2.VP8X.HasAlpha() {
		t.Fatalf("A: ALPH=%v VP8X=%v", w2.ALPH != nil, w2.VP8X != nil)
	}

	// B：已有 VP8X 的文件赋值 ALPH → ALPH 写出且标志位补上。
	// B: a file that already has a VP8X gets the ALPH and the flag bit.
	w, err = ParseWebpBytes(readFixture(t, "lossy_smooth.webp"))
	if err != nil {
		t.Fatal(err)
	}
	w.EXIF = &EXIF{Data: []byte("Exif\x00\x00")}
	w.ALPH = alpha
	out, err = w.MarshalBytes()
	if err != nil {
		t.Fatalf("B: marshal: %v", err)
	}
	w2, err = ParseWebpBytesWith(out, ParseOptions{Strict: true})
	if err != nil {
		t.Fatalf("B: strict parse: %v", err)
	}
	if w2.ALPH == nil || !w2.VP8X.HasAlpha() {
		t.Fatalf("B: ALPH=%v alpha flag=%v", w2.ALPH != nil, w2.VP8X.HasAlpha())
	}

	// C：动图赋值 ANIM（帧已存在）→ 写出且 animation 位正确。
	// C: an animation with an assigned ANIM gets it written with the flag.
	w, err = ParseWebpBytes(readFixture(t, "anim.webp"))
	if err != nil {
		t.Fatal(err)
	}
	anim := &ANIM{LoopCount: 3, BackgroundColor: 0x11223344}
	w.ANIM = anim
	out, err = w.MarshalBytes()
	if err != nil {
		t.Fatalf("C: marshal: %v", err)
	}
	w2, err = ParseWebpBytesWith(out, ParseOptions{Strict: true})
	if err != nil {
		t.Fatalf("C: strict parse: %v", err)
	}
	if w2.ANIM == nil || w2.ANIM.LoopCount != 3 {
		t.Fatalf("C: ANIM=%v", w2.ANIM)
	}

	// D：VP8L 旁加 ALPH 会产出非法流 → 报错。
	// D: an ALPH next to a VP8L would be invalid, so it is an error.
	w, err = ParseWebpBytes(readFixture(t, "gradient.webp"))
	if err != nil {
		t.Fatal(err)
	}
	w.ALPH = alpha
	if _, err := w.MarshalBytes(); err == nil {
		t.Fatal("D: an ALPH next to a VP8L was written")
	}

	// E：没有帧的 ANIM 会产出非法流 → 报错。
	// E: an ANIM without frames would be invalid, so it is an error.
	w, err = ParseWebpBytes(readFixture(t, "lossy_smooth.webp"))
	if err != nil {
		t.Fatal(err)
	}
	w.ANIM = &ANIM{}
	if _, err := w.MarshalBytes(); err == nil {
		t.Fatal("E: an ANIM without frames was written")
	}
}

// TestWriteRejectsOutOfRangeFrameFields 校验：赋值的奇数帧坐标
// 与超出 24 位的时长在写回时报错，边界值正常。
//
// TestWriteRejectsOutOfRangeFrameFields checks that an assigned odd
// frame position or an out-of-range duration is an error on write, while the
// boundary values are fine.
func TestWriteRejectsOutOfRangeFrameFields(t *testing.T) {
	load := func(t *testing.T) *Webp {
		t.Helper()
		w, err := ParseWebpBytes(readFixture(t, "anim.webp"))
		if err != nil {
			t.Fatal(err)
		}
		return w
	}

	// 奇数坐标 / odd position
	for _, pos := range [][2]int{{7, 0}, {0, 7}, {-2, 0}} {
		w := load(t)
		w.ANMFs[0].X, w.ANMFs[0].Y = pos[0], pos[1]
		if _, err := w.MarshalBytes(); err == nil {
			t.Fatalf("position %v was written", pos)
		}
	}
	// 偶数坐标正常往返 / even positions round-trip
	w := load(t)
	w.ANMFs[0].X, w.ANMFs[0].Y = 8, 12
	out, err := w.MarshalBytes()
	if err != nil {
		t.Fatalf("even position: %v", err)
	}
	w2, err := ParseWebpBytes(out)
	if err != nil {
		t.Fatal(err)
	}
	if w2.ANMFs[0].X != 8 || w2.ANMFs[0].Y != 12 {
		t.Fatalf("even position came back as %d,%d", w2.ANMFs[0].X, w2.ANMFs[0].Y)
	}

	// 时长越界 / duration out of range
	for _, dur := range []int{1 << 24, -1} {
		w := load(t)
		w.ANMFs[0].Duration = dur
		if _, err := w.MarshalBytes(); err == nil {
			t.Fatalf("duration %d was written", dur)
		}
	}
	w = load(t)
	w.ANMFs[0].Duration = 1<<24 - 1
	out, err = w.MarshalBytes()
	if err != nil {
		t.Fatalf("boundary duration: %v", err)
	}
	w2, err = ParseWebpBytes(out)
	if err != nil {
		t.Fatal(err)
	}
	if w2.ANMFs[0].Duration != 1<<24-1 {
		t.Fatalf("boundary duration came back as %d", w2.ANMFs[0].Duration)
	}
}

// TestAddRawChunkLeavesStateIntact 确认被拒绝的插入不会改动容器状态。
//
// TestAddRawChunkLeavesStateIntact makes sure a refused insert does not leave
// the container in a broken state.
func TestAddRawChunkLeavesStateIntact(t *testing.T) {
	src := readFixture(t, "gradient.webp")
	w, err := ParseWebpBytes(src)
	if err != nil {
		t.Fatal(err)
	}
	if err := w.AddRawChunk(VP8LChunk, nil); err == nil {
		t.Fatal("expected an error")
	}
	out, err := w.MarshalBytes()
	if err != nil {
		t.Fatalf("the container was left in a broken state: %v", err)
	}
	if string(out) != string(src) {
		t.Fatal("a refused insert changed the stream")
	}
}
