package lsp

import (
	"go/token"
	"strings"
	"testing"
)

// TestCompletionDocEagerScopeCandidate covers T9's eager path for a
// package-scope (tierPackage) candidate: a documented top-level func offered
// at a bare package-scope cursor (goCompletionItems, pkg.Types.Scope()
// directly, mirroring TestGoCompletionItemsStatementMemberDispatch's use of
// the real scope) carries Documentation inline — no Data, no round trip.
// The sibling `component Home` (an authored but UNDOCUMENTED package-scope
// func) is asserted to carry neither Documentation nor Data, pinning the
// "authored, no comment -> no doc at all" case from the SAME pass.
func TestCompletionDocEagerScopeCandidate(t *testing.T) {
	const src = `package page

// Greeting renders a friendly hello to name.
func Greeting(name string) string {
	return "Hello, " + name
}

component Home() {
	<div>hi</div>
}
`
	pkg, path := analyzedLSPPackage(t, src)
	items := goCompletionItems(pkg, pkg.Types.Scope(), nil, token.NoPos, false, nil, src, 0, 0, encUTF8, path, []byte(src))

	var greeting, home *CompletionItem
	for i := range items {
		switch items[i].Label {
		case "Greeting":
			greeting = &items[i]
		case "Home":
			home = &items[i]
		}
	}
	if greeting == nil {
		t.Fatal("scope completion missing package-scope func `Greeting`")
	}
	if greeting.Documentation == nil {
		t.Fatal("`Greeting` has a doc comment but Documentation is nil")
	}
	if !strings.Contains(greeting.Documentation.Value, "Greeting renders a friendly hello") {
		t.Errorf("`Greeting` Documentation = %q, want it to contain the doc comment text", greeting.Documentation.Value)
	}
	if greeting.Documentation.Kind != "markdown" {
		t.Errorf("`Greeting` Documentation.Kind = %q, want \"markdown\"", greeting.Documentation.Kind)
	}
	if greeting.Data != nil {
		t.Errorf("`Greeting` is authored in this package: eager Documentation must not ALSO carry lazy Data; got %+v", greeting.Data)
	}

	if home == nil {
		t.Fatal("scope completion missing package-scope component `Home`")
	}
	if home.Documentation != nil {
		t.Errorf("`Home` has no doc comment but Documentation = %+v, want nil", home.Documentation)
	}
	if home.Data != nil {
		t.Errorf("`Home` is authored (eager-only): a doc miss must not fall back to lazy Data; got %+v", home.Data)
	}
}

// TestCompletionDocEagerMemberCandidate covers decision #2 (same-package
// MEMBER items follow the eager rule too): a documented method on a
// same-package receiver type, reached via statementMemberItems' `.`-cursor
// path (mirroring TestStatementMemberItemsGoBlockTrailingDot), carries eager
// Documentation. The sibling field `Label` (authored, no doc) carries
// neither Documentation nor Data, alongside it in the same member list.
func TestCompletionDocEagerMemberCandidate(t *testing.T) {
	const src = `package page

type Widget struct {
	Label string
}

// Render returns the widget's label as markup text.
func (w Widget) Render() string {
	return w.Label
}

component Home(w Widget) {
	{{ _ = w.Render }}
}
`
	pkg, path := analyzedLSPPackage(t, src)
	nameOff := strings.Index(src, "w.Render") + len("w.")

	items, ok := statementMemberItems(pkg, path, src, nameOff, nameOff, encUTF8, []byte(src))
	if !ok {
		t.Fatal("statementMemberItems returned ok=false at a `.`-cursor")
	}
	var render, label *CompletionItem
	for i := range items {
		switch items[i].Label {
		case "Render":
			render = &items[i]
		case "Label":
			label = &items[i]
		}
	}
	if render == nil {
		t.Fatalf("member completion missing method `Render`; items=%+v", items)
	}
	if render.Documentation == nil {
		t.Fatal("`Render` has a doc comment but Documentation is nil")
	}
	if !strings.Contains(render.Documentation.Value, "Render returns the widget's label") {
		t.Errorf("`Render` Documentation = %q, want it to contain the doc comment text", render.Documentation.Value)
	}
	if render.Data != nil {
		t.Errorf("`Render` is authored: eager Documentation must not ALSO carry lazy Data; got %+v", render.Data)
	}

	if label == nil {
		t.Fatalf("member completion missing field `Label`; items=%+v", items)
	}
	if label.Documentation != nil {
		t.Errorf("`Label` has no doc comment but Documentation = %+v, want nil", label.Documentation)
	}
	if label.Data != nil {
		t.Errorf("`Label` is authored (eager-only): a doc miss must not fall back to lazy Data; got %+v", label.Data)
	}
}

