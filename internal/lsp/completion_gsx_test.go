package lsp

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"
)

// TestFilterItems checks filterItems' shape: label = Name, detail =
// "Pkg.Func" (with a " (ctx)" suffix when WantsCtx), kind = ciKindFunction,
// tier = tierContext, and every item's TextEdit replaces the given [start,end)
// span. Order is preserved verbatim (filters arrives pre-sorted from
// Package.Filters).
func TestFilterItems(t *testing.T) {
	fs := []FilterCandidate{
		{Name: "upper", Pkg: "github.com/gsxhq/gsx/std", Func: "Upper"},
		{Name: "urlFor", Pkg: "example.com/sp", Func: "URLFor", WantsCtx: true},
	}
	text := "{ x |> up }"
	items := filterItems(fs, text, 7, 9, encUTF8)
	if len(items) != 2 {
		t.Fatalf("len(items) = %d, want 2", len(items))
	}

	if items[0].Label != "upper" {
		t.Errorf("items[0].Label = %q, want %q", items[0].Label, "upper")
	}
	if items[1].Label != "urlFor" {
		t.Errorf("items[1].Label = %q, want %q", items[1].Label, "urlFor")
	}

	wantDetail0 := "github.com/gsxhq/gsx/std.Upper"
	if items[0].Detail != wantDetail0 {
		t.Errorf("items[0].Detail = %q, want %q", items[0].Detail, wantDetail0)
	}
	if !strings.HasSuffix(items[1].Detail, "(ctx)") {
		t.Errorf("items[1].Detail = %q, want suffix %q", items[1].Detail, "(ctx)")
	}
	if strings.HasSuffix(items[0].Detail, "(ctx)") {
		t.Errorf("items[0].Detail = %q, must not carry the ctx suffix (WantsCtx=false)", items[0].Detail)
	}

	for i, it := range items {
		if it.Kind != ciKindFunction {
			t.Errorf("items[%d].Kind = %d, want ciKindFunction", i, it.Kind)
		}
		if !strings.HasPrefix(it.SortText, "05") {
			t.Errorf("items[%d].SortText = %q, want tierContext (05) prefix", i, it.SortText)
		}
		if it.TextEdit == nil {
			t.Fatalf("items[%d].TextEdit is nil", i)
		}
		wantRange := rangeForSpan(text, 7, 9, encUTF8)
		if it.TextEdit.Range != wantRange {
			t.Errorf("items[%d].TextEdit.Range = %+v, want %+v", i, it.TextEdit.Range, wantRange)
		}
	}
}

// TestFilterItemsEmpty checks the empty-table case returns an empty (non-nil
// vs nil is not asserted; callers only check len) slice, not a panic.
func TestFilterItemsEmpty(t *testing.T) {
	items := filterItems(nil, "{ x |> u }", 7, 8, encUTF8)
	if len(items) != 0 {
		t.Fatalf("len(items) = %d, want 0", len(items))
	}
}

// pipeFilterAnalyzer is a fake Analyzer (embeds nilAnalyzer for the unused
// methods) whose AnalyzeEphemeral and Analyze are scripted independently, so
// tests can drive both the ephemeral path and the s.pkgs[dir] fallback path.
type pipeFilterAnalyzer struct {
	nilAnalyzer
	ephPkg   *Package
	ephErr   error
	analyzed *Package // returned by Analyze, populates s.pkgs[dir] via didOpen
}

func (a pipeFilterAnalyzer) AnalyzeEphemeral(string, string, []byte) (*Package, error) {
	return a.ephPkg, a.ephErr
}

func (a pipeFilterAnalyzer) Analyze(string, map[string][]byte) (*Package, error) {
	if a.analyzed != nil {
		return a.analyzed, nil
	}
	return &Package{}, nil
}

// TestPipeStageCompletionFromEphemeral drives textDocument/completion at a
// pipe-stage cursor and checks the resolved filter table (returned by a fake
// AnalyzeEphemeral) becomes completion items.
func TestPipeStageCompletionFromEphemeral(t *testing.T) {
	uri := "file:///m/a.gsx"
	text := "package p\n\ncomponent C(x string) {\n\t<div>{ x |> up }</div>\n}\n"
	off := strings.Index(text, "|> up") + len("|> up")
	pos := positionForByteOffset(text, off, encUTF16)

	a := pipeFilterAnalyzer{ephPkg: &Package{Filters: []FilterCandidate{
		{Name: "upper", Pkg: "github.com/gsxhq/gsx/std", Func: "Upper"},
		{Name: "urlquery", Pkg: "github.com/gsxhq/gsx/std", Func: "Urlquery"},
	}}}

	out := drive(t, a, initFrame()+didOpenFrame(uri, text)+
		completionFrame(2, uri, pos)+exitFrame())
	items := decodeCompletionItems(t, out, 2)
	labels := map[string]bool{}
	for _, it := range items {
		labels[it.Label] = true
	}
	if !labels["upper"] || !labels["urlquery"] {
		t.Fatalf("pipe-stage completion labels = %v, want upper+urlquery present", labels)
	}
}

// TestPipeStageCompletionFallsBackToRetainedPackage checks the fallback rule:
// when AnalyzeEphemeral comes back a shell (nil Info AND empty Filters — here,
// an error), the handler serves the retained s.pkgs[dir] snapshot's Filters
// instead of an empty list.
func TestPipeStageCompletionFallsBackToRetainedPackage(t *testing.T) {
	uri := "file:///m/a.gsx"
	text := "package p\n\ncomponent C(x string) {\n\t<div>{ x |> up }</div>\n}\n"
	off := strings.Index(text, "|> up") + len("|> up")
	pos := positionForByteOffset(text, off, encUTF16)

	a := pipeFilterAnalyzer{
		ephErr: errors.New("ephemeral analysis failed"),
		analyzed: &Package{Filters: []FilterCandidate{
			{Name: "lower", Pkg: "github.com/gsxhq/gsx/std", Func: "Lower"},
		}},
	}

	out := drive(t, a, initFrame()+didOpenFrame(uri, text)+
		completionFrame(2, uri, pos)+exitFrame())
	items := decodeCompletionItems(t, out, 2)
	if len(items) != 1 || items[0].Label != "lower" {
		t.Fatalf("pipe-stage fallback items = %v, want exactly [lower]", items)
	}
}

// TestPipeStageCompletionBothEmpty checks that when both the ephemeral and the
// retained package are shells/absent, the handler answers an empty list, never
// an error.
func TestPipeStageCompletionBothEmpty(t *testing.T) {
	uri := "file:///m/a.gsx"
	text := "package p\n\ncomponent C(x string) {\n\t<div>{ x |> up }</div>\n}\n"
	off := strings.Index(text, "|> up") + len("|> up")
	pos := positionForByteOffset(text, off, encUTF16)

	a := pipeFilterAnalyzer{ephErr: errors.New("ephemeral analysis failed")}

	out := drive(t, a, initFrame()+didOpenFrame(uri, text)+
		completionFrame(2, uri, pos)+exitFrame())
	items := decodeCompletionItems(t, out, 2)
	if len(items) != 0 {
		t.Fatalf("pipe-stage items = %v, want empty", items)
	}
}

// decodeCompletionItems unmarshals the textDocument/completion response with
// the given id into its Items slice.
func decodeCompletionItems(t *testing.T, out string, id int) []CompletionItem {
	t.Helper()
	var list CompletionList
	if err := json.Unmarshal(responseByID(t, out, id)["result"], &list); err != nil {
		t.Fatalf("decode completion result: %v\n%s", err, out)
	}
	return list.Items
}
