//go:build js && wasm

// Command gsx-wasm is the client-side playground engine: it builds the gsx
// transform+render Playground from the embedded type bundle (no go list, no
// subprocess) and exposes gsxTransform(source, invoke) -> {code, html,
// diagnostics}. Build with
//
//	GOOS=js GOARCH=wasm go build -o gsx.wasm ./playground/wasm
//
// then load it in the browser alongside Go's wasm_exec.js.
package main

import (
	"syscall/js"

	"github.com/gsxhq/gsx/playground/interp"
)

func main() {
	pg, err := interp.New()
	if err != nil {
		panic("gsx-wasm: build playground: " + err.Error())
	}

	js.Global().Set("gsxTransform", js.FuncOf(func(_ js.Value, args []js.Value) any {
		src, invoke := "", ""
		if len(args) > 0 && args[0].Type() == js.TypeString {
			src = args[0].String()
		}
		if len(args) > 1 && args[1].Type() == js.TypeString {
			invoke = args[1].String()
		}
		return jsResult(pg.Transform(src, invoke))
	}))

	if ready := js.Global().Get("gsxReady"); ready.Type() == js.TypeFunction {
		ready.Invoke()
	}
	select {}
}

func jsResult(r interp.Result) any {
	diags := make([]any, len(r.Diagnostics))
	for i, d := range r.Diagnostics {
		diags[i] = map[string]any{
			"severity": d.Severity,
			"message":  d.Message,
			"line":     d.Line,
			"column":   d.Column,
		}
	}
	return map[string]any{
		"code":        r.Code,
		"html":        r.HTML,
		"diagnostics": diags,
	}
}
