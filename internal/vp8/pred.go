package vp8

// 帧内预测模式常量（VP8 规范 11 节）；末尾三个是帧边界上使用的 DC 单边
// 变体：分别只依赖顶行、左列，或两者都不依赖。
//
// Intra prediction mode constants (VP8 spec section 11); the last three are
// the one sided DC variants used on frame borders: they depend on the top
// row only, the left column only, or neither.
const (
	predDC = iota
	predTM
	predVE
	predHE
	predRD
	predVR
	predLD
	predVL
	predHD
	predHU
	predDCTop
	predDCLeft
	predDCTopLeft
)

// plane 是帧的一个分量，上方和左侧各留一像素边框，预测时读取邻居不需要
// 边界检查。亮度平面还在右侧多留四列，供 4x4 预测器的右上超出像素使用。
//
// plane is one component of the frame with a one pixel border above and to
// the left, so that predictions can read their neighbours without bounds
// checks. Luma planes also reserve four extra columns on the right for the
// top-right overhang of the 4x4 predictors.
type plane struct {
	pix    []byte
	stride int
	width  int
	height int
	right  int
}

// newPlane 分配平面缓冲：stride 含左侧一列边框，缓冲比 height 多一行。
//
// newPlane allocates a plane buffer: the stride includes the one pixel left
// border and the buffer holds one row more than height.
func newPlane(width, height, right int) *plane {
	stride := width + right + 1
	return &plane{
		pix:    make([]byte, stride*(height+1)),
		stride: stride,
		width:  width,
		height: height,
		right:  right,
	}
}

// at 返回像素 (x, y) 的下标，其中 (-1, -1) 是左上角边框像素。
//
// at returns the index of pixel (x, y), where (-1, -1) is the top left border
// pixel.
func (p *plane) at(x, y int) int {
	return (y+1)*p.stride + x + 1
}

// initBorders 把顶行填成 127、左列填成 129，即规范 12.2 节为帧外像素
// 规定的取值。
//
// initBorders fills the top row with 127 and the left column with 129, the
// values section 12.2 prescribes for pixels outside the frame.
func (p *plane) initBorders() {
	for x := -1; x < p.width+p.right; x++ {
		p.pix[p.at(x, -1)] = 127
	}
	for y := 0; y < p.height; y++ {
		p.pix[p.at(-1, y)] = 129
	}
}

// fillOverhang 把已解码行的最后一列复制到右侧附加列；最右侧的 4x4
// 预测器把这些列当作右上像素。
//
// fillOverhang duplicates the last column of a decoded row into the extra
// columns, which the rightmost 4x4 predictors use as their top-right pixels.
func (p *plane) fillOverhang(y int) {
	if p.right == 0 {
		return
	}
	last := p.pix[p.at(p.width-1, y)]
	for x := p.width; x < p.width+p.right; x++ {
		p.pix[p.at(x, y)] = last
	}
}

// clip255 把 v 截断到 [0, 255] 范围。
//
// clip255 clamps v to the [0, 255] range.
func clip255(v int) byte {
	if v < 0 {
		return 0
	}
	if v > 255 {
		return 255
	}
	return byte(v)
}

// clipInt 把 v 截断到 [lo, hi] 范围。
//
// clipInt clamps v to the [lo, hi] range.
func clipInt(v, lo, hi int) int {
	if v < lo {
		return lo
	}
	if v > hi {
		return hi
	}
	return v
}

// avg2 把 (a+b)/2 向上取整。
//
// avg2 rounds (a+b)/2 up.
func avg2(a, b int) int { return (a + b + 1) >> 1 }

// avg3 是对角预测模式使用的平滑平均 (a+2*b+c+2)/4。
//
// avg3 is the smoothed average (a+2*b+c+2)/4 used by the diagonal predictors.
func avg3(a, b, c int) int { return (a + 2*b + c + 2) >> 2 }

