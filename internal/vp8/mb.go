package vp8

// 宏块解码：预测模式与 DCT/WHT 系数 token 的解析与重建。
// Macroblock decoding: prediction modes and DCT/WHT coefficient tokens.

// mbState 保存宏块解码过程中供相邻宏块参考的状态。
// mbState holds the state that neighbouring macroblocks refer to during decoding.
type mbState struct {
	// pred 存放下方或右侧 4x4 区域的预测模式。
	// pred holds the predictor modes of the bottom or right 4x4 regions.
	pred [4]uint8
	// nzMask 每个 4x4 区域占一位：亮度 4 位，色度按 2+2 位打包。
	// nzMask has one bit per 4x4 region: 4 for luma, 2+2 for chroma.
	nzMask uint8
	// nzY16 在 Y16 宏块存在非零系数时为 1。
	// nzY16 is 1 when a Y16 macroblock has non zero coefficients.
	nzY16 uint8
}

// unpackMask 把 4 位非零掩码展开成每 4x4 区域一个字节的 [4]uint8。
// unpackMask expands a 4-bit non zero mask into one byte per 4x4 region.
var unpackMask = [16][4]uint8{
	{0, 0, 0, 0},
	{1, 0, 0, 0},
	{0, 1, 0, 0},
	{1, 1, 0, 0},
	{0, 0, 1, 0},
	{1, 0, 1, 0},
	{0, 1, 1, 0},
	{1, 1, 1, 0},
	{0, 0, 0, 1},
	{1, 0, 0, 1},
	{0, 1, 0, 1},
	{1, 1, 0, 1},
	{0, 0, 1, 1},
	{1, 0, 1, 1},
	{0, 1, 1, 1},
	{1, 1, 1, 1},
}

// btou 把布尔值转成 0/1，便于参与按位运算。
// btou converts a boolean to 0/1 so it can take part in bit arithmetic.
func btou(b bool) uint8 {
	if b {
		return 1
	}
	return 0
}

// packMask 把 4 个区域标志打包成 4 位掩码，再左移 shift 位。
// packMask packs four region flags into a 4-bit mask shifted left by shift.
func packMask(v [4]uint8, shift int) uint32 {
	u := uint32(v[0]) | uint32(v[1])<<1 | uint32(v[2])<<2 | uint32(v[3])<<3
	return u << uint(shift)
}

// checkTopLeftPred 在帧边界上把 DC 模式替换为它的单边变体：左列用
// predDCLeft，顶行用 predDCTop，左上角用 predDCTopLeft。
//
// checkTopLeftPred replaces the DC mode by its one sided variant on the frame
// borders: predDCLeft on the left column, predDCTop on the top row and
// predDCTopLeft at the top-left corner.
func checkTopLeftPred(mbx, mby int, p uint8) uint8 {
	if p != predDC {
		return p
	}
	if mbx == 0 {
		if mby == 0 {
			return predDCTopLeft
		}
		return predDCLeft
	}
	if mby == 0 {
		return predDCTop
	}
	return predDC
}

// parsePredModeY16 读取 16x16 亮度预测模式（规范 11.2 节），并把它写入
// 上方与左侧宏块的参考状态。
//
// parsePredModeY16 reads the 16x16 luma mode, section 11.2, and writes it
// into the above and left macroblock reference state.
func (d *decoder) parsePredModeY16(mbx int) {
	var p uint8
	if !d.fp.readBit(156) {
		if !d.fp.readBit(163) {
			p = predDC
		} else {
			p = predVE
		}
	} else if !d.fp.readBit(128) {
		p = predHE
	} else {
		p = predTM
	}
	for i := 0; i < 4; i++ {
		d.upMB[mbx].pred[i] = p
		d.leftMB.pred[i] = p
	}
	d.predY16 = p
}

// parsePredModeC8 读取 8x8 色度预测模式（规范 11.4 节）。
// parsePredModeC8 reads the chroma mode, section 11.4.
func (d *decoder) parsePredModeC8() {
	if !d.fp.readBit(142) {
		d.predC8 = predDC
	} else if !d.fp.readBit(114) {
		d.predC8 = predVE
	} else if !d.fp.readBit(183) {
		d.predC8 = predHE
	} else {
		d.predC8 = predTM
	}
}

