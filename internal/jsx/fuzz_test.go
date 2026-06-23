package jsx

import (
	"testing"

	"github.com/gsxhq/gsx/ast"
)

// FuzzResolveScriptsFailClosed throws arbitrary JS text around a single @{ } hole
// in a <script> body and asserts the classifier (1) never panics and (2) never
// leaves the hole silently unescaped: a nil error MUST mean every surviving hole
// resolved to a real JS context (not JSCtxNone). A fail-closed error is fine.
func FuzzResolveScriptsFailClosed(f *testing.F) {
	for _, s := range []string{
		"", "var x=", "var x=\"", "var x=`", "var x=/", "let ", "//", "/*",
		"const y={a:", "return ", "x=1;y=", "function f(){", "`${", "</script>",
	} {
		f.Add(s, "")
		f.Add("", s)
		f.Add(s, s)
	}
	f.Fuzz(func(t *testing.T, prefix, suffix string) {
		in := &ast.Interp{Expr: "x"}
		script := &ast.Element{Tag: "script", Children: []ast.Markup{
			&ast.Text{Value: prefix}, in, &ast.Text{Value: suffix},
		}}
		file := &ast.File{Decls: []ast.Decl{&ast.Component{Name: "C", Body: []ast.Markup{script}}}}

		err := ResolveScriptsErr(file) // must not panic
		if err != nil {
			return // fail-closed is an acceptable outcome
		}
		// nil error: every surviving hole must carry a real escaping context.
		for _, c := range script.Children {
			if ip, ok := c.(*ast.Interp); ok && ip.JSCtx == ast.JSCtxNone {
				t.Fatalf("hole left JSCtxNone (silently unescaped) for prefix=%q suffix=%q", prefix, suffix)
			}
		}
	})
}
