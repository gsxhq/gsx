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
// "Pkg.Func" (with a " (ctx)" suffix when WantsCtx), kind = ciKindOperator
// (not Function — accepting a bare filter must not auto-append "()"), tier =
// tierContext, and every item's TextEdit replaces the given [start,end) span.
// Order is preserved verbatim (filters arrives pre-sorted from
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
		if it.Kind != ciKindOperator {
			t.Errorf("items[%d].Kind = %d, want ciKindOperator", i, it.Kind)
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
	ephPkg         *Package
	ephErr         error
	ephNotAcquired bool     // when true, the non-blocking variant reports acquired=false (contention)
	analyzed       *Package // returned by Analyze, populates s.pkgs[dir] via didOpen
}

func (a pipeFilterAnalyzer) AnalyzeEphemeral(string, string, []byte) (*Package, error) {
	return a.ephPkg, a.ephErr
}

// AnalyzeEphemeralNonBlocking scripts contention: when ephNotAcquired it
// reports acquired=false (nil Package) so the handler must serve the retained
// snapshot; otherwise it acquires and delegates to AnalyzeEphemeral.
func (a pipeFilterAnalyzer) AnalyzeEphemeralNonBlocking(dir, path string, content []byte) (*Package, bool, error) {
	if a.ephNotAcquired {
		return nil, false, nil
	}
	pkg, err := a.AnalyzeEphemeral(dir, path, content)
	return pkg, true, err
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

// TestPipeStageCompletionServesRetainedWhenContended pins the P4 insurance
// policy: when the non-blocking ephemeral analysis reports acquired=false (a
// background analysis holds the lock), pipe-stage completion serves the
// retained s.pkgs[dir] snapshot's Filters instead of stalling — and never
// consults the ephemeral package (ephPkg here carries a bogus filter that must
// NOT appear, proving the contended path skipped it).
func TestPipeStageCompletionServesRetainedWhenContended(t *testing.T) {
	uri := "file:///m/a.gsx"
	text := "package p\n\ncomponent C(x string) {\n\t<div>{ x |> up }</div>\n}\n"
	off := strings.Index(text, "|> up") + len("|> up")
	pos := positionForByteOffset(text, off, encUTF16)

	a := pipeFilterAnalyzer{
		ephNotAcquired: true,
		ephPkg:         &Package{Filters: []FilterCandidate{{Name: "shouldNotAppear", Pkg: "x", Func: "X"}}},
		analyzed:       &Package{Filters: []FilterCandidate{{Name: "lower", Pkg: "github.com/gsxhq/gsx/std", Func: "Lower"}}},
	}

	out := drive(t, a, initFrame()+didOpenFrame(uri, text)+
		completionFrame(2, uri, pos)+exitFrame())
	items := decodeCompletionItems(t, out, 2)
	if len(items) != 1 || items[0].Label != "lower" {
		t.Fatalf("contended pipe-stage items = %v, want exactly [lower] from the retained snapshot", items)
	}
}

// TestGoContextCompletionEmptyWhenContended pins the P4 policy for Go-identifier
// cursors: under contention (acquired=false) there is no safe retained fallback
// — a stale scope chain would list objects the live buffer may no longer have —
// so the handler answers an empty (non-nil) list, never an error and never a
// stall. Exercised directly since the empty answer is returned at the
// not-acquired guard, before any type-info bridging a fake could not supply.
func TestGoContextCompletionEmptyWhenContended(t *testing.T) {
	s := &Server{analyzer: pipeFilterAnalyzer{ephNotAcquired: true}, enc: encUTF16}
	text := "{ x }"
	got := s.goContextCompletion(completionContext{kind: ctxGoExpr}, "/m/a.gsx", text, 3, repairResult{src: []byte(text)})
	if got.Items == nil {
		t.Fatal("Go-context contended completion returned nil Items; want an empty non-nil list")
	}
	if len(got.Items) != 0 {
		t.Fatalf("Go-context contended completion returned %d items; want 0 (fail soft, not stale)", len(got.Items))
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
	if items[0].Label != "Card" || items[0].Kind != ciKindClass {
		t.Errorf("items[0] = %+v, want label Card kind ciKindClass", items[0])
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
	if items[0].Label != "Button" || items[0].Kind != ciKindClass {
		t.Errorf("items[0] = %+v, want label Button kind ciKindClass", items[0])
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

// buildIconValueComponentFixture constructs, entirely via the go/types object
// API (types.NewPackage/NewVar/NewFunc/NewSignatureType — no source parsed, no
// real import resolution needed, mirroring componentTagFixturePackage's
// approach), a three-package graph that reproduces the real-world gap this
// fixture is named after: a "github.com/gsxhq/gsx" package declaring the Node
// interface, a pure-Go "icon" sibling package (no `component`-keyword decls
// at all — see internal/lsp/completion_gsx.go's componentTagItems doc
// comment) with candidates spanning every shape tagCallableValueNames must
// discriminate, and a "page" package importing "icon" plus declaring one
// local value-component of its own.
//
// icon's package-scope names:
//   - X: exported var, func(name string) Node — the common shape (`var X =
//     named("x")`); must be offered.
//   - DirectFunc: exported FUNC (not var) of the same shape; must be offered.
//   - BadParam: exported var, func(string) Node with an UNNAMED parameter;
//     never offered (an unnamed parameter can never bind to a markup
//     attribute).
//   - WrongResult: exported var, func(name string) int — result does not
//     implement Node; never offered.
//   - unexp: unexported var of the X shape; never offered when scanned as an
//     IMPORTED package (Go visibility — only reachable via qualifier!="").
//
// page's package-scope names:
//   - LocalIcon: unexported var of the X shape, declared directly in page —
//     must be offered at a BARE (qualifier=="") cursor, proving the
//     exportedOnly=false same-package rule (contrast icon's "unexp").
func buildIconValueComponentFixture() *Package {
	gsxPkg := types.NewPackage("github.com/gsxhq/gsx", "gsx")
	nodeMethod := types.NewFunc(token.NoPos, gsxPkg, "isNode", types.NewSignatureType(nil, nil, nil, nil, nil, false))
	nodeIface := types.NewInterfaceType([]*types.Func{nodeMethod}, nil)
	nodeIface.Complete()
	nodeName := types.NewTypeName(token.NoPos, gsxPkg, "Node", nil)
	nodeNamed := types.NewNamed(nodeName, nodeIface, nil)
	gsxPkg.Scope().Insert(nodeName)
	gsxPkg.MarkComplete()

	nodeResult := func() *types.Tuple {
		return types.NewTuple(types.NewVar(token.NoPos, nil, "", nodeNamed))
	}
	namedParams := func() *types.Tuple {
		return types.NewTuple(types.NewVar(token.NoPos, nil, "name", types.Typ[types.String]))
	}

	iconPkg := types.NewPackage("github.com/tespkg/one-learning/ds/icon", "icon")
	iconPkg.SetImports([]*types.Package{gsxPkg})

	xSig := types.NewSignatureType(nil, nil, nil, namedParams(), nodeResult(), false)
	iconPkg.Scope().Insert(types.NewVar(token.NoPos, iconPkg, "X", xSig))
	iconPkg.Scope().Insert(types.NewFunc(token.NoPos, iconPkg, "DirectFunc", types.NewSignatureType(nil, nil, nil, namedParams(), nodeResult(), false)))

	unnamedParams := types.NewTuple(types.NewVar(token.NoPos, nil, "", types.Typ[types.String]))
	badSig := types.NewSignatureType(nil, nil, nil, unnamedParams, nodeResult(), false)
	iconPkg.Scope().Insert(types.NewVar(token.NoPos, iconPkg, "BadParam", badSig))

	intResult := types.NewTuple(types.NewVar(token.NoPos, nil, "", types.Typ[types.Int]))
	wrongSig := types.NewSignatureType(nil, nil, nil, namedParams(), intResult, false)
	iconPkg.Scope().Insert(types.NewVar(token.NoPos, iconPkg, "WrongResult", wrongSig))

	iconPkg.Scope().Insert(types.NewVar(token.NoPos, iconPkg, "unexp", xSig))
	iconPkg.MarkComplete()

	pagePkg := types.NewPackage("example.com/app/page", "page")
	pagePkg.SetImports([]*types.Package{gsxPkg, iconPkg})
	localSig := types.NewSignatureType(nil, nil, nil, namedParams(), nodeResult(), false)
	pagePkg.Scope().Insert(types.NewVar(token.NoPos, pagePkg, "LocalIcon", localSig))
	pagePkg.MarkComplete()

	return &Package{Types: pagePkg}
}

// TestComponentValueNameItemsQualified checks the qualifier!="" scan against
// the imported "icon" package: X and DirectFunc are offered (kind
// ciKindClass), BadParam/WrongResult/unexp are not.
func TestComponentValueNameItemsQualified(t *testing.T) {
	pkg := buildIconValueComponentFixture()
	items := componentTagItems(pkg, "icon", false, "", 0, 0, encUTF8)
	got := map[string]CompletionItem{}
	for _, it := range items {
		got[it.Label] = it
	}
	for _, name := range []string{"X", "DirectFunc"} {
		it, ok := got[name]
		if !ok {
			t.Fatalf("items = %+v, want %q offered", items, name)
		}
		if it.Kind != ciKindClass {
			t.Errorf("%s.Kind = %d, want ciKindClass", name, it.Kind)
		}
	}
	for _, name := range []string{"BadParam", "WrongResult", "unexp"} {
		if _, ok := got[name]; ok {
			t.Errorf("items = %+v, must NOT offer %q", items, name)
		}
	}
}

// TestComponentValueNameItemsBareCursorLocal checks the qualifier=="" scan
// against page's OWN scope: the unexported LocalIcon value-component is
// offered (same-package visibility has no exported-only gate), and the
// bare-cursor candidate list also carries the "icon" import qualifier item —
// packageHasTagCallableValue must recognize icon as qualifier-worthy even
// though it has zero ComponentDecls entries.
func TestComponentValueNameItemsBareCursorLocal(t *testing.T) {
	pkg := buildIconValueComponentFixture()
	items := componentTagItems(pkg, "", false, "", 0, 0, encUTF8)
	var local, qual *CompletionItem
	for i := range items {
		switch items[i].Label {
		case "LocalIcon":
			local = &items[i]
		case "icon":
			qual = &items[i]
		}
	}
	if local == nil {
		t.Fatalf("items = %+v, want local value-component LocalIcon offered", items)
	}
	if local.Kind != ciKindClass {
		t.Errorf("LocalIcon.Kind = %d, want ciKindClass", local.Kind)
	}
	if qual == nil {
		t.Fatalf("items = %+v, want qualifier item \"icon\" (zero ComponentDecls, but has tag-callable values)", items)
	}
	if qual.TextEdit == nil || qual.TextEdit.NewText != "icon." {
		t.Errorf("icon qualifier TextEdit = %+v, want NewText %q", qual.TextEdit, "icon.")
	}
}

// TestComponentValueNameItemsDedup checks that a name present in BOTH
// pkg.ComponentDecls (a `component`-keyword decl) and the value-component
// scan (its own underlying Go func trivially satisfies the same signature
// shape) is offered exactly once.
func TestComponentValueNameItemsDedup(t *testing.T) {
	pkg := buildIconValueComponentFixture()
	pkg.ComponentDecls = map[ComponentDeclKey][]sourceintel.VersionedSpan{
		{PackagePath: "github.com/tespkg/one-learning/ds/icon", ComponentKey: ".X"}: nil,
	}
	items := componentTagItems(pkg, "icon", false, "", 0, 0, encUTF8)
	count := 0
	for _, it := range items {
		if it.Label == "X" {
			count++
		}
	}
	if count != 1 {
		t.Fatalf("label \"X\" appeared %d times, want exactly 1: %+v", count, items)
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

func (a tagCompletionAnalyzer) AnalyzeEphemeralNonBlocking(dir, path string, content []byte) (*Package, bool, error) {
	pkg, err := a.AnalyzeEphemeral(dir, path, content)
	return pkg, true, err
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

// TestTagCompletionBothEmpty checks the fail-soft merge floor: when BOTH the
// ephemeral and retained component sources are shells/absent, the COMPONENT half
// contributes nothing, but the HTML tag list — a static dataset fact needing no
// codegen — must still be offered. So a bare tag cursor never comes back empty
// just because analysis failed.
func TestTagCompletionBothEmpty(t *testing.T) {
	uri := "file:///m/a.gsx"
	text := "package p\n\ncomponent C() {\n\t<div><Ca</div>\n}\n"
	off := strings.Index(text, "<Ca") + len("<Ca")
	pos := positionForByteOffset(text, off, encUTF16)

	a := tagCompletionAnalyzer{ephErr: errors.New("ephemeral analysis failed")}

	out := drive(t, a, initFrame()+didOpenFrame(uri, text)+
		completionFrame(2, uri, pos)+exitFrame())
	items := decodeCompletionItems(t, out, 2)
	labels := map[string]bool{}
	for _, it := range items {
		labels[it.Label] = true
	}
	// HTML tags survive a shell analysis; components do not.
	if !labels["div"] {
		t.Fatalf("tag completion labels = %v, want HTML tag `div` present despite shell analysis", labels)
	}
	if labels["Card"] {
		t.Fatalf("tag completion labels = %v, must NOT offer components when both sources are shells", labels)
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
// attrNameSpanContains can find it), with ComponentParamFact.Name set from a
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
	items := componentAttrItems(pkg, el, false, src, cursor, cursor, encUTF8, false)

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

// TestComponentAttrItemsExcludesGoOnlyVariadic checks that a signature ending
// in a Go-only variadic parameter (name other than the reserved "attrs"/
// "children") never offers that parameter as a candidate: the planner
// rejects any markup binding to a Go-only variadic param
// (component_positional_plan.go ~L303-304, "Go-only variadic parameter %d
// was populated from markup"), so surfacing it as completable is guaranteed
// to break the call. An ordinary unbound param ahead of the variadic tail
// ("count") stays offered.
func TestComponentAttrItemsExcludesGoOnlyVariadic(t *testing.T) {
	src := "package page\n\ncomponent Home() {\n\t<Card title=\"x\"/>\n}\n"
	fset := token.NewFileSet()
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
	if el == nil {
		t.Fatalf("no <Card> element found in source")
	}

	// (title string, count int, nums ...int) — nums is the variadic last
	// param, sliced-typed, with a name that is neither "attrs" nor
	// "children", so analyzeComponentSignature would classify it
	// roleGoOnlyVariadic.
	params := types.NewTuple(
		types.NewVar(token.NoPos, nil, "title", types.Typ[types.String]),
		types.NewVar(token.NoPos, nil, "count", types.Typ[types.Int]),
		types.NewVar(token.NoPos, nil, "nums", types.NewSlice(types.Typ[types.Int])),
	)
	sig := types.NewSignatureType(nil, nil, nil, params, nil, true)

	boundParams := map[gsxast.Attr]ComponentParamFact{}
	for _, attr := range el.Attrs {
		name, ok := attrName(attr)
		if !ok {
			continue
		}
		if name == "title" {
			boundParams[attr] = ComponentParamFact{Name: "title"}
		}
	}

	pkg := &Package{
		GSXFset: fset,
		Files:   map[string]*gsxast.File{"page.gsx": f},
		ComponentCalls: map[*gsxast.Element]ComponentCallFact{
			el: {Signature: sig, Params: boundParams},
		},
	}

	cursor := strings.Index(src, "<Card") + len("<Card")
	items := componentAttrItems(pkg, el, false, src, cursor, cursor, encUTF8, false)

	if len(items) != 1 {
		t.Fatalf("items = %+v, want exactly 1 (count)", items)
	}
	if items[0].Label != "count" {
		t.Errorf("items[0].Label = %q, want %q", items[0].Label, "count")
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
	items := componentAttrItems(pkg, el, false, src, nameStart, nameEnd, encUTF8, false)

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
	if items := componentAttrItems(pkg, el, false, src, 0, 0, encUTF8, false); items != nil {
		t.Fatalf("items = %+v, want nil (no planned call fact)", items)
	}
}

// TestComponentAttrItemsNilGuards checks that a nil pkg or nil el fails soft.
func TestComponentAttrItemsNilGuards(t *testing.T) {
	if items := componentAttrItems(nil, &gsxast.Element{}, false, "", 0, 0, encUTF8, false); items != nil {
		t.Fatalf("items = %+v, want nil for nil pkg", items)
	}
	if items := componentAttrItems(&Package{}, nil, false, "", 0, 0, encUTF8, false); items != nil {
		t.Fatalf("items = %+v, want nil for nil el", items)
	}
}

// componentAttrsCatchAllFixture parses src (which must contain exactly one
// element whose Tag == tag) and wires a *Package/element pair for
// componentAttrItems whose ComponentCalls[el].Signature is caller-supplied —
// unlike componentAttrFixture's fixed (ctx, title, count, children) shape,
// these tests need to vary whether an "attrs" catch-all parameter is present.
// Every attr already present on el is bound in fact.Params keyed by its own
// position, from a caller-supplied attr->param-name map (mirrors
// componentAttrFixture).
func componentAttrsCatchAllFixture(t *testing.T, src, tag string, sig *types.Signature, bound map[string]string) (pkg *Package, el *gsxast.Element) {
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

// TestComponentAttrItemsAttrsCatchAllOffersHTMLGlobals checks the core new
// rule: a component signature declaring a variadic "attrs" catch-all
// (`func(attrs ...gsx.Attr) gsx.Node` — icon.Bell's real shape) offers the
// HTML GLOBAL attribute set in addition to (here, absent) named params —
// boolean globals inserting the bare name (hidden), value globals inserting
// `name=""` (class), all sorted at tierSecondary (20) so real component
// params would lead.
func TestComponentAttrItemsAttrsCatchAllOffersHTMLGlobals(t *testing.T) {
	src := "package page\n\ncomponent Home() {\n\t<icon.Bell/>\n}\n"
	params := types.NewTuple(
		types.NewVar(token.NoPos, nil, "attrs", types.NewSlice(types.Typ[types.Int])),
	)
	sig := types.NewSignatureType(nil, nil, nil, params, nil, true)
	pkg, el := componentAttrsCatchAllFixture(t, src, "icon.Bell", sig, nil)

	cursor := strings.Index(src, "<icon.Bell") + len("<icon.Bell")
	items := componentAttrItems(pkg, el, false, src, cursor, cursor, encUTF8, false)

	byLabel := map[string]CompletionItem{}
	for _, it := range items {
		byLabel[it.Label] = it
	}

	hidden, ok := byLabel["hidden"]
	if !ok {
		t.Fatalf("labels = %v, want boolean global %q offered", byLabel, "hidden")
	}
	if hidden.TextEdit == nil || hidden.TextEdit.NewText != "hidden" {
		t.Errorf("hidden.TextEdit = %+v, want bare-name insert %q", hidden.TextEdit, "hidden")
	}
	if !strings.HasPrefix(hidden.SortText, "20") {
		t.Errorf("hidden.SortText = %q, want tierSecondary (20) prefix", hidden.SortText)
	}

	class, ok := byLabel["class"]
	if !ok {
		t.Fatalf("labels = %v, want value global %q offered", byLabel, "class")
	}
	if class.TextEdit == nil || class.TextEdit.NewText != `class=""` {
		t.Errorf("class.TextEdit = %+v, want %q insert", class.TextEdit, `class=""`)
	}
	if !strings.HasPrefix(class.SortText, "20") {
		t.Errorf("class.SortText = %q, want tierSecondary (20) prefix", class.SortText)
	}

	// The reserved "attrs" name itself is never offered as an attribute.
	if byLabel["attrs"].Label != "" {
		t.Errorf("labels = %v, must not offer reserved %q", byLabel, "attrs")
	}
}

// TestComponentAttrItemsAttrsCatchAllSnippetThreadsThrough checks that
// snippet=true reaches the forwarded HTML-globals branch (threaded through to
// the nested htmlAttrItems call): the value global `class` inserts
// `class="$1"` with InsertTextFormat = Snippet, same as the plain-HTML path.
// A NAMED component parameter, by contrast, is never a quote-value insert
// (per the task-13 "no `={}` snippet" rule for named params) and must stay a
// bare-name insert with InsertTextFormat unset even under snippet support —
// this is the one deliberate place componentAttrItems does NOT apply the
// gate, so pin it here alongside the branch that does.
func TestComponentAttrItemsAttrsCatchAllSnippetThreadsThrough(t *testing.T) {
	src := "package page\n\ncomponent Home() {\n\t<icon.Bell/>\n}\n"
	params := types.NewTuple(
		types.NewVar(token.NoPos, nil, "title", types.Typ[types.String]),
		types.NewVar(token.NoPos, nil, "attrs", types.NewSlice(types.Typ[types.Int])),
	)
	sig := types.NewSignatureType(nil, nil, nil, params, nil, true)
	pkg, el := componentAttrsCatchAllFixture(t, src, "icon.Bell", sig, nil)

	cursor := strings.Index(src, "<icon.Bell") + len("<icon.Bell")
	items := componentAttrItems(pkg, el, false, src, cursor, cursor, encUTF8, true)

	byLabel := map[string]CompletionItem{}
	for _, it := range items {
		byLabel[it.Label] = it
	}

	class, ok := byLabel["class"]
	if !ok {
		t.Fatalf("labels = %v, want forwarded global %q offered", byLabel, "class")
	}
	if class.TextEdit == nil || class.TextEdit.NewText != `class="$1"` {
		t.Errorf("class.TextEdit = %+v, want NewText %q", class.TextEdit, `class="$1"`)
	}
	if class.InsertTextFormat != insertTextFormatSnippet {
		t.Errorf("class.InsertTextFormat = %d, want insertTextFormatSnippet (2)", class.InsertTextFormat)
	}

	title, ok := byLabel["title"]
	if !ok {
		t.Fatalf("labels = %v, want named param %q offered", byLabel, "title")
	}
	if title.TextEdit == nil || title.TextEdit.NewText != "title" {
		t.Errorf("title.TextEdit = %+v, want bare NewText %q (named params never snippet)", title.TextEdit, "title")
	}
	if title.InsertTextFormat != 0 {
		t.Errorf("title.InsertTextFormat = %d, want 0 (named params never snippet)", title.InsertTextFormat)
	}
}

// TestComponentAttrItemsNoAttrsCatchAllNoHTMLGlobals checks the negative
// case: a plain-props signature with no "attrs" catch-all offers only the
// named params — no HTML globals leak in (an unknown attribute would be
// rejected by the planner, so suggesting one would invite invalid input).
func TestComponentAttrItemsNoAttrsCatchAllNoHTMLGlobals(t *testing.T) {
	src := "package page\n\ncomponent Home() {\n\t<Card/>\n}\n"
	params := types.NewTuple(
		types.NewVar(token.NoPos, nil, "title", types.Typ[types.String]),
		types.NewVar(token.NoPos, nil, "count", types.Typ[types.Int]),
	)
	sig := types.NewSignatureType(nil, nil, nil, params, nil, false)
	pkg, el := componentAttrsCatchAllFixture(t, src, "Card", sig, nil)

	cursor := strings.Index(src, "<Card") + len("<Card")
	items := componentAttrItems(pkg, el, false, src, cursor, cursor, encUTF8, false)

	labels := map[string]bool{}
	for _, it := range items {
		labels[it.Label] = true
	}
	if len(labels) != 2 || !labels["title"] || !labels["count"] {
		t.Fatalf("labels = %v, want exactly {title, count}", labels)
	}
	for _, global := range []string{"class", "hidden", "id", "style"} {
		if labels[global] {
			t.Errorf("labels = %v, must NOT offer HTML global %q (no attrs catch-all)", labels, global)
		}
	}
}

// TestComponentAttrItemsAttrsCatchAllPresentAttrExcluded checks that the
// forwarded-globals list applies the same present-attr exclusion and
// cursor-on-own-token carve-out as the plain HTML path (htmlAttrItems),
// reused rather than reimplemented: an already-authored "class" attribute
// elsewhere on the tag is excluded, but stays offered when the cursor sits
// on that very attribute's own token.
func TestComponentAttrItemsAttrsCatchAllPresentAttrExcluded(t *testing.T) {
	src := "package page\n\ncomponent Home() {\n\t<icon.Bell class=\"x\"/>\n}\n"
	params := types.NewTuple(
		types.NewVar(token.NoPos, nil, "attrs", types.NewSlice(types.Typ[types.Int])),
	)
	sig := types.NewSignatureType(nil, nil, nil, params, nil, true)
	pkg, el := componentAttrsCatchAllFixture(t, src, "icon.Bell", sig, nil)

	// Cursor elsewhere (right after the tag name): "class" already present,
	// must be excluded.
	elsewhere := strings.Index(src, "<icon.Bell") + len("<icon.Bell")
	items := componentAttrItems(pkg, el, false, src, elsewhere, elsewhere, encUTF8, false)
	for _, it := range items {
		if it.Label == "class" {
			t.Fatalf("items = %+v, must exclude already-present %q", items, "class")
		}
	}

	// Cursor ON the "class" token itself: carve-out keeps it offered.
	nameStart := strings.Index(src, "class")
	nameEnd := nameStart + len("class")
	onToken := componentAttrItems(pkg, el, false, src, nameStart, nameEnd, encUTF8, false)
	found := false
	for _, it := range onToken {
		if it.Label == "class" {
			found = true
		}
	}
	if !found {
		t.Fatalf("items = %+v, want %q offered (cursor is on its own token)", onToken, "class")
	}
}

// TestComponentAttrItemsAttrsCatchAllHTMXGated checks that hx-* forwarded
// candidates only appear when htmxEnabled — the same gate htmlAttrItems
// applies on the plain HTML path.
func TestComponentAttrItemsAttrsCatchAllHTMXGated(t *testing.T) {
	src := "package page\n\ncomponent Home() {\n\t<icon.Bell/>\n}\n"
	params := types.NewTuple(
		types.NewVar(token.NoPos, nil, "attrs", types.NewSlice(types.Typ[types.Int])),
	)
	sig := types.NewSignatureType(nil, nil, nil, params, nil, true)
	pkg, el := componentAttrsCatchAllFixture(t, src, "icon.Bell", sig, nil)
	cursor := strings.Index(src, "<icon.Bell") + len("<icon.Bell")

	off := componentAttrItems(pkg, el, false, src, cursor, cursor, encUTF8, false)
	for _, it := range off {
		if strings.HasPrefix(it.Label, "hx-") {
			t.Fatalf("items = %+v, must NOT offer hx-* when htmxEnabled=false", off)
		}
	}

	on := componentAttrItems(pkg, el, true, src, cursor, cursor, encUTF8, false)
	foundHx := false
	for _, it := range on {
		if strings.HasPrefix(it.Label, "hx-") {
			foundHx = true
		}
	}
	if !foundHx {
		t.Fatalf("items = %+v, want at least one hx-* candidate when htmxEnabled=true", on)
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
