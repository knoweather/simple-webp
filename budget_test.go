package simple_webp

import (
	"encoding/binary"
	"errors"
	"testing"

	"github.com/knoweather/simple-webp/internal/vp8"
	"github.com/knoweather/simple-webp/internal/vp8l"
)

// le32 返回按小端编码的 4 字节切片，供内联构造容器时使用。
//
// le32 returns a little-endian uint32 as a 4-byte slice for inline
// container construction.
func le32(v uint32) []byte {
	b := make([]byte, 4)
	binary.LittleEndian.PutUint32(b, v)
	return b
}

// makeWebpVP8L 构造最小合法 WebP 容器：唯一图像 chunk 是声明给定尺寸的 VP8L 头，
// 不含真实像素数据；目的是在解码前走尺寸/字节预算门禁。
//
// makeWebpVP8L builds a minimal valid WebP container whose only image chunk
// is a VP8L header claiming the given dimensions. There is no real pixel
// data: the point is to exercise the dimension/byte-budget gate before
// decoding.
func makeWebpVP8L(width, height int) []byte {
	bits := uint32(width-1) | uint32(height-1)<<14
	vp8lData := []byte{0x2f, byte(bits), byte(bits >> 8), byte(bits >> 16), byte(bits >> 24)}

	chunk := append([]byte("VP8L"), le32(uint32(len(vp8lData)))...)
	chunk = append(chunk, vp8lData...)
	if len(vp8lData)%2 == 1 {
		chunk = append(chunk, 0)
	}
	body := append([]byte("WEBP"), chunk...)
	out := append([]byte("RIFF"), le32(uint32(len(body)))...)
	out = append(out, body...)
	return out
}

// TestDecodeBudgetRejectsHugeVP8LHeader 验证解码器层的保护：5 字节头声明最大
// 可表示的 16383x16383（2.68 亿像素）的码流，必须在分配任何像素缓冲区之前被拒绝。
//
// TestDecodeBudgetRejectsHugeVP8LHeader exercises the decoder-level guard:
// a bitstream whose 5-byte header claims the maximum representable
// 16383x16383 (268M pixels) must be rejected before any pixel buffer is
// allocated.
func TestDecodeBudgetRejectsHugeVP8LHeader(t *testing.T) {
	bits := uint32(16382) | uint32(16382)<<14
	header := []byte{0x2f, byte(bits), byte(bits >> 8), byte(bits >> 16), byte(bits >> 24)}

	if _, _, _, err := vp8l.DecodeARGBLimit(header, 1<<20); !errors.Is(err, vp8l.ErrTooLarge) {
		t.Fatalf("vp8l: expected ErrTooLarge, got %v", err)
	}
}

// TestDecodeBudgetRejectsHugeVP8Header 对有损路径验证同样的保护：声明
// 16383x16383 的 VP8 关键帧头必须被拒绝。
//
// TestDecodeBudgetRejectsHugeVP8Header exercises the same guard for the
// lossy path: a VP8 key frame header claiming 16383x16383 must be rejected.
func TestDecodeBudgetRejectsHugeVP8Header(t *testing.T) {
	b := make([]byte, 10)
	tag := 10 << 5 // firstPartLen=10，关键帧 / firstPartLen=10, key frame
	b[0], b[1], b[2] = byte(tag), byte(tag>>8), byte(tag>>16)
	b[3], b[4], b[5] = 0x9d, 0x01, 0x2a
	b[6], b[7] = 0xff, 0x3f // 宽 16383 / width 16383
	b[8], b[9] = 0xff, 0x3f // 高 16383 / height 16383

	if _, err := vp8.DecodeLimit(b, 1<<20); !errors.Is(err, vp8.ErrTooLarge) {
		t.Fatalf("vp8: expected ErrTooLarge, got %v", err)
	}
}

// TestToImageWithBudget 验证顶层门禁：解析为 200x200（40000 像素）的文件，
// 调用方设置 1000 像素预算时必须被拒绝，预算足够时必须放行。
//
// TestToImageWithBudget exercises the top-level gate: a file that parses to
// a modest 200x200 (40000 pixels) must be rejected when the caller sets a
// 1000-pixel budget, and must clear the gate when the budget is large enough.
func TestToImageWithBudget(t *testing.T) {
	data := makeWebpVP8L(200, 200)
	w, err := ParseWebpBytes(data)
	if err != nil {
		t.Fatal(err)
	}
	if w.Width != 200 || w.Height != 200 {
		t.Fatalf("parsed size %dx%d", w.Width, w.Height)
	}

	if _, err := w.ToImageWith(DecodeOptions{MaxPixels: 1000}); !errors.Is(err, ErrImageTooLarge) {
		t.Fatalf("expected ErrImageTooLarge, got %v", err)
	}
	// 预算足够时门禁必须放行（随后的解码会失败，因为没有真实像素流，
	// 但已不再是预算错误）。
	// With a generous budget the gate must pass (the decode then fails
	// because there is no real pixel stream, but no longer with a budget error).
	if _, err := w.ToImageWith(DecodeOptions{MaxPixels: 1 << 20}); errors.Is(err, ErrImageTooLarge) {
		t.Fatalf("budget gate should pass for 40000 <= 1<<20, got %v", err)
	}
}

// TestToImageWithDefaultBudget 验证对恶意头部施加默认预算时不会 panic：
// 解码器自我设限，模糊测试语料无法再触发无界内存分配。
//
// TestToImageWithDefaultBudget confirms no panic on a hostile header under
// the default budget: the decoder self-limits so the fuzz corpus can no
// longer trigger unbounded allocations.
func TestToImageWithDefaultBudget(t *testing.T) {
	data := makeWebpVP8L(16383, 16383)
	w, err := ParseWebpBytes(data)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := w.ToImageWith(DecodeOptions{}); !errors.Is(err, ErrImageTooLarge) {
		t.Fatalf("expected ErrImageTooLarge with default budget, got %v", err)
	}
}
