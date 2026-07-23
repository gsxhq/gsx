package lsp

import (
	"go/token"
	"strings"
	"testing"

	gsxast "github.com/gsxhq/gsx/ast"
	"github.com/gsxhq/gsx/internal/htmldata"
	gsxparser "github.com/gsxhq/gsx/parser"
)

// parseElement parses src (a full .gsx file) and returns the first element whose
// Tag == tag. Used to build the CLASSIFICATION-style element htmlAttrItems reads.
func parseElement(t *testing.T, src, tag string) *gsxast.Element {
	t.Helper()
	fset := token.NewFileSet()
	f, err := gsxparser.ParseFile(fset, "page.gsx", []byte(src), 0)
	if err != nil {
		t.Fatalf("ParseFile: %v", err)
	}
	var el *gsxast.Element
	gsxast.Inspect(f, func(n gsxast.Node) bool {
		if e, ok := n.(*gsxast.Element); ok && e.Tag == tag && el == nil {
			el = e
		}
		return true
	})
	if el == nil {
		t.Fatalf("no <%s> element in source", tag)
	}
	return el
}

// itemByLabel returns the first item whose Label == label, or nil.
func itemByLabel(items []CompletionItem, label string) *CompletionItem {
	for i := range items {
		if items[i].Label == label {
			return &items[i]
		}
	}
	return nil
}

func labelSet(items []CompletionItem) map[string]bool {
	m := map[string]bool{}
	for _, it := range items {
		m[it.Label] = true
	}
	return m
}

// TestHTMLTagItems checks the full-dataset tag list: one item per htmldata.Tag,
// kind ciKindProperty, doc carried from the dataset, and the tier flip — a
// lowercase/empty prefix keeps HTML at tierContext, a capitalized prefix demotes
// it to tierSecondary so PascalCase component candidates lead.
func TestHTMLTagItems(t *testing.T) {
	lower := htmlTagItems(false, "<di", 1, 3, encUTF8)
	if len(lower) != len(htmldata.Tags) {
		t.Fatalf("len(items) = %d, want %d (one per dataset tag)", len(lower), len(htmldata.Tags))
	}
	div := itemByLabel(lower, "div")
	if div == nil {
		t.Fatal("HTML tag list missing `div`")
	}
	if div.Kind != ciKindProperty {
		t.Errorf("div.Kind = %d, want ciKindProperty", div.Kind)
	}
	if div.Documentation == nil || div.Documentation.Value == "" {
		t.Errorf("div.Documentation = %+v, want non-empty markdown", div.Documentation)
	}
	if !strings.HasPrefix(div.SortText, "05") {
		t.Errorf("lowercase-prefix div.SortText = %q, want tierContext (05)", div.SortText)
	}

	upper := htmlTagItems(true, "<Di", 1, 3, encUTF8)
	divU := itemByLabel(upper, "div")
	if divU == nil {
		t.Fatal("capitalized HTML tag list missing `div`")
	}
	if !strings.HasPrefix(divU.SortText, "20") {
		t.Errorf("capitalized-prefix div.SortText = %q, want tierSecondary (20)", divU.SortText)
	}
}

// TestHTMLAttrItemsExcludesPresent checks that an attribute already authored on
// the element (here `class`) is excluded by lowercase name, while other
// attributes remain offered.
func TestHTMLAttrItemsExcludesPresent(t *testing.T) {
	el := parseElement(t, "package p\ncomponent C() {\n\t<div class=\"x\"/>\n}\n", "div")
	items := htmlAttrItems(el, "div", false, tierContext, "", 0, 0, encUTF8, false)
	labels := labelSet(items)
	if labels["class"] {
		t.Errorf("attr list still offers already-present `class`: %v", labels)
	}
	if !labels["id"] {
		t.Errorf("attr list missing global `id`: %v", labels)
	}
}

