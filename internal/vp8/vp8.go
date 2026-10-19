package vp8

import (
	"errors"
	"image"
)

// 解码器返回的错误值。
// Error values returned by the decoder.
var (
	// ErrInvalid 表示码流损坏：帧头字段、分区表或熵解码数据不完整。
	// ErrInvalid reports a corrupt bitstream: truncated frame header fields,
	// a broken partition table or missing entropy coded data.
	ErrInvalid = errors.New("vp8: invalid bitstream")
	// ErrNotKeyFrame 表示输入不是关键帧（tag 的 bit0 为 1）。
	// ErrNotKeyFrame reports that the input is not a key frame (bit 0 of the tag is set).
	ErrNotKeyFrame = errors.New("vp8: not a key frame")
	// ErrTooLarge 表示帧尺寸非法或超限：宽高为 0、超过 14 位字段上限 16383，
	// 或超出 maxPixels 像素预算。
	// ErrTooLarge reports illegal or excessive frame dimensions: zero, above
	// the 14-bit field cap of 16383, or beyond the maxPixels budget.
	ErrTooLarge = errors.New("vp8: frame too large")
)

// VP8 规范定义的分段与分区上限。
// Segment and partition limits defined by the VP8 bitstream spec.
const (
	nSegment      = 4 // 分段数 / number of segments
	nSegmentProb  = 3 // segment ID 树的 3 个概率值 / the 3 tree probabilities for segment IDs
	maxPartitions = 8 // token 分区数上限 / maximum number of token partitions
)

// segmentHeader 是帧头中的分段特性数据：是否启用分段、是否更新分段图、
// 逐段的量化与滤波偏移，以及解 segment ID 用的树概率。
// segmentHeader is the segmentation feature data of the frame header:
// whether segmentation is enabled, whether the segment map gets updated,
// the per-segment quantizer and filter offsets, and the tree probabilities
// used to decode segment IDs.
type segmentHeader struct {
	useSegment    bool
	updateMap     bool
	relativeDelta bool // 偏移是相对基准还是绝对 / offsets are relative deltas or absolute
	// quantizer、filterStrength 是逐段的量化偏移与滤波强度偏移。
	// quantizer and filterStrength are the per-segment quantizer and filter strength offsets.
	quantizer      [nSegment]int8
	filterStrength [nSegment]int8
	// prob 是 segment ID 解码树的三个概率。/ prob are the three probabilities of the segment ID decode tree.
	prob [nSegmentProb]uint8
}

// filterHeader 是帧头中的环路滤波参数：滤波类型与强度、sharpness、
// 参考帧与模式对应的强度增量，以及逐段覆盖的最终强度。
// filterHeader is the loop filter part of the frame header: the filter type
// and level, the sharpness, the reference-frame and mode based level deltas,
// and the final per-segment levels derived from them.
type filterHeader struct {
	simple          bool
	level           int8
	sharpness       uint8
	useLFDelta      bool
	refLFDelta      [4]int8
	modeLFDelta     [4]int8
	perSegmentLevel [nSegment]int8
}

// quant 保存一个分段在 y1、y2、uv 三个系数平面上的 DC/AC 反量化系数。
// quant holds the DC and AC dequantization factors of one segment for the
// y1, y2 and uv coefficient planes.
type quant struct {
	y1 [2]uint16
	y2 [2]uint16
	uv [2]uint16
}

// filterParam 是施加到一个宏块的环路滤波阈值：level 为宏块外边缘强度，
// ilevel 为内边缘的判决阈值，hlevel 为高边缘方差（HEV）阈值，
// inner 表示本宏块还需要对内部边缘做滤波。
// filterParam is the loop filter threshold set applied to one macroblock:
// level is the outer edge level, ilevel the decision threshold for the
// inner edges, hlevel the high-edge-variance (HEV) threshold, and inner
// states that the macroblock's internal edges are filtered as well.
type filterParam struct {
	level, ilevel, hlevel uint8
	inner                 bool
}

