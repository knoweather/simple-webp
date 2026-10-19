package vp8l

import (
	"errors"
	"math/bits"
)

// Options 是 EncodeARGB 的压缩策略选项，目前只有 Method 一个字段。
//
// Options carries the compression strategy of EncodeARGB; it currently has a
// single field, Method.
type Options struct {
	// Method 以速度换压缩率：0 只存储字面量，1（默认）追加减绿变换、颜色
	// 索引与 LZ77，2 再启用预测变换。
	// Method trades speed for size: 0 stores literals only, 1 (the default)
	// adds the subtract green transform, colour indexing and LZ77, and 2
	// also applies a predictor transform.
	Method int
}

// 编码器调参：单个后向引用与搜索窗口的像素上限、哈希表位数、哈希链探测
// 深度、最短匹配长度，以及预测块边长的对数（块边长为 2^predictorBits 像素）。
// Encoder tuning: the pixel caps for one backward reference and for the search
// window, the hash table bits, the hash chain probe depth, the minimum match
// length, and the log2 of the predictor block edge (2^predictorBits pixels).
const (
	encoderMaxMatch = 4096
	encoderWindow   = 1 << 20
	encoderHashBits = 16
	encoderMaxChain = 48
	encoderMinMatch = 3
	predictorBits   = 4
)

// errEmptyImage 报告宽高非正、或像素缓冲短于 width*height 的输入。
//
// errEmptyImage reports a non-positive dimension or a pixel buffer shorter
// than width*height.
var errEmptyImage = errors.New("vp8l: empty image")

// EncodeARGB 把 0xAARRGGBB 像素压缩为 VP8L 码流（含 5 字节流头）。宽高必须
// 为正且不超过 maxDimension，len(pix) 不得小于 width*height，否则返回
// errEmptyImage 或 ErrTooLarge；opt 为 nil 时使用默认策略（Method 1）。
//
// EncodeARGB compresses 0xAARRGGBB pixels into a VP8L bitstream, the 5-byte
// stream header included. The dimensions must be positive and within
// maxDimension and len(pix) must be at least width*height; otherwise
// EncodeARGB returns errEmptyImage or ErrTooLarge. A nil opt selects the
// default strategy (Method 1).
func EncodeARGB(pix []uint32, width, height int, opt *Options) ([]byte, error) {
	if width <= 0 || height <= 0 || len(pix) < width*height {
		return nil, errEmptyImage
	}
	if width > maxDimension || height > maxDimension {
		return nil, ErrTooLarge
	}
	method := 1
	if opt != nil {
		method = opt.Method
	}

	e := &encoder{}
	e.w.writeBits(0x2f, 8)
	e.w.writeBits(uint32(width-1), 14)
	e.w.writeBits(uint32(height-1), 14)
	e.w.writeBits(alphaUsed(pix), 1)
	e.w.writeBits(0, 3) // 流头 version 字段固定为 0 / the stream header version field is always 0

	cur := pix
	curW, curH := width, height

	if method >= 1 {
		if palette, indices, packedW, ok := colorIndex(cur, curW, curH); ok {
			e.writeColorIndexing(palette, curW, curH)
			cur, curW = indices, packedW
		} else {
			e.w.writeBits(1, 1) // 变换标志：1 表示还有变换 / transform flag: 1 means a transform follows
			e.w.writeBits(transformSubtractGreen, 2)
			cur = subtractGreen(cur)
			if method >= 2 {
				cur = e.writePredictor(cur, curW, curH)
			}
		}
	}
	e.w.writeBits(0, 1) // 变换列表结束：0 表示没有更多变换 / end of the transform list: 0 means no more transforms

	e.writeEntropyImage(cur, curW, curH, true)
	return e.w.finish(), nil
}

