package simple_webp

import (
	"bytes"
	"image"
	"image/color"
	"os"
	"path/filepath"
	"testing"
)

// encodePureVP8L 编码一张不透明的无损 WebP（不含 VP8X），用于复现「纯 VP8L
// 文件根本没有 VP8X」这一真实场景。
//
// encodePureVP8L encodes an opaque lossless WebP with no VP8X chunk, reproducing
// the real-world case where a plain VP8L file has no extended header at all.
func encodePureVP8L(t *testing.T) []byte {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, 16, 16))
	for y := 0; y < 16; y++ {
		for x := 0; x < 16; x++ {
			img.Set(x, y, color.RGBA{uint8(x * 16), uint8(y * 16), uint8(x + y), 255})
		}
	}
	data, err := EncodeBytes(img, nil)
	if err != nil {
		t.Fatalf("encode: %v", err)
	}
	w, err := ParseWebpBytes(data)
	if err != nil {
		t.Fatalf("parse encoded: %v", err)
	}
	if w.VP8X != nil {
		t.Fatalf("encoded lossless unexpectedly has VP8X")
	}
	return data
}

// TestWriteXMPOnPureVP8L 覆盖「纯 VP8L 文件直接赋值 w.XMP」：原文件既没有 XMP
// 也没有 VP8X，写回时必须自动创建带 FlagXMP 的 VP8X，并逐像素无损还原。
//
// TestWriteXMPOnPureVP8L covers "assigning w.XMP on a plain VP8L file": the
// source has neither an XMP chunk nor a VP8X, so the write must auto-create a
// VP8X with FlagXMP set and round-trip the pixels losslessly.
func TestWriteXMPOnPureVP8L(t *testing.T) {
	src := encodePureVP8L(t)
	base, err := ParseWebpBytes(src)
	if err != nil {
		t.Fatal(err)
	}
	golden, err := base.ToImage()
	if err != nil {
		t.Fatal(err)
	}
	baseXMP := []byte("<xmp>hello-world</xmp>")

	base.XMP = &XMP{Data: baseXMP}
	out, err := base.MarshalBytes()
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}

	w2, err := ParseWebpBytes(out)
	if err != nil {
		t.Fatalf("re-parse: %v", err)
	}
	if w2.VP8X == nil {
		t.Fatal("expected a synthesized VP8X chunk, got none")
	}
	if !w2.VP8X.HasXMP() {
		t.Fatalf("FlagXMP not set on synthesized VP8X (flags=0x%02x)", w2.VP8X.Flags)
	}
	if w2.XMP == nil {
		t.Fatal("XMP not round-tripped")
	}
	if !bytes.Equal(w2.XMP.Data, baseXMP) {
		t.Fatalf("XMP data mismatch: got %q want %q", w2.XMP.Data, baseXMP)
	}
	// 像素级无损还原。/ pixel-level lossless restore.
	img2, err := w2.ToImage()
	if err != nil {
		t.Fatal(err)
	}
	compareNRGBA(t, "xmp-on-vp8l", img2, golden.(*image.NRGBA).Pix)
}

// TestAddRawChunkXMPOnPureVP8L 覆盖另一条路：通过 AddRawChunk 插入 XMP（而非
// 赋值结构体）。纯 VP8L 文件原本没有 VP8X，写回时同样必须自动补上带 FlagXMP 的
// VP8X——否则产出的流不合规（XMP 悬在 VP8X 之外）。
//
// TestAddRawChunkXMPOnPureVP8L covers the other route: inserting XMP via
// AddRawChunk instead of assigning the struct. A plain VP8L file has no VP8X, so
// the write must still auto-create a VP8X with FlagXMP set — otherwise the
// output is invalid (XMP dangling outside any VP8X).
func TestAddRawChunkXMPOnPureVP8L(t *testing.T) {
	src := encodePureVP8L(t)
	base, err := ParseWebpBytes(src)
	if err != nil {
		t.Fatal(err)
	}
	golden, err := base.ToImage()
	if err != nil {
		t.Fatal(err)
	}
	xmp := []byte("<xmp>via-add-raw-chunk</xmp>")
	if err := base.AddRawChunk(XMPChunk, xmp); err != nil {
		t.Fatalf("add raw chunk: %v", err)
	}
	out, err := base.MarshalBytes()
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	w2, err := ParseWebpBytes(out)
	if err != nil {
		t.Fatalf("re-parse: %v", err)
	}
	if w2.VP8X == nil {
		t.Fatal("expected an auto-created VP8X chunk, got none")
	}
	if !w2.VP8X.HasXMP() {
		t.Fatalf("FlagXMP not set (flags=0x%02x)", w2.VP8X.Flags)
	}
	if w2.XMP == nil || !bytes.Equal(w2.XMP.Data, xmp) {
		t.Fatalf("XMP not round-tripped: %+v", w2.XMP)
	}
	img2, err := w2.ToImage()
	if err != nil {
		t.Fatal(err)
	}
	compareNRGBA(t, "addraw-xmp-on-vp8l", img2, golden.(*image.NRGBA).Pix)
}

