package simple_webp

import (
	"bytes"
	"image"
	"math/rand"
	"testing"

	xwebp "golang.org/x/image/webp"
)

// alphaPredictor 返回 (x,y) 处按 method 得到的预测值，邻居取自 src。
// 规则照抄 WebP 容器规范「Alpha」一节：方法 1 用左邻 A、方法 2 用上邻 B、
// 方法 3 用 clip(A+B-C)；(0,0) 的预测值为 0；水平或梯度滤波时第 0 列改用
// 正上方的像素、垂直或梯度滤波时第 0 行改用左侧的像素。
//
// alphaPredictor returns the predictor of the pixel at (x,y) for the given
// filtering method, with the neighbours taken from src. The rules follow the
// "Alpha" section of the WebP container specification: method 1 uses the left
// neighbour A, method 2 the neighbour above B and method 3 clip(A+B-C); the
// predictor of (0,0) is 0; with the horizontal or gradient filter column 0
// uses the pixel just above, and with the vertical or gradient filter row 0
// uses the pixel on the left.
func alphaPredictor(src []byte, width, height int, method uint8, x, y int) int {
	at := func(px, py int) int {
		if px < 0 || py < 0 || px >= width || py >= height {
			return 0
		}
		return int(src[py*width+px])
	}
	if x == 0 && y == 0 {
		return 0
	}
	switch method {
	case ALPHFilterHorizontal:
		if x == 0 {
			return at(0, y-1)
		}
		return at(x-1, y)
	case ALPHFilterVertical:
		if y == 0 {
			return at(x-1, 0)
		}
		return at(x, y-1)
	case ALPHFilterGradient:
		if x == 0 {
			return at(0, y-1)
		}
		if y == 0 {
			return at(x-1, 0)
		}
		p := at(x-1, y) + at(x, y-1) - at(x-1, y-1)
		if p < 0 {
			p = 0
		} else if p > 255 {
			p = 255
		}
		return p
	}
	return 0
}

// alphaFilterReference 按规范做正向差分：每个像素减去它的预测值，预测邻居
// 取自原值。
//
// alphaFilterReference applies the forward differencing of the specification:
// every pixel minus its predictor, with the predictor neighbours taken from the
// original values.
func alphaFilterReference(plane []byte, width, height int, method uint8) []byte {
	out := make([]byte, len(plane))
	for y := 0; y < height; y++ {
		for x := 0; x < width; x++ {
			pred := alphaPredictor(plane, width, height, method, x, y)
			out[y*width+x] = plane[y*width+x] - byte(pred)
		}
	}
	return out
}

// alphaUnfilterReference 按规范做逆变换：每个像素加上它的预测值，预测邻居
// 取自已重建的值。
//
// alphaUnfilterReference applies the inverse transform of the specification:
// every pixel plus its predictor, with the predictor neighbours taken from the
// values reconstructed so far.
func alphaUnfilterReference(plane []byte, width, height int, method uint8) []byte {
	out := make([]byte, len(plane))
	copy(out, plane)
	for y := 0; y < height; y++ {
		for x := 0; x < width; x++ {
			pred := alphaPredictor(out, width, height, method, x, y)
			out[y*width+x] = plane[y*width+x] + byte(pred)
		}
	}
	return out
}

