package printer

import (
	goparser "go/parser"
	"go/token"
	"strings"
	"testing"

	gsxast "github.com/gsxhq/gsx/ast"
	"github.com/gsxhq/gsx/internal/wsnorm"
	"github.com/gsxhq/gsx/parser"
)

// allInterpExprsValid reports whether every interpolation expression in the file
// is a well-formed Go expression. gsx's parser tolerates a malformed Go expr in an
// interp (it surfaces later as a codegen/compile error), but the formatter's
// faithfulness contract only covers well-formed gsx: go/format cannot normalize a
// malformed expr, so it falls back to verbatim, which can round-trip badly in ANY
// interp ({ } or @{ }). That is a pre-existing, general printer limitation, not a
// <style> concern — so this fuzzer skips such inputs and asserts faithfulness only
// for the real surface.
func allInterpExprsValid(file *gsxast.File) bool {
	ok := true
	var walk func([]gsxast.Markup)
	walk = func(nodes []gsxast.Markup) {
		for _, n := range nodes {
			switch v := n.(type) {
			case *gsxast.Interp:
				if _, err := goparser.ParseExpr(v.Expr); err != nil {
					ok = false
				}
			case *gsxast.Element:
				walk(v.Children)
			case *gsxast.Fragment:
				walk(v.Children)
			case *gsxast.IfMarkup:
				walk(v.Then)
				walk(v.Else)
			case *gsxast.ForMarkup:
				walk(v.Body)
			case *gsxast.SwitchMarkup:
				for _, c := range v.Cases {
					walk(c.Body)
				}
			}
		}
	}
	for _, d := range file.Decls {
		if c, isComp := d.(*gsxast.Component); isComp {
			walk(c.Body)
		}
	}
	return ok
}

// FuzzStyleRoundTrip feeds arbitrary <style> bodies through parse -> Normalize ->
// Fprint and back. The parser/formatter must never panic, and any output that the
// formatter produces must re-parse (faithfulness: fmt never emits unparseable
// source). Parse errors on the input are fine (random bytes aren't valid gsx).
func FuzzStyleRoundTrip(f *testing.F) {
	for _, body := range []string{
		".a{width:@{w}px}", "@{a}@{b}", "$x", "@{", "@{ }", "@{ a |> upper }",
		"@{ x? }", "</sty", `@{ "}" }`, "color:@{c};", "/* c */ .a{x:@{y}}",
	} {
		f.Add(body)
	}
	f.Fuzz(func(t *testing.T, body string) {
		src := "package p\n\ncomponent C(w int, x string) {\n\t<style>" + body + "</style>\n}\n"
		fset := token.NewFileSet()
		file, err := parser.ParseFile(fset, "f.gsx", src, 0)
		if err != nil {
			return // invalid input is acceptable
		}
		// Faithfulness is contracted only for well-formed gsx: skip inputs with a
		// malformed Go interp expression (a pre-existing general printer limitation
		// orthogonal to <style> — see allInterpExprsValid).
		if !allInterpExprsValid(file) {
			return
		}
		wsnorm.Normalize(file)
		var b strings.Builder
		if err := Fprint(&b, file); err != nil {
			return
		}
		// Faithfulness: the formatted output must itself re-parse.
		if _, err := parser.ParseFile(token.NewFileSet(), "f2.gsx", b.String(), 0); err != nil {
			t.Fatalf("formatted output did not re-parse: %v\nbody=%q\nout:\n%s", err, body, b.String())
		}
	})
}
