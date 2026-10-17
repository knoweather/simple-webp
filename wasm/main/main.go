//go:build js && wasm

// Command wasm is the WebAssembly entry point. Building it with
//
//	GOOS=js GOARCH=wasm go build -o main.wasm ./wasm/main
//
// produces a module that, once instantiated, exposes webpDecode / webpInfo on
// the global object (see the wasm package's Register).
package main

import "github.com/knoweather/simple-webp/wasm"

func main() {
	wasm.Register()
	// Block forever so the Go runtime — and the JS callbacks it registered —
	// stay alive for the lifetime of the page.
	select {}
}