// alphaUsed 报告是否存在 alpha 不等于 0xff 的像素；结果写入流头的
// alpha_used 位。
//
// alphaUsed reports whether any pixel is not fully opaque (alpha != 0xff);
// the result fills the alpha_used bit of the stream header.
func alphaUsed(pix []uint32) uint32 {
	for _, p := range pix {
		if p>>24 != 0xff {
			return 1
		}
	}
	return 0
}

// subtractGreen 返回减绿变换后的像素：R、B 两通道各自减去 G（模 256），
// A 与 G 保持不变。这一无损预处理缩小了残差的数值范围，便于后续熵编码。
//
// subtractGreen applies the subtract green transform: channels R and B each
// have G subtracted (mod 256) while A and G stay unchanged. The lossless
// pre-pass narrows the residual range seen by the entropy coder.
func subtractGreen(pix []uint32) []uint32 {
	out := make([]uint32, len(pix))
	for i, p := range pix {
		g := p >> 8 & 0xff
		r := (p>>16&0xff - g) & 0xff
		b := (p&0xff - g) & 0xff
		out[i] = p&0xff00ff00 | r<<16 | b
	}
	return out
}

// subPixels 把两个 ARGB 像素逐通道按模 256 相减。四个通道必须一个一个
// 处理：若把两个通道打包进一次减法，借位会从蓝色通道泄漏进红色通道。
//
// subPixels subtracts two ARGB pixels channel by channel, modulo 256. The
// channels are handled one by one: packing two of them into one subtraction
// would let a borrow leak from blue into red.
func subPixels(a, b uint32) uint32 {
	var out uint32
	for shift := uint(0); shift < 32; shift += 8 {
		v := (a>>shift&0xff - b>>shift&0xff) & 0xff
		out |= v << shift
	}
	return out
}

// colorIndex 构造至多 256 色的调色板与打包后的索引图。像素总数不足 32、
// 或颜色种类超过 256 时放弃索引并返回 ok=false，由调用方回退到其他变换。
// 成功时索引按 bitsPerPixel（1/2/4/8）打包进绿色通道，packedW 为打包后的行宽。
//
// colorIndex builds a palette of at most 256 colours and the packed indices.
// It gives up and returns ok=false when the image has fewer than 32 pixels or
// more than 256 distinct colours, so the caller can fall back to the other
// transforms. On success the indices are packed into the green channel at
// bitsPerPixel (1/2/4/8) and packedW is the packed row width.
func colorIndex(pix []uint32, width, height int) (palette []uint32, indices []uint32, packedW int, ok bool) {
	if width*height < 32 {
		return nil, nil, 0, false
	}
	seen := make(map[uint32]int, 256)
	palette = make([]uint32, 0, 256)
	idx := make([]uint32, len(pix))
	for i, p := range pix {
		k, found := seen[p]
		if !found {
			if len(palette) >= 256 {
				return nil, nil, 0, false
			}
			k = len(palette)
			seen[p] = k
			palette = append(palette, p)
		}
		idx[i] = uint32(k)
	}
	bitsPerPixel := 8
	switch {
	case len(palette) <= 2:
		bitsPerPixel = 1
	case len(palette) <= 4:
		bitsPerPixel = 2
	case len(palette) <= 16:
		bitsPerPixel = 4
	}
	pixelsPerByte := 8 / bitsPerPixel
	packedW = divRoundUp(width, pixelsPerByte)
	out := make([]uint32, packedW*height)
	for y := 0; y < height; y++ {
		for x := 0; x < width; x++ {
			shift := uint(x&(pixelsPerByte-1)) * uint(bitsPerPixel)
			packed := out[y*packedW+x/pixelsPerByte]
			packed |= idx[y*width+x] << shift
			out[y*packedW+x/pixelsPerByte] = packed
		}
	}
	// 打包后的索引字节放进绿色通道传输 / the packed bytes are carried in the green channel
	for i := range out {
		out[i] <<= 8
	}
	return palette, out, packedW, true
}

