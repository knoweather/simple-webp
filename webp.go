package simple_webp

import (
	"bytes"
	"image"
	"io"
	"sync"

	"github.com/pkg/errors"
)

// Sentinel errors returned by the parser and the decoders.
var (
	// ErrNotWebP is returned when the stream does not start with RIFF....WEBP.
	ErrNotWebP = errors.New("invalid webp: bad RIFF/WEBP signature")
	// ErrChunkNotFound is returned by ParseChunk when no chunk of the
	// requested type is left in the stream.
	ErrChunkNotFound = errors.New("chunk not found")
	// ErrUnsupported is returned for valid but unimplemented webp features.
	ErrUnsupported = errors.New("unsupported webp feature")
	// ErrImageTooLarge is returned when the image exceeds the configured
	// pixel budget.
	ErrImageTooLarge = errors.New("webp image too large")
)

// Webp is a parsed WebP container. The chunk payloads of the image data are
// kept as raw bytes, the parsed structs are views on them, so metadata can be
// edited and written back without re-encoding the pixels.
type Webp struct {
	Width, Height int
	HasAlpha      bool
	HasAnimation  bool

	VP8X  *VP8X
	VP8   *VP8
	VP8L  *VP8L
	ALPH  *ALPH
	ANIM  *ANIM
	ANMFs []*ANMF
	ICCP  *ICCP
	EXIF  *EXIF
	XMP   *XMP

	// OtherChunk holds the custom chunks parsed through ParseChunk, grouped
	// by chunk type.
	OtherChunk map[FourCC][]ChunkParse
	// chunks are the chunks that were not decoded natively, in file order.
	chunks []*riffChunk

	// origChunks keeps every chunk of the file in its original order,
	// including the consumed and deleted ones, so WriteTo can reproduce the
	// stream faithfully. The parallel slices record what happened to each
	// chunk.
	origChunks   []*riffChunk
	origConsumed []bool
	origDeleted  []bool

	sync.RWMutex
	sync.Once

	// decoded image cache for the image.Image implementation, see image.go.
	img    image.Image
	imgErr error
}

// countingReader counts the bytes read, used to validate the RIFF size field.
type countingReader struct {
	r io.Reader
	n int64
}

func (c *countingReader) Read(p []byte) (int, error) {
	n, err := c.r.Read(p)
	c.n += int64(n)
	return n, err
}

// ParseWebp parses a webp stream: it reads the RIFF header, all chunks and
// decodes the container chunks (VP8X, VP8, VP8L, ALPH, ANIM, ANMF, ICCP,
// EXIF, XMP). Chunks of unknown type are kept in raw form, see RawChunks.
//
// The RIFF size field is validated only when verifySize is true.
func ParseWebp(r io.Reader, verifySize ...bool) (*Webp, error) {
	cr := &countingReader{r: r}
	var head [12]byte
	if _, err := io.ReadFull(cr, head[:]); err != nil {
		return nil, errors.WithMessage(err, "invalid webp: can not read RIFF header")
	}
	if string(head[:4]) != string(riffTag) || string(head[8:12]) != string(webpTag) {
		return nil, ErrNotWebP
	}
	declared := by.Uint32(head[4:8])

	var w = &Webp{}
	for {
		c, err := readRiffChunk(cr)
		if err != nil {
			if errors.Is(err, io.EOF) {
				break
			}
			return nil, err
		}
		w.chunks = append(w.chunks, c)
	}
	if len(w.chunks) == 0 {
		return nil, errors.New("invalid webp: no chunks")
	}
	if len(verifySize) > 0 && verifySize[0] {
		if got := uint32(cr.n - 8); got != declared {
			return nil, errors.Errorf("invalid webp: RIFF size is %d, stream is %d", declared, got)
		}
	}

	// snapshot the original chunk order before any chunk gets consumed
	w.origChunks = append([]*riffChunk(nil), w.chunks...)
	w.origConsumed = make([]bool, len(w.chunks))
	w.origDeleted = make([]bool, len(w.chunks))
	if err := w.parseBaseChunk(); err != nil {
		return nil, errors.WithStack(err)
	}
	return w, nil
}

// ParseWebpBytes parses a webp stream held in memory.
func ParseWebpBytes(b []byte) (*Webp, error) {
	return ParseWebp(bytes.NewReader(b))
}

// parseBaseChunk consumes every chunk the library understands and derives the
// image dimensions and flags from what was found.
func (w *Webp) parseBaseChunk() error {
	kept := make([]*riffChunk, 0, len(w.chunks))
	for _, c := range w.chunks {
		consumed, err := w.parseOne(c)
		if err != nil {
			return errors.WithStack(err)
		}
		if !consumed {
			kept = append(kept, c)
		}
	}
	w.chunks = kept
	return w.derive()
}