// TestHTMLAttrItemsCursorOnPresentAttrStaysOffered checks the parity carve-out
// with componentAttrItems (see
// TestComponentAttrItemsCursorOnBoundAttrStaysOffered): when the cursor sits
// on the exact token of an already-present attribute — `<div class` with the
// cursor right after "class" — that attribute must stay offered rather than
// be excluded as a duplicate, since the user is still mid-typing it. A
// different present attribute (`id`) stays excluded regardless of what token
// is typed, and `class` itself goes back to excluded once the typed token no
// longer exact-matches it (a shorter prefix, or none at all).
func TestHTMLAttrItemsCursorOnPresentAttrStaysOffered(t *testing.T) {
	el := parseElement(t, "package p\ncomponent C() {\n\t<div class=\"x\" id=\"y\"/>\n}\n", "div")

	text := "class"
	items := htmlAttrItems(el, "div", false, tierContext, text, 0, len(text), encUTF8, false)
	labels := labelSet(items)
	if !labels["class"] {
		t.Errorf("labels = %v, want `class` offered (cursor is on its own token)", labels)
	}
	if labels["id"] {
		t.Errorf("labels = %v, want `id` still excluded (present, cursor not on it)", labels)
	}
	class := itemByLabel(items, "class")
	if class == nil || class.TextEdit == nil || class.TextEdit.NewText != `class=""` {
		t.Errorf("class item = %+v, want NewText %q", class, `class=""`)
	}

	for _, typed := range []string{"cl", ""} {
		items := htmlAttrItems(el, "div", false, tierContext, typed, 0, len(typed), encUTF8, false)
		labels := labelSet(items)
		if labels["class"] {
			t.Errorf("typed %q: labels = %v, want `class` excluded (not an exact match)", typed, labels)
		}
		if labels["id"] {
			t.Errorf("typed %q: labels = %v, want `id` excluded", typed, labels)
		}
	}
}

// TestHTMLAttrItemsBooleanBareName checks a presence-only attribute (`hidden`,
// dataset valueSet "v") inserts the bare name with no `=""` and no FilterText.
func TestHTMLAttrItemsBooleanBareName(t *testing.T) {
	el := parseElement(t, "package p\ncomponent C() {\n\t<div/>\n}\n", "div")
	items := htmlAttrItems(el, "div", false, tierContext, "", 0, 0, encUTF8, false)
	hidden := itemByLabel(items, "hidden")
	if hidden == nil {
		t.Fatal("attr list missing boolean `hidden`")
	}
	if hidden.TextEdit == nil || hidden.TextEdit.NewText != "hidden" {
		t.Errorf("hidden.TextEdit = %+v, want bare NewText %q", hidden.TextEdit, "hidden")
	}
	if hidden.FilterText != "" {
		t.Errorf("hidden.FilterText = %q, want empty (bare name == label)", hidden.FilterText)
	}
}

// TestHTMLAttrItemsValueInsertsEquals checks a value attribute (`class`, on an
// element that does not yet have it) inserts `name=""` with FilterText = name so
// the client keeps matching against the typed name, not the `=""` suffix.
func TestHTMLAttrItemsValueInsertsEquals(t *testing.T) {
	el := parseElement(t, "package p\ncomponent C() {\n\t<div/>\n}\n", "div")
	items := htmlAttrItems(el, "div", false, tierContext, "", 0, 0, encUTF8, false)
	class := itemByLabel(items, "class")
	if class == nil {
		t.Fatal("attr list missing value attr `class`")
	}
	if class.TextEdit == nil || class.TextEdit.NewText != `class=""` {
		t.Errorf("class.TextEdit = %+v, want NewText %q", class.TextEdit, `class=""`)
	}
	if class.FilterText != "class" {
		t.Errorf("class.FilterText = %q, want %q", class.FilterText, "class")
	}
}

