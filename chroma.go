package simple_webp

// 色度上采样的行级路径。
//
// Row-level chroma upsampling.
//
// 解码一个行带时，色度行指针（k=y>>1 与 clamp(k±1)）在同一行内是常量，
// 因此按行预算是纯赚：原先每个像素要做 8 次带钳位的 chromaAt 查找
// （水平 + 垂直各 4 次），现在每行只算两次行指针，且只在行两端钳位。
//
// While decoding one row band the chroma row pointers (k=y>>1 and
// clamp(k±1)) are constant across the row, so resolving them once per row is
// a pure win: the old code did eight clamped chromaAt lookups per pixel (four
// horizontal plus four vertical); now only the two row ends need clamping.
//
// 向量化的落点也在这里：两端的钳位（j=0 与 j=cw-1）交给标量，中间部分按
// 8 个色度样本一组交给 SIMD 内核；chroma_test.go 用未重构的 upsampleChroma
// 逐位把关。
//
// Vectorisation plugs in at the same seam: the two clamped ends (j=0 and
// j=cw-1) stay scalar while the interior runs through a SIMD kernel eight
// chroma samples at a time, with chroma_test.go pinning the result bit for
// bit against the un-refactored upsampleChroma.

// upsampleChromaRowScalar 用标量路径产生 dst[x0:x1] 的上采样色度，
// 钳位规则与 chromaAt 一致。
//
// upsampleChromaRowScalar produces the upsampled chroma for dst[x0:x1] on the
// scalar path, clamping exactly like chromaAt.
func upsampleChromaRowScalar(dst, centerRow, vertRow []byte, cw, x0, x1 int) {
	for x := x0; x < x1; x++ {
		j := x >> 1
		jh := j + 1
		if x&1 == 0 {
			jh = j - 1
		}
		if jh < 0 {
			jh = 0
		} else if jh >= cw {
			jh = cw - 1
		}
		c := int(centerRow[j])
		h := int(centerRow[jh])
		v := int(vertRow[j])
		d := int(vertRow[jh])
		// 最大 (9*255+3*255+3*255+255+8)>>4 = 255，故 byte 足够。
		// The maximum is (9*255+3*255+3*255+255+8)>>4 = 255, so a byte fits.
		dst[x] = byte((9*c + 3*h + 3*v + d + 8) >> 4)
	}
}

// chromaRowPair 返回输出行 y 对应的中心色度行（k=y>>1）与垂直邻居行
// （clamp(k+dk)）切片。偶数 y 取上一行，奇数 y 取下一行。
//
// chromaRowPair returns the centre chroma row (k=y>>1) and the vertical
// neighbour row (clamp(k+dk)) slices for the output row y: even y looks one
// row up, odd y one row down.
func chromaRowPair(p []byte, stride, cw, ch, y int) (center, vert []byte) {
	k := y >> 1
	vk := k + 1
	if y&1 == 0 {
		vk = k - 1
	}
	if vk < 0 {
		vk = 0
	} else if vk >= ch {
		vk = ch - 1
	}
	return p[k*stride : k*stride+cw], p[vk*stride : vk*stride+cw]
}
