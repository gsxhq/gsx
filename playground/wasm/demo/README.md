# gsx WASM transform demo

A standalone, server-free harness proving the gsx transform runs client-side.

```sh
GOOS=js GOARCH=wasm go build -o gsx.wasm ../   # build the module
cp "$(go env GOROOT)/lib/wasm/wasm_exec.js" .  # Go's JS loader
python3 -m http.server 8765                     # serve (wasm needs http, not file://)
# open http://localhost:8765/
```

Type gsx into the left pane; the generated Go (or positioned diagnostics) appears
instantly — no network, no Go toolchain in the browser. gsx.wasm and wasm_exec.js
are build artifacts (gitignored).
