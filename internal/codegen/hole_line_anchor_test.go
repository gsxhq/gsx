package codegen

import (
	"maps"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"

	goast "go/ast"
	goparser "go/parser"
	"go/token"
)

// holeAnchorProbes declares one probe func per hole position; each is called
// exactly once in holeAnchorSrc, so its authored line is unambiguous.
const holeAnchorProbes = `package views

import (
	"context"

	"github.com/gsxhq/gsx"
)

func pAttr() string             { return "" }
func pPipe() string             { return "" }
func pStageArg() string         { return "" }
func pTuple() (string, error)   { return "", nil }
func pClassA() string           { return "" }
func pClassB() string           { return "" }
func pClassCond() bool          { return true }
func pStyle() string            { return "" }
func pFLitA() string            { return "" }
func pFLitB() string            { return "" }
func pJSA() string              { return "" }
func pJSB() string              { return "" }
func pCSS() string              { return "" }
func pSpread() gsx.Attrs        { return nil }
func pCond() bool               { return true }
func pCondThen() string         { return "" }
func pCondElse() string         { return "" }
func pSwitch() int              { return 1 }
func pCase() string             { return "" }
func pDefault() string          { return "" }
func pPropA() string            { return "" }
func pPropB() int               { return 1 }
func pPropCond() bool           { return true }
func pPropCondVal() string      { return "" }
func pFoldA() gsx.Attrs         { return nil }
func pFoldB() gsx.Attrs         { return nil }
func pFoldAttr() string         { return "" }
func pManualSpread() gsx.Attrs  { return nil }
func pManualAttr() string       { return "" }
func pChild() string            { return "" }
func pChildPipe() string        { return "" }
func pChildLitA() string        { return "" }
func pChildLitB() string        { return "" }
func pScript() string           { return "" }
func pStyleChild() string       { return "" }
func pCtrl() bool               { return true }

var _ context.Context
`

// holeAnchorFilter is a ctx-taking filter with variadic stage args, the shape
// of structpages.URLFor that structpages-lint inspects in the .x.go.
const holeAnchorFilter = `package sp

import "context"

func URLFor(ctx context.Context, page any, args ...any) (string, error) { return "", nil }
`

const holeAnchorSrc = `package views

import "github.com/gsxhq/gsx"

component Child(label string, n int, attrs gsx.Attrs) {
	<span {attrs...}>{label}{n}</span>
}

component Page() {
	<div
		id={pAttr()}
		hx-get={pPipe() |> url("id", pStageArg())}
		title={pTuple()}
		class={
			"x",
			pClassA(),
			pClassB(): pClassCond(),
		}
		style={"color:" + pStyle()}
		data-f=f` + "`" + `a @{pFLitA()}
		  b @{pFLitB()}` + "`" + `
		onclick=js` + "`" + `
			go(@{pJSA()});
			stop(@{pJSB()});
		` + "`" + `
		data-css=css` + "`" + `
			color: @{pCSS()};
		` + "`" + `
		{ if pCond() {
			data-t={pCondThen()}
		} else {
			data-e={pCondElse()}
		} }
		{ switch pSwitch() {
		case 1:
			data-c={pCase()}
		default:
			data-d={pDefault()}
		} }
	>
		<Child
			label={pPropA()}
			n={pPropB()}
			{ if pPropCond() { data-p={pPropCondVal()} } }
		/>
		<p {pFoldA()...} title={pFoldAttr()} {pFoldB()...}></p>
		<p
			{pManualSpread()...}
			data-m={pManualAttr()}
		></p>
		<p
			{pSpread()...}
		></p>
		<p>{pChild()}</p>
		<p>{pChildPipe() |> upper}</p>
		<p>{f` + "`" + `a @{pChildLitA()}
		  b @{pChildLitB()}` + "`" + ` |> upper}</p>
		<script>
			let v = @{pScript()};
		</script>
		<style>
			p { color: @{pStyleChild()}; }
		</style>
		{ if pCtrl() {
			<b>x</b>
		} }
	</div>
}
`

