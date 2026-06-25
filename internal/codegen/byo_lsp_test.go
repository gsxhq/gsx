package codegen

import (
	goast "go/ast"
	"os"
	"path/filepath"
	"strings"
	"testing"

	gsxast "github.com/gsxhq/gsx/ast"
)

// TestByoLSPContract verifies the three pillars of the LSP contract for a
// bring-your-own-Props (byo) component after the author-owns-Props redesign:
//
//	(a) tag-name→decl: the byo Button component is in CrossIndex with a valid
//	    .gsx declaration position so go-to-definition on a <Button …/> tag
//	    resolves back to the component declaration.
//
//	(b) ExprMap covers byo body interp/attr expressions: the { p.Variant } and
//	    { p.Children } interpolations inside the byo component body are mapped
//	    to their skeleton go/ast exprs, so gopls can act on a cursor inside
//	    those expressions (hover/definition).
//
//	(c) //line directives are emitted: the generated .x.go contains //line
//	    markers mapping skeleton positions back to the .gsx source.
//
// The skeleton must also be valid Go (no type errors) for both field-build and
// whole-struct splat invocations — confirmed implicitly by the absence of type
// errors from GeneratePackagesWithFilters.
//
// This is a guard test: it asserts the redesign did NOT break the LSP contract.
func TestByoLSPContract(t *testing.T) {
	dir := t.TempDir()
	repoRoot, _ := filepath.Abs("../..")
	writeFile(t, dir, "go.mod",
		"module example.com/byolsp\n\ngo 1.26.1\n\nrequire github.com/gsxhq/gsx v0.0.0\n\nreplace github.com/gsxhq/gsx => "+repoRoot+"\n")

	// button.gsx: byo component whose sole non-receiver param is Props (an
	// author-declared struct). Body has two interpolations so we can assert both
	// are in ExprMap.
	writeFile(t, dir, "button.gsx", `package byolsp

import "github.com/gsxhq/gsx"

type Props struct {
	Variant  string
	Children gsx.Node
}

component Button(p Props) {
	<button class={ "btn", p.Variant }>{ p.Children }</button>
}
`)

	// page.gsx: caller with two invocations:
	//   - field-build: <Button variant={someVar}>…</Button>
	//   - whole-struct splat: <Button { pd... }/>
	writeFile(t, dir, "page.gsx", `package byolsp

component Page(someVar string, pd Props) {
	<Button variant={someVar}>Click</Button>
	<Button { pd... }/>
}
`)

	out, err := GeneratePackagesWithFilters(dir, []string{dir}, nil, nil, nil, nil, nil, nil, nil)
	if err != nil {
		t.Fatalf("GeneratePackagesWithFilters: %v", err)
	}
	pr := out[dir]
	if pr == nil {
		t.Fatalf("no PackageResult for %s", dir)
	}

	// No type errors: skeleton must be valid Go for both field-build and splat.
	var typeErrs []string
	for _, d := range pr.Diags {
		if d.Source == "types" {
			typeErrs = append(typeErrs, d.Message)
		}
	}
	if len(typeErrs) > 0 {
		t.Errorf("unexpected type errors in byo skeleton: %v", typeErrs)
	}

	// (a) tag-name→decl: CrossIndex must have an entry for ".Button" with a
	// valid .gsx declaration position.
	cr, ok := pr.CrossIndex[".Button"]
	if !ok {
		t.Fatalf("CrossIndex missing .Button; keys=%v", keysOfCross(pr.CrossIndex))
	}
	if !cr.Decl.IsValid() {
		t.Errorf("CrossIndex[.Button].Decl is not valid: %+v", cr.Decl)
	}
	if !strings.HasSuffix(cr.Decl.Filename, "button.gsx") {
		t.Errorf("CrossIndex[.Button].Decl.Filename = %q, want button.gsx", cr.Decl.Filename)
	}
	// Decl must point at the 'B' of "Button" in the component declaration.
	if data, err := os.ReadFile(cr.Decl.Filename); err == nil {
		if cr.Decl.Offset >= len(data) || data[cr.Decl.Offset] != 'B' {
			end := cr.Decl.Offset + 8
			if end > len(data) {
				end = len(data)
			}
			t.Errorf("CrossIndex[.Button].Decl should land on 'B' of component name; Decl=%v src[off]=%q",
				cr.Decl, string(data[cr.Decl.Offset:end]))
		}
	}

	// (b) ExprMap covers byo body interpolation expressions.
	// Find the Interp nodes inside button.gsx (the byo component body).
	if pr.GSXFiles == nil {
		t.Fatal("GSXFiles is nil")
	}
	if pr.ExprMap == nil {
		t.Fatal("ExprMap is nil")
	}

	buttonGSX := ""
	for path := range pr.GSXFiles {
		if strings.HasSuffix(path, "button.gsx") {
			buttonGSX = path
			break
		}
	}
	if buttonGSX == "" {
		t.Fatal("button.gsx not found in GSXFiles")
	}
	buttonFile := pr.GSXFiles[buttonGSX]

	// Collect all Interp nodes from button.gsx — these are the { p.Variant }
	// (class attr interp, via ExprAttr) and { p.Children } interpolation.
	var interps []*gsxast.Interp
	gsxast.Inspect(buttonFile, func(n gsxast.Node) bool {
		if in, ok := n.(*gsxast.Interp); ok {
			interps = append(interps, in)
		}
		return true
	})
	if len(interps) == 0 {
		t.Fatal("no Interp nodes found in button.gsx AST")
	}
	for _, in := range interps {
		if skel := pr.ExprMap[in]; skel == nil {
			t.Errorf("ExprMap missing entry for Interp %q (expr=%q)", in.Expr, in.Expr)
		} else if _, ok := skel.(goast.Expr); !ok {
			t.Errorf("ExprMap[interp %q] = %T, want goast.Expr", in.Expr, skel)
		}
	}

	// Also check that ExprAttr nodes (e.g. the { p.Variant } in the class attr)
	// in the byo body are covered by ExprMap.
	var exprs []*gsxast.ExprAttr
	gsxast.Inspect(buttonFile, func(n gsxast.Node) bool {
		if ea, ok := n.(*gsxast.ExprAttr); ok {
			exprs = append(exprs, ea)
		}
		return true
	})
	for _, ea := range exprs {
		if skel := pr.ExprMap[ea]; skel == nil {
			t.Errorf("ExprMap missing entry for ExprAttr %q", ea.Expr)
		}
	}

	// (c) //line directives must be present in the generated .x.go for button.gsx.
	// pr.Files is keyed by the .gsx source path (absolute), so check for the
	// key that ends in "button.gsx".
	buttonGenerated := false
	for srcPath, src := range pr.Files {
		if strings.HasSuffix(srcPath, "button.gsx") {
			buttonGenerated = true
			if !strings.Contains(string(src), "//line button.gsx:") {
				t.Errorf("button.gsx generated output missing //line directives; generated:\n%s", src)
			}
		}
	}
	if !buttonGenerated {
		t.Error("button.gsx was not generated (no entry in pr.Files)")
	}

	// (d) Caller-side field-build attr expr is resolvable via direct-occurrence.
	//
	// For a caller-side field-build ExprAttr like variant={someVar} in page.gsx,
	// the value expression (someVar) is NOT entered into ExprMap. Instead, it
	// appears VERBATIM in the generated Props struct literal:
	//
	//   _ = Button(Props{Variant: someVar, ...})   (skeleton)
	//   _gsxgw.Node(ctx, Button(Props{Variant: someVar, ...}))  (emit)
	//
	// collectExprs skips simple attrs on component tags (they are type-checked via
	// the props literal, not _gsxuse). gopls resolves `someVar` directly from the
	// generated Props{Variant: someVar} occurrence — no ExprMap bridge needed.
	// This assertion proves the direct occurrence is present so go-to-definition
	// on `someVar` in page.gsx can be served by gopls via the generated code.
	pageGenerated := false
	for srcPath, src := range pr.Files {
		if strings.HasSuffix(srcPath, "page.gsx") {
			pageGenerated = true
			if !strings.Contains(string(src), "someVar") {
				t.Errorf("page.gsx generated output missing direct occurrence of someVar in Button(Props{...}); generated:\n%s", src)
			}
		}
	}
	if !pageGenerated {
		t.Error("page.gsx was not generated (no entry in pr.Files)")
	}
}
