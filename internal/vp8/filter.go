package vp8

// 环路滤波（deblocking），见 RFC 6386 第 15 节。
// Loop filtering, section 15 of RFC 6386.

// computeFilterParams 推导每个 segment、每种模式的滤波阈值：
// 基准 level 取 segment 或帧级设置，再叠加环路滤波 delta，
// 最后按 sharpness 折算出 inner level 与高阶阈值 hlevel。
// computeFilterParams derives the per segment and per mode filter
// thresholds: the base level comes from the segment or the frame, loop
// filter deltas are added on top, and sharpness then scales it down into
// the inner level ilevel and the high threshold hlevel.
func (d *decoder) computeFilterParams() {
	for i := range d.filterParams {
		baseLevel := d.filt.level
		if d.seg.useSegment {
			baseLevel = d.seg.filterStrength[i]
			if d.seg.relativeDelta {
				baseLevel += d.filt.level
			}
		}
		for j := range d.filterParams[i] {
			p := &d.filterParams[i][j]
			p.inner = j != 0
			level := baseLevel
			if d.filt.useLFDelta {
				level += d.filt.refLFDelta[0]
				if j != 0 {
					level += d.filt.modeLFDelta[0]
				}
			}
			if level <= 0 {
				p.level = 0
				continue
			}
			if level > 63 {
				level = 63
			}
			ilevel := level
			if d.filt.sharpness > 0 {
				if d.filt.sharpness > 4 {
					ilevel >>= 2
				} else {
					ilevel >>= 1
				}
				if x := int8(9 - d.filt.sharpness); ilevel > x {
					ilevel = x
				}
			}
			if ilevel < 1 {
				ilevel = 1
			}
			p.ilevel = uint8(ilevel)
			p.level = uint8(2*level + ilevel)
			if level < 15 {
				p.hlevel = 0
			} else if level < 40 {
				p.hlevel = 1
			} else {
				p.hlevel = 2
			}
		}
	}
}

// filter2 沿一条宏块边对两像素宽的带做平滑，属于简单滤波（simple filter）。
// 只在两侧差值低于 level 时才动笔，避免把真实边缘磨掉。
// filter2 smooths a two pixel band along one macroblock edge, the simple
// filter. It only touches pixels when the difference across the edge is
// below level, so real edges are preserved.
func filter2(p *plane, level, index, iStep, jStep int) {
	pix := p.pix
	for n := 16; n > 0; n, index = n-1, index+iStep {
		p1 := int(pix[index-2*jStep])
		p0 := int(pix[index-jStep])
		q0 := int(pix[index])
		q1 := int(pix[index+jStep])
		if (absInt(p0-q0)<<1)+(absInt(p1-q1)>>1) > level {
			continue
		}
		a := 3*(q0-p0) + clamp127(p1-q1)
		a1 := clamp15((a + 4) >> 3)
		a2 := clamp15((a + 3) >> 3)
		pix[index-jStep] = clamp255(p0 + a2)
		pix[index] = clamp255(q0 - a1)
	}
}

// filter246 沿一条边对 2、4 或 6 像素宽的带做平滑，属于普通滤波（normal filter）。
// 先用 level 做粗判，再用 ilevel 逐像素检查是否跨过边缘；
// 边缘较陡（超过 hlevel）时只平滑内侧两像素，否则按 fourNotSix
// 平滑 4 或 6 个像素。
// filter246 smooths a two, four or six pixel band along one edge, the
// normal filter. It gates on level first, then checks each pixel against
// ilevel; a steep edge (above hlevel) gets the inner two pixels only,
// otherwise four or six pixels are smoothed per fourNotSix.
func filter246(p *plane, n, level, ilevel, hlevel, index, iStep, jStep int, fourNotSix bool) {
	pix := p.pix
	for ; n > 0; n, index = n-1, index+iStep {
		p3 := int(pix[index-4*jStep])
		p2 := int(pix[index-3*jStep])
		p1 := int(pix[index-2*jStep])
		p0 := int(pix[index-jStep])
		q0 := int(pix[index])
		q1 := int(pix[index+jStep])
		q2 := int(pix[index+2*jStep])
		q3 := int(pix[index+3*jStep])
		if (absInt(p0-q0)<<1)+(absInt(p1-q1)>>1) > level {
			continue
		}
		if absInt(p3-p2) > ilevel ||
			absInt(p2-p1) > ilevel ||
			absInt(p1-p0) > ilevel ||
			absInt(q1-q0) > ilevel ||
			absInt(q2-q1) > ilevel ||
			absInt(q3-q2) > ilevel {
			continue
		}
		if absInt(p1-p0) > hlevel || absInt(q1-q0) > hlevel {
			a := 3*(q0-p0) + clamp127(p1-q1)
			a1 := clamp15((a + 4) >> 3)
			a2 := clamp15((a + 3) >> 3)
			pix[index-jStep] = clamp255(p0 + a2)
			pix[index] = clamp255(q0 - a1)
		} else if fourNotSix {
			a := 3 * (q0 - p0)
			a1 := clamp15((a + 4) >> 3)
			a2 := clamp15((a + 3) >> 3)
			a3 := (a1 + 1) >> 1
			pix[index-2*jStep] = clamp255(p1 + a3)
			pix[index-jStep] = clamp255(p0 + a2)
			pix[index] = clamp255(q0 - a1)
			pix[index+jStep] = clamp255(q1 - a3)
		} else {
			a := clamp127(3*(q0-p0) + clamp127(p1-q1))
			a1 := (27*a + 63) >> 7
			a2 := (18*a + 63) >> 7
			a3 := (9*a + 63) >> 7
			pix[index-3*jStep] = clamp255(p2 + a3)
			pix[index-2*jStep] = clamp255(p1 + a2)
			pix[index-jStep] = clamp255(p0 + a1)
			pix[index] = clamp255(q0 - a1)
			pix[index+jStep] = clamp255(q1 - a2)
			pix[index+2*jStep] = clamp255(q2 - a3)
		}
	}
}

