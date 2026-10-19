package vp8l

import "fmt"

const (
	maxDimension  = 1 << 14
	maxTransforms = 4
	maxDepth      = 8
	// maxHuffmanGroups 限制 meta 前缀码引用的码组数量，防止损坏的码流无节制地分配 Huffman 树。
	//
	// maxHuffmanGroups bounds the meta prefix code count so a corrupt file
	// cannot allocate an unbounded number of trees.
	maxHuffmanGroups = 1 << 16
)

// planeCodeToXY 把 120 个短距离码映射为 (x, y) 偏移；码值本身按 1 起始（索引 = 码 - 1）。
//
// planeCodeToXY maps the 120 short distance codes to a (x, y) offset.
// Codes are 1-based, so index = code - 1.
var planeCodeToXY = [120][2]int8{
	{0, 1}, {1, 0}, {1, 1}, {-1, 1}, {0, 2}, {2, 0}, {1, 2},
	{-1, 2}, {2, 1}, {-2, 1}, {2, 2}, {-2, 2}, {0, 3}, {3, 0},
	{1, 3}, {-1, 3}, {3, 1}, {-3, 1}, {2, 3}, {-2, 3}, {3, 2},
	{-3, 2}, {0, 4}, {4, 0}, {1, 4}, {-1, 4}, {4, 1}, {-4, 1},
	{3, 3}, {-3, 3}, {2, 4}, {-2, 4}, {4, 2}, {-4, 2}, {0, 5},
	{3, 4}, {-3, 4}, {4, 3}, {-4, 3}, {5, 0}, {1, 5}, {-1, 5},
	{5, 1}, {-5, 1}, {2, 5}, {-2, 5}, {5, 2}, {-5, 2}, {4, 4},
	{-4, 4}, {3, 5}, {-3, 5}, {5, 3}, {-5, 3}, {0, 6}, {6, 0},
	{1, 6}, {-1, 6}, {6, 1}, {-6, 1}, {2, 6}, {-2, 6}, {6, 2},
	{-6, 2}, {4, 5}, {-4, 5}, {5, 4}, {-5, 4}, {3, 6}, {-3, 6},
	{6, 3}, {-6, 3}, {0, 7}, {7, 0}, {1, 7}, {-1, 7}, {5, 5},
	{-5, 5}, {7, 1}, {-7, 1}, {4, 6}, {-4, 6}, {6, 4}, {-6, 4},
	{2, 7}, {-2, 7}, {7, 2}, {-7, 2}, {3, 7}, {-3, 7}, {7, 3},
	{-7, 3}, {5, 6}, {-5, 6}, {6, 5}, {-6, 5}, {8, 0}, {4, 7},
	{-4, 7}, {7, 4}, {-7, 4}, {8, 1}, {8, 2}, {6, 6}, {-6, 6},
	{8, 3}, {5, 7}, {-5, 7}, {7, 5}, {-7, 5}, {8, 4}, {6, 7},
	{-6, 7}, {7, 6}, {-7, 6}, {8, 5}, {7, 7}, {-7, 7}, {8, 6},
	{8, 7},
}

// huffmanGroup 是对一个块组编码的五棵前缀码树（绿、红、蓝、alpha 与距离）。
//
// huffmanGroup is the set of five prefix codes that codes one block group.
type huffmanGroup struct {
	green, red, blue, alpha, dist *huffmanTree
}

// decoder 持有码流读取器与递归深度计数；深度计数用于限制子图像的嵌套层数。
//
// decoder holds the bit-stream reader and a recursion depth counter; the
// depth counter bounds how deeply sub-images may nest.
type decoder struct {
	br    *bitReader
	depth int
}

// decodeImageStream 解码一幅空间编码图像：仅顶层图像可携带变换，子分辨率图像
// （预测器数据、颜色数据、meta 前缀数据与调色板）都是普通熵编码图像。
// 变换按写入顺序读取，逆变换按相反顺序应用。
//
// decodeImageStream decodes one spatially coded image. Only the top level
// image carries transforms, the sub-resolution images (predictor data, color
// data, meta prefix data and the palette) are plain entropy coded images.
// Transforms are read in stream order and applied in reverse order.
func (d *decoder) decodeImageStream(width, height int, isLevel0 bool) ([]uint32, error) {
	if width <= 0 || height <= 0 || width > maxDimension || height > maxDimension {
		return nil, errBadBitstream
	}
	if d.depth > maxDepth {
		return nil, errBadBitstream
	}
	d.depth++
	defer func() { d.depth-- }()

	var transforms []*transform
	if isLevel0 {
		for d.br.readBit() == 1 {
			if len(transforms) >= maxTransforms {
				return nil, errBadBitstream
			}
			t, err := d.readTransform(width, height)
			if err != nil {
				return nil, fmt.Errorf("transform %d: %w", len(transforms), err)
			}
			transforms = append(transforms, t)
			if t.kind == transformColorIndexing {
				// 索引打包使存储宽度按 1<<t.bits 缩小，此处还原为子图像宽度。
				// Index packing downsamples the stored image by 1<<t.bits;
				// recover the stored sub-image width here.
				width = divRoundUp(width, 1<<t.bits)
			}
		}
		if d.br.err != nil {
			return nil, d.br.err
		}
	}

	pix, err := d.decodeEntropyImage(width, height, isLevel0)
	if err != nil {
		return nil, err
	}
	for i := len(transforms) - 1; i >= 0; i-- {
		pix, err = transforms[i].inverse(pix)
		if err != nil {
			return nil, err
		}
	}
	return pix, nil
}