// TestHTMLAttrItemsSnippetInsertsTabstop checks the snippet-gated path: with
// snippet=true, a value attribute (`class`) inserts `class="$1"` with
// InsertTextFormat = Snippet (so the cursor lands inside the quotes) and
// FilterText UNCHANGED (still the bare name, matching the non-snippet case) —
// while a boolean attribute (`hidden`) stays a bare, unformatted insert:
// there are no quotes for a snippet tabstop to land inside.
func TestHTMLAttrItemsSnippetInsertsTabstop(t *testing.T) {
	el := parseElement(t, "package p\ncomponent C() {\n\t<div/>\n}\n", "div")
	items := htmlAttrItems(el, "div", false, tierContext, "", 0, 0, encUTF8, true)

	class := itemByLabel(items, "class")
	if class == nil {
		t.Fatal("attr list missing value attr `class`")
	}
	if class.TextEdit == nil || class.TextEdit.NewText != `class="$1"` {
		t.Errorf("class.TextEdit = %+v, want NewText %q", class.TextEdit, `class="$1"`)
	}
	if class.InsertTextFormat != insertTextFormatSnippet {
		t.Errorf("class.InsertTextFormat = %d, want insertTextFormatSnippet (2)", class.InsertTextFormat)
	}
	if class.FilterText != "class" {
		t.Errorf("class.FilterText = %q, want %q (unchanged by snippet)", class.FilterText, "class")
	}

	hidden := itemByLabel(items, "hidden")
	if hidden == nil {
		t.Fatal("attr list missing boolean `hidden`")
	}
	if hidden.TextEdit == nil || hidden.TextEdit.NewText != "hidden" {
		t.Errorf("hidden.TextEdit = %+v, want bare NewText %q even with snippet support", hidden.TextEdit, "hidden")
	}
	if hidden.InsertTextFormat != 0 {
		t.Errorf("hidden.InsertTextFormat = %d, want 0 (PlainText/omitted; no quotes to tabstop into)", hidden.InsertTextFormat)
	}
	if hidden.FilterText != "" {
		t.Errorf("hidden.FilterText = %q, want empty (bare name == label)", hidden.FilterText)
	}
}

// TestHTMLAttrItemsInputType checks the per-tag attribute path: `type` on
// <input> is an enumerated (non-boolean) attribute, so it inserts `type=""`.
func TestHTMLAttrItemsInputType(t *testing.T) {
	el := parseElement(t, "package p\ncomponent C() {\n\t<input/>\n}\n", "input")
	items := htmlAttrItems(el, "input", false, tierContext, "", 0, 0, encUTF8, false)
	typ := itemByLabel(items, "type")
	if typ == nil {
		t.Fatal("input attr list missing `type`")
	}
	if typ.TextEdit == nil || typ.TextEdit.NewText != `type=""` {
		t.Errorf("type.TextEdit = %+v, want NewText %q", typ.TextEdit, `type=""`)
	}
}

// TestHTMLAttrItemsHTMXGated checks hx-* attributes appear only when htmx is
// enabled, and that a dashed hx- name is a normal value-attribute insert.
func TestHTMLAttrItemsHTMXGated(t *testing.T) {
	el := parseElement(t, "package p\ncomponent C() {\n\t<div/>\n}\n", "div")

	off := htmlAttrItems(el, "div", false, tierContext, "", 0, 0, encUTF8, false)
	if labelSet(off)["hx-get"] {
		t.Errorf("hx-get offered with htmx disabled")
	}

	on := htmlAttrItems(el, "div", true, tierContext, "", 0, 0, encUTF8, false)
	hxGet := itemByLabel(on, "hx-get")
	if hxGet == nil {
		t.Fatalf("hx-get NOT offered with htmx enabled")
	}
	if hxGet.TextEdit == nil || hxGet.TextEdit.NewText != `hx-get=""` {
		t.Errorf("hx-get.TextEdit = %+v, want NewText %q", hxGet.TextEdit, `hx-get=""`)
	}
}

