#!/usr/bin/env bash
# Build the WASM decoder for the browser.
# Usage:  ./wasm/build.sh
set -euo pipefail

GO="${GO_BIN:-go}"
export GOOS=js GOARCH=wasm

"$GO" build -o wasm/example/main.wasm ./wasm/main
cp "$("$GO" env GOROOT)/lib/wasm/wasm_exec.js" wasm/example/wasm_exec.js
echo "built wasm/example/main.wasm (+ wasm_exec.js)"
