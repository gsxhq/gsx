package lsp

import (
	"encoding/json"
	"errors"
	"go/token"
	"go/types"
	"strings"
	"testing"

	gsxast "github.com/gsxhq/gsx/ast"
	"github.com/gsxhq/gsx/internal/sourceintel"
	gsxparser "github.com/gsxhq/gsx/parser"
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

// TestComponentTagItemsAliasedImport checks that qualifier resolution goes
// through file-scope *types.PkgName objects (see importQualifierCandidates),
// not pkg.Types.Imports()[i].Name() — which returns a package's DECLARED
// name and is blind to a local import alias. The fixture aliases the real
// "strings" package as "myui" (buildSyntheticPackage type-checks plain Go
// source, so Info.Scopes carries real PkgName objects; ComponentDecls is
// then attached by hand, keyed on "strings" — the import PATH, not its local
// name — exactly like a real gsx sibling package would be). A `<myui.`
// cursor must resolve "strings"'s (fake) components, and the bare-cursor
// qualifier item must insert "myui.", never the declared name "strings.".
func TestComponentTagItemsAliasedImport(t *testing.T) {
	src := `package p

import myui "strings"

var _ = myui.ToUpper
`
	pkg, _ := buildSyntheticPackage(t, src)
	pkg.ComponentDecls = map[ComponentDeclKey][]sourceintel.VersionedSpan{
		{PackagePath: "strings", ComponentKey: ".Button"}: nil,
	}

	items := componentTagItems(pkg, "myui", false, "", 0, 0, encUTF8)
	if len(items) != 1 || items[0].Label != "Button" {
		t.Fatalf("qualifier=%q items = %+v, want exactly [Button]", "myui", items)
	}

	// The declared name ("strings") must NOT resolve — only the alias does.
	if items := componentTagItems(pkg, "strings", false, "", 0, 0, encUTF8); len(items) != 0 {
		t.Fatalf("qualifier=%q items = %+v, want empty (declared name is not the local name)", "strings", items)
	}

	bareItems := componentTagItems(pkg, "", false, "", 0, 0, encUTF8)
	var qualItem *CompletionItem
	for i := range bareItems {
		if bareItems[i].Kind == ciKindModule {
			qualItem = &bareItems[i]
		}
	}
	if qualItem == nil {
		t.Fatalf("bare-cursor items = %+v, want a qualifier item", bareItems)
	}
	if qualItem.Label != "myui" {
		t.Errorf("qualifier item Label = %q, want %q (the alias)", qualItem.Label, "myui")
	}
	if qualItem.TextEdit == nil || qualItem.TextEdit.NewText != "myui." {
		t.Errorf("qualifier item TextEdit = %+v, want NewText %q", qualItem.TextEdit, "myui.")
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

// componentAttrFixture parses src (which must contain exactly one element
// whose Tag == tag) and returns a *Package/element pair wired for
// componentAttrItems: GSXFset resolves el's (and its attrs') real positions,
// and ComponentCalls[el] carries a hand-synthesized Signature — (ctx
// context.Context, title string, count int, children gsx.Node) — built via
// types.NewSignatureType/types.NewVar (no real gsx types needed; only names
// and reserved-ness matter to componentAttrItems). Every attr already present
// on el is bound in fact.Params keyed by its own position (so
// attrUnderCursor can find it), with ComponentParamFact.Name set from a
// caller-supplied attr->param-name map.
func componentAttrFixture(t *testing.T, src, tag string, bound map[string]string) (pkg *Package, el *gsxast.Element) {
	t.Helper()
	fset := token.NewFileSet()
	f, err := gsxparser.ParseFile(fset, "page.gsx", []byte(src), 0)
	if err != nil {
		t.Fatalf("ParseFile: %v", err)
	}
	gsxast.Inspect(f, func(n gsxast.Node) bool {
		if e, ok := n.(*gsxast.Element); ok && e.Tag == tag {
			el = e
		}
		return true
	})
	if el == nil {
		t.Fatalf("no <%s> element found in source", tag)
	}

	ctxType := types.NewInterfaceType(nil, nil)
	params := types.NewTuple(
		types.NewVar(token.NoPos, nil, "ctx", ctxType),
		types.NewVar(token.NoPos, nil, "title", types.Typ[types.String]),
		types.NewVar(token.NoPos, nil, "count", types.Typ[types.Int]),
		types.NewVar(token.NoPos, nil, "children", ctxType),
	)
	sig := types.NewSignatureType(nil, nil, nil, params, nil, false)

	boundParams := map[gsxast.Attr]ComponentParamFact{}
	for _, attr := range el.Attrs {
		name, ok := attrName(attr)
		if !ok {
			continue
		}
		if paramName, ok := bound[name]; ok {
			boundParams[attr] = ComponentParamFact{Name: paramName}
		}
	}

	pkg = &Package{
		GSXFset: fset,
		Files:   map[string]*gsxast.File{"page.gsx": f},
		ComponentCalls: map[*gsxast.Element]ComponentCallFact{
			el: {Signature: sig, Params: boundParams},
		},
	}
	return pkg, el
}

// TestComponentAttrItems checks the core candidate rule: signature params
// minus reserved names (ctx, children — both excluded regardless of any
// binding) minus the ALREADY-bound "title" (a real authored attr elsewhere on
// the tag, cursor not on it) leaves exactly "count".
func TestComponentAttrItems(t *testing.T) {
	src := "package page\n\ncomponent Home() {\n\t<Card title=\"x\"/>\n}\n"
	pkg, el := componentAttrFixture(t, src, "Card", map[string]string{"title": "title"})

	// Cursor position unrelated to the "title" attr's own span (e.g. right
	// after the tag name, in the whitespace before "title").
	cursor := strings.Index(src, "<Card") + len("<Card")
	items := componentAttrItems(pkg, el, src, cursor, cursor, encUTF8)

	if len(items) != 1 {
		t.Fatalf("items = %+v, want exactly 1 (count)", items)
	}
	if items[0].Label != "count" {
		t.Errorf("items[0].Label = %q, want %q", items[0].Label, "count")
	}
	if items[0].Kind != ciKindField {
		t.Errorf("items[0].Kind = %d, want ciKindField", items[0].Kind)
	}
	if !strings.HasPrefix(items[0].SortText, "05") {
		t.Errorf("items[0].SortText = %q, want tierContext (05) prefix", items[0].SortText)
	}
	if items[0].Detail != "int" {
		t.Errorf("items[0].Detail = %q, want %q", items[0].Detail, "int")
	}
}

// TestComponentAttrItemsCursorOnBoundAttrStaysOffered checks the phantom-heal
// interaction called out in the task-13 brief: when the cursor sits ON the
// authored token that the planner bound (e.g. `<Card title` cursor right
// after "title", still mid-typing — no value attached yet), the exclusion
// rule must NOT hide "title" — excluding it would hide the very candidate
// the user is completing. "count" stays offered too (it was never bound).
func TestComponentAttrItemsCursorOnBoundAttrStaysOffered(t *testing.T) {
	src := "package page\n\ncomponent Home() {\n\t<Card title/>\n}\n"
	pkg, el := componentAttrFixture(t, src, "Card", map[string]string{"title": "title"})

	nameStart := strings.Index(src, "title")
	nameEnd := nameStart + len("title")
	items := componentAttrItems(pkg, el, src, nameStart, nameEnd, encUTF8)

	labels := map[string]bool{}
	for _, it := range items {
		labels[it.Label] = true
	}
	if !labels["title"] {
		t.Errorf("labels = %v, want %q present (cursor is on its own token)", labels, "title")
	}
	if !labels["count"] {
		t.Errorf("labels = %v, want %q present (never bound)", labels, "count")
	}
	if labels["ctx"] || labels["children"] {
		t.Errorf("labels = %v, must exclude reserved names ctx/children", labels)
	}
}

// TestComponentAttrItemsNoFact checks the fail-soft rule: an element with no
// ComponentCalls entry (the call was never planned — a broken tag, an
// unresolved target, ...) yields nil, not a panic or a guess.
func TestComponentAttrItemsNoFact(t *testing.T) {
	fset := token.NewFileSet()
	src := "package page\n\ncomponent Home() {\n\t<Card title=\"x\"/>\n}\n"
	f, err := gsxparser.ParseFile(fset, "page.gsx", []byte(src), 0)
	if err != nil {
		t.Fatalf("ParseFile: %v", err)
	}
	var el *gsxast.Element
	gsxast.Inspect(f, func(n gsxast.Node) bool {
		if e, ok := n.(*gsxast.Element); ok && e.Tag == "Card" {
			el = e
		}
		return true
	})
	pkg := &Package{GSXFset: fset, Files: map[string]*gsxast.File{"page.gsx": f}, ComponentCalls: map[*gsxast.Element]ComponentCallFact{}}
	if items := componentAttrItems(pkg, el, src, 0, 0, encUTF8); items != nil {
		t.Fatalf("items = %+v, want nil (no planned call fact)", items)
	}
}

// TestComponentAttrItemsNilGuards checks that a nil pkg or nil el fails soft.
func TestComponentAttrItemsNilGuards(t *testing.T) {
	if items := componentAttrItems(nil, &gsxast.Element{}, "", 0, 0, encUTF8); items != nil {
		t.Fatalf("items = %+v, want nil for nil pkg", items)
	}
	if items := componentAttrItems(&Package{}, nil, "", 0, 0, encUTF8); items != nil {
		t.Fatalf("items = %+v, want nil for nil el", items)
	}
}

// elementAtTagOffsetFixture parses src into a *Package (GSXFset + Files) with
// no type info, for elementAtTagOffset tests — the function only needs
// GSXFset/Files, never ComponentCalls or Types.
func elementAtTagOffsetFixture(t *testing.T, src string) *Package {
	t.Helper()
	fset := token.NewFileSet()
	f, err := gsxparser.ParseFile(fset, "page.gsx", []byte(src), 0)
	if err != nil {
		t.Fatalf("ParseFile: %v", err)
	}
	return &Package{GSXFset: fset, Files: map[string]*gsxast.File{"page.gsx": f}}
}

// TestElementAtTagOffset checks the byte-offset bridge: a tagOff computed
// against one parse of src locates the element at that same tagOff in an
// INDEPENDENT second parse of the identical bytes — pointer identity never
// holds across the two parses, only the offset does.
func TestElementAtTagOffset(t *testing.T) {
	src := "package page\n\ncomponent Home() {\n\t<div><Card title=\"x\"/></div>\n}\n"

	// First parse: locate the "Card" element's TagPos offset, as
	// classifyCompletionContext would from repairAtCursor's own FileSet.
	first := elementAtTagOffsetFixture(t, src)
	var firstCard *gsxast.Element
	gsxast.Inspect(first.Files["page.gsx"], func(n gsxast.Node) bool {
		if e, ok := n.(*gsxast.Element); ok && e.Tag == "Card" {
			firstCard = e
		}
		return true
	})
	if firstCard == nil {
		t.Fatal("no Card element in first parse")
	}
	tagOff := first.GSXFset.Position(firstCard.TagPos).Offset

	// Second, independent parse (mirrors the ephemeral analysis's own parse of
	// the same buffer bytes).
	second := elementAtTagOffsetFixture(t, src)
	got := elementAtTagOffset(second, "page.gsx", tagOff)
	if got == nil {
		t.Fatal("elementAtTagOffset returned nil, want the Card element from the second parse")
	}
	if got == firstCard {
		t.Fatal("elementAtTagOffset returned the FIRST parse's pointer; pointer identity must never hold across independent parses")
	}
	if got.Tag != "Card" {
		t.Errorf("got.Tag = %q, want %q", got.Tag, "Card")
	}
}

// TestElementAtTagOffsetNoMatch checks that an offset matching nothing (and a
// missing path) both fail soft to nil.
func TestElementAtTagOffsetNoMatch(t *testing.T) {
	pkg := elementAtTagOffsetFixture(t, "package page\n\ncomponent Home() {\n\t<div/>\n}\n")
	if got := elementAtTagOffset(pkg, "page.gsx", -1); got != nil {
		t.Fatalf("got = %+v, want nil for an unmatched offset", got)
	}
	if got := elementAtTagOffset(pkg, "missing.gsx", 0); got != nil {
		t.Fatalf("got = %+v, want nil for a missing path", got)
	}
	if got := elementAtTagOffset(nil, "page.gsx", 0); got != nil {
		t.Fatalf("got = %+v, want nil for a nil package", got)
	}
}
