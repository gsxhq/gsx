package lsp

import (
	"go/token"
	"go/types"
	"os"
	"path/filepath"
	"strings"
	"testing"

	gsxast "github.com/gsxhq/gsx/ast"
	"github.com/gsxhq/gsx/internal/codegen"
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

func writeLSPTestFile(t *testing.T, dir, name, content string) {
	t.Helper()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func analyzedLSPPackage(t *testing.T, src string) (*Package, string) {
	t.Helper()
	root := t.TempDir()
	repoRoot, err := filepath.Abs("../..")
	if err != nil {
		t.Fatal(err)
	}
	writeLSPTestFile(t, root, "go.mod", "module example.com/app\n\ngo 1.26.1\n\nrequire github.com/gsxhq/gsx v0.0.0\n\nreplace github.com/gsxhq/gsx => "+repoRoot+"\n")
	storeDir := filepath.Join(root, "store")
	writeLSPTestFile(t, storeDir, "store.go", "package store\n\ntype ID string\n")
	pageDir := filepath.Join(root, "page")
	gsxPath := filepath.Join(pageDir, "page.gsx")
	writeLSPTestFile(t, pageDir, "page.gsx", src)

	m, err := codegen.Open(codegen.Options{ModuleRoot: root, ModulePath: "example.com/app", FilterPkgs: []string{codegen.StdImportPath}})
	if err != nil {
		t.Fatal(err)
	}
	pr, err := m.Package(pageDir)
	if err != nil {
		t.Fatal(err)
	}
	if len(pr.Diags) > 0 {
		t.Fatalf("unexpected diagnostics: %+v", pr.Diags)
	}
	sigTypes := map[*gsxast.Component][]SigTypeRef{}
	for c, refs := range pr.SigTypes {
		for _, r := range refs {
			sigTypes[c] = append(sigTypes[c], SigTypeRef{
				GSXPos:  r.GSXPos,
				Len:     r.Len,
				SkelTyp: r.SkelTyp,
			})
		}
	}
	return &Package{
		GSXFset:  pr.GSXFset,
		Fset:     pr.Fset,
		Info:     pr.Info,
		Types:    pr.Types,
		Files:    pr.GSXFiles,
		SigTypes: sigTypes,
	}, gsxPath
}

func TestSignatureTypeIdentAtTypeParams(t *testing.T) {
	src := "package page\n\nimport \"example.com/app/store\"\n\ncomponent Box[T store.ID](value T) {\n\t<span>{value}</span>\n}\n"
	pkg, path := analyzedLSPPackage(t, src)

	idOff := strings.Index(src, "store.ID") + len("store.")
	obj, gsxStart, idLen, ok := signatureTypeIdentAt(pkg, path, idOff)
	if !ok {
		t.Fatal("signatureTypeIdentAt returned false for type-param constraint type")
	}
	if _, ok := obj.(*types.TypeName); !ok || obj.Name() != "ID" {
		t.Fatalf("obj = %T %q, want *types.TypeName ID", obj, obj.Name())
	}
	if got := src[gsxStart : gsxStart+idLen]; got != "ID" {
		t.Fatalf("range = %q, want ID", got)
	}

	pkgOff := strings.Index(src, "store.ID")
	obj, gsxStart, idLen, ok = signatureTypeIdentAt(pkg, path, pkgOff)
	if !ok {
		t.Fatal("signatureTypeIdentAt returned false for type-param constraint package qualifier")
	}
	if _, ok := obj.(*types.PkgName); !ok || obj.Name() != "store" {
		t.Fatalf("obj = %T %q, want *types.PkgName store", obj, obj.Name())
	}
	if got := src[gsxStart : gsxStart+idLen]; got != "store" {
		t.Fatalf("range = %q, want store", got)
	}

	tOff := strings.Index(src, "[T store.ID]") + 1
	obj, gsxStart, idLen, ok = signatureTypeIdentAt(pkg, path, tOff)
	if !ok {
		t.Fatal("signatureTypeIdentAt returned false for type-param declaration name")
	}
	if _, ok := obj.(*types.TypeName); !ok || obj.Name() != "T" {
		t.Fatalf("obj = %T %q, want *types.TypeName T", obj, obj.Name())
	}
	if got := src[gsxStart : gsxStart+idLen]; got != "T" {
		t.Fatalf("range = %q, want T", got)
	}

	outside := strings.Index(src, "component Box")
	if _, _, _, ok := signatureTypeIdentAt(pkg, path, outside); ok {
		t.Fatal("signatureTypeIdentAt unexpectedly resolved outside type-param list")
	}
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

// TestComponentTagDeclAtClosingTag verifies go-to-definition works from the
// CLOSING tag too: a cursor on "Card" in "</Card>" resolves to the component
// declaration just like the opening tag (relies on ast.Element.CloseNamePos).
func TestComponentTagDeclAtClosingTag(t *testing.T) {
	// Card has children, so the element has an explicit closing tag </Card>.
	src := "package x\n\ncomponent Page() {\n\t<Card title=\"hi\">body</Card>\n}\n"
	pkg, path := parseOnlyPackage(t, "page.gsx", src)

	declPos := token.Position{Filename: "card.gsx", Line: 3, Column: 11, Offset: 24}
	pkg.CrossIndex = map[string]CrossRef{
		".Card": {Name: "Card", Decl: declPos},
	}

	// Offset of "Card" inside "</Card>" (the closing tag, the last occurrence).
	closeStart := strings.Index(src, "</Card>") + 2 // +2 to skip '</'
	if closeStart < 2 {
		t.Fatal("could not find </Card> in src")
	}

	// Cursor on 'C' of the closing tag name.
	decl, ok := componentTagDeclAt(pkg, path, closeStart)
	if !ok {
		t.Fatalf("componentTagDeclAt returned false for cursor on closing tag </Card>")
	}
	if decl != declPos {
		t.Errorf("closing-tag decl = %+v, want %+v", decl, declPos)
	}

	// Cursor in the middle of the closing tag name ("Ca|rd") — must also resolve.
	if decl2, ok2 := componentTagDeclAt(pkg, path, closeStart+2); !ok2 || decl2 != declPos {
		t.Errorf("closing-tag (mid) ok=%v decl=%+v, want true %+v", ok2, decl2, declPos)
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

// TestExprNodeAtOffsetControlFlow verifies that exprNodeAtOffset recognizes a
// ForMarkup node when the cursor sits on an identifier inside the for-clause.
func TestExprNodeAtOffsetControlFlow(t *testing.T) {
	src := "package x\n\ncomponent P(props Props) {\n\t{ for _, post := range props.Posts { <li>x</li> } }\n}\n"
	pkg, path := parseOnlyPackage(t, "p.gsx", src)
	// offset of "Posts" inside the for-clause
	off := strings.Index(src, "props.Posts") + len("props.")
	node, _ := exprNodeAtOffset(pkg, path, off)
	if _, ok := node.(*gsxast.ForMarkup); !ok {
		t.Fatalf("exprNodeAtOffset on a for-clause = %T, want *ForMarkup", node)
	}
}

// TestComponentTagDeclAtClosingTagWhitespace verifies go-to-definition still
// resolves on a closing tag with whitespace before '>' (</Card >), since the
// parser allows it and records CloseNamePos at the name regardless.
func TestComponentTagDeclAtClosingTagWhitespace(t *testing.T) {
	src := "package x\n\ncomponent Page() {\n\t<Card>body</Card >\n}\n"
	pkg, path := parseOnlyPackage(t, "page.gsx", src)
	declPos := token.Position{Filename: "card.gsx", Line: 3, Column: 11, Offset: 24}
	pkg.CrossIndex = map[string]CrossRef{".Card": {Name: "Card", Decl: declPos}}

	closeStart := strings.Index(src, "</Card >") + 2 // skip '</'
	if closeStart < 2 {
		t.Fatal("could not find </Card > in src")
	}
	decl, ok := componentTagDeclAt(pkg, path, closeStart+1) // cursor on 'a' of Card
	if !ok || decl != declPos {
		t.Errorf("whitespace closing tag: ok=%v decl=%+v, want true %+v", ok, decl, declPos)
	}
}