// readTransform 读取一条变换描述及其数据；子图像宽度按各变换的块大小缩放。
//
// readTransform reads one transform description and its data. The sub-image
// dimensions are scaled down by the transform's block size.
func (d *decoder) readTransform(width, height int) (*transform, error) {
	kind := int(d.br.readBits(2))
	t := &transform{kind: kind, xsize: width, ysize: height}
	switch kind {
	case transformPredictor, transformColor:
		t.bits = int(d.br.readBits(3)) + 2
		subW := divRoundUp(width, 1<<t.bits)
		subH := divRoundUp(height, 1<<t.bits)
		data, err := d.decodeImageStream(subW, subH, false)
		if err != nil {
			return nil, err
		}
		t.data = data
	case transformSubtractGreen:
		// 该变换不携带数据 / this transform carries no data
	case transformColorIndexing:
		numColors := int(d.br.readBits(8)) + 1
		data, err := d.decodeImageStream(numColors, 1, false)
		if err != nil {
			return nil, err
		}
		t.data = expandColorMap(data, numColors)
		t.bits = packingBits(numColors)
	}
	if d.br.err != nil {
		return nil, d.br.err
	}
	return t, nil
}

// decodeEntropyImage 解码颜色缓存信息、meta 前缀码图像与 LZ77 编码的像素流。
// 仅顶层图像可携带 meta 前缀图像，子分辨率图像是普通熵编码图像。
// 码组数量由 meta 图像中的最大索引推导，并受 maxHuffmanGroups 限制。
//
// decodeEntropyImage decodes the color cache info, the meta prefix codes and
// the LZ77 coded pixel stream. Only the top level image may carry a meta
// prefix image, the sub-resolution images are plain entropy coded images.
// The group count derives from the largest index in the meta image and is
// bounded by maxHuffmanGroups.
func (d *decoder) decodeEntropyImage(width, height int, allowMeta bool) ([]uint32, error) {
	cacheBits := 0
	if d.br.readBit() == 1 {
		cacheBits = int(d.br.readBits(4))
		if cacheBits < 1 || cacheBits > 11 {
			return nil, errBadBitstream
		}
	}

	var huffImage []uint32
	metaBits := 0
	metaWidth := 0
	if allowMeta && d.br.readBit() == 1 {
		metaBits = int(d.br.readBits(3)) + 2
		metaWidth = divRoundUp(width, 1<<metaBits)
		metaHeight := divRoundUp(height, 1<<metaBits)
		img, err := d.decodeImageStream(metaWidth, metaHeight, false)
		if err != nil {
			return nil, err
		}
		huffImage = img
	}
	if d.br.err != nil {
		return nil, d.br.err
	}

	numGroups := 1
	if huffImage != nil {
		maxIndex := 0
		for _, p := range huffImage {
			if index := int(p >> 8 & 0xffff); index > maxIndex {
				maxIndex = index
			}
		}
		numGroups = maxIndex + 1
	}
	if numGroups > maxHuffmanGroups {
		return nil, errBadBitstream
	}

	cacheSize := 0
	if cacheBits > 0 {
		cacheSize = 1 << cacheBits
	}
	greenAlphabet := 256 + 24 + cacheSize
	groups := make([]*huffmanGroup, numGroups)
	for i := 0; i < numGroups; i++ {
		g := &huffmanGroup{}
		var err error
		if g.green, err = readHuffmanCode(d.br, greenAlphabet); err != nil {
			return nil, fmt.Errorf("group %d green tree at bit %d: %w", i, d.br.bitPos(), err)
		}
		if g.red, err = readHuffmanCode(d.br, 256); err != nil {
			return nil, fmt.Errorf("group %d red tree at bit %d: %w", i, d.br.bitPos(), err)
		}
		if g.blue, err = readHuffmanCode(d.br, 256); err != nil {
			return nil, fmt.Errorf("group %d blue tree at bit %d: %w", i, d.br.bitPos(), err)
		}
		if g.alpha, err = readHuffmanCode(d.br, 256); err != nil {
			return nil, fmt.Errorf("group %d alpha tree at bit %d: %w", i, d.br.bitPos(), err)
		}
		if g.dist, err = readHuffmanCode(d.br, 40); err != nil {
			return nil, fmt.Errorf("group %d dist tree at bit %d: %w", i, d.br.bitPos(), err)
		}
		groups[i] = g
	}
	return d.decodePixels(width, height, groups, huffImage, metaBits, cacheBits)
}