// decoder 是一次性使用的 VP8 解码器：保存帧头解析结果、熵解码状态与三个
// 色度平面。Decode/DecodeLimit 每次调用都新建一个实例，因此无须同步，
// 但也绝不能并发复用同一个 decoder。
// decoder is a single-use VP8 decoder: it holds the parsed frame header, the
// entropy decoding state and the three colour planes. Decode/DecodeLimit
// create a fresh instance per call, so no locking is needed, but a decoder
// must never be shared between goroutines.
type decoder struct {
	width, height int
	mbw, mbh      int // 宏块网格尺寸 / macroblock grid size
	maxPixels     int // 调用方给的像素预算，0 表示不限 / caller pixel budget, 0 means unlimited

	seg         segmentHeader
	filt        filterHeader
	q           [nSegment]quant
	tokenProb   [nPlane][nBand][nContext][nProb]uint8
	useSkipProb bool
	skipProb    uint8

	// fp 读首分区（控制头与宏块头），op[i] 读第 i 个 token 分区，nOP 为分区数。
	// fp reads the first partition (control and macroblock headers), op[i]
	// reads token partition i, nOP is the partition count.
	fp  boolDecoder
	op  [maxPartitions]boolDecoder
	nOP int

	// y/cb/cr 是 4:2:0 的三个平面，按宏块网格向上/右取整并带重建所需边距。
	// y, cb and cr are the 4:2:0 planes, padded up to the macroblock grid on
	// the bottom/right with the margin reconstruction needs.
	y, cb, cr *plane

	// leftMB 与 upMB 保存左、上相邻宏块的解码状态，作为上下文。
	// leftMB and upMB hold the decoded state of the left and above macroblocks as context.
	leftMB     mbState
	upMB       []mbState
	segment    int
	usePredY16 bool
	predY16    uint8
	predC8     uint8
	predY4     [4][4]uint8

	// tr 缓存上一行中、当前宏块右侧的 4 个像素，用作最右 4x4 块的右上角预测像素。
	// tr caches the four pixels right of the current macroblock, in the row
	// above it, used as the top-right pixels of its rightmost 4x4 blocks.
	tr [4]byte

	// coeff 是当前宏块残差系数的暂存区，nzDCMask/nzACMask 记录各块是否含非零 DC/AC。
	// coeff is the scratch buffer for the current macroblock's residual
	// coefficients; nzDCMask and nzACMask record which blocks have non-zero DC/AC.
	coeff    [400]int16
	nzDCMask uint32
	nzACMask uint32

	// filterParams[段][模式] 是逐段逐模式的滤波阈值（模式 0 只滤外边缘，
	// 模式 1 还滤内边缘），perMBFilterParams 是逐宏块最终值。
	// filterParams[segment][mode] are the loop filter thresholds per segment
	// and mode (mode 0 filters the outer edges only, mode 1 the inner ones
	// as well); perMBFilterParams are the final per-macroblock values.
	filterParams      [nSegment][2]filterParam
	perMBFilterParams []filterParam
}

// defaultMaxPixels 是默认像素预算：1<<27 像素约等于 11585×11585 的图像，
// 解码为 ARGB 后占 512 MB 内存。
// defaultMaxPixels is the default pixel budget: 1<<27 pixels is roughly an
// 11585 by 11585 image, i.e. 512 MB of ARGB pixels once decoded.
const defaultMaxPixels = 1 << 27

// Decode 解码一个 VP8 关键帧——WebP 静态图只包含这种帧——并返回 4:2:0 的
// YCbCr 图像，像素预算取 defaultMaxPixels。
// Decode decodes a VP8 key frame, the only frame type a WebP still image
// contains, and returns the frame as a 4:2:0 YCbCr image with the
// defaultMaxPixels budget.
func Decode(data []byte) (*image.YCbCr, error) {
	return DecodeLimit(data, defaultMaxPixels)
}