// writeColorIndexing 写出颜色索引变换：先写变换类型与调色板大小减一，
// 再把经差分编码的调色板作为一张单行熵编码图像写出。
//
// writeColorIndexing writes the colour indexing transform and its palette:
// the transform type and the palette size minus one come first, then the
// delta-coded palette is written as a one-row entropy coded image.
func (e *encoder) writeColorIndexing(palette []uint32, width, height int) {
	e.w.writeBits(1, 1)
	e.w.writeBits(transformColorIndexing, 2)
	e.w.writeBits(uint32(len(palette)-1), 8)

	// 调色板逐项差分编码，通道从 alpha 起算 / the palette is delta coded, alpha first
	delta := make([]uint32, len(palette))
	var prev uint32
	for i, c := range palette {
		delta[i] = subPixels(c, prev)
		prev = c
	}
	e.writeEntropyImage(delta, len(palette), 1, false)
}

// writePredictor 写出预测变换并返回残差图。图像划分为 2^predictorBits
// 见方的块，每块从固定候选模式中按残差绝对值之和选最优预测模式；模式图本身
// 作为一张熵编码图像写出，逐像素的预测残差则交给调用方继续熵编码。
//
// writePredictor writes the predictor transform and returns the residuals.
// The image is split into 2^predictorBits-square blocks; each block picks its
// predictor mode from a fixed candidate set by the summed absolute residual.
// The mode image is written as its own entropy coded image, and the per-pixel
// prediction residuals are returned to the caller for entropy coding.
func (e *encoder) writePredictor(pix []uint32, width, height int) []uint32 {
	blockW := divRoundUp(width, 1<<predictorBits)
	blockH := divRoundUp(height, 1<<predictorBits)
	modes := make([]uint32, blockW*blockH)
	residual := make([]uint32, len(pix))

	for by := 0; by < blockH; by++ {
		for bx := 0; bx < blockW; bx++ {
			bestMode, bestCost := 1, -1
			for _, mode := range [...]int{0, 1, 2, 7, 11, 12, 13} {
				cost := 0
				for y := by << predictorBits; y < (by+1)<<predictorBits && y < height; y++ {
					for x := bx << predictorBits; x < (bx+1)<<predictorBits && x < width; x++ {
						r := subPixels(pix[y*width+x], predictorAt(pix, width, height, x, y, mode))
						cost += int(r>>24&0xff) + int(r>>16&0xff) + int(r>>8&0xff) + int(r&0xff)
					}
				}
				if bestCost < 0 || cost < bestCost {
					bestCost, bestMode = cost, mode
				}
			}
			modes[by*blockW+bx] = uint32(bestMode)
		}
	}

	// 模式图把模式号放进绿色通道 / the predictor image stores the mode in the green channel
	predImage := make([]uint32, len(modes))
	for i, m := range modes {
		predImage[i] = 0xff000000 | m<<8
	}

	e.w.writeBits(1, 1)
	e.w.writeBits(transformPredictor, 2)
	e.w.writeBits(predictorBits-2, 3)
	e.writeEntropyImage(predImage, blockW, blockH, false)

	for y := 0; y < height; y++ {
		for x := 0; x < width; x++ {
			mode := int(modes[(y>>predictorBits)*blockW+(x>>predictorBits)])
			pred := predictorAt(pix, width, height, x, y, mode)
			residual[y*width+x] = subPixels(pix[y*width+x], pred)
		}
	}
	return residual
}

// predictorAt 按解码器相同的规则计算单个像素的预测值：原点取不透明黑，
// 首行、首列分别退化为左邻、上邻；最右列的 topRight 取本行首像素
// （VP8L 规范的回绕规则）。
//
// predictorAt computes the predicted value of one pixel with the same rules
// the decoder uses: the origin is opaque black, the first row and the first
// column degrade to the left and top neighbours, and on the rightmost column
// topRight wraps to the first pixel of the same row (the VP8L wrap-around rule).
func predictorAt(pix []uint32, width, height, x, y, mode int) uint32 {
	if x == 0 && y == 0 {
		return 0xff000000
	}
	if y == 0 {
		return pix[y*width+x-1]
	}
	if x == 0 {
		return pix[(y-1)*width]
	}
	left := pix[y*width+x-1]
	top := pix[(y-1)*width+x]
	topLeft := pix[(y-1)*width+x-1]
	topRight := pix[(y-1)*width+x+1]
	if x == width-1 {
		topRight = pix[y*width]
	}
	return predict(mode, left, top, topLeft, topRight)
}

