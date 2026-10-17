package vp8

// Intra prediction modes, section 11.
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

// at returns the index of pixel (x, y), where (-1, -1) is the top left border
// pixel.
func (p *plane) at(x, y int) int {
	return (y+1)*p.stride + x + 1
}

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

func clip255(v int) byte {
	if v < 0 {
		return 0
	}
	if v > 255 {
		return 255
	}
	return byte(v)
}

func clipInt(v, lo, hi int) int {
	if v < lo {
		return lo
	}
	if v > hi {
		return hi
	}
	return v
}

// avg2 rounds (a+b)/2 up, avg3 is the smoothed average of the diagonal
// predictor modes.
func avg2(a, b int) int { return (a + b + 1) >> 1 }

func avg3(a, b, c int) int { return (a + 2*b + c + 2) >> 2 }

// predict4 writes the 4x4 predicted block with its top left corner at (x, y).
func (d *decoder) predict4(mode uint8, x, y int) {
	p := d.y
	a := int(p.pix[p.at(x-1, y-1)])
	b := int(p.pix[p.at(x+0, y-1)])
	c := int(p.pix[p.at(x+1, y-1)])
	dd := int(p.pix[p.at(x+2, y-1)])
	e := int(p.pix[p.at(x+3, y-1)])
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
		// LD and VL only look at the top row, so their a..h are top[0..7]
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

// set4 writes one row of four pixels.
func (d *decoder) set4(x, y int, v0, v1, v2, v3 int) {
	p := d.y
	p.pix[p.at(x+0, y)] = clip255(v0)
	p.pix[p.at(x+1, y)] = clip255(v1)
	p.pix[p.at(x+2, y)] = clip255(v2)
	p.pix[p.at(x+3, y)] = clip255(v3)
}

func (d *decoder) fillBlock4(x, y int, v byte) {
	p := d.y
	for j := 0; j < 4; j++ {
		for i := 0; i < 4; i++ {
			p.pix[p.at(x+i, y+j)] = v
		}
	}
}

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

func fillBlock(p *plane, x, y, n int, v byte) {
	for j := 0; j < n; j++ {
		row := p.pix[p.at(x, y+j) : p.at(x, y+j)+n]
		for i := range row {
			row[i] = v
		}
	}
}
