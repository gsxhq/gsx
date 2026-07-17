package codegen

import (
	"go/token"
	"go/types"
	"path/filepath"
	"strings"
	"testing"

	gsxast "github.com/gsxhq/gsx/ast"
	"github.com/gsxhq/gsx/internal/diag"
	gsxparser "github.com/gsxhq/gsx/parser"
)

// TestElementValueLowersToGsxFunc is Task 4's Step 1 test: a gsx element
// embedded directly in Go-expression position (a top-level *ast.GoWithElements,
// Task 2) must lower to a self-contained gsx.Node VALUE — a
// `gsx.Func(func(ctx context.Context, _gsxw io.Writer) error { … })` spliced
// inline in place of the element's own source text — rather than leaving the
// bare `<div/>` markup syntax (invalid as a Go expression) in the var
// initializer.
func TestElementValueLowersToGsxFunc(t *testing.T) {
	t.Parallel()
	repoRoot, _ := filepath.Abs("../..")
	tmp := t.TempDir()
	writeFile(t, tmp, "go.mod", "module gsxel\n\ngo 1.26.1\n\nrequire github.com/gsxhq/gsx v0.0.0\n\nreplace github.com/gsxhq/gsx => "+repoRoot+"\n")
	pkgDir := filepath.Join(tmp, "genpkg")
	writeFile(t, pkgDir, "views.gsx", "package views\n\nvar x = <div/>\n")

	res, err := GenerateDirs(tmp, []string{pkgDir}, Options{FilterPkgs: []string{stdImportPath}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	var got string
	for _, src := range res[pkgDir].Files {
		got = string(src)
	}
	if got == "" {
		t.Fatal("no generated output")
	}
	if !strings.Contains(got, "var x = _gsxrt.Func(") {
		t.Fatalf("expected `var x = _gsxrt.Func(` in generated source, got:\n%s", got)
	}
	if strings.Contains(got, "var x = <div") {
		t.Fatalf("element literal was not lowered — bare markup survived into the var initializer:\n%s", got)
	}
}

// TestElementValueOnlyFileGetsRuntimeImports confirms that a .gsx file whose
// ONLY gsx construct is a top-level embedded element (no `component` at all)
// still gets the context/io/gsx runtime imports the emitted gsx.Func closure
// needs — import injection must not be gated on the presence of a Component.
func TestElementValueOnlyFileGetsRuntimeImports(t *testing.T) {
	t.Parallel()
	repoRoot, _ := filepath.Abs("../..")
	tmp := t.TempDir()
	writeFile(t, tmp, "go.mod", "module gsxel2\n\ngo 1.26.1\n\nrequire github.com/gsxhq/gsx v0.0.0\n\nreplace github.com/gsxhq/gsx => "+repoRoot+"\n")
	pkgDir := filepath.Join(tmp, "genpkg")
	writeFile(t, pkgDir, "views.gsx", "package views\n\nvar x = <div/>\n")

	res, err := GenerateDirs(tmp, []string{pkgDir}, Options{FilterPkgs: []string{stdImportPath}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	var got string
	for _, src := range res[pkgDir].Files {
		got = string(src)
	}
	for _, want := range []string{`"context"`, `"io"`, `"github.com/gsxhq/gsx"`} {
		if !strings.Contains(got, want) {
			t.Fatalf("expected import %s in element-only generated source, got:\n%s", want, got)
		}
	}
}

// TestElementValueInterpCapturesOuterScope checks that an interpolation
// inside an embedded element's markup (`{ label }`) is emitted VERBATIM —
// exactly as a component body's interpolation is — so it resolves against
// whatever the ENCLOSING Go scope binds via ordinary closure capture, not
// against any component-scope machinery (there is none: no _gsxp, no attrs,
// no children — this element has no surrounding component).
//
// This exercises generateFile directly with a hand-built `resolved` map
// rather than going through the full Open/Package pipeline: type-checking
// interpolations embedded in a top-level Go-expression-position element is
// analyze.go's buildSkeleton job (Task 5, not yet implemented — buildSkeleton
// does not yet walk *ast.GoWithElements), so the full pipeline cannot yet
// resolve this interpolation's type. Task 4's own scope is the emitter: given
// a resolved type, does it emit the right thing.
func TestElementValueInterpCapturesOuterScope(t *testing.T) {
	t.Parallel()
	fset := token.NewFileSet()
	src := "package views\n\nvar label = \"Home\"\n\nvar help = <a href=\"/\">{ label }</a>\n"
	file, err := gsxparser.ParseFile(fset, "views.gsx", []byte(src), 0)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}

	var we *gsxast.GoWithElements
	for _, d := range file.Decls {
		if w, ok := d.(*gsxast.GoWithElements); ok {
			we = w
		}
	}
	if we == nil {
		t.Fatalf("no GoWithElements decl in parsed file: %#v", file.Decls)
	}
	var interp *gsxast.Interp
	gsxast.Inspect(we, func(n gsxast.Node) bool {
		if it, ok := n.(*gsxast.Interp); ok {
			interp = it
		}
		return true
	})
	if interp == nil {
		t.Fatal("no *ast.Interp found inside the embedded element")
	}

	// Hand-build the resolved map genInterp needs (normally analyze.go's job —
	// out of scope for Task 4, see doc comment above): `label` resolves to string.
	resolved := map[gsxast.Node]types.Type{
		interp: types.Typ[types.String],
	}
	bag := diag.NewBag(fset)
	out, ok := generateFile(file, nil, resolved, funcTables{}, fset, nil, bag, nil, nil, nil, false, false, nil, componentPositionalPackagePlan{})
	if !ok {
		t.Fatalf("generateFile failed: %v", bag.Sorted())
	}
	got := string(out)
	if !strings.Contains(got, "var help = _gsxrt.Func(") {
		t.Fatalf("expected `var help = _gsxrt.Func(` in generated source, got:\n%s", got)
	}
	// The interpolation must be emitted verbatim (`label`, the outer-scope Go
	// var), captured by ordinary closure capture — not rebound through any
	// component-scope machinery.
	if !strings.Contains(got, "label") {
		t.Fatalf("expected the outer-scope identifier `label` referenced verbatim in the closure, got:\n%s", got)
	}
	for _, absent := range []string{"_gsxp", "_gsxp.", "attrs :=", "children :="} {
		if strings.Contains(got, absent) {
			t.Fatalf("element-value closure must not reference component-scope machinery (%q found), got:\n%s", absent, got)
		}
	}
}