// DecodeLimit 与 Decode 相同，但像素预算由调用方指定，使损坏或恶意的帧头
// 无法让解码器无上限地分配内存。
// DecodeLimit is Decode with an explicit pixel budget, so that a corrupt or
// hostile frame header can not make the decoder allocate unbounded memory.
func DecodeLimit(data []byte, maxPixels int) (*image.YCbCr, error) {
	d := &decoder{}
	d.maxPixels = maxPixels
	if err := d.parseHeaders(data); err != nil {
		return nil, err
	}
	if err := d.decodeFrame(); err != nil {
		return nil, err
	}
	return d.image(), nil
}

// parseHeaders 解析 10 字节帧头（tag、起始码、宽高）与首分区里的全部控制头：
// 分段、环路滤波、分区表、量化、token 概率更新与 skip 概率。
// parseHeaders decodes the 10-byte frame header (tag, start code, dimensions)
// and every control header in the first partition: segmentation, loop
// filtering, the partition table, quantization, the token probability updates
// and the skip probability.
func (d *decoder) parseHeaders(data []byte) error {
	if len(data) < 10 {
		return ErrInvalid
	}
	tag := uint32(data[0]) | uint32(data[1])<<8 | uint32(data[2])<<16
	if tag&1 != 0 {
		return ErrNotKeyFrame
	}
	firstPartLen := int(tag >> 5)
	if data[3] != 0x9d || data[4] != 0x01 || data[5] != 0x2a {
		return ErrInvalid
	}
	d.width = int(by16(data[6:8]) & 0x3fff)
	d.height = int(by16(data[8:10]) & 0x3fff)
	if d.width == 0 || d.height == 0 || d.width > 16383 || d.height > 16383 {
		return ErrTooLarge
	}
	if d.maxPixels > 0 && int64(d.width)*int64(d.height) > int64(d.maxPixels) {
		return ErrTooLarge
	}
	d.mbw = (d.width + 15) >> 4
	d.mbh = (d.height + 15) >> 4

	rest := data[10:]
	if firstPartLen > len(rest) {
		return ErrInvalid
	}
	d.fp.init(rest[:firstPartLen])
	coeff := rest[firstPartLen:]

	d.tokenProb = defaultTokenProb
	d.segment = 0
	d.seg = segmentHeader{prob: [3]uint8{0xff, 0xff, 0xff}}

	// 色彩空间与像素钳位标志，只对视频有意义，静态图直接跳过。
	// colour space and pixel clamp bits, only meaningful for video
	d.fp.readBit(uniformProb)
	d.fp.readBit(uniformProb)

	d.parseSegmentHeader()
	d.parseFilterHeader()
	if err := d.parsePartitions(coeff); err != nil {
		return err
	}
	d.parseQuant()
	// 刷新上一帧参考缓冲的标志，只对视频有意义，静态图直接跳过。
	// refresh last frame buffer flag, only meaningful for video
	d.fp.readBit(uniformProb)
	d.parseTokenProb()
	d.useSkipProb = d.fp.readBit(uniformProb)
	if d.useSkipProb {
		d.skipProb = uint8(d.fp.readUint(uniformProb, 8))
	}
	if d.fp.eof {
		return ErrInvalid
	}
	return nil
}

// parseSegmentHeader 解析分段特性数据：启用与更新标志、逐段量化偏移和滤波
// 强度偏移、以及 segment ID 树概率；未启用分段时其余字段保持默认。
// parseSegmentHeader parses the segmentation feature data: the enable and
// update flags, the per-segment quantizer and filter strength offsets and
// the segment ID tree probabilities; without segmentation the remaining
// fields stay at their defaults.
func (d *decoder) parseSegmentHeader() {
	d.seg.useSegment = d.fp.readBit(uniformProb)
	if !d.seg.useSegment {
		d.seg.updateMap = false
		return
	}
	d.seg.updateMap = d.fp.readBit(uniformProb)
	if d.fp.readBit(uniformProb) {
		d.seg.relativeDelta = !d.fp.readBit(uniformProb)
		for i := range d.seg.quantizer {
			d.seg.quantizer[i] = int8(d.fp.readOptionalInt(uniformProb, 7))
		}
		for i := range d.seg.filterStrength {
			d.seg.filterStrength[i] = int8(d.fp.readOptionalInt(uniformProb, 6))
		}
	}
	if !d.seg.updateMap {
		return
	}
	for i := range d.seg.prob {
		if d.fp.readBit(uniformProb) {
			d.seg.prob[i] = uint8(d.fp.readUint(uniformProb, 8))
		} else {
			d.seg.prob[i] = 0xff
		}
	}
}

