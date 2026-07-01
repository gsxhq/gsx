//go:build js && wasm

package main

import (
	"bytes"
	"encoding/json"
	"go/token"
	"reflect"
	"testing"

	"github.com/gsxhq/gsx/internal/diag"
)

func TestJSDiagsMatchesRenderJSON(t *testing.T) {
	ds := []diag.Diagnostic{{
		Start:    testPos("source.gsx", 3, 11),
		End:      testPos("source.gsx", 3, 18),
		Severity: diag.Error,
		Code:     "type-error",
		Message:  "undefined: missng",
		Help:     "check the identifier spelling",
		Source:   "types",
	}}

	var wantBuf bytes.Buffer
	if err := diag.RenderJSON(&wantBuf, ds); err != nil {
		t.Fatal(err)
	}
	var want []any
	if err := json.Unmarshal(wantBuf.Bytes(), &want); err != nil {
		t.Fatal(err)
	}

	if got := jsDiags(ds); !reflect.DeepEqual(got, want) {
		gotJSON, _ := json.Marshal(got)
		t.Fatalf("WASM diagnostics shape drift:\n got=%s\nwant=%s", gotJSON, bytes.TrimSpace(wantBuf.Bytes()))
	}
}

func testPos(filename string, line, col int) token.Position {
	return token.Position{Filename: filename, Line: line, Column: col}
}