// parseOne parses a single chunk into its struct. It reports whether the
// chunk was consumed; a second occurrence of a single-instance chunk is left
// raw instead of failing the parse.
func (w *Webp) parseOne(c *riffChunk) (bool, error) {
	var target ChunkParse
	switch c.name {
	case VP8XChunk:
		if w.VP8X != nil {
			return false, nil
		}
		w.VP8X = &VP8X{}
		target = w.VP8X
	case VP8Chunk:
		if w.VP8 != nil {
			return false, nil
		}
		w.VP8 = &VP8{}
		target = w.VP8
	case VP8LChunk:
		if w.VP8L != nil {
			return false, nil
		}
		w.VP8L = &VP8L{}
		target = w.VP8L
	case ALPHChunk:
		if w.ALPH != nil {
			return false, nil
		}
		w.ALPH = &ALPH{}
		target = w.ALPH
	case ANIMChunk:
		if w.ANIM != nil {
			return false, nil
		}
		w.ANIM = &ANIM{}
		target = w.ANIM
	case ANMFChunk:
		frame := &ANMF{}
		if err := frame.Parse(c); err != nil {
			return false, err
		}
		w.ANMFs = append(w.ANMFs, frame)
		w.markOrigConsumed(c)
		return true, nil
	case ICCPChunk:
		if w.ICCP != nil {
			return false, nil
		}
		w.ICCP = &ICCP{}
		target = w.ICCP
	case EXIFChunk:
		if w.EXIF != nil {
			return false, nil
		}
		w.EXIF = &EXIF{}
		target = w.EXIF
	case XMPChunk:
		if w.XMP != nil {
			return false, nil
		}
		w.XMP = &XMP{}
		target = w.XMP
	default:
		return false, nil
	}
	if err := target.Parse(c); err != nil {
		return false, err
	}
	w.markOrigConsumed(c)
	return true, nil
}

// derive fills Width/Height/HasAlpha/HasAnimation from the parsed chunks.
func (w *Webp) derive() error {
	switch {
	case w.VP8X != nil:
		w.Width, w.Height = w.VP8X.CanvasWidth, w.VP8X.CanvasHeight
		w.HasAlpha = w.VP8X.HasAlpha()
		w.HasAnimation = w.VP8X.HasAnimation()
	case w.VP8L != nil:
		w.Width, w.Height = w.VP8L.Width, w.VP8L.Height
		w.HasAlpha = w.VP8L.AlphaUsed
	case w.VP8 != nil:
		w.Width, w.Height = w.VP8.Width, w.VP8.Height
	default:
		return errors.New("invalid webp: no image data (VP8/VP8L chunk)")
	}
	if w.Width <= 0 || w.Height <= 0 {
		return errors.Errorf("invalid webp: bad image size %dx%d", w.Width, w.Height)
	}
	if w.ALPH != nil {
		w.HasAlpha = true
	}
	if w.VP8L != nil && w.VP8L.AlphaUsed {
		w.HasAlpha = true
	}
	if w.HasAnimation && len(w.ANMFs) == 0 {
		return errors.New("invalid webp: animation flag without ANMF chunk")
	}
	return nil
}

// markOrigConsumed marks a chunk as consumed in the original order snapshot.
func (w *Webp) markOrigConsumed(c *riffChunk) {
	for i, oc := range w.origChunks {
		if oc == c {
			w.origConsumed[i] = true
			return
		}
	}
}

// ParseChunk parses the first remaining chunk whose type matches
// c.ChunkName(). After a successful parse the chunk is consumed and removed
// from the raw chunk list. Unless notSave is true, the parser instance is
// recorded in OtherChunk and can be retrieved later with
// GetOtherChunkByName.
func (w *Webp) ParseChunk(c ChunkParse, notSave ...bool) error {
	w.Do(func() {
		w.OtherChunk = make(map[FourCC][]ChunkParse)
	})
	for i, cur := range w.chunks {
		if cur.name != c.ChunkName() {
			continue
		}
		if err := c.Parse(cur); err != nil {
			return errors.WithStack(err)
		}
		w.chunks = append(w.chunks[:i], w.chunks[i+1:]...)
		w.markOrigConsumed(cur)
		if !(len(notSave) > 0 && notSave[0]) {
			w.Lock()
			w.OtherChunk[c.ChunkName()] = append(w.OtherChunk[c.ChunkName()], c)
			w.Unlock()
		}
		return nil
	}
	return ErrChunkNotFound
}

