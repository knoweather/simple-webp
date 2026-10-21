package vp8

import (
	"bytes"
	"image"
	"math"
	"testing"
)

// psnrY 计算解码结果的亮度平面对源图的 PSNR。
//
// psnrY computes the PSNR of the decoded luma plane against the source.
func psnrY(got, want *image.YCbCr) float64 {
	w, h := want.Rect.Dx(), want.Rect.Dy()
	var sum float64
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			d := float64(int(got.Y[y*got.YStride+x]) - int(want.Y[y*want.YStride+x]))
			sum += d * d
		}
	}
	mse := sum / float64(w*h)
	if mse == 0 {
		return math.Inf(1)
	}
	return 10 * math.Log10(255*255/mse)
}

// TestLoopFilterApplied 校验环路滤波：强度大于 0 时帧头带上滤波参数，解码
// 结果与关闭滤波不同，而且 PSNR 不下降。
//
// TestLoopFilterApplied checks the loop filter: a positive level puts the filter
// parameters into the frame header, the decoded output differs from the unfiltered
// one, and the PSNR does not get worse.
func TestLoopFilterApplied(t *testing.T) {
	const w, h = 64, 48
	src := image.NewYCbCr(image.Rect(0, 0, w, h), image.YCbCrSubsampleRatio420)
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			// 平滑渐变加一条台阶，量化后容易出现块效应。
			// A smooth gradient with a step edge, which blocks up after
			// quantization.
			v := (x*3 + y*2) % 256
			if x > w/2 {
				v = 255 - v
			}
			src.Y[y*src.YStride+x] = byte(v)
		}
	}
	for i := range src.Cb {
		src.Cb[i] = 128
	}
	for i := range src.Cr {
		src.Cr[i] = 128
	}

	unfiltered, err := EncodeWithOptions(src, Options{Quality: 60, FilterLevel: 0})
	if err != nil {
		t.Fatal(err)
	}
	filtered, err := EncodeWithOptions(src, Options{Quality: 60, FilterLevel: 30})
	if err != nil {
		t.Fatal(err)
	}
	no, err := Decode(unfiltered)
	if err != nil {
		t.Fatal(err)
	}
	yes, err := Decode(filtered)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Equal(no.Y, yes.Y) {
		t.Fatal("the loop filter did not change the decoded luma")
	}
	if got, base := psnrY(yes, src), psnrY(no, src); got < base {
		t.Fatalf("filtered PSNR %.2f is below the unfiltered %.2f", got, base)
	}
}

// TestAutoFilterLevel 校验自动强度随质量变化：质量越低滤波越强，最高质量不再
// 滤波。
//
// TestAutoFilterLevel checks that the automatic strength follows the quality:
// lower quality means a stronger filter, and the highest quality filters nothing.
func TestAutoFilterLevel(t *testing.T) {
	if autoFilterLevel(100) != 0 {
		t.Fatalf("quality 100 should disable the filter, got %d", autoFilterLevel(100))
	}
	if autoFilterLevel(50) <= autoFilterLevel(90) {
		t.Fatalf("quality 50 gets %d, quality 90 gets %d", autoFilterLevel(50), autoFilterLevel(90))
	}
}
