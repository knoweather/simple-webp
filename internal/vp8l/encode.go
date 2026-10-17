package vp8l

import (
	"errors"
	"math/bits"
)

// Encoder options.
type Options struct {
	// Method trades speed for size: 0 stores literals only, 1 (the default)
	// adds the subtract green transform, colour indexing and LZ77, 2 also
	// applies a predictor transform.
	Method int
}

const (
	encoderMaxMatch = 4096
	encoderWindow   = 1 << 20
	encoderHashBits = 16
	encoderMaxChain = 48
	encoderMinMatch = 3
	predictorBits   = 4
)

var errEmptyImage = errors.New("vp8l: empty image")

// EncodeARGB compresses 0xAARRGGBB pixels into a VP8L bitstream, header
// included.
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
	e.w.writeBits(0, 3) // version

	cur := pix
	curW, curH := width, height

	if method >= 1 {
		if palette, indices, packedW, ok := colorIndex(cur, curW, curH); ok {
			e.writeColorIndexing(palette, curW, curH)
			cur, curW = indices, packedW
		} else {
			e.w.writeBits(1, 1) // transform present
			e.w.writeBits(transformSubtractGreen, 2)
			cur = subtractGreen(cur)
			if method >= 2 {
				cur = e.writePredictor(cur, curW, curH)
			}
		}
	}
	e.w.writeBits(0, 1) // end of the transform list

	e.writeEntropyImage(cur, curW, curH, true)
	return e.w.finish(), nil
}

// alphaUsed reports whether any pixel is not fully opaque.
func alphaUsed(pix []uint32) uint32 {
	for _, p := range pix {
		if p>>24 != 0xff {
			return 1
		}
	}
	return 0
}

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

// colorIndex builds a palette of at most 256 colours and the packed indices.
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
	// the packed bytes are carried in the green channel
	for i := range out {
		out[i] <<= 8
	}
	return palette, out, packedW, true
}

// writeColorIndexing writes the colour indexing transform and its palette.
func (e *encoder) writeColorIndexing(palette []uint32, width, height int) {
	e.w.writeBits(1, 1)
	e.w.writeBits(transformColorIndexing, 2)
	e.w.writeBits(uint32(len(palette)-1), 8)

	// the palette is delta coded, alpha first
	delta := make([]uint32, len(palette))
	var prev uint32
	for i, c := range palette {
		delta[i] = subPixels(c, prev)
		prev = c
	}
	e.writeEntropyImage(delta, len(palette), 1, false)
}

// writePredictor writes the predictor transform and returns the residuals.
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

	// the predictor image stores the mode in the green channel
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

// predictorAt computes the predicted value of one pixel with the same rules
// the decoder uses.
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

// writeEntropyImage writes one entropy coded image: the colour cache flag,
// the five prefix codes and the LZ77 coded pixels.
func (e *encoder) writeEntropyImage(pix []uint32, width, height int, level0 bool) {
	e.w.writeBits(0, 1) // no colour cache
	if level0 {
		e.w.writeBits(0, 1) // no meta prefix image
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

// token is one LZ77 step: a literal when length is zero, a backward
// reference otherwise.
type token struct {
	literal uint32
	length  int
	dist    int
}

// lz77 turns the pixels into literals and backward references.
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

// encodePrefix splits a length or distance value into its prefix code, the
// extra bits value and the number of extra bits.
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
Huffman coding
--------------------------------------------------------------------------------------
*/

// huffmanCode is a canonical prefix code ready for writing.
type huffmanCode struct {
	lengths []uint32
	codes   []uint32
	// single is true when the code has one symbol and therefore costs no bits
	single bool
}

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

func (e *encoder) writeSymbol(h huffmanCode, sym uint32) {
	if h.single || int(sym) >= len(h.codes) {
		return
	}
	e.w.writeCode(h.codes[sym], h.lengths[sym])
}

// buildLengths returns length limited code lengths for the symbol
// frequencies. Symbols with a zero frequency get no code.
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

	// repeatedly merge the two least frequent nodes
	for len(live) > 1 {
		// find the two smallest
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
		// drop a and b (a is smaller index only by chance)
		if a > b {
			a, b = b, a
		}
		live = append(live[:b], live[b+1:]...)
		live = append(live[:a], live[a+1:]...)
		live = append(live, merged)
	}

	// depth first walk assigns the code lengths
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

// balancedLengths builds a complete code with lengths limit-1 and limit, used
// when the optimal tree is deeper than the format allows.
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
	// the most frequent symbols get the shorter code
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

// canonicalCodes assigns the canonical code of every symbol.
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

// writeHuffmanCode writes one prefix code description.
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
		// simple code: one or two symbols, both below 256
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
			e.w.writeBits(1, 1) // the first symbol needs eight bits
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

	// normal code: encode the code lengths with the code length code
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

	e.w.writeBits(0, 1) // normal code
	e.w.writeBits(19-4, 4)
	for i := 0; i < 19; i++ {
		e.w.writeBits(clLengths[kCodeLengthCodeOrder[i]], 3)
	}
	e.w.writeBits(0, 1) // no explicit max symbol

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
Bit writer
--------------------------------------------------------------------------------------
*/

// encoder collects the output bits.
type encoder struct {
	w bitWriter
}

// bitWriter writes bits least significant bit first, as VP8L requires.
type bitWriter struct {
	buf  []byte
	bits uint64
	n    uint
}

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

// writeCode writes a canonical prefix code, most significant bit first.
func (w *bitWriter) writeCode(code uint32, n uint32) {
	for i := int(n) - 1; i >= 0; i-- {
		w.writeBits(code>>uint(i)&1, 1)
	}
}

// bitPos returns the number of bits written so far.
func (w *bitWriter) bitPos() int {
	return len(w.buf)*8 + int(w.n)
}

func (w *bitWriter) finish() []byte {
	if w.n > 0 {
		w.buf = append(w.buf, byte(w.bits))
		w.bits, w.n = 0, 0
	}
	return w.buf
}