// TestCompletionDocEagerGoWithElementsDecl covers the GoWithElements decl
// class explicitly required by the design's test plan: a package-level var
// initialized to an embedded element literal (`var Banner = <div>...</div>`,
// a *gsxast.GoWithElements — NOT a plain GoChunk) with a doc comment still
// resolves its doc eagerly, proving the byte-exact recovery reconstruction
// (reconstructGoWithElements, shared with textDocument/documentSymbol) is
// exercised, not just the simpler GoChunk path.
func TestCompletionDocEagerGoWithElementsDecl(t *testing.T) {
	const src = `package page

// Banner is a small reusable markup fragment.
var Banner = <div>banner</div>

component Home() {
	<div>{ Banner }</div>
}
`
	pkg, path := analyzedLSPPackage(t, src)
	items := goCompletionItems(pkg, pkg.Types.Scope(), nil, token.NoPos, false, nil, src, 0, 0, encUTF8, path, []byte(src))

	var banner *CompletionItem
	for i := range items {
		if items[i].Label == "Banner" {
			banner = &items[i]
		}
	}
	if banner == nil {
		t.Fatalf("scope completion missing package-scope var `Banner`; items=%+v", items)
	}
	if banner.Documentation == nil {
		t.Fatal("`Banner` (a GoWithElements decl) has a doc comment but Documentation is nil")
	}
	if !strings.Contains(banner.Documentation.Value, "Banner is a small reusable markup fragment") {
		t.Errorf("`Banner` Documentation = %q, want it to contain the doc comment text", banner.Documentation.Value)
	}
	if banner.Data != nil {
		t.Errorf("`Banner` is authored: eager Documentation must not ALSO carry lazy Data; got %+v", banner.Data)
	}
}

// TestChunkDocCacheReuseAcrossRequests covers the "second completion in the
// same file doesn't re-parse chunks" cache contract (chunkDocCache.doc):
// two goCompletionItems passes over the IDENTICAL pkg/source (simulating two
// completion requests over an unchanged buffer) must not re-run the
// recovery parse the second time — globalChunkDocCache.parses (a
// cache-miss-only counter) must stay flat across the second pass, while both
// passes still report the identical Documentation.
func TestChunkDocCacheReuseAcrossRequests(t *testing.T) {
	const src = `package page

// Greeting renders a friendly hello to name.
func Greeting(name string) string {
	return "Hello, " + name
}

component Home() {
	<div>hi</div>
}
`
	pkg, path := analyzedLSPPackage(t, src)

	docOf := func(items []CompletionItem) *MarkupContent {
		for _, it := range items {
			if it.Label == "Greeting" {
				return it.Documentation
			}
		}
		return nil
	}

	globalChunkDocCache.mu.Lock()
	before := globalChunkDocCache.parses
	globalChunkDocCache.mu.Unlock()

	items1 := goCompletionItems(pkg, pkg.Types.Scope(), nil, token.NoPos, false, nil, src, 0, 0, encUTF8, path, []byte(src))
	globalChunkDocCache.mu.Lock()
	afterFirst := globalChunkDocCache.parses
	globalChunkDocCache.mu.Unlock()
	if afterFirst <= before {
		t.Fatalf("first completion pass: chunkDocCache parses did not increase (before=%d after=%d)", before, afterFirst)
	}

	items2 := goCompletionItems(pkg, pkg.Types.Scope(), nil, token.NoPos, false, nil, src, 0, 0, encUTF8, path, []byte(src))
	globalChunkDocCache.mu.Lock()
	afterSecond := globalChunkDocCache.parses
	globalChunkDocCache.mu.Unlock()
	if afterSecond != afterFirst {
		t.Errorf("second completion pass over an UNCHANGED chunk re-parsed: parses %d -> %d, want no change", afterFirst, afterSecond)
	}

	d1, d2 := docOf(items1), docOf(items2)
	if d1 == nil || d2 == nil {
		t.Fatalf("Greeting Documentation missing on one of the two passes: pass1=%+v pass2=%+v", d1, d2)
	}
	if d1.Value != d2.Value {
		t.Errorf("Greeting Documentation differs across cached/uncached passes: %q vs %q", d1.Value, d2.Value)
	}
}
