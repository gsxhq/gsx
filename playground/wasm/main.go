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

	"github.com/gsxhq/gsx/gen"
	"github.com/gsxhq/gsx/playground/interp"
)

func main() {
	pg, err := interp.New()
	if err != nil {
		panic("gsx-wasm: build playground: " + err.Error())
	}

	// gsxTransform(source, invoke) -> {html, generatedGo, diagnostics}.
	js.Global().Set("gsxTransform", js.FuncOf(func(_ js.Value, args []js.Value) any {
		src, invoke := stringArg(args, 0), stringArg(args, 1)
		return jsResult(pg.Transform(src, invoke))
	}))

	// gsxFormat(source) -> {formatted} | {error} — the gsx formatter (pure, no
	// subprocess), so `gsx fmt` also runs client-side.
	js.Global().Set("gsxFormat", js.FuncOf(func(_ js.Value, args []js.Value) any {
		out, ferr := gen.Format("playground.gsx", []byte(stringArg(args, 0)))
		if ferr != nil {
			return map[string]any{"error": ferr.Error()}
		}
		return map[string]any{"formatted": string(out)}
	}))

	if ready := js.Global().Get("gsxReady"); ready.Type() == js.TypeFunction {
		ready.Invoke()
	}
	select {}
}

func stringArg(args []js.Value, i int) string {
	if i < len(args) && args[i].Type() == js.TypeString {
		return args[i].String()
	}
	return ""
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
		"generatedGo": r.Code,
		"html":        r.HTML,
		"diagnostics": diags,
	}
}
