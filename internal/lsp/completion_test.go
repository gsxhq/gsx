package lsp

import (
	"encoding/json"
	"strings"
	"testing"
)

// completionFrame builds a textDocument/completion request frame.
func completionFrame(id int, uri string, position Position) string {
	return jsonFrame(map[string]any{
		"jsonrpc": "2.0", "id": id, "method": "textDocument/completion",
		"params": map[string]any{
			"textDocument": map[string]any{"uri": uri},
			"position":     position,
		},
	})
}

// TestInitializeAdvertisesCompletionTriggerCharacters checks the initialize
// result advertises completionProvider with the exact trigger character set,
// in order: element/attribute-name dot, tag open/close, string-attribute
// quote, and pipe (its second character, so completion fires once the pipe
// context is complete).
func TestInitializeAdvertisesCompletionTriggerCharacters(t *testing.T) {
	out := drive(t, nilAnalyzer{}, initFrame()+exitFrame())
	var res initializeResult
	if err := json.Unmarshal(responseByID(t, out, 1)["result"], &res); err != nil {
		t.Fatal(err)
	}
	got := res.Capabilities.CompletionProvider
	if got == nil {
		t.Fatalf("initialize result missing completionProvider:\n%s", out)
	}
	want := []string{".", "<", ">", "\"", "|"}
	if len(got.TriggerCharacters) != len(want) {
		t.Fatalf("triggerCharacters = %v, want %v", got.TriggerCharacters, want)
	}
	for i, c := range want {
		if got.TriggerCharacters[i] != c {
			t.Fatalf("triggerCharacters = %v, want %v", got.TriggerCharacters, want)
		}
	}
}

// TestCompletionOnOpenGSXBufferRepliesEmptyList exercises the stub handler:
// completion is now a live request path, but returns a non-null empty list
// until a later task fills it in.
func TestCompletionOnOpenGSXBufferRepliesEmptyList(t *testing.T) {
	uri := "file:///m/a.gsx"
	text := "package x\n\ncomponent Card() {\n\t<div/>\n}\n"
	out := drive(t, nilAnalyzer{}, initFrame()+didOpenFrame(uri, text)+
		completionFrame(2, uri, Position{Line: 3, Character: 2})+exitFrame())
	result := responseByID(t, out, 2)["result"]
	if got := strings.TrimSpace(string(result)); got != `{"isIncomplete":false,"items":[]}` {
		t.Fatalf("completion result = %s, want {\"isIncomplete\":false,\"items\":[]}", got)
	}
}

// TestCompletionOnGoURIRepliesNull mirrors handleHover: .go files are gopls's
// to complete.
func TestCompletionOnGoURIRepliesNull(t *testing.T) {
	uri := "file:///m/a.go"
	out := drive(t, nilAnalyzer{}, initFrame()+
		completionFrame(2, uri, Position{Line: 0, Character: 0})+exitFrame())
	result := responseByID(t, out, 2)["result"]
	if got := strings.TrimSpace(string(result)); got != "null" {
		t.Fatalf("completion result for .go = %s, want null", got)
	}
}