// parseFilterHeader 解析环路滤波头：滤波类型、基础强度、sharpness、
// 参考帧/模式增量；随后把逐段强度折算成最终的 perSegmentLevel，
// 并预计算各段各模式的 filterParam。
// parseFilterHeader parses the loop filter header: the filter type, the base
// level, the sharpness and the reference-frame/mode deltas; it then derives
// the final perSegmentLevel per segment and precomputes the filterParam of
// every segment and mode.
func (d *decoder) parseFilterHeader() {
	d.filt.simple = d.fp.readBit(uniformProb)
	d.filt.level = int8(d.fp.readUint(uniformProb, 6))
	d.filt.sharpness = uint8(d.fp.readUint(uniformProb, 3))
	d.filt.useLFDelta = d.fp.readBit(uniformProb)
	if d.filt.useLFDelta && d.fp.readBit(uniformProb) {
		for i := range d.filt.refLFDelta {
			d.filt.refLFDelta[i] = int8(d.fp.readOptionalInt(uniformProb, 6))
		}
		for i := range d.filt.modeLFDelta {
			d.filt.modeLFDelta[i] = int8(d.fp.readOptionalInt(uniformProb, 6))
		}
	}
	if d.filt.level == 0 {
		return
	}
	if d.seg.useSegment {
		for i := range d.filt.perSegmentLevel {
			strength := d.seg.filterStrength[i]
			if d.seg.relativeDelta {
				strength += d.filt.level
			}
			d.filt.perSegmentLevel[i] = strength
		}
	} else {
		d.filt.perSegmentLevel[0] = d.filt.level
	}
	d.computeFilterParams()
}

// parsePartitions 从首分区读出 token 分区数（2 位，得 1/2/4/8 个），再从系数
// 数据开头读出各分区的 3 字节长度表（最后一个分区取余下全部），据此切出每个
// 分区的 boolDecoder；长度越界或数据不足都判为损坏。
// parsePartitions reads the token partition count from the first partition
// (2 bits, yielding 1/2/4/8 partitions) and the 3-byte size table at the
// start of the coefficient data (the last partition takes the rest), then
// slices out a boolDecoder per partition; out-of-range sizes or missing
// data are rejected.
func (d *decoder) parsePartitions(data []byte) error {
	d.nOP = 1 << d.fp.readUint(uniformProb, 2)
	n := 3 * (d.nOP - 1)
	if len(data) < n {
		return ErrInvalid
	}
	sizes := [maxPartitions]int{}
	rest := len(data) - n
	for i := 0; i < d.nOP-1; i++ {
		size := int(data[3*i]) | int(data[3*i+1])<<8 | int(data[3*i+2])<<16
		if size > rest {
			return ErrInvalid
		}
		sizes[i] = size
		rest -= size
	}
	sizes[d.nOP-1] = rest

	off := n
	for i := 0; i < d.nOP; i++ {
		if sizes[i] < 0 || sizes[i] > len(data)-off {
			return ErrInvalid
		}
		d.op[i].init(data[off : off+sizes[i]])
		off += sizes[i]
	}
	return nil
}

