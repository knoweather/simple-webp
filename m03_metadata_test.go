package simple_webp

import (
	"bytes"
	"image"
	"image/color"
	"os"
	"testing"
)

// m03Gradient 构造 w×h 的 NRGBA 渐变，作为元数据测试的合成输入。
//
// m03Gradient builds a w×h NRGBA gradient used as the synthetic input of the
// metadata tests.
func m03Gradient(w, h int) *image.NRGBA {
	img := image.NewNRGBA(image.Rect(0, 0, w, h))
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			img.SetNRGBA(x, y, color.NRGBA{
				R: uint8(x * 255 / (w - 1)),
				G: uint8(y * 255 / (h - 1)),
				B: uint8((x + y) & 0xff),
				A: 255,
			})
		}
	}
	return img
}

// m03ChunkNames 按 chunk 头边界走读容器，返回各 chunk 类型码的出现顺序。
//
// m03ChunkNames walks the container chunk by chunk and returns the type codes
// in the order they appear in the stream.
func m03ChunkNames(data []byte) []FourCC {
	var names []FourCC
	for i := 12; i+8 <= len(data); {
		name := FourCC(data[i : i+4])
		size := int(by.Uint32(data[i+4 : i+8]))
		names = append(names, name)
		i += 8 + size + size&1
	}
	return names
}

func m03SameOrder(got, want []FourCC) bool {
	if len(got) != len(want) {
		return false
	}
	for i := range got {
		if got[i] != want[i] {
			return false
		}
	}
	return true
}

// TestEncodeMetadataAbsentKeepsSimpleContainer 不带元数据编码：输出必须可复现，
// 且仍是 RIFF + WEBP + 单个 VP8L 的简单容器，不会凭空多出 VP8X。
//
// TestEncodeMetadataAbsentKeepsSimpleContainer encodes without metadata: the
// output must be reproducible and stay the simple container of RIFF, WEBP and a
// single VP8L chunk, with no VP8X invented.
func TestEncodeMetadataAbsentKeepsSimpleContainer(t *testing.T) {
	src := m03Gradient(32, 24)
	first, err := EncodeBytes(src, nil)
	if err != nil {
		t.Fatal(err)
	}
	second, err := EncodeBytes(src, nil)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(first, second) {
		t.Fatal("output is not reproducible")
	}
	w, err := ParseWebpBytesWith(first, ParseOptions{Strict: true})
	if err != nil {
		t.Fatalf("strict parse: %v", err)
	}
	if w.VP8X != nil {
		t.Fatalf("no metadata must not write VP8X: %+v", w.VP8X)
	}
	if w.ICCP != nil || w.EXIF != nil || w.XMP != nil {
		t.Fatal("unexpected metadata chunk")
	}
	if w.VP8L == nil {
		t.Fatal("no VP8L chunk")
	}
	// 无元数据时输出必须与改动前逐字节一致 / without metadata the output must be
	// byte for byte the simple container as it was before the change
	want := buildWebp(chunkBytes("VP8L", w.VP8L.Data))
	if !bytes.Equal(first, want) {
		t.Fatalf("simple container mismatch: got %d bytes, want %d", len(first), len(want))
	}
}

// TestEncodeMetadataEXIF 带 EXIF 编码：解析后必须有带 FlagEXIF 的 VP8X，
// EXIF 载荷与输入一致，且顺序是 VP8X、VP8L、EXIF。
//
// TestEncodeMetadataEXIF encodes with EXIF: the result must carry a VP8X with
// FlagEXIF, the EXIF payload must match the input, and the order must be VP8X,
// VP8L, EXIF.
func TestEncodeMetadataEXIF(t *testing.T) {
	src := m03Gradient(32, 24)
	exif := []byte("Exif\x00\x00MM\x00\x2a")
	data, err := EncodeBytes(src, &EncodeOptions{EXIF: exif})
	if err != nil {
		t.Fatal(err)
	}
	w, err := ParseWebpBytesWith(data, ParseOptions{Strict: true})
	if err != nil {
		t.Fatalf("strict parse: %v", err)
	}
	if w.VP8X == nil {
		t.Fatal("VP8X missing")
	}
	if !w.VP8X.HasEXIF() {
		t.Fatalf("FlagEXIF not set (flags=0x%02x)", w.VP8X.Flags)
	}
	if w.EXIF == nil || !bytes.Equal(w.EXIF.Data, exif) {
		t.Fatalf("exif data: %+v", w.EXIF)
	}
	if w.Width != 32 || w.Height != 24 {
		t.Fatalf("canvas: got %dx%d, want 32x24", w.Width, w.Height)
	}
	want := []FourCC{VP8XChunk, VP8LChunk, EXIFChunk}
	if names := m03ChunkNames(data); !m03SameOrder(names, want) {
		t.Fatalf("chunk order: got %v, want %v", names, want)
	}
}

