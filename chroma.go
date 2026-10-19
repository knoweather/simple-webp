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
// 行级路径也是将来向量化的自然落点：upsampleChromaRow 这一层接缝由
// chroma_test.go 用未重构的 upsampleChroma 逐位把关。
//
// The row-level path is also the natural seam for a future vectorization:
// chroma_test.go pins upsampleChromaRow bit for bit against the un-refactored
// upsampleChroma.

// upsampleChromaRow 用 9:3:3:1 "fancy" 滤波为输出行生成上采样后的色度，
// 结果写入 dst[0:w]（取值 0..255）。
//
// centerRow 是中心色度行（k=y>>1），vertRow 是垂直邻居色度行
// （k+dk 已由调用方按平面高度钳位）。水平方向的 tap 越界时钳位到
// [0, cw-1]，与 chromaAt 的行为一致。
//
// upsampleChromaRow produces the "fancy" 9:3:3:1 upsampled chroma for one
// output row into dst[0:w] (values 0..255).
//
// centerRow is the centre chroma row (k=y>>1); vertRow is the vertical
// neighbour row (k+dk already clamped to the plane height by the caller).
// Horizontal taps out of range are clamped to [0, cw-1], matching chromaAt.
//
// 色度样本与其 2x2 亮度块的左上角像素共位：块内偶数 x 用左邻居（j-1）、
// 奇数 x 用右邻居（j+1），j=x>>1。行指针已由调用方定好，这里只处理水平
// 钳位，因此内循环除两端外没有分支判断。
//
// A chroma sample is co-sited with the top left pixel of its 2x2 luma
// block: even x takes the left neighbour (j-1) and odd x the right one
// (j+1), with j=x>>1. The row pointers are already resolved by the caller,
// so only horizontal clamping remains and the inner loop is branch free
// apart from the two row ends.
func upsampleChromaRow(dst []byte, centerRow, vertRow []byte, cw, w int) {
	for x := 0; x < w; x++ {
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
