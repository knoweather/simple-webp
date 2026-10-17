# Build the WASM decoder for the browser (Windows).
# Usage:  pwsh wasm/build.ps1
$ErrorActionPreference = "Stop"

$go = $env:GO_BIN
if (-not $go) {
    # managed toolchain used by this project
    $go = "C:\Users\64517\go\go1.27.1\bin\go.exe"
}
if (-not (Test-Path $go)) { $go = "go" }

$goroot = & $go env GOROOT
$env:GOOS = "js"
$env:GOARCH = "wasm"

& $go build -o wasm/example/main.wasm ./wasm/main
if ($LASTEXITCODE -ne 0) { exit $LASTEXITCODE }

Copy-Item "$goroot/lib/wasm/wasm_exec.js" -Destination wasm/example/wasm_exec.js -Force
Write-Host "built wasm/example/main.wasm (+ wasm_exec.js)"