// parsePredModeY4 按上下文概率树读取 16 个 4x4 亮度预测模式（规范 11.5 节）；
// 当前模式 p 在块之间滚动传递，上方与左侧参考状态随之更新。
//
// parsePredModeY4 reads the sixteen 4x4 luma modes, section 11.5, using the
// context probability tree; the current mode p carries over between blocks
// and the above/left reference state is updated as it goes.
func (d *decoder) parsePredModeY4(mbx int) {
	for j := 0; j < 4; j++ {
		p := d.leftMB.pred[j]
		for i := 0; i < 4; i++ {
			prob := &predProb[d.upMB[mbx].pred[i]][p]
			if !d.fp.readBit(prob[0]) {
				p = predDC
			} else if !d.fp.readBit(prob[1]) {
				p = predTM
			} else if !d.fp.readBit(prob[2]) {
				p = predVE
			} else if !d.fp.readBit(prob[3]) {
				if !d.fp.readBit(prob[4]) {
					p = predHE
				} else if !d.fp.readBit(prob[5]) {
					p = predRD
				} else {
					p = predVR
				}
			} else if !d.fp.readBit(prob[6]) {
				p = predLD
			} else if !d.fp.readBit(prob[7]) {
				p = predVL
			} else if !d.fp.readBit(prob[8]) {
				p = predHD
			} else {
				p = predHU
			}
			d.predY4[j][i] = p
			d.upMB[mbx].pred[i] = p
		}
		d.leftMB.pred[j] = p
	}
}

// parseResiduals4 解码一个 4x4 块的系数 token（规范 13.3 节）。skipDC=true
// 时跳过 DC 系数（用于走 Y2 的 Y16 宏块）；q[0]/q[1] 分别是 DC/AC 量化因子，
// coeffBase 是系数写入 d.coeff 的基址。至少有一个非零系数时返回 1。
//
// parseResiduals4 decodes one 4x4 block of coefficient tokens, section 13.3.
// With skipDC=true it skips the DC coefficient (Y16 macroblocks that use
// Y2); q[0]/q[1] are the DC/AC quantizers and coeffBase is the base offset
// into d.coeff. It returns 1 when at least one coefficient was non zero.
func (d *decoder) parseResiduals4(r *boolDecoder, plane, context int, q [2]uint16, skipDC bool, coeffBase int) uint8 {
	prob := &d.tokenProb[plane]
	n := 0
	if skipDC {
		n = 1
	}
	p := prob[coeffBands[n]][context]
	if !r.readBit(p[0]) {
		return 0
	}
	for n != 16 {
		n++
		if !r.readBit(p[1]) {
			p = prob[coeffBands[n]][0]
			continue
		}
		var v uint32
		if !r.readBit(p[2]) {
			v = 1
			p = prob[coeffBands[n]][1]
		} else {
			if !r.readBit(p[3]) {
				if !r.readBit(p[4]) {
					v = 2
				} else {
					v = 3 + r.readUint(p[5], 1)
				}
			} else if !r.readBit(p[6]) {
				if !r.readBit(p[7]) {
					v = 5 + r.readUint(159, 1)
				} else {
					v = 7 + 2*r.readUint(165, 1) + r.readUint(145, 1)
				}
			} else {
				b1 := r.readUint(p[8], 1)
				b0 := r.readUint(p[9+b1], 1)
				cat := 2*b1 + b0
				tab := &cat3456[cat]
				v = 0
				for i := 0; tab[i] != 0; i++ {
					v *= 2
					v += r.readUint(tab[i], 1)
				}
				v += 3 + (8 << cat)
			}
			p = prob[coeffBands[n]][2]
		}
		z := zigzag[n-1]
		c := int32(v) * int32(q[btou(z > 0)])
		if r.readBit(uniformProb) {
			c = -c
		}
		d.coeff[coeffBase+int(z)] = int16(c)
		if n == 16 || !r.readBit(p[0]) {
			return 1
		}
	}
	return 1
}

// parseResiduals 解码整个宏块的所有系数，并报告该宏块是否完全没有系数；
// 完全无系数时可以跳过内环路滤波。
//
// parseResiduals decodes every coefficient of the macroblock and reports
// whether the macroblock has no coefficients at all, in which case the inner
// loop filter can be skipped.
func (d *decoder) parseResiduals(mbx, mby int) (skip bool) {
	part := &d.op[mby&(d.nOP-1)]
	plane := planeY1SansY2
	q := &d.q[d.segment]

	if d.usePredY16 {
		nz := d.parseResiduals4(part, planeY2, int(d.leftMB.nzY16+d.upMB[mbx].nzY16), q.y2, false, 384)
		d.leftMB.nzY16 = nz
		d.upMB[mbx].nzY16 = nz
		d.inverseWHT()
		plane = planeY1WithY2
	}

	var (
		nzDC, nzAC         [4]uint8
		nzDCMask, nzACMask uint32
		coeffBase          int
	)

	lnz := unpackMask[d.leftMB.nzMask&0x0f]
	unz := unpackMask[d.upMB[mbx].nzMask&0x0f]
	for y := 0; y < 4; y++ {
		nz := lnz[y]
		for x := 0; x < 4; x++ {
			nz = d.parseResiduals4(part, plane, int(nz+unz[x]), q.y1, d.usePredY16, coeffBase)
			unz[x] = nz
			nzAC[x] = nz
			nzDC[x] = btou(d.coeff[coeffBase] != 0)
			coeffBase += 16
		}
		lnz[y] = nz
		nzDCMask |= packMask(nzDC, y*4)
		nzACMask |= packMask(nzAC, y*4)
	}
	lnzMask := packMask(lnz, 0)
	unzMask := packMask(unz, 0)

	lnz = unpackMask[d.leftMB.nzMask>>4]
	unz = unpackMask[d.upMB[mbx].nzMask>>4]
	for c := 0; c < 4; c += 2 {
		for y := 0; y < 2; y++ {
			nz := lnz[y+c]
			for x := 0; x < 2; x++ {
				nz = d.parseResiduals4(part, planeUV, int(nz+unz[x+c]), q.uv, false, coeffBase)
				unz[x+c] = nz
				nzAC[y*2+x] = nz
				nzDC[y*2+x] = btou(d.coeff[coeffBase] != 0)
				coeffBase += 16
			}
			lnz[y+c] = nz
		}
		nzDCMask |= packMask(nzDC, 16+c*2)
		nzACMask |= packMask(nzAC, 16+c*2)
	}
	lnzMask |= packMask(lnz, 4)
	unzMask |= packMask(unz, 4)

	d.leftMB.nzMask = uint8(lnzMask)
	d.upMB[mbx].nzMask = uint8(unzMask)
	d.nzDCMask = nzDCMask
	d.nzACMask = nzACMask

	return nzDCMask == 0 && nzACMask == 0
}

