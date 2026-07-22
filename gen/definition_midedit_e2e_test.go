package gen

import (
	"path/filepath"
	"strings"
	"testing"
)

// cursorOnNeedle returns the 0-based line/character of a cursor placed ON the
// last character of needle inside page (the natural "I just typed this" spot).
func cursorOnNeedle(t *testing.T, page, needle string) (line, character int) {
	t.Helper()
	i := strings.Index(page, needle)
	if i < 0 {
		t.Fatalf("needle %q not found in page", needle)
	}
	off := i + len(needle) - 1
	line = strings.Count(page[:off], "\n")
	character = off - (strings.LastIndexByte(page[:off], '\n') + 1)
	return line, character
}

// TestDefinitionMidEditUnclosedTag is the core regression: go-to-definition on a
// cross-package component-value tag that has NOT been closed yet (`<widget.X`,
// no `/>`). The unclosed tag makes the buffer unparseable, so analysis falls back
// to a diagnostics-only shell and the primary resolver cascade answers nothing;
// the completion-repair + ephemeral-analysis fallback heals `<widget.X/>` and
// resolves X to its declaration in widget/named.gsx.
func TestDefinitionMidEditUnclosedTag(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("skipping module-resolution test in -short mode")
	}
	dir := t.TempDir()
	// `<widget.X` with no closing `/>` — a mid-edit half-typed tag.
	page := "package page\n\nimport \"example.com/x/widget\"\n\ncomponent Page() {\n\t<widget.X\n}\n"
	writeCrossPkgValueModule(t, dir, page)
	line, character := cursorOnNeedle(t, page, "widget.X")

	loc, hov := runCrossPkgValueDefinition(t, dir, page, line, character)
	if loc == nil {
		t.Fatal("mid-edit tag definition returned null; want widget/named.gsx X declaration")
	}
	if !strings.HasSuffix(loc.URI, filepath.Join("widget", "named.gsx")) {
		t.Fatalf("resolved to %q, want widget/named.gsx", loc.URI)
	}
	if strings.HasSuffix(loc.URI, ".x.go") {
		t.Fatalf("resolved into a generated .x.go (%q); must never jump into generated code", loc.URI)
	}
	wantLine, wantCol := wantXLocation(t)
	if loc.Range.Start.Line != wantLine || loc.Range.Start.Character != wantCol {
		t.Fatalf("mid-edit gd landed at L%d:C%d, want L%d:C%d (the X value decl)",
			loc.Range.Start.Line, loc.Range.Start.Character, wantLine, wantCol)
	}
	if hov == nil {
		t.Fatal("mid-edit hover on the unclosed tag was null; want the value signature")
	}
	if !strings.Contains(hov.Contents.Value, "gsx.Node") {
		t.Fatalf("mid-edit hover = %q, want the func(...gsx.Attr) gsx.Node signature", hov.Contents.Value)
	}
}

// TestDefinitionMidEditInterpInUnclosedAttr covers a NON-tag cursor: an
// identifier inside an attribute expression whose interpolation and enclosing
// component tag are both still open (`foo={ widget.Menu`, no `}` and no `/>`).
// The completion patch set closes both (`}/>`), and the fallback resolves the
// cross-package reference under the cursor. This proves the fallback heals
// expression positions, not just tag names.
func TestDefinitionMidEditInterpInUnclosedAttr(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("skipping module-resolution test in -short mode")
	}
	dir := t.TempDir()
	page := "package page\n\nimport \"example.com/x/widget\"\n\ncomponent Page() {\n\t<widget.X foo={ widget.Menu\n}\n"
	writeCrossPkgValueModule(t, dir, page)
	line, character := cursorOnNeedle(t, page, "widget.Menu")

	loc, hov := runCrossPkgValueDefinition(t, dir, page, line, character)
	if loc == nil {
		t.Fatal("mid-edit interp definition returned null; want widget/named.gsx Menu declaration")
	}
	if !strings.HasSuffix(loc.URI, filepath.Join("widget", "named.gsx")) {
		t.Fatalf("resolved to %q, want widget/named.gsx", loc.URI)
	}
	if hov == nil {
		t.Fatal("mid-edit hover on the interp reference was null")
	}
}

// TestDefinitionMidEditUnhealableGarbage confirms the fallback fails soft: a
// buffer no single repair patch can heal yields a null result and no error (the
// server keeps serving).
func TestDefinitionMidEditUnhealableGarbage(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("skipping module-resolution test in -short mode")
	}
	dir := t.TempDir()
	// Structurally broken markup no self-close / interp-close patch can repair.
	page := "package page\n\ncomponent Page() {\n\t<div><span></div> foo }} { <<\n}\n"
	writeCrossPkgValueModule(t, dir, page)
	line, character := cursorOnNeedle(t, page, "foo")

	loc, _ := runCrossPkgValueDefinition(t, dir, page, line, character)
	if loc != nil {
		t.Fatalf("unhealable buffer resolved to %+v; want null", loc)
	}
}
