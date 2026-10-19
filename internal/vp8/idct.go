package vp8

// 逆变换，见 RFC 6386 第 14.3、14.4 节。
// Inverse transforms, sections 14.3 and 14.4 of RFC 6386.

const (
	idctC1 = 85627 // Q16 定标系数：65536 * cos(pi/8) * sqrt(2) / Q16 fixed-point coefficient: 65536 * cos(pi/8) * sqrt(2)
	idctC2 = 35468 // Q16 定标系数：65536 * sin(pi/8) * sqrt(2) / Q16 fixed-point coefficient: 65536 * sin(pi/8) * sqrt(2)
)

// addIDCT4Plane 对一个 4x4 块做逆 DCT，并把结果加到平面 (x, y) 处的像素上。
// addIDCT4Plane applies the inverse DCT of one 4x4 block and adds the result
// to the plane at (x, y).
func (d *decoder) addIDCT4Plane(p *plane, x, y, coeffBase int) {
	var m [4][4]int32
	for i := 0; i < 4; i++ {
		a := int32(d.coeff[coeffBase+0]) + int32(d.coeff[coeffBase+8])
		b := int32(d.coeff[coeffBase+0]) - int32(d.coeff[coeffBase+8])
		c := (int32(d.coeff[coeffBase+4])*idctC2)>>16 - (int32(d.coeff[coeffBase+12])*idctC1)>>16
		dd := (int32(d.coeff[coeffBase+4])*idctC1)>>16 + (int32(d.coeff[coeffBase+12])*idctC2)>>16
		m[i][0] = a + dd
		m[i][1] = b + c
		m[i][2] = b - c
		m[i][3] = a - dd
		coeffBase++
	}
	for j := 0; j < 4; j++ {
		dc := m[0][j] + 4
		a := dc + m[2][j]
		b := dc - m[2][j]
		c := (m[1][j]*idctC2)>>16 - (m[3][j]*idctC1)>>16
		dd := (m[1][j]*idctC1)>>16 + (m[3][j]*idctC2)>>16
		i0 := p.at(x+0, y+j)
		i1 := p.at(x+1, y+j)
		i2 := p.at(x+2, y+j)
		i3 := p.at(x+3, y+j)
		p.pix[i0] = clip8(int32(p.pix[i0]) + (a+dd)>>3)
		p.pix[i1] = clip8(int32(p.pix[i1]) + (b+c)>>3)
		p.pix[i2] = clip8(int32(p.pix[i2]) + (b-c)>>3)
		p.pix[i3] = clip8(int32(p.pix[i3]) + (a-dd)>>3)
	}
}

// addIDCT4DCPlane 只处理仅有 DC 系数的块：把 DC 值均匀加到 4x4 像素上。
// addIDCT4DCPlane adds a block that only has a DC coefficient.
func (d *decoder) addIDCT4DCPlane(p *plane, x, y, coeffBase int) {
	dc := (int32(d.coeff[coeffBase+0]) + 4) >> 3
	for j := 0; j < 4; j++ {
		for i := 0; i < 4; i++ {
			idx := p.at(x+i, y+j)
			p.pix[idx] = clip8(int32(p.pix[idx]) + dc)
		}
	}
}

// inverseWHT 对 Y2 块做逆 Walsh-Hamard 变换，并把得到的 DC 值分散写入 16 个亮度块。
// inverseWHT applies the inverse Walsh Hadamard transform to the Y2 block and
// scatters the resulting DC values into the 16 luma blocks.
func (d *decoder) inverseWHT() {
	const whtBase = 384
	var m [16]int32
	for i := 0; i < 4; i++ {
		a0 := int32(d.coeff[whtBase+0+i]) + int32(d.coeff[whtBase+12+i])
		a1 := int32(d.coeff[whtBase+4+i]) + int32(d.coeff[whtBase+8+i])
		a2 := int32(d.coeff[whtBase+4+i]) - int32(d.coeff[whtBase+8+i])
		a3 := int32(d.coeff[whtBase+0+i]) - int32(d.coeff[whtBase+12+i])
		m[0+i] = a0 + a1
		m[8+i] = a0 - a1
		m[4+i] = a3 + a2
		m[12+i] = a3 - a2
	}
	out := 0
	for i := 0; i < 4; i++ {
		dc := m[0+i*4] + 3
		a0 := dc + m[3+i*4]
		a1 := m[1+i*4] + m[2+i*4]
		a2 := m[1+i*4] - m[2+i*4]
		a3 := dc - m[3+i*4]
		d.coeff[out+0] = int16((a0 + a1) >> 3)
		d.coeff[out+16] = int16((a3 + a2) >> 3)
		d.coeff[out+32] = int16((a0 - a1) >> 3)
		d.coeff[out+48] = int16((a3 - a2) >> 3)
		out += 64
	}
}

// clip8 把带符号值钳制到无符号 8 位范围 [0, 255]。
// clip8 clamps a signed value to the unsigned 8 bit range [0, 255].
func clip8(v int32) byte {
	if v < 0 {
		return 0
	}
	if v > 255 {
		return 255
	}
	return byte(v)
}
