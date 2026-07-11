package gen

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

// lowercaseTagModule writes a temp module with a lowercase `component card`
// declared in card.gsx and used from page.gsx, mirroring hoverModule/the
// TestHoverComponentTag fixture but exercising the lowercase-tag-resolution
// rule (docs/superpowers/specs/2026-07-10-lowercase-tag-symbol-resolution-
// design.md): a lowercase tag is a component iff its name matches a
// package-level declaration. Returns dir + page.gsx's source.
func lowercaseTagModule(t *testing.T) (dir, pageSrc string) {
	t.Helper()
	if testing.Short() {
		t.Skip("skipping module-resolution test in -short mode")
	}
	dir = t.TempDir()
	repoRoot, _ := filepath.Abs("..")
	must := func(p, c string) {
		t.Helper()
		if err := os.WriteFile(filepath.Join(dir, p), []byte(c), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	must("go.mod", "module example.com/lc\n\ngo 1.26.1\n\nrequire github.com/gsxhq/gsx v0.0.0\n\nreplace github.com/gsxhq/gsx => "+repoRoot+"\n")
	must("card.gsx", "package lc\n\ncomponent card(title string) {\n\t<div>{ title }</div>\n}\n")
	pageSrc = "package lc\n\ncomponent Page() {\n\t<main><card title=\"hi\"/></main>\n}\n"
	must("page.gsx", pageSrc)
	return dir, pageSrc
}

// TestHoverLowercaseComponentTag verifies hover on a lowercase component
// invocation tag (`<card/>`, resolving against the package-level `component
// card(...)` declaration, not the old capital-only syntactic rule) shows the
// component's signature — hover.go's componentAtTag must read el.IsComponent,
// not the syntactic isComponentTag string check.
func TestHoverLowercaseComponentTag(t *testing.T) {
	t.Parallel()
	dir, pageSrc := lowercaseTagModule(t)
	h := hoverAt(t, dir, "page.gsx", pageSrc, "<card", len("<")) // on the 'card' tag name
	if h == nil || !strings.Contains(h.Contents.Value, "component card(title string)") {
		t.Fatalf("want 'component card(title string)', got %+v", h)
	}
}

// TestDefinitionLowercaseComponentTag verifies go-to-definition on a lowercase
// component tag name jumps to the `component card(...)` declaration in
// card.gsx (componentTagDeclAt must read el.IsComponent, not the syntactic
// capital-letter guess it used before — see internal/lsp/definition.go).
func TestDefinitionLowercaseComponentTag(t *testing.T) {
	t.Parallel()
	dir, pageSrc := lowercaseTagModule(t)
	uri := "file://" + filepath.Join(dir, "page.gsx")

	lines := strings.Split(pageSrc, "\n")
	var line, character int
	for i, l := range lines {
		if c := strings.Index(l, "<card"); c >= 0 {
			line, character = i, c+1 // the 'c' of 'card'
			break
		}
	}

	frame := func(v any) string {
		b, _ := json.Marshal(v)
		return "Content-Length: " + strconv.Itoa(len(b)) + "\r\n\r\n" + string(b)
	}
	in := frame(map[string]any{"jsonrpc": "2.0", "id": 1, "method": "initialize", "params": map[string]any{}})
	in += frame(map[string]any{"jsonrpc": "2.0", "method": "textDocument/didOpen",
		"params": map[string]any{"textDocument": map[string]any{"uri": uri, "version": 1, "text": pageSrc}}})
	in += frame(map[string]any{"jsonrpc": "2.0", "id": 2, "method": "textDocument/definition",
		"params": map[string]any{"textDocument": map[string]any{"uri": uri},
			"position": map[string]any{"line": line, "character": character}}})
	in += frame(map[string]any{"jsonrpc": "2.0", "method": "exit"})

	var out, errBuf bytes.Buffer
	if code := runLSP(strings.NewReader(in), &out, &errBuf, config{}, nil); code != 0 {
		t.Fatalf("runLSP=%d stderr=%s", code, errBuf.String())
	}
	loc := definitionResult(t, out.String(), 2)
	if loc == nil {
		t.Fatalf("definition returned null for lowercase component tag; out:\n%s\nstderr:\n%s", out.String(), errBuf.String())
	}
	if !strings.HasSuffix(loc.URI, "card.gsx") {
		t.Fatalf("resolved to %q, want card.gsx", loc.URI)
	}
	// "component card(title string) {" is card.gsx's line index 2; the name
	// "card" starts right after "component ".
	cardSrc := "package lc\n\ncomponent card(title string) {\n\t<div>{ title }</div>\n}\n"
	declLine := 2
	declCol := strings.Index(strings.Split(cardSrc, "\n")[declLine], "card")
	if loc.Range.Start.Line != declLine || loc.Range.Start.Character != declCol {
		t.Fatalf("landed at L%d:C%d, want L%d:C%d (the card decl name)",
			loc.Range.Start.Line, loc.Range.Start.Character, declLine, declCol)
	}
}

// TestDefinitionLowercaseComponentAttrParam verifies go-to-definition on the
// `title` ATTRIBUTE name in `<card title="hi"/>` (a lowercase component
// invocation) resolves to the `title` PARAMETER of `component
// card(title string)` — componentAttrAtOffset (definition_attr.go) must read
// el.IsComponent so the attr-name lookup fires on a lowercase tag at all.
func TestDefinitionLowercaseComponentAttrParam(t *testing.T) {
	t.Parallel()
	dir, pageSrc := lowercaseTagModule(t)
	uri := "file://" + filepath.Join(dir, "page.gsx")

	// Cursor on the 't' of the `title` ATTRIBUTE (the one followed by '="hi"').
	lines := strings.Split(pageSrc, "\n")
	var line, character int
	for i, l := range lines {
		if c := strings.Index(l, `title="hi"`); c >= 0 {
			line, character = i, c
			break
		}
	}

	frame := func(v any) string {
		b, _ := json.Marshal(v)
		return "Content-Length: " + strconv.Itoa(len(b)) + "\r\n\r\n" + string(b)
	}
	in := frame(map[string]any{"jsonrpc": "2.0", "id": 1, "method": "initialize", "params": map[string]any{}})
	in += frame(map[string]any{"jsonrpc": "2.0", "method": "textDocument/didOpen",
		"params": map[string]any{"textDocument": map[string]any{"uri": uri, "version": 1, "text": pageSrc}}})
	in += frame(map[string]any{"jsonrpc": "2.0", "id": 2, "method": "textDocument/definition",
		"params": map[string]any{"textDocument": map[string]any{"uri": uri},
			"position": map[string]any{"line": line, "character": character}}})
	in += frame(map[string]any{"jsonrpc": "2.0", "method": "exit"})

	var out, errBuf bytes.Buffer
	if code := runLSP(strings.NewReader(in), &out, &errBuf, config{}, nil); code != 0 {
		t.Fatalf("runLSP=%d stderr=%s", code, errBuf.String())
	}
	loc := definitionResult(t, out.String(), 2)
	if loc == nil {
		t.Fatalf("definition returned null for lowercase-component attr name; out:\n%s\nstderr:\n%s", out.String(), errBuf.String())
	}
	if !strings.HasSuffix(loc.URI, "card.gsx") {
		t.Fatalf("resolved to %q, want card.gsx", loc.URI)
	}
	// The decl line is "component card(title string) {" (line index 2).
	cardSrc := "package lc\n\ncomponent card(title string) {\n\t<div>{ title }</div>\n}\n"
	declLine := 2
	declCol := strings.Index(strings.Split(cardSrc, "\n")[declLine], "title")
	if loc.Range.Start.Line != declLine || loc.Range.Start.Character != declCol {
		t.Fatalf("landed at L%d:C%d, want L%d:C%d (the title param)",
			loc.Range.Start.Line, loc.Range.Start.Character, declLine, declCol)
	}
}

// TestHoverUndeclaredLowercaseTagIsPlainElement is the negative case: `<span/>`
// has no matching package-level declaration, so it resolves as a leaf HTML
// element (rule 3 of the lowercase-tag-resolution design), not a component —
// hovering its tag name must NOT produce a component signature (it falls
// through hover's H1 component-tag path to null, same as any other plain
// markup — see TestHoverNonExprNull).
func TestHoverUndeclaredLowercaseTagIsPlainElement(t *testing.T) {
	t.Parallel()
	dir, _ := lowercaseTagModule(t)
	// Rewrite page.gsx to also contain an undeclared <span/> leaf tag.
	pageSrc := "package lc\n\ncomponent Page() {\n\t<main><span>hi</span><card title=\"hi\"/></main>\n}\n"
	if err := os.WriteFile(filepath.Join(dir, "page.gsx"), []byte(pageSrc), 0o644); err != nil {
		t.Fatal(err)
	}
	h := hoverAt(t, dir, "page.gsx", pageSrc, "<span", len("<")) // on the 'span' tag name
	if h != nil {
		t.Fatalf("hover on an undeclared lowercase tag must be null (plain element, not a component), got %+v", h)
	}
}

// TestDefinitionUndeclaredLowercaseTagNull is the go-to-definition negative
// case mirroring TestHoverUndeclaredLowercaseTagIsPlainElement: `<span/>`
// resolves as a leaf, so go-to-definition on its tag name must return null,
// not a false jump.
func TestDefinitionUndeclaredLowercaseTagNull(t *testing.T) {
	t.Parallel()
	dir, _ := lowercaseTagModule(t)
	pageSrc := "package lc\n\ncomponent Page() {\n\t<main><span>hi</span><card title=\"hi\"/></main>\n}\n"
	if err := os.WriteFile(filepath.Join(dir, "page.gsx"), []byte(pageSrc), 0o644); err != nil {
		t.Fatal(err)
	}
	uri := "file://" + filepath.Join(dir, "page.gsx")

	lines := strings.Split(pageSrc, "\n")
	var line, character int
	for i, l := range lines {
		if c := strings.Index(l, "<span"); c >= 0 {
			line, character = i, c+1 // the 's' of 'span'
			break
		}
	}

	frame := func(v any) string {
		b, _ := json.Marshal(v)
		return "Content-Length: " + strconv.Itoa(len(b)) + "\r\n\r\n" + string(b)
	}
	in := frame(map[string]any{"jsonrpc": "2.0", "id": 1, "method": "initialize", "params": map[string]any{}})
	in += frame(map[string]any{"jsonrpc": "2.0", "method": "textDocument/didOpen",
		"params": map[string]any{"textDocument": map[string]any{"uri": uri, "version": 1, "text": pageSrc}}})
	in += frame(map[string]any{"jsonrpc": "2.0", "id": 2, "method": "textDocument/definition",
		"params": map[string]any{"textDocument": map[string]any{"uri": uri},
			"position": map[string]any{"line": line, "character": character}}})
	in += frame(map[string]any{"jsonrpc": "2.0", "method": "exit"})

	var out, errBuf bytes.Buffer
	if code := runLSP(strings.NewReader(in), &out, &errBuf, config{}, nil); code != 0 {
		t.Fatalf("runLSP=%d stderr=%s", code, errBuf.String())
	}
	loc := definitionResult(t, out.String(), 2)
	if loc != nil {
		t.Fatalf("definition on undeclared <span/> must be null, got %+v", loc)
	}
}