// writeEntropyImage 写出一张熵编码图像：颜色缓存标志（本编码器恒不使用）、
// 五组前缀码（绿、红、蓝、码长、距离）以及 LZ77 切分后的像素流。level0 为
// 真时（流的最外层图像）再写一位，声明不使用元前缀图像。
//
// writeEntropyImage writes one entropy coded image: the colour cache flag
// (never used by this encoder), the five prefix codes (green, red, blue, code
// length and distance) and the LZ77 coded pixels. When level0 is true (the
// outermost image of the stream) it also writes the bit that disables the meta
// prefix image.
func (e *encoder) writeEntropyImage(pix []uint32, width, height int, level0 bool) {
	e.w.writeBits(0, 1) // 不使用颜色缓存 / no colour cache
	if level0 {
		e.w.writeBits(0, 1) // 不使用元前缀图像 / no meta prefix image
	}

	tokens := lz77(pix)
	var freq [5][]uint32
	freq[0] = make([]uint32, 256+24)
	freq[1] = make([]uint32, 256)
	freq[2] = make([]uint32, 256)
	freq[3] = make([]uint32, 256)
	freq[4] = make([]uint32, 40)
	for _, t := range tokens {
		if t.length == 0 {
			freq[0][t.literal>>8&0xff]++
			freq[1][t.literal>>16&0xff]++
			freq[2][t.literal&0xff]++
			freq[3][t.literal>>24&0xff]++
			continue
		}
		prefix, _, _ := encodePrefix(t.length)
		freq[0][256+prefix]++
		dprefix, _, _ := encodePrefix(t.dist + 120)
		freq[4][dprefix]++
	}

	var trees [5]huffmanCode
	for i := range freq {
		trees[i] = newHuffmanCode(freq[i], 15)
		e.writeHuffmanCode(trees[i])
	}

	for _, t := range tokens {
		if t.length == 0 {
			e.writeSymbol(trees[0], t.literal>>8&0xff)
			e.writeSymbol(trees[1], t.literal>>16&0xff)
			e.writeSymbol(trees[2], t.literal&0xff)
			e.writeSymbol(trees[3], t.literal>>24&0xff)
			continue
		}
		prefix, extra, n := encodePrefix(t.length)
		e.writeSymbol(trees[0], 256+prefix)
		e.w.writeBits(extra, n)
		dprefix, dextra, dn := encodePrefix(t.dist + 120)
		e.writeSymbol(trees[4], dprefix)
		e.w.writeBits(dextra, dn)
	}
}

// token 是 LZ77 的一步：length 为零时 literal 保存一个字面像素，否则
// (length, dist) 表示从 dist 个像素之前的位置复制 length 个像素。
//
// token is one LZ77 step: a literal when length is zero (literal holds the
// pixel value), a backward reference otherwise: (length, dist) copies length
// pixels from dist pixels back.
type token struct {
	literal uint32
	length  int
	dist    int
}

