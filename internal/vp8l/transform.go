package vp8l

// 无损位流定义的四种变换，取值即位流中的 transform type。
// The four transforms of the lossless bitstream, the values are the transform
// type codes used on the wire.
const (
	transformPredictor     = 0
	transformColor         = 1
	transformSubtractGreen = 2
	transformColorIndexing = 3
)

// transform 是一个已解码的变换记录。逆变换按位流顺序的逆序施加，
// 每个变换都记住了它所作用的图像尺寸。
// transform is one decoded transform. The inverse transforms are applied in
// the reverse order of the bitstream, every transform knows the image size it
// was applied to.
type transform struct {
	kind int
	// bits 对预测器变换和颜色变换是分块尺寸的以 2 为底的指数，
	// 对颜色索引变换则是像素打包宽度的指数。
	// bits is the block size exponent for the predictor and color transforms,
	// and the pixel packing exponent for the color indexing transform.
	bits  int
	xsize int
	ysize int
	// data 是预测器/颜色变换的子图像，或是颜色索引变换展开后的调色板。
	// data is the predictor / color transform sub-image, or the expanded
	// palette of the color indexing transform.
	data []uint32
}

// divRoundUp 返回 (a+b-1)/b，即向上取整的除法，用于把尺寸换算成块数。
// divRoundUp returns (a+b-1)/b, division rounded up, used to turn a size into
// a block count.
func divRoundUp(a, b int) int {
	return (a + b - 1) / b
}

// packingBits 返回颜色索引变换的打包指数：每字节存放 1<<bits 个像素，
// 因此每个索引占 8>>bits 位（调色板越小索引越窄）。
// packingBits returns the color indexing packing exponent: the number of
// pixels stored per byte is 1<<bits, so the bits per index are 8>>bits.
func packingBits(numColors int) int {
	switch {
	case numColors <= 2:
		return 3
	case numColors <= 4:
		return 2
	case numColors <= 16:
		return 1
	default:
		return 0
	}
}

// inverse 对解码出的残差施加逆变换，返回重建后的像素；未知 kind 返回 errBadBitstream。
// inverse applies the inverse transform to the decoded residuals and returns
// the reconstructed pixels. An unknown kind yields errBadBitstream.
func (t *transform) inverse(pix []uint32) ([]uint32, error) {
	switch t.kind {
	case transformSubtractGreen:
		for i, p := range pix {
			pix[i] = addGreen(p)
		}
		return pix, nil
	case transformPredictor:
		inversePredictor(pix, t.xsize, t.ysize, t.bits, t.data)
		return pix, nil
	case transformColor:
		inverseColor(pix, t.xsize, t.ysize, t.bits, t.data)
		return pix, nil
	case transformColorIndexing:
		return inverseColorIndexing(pix, t.xsize, t.ysize, t.bits, t.data), nil
	}
	return nil, errBadBitstream
}

// addPixels 逐通道相加两个 ARGB 像素，模 256（进位被掩码丢弃）。
// addPixels adds two ARGB pixels channel by channel, modulo 256.
func addPixels(a, b uint32) uint32 {
	ag := (a&0xff00ff00 + b&0xff00ff00) & 0xff00ff00
	rb := (a&0x00ff00ff + b&0x00ff00ff) & 0x00ff00ff
	return ag | rb
}

// average2 逐通道求两个 ARGB 像素的平均值，向下取整。
// 无进位写法很关键：若直接把两个 alpha 字节相加，会溢出 32 位字并丢掉进位。
// average2 averages two ARGB pixels channel by channel, rounding down.
// The carry-free form matters: adding the two alpha bytes in place would
// overflow the 32-bit word and lose the carry.
func average2(a, b uint32) uint32 {
	return ((a^b)&0xfefefefe)>>1 + (a & b)
}

// addGreen 是减绿变换的逆变换：把绿通道加回到红、蓝通道上（alpha 不变）。
// addGreen is the inverse subtract green transform: it adds the green channel
// back into the red and blue channels, leaving alpha untouched.
func addGreen(p uint32) uint32 {
	g := p >> 8 & 0xff
	r := (p>>16&0xff + g) & 0xff
	b := (p&0xff + g) & 0xff
	return p&0xff00ff00 | r<<16 | b
}