// parseQuant 解析 7 位基础量化索引与 5 个增量（y1 DC、y2 DC/AC、uv DC/AC），
// 逐段折算出 y1/y2/uv 的 DC/AC 反量化系数；y2 的 AC 系数乘 155/100 后
// 下限钳到 8。
// parseQuant parses the 7-bit base quant index and the five deltas (y1 DC,
// y2 DC/AC, uv DC/AC) and derives the y1/y2/uv DC/AC dequantization factors
// of every segment; the y2 AC factor is scaled by 155/100 and clamped to a
// minimum of 8.
func (d *decoder) parseQuant() {
	baseQ0 := d.fp.readUint(uniformProb, 7)
	dqy1DC := d.fp.readOptionalInt(uniformProb, 4)
	const dqy1AC = 0
	dqy2DC := d.fp.readOptionalInt(uniformProb, 4)
	dqy2AC := d.fp.readOptionalInt(uniformProb, 4)
	dquvDC := d.fp.readOptionalInt(uniformProb, 4)
	dquvAC := d.fp.readOptionalInt(uniformProb, 4)

	for i := 0; i < nSegment; i++ {
		q := int32(baseQ0)
		if d.seg.useSegment {
			if d.seg.relativeDelta {
				q += int32(d.seg.quantizer[i])
			} else {
				q = int32(d.seg.quantizer[i])
			}
		}
		d.q[i].y1[0] = dequantTableDC[clipInt(int(q+dqy1DC), 0, 127)]
		d.q[i].y1[1] = dequantTableAC[clipInt(int(q+dqy1AC), 0, 127)]
		d.q[i].y2[0] = dequantTableDC[clipInt(int(q+dqy2DC), 0, 127)] * 2
		d.q[i].y2[1] = dequantTableAC[clipInt(int(q+dqy2AC), 0, 127)] * 155 / 100
		if d.q[i].y2[1] < 8 {
			d.q[i].y2[1] = 8
		}
		// 117 不是笔误：参考解码器把色度 DC 系数钳到 132，即 dequantTableDC[117]。
		// 117 is not a typo: the reference decoder clamps the chroma DC
		// factor at 132, which is dequantTableDC[117]
		d.q[i].uv[0] = dequantTableDC[clipInt(int(q+dquvDC), 0, 117)]
		d.q[i].uv[1] = dequantTableAC[clipInt(int(q+dquvAC), 0, 127)]
	}
}

// parseTokenProb 依据各自的更新概率，逐项读出 token 概率表的更新值并覆盖
// defaultTokenProb 中的对应项。
// parseTokenProb reads the per-entry updates of the token probability table,
// each guarded by its own update probability, over defaultTokenProb.
func (d *decoder) parseTokenProb() {
	for i := range d.tokenProb {
		for j := range d.tokenProb[i] {
			for k := range d.tokenProb[i][j] {
				for l := range d.tokenProb[i][j][k] {
					if d.fp.readBit(tokenUpdateProb[i][j][k][l]) {
						d.tokenProb[i][j][k][l] = uint8(d.fp.readUint(uniformProb, 8))
					}
				}
			}
		}
	}
}

// decodeFrame 逐宏块重建整帧，记录每块的滤波阈值，最后按帧头设置施加简单
// 滤波或普通滤波；任一解码器提前耗尽数据都判为损坏。
// decodeFrame reconstructs every macroblock, records the filter thresholds
// per macroblock and finally applies the simple or the normal loop filter as
// the frame header dictates; any entropy decoder running out of data is
// treated as corrupt.
func (d *decoder) decodeFrame() error {
	d.y = newPlane(d.mbw*16, d.mbh*16, 4)
	d.cb = newPlane(d.mbw*8, d.mbh*8, 0)
	d.cr = newPlane(d.mbw*8, d.mbh*8, 0)
	d.y.initBorders()
	d.cb.initBorders()
	d.cr.initBorders()

	d.upMB = make([]mbState, d.mbw)
	d.perMBFilterParams = make([]filterParam, d.mbw*d.mbh)

	for mby := 0; mby < d.mbh; mby++ {
		d.leftMB = mbState{}
		for mbx := 0; mbx < d.mbw; mbx++ {
			skip := d.reconstruct(mbx, mby)
			// 阈值取本段本模式的预设值；宏块含非零系数（非 skip）时
			// 还要对内部边缘做滤波。
			// thresholds come from the segment/mode table; a macroblock
			// with non-zero coefficients (not skip) is also inner filtered.
			fs := d.filterParams[d.segment][btou(!d.usePredY16)]
			fs.inner = fs.inner || !skip
			d.perMBFilterParams[d.mbw*mby+mbx] = fs
		}
		d.y.fillOverhang(mby*16 + 15)
	}
	if d.fp.eof {
		return ErrInvalid
	}
	for i := 0; i < d.nOP; i++ {
		if d.op[i].eof {
			return ErrInvalid
		}
	}

	if d.filt.level != 0 {
		if d.filt.simple {
			d.simpleFilter()
		} else {
			d.normalFilter()
		}
	}
	return nil
}