// TestHTMLValueItemsInputTypeSubmit checks the enumerated-value path: <input>
// type's value set includes "submit", offered as ciKindEnumMember at tierContext.
func TestHTMLValueItemsInputTypeSubmit(t *testing.T) {
	items := htmlValueItems("input", "type", "", 0, 0, encUTF8)
	submit := itemByLabel(items, "submit")
	if submit == nil {
		t.Fatalf("input[type] values missing `submit`: %v", labelSet(items))
	}
	if submit.Kind != ciKindEnumMember {
		t.Errorf("submit.Kind = %d, want ciKindEnumMember", submit.Kind)
	}
	if !strings.HasPrefix(submit.SortText, "05") {
		t.Errorf("submit.SortText = %q, want tierContext (05)", submit.SortText)
	}
}

// TestHTMLValueItemsGlobalFallback checks the value set resolves through the
// same-named GLOBAL attribute when the tag itself does not declare it: `dir` is
// a global enumerated attribute (ltr/rtl/auto), offered on any element.
func TestHTMLValueItemsGlobalFallback(t *testing.T) {
	items := htmlValueItems("div", "dir", "", 0, 0, encUTF8)
	labels := labelSet(items)
	for _, v := range []string{"ltr", "rtl", "auto"} {
		if !labels[v] {
			t.Errorf("div[dir] values missing %q: %v", v, labels)
		}
	}
}

// TestHTMLValueItemsFreeform checks a freeform attribute (no value set) yields
// nothing, so completion offers no bogus enum.
func TestHTMLValueItemsFreeform(t *testing.T) {
	if items := htmlValueItems("div", "class", "", 0, 0, encUTF8); len(items) != 0 {
		t.Fatalf("freeform div[class] values = %v, want empty", items)
	}
	if items := htmlValueItems("div", "no-such-attr", "", 0, 0, encUTF8); len(items) != 0 {
		t.Fatalf("unknown attr values = %v, want empty", items)
	}
}

// TestTagCompletionMergeHTMLAfterComponents drives the handler at a `<di▮`
// cursor with a fake analyzer that also supplies components, and checks the
// merge: the HTML `div` is present (Property, doc non-empty), and because the
// typed prefix is lowercase both components and HTML sort at tierContext.
func TestTagCompletionMergeHTMLAfterComponents(t *testing.T) {
	uri := "file:///m/a.gsx"
	text := "package p\n\ncomponent C() {\n\t<div><di</div>\n}\n"
	off := strings.LastIndex(text, "<di") + len("<di")
	pos := positionForByteOffset(text, off, encUTF16)

	a := tagCompletionAnalyzer{ephPkg: componentTagFixturePackage()}
	out := drive(t, a, initFrame()+didOpenFrame(uri, text)+
		completionFrame(2, uri, pos)+exitFrame())
	items := decodeCompletionItems(t, out, 2)

	div := itemByLabel(items, "div")
	if div == nil {
		t.Fatalf("merged tag completion missing HTML `div`; labels=%v", labelSet(items))
	}
	if div.Kind != ciKindProperty {
		t.Errorf("div.Kind = %d, want ciKindProperty", div.Kind)
	}
	if div.Documentation == nil || div.Documentation.Value == "" {
		t.Errorf("div.Documentation = %+v, want non-empty markdown", div.Documentation)
	}
	if !strings.HasPrefix(div.SortText, "05") {
		t.Errorf("lowercase-prefix div.SortText = %q, want tierContext (05)", div.SortText)
	}
	// Components still present alongside HTML.
	if !labelSet(items)["Card"] {
		t.Errorf("merged tag completion missing component `Card`; labels=%v", labelSet(items))
	}
}