// simpleFilter 在整帧上运行简单环路滤波：
// 只滤亮度平面，宏块外边界用 level+4，内边界（若启用）用 level。
// simpleFilter runs the simple loop filter over the frame. It touches the
// luma plane only, using level+4 on macroblock outer edges and level on
// inner edges (when enabled).
func (d *decoder) simpleFilter() {
	for mby := 0; mby < d.mbh; mby++ {
		for mbx := 0; mbx < d.mbw; mbx++ {
			f := d.perMBFilterParams[d.mbw*mby+mbx]
			if f.level == 0 {
				continue
			}
			l := int(f.level)
			yIndex := d.y.at(mbx*16, mby*16)
			if mbx > 0 {
				filter2(d.y, l+4, yIndex, d.y.stride, 1)
			}
			if f.inner {
				filter2(d.y, l, yIndex+0x4, d.y.stride, 1)
				filter2(d.y, l, yIndex+0x8, d.y.stride, 1)
				filter2(d.y, l, yIndex+0xc, d.y.stride, 1)
			}
			if mby > 0 {
				filter2(d.y, l+4, yIndex, 1, d.y.stride)
			}
			if f.inner {
				filter2(d.y, l, yIndex+d.y.stride*0x4, 1, d.y.stride)
				filter2(d.y, l, yIndex+d.y.stride*0x8, 1, d.y.stride)
				filter2(d.y, l, yIndex+d.y.stride*0xc, 1, d.y.stride)
			}
		}
	}
}

// normalFilter 在整帧上运行普通环路滤波：亮度与两个色度平面都处理，
// 宏块外边界用 level+4 且做 6 像素滤波，内边界（若启用）用 level 做 4 像素滤波。
// normalFilter runs the normal loop filter over the frame: it covers the
// luma plane and both chroma planes, using level+4 with the six pixel
// filter on macroblock outer edges and level with the four pixel filter
// on inner edges (when enabled).
func (d *decoder) normalFilter() {
	for mby := 0; mby < d.mbh; mby++ {
		for mbx := 0; mbx < d.mbw; mbx++ {
			f := d.perMBFilterParams[d.mbw*mby+mbx]
			if f.level == 0 {
				continue
			}
			l, il, hl := int(f.level), int(f.ilevel), int(f.hlevel)
			yIndex := d.y.at(mbx*16, mby*16)
			cIndex := d.cb.at(mbx*8, mby*8)
			if mbx > 0 {
				filter246(d.y, 16, l+4, il, hl, yIndex, d.y.stride, 1, false)
				filter246(d.cb, 8, l+4, il, hl, cIndex, d.cb.stride, 1, false)
				filter246(d.cr, 8, l+4, il, hl, cIndex, d.cr.stride, 1, false)
			}
			if f.inner {
				filter246(d.y, 16, l, il, hl, yIndex+0x4, d.y.stride, 1, true)
				filter246(d.y, 16, l, il, hl, yIndex+0x8, d.y.stride, 1, true)
				filter246(d.y, 16, l, il, hl, yIndex+0xc, d.y.stride, 1, true)
				filter246(d.cb, 8, l, il, hl, cIndex+0x4, d.cb.stride, 1, true)
				filter246(d.cr, 8, l, il, hl, cIndex+0x4, d.cr.stride, 1, true)
			}
			if mby > 0 {
				filter246(d.y, 16, l+4, il, hl, yIndex, 1, d.y.stride, false)
				filter246(d.cb, 8, l+4, il, hl, cIndex, 1, d.cb.stride, false)
				filter246(d.cr, 8, l+4, il, hl, cIndex, 1, d.cr.stride, false)
			}
			if f.inner {
				filter246(d.y, 16, l, il, hl, yIndex+d.y.stride*0x4, 1, d.y.stride, true)
				filter246(d.y, 16, l, il, hl, yIndex+d.y.stride*0x8, 1, d.y.stride, true)
				filter246(d.y, 16, l, il, hl, yIndex+d.y.stride*0xc, 1, d.y.stride, true)
				filter246(d.cb, 8, l, il, hl, cIndex+d.cb.stride*0x4, 1, d.cb.stride, true)
				filter246(d.cr, 8, l, il, hl, cIndex+d.cr.stride*0x4, 1, d.cr.stride, true)
			}
		}
	}
}

// absInt 返回整数绝对值。/ absInt returns the absolute value of an int.
func absInt(x int) int {
	if x < 0 {
		return -x
	}
	return x
}

// clamp15 把值钳制到 [-16, 15]。/ clamp15 clamps x to [-16, 15].
func clamp15(x int) int { return clipInt(x, -16, 15) }

// clamp127 把值钳制到 [-128, 127]。/ clamp127 clamps x to [-128, 127].
func clamp127(x int) int { return clipInt(x, -128, 127) }

// clamp255 把值钳制到 [0, 255] 并返回一个字节。
// clamp255 clamps x to [0, 255] and returns it as a byte.
func clamp255(x int) byte {
	if x < 0 {
		return 0
	}
	if x > 255 {
		return 255
	}
	return byte(x)
}
