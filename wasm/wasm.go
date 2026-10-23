//go:build js && wasm

// Package wasm 是 WebP 的 WebAssembly（WASM）解码前端，供浏览器与 Node.js
// 使用：把 WebP 字节流解码成 RGBA 像素，不做编码，也不依赖 libwebp 或 cgo。
// 构建方式见仓库的 wasm/README.md。
//
// Package wasm is the WebAssembly (WASM) front end for WebP decoding in the
// browser and in Node.js: it decodes a WebP byte stream into RGBA pixels and
// never encodes, with no libwebp and no cgo. See wasm/README.md for the build.
package wasm

import "syscall/js"

// Register 在 JavaScript 全局对象上安装 WebP 解码函数：
//
//	webpDecode(bytes)        -> { width, height, hasAlpha, hasAnimation, rgba } | { error }
//	webpDecodeFrames(bytes)  -> [ { width, height, duration, rgba }, ... ]       | { error }
//	webpInfo(bytes)          -> { width, height, hasAlpha, hasAnimation,
//	                              hasICC, hasEXIF, hasXMP, frameCount }          | { error }
//
// bytes 可为 Uint8Array 或 ArrayBuffer。rgba 是直通 alpha 像素的 Uint8Array
// （行优先，Width*Height*4 字节）。调用方必须同步消费它，例如
// new ImageData(new Uint8ClampedArray(res.rgba), w, h)；其底层内存由 Go 管理，
// 不得跨 wasm 调用持有引用。
//
// Register installs the WebP decode functions on the JavaScript global object:
//
//	webpDecode(bytes)        -> { width, height, hasAlpha, hasAnimation, rgba } | { error }
//	webpDecodeFrames(bytes)  -> [ { width, height, duration, rgba }, ... ]       | { error }
//	webpInfo(bytes)          -> { width, height, hasAlpha, hasAnimation,
//	                              hasICC, hasEXIF, hasXMP, frameCount }          | { error }
//
// bytes may be a Uint8Array or an ArrayBuffer. rgba is a Uint8Array of
// straight-alpha pixels (row-major, Width*Height*4). Consume it synchronously
// (e.g. new ImageData(new Uint8ClampedArray(res.rgba), w, h)); the backing
// memory is Go-managed and must not be retained across wasm calls.
func Register() {
	js.Global().Set("webpDecode", js.FuncOf(decodeFunc))
	js.Global().Set("webpDecodeFrames", js.FuncOf(decodeFramesFunc))
	js.Global().Set("webpInfo", js.FuncOf(infoFunc))
}

// decodeFunc 是 webpDecode 的 JS 回调：缺少入参或解码失败时返回 { error } 对象，
// 成功时返回带尺寸、标志位与像素数据的结果对象。
//
// decodeFunc backs the webpDecode JS callback: it returns an { error } object
// on missing arguments or decode failure, and a result object carrying the
// dimensions, flags, and pixel data on success.
func decodeFunc(_ js.Value, args []js.Value) any {
	if len(args) < 1 {
		return errObj("webpDecode: missing input bytes")
	}
	data, e := bytesFromJS(args[0])
	if e != nil {
		return errObj(e.Error())
	}
	res, e := Decode(data)
	if e != nil {
		return errObj(e.Error())
	}
	out := js.Global().Get("Object").New()
	out.Set("width", res.Width)
	out.Set("height", res.Height)
	out.Set("hasAlpha", res.HasAlpha)
	out.Set("hasAnimation", res.HasAnimation)
	out.Set("rgba", bytesToJS(res.RGBA))
	return out
}

// decodeFramesFunc 是 webpDecodeFrames 的 JS 回调：缺少入参或解码失败时返回
// { error } 对象，成功时返回一个帧对象数组，每帧含 width/height/duration/rgba。
//
// decodeFramesFunc backs the webpDecodeFrames JS callback: it returns an
// { error } object on missing arguments or decode failure, and an array of frame
// objects (each with width/height/duration/rgba) on success.
func decodeFramesFunc(_ js.Value, args []js.Value) any {
	if len(args) < 1 {
		return errObj("webpDecodeFrames: missing input bytes")
	}
	data, e := bytesFromJS(args[0])
	if e != nil {
		return errObj(e.Error())
	}
	frames, e := DecodeFrames(data)
	if e != nil {
		return errObj(e.Error())
	}
	arr := js.Global().Get("Array").New(len(frames))
	for i, f := range frames {
		o := js.Global().Get("Object").New()
		o.Set("width", f.Width)
		o.Set("height", f.Height)
		o.Set("duration", f.Duration)
		o.Set("rgba", bytesToJS(f.RGBA))
		arr.SetIndex(i, o)
	}
	return arr
}

// infoFunc 是 webpInfo 的 JS 回调：只读出容器级元数据，不解码像素；
// 缺少入参或解析失败时返回 { error } 对象。
//
// infoFunc backs the webpInfo JS callback: it reports container-level
// metadata without decoding pixels; missing arguments or parse failures
// yield an { error } object.
func infoFunc(_ js.Value, args []js.Value) any {
	if len(args) < 1 {
		return errObj("webpInfo: missing input bytes")
	}
	data, e := bytesFromJS(args[0])
	if e != nil {
		return errObj(e.Error())
	}
	res, e := Info(data)
	if e != nil {
		return errObj(e.Error())
	}
	out := js.Global().Get("Object").New()
	out.Set("width", res.Width)
	out.Set("height", res.Height)
	out.Set("hasAlpha", res.HasAlpha)
	out.Set("hasAnimation", res.HasAnimation)
	out.Set("hasICC", res.HasICC)
	out.Set("hasEXIF", res.HasEXIF)
	out.Set("hasXMP", res.HasXMP)
	out.Set("frameCount", res.FrameCount)
	return out
}

// bytesFromJS 把 JS 的 Uint8Array / ArrayBuffer 拷贝进 Go 字节切片。
//
// bytesFromJS copies a JS Uint8Array / ArrayBuffer into a Go byte slice.
func bytesFromJS(v js.Value) ([]byte, error) {
	var view js.Value
	if v.InstanceOf(js.Global().Get("ArrayBuffer")) {
		view = js.Global().Get("Uint8Array").New(v)
	} else {
		view = v
	}
	buf := make([]byte, view.Length())
	js.CopyBytesToGo(buf, view)
	return buf, nil
}

// bytesToJS 把 Go 字节切片拷贝为 JS 侧的 Uint8Array。数组在 JS 堆上分配、
// 字节逐份拷入，因此内存归调用方所有，不存在 Go GC 生命周期风险。
//
// bytesToJS exposes a Go byte slice to JS as a Uint8Array. The array is
// allocated in JS and the bytes are copied in, so the caller owns the memory
// and there is no Go-GC lifetime hazard.
func bytesToJS(b []byte) js.Value {
	arr := js.Global().Get("Uint8Array").New(len(b))
	js.CopyBytesToJS(arr, b)
	return arr
}

// errObj 构造返回给 JS 的错误对象 { error: msg }。
//
// errObj builds the error object { error: msg } returned to JS.
func errObj(msg string) js.Value {
	o := js.Global().Get("Object").New()
	o.Set("error", msg)
	return o
}