// GetOtherChunkByName returns the custom chunk parsers recorded for a type.
func (w *Webp) GetOtherChunkByName(name FourCC) ([]ChunkParse, error) {
	w.RLock()
	defer w.RUnlock()
	if list, ok := w.OtherChunk[name]; ok {
		return list, nil
	}
	return nil, ErrChunkNotFound
}

// RawChunks returns all chunks that are still left after base parsing, in the
// order they appear in the file.
func (w *Webp) RawChunks() []RawChunk {
	out := make([]RawChunk, 0, len(w.chunks))
	for _, c := range w.chunks {
		out = append(out, RawChunk{Name: c.name, Data: c.data})
	}
	return out
}

// RawChunkByName returns the raw payload of the first still-present chunk of
// the given type.
func (w *Webp) RawChunkByName(name FourCC) ([]byte, bool) {
	for _, c := range w.chunks {
		if c.name == name {
			return c.data, true
		}
	}
	return nil, false
}

// DeleteRawChunk removes every chunk of the given type from the stream and
// returns how many chunks were removed. It works for the metadata chunks that
// are decoded natively (ICCP, EXIF, XMP) as well as for unknown raw chunks; a
// WriteTo afterwards writes the stream without those chunks.
func (w *Webp) DeleteRawChunk(name FourCC) int {
	removed := 0
	// native metadata chunks are dropped by clearing the parsed struct
	switch name {
	case ICCPChunk:
		if w.ICCP != nil {
			w.ICCP = nil
			removed++
		}
	case EXIFChunk:
		if w.EXIF != nil {
			w.EXIF = nil
			removed++
		}
	case XMPChunk:
		if w.XMP != nil {
			w.XMP = nil
			removed++
		}
	}

	// drop the chunks that are still raw
	rest := make([]*riffChunk, 0, len(w.chunks))
	for _, c := range w.chunks {
		if c.name == name {
			continue
		}
		rest = append(rest, c)
	}
	rawRemoved := len(w.chunks) - len(rest)
	w.chunks = rest
	removed += rawRemoved

	// keep the original order snapshot in sync so WriteTo skips the chunks
	for i, c := range w.origChunks {
		if c.name != name || w.origDeleted[i] {
			continue
		}
		// a consumed chunk is skipped only when its struct is gone, an
		// unconsumed one only when it was removed from the raw list
		if w.origConsumed[i] {
			switch name {
			case ICCPChunk, EXIFChunk, XMPChunk:
				w.origDeleted[i] = true
			}
			continue
		}
		if rawRemoved > 0 {
			w.origDeleted[i] = true
		}
	}
	return removed
}

// AddRawChunk inserts a new raw chunk into the stream before the first image
// chunk (VP8, VP8L or ANMF); if there is no image chunk it is appended.
// Use it e.g. to add an EXIF or XMP chunk to an already parsed file.
func (w *Webp) AddRawChunk(name FourCC, data []byte) error {
	if !validFourCC(name) {
		return errors.Errorf("invalid chunk type %q", string(name))
	}
	if len(data) > maxChunkSize {
		return errors.New("chunk data too large")
	}
	if w.origChunks == nil {
		return errors.New("webp is not parsed")
	}
	c := &riffChunk{name: name, data: data}
	insertAt := len(w.origChunks)
	for i, oc := range w.origChunks {
		switch oc.name {
		case VP8Chunk, VP8LChunk, ANMFChunk:
			insertAt = i
		}
		if insertAt != len(w.origChunks) {
			break
		}
	}
	n := len(w.origChunks) + 1
	chunks := make([]*riffChunk, 0, n)
	consumed := make([]bool, 0, n)
	deleted := make([]bool, 0, n)
	for i := range w.origChunks {
		if i == insertAt {
			chunks = append(chunks, c)
			consumed = append(consumed, false)
			deleted = append(deleted, false)
		}
		chunks = append(chunks, w.origChunks[i])
		consumed = append(consumed, w.origConsumed[i])
		deleted = append(deleted, w.origDeleted[i])
	}
	if insertAt == len(w.origChunks) {
		chunks = append(chunks, c)
		consumed = append(consumed, false)
		deleted = append(deleted, false)
	}
	w.origChunks, w.origConsumed, w.origDeleted = chunks, consumed, deleted
	w.rebuildChunks()
	return nil
}

// rebuildChunks re-derives the remaining raw chunk list from the original
// order snapshot.
func (w *Webp) rebuildChunks() {
	rest := make([]*riffChunk, 0, len(w.origChunks))
	for i, c := range w.origChunks {
		if w.origConsumed[i] || w.origDeleted[i] {
			continue
		}
		rest = append(rest, c)
	}
	w.chunks = rest
}
