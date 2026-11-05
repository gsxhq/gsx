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

func TestBraceBodiesCaptureInteriorWhitespace(t *testing.T) {
	for _, context := range []string{"component", "slot"} {
		for _, gap := range []string{" ", "\n\t", "\r\n  "} {
			t.Run(context+gap, func(t *testing.T) {
				body := " <b>x</b>" + gap + "<i>y</i> "
				src := "package p\ncomponent C(){" + body + "}\n"
				if context == "slot" {
					src = "package p\ncomponent C(){ <Card content={" + body + "}/> }\n"
				}
				f, err := ParseFile(token.NewFileSet(), "test.gsx", src, 0)
				if err != nil {
					t.Fatal(err)
				}
				var texts []string
				ast.Inspect(f, func(n ast.Node) bool {
					if text, ok := n.(*ast.Text); ok {
						texts = append(texts, text.Value)
					}
					return true
				})
				if len(texts) != 3 || texts[0] != "x" || texts[1] != gap || texts[2] != "y" {
					t.Fatalf("captured text = %#v, want x / %q / y (brace padding excluded)", texts, gap)
				}
			})
		}
	}
}
