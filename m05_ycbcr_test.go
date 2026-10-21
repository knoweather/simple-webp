package simple_webp

import (
	"bytes"
	"errors"
	"image"
	"os"
	"testing"

	"github.com/knoweather/simple-webp/internal/vp8"
)

// m05Fixture 读取 testdata 下的样例，缺失时跳过测试。
//
// m05Fixture loads a fixture from testdata and skips the test when it is
// missing.
func m05Fixture(t *testing.T, name string) []byte {
	t.Helper()
	data, err := os.ReadFile("testdata/" + name)
	if err != nil {
		t.Skipf("no fixture %s", name)
	}
	return data
}

// TestYCbCrPlanesMatchVP8Decoder 检查 PixelFormatYCbCr 返回的就是解码器得到的
// 4:2:0 平面：尺寸取自容器，三个平面与直接调用 vp8.Decode 逐字节相同，说明
// 这条路径没有做 YUV 到 RGB 的转换。
//
// TestYCbCrPlanesMatchVP8Decoder checks that PixelFormatYCbCr returns the
// 4:2:0 planes as the decoder produced them: the size comes from the container
// and the three planes are byte identical to a direct vp8.Decode call, which
// shows that no YUV to RGB conversion happened on this path.
func TestYCbCrPlanesMatchVP8Decoder(t *testing.T) {
	w, err := ParseWebpBytes(m05Fixture(t, "lossy_smooth.webp"))
	if err != nil {
		t.Fatal(err)
	}
	if w.VP8 == nil {
		t.Skip("fixture is not a lossy bitstream")
	}

	img, err := w.ToImageWith(DecodeOptions{Format: PixelFormatYCbCr})
	if err != nil {
		t.Fatal(err)
	}
	got, ok := img.(*image.YCbCr)
	if !ok {
		t.Fatalf("got %T, want *image.YCbCr", img)
	}
	if got.Bounds().Dx() != w.Width || got.Bounds().Dy() != w.Height {
		t.Fatalf("bounds %v, want %dx%d", got.Bounds(), w.Width, w.Height)
	}
	if got.SubsampleRatio != image.YCbCrSubsampleRatio420 {
		t.Fatalf("subsample ratio: %v", got.SubsampleRatio)
	}

	ref, err := vp8.Decode(w.VP8.Data)
	if err != nil {
		t.Fatal(err)
	}
	if ref.Bounds() != got.Bounds() || ref.SubsampleRatio != got.SubsampleRatio {
		t.Fatalf("reference %v/%v, got %v/%v", ref.Bounds(), ref.SubsampleRatio, got.Bounds(), got.SubsampleRatio)
	}
	for _, plane := range []struct {
		name string
		got  []byte
		want []byte
	}{
		{"Y", got.Y, ref.Y},
		{"Cb", got.Cb, ref.Cb},
		{"Cr", got.Cr, ref.Cr},
	} {
		if !bytes.Equal(plane.got, plane.want) {
			t.Fatalf("%s plane differs: %d bytes, want %d bytes", plane.name, len(plane.got), len(plane.want))
		}
	}
	t.Logf("%dx%d YCbCr planes match vp8.Decode", got.Bounds().Dx(), got.Bounds().Dy())
}

// TestYCbCrUnsupported 检查没有 YCbCr 表示的输入都返回 ErrUnsupported：纯无损
// 码流、带 alpha 平面的有损图，以及动画。
//
// TestYCbCrUnsupported checks that every input without a YCbCr representation
// returns ErrUnsupported: a plain lossless bitstream, a lossy image with an
// alpha plane, and an animation.
func TestYCbCrUnsupported(t *testing.T) {
	for _, tc := range []struct {
		file string
		why  string
	}{
		{"gradient.webp", "lossless bitstream"},
		{"lossy_alpha.webp", "alpha plane"},
		{"anim.webp", "animation"},
	} {
		w, err := ParseWebpBytes(m05Fixture(t, tc.file))
		if err != nil {
			t.Fatal(err)
		}
		_, err = w.ToImageWith(DecodeOptions{Format: PixelFormatYCbCr})
		if !errors.Is(err, ErrUnsupported) {
			t.Fatalf("%s (%s): got %v, want ErrUnsupported", tc.file, tc.why, err)
		}
		t.Logf("%s (%s): %v", tc.file, tc.why, err)
	}
}

// TestYCbCrDefaultFormatUnchanged 用同一张有损样例确认默认布局仍是 NRGBA：
// 新增的 YCbCr 取值不能改变不带 Format 时的旧行为。
//
// TestYCbCrDefaultFormatUnchanged uses the same lossy fixture to confirm that
// the default layout is still NRGBA: the new YCbCr value must not change the
// behaviour when Format is left at its zero value.
func TestYCbCrDefaultFormatUnchanged(t *testing.T) {
	w, err := ParseWebpBytes(m05Fixture(t, "lossy_smooth.webp"))
	if err != nil {
		t.Fatal(err)
	}
	img, err := w.ToImageWith(DecodeOptions{})
	if err != nil {
		t.Fatal(err)
	}
	nrgba, ok := img.(*image.NRGBA)
	if !ok {
		t.Fatalf("got %T, want *image.NRGBA", img)
	}
	if nrgba.Bounds().Dx() != w.Width || nrgba.Bounds().Dy() != w.Height {
		t.Fatalf("bounds %v, want %dx%d", nrgba.Bounds(), w.Width, w.Height)
	}
}
