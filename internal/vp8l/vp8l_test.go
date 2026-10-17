package vp8l

import (
	"os"
	"testing"
)

// TestDecodeFile decodes a raw VP8L bitstream (a VP8L chunk payload, header
// included) and writes the RGBA pixels to the file named by VP8L_TEST_OUT so
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
