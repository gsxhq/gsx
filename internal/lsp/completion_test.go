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

// TestCompletionOnNoCandidateContextRepliesEmptyList checks that a cursor in a
// context with no candidates (here, inside plain markup text — ctxNone) still
// answers a well-formed, non-null EMPTY list rather than null or an error.
// (Tag/attr/value positions are now live; a text-content cursor is not.)
func TestCompletionOnNoCandidateContextRepliesEmptyList(t *testing.T) {
	uri := "file:///m/a.gsx"
	text := "package x\n\ncomponent Card() {\n\t<div>hi</div>\n}\n"
	// Line 3 "\t<div>hi</div>": tab + "<div>" = 6 columns, so char 7 sits between
	// the "h" and "i" of the text child — plain markup text, ctxNone.
	out := drive(t, nilAnalyzer{}, initFrame()+didOpenFrame(uri, text)+
		completionFrame(2, uri, Position{Line: 3, Character: 7})+exitFrame())
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
