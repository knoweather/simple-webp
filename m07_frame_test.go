package simple_webp

import (
	"bytes"
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

// readAnimFixture 读取动画样例，缺失时跳过。
//
// readAnimFixture loads the animation fixture and skips when it is missing.
func readAnimFixture(t *testing.T) []byte {
	t.Helper()
	path := filepath.Join("testdata", "anim.webp")
	if _, err := os.Stat(path); err != nil {
		t.Skip("no anim.webp fixture")
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return data
}

// TestFrameAtMatchesFrames 校验 M-07：FrameAt 的每一帧都与 Frames 整批解码的
// 对应帧逐像素一致，动画信息（画布、循环次数、背景色、帧数）可一次取回。
//
// TestFrameAtMatchesFrames checks M-07: every FrameAt result matches the batch
// Frames output pixel for pixel, and the animation info (canvas, loop count,
// background, frame count) is available in one call.
func TestFrameAtMatchesFrames(t *testing.T) {
	w, err := ParseWebpBytes(readAnimFixture(t))
	if err != nil {
		t.Fatal(err)
	}
	frames, durations, err := w.Frames(0)
	if err != nil {
		t.Fatal(err)
	}
	if len(frames) != len(w.ANMFs) {
		t.Fatalf("Frames returned %d frames, want %d", len(frames), len(w.ANMFs))
	}
	for i := range frames {
		got, dur, err := w.FrameAt(i, FrameOptions{})
		if err != nil {
			t.Fatalf("FrameAt(%d): %v", i, err)
		}
		if dur != durations[i] {
			t.Fatalf("FrameAt(%d) duration %d, want %d", i, dur, durations[i])
		}
		if !bytes.Equal(got.Pix, frames[i].Pix) {
			t.Fatalf("FrameAt(%d) pixels differ from Frames", i)
		}
	}
	if _, _, err := w.FrameAt(len(frames), FrameOptions{}); err == nil {
		t.Fatal("out-of-range frame index was accepted")
	}
	if _, _, err := w.FrameAt(-1, FrameOptions{}); err == nil {
		t.Fatal("negative frame index was accepted")
	}

	info, err := w.AnimationInfo()
	if err != nil {
		t.Fatal(err)
	}
	if info.FrameCount != len(w.ANMFs) || info.Width != w.Width || info.Height != w.Height {
		t.Fatalf("AnimationInfo: %+v", info)
	}
	if w.ANIM != nil {
		if info.LoopCount != w.ANIM.LoopCount {
			t.Fatalf("LoopCount %d, want %d", info.LoopCount, w.ANIM.LoopCount)
		}
		bg := w.ANIM.BackgroundColor
		want := [4]byte{byte(bg >> 16), byte(bg >> 8), byte(bg), byte(bg >> 24)}
		got := [4]byte{info.BackgroundColor.R, info.BackgroundColor.G, info.BackgroundColor.B, info.BackgroundColor.A}
		if got != want {
			t.Fatalf("background %v, want %v", got, want)
		}
	}
}

// TestFrameAtDoesNotCloneEveryFrame 校验 M-07 的内存语义：在多帧动画上取第 0 帧
// 只应分配一块画布，而整批 Frames 要为每一帧克隆一份。
//
// TestFrameAtDoesNotCloneEveryFrame checks the M-07 memory behaviour: taking
// frame 0 allocates a single canvas while the batch Frames clones one per frame.
func TestFrameAtDoesNotCloneEveryFrame(t *testing.T) {
	src := readFixture(t, "lossy_sharp.webp")
	still, err := ParseWebpBytes(src)
	if err != nil {
		t.Fatal(err)
	}
	if still.VP8 == nil {
		t.Skip("fixture has no lossy bitstream")
	}
	const frames = 40
	cw, ch := still.VP8.Width, still.VP8.Height
	vp8 := chunkBytes("VP8 ", still.VP8.Data)

	var chunks [][]byte
	chunks = append(chunks, chunkBytes("VP8X", vp8xPayload(FlagAnimation, cw, ch)))
	chunks = append(chunks, chunkBytes("ANIM", make([]byte, 6)))
	for i := 0; i < frames; i++ {
		head := make([]byte, 16)
		writeUint24(head[6:9], uint32(cw-1))
		writeUint24(head[9:12], uint32(ch-1))
		writeUint24(head[12:15], 40)
		head = append(head, vp8...)
		chunks = append(chunks, chunkBytes("ANMF", head))
	}
	w, err := ParseWebpBytes(buildWebp(chunks...))
	if err != nil {
		t.Fatal(err)
	}
	opt := FrameOptions{MaxPixels: 1 << 24, MaxTotalPixels: 1 << 28}

	measure := func(f func()) uint64 {
		runtime.GC()
		var before, after runtime.MemStats
		runtime.ReadMemStats(&before)
		f()
		runtime.ReadMemStats(&after)
		return after.TotalAlloc - before.TotalAlloc
	}

	one := measure(func() {
		if _, _, err := w.FrameAt(0, opt); err != nil {
			t.Fatalf("FrameAt: %v", err)
		}
	})
	all := measure(func() {
		if _, _, err := w.FramesWith(opt); err != nil {
			t.Fatalf("FramesWith: %v", err)
		}
	})
	if one*4 > all {
		t.Fatalf("FrameAt allocated %d bytes, Frames %d: frame 0 should allocate far less", one, all)
	}
}
