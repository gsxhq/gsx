//go:build js && wasm

// Command gsx-wasm is the client-side playground transform: it builds the gsx
// resolver from the embedded type bundle (no go list, no subprocess) and exposes
// a JS-callable gsxTransform(source) -> {code, diagnostics}. Build with
//
//	GOOS=js GOARCH=wasm go build -o gsx.wasm ./playground/wasm
//
// then load it in the browser alongside Go's wasm_exec.js.
package main

import (
	"syscall/js"

	"github.com/gsxhq/gsx/gen"
	"github.com/gsxhq/gsx/playground/playbundle"
)

func main() {
	resolver, err := playbundle.NewResolver()
	if err != nil {
		panic("gsx-wasm: build resolver: " + err.Error())
	}

	js.Global().Set("gsxTransform", js.FuncOf(func(_ js.Value, args []js.Value) any {
		if len(args) < 1 || args[0].Type() != js.TypeString {
			return jsResult("", []map[string]any{{"severity": "error", "message": "gsxTransform expects (source string)"}})
		}
		return transform(resolver, args[0].String())
	}))

	// Tell the host the function is registered, then keep the runtime alive so the
	// exported callback stays callable.
	if ready := js.Global().Get("gsxReady"); ready.Type() == js.TypeFunction {
		ready.Invoke()
	}
	select {}
}

// transform runs the in-memory gsx transform and shapes the result for JS.
func transform(resolver *gen.CachedResolver, src string) any {
	res, _ := resolver.GenerateSource("source.gsx", []byte(src))
	var code string
	for _, b := range res.Files {
		code = string(b) // single virtual source -> single output
	}
	diags := make([]map[string]any, 0, len(res.Diags))
	for _, d := range res.Diags {
		diags = append(diags, map[string]any{
			"severity": d.Severity.String(),
			"code":     d.Code,
			"message":  d.Message,
			"line":     d.Start.Line,
			"column":   d.Start.Column,
		})
	}
	return jsResult(code, diags)
}

func jsResult(code string, diags []map[string]any) any {
	js := make([]any, len(diags))
	for i, d := range diags {
		js[i] = d
	}
	return map[string]any{"code": code, "diagnostics": js}
}