// predict4 以 (x, y) 为左上角写出 4x4 亮度预测块。
//
// predict4 writes the 4x4 predicted block with its top left corner at (x, y).
func (d *decoder) predict4(mode uint8, x, y int) {
	p := d.y
	a := int(p.pix[p.at(x-1, y-1)])
	b := int(p.pix[p.at(x+0, y-1)])
	c := int(p.pix[p.at(x+1, y-1)])
	dd := int(p.pix[p.at(x+2, y-1)])
	e := int(p.pix[p.at(x+3, y-1)])
	// 宏块最右块右侧的四个像素取自宏块上方那一行，而不是块上方那一行：
	// 此时右侧相邻宏块还没有解码。
	// 宏块最右块右侧的四个像素取自宏块上方那一行，而不是该块上方那一行：
	// 此时右侧相邻宏块还没有解码。
	// The four pixels right of the rightmost block of a macroblock are taken
	// from the row above the macroblock, not from the row above the block:
	// the neighbouring macroblock has not been decoded yet at this point.
	var f, g, h, i8 int
	if x&15 == 12 {
		f, g, h, i8 = int(d.tr[0]), int(d.tr[1]), int(d.tr[2]), int(d.tr[3])
	} else {
		f = int(p.pix[p.at(x+4, y-1)])
		g = int(p.pix[p.at(x+5, y-1)])
		h = int(p.pix[p.at(x+6, y-1)])
		i8 = int(p.pix[p.at(x+7, y-1)])
	}
	pp := int(p.pix[p.at(x-1, y+0)])
	q := int(p.pix[p.at(x-1, y+1)])
	r := int(p.pix[p.at(x-1, y+2)])
	s := int(p.pix[p.at(x-1, y+3)])

	switch mode {
	case predDC:
		avg := (b + c + dd + e + pp + q + r + s + 4) >> 3
		d.fillBlock4(x, y, byte(avg))

	case predTM:
		for j := 0; j < 4; j++ {
			left := [4]int{pp, q, r, s}[j]
			for i := 0; i < 4; i++ {
				top := [4]int{b, c, dd, e}[i]
				p.pix[p.at(x+i, y+j)] = clip255(left + top - a)
			}
		}

	case predVE:
		col := [4]int{avg3(a, b, c), avg3(b, c, dd), avg3(c, dd, e), avg3(dd, e, f)}
		for j := 0; j < 4; j++ {
			for i := 0; i < 4; i++ {
				p.pix[p.at(x+i, y+j)] = byte(col[i])
			}
		}

	case predHE:
		row := [4]int{avg3(a, pp, q), avg3(pp, q, r), avg3(q, r, s), avg3(r, s, s)}
		for j := 0; j < 4; j++ {
			for i := 0; i < 4; i++ {
				p.pix[p.at(x+i, y+j)] = byte(row[j])
			}
		}

	case predRD:
		srq := avg3(s, r, q)
		rqp := avg3(r, q, pp)
		qpa := avg3(q, pp, a)
		pab := avg3(pp, a, b)
		abc := avg3(a, b, c)
		bcd := avg3(b, c, dd)
		cde := avg3(c, dd, e)
		d.set4(x, y+0, pab, abc, bcd, cde)
		d.set4(x, y+1, qpa, pab, abc, bcd)
		d.set4(x, y+2, rqp, qpa, pab, abc)
		d.set4(x, y+3, srq, rqp, qpa, pab)

	case predVR:
		ab := avg2(a, b)
		bc := avg2(b, c)
		cd := avg2(c, dd)
		de := avg2(dd, e)
		rqp := avg3(r, q, pp)
		qpa := avg3(q, pp, a)
		pab := avg3(pp, a, b)
		abc := avg3(a, b, c)
		bcd := avg3(b, c, dd)
		cde := avg3(c, dd, e)
		d.set4(x, y+0, ab, bc, cd, de)
		d.set4(x, y+1, pab, abc, bcd, cde)
		d.set4(x, y+2, qpa, ab, bc, cd)
		d.set4(x, y+3, rqp, pab, abc, bcd)

	case predLD:
		// LD 与 VL 只看顶行，所以它们的 a..h 就是 top[0..7]。
		// LD and VL only look at the top row, so their a..h are top[0..7].
		abc := avg3(b, c, dd)
		bcd := avg3(c, dd, e)
		cde := avg3(dd, e, f)
		def := avg3(e, f, g)
		efg := avg3(f, g, h)
		fgh := avg3(g, h, i8)
		ghh := avg3(h, i8, i8)
		d.set4(x, y+0, abc, bcd, cde, def)
		d.set4(x, y+1, bcd, cde, def, efg)
		d.set4(x, y+2, cde, def, efg, fgh)
		d.set4(x, y+3, def, efg, fgh, ghh)

	case predVL:
		ab := avg2(b, c)
		bc := avg2(c, dd)
		cd := avg2(dd, e)
		de := avg2(e, f)
		abc := avg3(b, c, dd)
		bcd := avg3(c, dd, e)
		cde := avg3(dd, e, f)
		def := avg3(e, f, g)
		efg := avg3(f, g, h)
		fgh := avg3(g, h, i8)
		d.set4(x, y+0, ab, bc, cd, de)
		d.set4(x, y+1, abc, bcd, cde, def)
		d.set4(x, y+2, bc, cd, de, efg)
		d.set4(x, y+3, bcd, cde, def, fgh)

	case predHD:
		sr := avg2(s, r)
		rq := avg2(r, q)
		qp := avg2(q, pp)
		pa := avg2(pp, a)
		srq := avg3(s, r, q)
		rqp := avg3(r, q, pp)
		qpa := avg3(q, pp, a)
		pab := avg3(pp, a, b)
		abc := avg3(a, b, c)
		bcd := avg3(b, c, dd)
		d.set4(x, y+0, pa, pab, abc, bcd)
		d.set4(x, y+1, qp, qpa, pa, pab)
		d.set4(x, y+2, rq, rqp, qp, qpa)
		d.set4(x, y+3, sr, srq, rq, rqp)

	case predHU:
		pq := avg2(pp, q)
		qr := avg2(q, r)
		rs := avg2(r, s)
		pqr := avg3(pp, q, r)
		qrs := avg3(q, r, s)
		rss := avg3(r, s, s)
		d.set4(x, y+0, pq, pqr, qr, qrs)
		d.set4(x, y+1, qr, qrs, rs, rss)
		d.set4(x, y+2, rs, rss, s, s)
		d.set4(x, y+3, s, s, s, s)
	}
}

