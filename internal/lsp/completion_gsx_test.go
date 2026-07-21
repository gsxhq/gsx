package lsp

import (
	"encoding/json"
	"errors"
	"go/types"
	"strings"
	"testing"

	"github.com/gsxhq/gsx/internal/sourceintel"
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

// componentTagFixturePackage builds a *Package whose Types is a synthesized
// go/types.Package (path "example.com/app/page", built directly via
// types.NewPackage/SetImports — no source to parse or real import to
// resolve) importing one gsx sibling package (path "example.com/app/ui", name
// "ui"). ComponentDecls covers three shapes: a plain local component
// ("Card"), a local receiver/method component ("Page.Row"), and a plain
// component in the imported package ("Button").
//
// ComponentKey format — VERIFIED against componentObjectKey (definition.go)
// and crossRefKeyForFunc (gen/lsp.go): a plain function component keys as
// "."+Name (a LEADING dot marker), not the bare name the plan assumed; a
// receiver component keys as RecvType+"."+Name. Every key therefore contains
// a dot — componentTagItems' exclusion rule (plainComponentName) checks the
// dot's position, not its presence.
func componentTagFixturePackage() *Package {
	imp := types.NewPackage("example.com/app/ui", "ui")
	imp.MarkComplete()
	main := types.NewPackage("example.com/app/page", "page")
	main.SetImports([]*types.Package{imp})
	main.MarkComplete()
	return &Package{
		Types: main,
		ComponentDecls: map[ComponentDeclKey][]sourceintel.VersionedSpan{
			{PackagePath: "example.com/app/page", ComponentKey: ".Card"}:    nil,
			{PackagePath: "example.com/app/page", ComponentKey: "Page.Row"}: nil,
			{PackagePath: "example.com/app/ui", ComponentKey: ".Button"}:    nil,
		},
	}
}

// TestComponentTagItemsBareCursor checks the qualifier=="" candidate list: the
// local plain component ("Card"), NOT the local receiver component
// ("Page.Row" — needs a receiver expression, excluded from v1), plus one
// qualifier item for the imported gsx package that declares components
// ("ui", inserting "ui."). Order is local components (sorted) then import
// qualifiers (sorted) — deterministic despite ComponentDecls being a map.
func TestComponentTagItemsBareCursor(t *testing.T) {
	pkg := componentTagFixturePackage()
	items := componentTagItems(pkg, "", false, "", 0, 0, encUTF8)
	if len(items) != 2 {
		t.Fatalf("len(items) = %d, want 2: %+v", len(items), items)
	}
	if items[0].Label != "Card" || items[0].Kind != ciKindFunction {
		t.Errorf("items[0] = %+v, want label Card kind ciKindFunction", items[0])
	}
	if items[0].TextEdit == nil || items[0].TextEdit.NewText != "Card" {
		t.Errorf("items[0].TextEdit = %+v, want NewText %q", items[0].TextEdit, "Card")
	}
	if items[1].Label != "ui" || items[1].Kind != ciKindModule {
		t.Errorf("items[1] = %+v, want label ui kind ciKindModule", items[1])
	}
	if items[1].TextEdit == nil || items[1].TextEdit.NewText != "ui." {
		t.Errorf("items[1].TextEdit = %+v, want NewText %q", items[1].TextEdit, "ui.")
	}
	for i, it := range items {
		if !strings.HasPrefix(it.SortText, "05") {
			t.Errorf("items[%d].SortText = %q, want tierContext (05) prefix", i, it.SortText)
		}
	}
}

// TestComponentTagItemsQualified checks qualifier=="ui": only the imported
// package's plain component ("Button"), dot-free.
func TestComponentTagItemsQualified(t *testing.T) {
	pkg := componentTagFixturePackage()
	items := componentTagItems(pkg, "ui", false, "", 0, 0, encUTF8)
	if len(items) != 1 {
		t.Fatalf("len(items) = %d, want 1: %+v", len(items), items)
	}
	if items[0].Label != "Button" || items[0].Kind != ciKindFunction {
		t.Errorf("items[0] = %+v, want label Button kind ciKindFunction", items[0])
	}
	if items[0].TextEdit == nil || items[0].TextEdit.NewText != "Button" {
		t.Errorf("items[0].TextEdit = %+v, want NewText %q", items[0].TextEdit, "Button")
	}
}

// TestComponentTagItemsQualifiedUnknownImport checks that a qualifier
// matching no import's Name() yields an empty list, not a panic.
func TestComponentTagItemsQualifiedUnknownImport(t *testing.T) {
	pkg := componentTagFixturePackage()
	if items := componentTagItems(pkg, "nope", false, "", 0, 0, encUTF8); len(items) != 0 {
		t.Fatalf("items = %+v, want empty for an unresolvable qualifier", items)
	}
}

// TestComponentTagItemsNilTypesFailsSoft checks the nil-Types guard: without
// Types there is no way to resolve the current package path or imports, so
// componentTagItems offers nothing rather than guessing.
func TestComponentTagItemsNilTypesFailsSoft(t *testing.T) {
	pkg := &Package{ComponentDecls: map[ComponentDeclKey][]sourceintel.VersionedSpan{
		{PackagePath: "example.com/app/page", ComponentKey: ".Card"}: nil,
	}}
	if items := componentTagItems(pkg, "", false, "", 0, 0, encUTF8); len(items) != 0 {
		t.Fatalf("items = %+v, want empty when pkg.Types is nil", items)
	}
}

// tagCompletionAnalyzer is a fake Analyzer (embeds nilAnalyzer for the unused
// methods) whose AnalyzeEphemeral and Analyze are scripted independently, so
// tests can drive both the ephemeral path and the s.pkgs[dir] fallback path —
// mirrors pipeFilterAnalyzer.
type tagCompletionAnalyzer struct {
	nilAnalyzer
	ephPkg   *Package
	ephErr   error
	analyzed *Package // returned by Analyze, populates s.pkgs[dir] via didOpen
}

func (a tagCompletionAnalyzer) AnalyzeEphemeral(string, string, []byte) (*Package, error) {
	return a.ephPkg, a.ephErr
}

func (a tagCompletionAnalyzer) Analyze(string, map[string][]byte) (*Package, error) {
	if a.analyzed != nil {
		return a.analyzed, nil
	}
	return &Package{}, nil
}

// TestTagCompletionFromEphemeral drives textDocument/completion at a bare tag
// cursor and checks the resolved component package (returned by a fake
// AnalyzeEphemeral) becomes completion items — local component present,
// import qualifier present, local receiver component absent.
func TestTagCompletionFromEphemeral(t *testing.T) {
	uri := "file:///m/a.gsx"
	text := "package p\n\ncomponent C() {\n\t<div><Ca</div>\n}\n"
	off := strings.Index(text, "<Ca") + len("<Ca")
	pos := positionForByteOffset(text, off, encUTF16)

	a := tagCompletionAnalyzer{ephPkg: componentTagFixturePackage()}

	out := drive(t, a, initFrame()+didOpenFrame(uri, text)+
		completionFrame(2, uri, pos)+exitFrame())
	items := decodeCompletionItems(t, out, 2)
	labels := map[string]bool{}
	for _, it := range items {
		labels[it.Label] = true
	}
	if !labels["Card"] || !labels["ui"] {
		t.Fatalf("tag completion labels = %v, want Card+ui present", labels)
	}
	if labels["Row"] {
		t.Fatalf("tag completion labels = %v, must exclude the receiver component", labels)
	}
}

// TestTagCompletionFallsBackToRetainedPackage checks the fallback rule: when
// AnalyzeEphemeral comes back a shell (here, an error), the handler serves
// the retained s.pkgs[dir] snapshot instead of an empty list.
func TestTagCompletionFallsBackToRetainedPackage(t *testing.T) {
	uri := "file:///m/a.gsx"
	text := "package p\n\ncomponent C() {\n\t<div><Ca</div>\n}\n"
	off := strings.Index(text, "<Ca") + len("<Ca")
	pos := positionForByteOffset(text, off, encUTF16)

	a := tagCompletionAnalyzer{
		ephErr:   errors.New("ephemeral analysis failed"),
		analyzed: componentTagFixturePackage(),
	}

	out := drive(t, a, initFrame()+didOpenFrame(uri, text)+
		completionFrame(2, uri, pos)+exitFrame())
	items := decodeCompletionItems(t, out, 2)
	labels := map[string]bool{}
	for _, it := range items {
		labels[it.Label] = true
	}
	if !labels["Card"] || !labels["ui"] {
		t.Fatalf("tag completion fallback labels = %v, want Card+ui present", labels)
	}
}

// TestTagCompletionBothEmpty checks that when both the ephemeral and the
// retained package are shells/absent, the handler answers an empty list,
// never an error.
func TestTagCompletionBothEmpty(t *testing.T) {
	uri := "file:///m/a.gsx"
	text := "package p\n\ncomponent C() {\n\t<div><Ca</div>\n}\n"
	off := strings.Index(text, "<Ca") + len("<Ca")
	pos := positionForByteOffset(text, off, encUTF16)

	a := tagCompletionAnalyzer{ephErr: errors.New("ephemeral analysis failed")}

	out := drive(t, a, initFrame()+didOpenFrame(uri, text)+
		completionFrame(2, uri, pos)+exitFrame())
	items := decodeCompletionItems(t, out, 2)
	if len(items) != 0 {
		t.Fatalf("tag completion items = %v, want empty", items)
	}
}