// TestAttrNameCompletionHTMLPath drives the handler at an HTML element's
// attribute-name position. The fake analyzer never resolves the element to a
// component (its ephemeral package has no matching element), so the handler
// takes the HTML path: `class` (value attr) and `hidden` (bare boolean) are both
// offered, computed purely from the classification element — no codegen facts.
func TestAttrNameCompletionHTMLPath(t *testing.T) {
	uri := "file:///m/a.gsx"
	text := "package p\n\ncomponent C() {\n\t<div ></div>\n}\n"
	off := strings.Index(text, "<div ") + len("<div ")
	pos := positionForByteOffset(text, off, encUTF16)

	a := tagCompletionAnalyzer{ephPkg: &Package{}}
	out := drive(t, a, initFrame()+didOpenFrame(uri, text)+
		completionFrame(2, uri, pos)+exitFrame())
	items := decodeCompletionItems(t, out, 2)
	labels := labelSet(items)
	if !labels["class"] || !labels["hidden"] {
		t.Fatalf("HTML attr-name completion labels = %v, want class+hidden present", labels)
	}
	if hidden := itemByLabel(items, "hidden"); hidden == nil || hidden.TextEdit.NewText != "hidden" {
		t.Errorf("hidden must insert bare name; got %+v", hidden)
	}
	if class := itemByLabel(items, "class"); class == nil || class.TextEdit.NewText != `class=""` {
		t.Errorf("class must insert `class=\"\"`; got %+v", class)
	}
}

// TestAttrNameCompletionHTMLPathSnippet drives the same HTML attribute-name
// position as TestAttrNameCompletionHTMLPath, but through an initialize frame
// that advertises snippetSupport (snippetInitFrame). The end-to-end wiring
// under test: handleInitialize captures the capability onto s.snippetSupport,
// which reaches htmlAttrItems via attrNameCompletion — so `class` now inserts
// `class="$1"` with InsertTextFormat = Snippet, while `hidden` (no quotes to
// place a cursor inside) is unaffected.
func TestAttrNameCompletionHTMLPathSnippet(t *testing.T) {
	uri := "file:///m/a.gsx"
	text := "package p\n\ncomponent C() {\n\t<div ></div>\n}\n"
	off := strings.Index(text, "<div ") + len("<div ")
	pos := positionForByteOffset(text, off, encUTF16)

	a := tagCompletionAnalyzer{ephPkg: &Package{}}
	out := drive(t, a, snippetInitFrame()+didOpenFrame(uri, text)+
		completionFrame(2, uri, pos)+exitFrame())
	items := decodeCompletionItems(t, out, 2)

	class := itemByLabel(items, "class")
	if class == nil || class.TextEdit == nil || class.TextEdit.NewText != `class="$1"` {
		t.Fatalf("class must insert `class=\"$1\"` under snippetSupport; got %+v", class)
	}
	if class.InsertTextFormat != insertTextFormatSnippet {
		t.Errorf("class.InsertTextFormat = %d, want insertTextFormatSnippet (2)", class.InsertTextFormat)
	}

	hidden := itemByLabel(items, "hidden")
	if hidden == nil || hidden.TextEdit == nil || hidden.TextEdit.NewText != "hidden" {
		t.Errorf("hidden must still insert the bare name under snippetSupport; got %+v", hidden)
	}
	if hidden.InsertTextFormat != 0 {
		t.Errorf("hidden.InsertTextFormat = %d, want 0 (no quotes to tabstop into)", hidden.InsertTextFormat)
	}
}

// TestAttrValueCompletionHTMLPath drives the handler at an HTML attribute VALUE
// position (`<input type="▮"/>`) and checks the enumerated value `submit` is
// offered. Needs no analyzer at all — a pure dataset lookup.
func TestAttrValueCompletionHTMLPath(t *testing.T) {
	uri := "file:///m/a.gsx"
	text := "package p\n\ncomponent C() {\n\t<input type=\"\"/>\n}\n"
	off := strings.Index(text, `type="`) + len(`type="`)
	pos := positionForByteOffset(text, off, encUTF16)

	out := drive(t, nilAnalyzer{}, initFrame()+didOpenFrame(uri, text)+
		completionFrame(2, uri, pos)+exitFrame())
	items := decodeCompletionItems(t, out, 2)
	if !labelSet(items)["submit"] {
		t.Fatalf("HTML attr-value completion labels = %v, want `submit` present", labelSet(items))
	}
}
