package simple_webp

import (
	"bytes"
	"math"

	"github.com/pkg/errors"
)

// marshalWebpFile 按给定顺序把 chunk 序列写成完整的 RIFF/WEBP 文件，RIFF 的
// 大小字段按实际内容计算。
//
// marshalWebpFile writes the given chunk sequence, in order, as a complete
// RIFF/WEBP file; the RIFF size field is computed from the actual content.
func marshalWebpFile(chunks []riffChunk) ([]byte, error) {
	var buf bytes.Buffer
	buf.Write(riffTag)
	total := 4
	for _, c := range chunks {
		total += chunkSize(len(c.data))
	}
	if int64(total) > math.MaxUint32 {
		return nil, errors.New("webp stream too large")
	}
	var size [4]byte
	by.PutUint32(size[:], uint32(total))
	buf.Write(size[:])
	buf.Write(webpTag)
	for _, c := range chunks {
		if err := writeRiffChunk(&buf, c.name, c.data); err != nil {
			return nil, err
		}
	}
	return buf.Bytes(), nil
}

// encodeChunks 按容器顺序拼出 chunk 序列：有元数据时先写带标志位的 VP8X，
// 然后 ICCP、图像数据、EXIF、XMP。
//
// encodeChunks assembles the chunk sequence in container order: a flagged VP8X
// first when metadata is present, then ICCP, the image data, EXIF and XMP.
func encodeChunks(imageChunk FourCC, payload []byte, width, height int, iccp, exif, xmp []byte) ([]riffChunk, error) {
	if width <= 0 || height <= 0 || width > 1<<24 || height > 1<<24 {
		return nil, errors.Errorf("webp: image size %dx%d out of range", width, height)
	}
	var flags uint8
	if len(iccp) > 0 {
		flags |= FlagICC
	}
	if len(exif) > 0 {
		flags |= FlagEXIF
	}
	if len(xmp) > 0 {
		flags |= FlagXMP
	}
	chunks := make([]riffChunk, 0, 5)
	if flags != 0 {
		p := make([]byte, 10)
		p[0] = flags
		writeUint24(p[4:7], uint32(width-1))
		writeUint24(p[7:10], uint32(height-1))
		chunks = append(chunks, riffChunk{name: VP8XChunk, data: p})
	}
	if len(iccp) > 0 {
		chunks = append(chunks, riffChunk{name: ICCPChunk, data: iccp})
	}
	chunks = append(chunks, riffChunk{name: imageChunk, data: payload})
	if len(exif) > 0 {
		chunks = append(chunks, riffChunk{name: EXIFChunk, data: exif})
	}
	if len(xmp) > 0 {
		chunks = append(chunks, riffChunk{name: XMPChunk, data: xmp})
	}
	return chunks, nil
}
