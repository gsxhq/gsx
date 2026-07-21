package lsp

import (
	"encoding/json"
	"slices"
	"testing"
)

func TestPublishDiagnosticsParamsJSON(t *testing.T) {
	p := publishDiagnosticsParams{
		URI: "file:///x/page.gsx",
		Diagnostics: []Diagnostic{{
			Range:    Range{Start: Position{Line: 2, Character: 5}, End: Position{Line: 2, Character: 9}},
			Severity: 1,
			Code:     "type-error",
			Source:   "types",
			Message:  "undefined: foo",
		}},
	}
	b, err := json.Marshal(p)
	if err != nil {
		t.Fatal(err)
	}
	got := string(b)
	want := `{"uri":"file:///x/page.gsx","diagnostics":[{"range":{"start":{"line":2,"character":5},"end":{"line":2,"character":9}},"severity":1,"code":"type-error","source":"types","message":"undefined: foo"}]}`
	if got != want {
		t.Fatalf("\n got: %s\nwant: %s", got, want)
	}
}

func TestInitializeParamsParse(t *testing.T) {
	in := `{"capabilities":{"general":{"positionEncodings":["utf-8","utf-16"]}},"rootUri":"file:///fallback","workspaceFolders":[{"uri":"file:///workspace","name":"workspace"}]}`
	var p initializeParams
	if err := json.Unmarshal([]byte(in), &p); err != nil {
		t.Fatal(err)
	}
	if len(p.Capabilities.General.PositionEncodings) != 2 || p.Capabilities.General.PositionEncodings[0] != "utf-8" {
		t.Fatalf("encodings = %v", p.Capabilities.General.PositionEncodings)
	}
	if p.RootURI != "file:///fallback" {
		t.Fatalf("root URI = %q", p.RootURI)
	}
	if want := []workspaceFolder{{URI: "file:///workspace", Name: "workspace"}}; !slices.Equal(p.WorkspaceFolders, want) {
		t.Fatalf("workspace folders = %+v, want %+v", p.WorkspaceFolders, want)
	}
}