// loadTopRight 缓存上一行中本宏块右侧的 4 个像素，供最右 4x4 块的右上角
// 预测使用。
// loadTopRight caches the four pixels right of the macroblock in the row
// above it, for the top-right prediction of its rightmost 4x4 blocks.
func (d *decoder) loadTopRight(mbx, mby int) {
	row := mby*16 - 1
	base := mbx*16 + 16
	for i := 0; i < 4; i++ {
		d.tr[i] = d.y.pix[d.y.at(base+i, row)]
	}
}

// reconstruct 解码一个宏块：读 segment ID 与 skip 标志，解析 16x16 或 4x4
// 的 Y 预测模式与 8x8 色度预测模式，非 skip 时再解析残差系数，最后重建像素；
// 返回值表示该宏块是否跳过了残差（供环路滤波判断是否需要内边缘滤波）。
// reconstruct decodes one macroblock: it reads the segment ID and the skip
// flag, parses the 16x16 or 4x4 luma and the 8x8 chroma prediction modes,
// parses the residual coefficients when not skipped and reconstructs the
// pixels; the result reports whether residuals were skipped (used by the
// loop filter to decide on inner edge filtering).
func (d *decoder) reconstruct(mbx, mby int) (skip bool) {
	if d.seg.updateMap {
		if !d.fp.readBit(d.seg.prob[0]) {
			d.segment = int(d.fp.readUint(d.seg.prob[1], 1))
		} else {
			d.segment = int(d.fp.readUint(d.seg.prob[2], 1)) + 2
		}
	}
	if d.useSkipProb {
		skip = d.fp.readBit(d.skipProb)
	}
	for i := range d.coeff {
		d.coeff[i] = 0
	}
	d.loadTopRight(mbx, mby)
	d.usePredY16 = d.fp.readBit(145)
	if d.usePredY16 {
		d.parsePredModeY16(mbx)
	} else {
		d.parsePredModeY4(mbx)
	}
	d.parsePredModeC8()
	if !skip {
		skip = d.parseResiduals(mbx, mby)
	} else {
		if d.usePredY16 {
			d.leftMB.nzY16 = 0
			d.upMB[mbx].nzY16 = 0
		}
		d.leftMB.nzMask = 0
		d.upMB[mbx].nzMask = 0
		d.nzDCMask = 0
		d.nzACMask = 0
	}
	d.reconstructMacroblock(mbx, mby)
	return skip
}

// image 把按宏块网格加了边距的三个平面裁剪成帧尺寸的 4:2:0 YCbCr 图像。
// image crops the macroblock-padded planes into a frame-sized 4:2:0 YCbCr image.
func (d *decoder) image() *image.YCbCr {
	img := image.NewYCbCr(image.Rect(0, 0, d.width, d.height), image.YCbCrSubsampleRatio420)
	for y := 0; y < d.height; y++ {
		copy(img.Y[y*img.YStride:y*img.YStride+d.width], d.y.pix[d.y.at(0, y):d.y.at(0, y)+d.width])
	}
	cw, ch := (d.width+1)/2, (d.height+1)/2
	for y := 0; y < ch; y++ {
		copy(img.Cb[y*img.CStride:y*img.CStride+cw], d.cb.pix[d.cb.at(0, y):d.cb.at(0, y)+cw])
		copy(img.Cr[y*img.CStride:y*img.CStride+cw], d.cr.pix[d.cr.at(0, y):d.cr.at(0, y)+cw])
	}
	return img
}

// by16 按小端序读出一个 16 位无符号值。/ by16 reads a 16-bit little-endian value.
func by16(b []byte) uint16 {
	return uint16(b[0]) | uint16(b[1])<<8
}
