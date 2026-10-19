package vp8l

// maxCodeLength 是规范允许的最长哈夫曼码长（比特）。
// maxCodeLength is the longest Huffman code length (bits) allowed by the spec.
const maxCodeLength = 15

// kCodeLengthCodeOrder 是位流中存储 19 个码长编码之码长的顺序表（规范 Table 3-3）。
// kCodeLengthCodeOrder is the order in which the 19 code length code lengths
// are stored in the bitstream (spec Table 3-3).
var kCodeLengthCodeOrder = [19]int{17, 18, 0, 1, 2, 3, 4, 5, 16, 6, 7, 8, 9, 10, 11, 12, 13, 14, 15}

// huffmanTree 是一棵规范前缀码树：符号按码长计数（counts）逐位解码，
// 方式与 DEFLATE 规范码的解码完全相同。
// huffmanTree is a canonical prefix code. Codes are decoded one bit at a time
// walking the code length counts, the same way the canonical codes of DEFLATE
// are decoded.
type huffmanTree struct {
	counts  [maxCodeLength + 1]uint32
	symbols []uint16
	// single 是只有一个叶子的树所对应的符号；这种树每个符号消耗 0 比特，
	// 普通树则取值 -1。
	// single is the symbol of a tree that has only one leaf; such a tree
	// consumes zero bits per symbol. It is -1 for a regular tree.
	single int32
}

// decode 从位流中解出一个符号；遇到非法码时返回 errBadHuffmanCode。
// decode reads one symbol from the bitstream. It returns errBadHuffmanCode
// when the bits walked do not match any code.
func (h *huffmanTree) decode(br *bitReader) (uint32, error) {
	if h.single >= 0 {
		return uint32(h.single), nil
	}
	var code, first, index uint32
	for l := 1; l <= maxCodeLength; l++ {
		code |= br.readBits(1)
		count := h.counts[l]
		if code-first < count {
			return uint32(h.symbols[index+code-first]), nil
		}
		index += count
		first = (first + count) << 1
		code <<= 1
	}
	return 0, errBadHuffmanCode
}

// buildHuffmanTree 由每个符号的码长构造规范前缀码：码集必须完备，
// 唯一例外是只有一个叶子的树（该符号用 0 比特编码）。
// buildHuffmanTree builds a canonical prefix code out of per-symbol code
// lengths. The code must be complete, except for the single leaf case where
// the one symbol is coded with zero bits.
func buildHuffmanTree(lengths []uint32) (*huffmanTree, error) {
	h := &huffmanTree{single: -1}
	total := 0
	last := -1
	for sym, l := range lengths {
		if l > maxCodeLength {
			return nil, errBadHuffmanCode
		}
		if l > 0 {
			h.counts[l]++
			total++
			last = sym
		}
	}
	if total == 0 {
		return nil, errBadHuffmanCode
	}
	if total == 1 {
		h.single = int32(last)
		return h, nil
	}

	// 码集必须完备：二叉树的每个分支都要被用到（Kraft 不等式取等）。
	// The code must be complete: every branch of the binary tree is used
	// (Kraft inequality holds with equality).
	left := 1
	for l := 1; l <= maxCodeLength; l++ {
		left <<= 1
		left -= int(h.counts[l])
		if left < 0 {
			return nil, errBadHuffmanCode
		}
	}
	if left != 0 {
		return nil, errBadHuffmanCode
	}

	// symbols 先按码长、再按符号值排序，正是解码器逐位遍历所依赖的规范序。
	// Symbols are stored ordered by code length, then by symbol value, which
	// is the canonical ordering the decoder walks.
	h.symbols = make([]uint16, total)
	var offsets [maxCodeLength + 2]uint32
	for l := 1; l <= maxCodeLength; l++ {
		offsets[l+1] = offsets[l] + h.counts[l]
	}
	for sym, l := range lengths {
		if l > 0 {
			h.symbols[offsets[l]] = uint16(sym)
			offsets[l]++
		}
	}
	return h, nil
}

// readHuffmanCode 从位流读取一段前缀码描述，并为 alphabetSize 个符号的字母表
// 构建解码树。
// readHuffmanCode reads one prefix code description from the bitstream and
// builds the tree for an alphabet of alphabetSize symbols.
func readHuffmanCode(br *bitReader, alphabetSize int) (*huffmanTree, error) {
	lengths := make([]uint32, alphabetSize)

	// 简单码：一或两个符号，各用 1 比特编码。
	// Simple code: one or two symbols, each coded with one bit.
	if br.readBit() == 1 {
		numSymbols := int(br.readBits(1)) + 1
		firstIs8Bits := br.readBits(1)
		sym0 := int(br.readBits(uint(1 + 7*firstIs8Bits)))
		if sym0 >= alphabetSize {
			return nil, errBadHuffmanCode
		}
		lengths[sym0] = 1
		if numSymbols == 2 {
			sym1 := int(br.readBits(8))
			if sym1 >= alphabetSize {
				return nil, errBadHuffmanCode
			}
			lengths[sym1] = 1
		}
		return buildHuffmanTree(lengths)
	}

	// 普通码：码长本身再用一层前缀码编码。
	// Normal code: the code lengths are themselves prefix coded.
	numCodeLengths := int(br.readBits(4)) + 4
	var clLengths [19]uint32
	for i := 0; i < numCodeLengths; i++ {
		clLengths[kCodeLengthCodeOrder[i]] = br.readBits(3)
	}
	clTree, err := buildHuffmanTree(clLengths[:])
	if err != nil {
		return nil, err
	}

	maxSymbol := alphabetSize
	if br.readBit() == 1 {
		nbits := 2 + 2*int(br.readBits(3))
		maxSymbol = 2 + int(br.readBits(uint(nbits)))
		if maxSymbol > alphabetSize {
			return nil, errBadHuffmanCode
		}
	}

	// 码 16 重复上一个非零码长，17 和 18 输出连续的零。
	// Code 16 repeats the previous non zero length; 17 and 18 emit zero runs.
	prevCodeLen := uint32(8)
	symbol := 0
	for symbol < alphabetSize && maxSymbol > 0 {
		maxSymbol--
		codeLen, err := clTree.decode(br)
		if err != nil {
			return nil, err
		}
		if codeLen < 16 {
			lengths[symbol] = codeLen
			if codeLen != 0 {
				prevCodeLen = codeLen
			}
			symbol++
			continue
		}
		var repeat int
		value := uint32(0)
		switch codeLen {
		case 16:
			repeat = int(br.readBits(2)) + 3
			value = prevCodeLen
		case 17:
			repeat = int(br.readBits(3)) + 3
		case 18:
			repeat = int(br.readBits(7)) + 11
		default:
			return nil, errBadHuffmanCode
		}
		if symbol+repeat > alphabetSize {
			return nil, errBadHuffmanCode
		}
		for i := 0; i < repeat; i++ {
			lengths[symbol] = value
			symbol++
		}
	}
	if br.err != nil {
		return nil, br.err
	}
	return buildHuffmanTree(lengths)
}
