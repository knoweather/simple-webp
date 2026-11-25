package vp8l

import (
	"os"
	"testing"
)

// TestDecodeFile 解码原始 VP8L 比特流（即含文件头的 VP8L chunk 载荷），
// 并把 RGBA 像素写入 VP8L_TEST_OUT 指定的文件，以便与参考解码器逐像素比对。
//
// TestDecodeFile decodes a raw VP8L bitstream (a VP8L chunk payload with its
// header) and writes the RGBA pixels to the file named by VP8L_TEST_OUT so
// they can be compared against a reference decoder.
func TestDecodeFile(t *testing.T) {
	in := os.Getenv("VP8L_TEST_FILE")
	if in == "" {
		t.Skip("VP8L_TEST_FILE is not set")
	}
	data, err := os.ReadFile(in)
	if err != nil {
		t.Fatal(err)
	}
	img, err := Decode(data)
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("%s: %v", in, img.Bounds())
	if out := os.Getenv("VP8L_TEST_OUT"); out != "" {
		if err = os.WriteFile(out, img.Pix, 0o644); err != nil {
			t.Fatal(err)
		}
	}
}

func TestDecodeConfig(t *testing.T) {
	data := make([]byte, 5)
	data[0] = 0x2f
	bits := uint32(7) | uint32(3)<<14 | 1<<28
	data[1], data[2], data[3], data[4] = byte(bits), byte(bits>>8), byte(bits>>16), byte(bits>>24)
	w, h, alpha, err := DecodeConfig(data)
	if err != nil {
		t.Fatal(err)
	}
	if w != 8 || h != 4 || !alpha {
		t.Fatalf("config: %dx%d alpha=%v", w, h, alpha)
	}
	if _, err = Decode(data); err == nil {
		t.Fatal("expected an error for a header-only stream")
	}
	if _, err = Decode([]byte("nope")); err == nil {
		t.Fatal("expected an error for a bad signature")
	}
}

// TestDecodeSolidOnePixel decodes a hand written 1x1 image: one green literal
// with red/blue/alpha literals, coded with simple prefix codes.
func TestDecodeSolidOnePixel(t *testing.T) {
	// Build the bitstream by hand, LSB first.
	var bw testBits
	bw.writeBits(0x2f, 8) // signature
	bw.writeBits(0, 14)   // width - 1
	bw.writeBits(0, 14)   // height - 1
	bw.writeBits(0, 1)    // alpha is not used
	bw.writeBits(0, 3)    // version
	bw.writeBits(0, 1)    // no transform
	bw.writeBits(0, 1)    // no color cache
	bw.writeBits(0, 1)    // no meta prefix image
	// five simple prefix codes with a single symbol each (symbol 0)
	for i := 0; i < 5; i++ {
		bw.writeBits(1, 1) // simple code
		bw.writeBits(0, 1) // one symbol
		bw.writeBits(0, 1) // the symbol needs one bit
		bw.writeBits(0, 1) // symbol value 0
	}
	pix, w, h, err := DecodeARGB(bw.bytes())
	if err != nil {
		t.Fatal(err)
	}
	if w != 1 || h != 1 {
		t.Fatalf("size: %dx%d", w, h)
	}
	if pix[0] != 0xff000000 {
		t.Fatalf("pixel: %08x", pix[0])
	}
}

// testBits is the LSB first writer the test streams are built with.
type testBits struct {
	buf  []byte
	bits uint64
	n    uint
}

func (w *testBits) writeBits(v uint32, n uint) {
	w.bits |= uint64(v) << w.n
	w.n += n
	for w.n >= 8 {
		w.buf = append(w.buf, byte(w.bits))
		w.bits >>= 8
		w.n -= 8
	}
}

func (w *testBits) bytes() []byte {
	if w.n > 0 {
		w.buf = append(w.buf, byte(w.bits))
		w.bits, w.n = 0, 0
	}
	return w.buf
}

// TestColorIndexAlphaIs255 校验颜色索引图按规范把 alpha 写成 255。
func TestColorIndexAlphaIs255(t *testing.T) {
	const w, h = 32, 32
	pix := make([]uint32, w*h)
	for i := range pix {
		pix[i] = 0xff000000 | uint32(i%4)<<8
	}
	_, packed, packedW, ok := colorIndex(pix, w, h)
	if !ok {
		t.Fatal("colorIndex refused a four-colour image")
	}
	if packedW != w/4 { // 四种颜色打包成每字节四个索引 / four indices per byte
		t.Fatalf("packed width = %d", packedW)
	}
	for i, v := range packed {
		if v>>24 != 0xff {
			t.Fatalf("packed[%d] = %08x, alpha = %02x", i, v, v>>24)
		}
	}
}

// TestRejectDuplicateTransform 校验同一种变换出现两次时解码报错，与 libwebp
// 的严格模式一致。
func TestRejectDuplicateTransform(t *testing.T) {
	var bw testBits
	bw.writeBits(0x2f, 8) // signature
	bw.writeBits(0, 14)   // width - 1
	bw.writeBits(0, 14)   // height - 1
	bw.writeBits(0, 1)    // alpha is not used
	bw.writeBits(0, 3)    // version
	bw.writeBits(1, 1)    // a transform follows
	bw.writeBits(transformSubtractGreen, 2)
	bw.writeBits(1, 1) // the same transform again
	bw.writeBits(transformSubtractGreen, 2)
	bw.writeBits(0, 1) // end of transforms
	if _, _, _, err := DecodeARGB(bw.bytes()); err == nil {
		t.Fatal("duplicate transform accepted")
	}
}
