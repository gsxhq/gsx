package printer

import (
	"go/token"
	"testing"

	"github.com/gsxhq/gsx/ast"
	"github.com/gsxhq/gsx/internal/wsnorm"
	"github.com/gsxhq/gsx/parser"
)

// fmtGoExprParts degrades gracefully: when go/format rejects the
// placeholder-substituted source it returns ok=false and the caller relays the
// whole GoWithElements region VERBATIM. That fallback is silent, and a verbatim
// relay is perfectly idempotent and perfectly render-faithful — so neither
// TestCorpusInputsProperty nor TestCorpusIdempotence can see it. The only way
// to catch a region that silently stopped being gofmt'd is to assert the
// non-fallback path was actually taken.
//
// The regression that motivated this test only fires on the SECOND format: it
// takes fmt's own decorative-paren output (`icon: (\n\t<Icon/>\n)`) to put a
// placeholder alone on a line inside a bracket, where Go's automatic semicolon
// insertion makes the substituted source unparseable. So each input is checked
// both as authored and as formatted.
// invalidGoFixtures are the corpus inputs whose embedded Go is deliberately not
// valid Go, independent of gsx. go/format is right to reject them, so a verbatim
// relay is the correct outcome and the fallback is doing its job. Every entry is
// asserted to actually fall back, so this list cannot rot into a silent
// suppression of a real regression.
var invalidGoFixtures = map[string]string{
	"element-literals/stray-import-after-func.txtar:input.gsx": `an "import" decl after a func decl is not valid Go`,
	"imports/reserved_prefix_decl_element_gostmt.txtar:input.gsx": "`go <b/>` substitutes to `go IDENT`, " +
		"and a go statement requires a call expression",
}

func TestCorpusGoExprRegionsAlwaysReachGofmt(t *testing.T) {
	checked := 0
	fellBack := map[string]bool{}
	for _, c := range corpusGsxSources(t) {
		fset := token.NewFileSet()
		if _, err := parser.ParseFile(fset, "x.gsx", c.src, 0); err != nil {
			continue // parser-error fixture: out of scope
		}
		formatted, err := normPrint(t, c.src)
		if err != nil {
			t.Errorf("%s: fmt failed: %v", c.name, err)
			continue
		}
		for _, pass := range []struct {
			label string
			src   string
		}{{"as authored", c.src}, {"as formatted", formatted}} {
			for _, parts := range goWithElementsParts(t, pass.src) {
				checked++
				p := printer{}
				if _, _, ok := p.fmtGoExprParts(parts); ok {
					continue
				}
				fellBack[c.name] = true
				if _, expected := invalidGoFixtures[c.name]; !expected {
					t.Errorf("%s (%s): GoWithElements region fell back to a verbatim relay — go/format rejected the substituted source, so this region is no longer gofmt'd", c.name, pass.label)
				}
			}
		}
	}
	if checked == 0 {
		t.Fatal("no GoWithElements regions found in the corpus — this test is asserting nothing")
	}
	// A pinned exception that no longer falls back means the fixture changed or
	// the fallback path moved: drop it from the list rather than let it hide a
	// future regression.
	for name, why := range invalidGoFixtures {
		if !fellBack[name] {
			t.Errorf("%s is pinned as invalid Go (%s) but no longer falls back — remove it from invalidGoFixtures", name, why)
		}
	}
	t.Logf("checked %d GoWithElements regions", checked)
}

// goWithElementsParts parses src and returns the Parts of every GoWithElements
// decl in it.
func goWithElementsParts(t *testing.T, src string) [][]ast.GoPart {
	t.Helper()
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, "x.gsx", src, 0)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	wsnorm.Normalize(f)
	var out [][]ast.GoPart
	for _, d := range f.Decls {
		if g, ok := d.(*ast.GoWithElements); ok {
			out = append(out, g.Parts)
		}
	}
	return out
}
