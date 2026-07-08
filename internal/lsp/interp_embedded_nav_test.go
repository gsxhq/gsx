package lsp

import (
	"go/token"
	"go/types"
	"strings"
	"testing"
)

// interpEmbeddedSrc exercises the interpolation-embedded nav positions the
// interp-embedded-literals branch created: a <tag>/<> literal and a prefixed
// backtick literal used in operand position INSIDE a body `{ }` interpolation,
// where they ride in Interp.Embedded rather than the direct body-child path.
const interpEmbeddedSrc = `package page

import "github.com/gsxhq/gsx"

func wrap(n gsx.Node) gsx.Node { return n }

func emphasize(s string) string { return "*" + s + "*" }

component Badge(count int, name string) {
	<b>{name}: {count}</b>
}

component Uses(n int, label string) {
	<div>{ wrap(<Badge count={n} name={label}/>) }</div>
	<p>{ emphasize(f` + "`hi @{label}`" + `) }</p>
}
`

// TestInterpEmbeddedDefinition asserts go-to-definition descends into
// Interp.Embedded: an embedded component tag jumps to its declaration, an
// identifier in an embedded tag's prop/interp resolves to its enclosing-scope
// declaration, and an @{ } hole inside an embedded f-literal resolves too.
func TestInterpEmbeddedDefinition(t *testing.T) {
	src := interpEmbeddedSrc
	pkg, path := analyzedLSPPackage(t, src)

	lineCol := func(off int) (int, int) {
		return strings.Count(src[:off], "\n") + 1, off - strings.LastIndexByte(src[:off], '\n')
	}

	compBadge := strings.Index(src, "component Badge") + len("component ")
	paramN := strings.Index(src, "n int, label string") // Uses' param n
	paramLabel := strings.Index(src, "label string")    // Uses' param label

	// 1. Embedded component tag name → component Badge declaration.
	t.Run("embedded component tag", func(t *testing.T) {
		off := strings.Index(src, "wrap(<Badge") + len("wrap(<")
		decls, ok := componentTagDeclAt(pkg, path, off)
		if !ok || len(decls) == 0 {
			t.Fatal("embedded component tag did not resolve to a declaration")
		}
		wantLine, wantCol := lineCol(compBadge)
		if decls[0].Line != wantLine || decls[0].Column != wantCol {
			t.Errorf("Badge decl at %d:%d, want %d:%d", decls[0].Line, decls[0].Column, wantLine, wantCol)
		}
	})

	// 2a. Identifier inside an embedded tag's prop value: `count={n}` → param n.
	t.Run("embedded prop expr ident", func(t *testing.T) {
		off := strings.Index(src, "count={n}") + len("count={")
		dp, ok := exprDefinitionAt(pkg, path, off)
		if !ok {
			t.Fatal("embedded prop expr ident did not resolve")
		}
		wantLine, wantCol := lineCol(paramN)
		if !strings.HasSuffix(dp.Filename, ".gsx") || dp.Line != wantLine || dp.Column != wantCol {
			t.Errorf("n resolved to %s:%d:%d, want .gsx %d:%d", dp.Filename, dp.Line, dp.Column, wantLine, wantCol)
		}
	})

	// 2b. Identifier inside another embedded prop value: `name={label}` → param label.
	t.Run("embedded prop expr ident label", func(t *testing.T) {
		off := strings.Index(src, "name={label}") + len("name={")
		dp, ok := exprDefinitionAt(pkg, path, off)
		if !ok {
			t.Fatal("embedded prop expr ident (label) did not resolve")
		}
		wantLine, wantCol := lineCol(paramLabel)
		if dp.Line != wantLine || dp.Column != wantCol {
			t.Errorf("label resolved to %d:%d, want %d:%d", dp.Line, dp.Column, wantLine, wantCol)
		}
	})

	// 3. @{ } hole inside an embedded f-literal: `f`hi @{label}`` → param label.
	t.Run("embedded f-literal hole", func(t *testing.T) {
		off := strings.Index(src, "@{label}") + len("@{")
		dp, ok := exprDefinitionAt(pkg, path, off)
		if !ok {
			t.Fatal("embedded f-literal hole did not resolve")
		}
		wantLine, wantCol := lineCol(paramLabel)
		if dp.Line != wantLine || dp.Column != wantCol {
			t.Errorf("hole label resolved to %d:%d, want %d:%d", dp.Line, dp.Column, wantLine, wantCol)
		}
	})
}

// TestInterpEmbeddedHover asserts hover descends into Interp.Embedded for the
// same three positions: the embedded component tag shows its signature, and the
// embedded prop ident / f-literal hole show the resolved object's Go type.
func TestInterpEmbeddedHover(t *testing.T) {
	src := interpEmbeddedSrc
	pkg, path := analyzedLSPPackage(t, src)

	// 1. Embedded component tag → component signature (AST-only path).
	t.Run("embedded component tag sig", func(t *testing.T) {
		off := strings.Index(src, "wrap(<Badge") + len("wrap(<")
		c, nameStart, nameLen, ok := componentAtTag(pkg, path, off)
		if !ok {
			t.Fatal("componentAtTag did not resolve an embedded tag")
		}
		if c.Name != "Badge" {
			t.Errorf("hovered component = %q, want Badge", c.Name)
		}
		if got := src[nameStart : nameStart+nameLen]; got != "Badge" {
			t.Errorf("hover range = %q, want Badge", got)
		}
	})

	// 2. Embedded prop ident hover → resolved object via ExprMap bridge.
	t.Run("embedded prop ident hover", func(t *testing.T) {
		off := strings.Index(src, "count={n}") + len("count={")
		obj := hoverObjectAt(t, pkg, path, off)
		if obj == nil || obj.Name() != "n" {
			t.Fatalf("prop ident hover obj = %v, want n", obj)
		}
	})

	// 3. Embedded f-literal hole hover → resolved object.
	t.Run("embedded f-literal hole hover", func(t *testing.T) {
		off := strings.Index(src, "@{label}") + len("@{")
		obj := hoverObjectAt(t, pkg, path, off)
		if obj == nil || obj.Name() != "label" {
			t.Fatalf("hole hover obj = %v, want label", obj)
		}
	})
}

// hoverObjectAt mirrors handleHover's ExprMap bridge to return the go/types
// object under the cursor for a plain (non-ctrl, non-piped) expression span.
func hoverObjectAt(t *testing.T, pkg *Package, path string, off int) types.Object {
	t.Helper()
	node, exprPos := exprNodeAtOffset(pkg, path, off)
	if node == nil {
		t.Fatal("no expr node at cursor")
	}
	skel := pkg.ExprMap[node]
	if skel == nil {
		t.Fatal("no ExprMap entry for embedded node")
	}
	exprStart := pkg.GSXFset.Position(exprPos).Offset
	skelPos := skel.Pos() + token.Pos(off-exprStart)
	id := innermostIdent(skel, skelPos)
	if id == nil {
		t.Fatal("no identifier under cursor in skeleton")
	}
	obj := pkg.Info.Uses[id]
	if obj == nil {
		obj = pkg.Info.Defs[id]
	}
	return obj
}
