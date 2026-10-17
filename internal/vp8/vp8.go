package vp8

import (
	"errors"
	"image"
)

// Errors returned by the decoder.
var (
	ErrInvalid     = errors.New("vp8: invalid bitstream")
	ErrNotKeyFrame = errors.New("vp8: not a key frame")
	ErrTooLarge    = errors.New("vp8: frame too large")
)

const (
	nSegment      = 4
	nSegmentProb  = 3
	maxPartitions = 8
)

type segmentHeader struct {
	useSegment     bool
	updateMap      bool
	relativeDelta  bool
	quantizer      [nSegment]int8
	filterStrength [nSegment]int8
	prob           [nSegmentProb]uint8
}

type filterHeader struct {
	simple          bool
	level           int8
	sharpness       uint8
	useLFDelta      bool
	refLFDelta      [4]int8
	modeLFDelta     [4]int8
	perSegmentLevel [nSegment]int8
}

// quant holds the DC and AC dequantization factors of one segment.
type quant struct {
	y1 [2]uint16
	y2 [2]uint16
	uv [2]uint16
}

// filterParam holds the loop filter thresholds of one macroblock.
type filterParam struct {
	level, ilevel, hlevel uint8
	inner                 bool
}

type decoder struct {
	width, height int
	mbw, mbh      int
	maxPixels     int

	seg         segmentHeader
	filt        filterHeader
	q           [nSegment]quant
	tokenProb   [nPlane][nBand][nContext][nProb]uint8
	useSkipProb bool
	skipProb    uint8

	fp  boolDecoder
	op  [maxPartitions]boolDecoder
	nOP int

	y, cb, cr *plane

	leftMB     mbState
	upMB       []mbState
	segment    int
	usePredY16 bool
	predY16    uint8
	predC8     uint8
	predY4     [4][4]uint8

	// tr holds the four pixels right of the current macroblock, in the row
	// above it, used as the top-right pixels of its rightmost 4x4 blocks.
	tr [4]byte

	coeff    [400]int16
	nzDCMask uint32
	nzACMask uint32

	filterParams      [nSegment][2]filterParam
	perMBFilterParams []filterParam
}

// defaultMaxPixels bounds the decoded frame, 1<<27 pixels is a 11585 by 11585
// image and 512 MB of ARGB pixels.
const defaultMaxPixels = 1 << 27

// Decode decodes a VP8 key frame, the only frame type a WebP still image
// contains, and returns the frame as a 4:2:0 YCbCr image.
func Decode(data []byte) (*image.YCbCr, error) {
	return DecodeLimit(data, defaultMaxPixels)
}

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

// parseHeaders parses the frame header and every control header of the first
// partition.
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

	// colour space and pixel clamp, only meaningful for video
	d.fp.readBit(uniformProb)
	d.fp.readBit(uniformProb)

	d.parseSegmentHeader()
	d.parseFilterHeader()
	if err := d.parsePartitions(coeff); err != nil {
		return err
	}
	d.parseQuant()
	// refresh last frame buffer, only meaningful for video
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

// parsePartitions reads the token partition count from the first partition and
// the partition size table from the beginning of the coefficient data.
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
		// 117 is not a typo: the reference decoder clamps the chroma DC
		// factor at 132, which is dequantTableDC[117]
		d.q[i].uv[0] = dequantTableDC[clipInt(int(q+dquvDC), 0, 117)]
		d.q[i].uv[1] = dequantTableAC[clipInt(int(q+dquvAC), 0, 127)]
	}
}

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

// decodeFrame reconstructs every macroblock and applies the loop filter.
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

// loadTopRight caches the four pixels that are right of the macroblock in the
// row above it.
func (d *decoder) loadTopRight(mbx, mby int) {
	row := mby*16 - 1
	base := mbx*16 + 16
	for i := 0; i < 4; i++ {
		d.tr[i] = d.y.pix[d.y.at(base+i, row)]
	}
}

// reconstruct decodes one macroblock and returns whether the inner loop
// filter can be skipped for it.
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

// image crops the padded planes into a YCbCr image of the frame size.
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

func by16(b []byte) uint16 {
	return uint16(b[0]) | uint16(b[1])<<8
}
