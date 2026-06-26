//go:build js && wasm

package playbundle

import (
	"strings"
	"testing"
)

// TestTransformRunsUnderWASM executes the gsx transform inside the js/wasm
// runtime itself (run via GOOS=js GOARCH=wasm go test + go_js_wasm_exec). It is
// the proof that the entire stack — go/types, gcexportdata, and gsx codegen —
// works in WASM, resolving against the embedded bundle with no filesystem and no
// subprocess (neither exists in a browser).
func TestTransformRunsUnderWASM(t *testing.T) {
	r, err := NewResolver()
	if err != nil {
		t.Fatalf("NewResolver from embedded bundle: %v", err)
	}
	const src = `package main

component Greeting(name string) {
	<p>Hi { name |> upper }</p>
}
`
	res, err := r.GenerateSource("greeting.gsx", []byte(src))
	if err != nil {
		t.Fatalf("GenerateSource: %v (diags=%v)", err, res.Diags)
	}
	if len(res.Diags) != 0 {
		t.Fatalf("unexpected diagnostics: %v", res.Diags)
	}
	var out string
	for _, b := range res.Files {
		out = string(b)
	}
	if !strings.Contains(out, "Upper(") {
		t.Fatalf("WASM transform missing the bundled std.Upper call:\n%s", out)
	}
}