// TestWriteXMPOnVP8XFile 在已有 VP8X 的文件上直接赋 w.XMP：写回应置上 FlagXMP
// 且像素不变，而不是静默丢弃。用 anim.webp（VP8X+ANIM+ANMF）作为带 VP8X 的样例。
//
// TestWriteXMPOnVP8XFile assigns w.XMP on a file that already has a VP8X chunk:
// the write must set FlagXMP and leave the pixels unchanged instead of dropping
// the metadata. anim.webp (VP8X+ANIM+ANMF) is the VP8X-bearing sample.
func TestWriteXMPOnVP8XFile(t *testing.T) {
	const name = "anim.webp"
	if _, err := os.Stat(filepath.Join("testdata", name)); err != nil {
		t.Skip("no fixture")
	}
	src, err := os.ReadFile(filepath.Join("testdata", name))
	if err != nil {
		t.Fatal(err)
	}
	base, err := ParseWebpBytes(src)
	if err != nil {
		t.Fatal(err)
	}
	if base.VP8X == nil {
		t.Skip("fixture has no VP8X")
	}
	if !base.HasAnimation {
		t.Skip("fixture is not animated")
	}
	golden, err := base.ToImage()
	if err != nil {
		t.Fatal(err)
	}
	xmp := []byte("<xmp>on-vp8x</xmp>")

	base.XMP = &XMP{Data: xmp}
	out, err := base.MarshalBytes()
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	w2, err := ParseWebpBytes(out)
	if err != nil {
		t.Fatalf("re-parse: %v", err)
	}
	if !w2.VP8X.HasXMP() {
		t.Fatalf("FlagXMP not set (flags=0x%02x)", w2.VP8X.Flags)
	}
	if !bytes.Equal(w2.XMP.Data, xmp) {
		t.Fatalf("XMP data mismatch")
	}
	// 加 XMP 后像素不变。/ pixels unchanged after adding XMP.
	img2, err := w2.ToImage()
	if err != nil {
		t.Fatal(err)
	}
	compareNRGBA(t, "xmp-on-vp8x", img2, golden.(*image.NRGBA).Pix)
}

// TestDeleteXMPClearsFlag 删除 XMP 后，VP8X 的 FlagXMP 应被清除（若已无任何
// 扩展特性，不强制删 VP8X，但标志位必须一致）。
//
// TestDeleteXMPClearsFlag checks that deleting XMP also clears FlagXMP so the
// flags stay consistent with the data.
func TestDeleteXMPClearsFlag(t *testing.T) {
	const name = "alpha.webp"
	if _, err := os.Stat(filepath.Join("testdata", name)); err != nil {
		t.Skip("no fixture")
	}
	src, err := os.ReadFile(filepath.Join("testdata", name))
	if err != nil {
		t.Fatal(err)
	}
	base, err := ParseWebpBytes(src)
	if err != nil {
		t.Fatal(err)
	}
	base.XMP = &XMP{Data: []byte("<xmp>temp</xmp>")}
	out, err := base.MarshalBytes()
	if err != nil {
		t.Fatal(err)
	}
	w2, err := ParseWebpBytes(out)
	if err != nil {
		t.Fatal(err)
	}
	if !w2.VP8X.HasXMP() {
		t.Fatal("FlagXMP should be set before delete")
	}
	removed := w2.DeleteRawChunk(XMPChunk)
	if removed != 1 {
		t.Fatalf("DeleteRawChunk removed %d, want 1", removed)
	}
	out2, err := w2.MarshalBytes()
	if err != nil {
		t.Fatalf("marshal after delete: %v", err)
	}
	w3, err := ParseWebpBytes(out2)
	if err != nil {
		t.Fatalf("re-parse after delete: %v", err)
	}
	if w3.XMP != nil {
		t.Fatal("XMP still present after delete")
	}
	if w3.VP8X != nil && w3.VP8X.HasXMP() {
		t.Fatalf("FlagXMP not cleared (flags=0x%02x)", w3.VP8X.Flags)
	}
}

// TestWriteNoXMPNoError 不做任何修改地解析写回不应报错，且像素保持一致——覆盖
// 「无 XMP 不报错」这一基线。
//
// TestWriteNoXMPNoError checks that parsing and writing back unchanged does not
// error and keeps the pixels identical — the "no XMP, no error" baseline.
func TestWriteNoXMPNoError(t *testing.T) {
	src := encodePureVP8L(t)
	base, err := ParseWebpBytes(src)
	if err != nil {
		t.Fatal(err)
	}
	golden, err := base.ToImage()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := base.MarshalBytes(); err != nil {
		t.Fatalf("marshal unchanged: %v", err)
	}
	// 重新编码对照：两次编码结果逐像素一致。
	// Re-encode for comparison: both encodings match pixel for pixel.
	again, err := ParseWebpBytes(src)
	if err != nil {
		t.Fatal(err)
	}
	img2, err := again.ToImage()
	if err != nil {
		t.Fatal(err)
	}
	compareNRGBA(t, "no-xmp", img2, golden.(*image.NRGBA).Pix)
}