// TestAlphaFiltersFollowTheSpec 校验四种行滤波的正向差分与逆变换都符合容器
// 规范，尤其是水平/垂直滤波的首列/首行预测规则。
//
// TestAlphaFiltersFollowTheSpec checks that the forward differencing and the
// inverse transform of all four row filters follow the container
// specification, in particular the first column and first row predictor rules
// of the horizontal and vertical filters.
func TestAlphaFiltersFollowTheSpec(t *testing.T) {
	const width, height = 17, 11
	rng := rand.New(rand.NewSource(20240607))
	orig := make([]byte, width*height)
	for i := range orig {
		orig[i] = byte(rng.Intn(256))
	}

	methods := []uint8{ALPHFilterNone, ALPHFilterHorizontal, ALPHFilterVertical, ALPHFilterGradient}
	names := []string{"none", "horizontal", "vertical", "gradient"}
	for i, method := range methods {
		// 正向差分
		got := make([]byte, len(orig))
		copy(got, orig)
		filterAlpha(got, width, height, method)
		want := alphaFilterReference(orig, width, height, method)
		for p := range want {
			if got[p] != want[p] {
				t.Fatalf("%s filter: forward differencing differs at (%d,%d): got %d want %d",
					names[i], p%width, p/width, got[p], want[p])
			}
		}

		// 逆变换
		got = make([]byte, len(orig))
		copy(got, want)
		unfilterAlpha(got, width, height, method)
		wantBack := alphaUnfilterReference(want, width, height, method)
		for p := range wantBack {
			if got[p] != wantBack[p] {
				t.Fatalf("%s filter: inverse transform differs at (%d,%d): got %d want %d",
					names[i], p%width, p/width, got[p], wantBack[p])
			}
		}

		// 滤波再逆变换必须还原原值
		for p := range orig {
			if got[p] != orig[p] {
				t.Fatalf("%s filter: round trip differs at (%d,%d): got %d want %d",
					names[i], p%width, p/width, got[p], orig[p])
			}
		}
	}
}

// newAlphaNoiseImage 构造一张 alpha 通道取值杂乱的图，便于暴露行滤波的边界错误。
//
// newAlphaNoiseImage builds an image with a noisy alpha channel, which makes
// the boundary errors of the row filters visible.
func newAlphaNoiseImage(width, height int) *image.NRGBA {
	img := image.NewNRGBA(image.Rect(0, 0, width, height))
	rng := rand.New(rand.NewSource(31337))
	for y := 0; y < height; y++ {
		for x := 0; x < width; x++ {
			o := img.PixOffset(x, y)
			img.Pix[o] = uint8(x * 255 / width)
			img.Pix[o+1] = uint8(y * 255 / height)
			img.Pix[o+2] = 90
			img.Pix[o+3] = byte(rng.Intn(256))
		}
	}
	return img
}

// TestLossyAlphaMatchesReferenceDecoder 校验本库写出的 ALPH 平面能被参考解码器
// （golang.org/x/image，libwebp 的移植版）解出同样的 alpha 值。
//
// 这一条用独立实现当 oracle：自己编自己解的往返测试看不出滤波规则写错，
// 因为编解码两端会一致地错。
//
// TestLossyAlphaMatchesReferenceDecoder checks that the ALPH plane this
// library writes is decoded to the same alpha values by the reference decoder
// (golang.org/x/image, a port of libwebp).
//
// An independent implementation is the oracle here: a round trip through this
// library's own encoder and decoder cannot catch a wrong filtering rule,
// because both ends would be wrong in the same way.
func TestLossyAlphaMatchesReferenceDecoder(t *testing.T) {
	src := newAlphaNoiseImage(64, 48)
	names := []string{"none", "horizontal", "vertical", "gradient"}
	for filter := uint8(0); filter <= ALPHFilterGradient; filter++ {
		data, err := EncodeLossyBytes(src, &LossyOptions{Quality: 90, AlphaFilter: filter})
		if err != nil {
			t.Fatalf("%s: encode: %v", names[filter], err)
		}
		ref, err := xwebp.Decode(bytes.NewReader(data))
		if err != nil {
			t.Fatalf("%s: reference decode: %v", names[filter], err)
		}
		w, err := ParseWebpBytes(data)
		if err != nil {
			t.Fatalf("%s: parse: %v", names[filter], err)
		}
		got, err := w.ToImage()
		if err != nil {
			t.Fatalf("%s: decode: %v", names[filter], err)
		}
		b := got.Bounds()
		for y := 0; y < b.Dy(); y++ {
			for x := 0; x < b.Dx(); x++ {
				_, _, _, ga := got.At(x, y).RGBA()
				_, _, _, ra := ref.At(x, y).RGBA()
				if ga != ra {
					t.Fatalf("%s: alpha differs at %d,%d: got %d want %d",
						names[filter], x, y, ga>>8, ra>>8)
				}
			}
		}
	}
}
