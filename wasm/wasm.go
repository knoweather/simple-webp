//go:build js && wasm

package wasm

import "syscall/js"

// Register installs the WebP decode functions on the JavaScript global object:
//
//	webpDecode(bytes) -> { width, height, hasAlpha, hasAnimation, rgba } | { error }
//	webpInfo(bytes)   -> { width, height, hasAlpha, hasAnimation }       | { error }
//
// bytes may be a Uint8Array or an ArrayBuffer. rgba is a Uint8Array of
// straight-alpha pixels (row-major, Width*Height*4). Consume it synchronously
// (e.g. new ImageData(new Uint8ClampedArray(res.rgba), w, h)); the backing
// memory is Go-managed and must not be retained across wasm calls.
func Register() {
	js.Global().Set("webpDecode", js.FuncOf(decodeFunc))
	js.Global().Set("webpInfo", js.FuncOf(infoFunc))
}

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
	return out
}

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

// bytesToJS exposes a Go byte slice to JS as a Uint8Array. The array is
// allocated in JS and the bytes are copied in, so the caller owns the memory
// and there is no Go-GC lifetime hazard.
func bytesToJS(b []byte) js.Value {
	arr := js.Global().Get("Uint8Array").New(len(b))
	js.CopyBytesToJS(arr, b)
	return arr
}

func errObj(msg string) js.Value {
	o := js.Global().Get("Object").New()
	o.Set("error", msg)
	return o
}