// lz77 把像素序列切成字面量与后向引用。哈希表以连续三个像素的混合值索引，
// 每个位置最多沿冲突链探测 encoderMaxChain 个候选，且匹配长度至少为
// encoderMinMatch 才成立；被匹配覆盖的每个位置同样入表，让后续位置能够
// 延续出更长的匹配。
//
// lz77 turns the pixels into literals and backward references. The hash table
// is keyed by a mix of three consecutive pixels; at most encoderMaxChain chain
// candidates are probed per position and a run needs at least encoderMinMatch
// pixels to count. Every position covered by a match is inserted as well, so
// later positions can extend it into a longer match.
func lz77(pix []uint32) []token {
	n := len(pix)
	out := make([]token, 0, n)
	head := make([]int32, 1<<encoderHashBits)
	for i := range head {
		head[i] = -1
	}
	prev := make([]int32, n)

	hash := func(i int) uint32 {
		h := pix[i]*0x1e35a7bd + pix[i+1]*0x85ebca6b + pix[i+2]*0xc2b2ae35
		return h >> (32 - encoderHashBits)
	}

	for i := 0; i < n; {
		bestLen, bestDist := 0, 0
		if i+encoderMinMatch <= n {
			h := hash(i)
			chain := 0
			for j := head[h]; j >= 0 && chain < encoderMaxChain; j = prev[j] {
				dist := i - int(j)
				if dist > encoderWindow {
					break
				}
				maxLen := n - i
				if maxLen > encoderMaxMatch {
					maxLen = encoderMaxMatch
				}
				l := 0
				for l < maxLen && pix[int(j)+l] == pix[i+l] {
					l++
				}
				if l > bestLen {
					bestLen, bestDist = l, dist
					if l >= maxLen {
						break
					}
				}
				chain++
			}
		}
		if bestLen >= encoderMinMatch {
			out = append(out, token{length: bestLen, dist: bestDist})
			for k := 0; k < bestLen; k++ {
				if i+k+encoderMinMatch <= n {
					h := hash(i + k)
					prev[i+k] = head[h]
					head[h] = int32(i + k)
				}
			}
			i += bestLen
			continue
		}
		out = append(out, token{literal: pix[i]})
		if i+encoderMinMatch <= n {
			h := hash(i)
			prev[i] = head[h]
			head[h] = int32(i)
		}
		i++
	}
	return out
}

// encodePrefix 把长度或距离值拆成 VP8L 的变长编码：前缀码、附加位数值与
// 附加位数；v-1 小于 4 时全部落在前缀码内，不带附加位。
//
// encodePrefix splits a length or distance value into its VP8L variable length
// code: the prefix code, the extra bits value and the number of extra bits.
// Values with v-1 < 4 fit in the prefix alone and carry no extra bits.
func encodePrefix(v int) (prefix uint32, extra uint32, nbits uint) {
	e := v - 1
	if e < 4 {
		return uint32(e), 0, 0
	}
	b := bits.Len(uint(e)) - 2
	bit := uint32(e>>b) & 1
	prefix = uint32(2+2*b) + bit
	offset := (2 + int(bit)) << b
	return prefix, uint32(e - offset), uint(b)
}

/*
--------------------------------------------------------------------------------------
Huffman coding / Huffman 编码
--------------------------------------------------------------------------------------
*/

// huffmanCode 是可直接写出的规范前缀码：码长、预计算的规范码字，以及
// “码表是否只有一个符号”的标记。
//
// huffmanCode is a canonical prefix code ready for writing: the code lengths,
// the precomputed canonical code words, and the flag telling whether the code
// has a single symbol.
type huffmanCode struct {
	lengths []uint32
	codes   []uint32
	// single 为真表示码表只有一个符号，写出该符号不消耗比特
	// single is true when the code has one symbol and therefore costs no bits
	single bool
}

// newHuffmanCode 由符号频次构造限长 Huffman 码，并预计算规范码字。
//
// newHuffmanCode builds a length-limited Huffman code from the symbol
// frequencies and precomputes the canonical code words.
func newHuffmanCode(freq []uint32, limit int) huffmanCode {
	lengths := buildLengths(freq, limit)
	codes := canonicalCodes(lengths)
	h := huffmanCode{lengths: lengths, codes: codes}
	count := 0
	for _, l := range lengths {
		if l > 0 {
			count++
		}
	}
	h.single = count == 1
	return h
}

