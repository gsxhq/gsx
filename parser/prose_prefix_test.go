package parser

import (
	"go/token"
	"testing"

	"github.com/gsxhq/gsx/ast"
)

func TestProsePrefixAfterSibling(t *testing.T) {
	for _, prefix := range []string{"\n\t", " ", "\r\n  "} {
		for _, context := range []string{"component", "slot"} {
			t.Run(context+prefix, func(t *testing.T) {
				body := "<b>x</b>" + prefix + "word"
				src := "package p\ncomponent C(){\n" + body + "\n}\n"
				if context == "slot" {
					src = "package p\ncomponent C(){ <Card content={" + body + "}/> }\n"
				}
				fs := token.NewFileSet()
				f, err := ParseFile(fs, "test.gsx", src, 0)
				if err != nil {
					t.Fatal(err)
				}
				var text *ast.Text
				ast.Inspect(f, func(n ast.Node) bool {
					if v, ok := n.(*ast.Text); ok && len(v.Value) >= len(prefix)+4 && v.Value[:len(prefix)+4] == prefix+"word" {
						text = v
					}
					return true
				})
				if text == nil {
					t.Fatal("substantive text lost its source prefix")
				}
				if text.LeadingBreak != leadingRunBreaks(prefix) {
					t.Fatalf("LeadingBreak = %v for prefix %q", text.LeadingBreak, prefix)
				}
				file := fs.File(text.Pos())
				start, end := file.Offset(text.Pos()), file.Offset(text.End())
				if got := src[start:end]; got != text.Value {
					t.Fatalf("span text %q != value %q", got, text.Value)
				}
			})
		}
	}
}