// decodePixels 在图像上运行 LZ77 解码器：绿通道符号决定 literal、copy 或
// 缓存引用；copy 的重叠拷贝语义与 VP8L 规范一致（逐像素向前推进）。
//
// decodePixels runs the LZ77 decoder over the image. The green symbol selects
// a literal, a copy or a cache reference; overlapping copies advance one
// pixel at a time, matching the VP8L specification.
func (d *decoder) decodePixels(width, height int, groups []*huffmanGroup, huffImage []uint32, metaBits, cacheBits int) ([]uint32, error) {
	pix := make([]uint32, width*height)
	var cache []uint32
	if cacheBits > 0 {
		cache = make([]uint32, 1<<cacheBits)
	}
	cacheShift := uint(32 - cacheBits)
	metaWidth := 0
	if huffImage != nil {
		metaWidth = divRoundUp(width, 1<<metaBits)
	}

	br := d.br
	group := groups[0]
	blockX, blockY := -1, -1
	x, y := 0, 0
	for y < height {
		if huffImage != nil {
			bx, by := x>>metaBits, y>>metaBits
			if bx != blockX || by != blockY {
				blockX, blockY = bx, by
				index := int(huffImage[by*metaWidth+bx] >> 8 & 0xffff)
				if index >= len(groups) {
					return nil, errBadBitstream
				}
				group = groups[index]
			}
		}

		symbol, err := group.green.decode(br)
		if err != nil {
			return nil, err
		}
		switch {
		case symbol < 256:
			red, err := group.red.decode(br)
			if err != nil {
				return nil, err
			}
			blue, err := group.blue.decode(br)
			if err != nil {
				return nil, err
			}
			alpha, err := group.alpha.decode(br)
			if err != nil {
				return nil, err
			}
			p := alpha<<24 | red<<16 | symbol<<8 | blue
			pix[y*width+x] = p
			if cache != nil {
				cache[(0x1e35a7bd*p)>>cacheShift] = p
			}
			x++
			if x == width {
				x = 0
				y++
			}

		case symbol < 256+24:
			length := prefixValue(br, symbol-256)
			distSymbol, err := group.dist.decode(br)
			if err != nil {
				return nil, err
			}
			distCode := int(prefixValue(br, distSymbol))
			dist := planeCodeToDistance(width, distCode)
			pos := y*width + x
			if dist > pos || int(length) > len(pix)-pos {
				return nil, errBadBitstream
			}
			for i := uint32(0); i < length; i++ {
				p := pix[pos-dist]
				pix[pos] = p
				if cache != nil {
					cache[(0x1e35a7bd*p)>>cacheShift] = p
				}
				pos++
				x++
				if x == width {
					x = 0
					y++
				}
			}

		default:
			index := int(symbol) - (256 + 24)
			if cache == nil || index >= len(cache) {
				return nil, errBadBitstream
			}
			pix[y*width+x] = cache[index]
			x++
			if x == width {
				x = 0
				y++
			}
		}
		if br.err != nil {
			return nil, br.err
		}
	}
	return pix, nil
}

// prefixValue 解码长度或距离的前缀码及其附加位：前缀 < 4 直接返回 prefix+1，
// 否则按规范的分段表用附加位扩展。
//
// prefixValue decodes a length or distance prefix code with its extra bits:
// prefixes below 4 return prefix+1 directly, larger ones are extended per the
// specification's segmented table.
func prefixValue(br *bitReader, prefix uint32) uint32 {
	if prefix < 4 {
		return prefix + 1
	}
	extraBits := (prefix - 2) >> 1
	offset := (2 + (prefix & 1)) << extraBits
	return offset + br.readBits(uint(extraBits)) + 1
}

// planeCodeToDistance 把距离码转换为像素距离：不超过 120 的码表示当前像素
// 邻域内的 (x, y) 偏移，更大的码是整体偏移 120 后的直线距离；结果至少为 1。
//
// planeCodeToDistance converts a distance code to a pixel distance. Codes up
// to 120 address the neighborhood of the current pixel, larger codes are
// plain distances offset by 120. The result is clamped to at least 1.
func planeCodeToDistance(width, planeCode int) int {
	if planeCode > len(planeCodeToXY) {
		return planeCode - len(planeCodeToXY)
	}
	xy := planeCodeToXY[planeCode-1]
	dist := int(xy[0]) + int(xy[1])*width
	if dist < 1 {
		dist = 1
	}
	return dist
}
