package lsp

import (
	"go/token"
	"strings"
	"testing"

	gsxast "github.com/gsxhq/gsx/ast"
	gsxparser "github.com/gsxhq/gsx/parser"
)

// parseOnlyPackage parses src as a .gsx file into a Package with only GSXFset
// and Files set (no types). Sufficient for testing exprNodeAtOffset.
func parseOnlyPackage(t *testing.T, name, src string) (*Package, string) {
	t.Helper()
	fset := token.NewFileSet()
	f, err := gsxparser.ParseFile(fset, name, []byte(src), 0)
	if err != nil {
		t.Fatal(err)
	}
	return &Package{GSXFset: fset, Files: map[string]*gsxast.File{name: f}}, name
}

func TestExprNodeAtOffset(t *testing.T) {
	// Build a Package with one .gsx file parsed by the gsx parser, no types.
	src := "package x\n\ncomponent C(u User) {\n\t<div>{ u.Name }</div>\n}\n"
	pkg, path := parseOnlyPackage(t, "c.gsx", src)
	// offset of 'N' in "u.Name"
	off := strings.Index(src, "u.Name") + 2
	node, exprPos := exprNodeAtOffset(pkg, path, off)
	if node == nil {
		t.Fatalf("no expr node found at offset %d", off)
	}
	if _, ok := node.(*gsxast.Interp); !ok {
		t.Fatalf("node = %T, want *gsxast.Interp", node)
	}
	if !exprPos.IsValid() {
		t.Fatalf("exprNodeAtOffset returned invalid ExprPos")
	}
}

// TestComponentTagDeclAtByo verifies that componentTagDeclAt resolves a byo
// component tag (one whose sole param is an author struct) to the correct
// component declaration, using a synthetic CrossIndex. This is the tag→decl
// LSP guard: a cursor on "Button" in <Button variant={v}/> must find the
// component's .gsx declaration via CrossIndex[".Button"].
func TestComponentTagDeclAtByo(t *testing.T) {
	// A calling component that uses a byo Button tag. Button is declared
	// elsewhere (not in this file), so CrossIndex is supplied artificially.
	src := "package x\n\ncomponent Page(v string) {\n\t<Button variant={v}/>\n}\n"
	pkg, path := parseOnlyPackage(t, "page.gsx", src)

	// Build a synthetic CrossIndex pointing ".Button" to a fake .gsx location.
	declPos := token.Position{Filename: "button.gsx", Line: 5, Column: 11, Offset: 42}
	pkg.CrossIndex = map[string]CrossRef{
		".Button": {Name: "Button", Decl: declPos},
	}

	// The tag name "Button" starts right after the '<' on line 4.
	// "package x\n\ncomponent Page(v string) {\n\t<" is the prefix;
	// the '<' is at offset = len("package x\n\ncomponent Page(v string) {\n\t"), and
	// "Button" follows immediately after it.
	tagStart := strings.Index(src, "<Button") + 1 // +1 to skip '<'
	if tagStart < 1 {
		t.Fatal("could not find <Button in src")
	}

	// Cursor on 'B' (first char of the tag name).
	decl, ok := componentTagDeclAt(pkg, path, tagStart)
	if !ok {
		t.Fatalf("componentTagDeclAt returned false for cursor on 'B' of Button tag")
	}
	if decl != declPos {
		t.Errorf("componentTagDeclAt decl = %+v, want %+v", decl, declPos)
	}

	// Cursor on 't' (middle of the tag name "But|ton") — must also resolve.
	midCursor := tagStart + 2 // 'B'+'u'+'t' → offset of 't'
	decl2, ok2 := componentTagDeclAt(pkg, path, midCursor)
	if !ok2 {
		t.Fatalf("componentTagDeclAt returned false for cursor in middle of Button tag")
	}
	if decl2 != declPos {
		t.Errorf("componentTagDeclAt (mid) decl = %+v, want %+v", decl2, declPos)
	}

	// Cursor BEFORE the tag name (on the '<') — must NOT resolve (cursor is not
	// on the identifier, it is on the '<' delimiter).
	preCursor := tagStart - 1
	_, notOK := componentTagDeclAt(pkg, path, preCursor)
	if notOK {
		t.Errorf("componentTagDeclAt incorrectly resolved for cursor on '<' before tag name")
	}
}

// TestExprNodeAtOffsetByoBody verifies that exprNodeAtOffset works inside a
// byo component body: the interpolation { p.Variant } (where p is the author
// struct param) is found and its ExprPos is valid. This proves
// exprNodeAtOffset locates the byo-body Interp node and its ExprPos; the
// ExprMap→gopls bridge (ExprMap[interp] → skeleton go/ast expr → gopls) is
// covered by TestByoLSPContract. Note: exprNodeAtOffset does NOT consult
// ExprMap — it only finds the Interp/ExprAttr node and ExprPos in the gsx AST.
func TestExprNodeAtOffsetByoBody(t *testing.T) {
	// A byo component with a { p.Variant } interpolation (no pipe stages).
	src := "package x\n\ncomponent Button(p Props) {\n\t<button>{ p.Variant }</button>\n}\n"
	pkg, path := parseOnlyPackage(t, "button.gsx", src)

	// offset of 'V' in "p.Variant"
	variantOff := strings.Index(src, "p.Variant") + 2 // +2 to land on 'V'
	node, exprPos := exprNodeAtOffset(pkg, path, variantOff)
	if node == nil {
		t.Fatalf("exprNodeAtOffset returned nil for cursor on Variant in byo body")
	}
	if _, ok := node.(*gsxast.Interp); !ok {
		t.Fatalf("node = %T, want *gsxast.Interp (byo body interp)", node)
	}
	if !exprPos.IsValid() {
		t.Fatalf("exprNodeAtOffset returned invalid ExprPos for byo body interp")
	}
}

func TestHasPipeStages(t *testing.T) {
	firstInterp := func(src string) *gsxast.Interp {
		fset := token.NewFileSet()
		f, err := gsxparser.ParseFile(fset, "c.gsx", []byte(src), 0)
		if err != nil {
			t.Fatal(err)
		}
		var in *gsxast.Interp
		gsxast.Inspect(f, func(n gsxast.Node) bool {
			if in != nil {
				return false
			}
			if i, ok := n.(*gsxast.Interp); ok {
				in = i
				return false
			}
			return true
		})
		if in == nil {
			t.Fatalf("no Interp parsed from %q", src)
		}
		return in
	}

	plain := firstInterp("package x\n\ncomponent C(u User) {\n\t<div>{ u.Name }</div>\n}\n")
	if hasPipeStages(plain) {
		t.Fatalf("plain interp reported as piped")
	}
	piped := firstInterp("package x\n\ncomponent C(u User) {\n\t<div>{ u.Name |> upper }</div>\n}\n")
	if !hasPipeStages(piped) {
		t.Fatalf("piped interp not reported as piped")
	}
}
