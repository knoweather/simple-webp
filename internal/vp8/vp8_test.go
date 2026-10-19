package vp8

import (
	"bytes"
	"fmt"
	"image"
	"os"
	"testing"

	xvp8 "golang.org/x/image/vp8"
)

// decodeReference 用 golang.org/x/image/vp8 实现解码同一码流，
// 测试中仅把它当作 YCbCr 平面的对照实现（oracle）。
// decodeReference decodes the bitstream with the x/image implementation,
// which is only used by the tests as an oracle for the YCbCr planes.
func decodeReference(data []byte) (*image.YCbCr, error) {
	d := xvp8.NewDecoder()
	d.Init(bytes.NewReader(data), len(data))
	if _, err := d.DecodeFrameHeader(); err != nil {
		return nil, err
	}
	return d.DecodeFrame()
}

// comparePlane 逐样本比较两个平面：统计不同样本数与最大偏差，
// 有差异时报告第一个不匹配的位置。
// comparePlane compares two planes sample by sample: it counts differing
// samples and the maximum delta, and reports the first mismatching
// position when they differ.
func comparePlane(t *testing.T, name string, got []byte, gotStride int, want []byte, wantStride int, width, height int) {
	t.Helper()
	diff, maxDiff := 0, 0
	var first string
	for y := 0; y < height; y++ {
		row := got[y*gotStride : y*gotStride+width]
		ref := want[y*wantStride : y*wantStride+width]
		for x := range row {
			if row[x] != ref[x] {
				d := int(row[x]) - int(ref[x])
				if d < 0 {
					d = -d
				}
				if d > maxDiff {
					maxDiff = d
				}
				diff++
				if first == "" {
					first = fmt.Sprintf("(%d,%d) got %d want %d", x, y, row[x], ref[x])
				}
			}
		}
	}
	if diff != 0 {
		t.Errorf("%s: %d of %d samples differ, max delta %d, first at %s", name, diff, width*height, maxDiff, first)
	}
}

// TestPlanesAgainstReference 把解码帧的每个平面与 x/image vp8 解码器的结果对比。
// 码流路径由环境变量 VP8_TEST_FILE 指定，未设置时跳过该测试。
// TestPlanesAgainstReference compares every plane of the decoded frame with
// the x/image vp8 decoder. The bitstream path comes from the VP8_TEST_FILE
// environment variable; the test is skipped when it is not set.
func TestPlanesAgainstReference(t *testing.T) {
	path := os.Getenv("VP8_TEST_FILE")
	if path == "" {
		t.Skip("VP8_TEST_FILE is not set")
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	got, err := Decode(data)
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	want, err := decodeReference(data)
	if err != nil {
		t.Fatalf("reference decode: %v", err)
	}
	if got.Bounds() != want.Bounds() {
		t.Fatalf("bounds: got %v want %v", got.Bounds(), want.Bounds())
	}
	b := got.Bounds()
	comparePlane(t, "Y", got.Y, got.YStride, want.Y, want.YStride, b.Dx(), b.Dy())
	cw, ch := (b.Dx()+1)/2, (b.Dy()+1)/2
	comparePlane(t, "Cb", got.Cb, got.CStride, want.Cb, want.CStride, cw, ch)
	comparePlane(t, "Cr", got.Cr, got.CStride, want.Cr, want.CStride, cw, ch)
	t.Logf("%s: %dx%d planes match", path, b.Dx(), b.Dy())
}
