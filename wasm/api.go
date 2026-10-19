// Package wasm 将纯 Go 实现的 webp 解码器暴露给 WebAssembly / JavaScript。
//
// 本包的 WASM 构建是只读的：把 WebP 字节流解码为可直接绘制到 <canvas> 的 RGBA
// 像素。编码功能有意不在范围内。
//
// 面向 JS 的绑定位于 wasm.go（build tag js&&wasm），由 Register() 接线；
// 本文件存放与框架无关的逻辑，以便在普通宿主机上进行单元测试。
//
// Package wasm exposes the pure-Go webp decoder to WebAssembly /
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

// DecodeResult 是解码结果，可直接拷贝进 canvas 的 ImageData。
// RGBA 存放直通（非预乘）alpha，按行优先排列，共 Width*Height*4 字节——
// 正是 new ImageData(rgba, width, height) 所期望的格式。
//
// DecodeResult is the decoded image, ready to copy into a canvas ImageData.
// RGBA holds straight (non-premultiplied) alpha, Width*Height*4 bytes in
// row-major order — exactly what new ImageData(rgba, width, height) expects.
type DecodeResult struct {
	Width, Height int
	HasAlpha      bool
	HasAnimation  bool
	RGBA          []byte
}

// InfoResult 是容器级元数据，不解码像素即可读出。
//
// InfoResult is the container-level metadata, decoded without touching pixels.
type InfoResult struct {
	Width, Height int
	HasAlpha      bool
	HasAnimation  bool
	// HasICC / HasEXIF / HasXMP 报告文件是否内嵌对应的元数据 chunk。
	// HasICC / HasEXIF / HasXMP report whether the file embeds the matching
	// metadata chunk.
	HasICC  bool
	HasEXIF bool
	HasXMP  bool
	// FrameCount 是动画帧数；非动画文件为 0。
	// FrameCount is the number of animation frames; 0 for still images.
	FrameCount int
}

// Decode 解析并完整解码 WebP 字节流为直通 alpha 的 RGBA 像素。
// 动画文件解码为首帧。
//
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

// FrameResult 是动画中的一帧：画布尺寸（所有帧共享）与逐帧像素、显示时长。
// RGBA 存放直通（非预乘）alpha，按行优先排列，共 Width*Height*4 字节。
//
// FrameResult is one animation frame: the shared canvas size, the frame pixels
// and its display duration. RGBA holds straight (non-premultiplied) alpha in
// row-major order, Width*Height*4 bytes.
type FrameResult struct {
	Width, Height int
	Duration      int // 显示时长（毫秒）/ display duration in milliseconds
	RGBA          []byte
}

// DecodeFrames 解析并完整解码 WebP 字节流的所有帧：动画文件返回每一帧合成后的
// 画布像素与其显示时长；非动画文件返回单帧（Duration 为 0）。每一帧的 RGBA 与
// Decode 的 RGBA 同格式，可直接绘制到 canvas。
//
// DecodeFrames parses and fully decodes every frame of a WebP byte stream: an
// animated file yields each composited frame with its display duration; a still
// image yields a single frame with Duration 0. Each frame's RGBA shares the
// format of Decode's RGBA and can be painted straight onto a canvas.
func DecodeFrames(data []byte) ([]*FrameResult, error) {
	w, err := simple_webp.ParseWebpBytes(data)
	if err != nil {
		return nil, err
	}
	if w.HasAnimation {
		imgs, durs, err := w.Frames(0)
		if err != nil {
			return nil, err
		}
		out := make([]*FrameResult, len(imgs))
		for i, img := range imgs {
			rgba, err := rgbaFromImage(img)
			if err != nil {
				return nil, err
			}
			out[i] = &FrameResult{
				Width:    w.Width,
				Height:   w.Height,
				Duration: durs[i],
				RGBA:     rgba,
			}
		}
		return out, nil
	}
	img, err := w.ToImage()
	if err != nil {
		return nil, err
	}
	rgba, err := rgbaFromImage(img)
	if err != nil {
		return nil, err
	}
	return []*FrameResult{{
		Width:    w.Width,
		Height:   w.Height,
		Duration: 0,
		RGBA:     rgba,
	}}, nil
}

// Info 读取尺寸与标志位，不解码像素。
//
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
		HasICC:       w.ICCP != nil,
		HasEXIF:      w.EXIF != nil,
		HasXMP:       w.XMP != nil,
		FrameCount:   len(w.ANMFs),
	}, nil
}

// rgbaFromImage 把 image.Image 拷贝为直通 alpha 的 RGBA 字节切片。
//
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