// TestEncodeLossyMetadataICCPXMP 带 ICCP 与 XMP 的有损编码：解析后标志位与
// 载荷都要对得上，顺序是 VP8X、ICCP、VP8 、XMP。
//
// TestEncodeLossyMetadataICCPXMP encodes lossy with ICCP and XMP: the flags and
// the payloads must match, and the order must be VP8X, ICCP, VP8 , XMP.
func TestEncodeLossyMetadataICCPXMP(t *testing.T) {
	src := m03Gradient(32, 24)
	iccp := []byte(" ICC profile bytes") // 奇数长度，写出填充字节 / odd length, so a pad byte is written
	xmp := []byte("<x:xmpmeta/>")
	data, err := EncodeLossyBytes(src, &LossyOptions{Quality: 80, ICCP: iccp, XMP: xmp})
	if err != nil {
		t.Fatal(err)
	}
	w, err := ParseWebpBytesWith(data, ParseOptions{Strict: true})
	if err != nil {
		t.Fatalf("strict parse: %v", err)
	}
	if w.VP8X == nil {
		t.Fatal("VP8X missing")
	}
	if !w.VP8X.HasICC() || !w.VP8X.HasXMP() {
		t.Fatalf("flags: iccp=%v xmp=%v (flags=0x%02x)", w.VP8X.HasICC(), w.VP8X.HasXMP(), w.VP8X.Flags)
	}
	if w.ICCP == nil || !bytes.Equal(w.ICCP.Data, iccp) {
		t.Fatalf("iccp data: %+v", w.ICCP)
	}
	if w.XMP == nil || !bytes.Equal(w.XMP.Data, xmp) {
		t.Fatalf("xmp data: %+v", w.XMP)
	}
	want := []FourCC{VP8XChunk, ICCPChunk, VP8Chunk, XMPChunk}
	if names := m03ChunkNames(data); !m03SameOrder(names, want) {
		t.Fatalf("chunk order: got %v, want %v", names, want)
	}

	// 同一张图不带元数据时仍是简单容器，不写 VP8X
	// the same image without metadata stays the simple container, no VP8X
	plain, err := EncodeLossyBytes(src, &LossyOptions{Quality: 80})
	if err != nil {
		t.Fatal(err)
	}
	w2, err := ParseWebpBytes(plain)
	if err != nil {
		t.Fatal(err)
	}
	if w2.VP8X != nil {
		t.Fatalf("no metadata must not write VP8X: %+v", w2.VP8X)
	}
	if w2.VP8 == nil {
		t.Fatal("no VP8 chunk")
	}
	if want := buildWebp(chunkBytes("VP8 ", w2.VP8.Data)); !bytes.Equal(plain, want) {
		t.Fatalf("simple container mismatch: got %d bytes, want %d", len(plain), len(want))
	}
}

// TestEncodeMetadataOnSample 用真实样例编码并带上 EXIF 与 XMP：严格解析必须
// 通过，元数据与像素都要原样保留。样例缺失时跳过。
//
// TestEncodeMetadataOnSample encodes a real fixture with EXIF and XMP: strict
// parsing must pass and both the metadata and the pixels must survive. It is
// skipped when the fixture is missing.
func TestEncodeMetadataOnSample(t *testing.T) {
	raw, err := os.ReadFile("testdata/alpha.webp")
	if err != nil {
		t.Skip("testdata/alpha.webp is missing")
	}
	w, err := ParseWebpBytes(raw)
	if err != nil {
		t.Fatal(err)
	}
	src, err := w.ToImage()
	if err != nil {
		t.Fatal(err)
	}
	nrgba, ok := src.(*image.NRGBA)
	if !ok {
		t.Fatalf("decoded image is %T", src)
	}
	exif := []byte("Exif\x00\x00MM\x00\x2a")
	xmp := []byte("<x:xmpmeta/>")
	data, err := EncodeBytes(src, &EncodeOptions{EXIF: exif, XMP: xmp})
	if err != nil {
		t.Fatal(err)
	}
	got, err := ParseWebpBytesWith(data, ParseOptions{Strict: true})
	if err != nil {
		t.Fatalf("strict parse: %v", err)
	}
	if got.EXIF == nil || !bytes.Equal(got.EXIF.Data, exif) {
		t.Fatalf("exif data: %+v", got.EXIF)
	}
	if got.XMP == nil || !bytes.Equal(got.XMP.Data, xmp) {
		t.Fatalf("xmp data: %+v", got.XMP)
	}
	if got.Width != w.Width || got.Height != w.Height {
		t.Fatalf("canvas: got %dx%d, want %dx%d", got.Width, got.Height, w.Width, w.Height)
	}
	img, err := got.ToImage()
	if err != nil {
		t.Fatal(err)
	}
	compareNRGBA(t, "sample with metadata", img, nrgba.Pix)
	want := []FourCC{VP8XChunk, VP8LChunk, EXIFChunk, XMPChunk}
	if names := m03ChunkNames(data); !m03SameOrder(names, want) {
		t.Fatalf("chunk order: got %v, want %v", names, want)
	}
}