// writeSymbol 写出一个符号：码表只有一个符号时（解码端可自行推断）以及
// 符号越界时（防御性保护）都不写任何比特。
//
// writeSymbol writes one symbol; it emits no bits when the code has a single
// symbol (the decoder can infer it) and when the symbol is out of range (a
// defensive guard).
func (e *encoder) writeSymbol(h huffmanCode, sym uint32) {
	if h.single || int(sym) >= len(h.codes) {
		return
	}
	e.w.writeCode(h.codes[sym], h.lengths[sym])
}

// buildLengths 按符号频次构造限长码长：先建最优 Huffman 树并以深度优先
// 遍历按深度赋予码长，树深超过 limit 时回退到 balancedLengths。频次为零的
// 符号不分配码长；频次全为零时退化为给符号 0 一个 1 位码。
//
// buildLengths returns length limited code lengths for the symbol
// frequencies: it first builds the optimal Huffman tree and assigns the
// lengths by a depth first walk, falling back to balancedLengths when the tree
// is deeper than limit. Symbols with a zero frequency get no code; an all-zero
// frequency vector degenerates to a one-bit code on symbol 0.
func buildLengths(freq []uint32, limit int) []uint32 {
	lengths := make([]uint32, len(freq))
	type leaf struct {
		freq  uint32
		sym   int
		left  int
		right int
	}
	var nodes []leaf
	var live []int
	for sym, f := range freq {
		if f > 0 {
			nodes = append(nodes, leaf{freq: f, sym: sym, left: -1, right: -1})
			live = append(live, len(nodes)-1)
		}
	}
	if len(live) == 0 {
		lengths[0] = 1
		return lengths
	}
	if len(live) == 1 {
		lengths[nodes[live[0]].sym] = 1
		return lengths
	}

	// 反复合并活动集合中频次最小的两个节点 / repeatedly merge the two least frequent nodes
	for len(live) > 1 {
		// 找出当前频次最小的两个节点 / find the two smallest
		a, b := 0, 1
		if nodes[live[b]].freq < nodes[live[a]].freq {
			a, b = b, a
		}
		for i := 2; i < len(live); i++ {
			if f := nodes[live[i]].freq; f < nodes[live[a]].freq {
				b = a
				a = i
			} else if f < nodes[live[b]].freq {
				b = i
			}
		}
		na, nb := live[a], live[b]
		nodes = append(nodes, leaf{freq: nodes[na].freq + nodes[nb].freq, sym: -1, left: na, right: nb})
		merged := len(nodes) - 1
		// 从活动集合移除 a、b（a 下标更小纯属巧合）/ drop a and b (a being the smaller index is only by chance)
		if a > b {
			a, b = b, a
		}
		live = append(live[:b], live[b+1:]...)
		live = append(live[:a], live[a+1:]...)
		live = append(live, merged)
	}

	// 深度优先遍历按深度赋予码长 / the depth first walk assigns the code lengths
	var walk func(idx, depth int)
	maxDepth := 0
	walk = func(idx, depth int) {
		if nodes[idx].sym >= 0 {
			lengths[nodes[idx].sym] = uint32(depth)
			if depth > maxDepth {
				maxDepth = depth
			}
			return
		}
		walk(nodes[idx].left, depth+1)
		walk(nodes[idx].right, depth+1)
	}
	walk(live[0], 0)

	if maxDepth > limit {
		return balancedLengths(freq, limit)
	}
	return lengths
}