// reconstructMacroblock 对宏块做预测并叠加残差：AC 非零的块走完整 IDCT，
// 只有 DC 非零的块走 DC-only 快速路径。
//
// reconstructMacroblock predicts the macroblock and adds the residuals:
// blocks with non zero AC take the full IDCT, blocks with only a non zero DC
// take the DC-only fast path.
func (d *decoder) reconstructMacroblock(mbx, mby int) {
	bx, by := mbx*16, mby*16
	if d.usePredY16 {
		d.predict16(checkTopLeftPred(mbx, mby, d.predY16), bx, by)
		for j := 0; j < 4; j++ {
			for i := 0; i < 4; i++ {
				n := 4*j + i
				x, y := bx+i*4, by+j*4
				mask := uint32(1) << uint(n)
				if d.nzACMask&mask != 0 {
					d.addIDCT4Plane(d.y, x, y, 16*n)
				} else if d.nzDCMask&mask != 0 {
					d.addIDCT4DCPlane(d.y, x, y, 16*n)
				}
			}
		}
	} else {
		for j := 0; j < 4; j++ {
			for i := 0; i < 4; i++ {
				n := 4*j + i
				x, y := bx+i*4, by+j*4
				d.predict4(d.predY4[j][i], x, y)
				mask := uint32(1) << uint(n)
				if d.nzACMask&mask != 0 {
					d.addIDCT4Plane(d.y, x, y, 16*n)
				} else if d.nzDCMask&mask != 0 {
					d.addIDCT4DCPlane(d.y, x, y, 16*n)
				}
			}
		}
	}

	cx, cy := mbx*8, mby*8
	cp := checkTopLeftPred(mbx, mby, d.predC8)
	d.predict8(d.cb, cp, cx, cy)
	if d.nzACMask&0x0f0000 != 0 {
		d.addIDCT8Plane(d.cb, cx, cy, 256)
	} else if d.nzDCMask&0x0f0000 != 0 {
		d.addIDCT8DCPlane(d.cb, cx, cy, 256)
	}
	d.predict8(d.cr, cp, cx, cy)
	if d.nzACMask&0xf00000 != 0 {
		d.addIDCT8Plane(d.cr, cx, cy, 320)
	} else if d.nzDCMask&0xf00000 != 0 {
		d.addIDCT8DCPlane(d.cr, cx, cy, 320)
	}
}

// addIDCT8Plane 对 8x8 色度块的四个 4x4 子块做逆 DCT 并累加到像素上。
// addIDCT8Plane applies the four 4x4 inverse DCTs of an 8x8 chroma block.
func (d *decoder) addIDCT8Plane(p *plane, x, y, coeffBase int) {
	d.addIDCT4Plane(p, x+0, y+0, coeffBase+0*16)
	d.addIDCT4Plane(p, x+4, y+0, coeffBase+1*16)
	d.addIDCT4Plane(p, x+0, y+4, coeffBase+2*16)
	d.addIDCT4Plane(p, x+4, y+4, coeffBase+3*16)
}

// addIDCT8DCPlane 是 addIDCT8Plane 的 DC-only 快速路径。
// addIDCT8DCPlane is the DC-only fast path of addIDCT8Plane.
func (d *decoder) addIDCT8DCPlane(p *plane, x, y, coeffBase int) {
	d.addIDCT4DCPlane(p, x+0, y+0, coeffBase+0*16)
	d.addIDCT4DCPlane(p, x+4, y+0, coeffBase+1*16)
	d.addIDCT4DCPlane(p, x+0, y+4, coeffBase+2*16)
	d.addIDCT4DCPlane(p, x+4, y+4, coeffBase+3*16)
}
