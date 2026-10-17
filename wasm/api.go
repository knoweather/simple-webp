// Package wasm exposes the pure-Go simple-webp decoder to WebAssembly /
// JavaScript.
//
// The WASM build is read-only: it turns a WebP byte stream into RGBA pixels
// that can be painted straight onto a <canvas>. Encoding is intentionally out
// of scope.
//
// The JS-facing bindings live in wasm.go (build tag js&&wasm) and are wired up
// by Register(); this file holds the framework-independent logic so it can be
// unit tested on a normal host.
package wasm

import (
	"image"
	"image/draw"

	"github.com/knoweather/simple-webp"
)

// DecodeResult is the decoded image, ready to copy into a canvas ImageData.
// RGBA holds straight (non-premultiplied) alpha, Width*Height*4 bytes in
// row-major order — exactly what new ImageData(rgba, width, height) expects.
type DecodeResult struct {
	Width, Height int
	HasAlpha      bool
	HasAnimation  bool
	RGBA          []byte
}

// InfoResult is the container-level metadata, decoded without touching pixels.
type InfoResult struct {
	Width, Height int
	HasAlpha      bool
	HasAnimation  bool
}

// Decode parses and fully decodes a WebP byte stream into straight-alpha RGBA.
// Animated files decode to their first frame.
func Decode(data []byte) (*DecodeResult, error) {
	w, err := simple_webp.ParseWebpBytes(data)
	if err != nil {
		return nil, err
	}
	img, err := w.ToImage()
	if err != nil {
		return nil, err
	}
	res := &DecodeResult{
		Width:        w.Width,
		Height:       w.Height,
		HasAlpha:     w.HasAlpha,
		HasAnimation: w.HasAnimation,
	}
	res.RGBA, err = rgbaFromImage(img)
	if err != nil {
		return nil, err
	}
	return res, nil
}

// Info reads the dimensions and flags without decoding pixels.
func Info(data []byte) (*InfoResult, error) {
	w, err := simple_webp.ParseWebpBytes(data)
	if err != nil {
		return nil, err
	}
	return &InfoResult{
		Width:        w.Width,
		Height:       w.Height,
		HasAlpha:     w.HasAlpha,
		HasAnimation: w.HasAnimation,
	}, nil
}

// rgbaFromImage copies an image.Image into a straight-alpha RGBA byte slice.
func rgbaFromImage(img image.Image) ([]byte, error) {
	b := img.Bounds()
	w, h := b.Dx(), b.Dy()
	var src *image.NRGBA
	if n, ok := img.(*image.NRGBA); ok && n.Rect.Min.X == 0 && n.Rect.Min.Y == 0 {
		src = n
	} else {
		src = image.NewNRGBA(image.Rect(0, 0, w, h))
		draw.Draw(src, src.Bounds(), img, b.Min, draw.Src)
	}
	out := make([]byte, len(src.Pix))
	copy(out, src.Pix)
	return out, nil
}