// balancedLengths 构造只含 limit-1 与 limit 两种码长的完备码，用于最优树
// 深超过格式上限的情形。频次最高的符号分到较短的码长，两种长度的符号个数
// 按完备性条件分配。
//
// balancedLengths builds a complete code whose lengths are only limit-1 and
// limit, used when the optimal tree is deeper than the format allows. The most
// frequent symbols get the shorter length, and the split between the two
// lengths keeps the code complete.
func balancedLengths(freq []uint32, limit int) []uint32 {
	lengths := make([]uint32, len(freq))
	type symFreq struct {
		sym  int
		freq uint32
	}
	var syms []symFreq
	for sym, f := range freq {
		if f > 0 {
			syms = append(syms, symFreq{sym, f})
		}
	}
	if len(syms) == 0 {
		lengths[0] = 1
		return lengths
	}
	l := bits.Len(uint(len(syms) - 1))
	if l > limit {
		l = limit
	}
	if l < 1 {
		l = 1
	}
	// 频次最高的符号分到较短的码长 / the most frequent symbols get the shorter code
	for i := 0; i < len(syms); i++ {
		for j := i + 1; j < len(syms); j++ {
			if syms[j].freq > syms[i].freq {
				syms[i], syms[j] = syms[j], syms[i]
			}
		}
	}
	short := 1<<l - len(syms)
	if short < 0 {
		short = 0
	}
	if short > len(syms) {
		short = len(syms)
	}
	for i, s := range syms {
		if i < short {
			lengths[s.sym] = uint32(l - 1)
		} else {
			lengths[s.sym] = uint32(l)
		}
	}
	return lengths
}

// canonicalCodes 按码长为每个符号分配规范码字：同一码长组内的符号按符号
// 升序编号，码字连续递增。
//
// canonicalCodes assigns the canonical code of every symbol from its length:
// symbols sharing one length are numbered in ascending symbol order with
// consecutive code words.
func canonicalCodes(lengths []uint32) []uint32 {
	var count [16]uint32
	for _, l := range lengths {
		if l > 0 && l < 16 {
			count[l]++
		}
	}
	var next [16]uint32
	var code uint32
	for l := 1; l < 16; l++ {
		code = (code + count[l-1]) << 1
		next[l] = code
	}
	codes := make([]uint32, len(lengths))
	for sym, l := range lengths {
		if l > 0 && l < 16 {
			codes[sym] = next[l]
			next[l]++
		}
	}
	return codes
}

// writeHuffmanCode 写出一份前缀码描述：至多两个符号且都小于 256 时走
// simple code 快速路径；否则以码长码（code length code）对码长做游程
// 编码后写出完整码表。
//
// writeHuffmanCode writes one prefix code description: codes with at most two
// symbols, all below 256, take the simple code fast path; everything else is
// written in full, with the code lengths run-length encoded through the code
// length code.
func (e *encoder) writeHuffmanCode(h huffmanCode) {
	count := 0
	last := 0
	for sym, l := range h.lengths {
		if l > 0 {
			count++
			last = sym
		}
	}
	if count <= 2 && last < 256 {
		// 简单码：一或两个符号且都小于 256 / simple code: one or two symbols, both below 256
		e.w.writeBits(1, 1)
		e.w.writeBits(uint32(count-1), 1)
		first := uint32(0)
		for sym, l := range h.lengths {
			if l > 0 {
				first = uint32(sym)
				break
			}
		}
		if first > 1 {
			e.w.writeBits(1, 1) // 首符号大于 1，需要八位编码 / the first symbol needs eight bits
			e.w.writeBits(first, 8)
		} else {
			e.w.writeBits(0, 1)
			e.w.writeBits(first, 1)
		}
		if count == 2 {
			e.w.writeBits(uint32(last), 8)
		}
		return
	}

	// 普通码：用码长码编码全部码长 / normal code: encode the code lengths with the code length code
	type clToken struct {
		sym   uint32
		extra uint32
		nbits uint
	}
	var seq []clToken
	for sym := 0; sym < len(h.lengths); {
		l := h.lengths[sym]
		run := 1
		for sym+run < len(h.lengths) && h.lengths[sym+run] == l {
			run++
		}
		sym += run
		if l == 0 {
			for run >= 11 {
				n := run
				if n > 138 {
					n = 138
				}
				seq = append(seq, clToken{sym: 18, extra: uint32(n - 11), nbits: 7})
				run -= n
			}
			if run >= 3 {
				n := run
				if n > 10 {
					n = 10
				}
				seq = append(seq, clToken{sym: 17, extra: uint32(n - 3), nbits: 3})
				run -= n
			}
			for ; run > 0; run-- {
				seq = append(seq, clToken{sym: 0})
			}
			continue
		}
		seq = append(seq, clToken{sym: l})
		run--
		for run >= 3 {
			n := run
			if n > 6 {
				n = 6
			}
			seq = append(seq, clToken{sym: 16, extra: uint32(n - 3), nbits: 2})
			run -= n
		}
		for ; run > 0; run-- {
			seq = append(seq, clToken{sym: l})
		}
	}

	var clFreq [19]uint32
	for _, t := range seq {
		clFreq[t.sym]++
	}
	clLengths := buildLengths(clFreq[:], 7)
	clCodes := canonicalCodes(clLengths)

	e.w.writeBits(0, 1) // 0 = 普通码 / 0 = normal code
	e.w.writeBits(19-4, 4)
	for i := 0; i < 19; i++ {
		e.w.writeBits(clLengths[kCodeLengthCodeOrder[i]], 3)
	}
	e.w.writeBits(0, 1) // 不写显式最大符号 / no explicit max symbol

	singleCL := true
	for sym, l := range clLengths {
		if l > 0 && sym != int(clLengths[0]) {
			singleCL = false
		}
	}
	for _, t := range seq {
		if !singleCL {
			e.w.writeCode(clCodes[t.sym], clLengths[t.sym])
		}
		if t.nbits > 0 {
			e.w.writeBits(t.extra, t.nbits)
		}
	}
}