// set4 写出一行四个像素。
//
// set4 writes one row of four pixels.
func (d *decoder) set4(x, y int, v0, v1, v2, v3 int) {
	p := d.y
	p.pix[p.at(x+0, y)] = clip255(v0)
	p.pix[p.at(x+1, y)] = clip255(v1)
	p.pix[p.at(x+2, y)] = clip255(v2)
	p.pix[p.at(x+3, y)] = clip255(v3)
}

// fillBlock4 把 4x4 块全部填充为常量 v。
//
// fillBlock4 fills a 4x4 block with the constant v.
func (d *decoder) fillBlock4(x, y int, v byte) {
	p := d.y
	for j := 0; j < 4; j++ {
		for i := 0; i < 4; i++ {
			p.pix[p.at(x+i, y+j)] = v
		}
	}
}

// predict8 在 (x, y) 处写出 8x8 色度预测块。
//
// predict8 writes the 8x8 chroma prediction block at (x, y).
func (d *decoder) predict8(p *plane, mode uint8, x, y int) {
	a := int(p.pix[p.at(x-1, y-1)])
	switch mode {
	case predDC:
		sum := 8
		for i := 0; i < 8; i++ {
			sum += int(p.pix[p.at(x+i, y-1)])
			sum += int(p.pix[p.at(x-1, y+i)])
		}
		v := byte(sum / 16)
		fillBlock(p, x, y, 8, v)
	case predDCTop:
		sum := 4
		for j := 0; j < 8; j++ {
			sum += int(p.pix[p.at(x-1, y+j)])
		}
		fillBlock(p, x, y, 8, byte(sum/8))
	case predDCLeft:
		sum := 4
		for i := 0; i < 8; i++ {
			sum += int(p.pix[p.at(x+i, y-1)])
		}
		fillBlock(p, x, y, 8, byte(sum/8))
	case predDCTopLeft:
		fillBlock(p, x, y, 8, 128)
	case predTM:
		for j := 0; j < 8; j++ {
			left := int(p.pix[p.at(x-1, y+j)])
			for i := 0; i < 8; i++ {
				top := int(p.pix[p.at(x+i, y-1)])
				p.pix[p.at(x+i, y+j)] = clip255(left + top - a)
			}
		}
	case predVE:
		for j := 0; j < 8; j++ {
			copy(p.pix[p.at(x, y+j):p.at(x, y+j)+8], p.pix[p.at(x, y-1):p.at(x, y-1)+8])
		}
	case predHE:
		for j := 0; j < 8; j++ {
			left := p.pix[p.at(x-1, y+j)]
			for i := 0; i < 8; i++ {
				p.pix[p.at(x+i, y+j)] = left
			}
		}
	}
}

// predict16 在 (x, y) 处写出 16x16 亮度预测块。
//
// predict16 writes the 16x16 luma prediction block at (x, y).
func (d *decoder) predict16(mode uint8, x, y int) {
	p := d.y
	a := int(p.pix[p.at(x-1, y-1)])
	switch mode {
	case predDC:
		sum := 16
		for i := 0; i < 16; i++ {
			sum += int(p.pix[p.at(x+i, y-1)])
			sum += int(p.pix[p.at(x-1, y+i)])
		}
		fillBlock(p, x, y, 16, byte(sum/32))
	case predDCTop:
		sum := 8
		for j := 0; j < 16; j++ {
			sum += int(p.pix[p.at(x-1, y+j)])
		}
		fillBlock(p, x, y, 16, byte(sum/16))
	case predDCLeft:
		sum := 8
		for i := 0; i < 16; i++ {
			sum += int(p.pix[p.at(x+i, y-1)])
		}
		fillBlock(p, x, y, 16, byte(sum/16))
	case predDCTopLeft:
		fillBlock(p, x, y, 16, 128)
	case predTM:
		for j := 0; j < 16; j++ {
			left := int(p.pix[p.at(x-1, y+j)])
			for i := 0; i < 16; i++ {
				top := int(p.pix[p.at(x+i, y-1)])
				p.pix[p.at(x+i, y+j)] = clip255(left + top - a)
			}
		}
	case predVE:
		for j := 0; j < 16; j++ {
			copy(p.pix[p.at(x, y+j):p.at(x, y+j)+16], p.pix[p.at(x, y-1):p.at(x, y-1)+16])
		}
	case predHE:
		for j := 0; j < 16; j++ {
			left := p.pix[p.at(x-1, y+j)]
			for i := 0; i < 16; i++ {
				p.pix[p.at(x+i, y+j)] = left
			}
		}
	}
}

// fillBlock 把 n x n 块填充为常量 v，逐行用整行写入。
//
// fillBlock fills an n x n block with the constant v, one full row at a time.
func fillBlock(p *plane, x, y, n int, v byte) {
	for j := 0; j < n; j++ {
		row := p.pix[p.at(x, y+j) : p.at(x, y+j)+n]
		for i := range row {
			row[i] = v
		}
	}
}