// clip255 把 v 夹到 [0,255]，供各预测器模式逐通道钳位。
// clip255 clamps v into [0,255]; it is the per-channel clamp of the predictor
// modes.
func clip255(v int) int {
	if v < 0 {
		return 0
	}
	if v > 255 {
		return 255
	}
	return v
}

// absInt 返回 v 的绝对值，用于计算曼哈顿距离。
// absInt returns the absolute value of v, used for Manhattan distances.
func absInt(v int) int {
	if v < 0 {
		return -v
	}
	return v
}

// selectPixel 实现预测器模式 11：在左边像素和上边像素中取与 L+T-TL 估计值
// 曼哈顿距离更近的一个。距离相同时取上边像素，与参考实现一致。
// selectPixel is predictor mode 11: the left or the top pixel, whichever is
// closer to the L+T-TL estimate in Manhattan distance. A tie picks the top
// pixel, as the reference implementation does.
func selectPixel(l, t, tl uint32) uint32 {
	pl, pt := 0, 0
	for shift := uint(0); shift < 32; shift += 8 {
		lv := int(l >> shift & 0xff)
		tv := int(t >> shift & 0xff)
		tlv := int(tl >> shift & 0xff)
		p := lv + tv - tlv
		pl += absInt(p - lv)
		pt += absInt(p - tv)
	}
	if pl < pt {
		return l
	}
	return t
}

// clampAddSubtractFull 实现预测器模式 12：逐通道计算 a+b-c 并钳位到 [0,255]。
// clampAddSubtractFull is predictor mode 12: it computes a+b-c per channel and
// clamps into [0,255].
func clampAddSubtractFull(a, b, c uint32) uint32 {
	var out uint32
	for shift := uint(0); shift < 32; shift += 8 {
		v := int(a>>shift&0xff) + int(b>>shift&0xff) - int(c>>shift&0xff)
		out |= uint32(clip255(v)) << shift
	}
	return out
}

// clampAddSubtractHalf 实现预测器模式 13：逐通道计算 a+(a-b)/2 并钳位到 [0,255]。
// clampAddSubtractHalf is predictor mode 13: it computes a+(a-b)/2 per channel
// and clamps into [0,255].
func clampAddSubtractHalf(a, b uint32) uint32 {
	var out uint32
	for shift := uint(0); shift < 32; shift += 8 {
		av := int(a >> shift & 0xff)
		bv := int(b >> shift & 0xff)
		out |= uint32(clip255(av+(av-bv)/2)) << shift
	}
	return out
}

// predict 按给定模式返回单个像素的预测 ARGB 值（模式 0..13，见规范
// “Predictor Transform”一节）；未知模式返回 0。
// predict returns the predicted ARGB value of one pixel for the given mode
// (modes 0..13, see the spec section on the Predictor Transform); an unknown
// mode yields 0.
func predict(mode int, left, top, topLeft, topRight uint32) uint32 {
	switch mode {
	case 0:
		return 0xff000000
	case 1:
		return left
	case 2:
		return top
	case 3:
		return topRight
	case 4:
		return topLeft
	case 5:
		return average2(average2(left, topRight), top)
	case 6:
		return average2(left, topLeft)
	case 7:
		return average2(left, top)
	case 8:
		return average2(topLeft, top)
	case 9:
		return average2(top, topRight)
	case 10:
		return average2(average2(left, topLeft), average2(top, topRight))
	case 11:
		return selectPixel(left, top, topLeft)
	case 12:
		return clampAddSubtractFull(left, top, topLeft)
	case 13:
		return clampAddSubtractHalf(average2(left, top), topLeft)
	}
	return 0
}