// TestShippedHoleLineAnchors pins that every authored Go token in the shipped
// .x.go maps back through its //line and /*line*/ directives to the .gsx line
// it was written on — the positions go build, go vet, linters (structpages-lint)
// and panic traces report. Columns are not pinned: format.Source respaces
// authored expressions, so they are approximate by construction.
func TestShippedHoleLineAnchors(t *testing.T) {
	t.Parallel()
	repoRoot, _ := filepath.Abs("../..")
	tmp := t.TempDir()
	writeFile(t, tmp, "go.mod", "module gsxh\n\ngo 1.26.1\n\nrequire github.com/gsxhq/gsx v0.0.0\n\nreplace github.com/gsxhq/gsx => "+repoRoot+"\n")
	pkgDir := filepath.Join(tmp, "views")
	spDir := filepath.Join(tmp, "sp")
	for _, d := range []string{pkgDir, spDir} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	writeFile(t, spDir, "sp.go", holeAnchorFilter)
	writeFile(t, pkgDir, "probes.go", holeAnchorProbes)
	writeFile(t, pkgDir, "page.gsx", holeAnchorSrc)

	opts := Options{
		FilterPkgs: []string{stdImportPath},
		LoadPkgs:   []string{"gsxh/sp"},
		Aliases:    []FilterAlias{{Name: "url", PkgPath: "gsxh/sp", FuncName: "URLFor"}},
	}
	res, err := GenerateDirs(tmp, []string{pkgDir}, opts, nil)
	if err != nil {
		t.Fatalf("generate: %v", err)
	}
	dr := res[pkgDir]
	if hasDiagErrors(dr.Diags) {
		t.Fatalf("generate: unexpected errors: %v", dr.Diags)
	}
	var gen string
	for name, b := range dr.Files {
		if strings.HasSuffix(name, "page.gsx") {
			gen = string(b)
		}
	}
	if gen == "" {
		t.Fatalf("page.x.go not generated; files: %v", slices.Collect(maps.Keys(dr.Files)))
	}

	probe := regexp.MustCompile(`\bp[A-Z]\w*\b`)
	want := map[string]int{}
	for i, line := range strings.Split(holeAnchorSrc, "\n") {
		for _, name := range probe.FindAllString(line, -1) {
			if _, dup := want[name]; dup {
				t.Fatalf("probe %s used twice in holeAnchorSrc; each must be unique", name)
			}
			want[name] = i + 1
		}
	}

	fset := token.NewFileSet()
	f, err := goparser.ParseFile(fset, "page.x.go", gen, goparser.ParseComments)
	if err != nil {
		t.Fatalf("parse generated output: %v\n%s", err, gen)
	}
	seen := map[string]bool{}
	goast.Inspect(f, func(n goast.Node) bool {
		switch n := n.(type) {
		case *goast.Ident:
			line, ok := want[n.Name]
			if !ok {
				return true
			}
			seen[n.Name] = true
			if p := fset.Position(n.Pos()); p.Filename != "page.gsx" || p.Line != line {
				t.Errorf("%s maps to %s:%d, want page.gsx:%d (physical .x.go line %d)", n.Name, p.Filename, p.Line, line, fset.PositionFor(n.Pos(), false).Line)
			}
		case *goast.CallExpr:
			// The synthesized filter call lands on its hole's line too.
			sel, ok := n.Fun.(*goast.SelectorExpr)
			if !ok || sel.Sel.Name != "URLFor" {
				return true
			}
			if p := fset.Position(sel.Sel.Pos()); p.Line != want["pPipe"] {
				t.Errorf("synthesized URLFor call maps to %s:%d, want page.gsx:%d", p.Filename, p.Line, want["pPipe"])
			}
		}
		return true
	})
	for name := range want {
		if !seen[name] {
			t.Errorf("probe %s not found in generated output", name)
		}
	}
	if t.Failed() {
		t.Logf("generated:\n%s", gen)
	}
}