/*
--------------------------------------------------------------------------------------
Bit writer / 比特写入器
--------------------------------------------------------------------------------------
*/

// encoder 汇集编码过程产生的全部输出比特。
//
// encoder collects the output bits produced during encoding.
type encoder struct {
	w bitWriter
}

// bitWriter 按 VP8L 的要求以最低有效位优先写比特。
//
// bitWriter writes bits least significant bit first, as VP8L requires.
type bitWriter struct {
	buf  []byte
	bits uint64
	n    uint
}

// writeBits 把 v 的低 n 位按最低有效位优先追加进输出，n 为 0 时什么都不写。
//
// writeBits appends the low n bits of v, least significant bit first; n == 0
// writes nothing.
func (w *bitWriter) writeBits(v uint32, n uint) {
	if n == 0 {
		return
	}
	w.bits |= uint64(v&(1<<n-1)) << w.n
	w.n += n
	for w.n >= 8 {
		w.buf = append(w.buf, byte(w.bits))
		w.bits >>= 8
		w.n -= 8
	}
}

// writeCode 按最高有效位优先写出一个 n 位的规范前缀码；码字本身不含长度
// 信息，长度由所属码表另行描述。
//
// writeCode writes an n-bit canonical prefix code, most significant bit first;
// the code word carries no length of its own, the length comes from the code
// table it belongs to.
func (w *bitWriter) writeCode(code uint32, n uint32) {
	for i := int(n) - 1; i >= 0; i-- {
		w.writeBits(code>>uint(i)&1, 1)
	}
}

// bitPos 返回迄今为止写出的比特总数（含尚未凑满一个字节的余位）。
//
// bitPos returns the number of bits written so far, including the trailing
// bits that do not yet fill a byte.
func (w *bitWriter) bitPos() int {
	return len(w.buf)*8 + int(w.n)
}

// finish 把末尾不足一个字节的余位补零成整字节，并返回输出缓冲区。
//
// finish zero-pads the trailing partial byte and returns the output buffer.
func (w *bitWriter) finish() []byte {
	if w.n > 0 {
		w.buf = append(w.buf, byte(w.bits))
		w.bits, w.n = 0, 0
	}
	return w.buf
}