// inversePredictor 重建预测器变换的像素。
//
// 左上像素按不透明纯黑预测；第一整行用左边像素预测，其余各行的第一列用
// 上边像素预测。最右列的“右上像素”取当前行的最左像素，这与参考实现的行布局一致。
// inversePredictor reconstructs the pixels of the predictor transform.
//
// The top left pixel is predicted as opaque black, the whole first row uses
// the left pixel and the first column of every other row uses the top pixel.
// The top right pixel of the rightmost column is the leftmost pixel of the
// current row, as the reference implementation lays its rows out.
func inversePredictor(pix []uint32, width, height, bits int, pred []uint32) {
	if width <= 0 || height <= 0 || len(pix) < width*height {
		return
	}
	predWidth := divRoundUp(width, 1<<bits)
	pix[0] = addPixels(pix[0], 0xff000000)
	for x := 1; x < width; x++ {
		pix[x] = addPixels(pix[x], pix[x-1])
	}
	for y := 1; y < height; y++ {
		row := pix[y*width : (y+1)*width]
		prev := pix[(y-1)*width : y*width]
		predRow := pred[(y>>bits)*predWidth:]
		row[0] = addPixels(row[0], prev[0])
		for x := 1; x < width; x++ {
			mode := int(predRow[x>>bits] >> 8 & 0xf)
			var topRight uint32
			if x == width-1 {
				topRight = row[0]
			} else {
				topRight = prev[x+1]
			}
			row[x] = addPixels(row[x], predict(mode, row[x-1], prev[x], prev[x-1], topRight))
		}
	}
}

// colorDelta 即 ColorTransformDelta：带符号乘法后右移 5 位。
// colorDelta is ColorTransformDelta: a signed multiply with a five bit shift.
func colorDelta(t, c int8) int32 {
	return int32(t) * int32(c) >> 5
}

// inverseColor 由绿色通道重建红、蓝两个通道（颜色变换的逆）。
// inverseColor reconstructs the red and blue channels from the green one.
func inverseColor(pix []uint32, width, height, bits int, data []uint32) {
	blockWidth := divRoundUp(width, 1<<bits)
	for y := 0; y < height; y++ {
		row := pix[y*width : (y+1)*width]
		blocks := data[(y>>bits)*blockWidth:]
		for x := 0; x < width; x++ {
			c := blocks[x>>bits]
			greenToRed := int8(c)
			greenToBlue := int8(c >> 8)
			redToBlue := int8(c >> 16)

			p := row[x]
			green := int8(p >> 8)
			red := int32(p>>16) & 0xff
			blue := int32(p) & 0xff
			red += colorDelta(greenToRed, green)
			blue += colorDelta(greenToBlue, green)
			blue += colorDelta(redToBlue, int8(red&0xff))
			row[x] = p&0xff00ff00 | uint32(red&0xff)<<16 | uint32(blue&0xff)
		}
	}
}

// expandColorMap 把增量编码的调色板展开为绝对颜色。
// expandColorMap turns the delta coded palette into absolute colors.
func expandColorMap(pix []uint32, numColors int) []uint32 {
	out := make([]uint32, numColors)
	out[0] = pix[0]
	for i := 1; i < numColors; i++ {
		out[i] = addPixels(out[i-1], pix[i])
	}
	return out
}

// inverseColorIndexing 把打包的调色板索引展开成 ARGB 像素，返回新缓冲；
// 索引超出调色板范围的像素保持为 0。
// inverseColorIndexing expands the packed palette indices to ARGB pixels and
// returns a fresh buffer; a pixel whose index falls outside the palette stays
// zero.
func inverseColorIndexing(pix []uint32, xsize, ysize, bits int, colorMap []uint32) []uint32 {
	pixelsPerByte := 1 << bits
	bitsPerPixel := uint(8 >> bits)
	mask := uint32(1)<<bitsPerPixel - 1
	inWidth := divRoundUp(xsize, pixelsPerByte)
	out := make([]uint32, xsize*ysize)
	for y := 0; y < ysize; y++ {
		inRow := pix[y*inWidth : (y+1)*inWidth]
		outRow := out[y*xsize : (y+1)*xsize]
		for x := 0; x < xsize; x++ {
			packed := inRow[x>>bits] >> 8 & 0xff
			shift := uint(x&(pixelsPerByte-1)) * bitsPerPixel
			index := (packed >> shift) & mask
			if int(index) < len(colorMap) {
				outRow[x] = colorMap[index]
			}
		}
	}
	return out
}
