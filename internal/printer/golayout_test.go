package printer

import (
	"fmt"
	"go/token"
	"reflect"
	"strings"
	"testing"

	"github.com/gsxhq/gsx/ast"
	"github.com/gsxhq/gsx/parser"
)

func TestSourceGoIslands(t *testing.T) {
	tests := []struct{ name, src string }{
		{"declarations", "package p\nvar before=1\nvar n=wrap(<span>\n\tx\n</span>)\nvar after=2\n"},
		{"composite", "package p\nvar rows = []item{\n\t{label: \"a\", icon: <Icon>\n\t\t<title>x</title>\n\t</Icon>, page: P{}},\n}\n"},
		{"same line", "package p\nvar nodes=pair(<b>\n\tx\n</b>, <i>\n\ty\n</i>)\nvar outside=1+2\n"},
		{"authored parens", "package p\nvar n = (\n\t<span>\n\t\tx\n\t</span>\n)\nvar outside=1+2\n"},
		{"comments and semicolons", "package p\nfunc f(){\n\t// Keep the authored return line.\n\treturn wrap( /* inner */ <b>\n\t\tx\n\t</b>);\n}\nvar outside=3\n"},
		{"crlf", "package p\r\nvar n=wrap(<span>\r\n\tx\r\n</span>)\r\nvar outside=1\r\n"},
		{"literal on own line", "package p\nvar n = wrap(\n\t<b>\n\t\tx\n\t</b>,\n)\n"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := fmtSource(t, tt.src)
			if again := fmtSource(t, got); again != got {
				t.Fatal("not idempotent")
			}
			if !reflect.DeepEqual(contentAST(t, tt.src), contentAST(t, got)) {
				t.Fatal("exact markup or Go structure changed")
			}
		})
	}
}

func TestSourceGoIslandUsesCurrentAST(t *testing.T) {
	src := "package p\nvar n=wrap(<span>\n\told\n</span>)\nvar other=1\n"
	f, err := parser.ParseFile(token.NewFileSet(), "test.gsx", src, 0)
	if err != nil {
		t.Fatal(err)
	}
	ast.Inspect(f, func(n ast.Node) bool {
		if text, ok := n.(*ast.Text); ok {
			text.Value = strings.ReplaceAll(text.Value, "old", "edited")
		}
		return true
	})
	v := f.Decls[0].(*ast.GoWithElements)
	gt := v.Parts[0].(ast.GoText)
	gt.Src = strings.ReplaceAll(gt.Src, "wrap(", "otherWrap(")
	v.Parts[0] = gt
	var b strings.Builder
	if err := Fprint(&b, f, 80, 2); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(b.String(), "otherWrap(") || strings.Contains(b.String(), "var n=") {
		t.Fatalf("Go syntax did not format: %s", b.String())
	}
	// The independent exact-content oracle must still notice a semantic edit.
	if strings.Contains(b.String(), "old") || !strings.Contains(b.String(), "edited") {
		t.Fatal("source snapshot replaced semantic Text")
	}
}

func TestSourceGoManyIndependentIslands(t *testing.T) {
	var source strings.Builder
	source.WriteString("package p\nvar 一=1\nvar 丁=2\n")
	for i := range 64 {
		fmt.Fprintf(&source, "var node%d=Wrap(<b>\n\tvalue %d\n</b>)\nvar outside%d=1+2\n", i, i, i)
	}
	output := fmtSource(t, source.String())
	for i := range 64 {
		if !strings.Contains(output, fmt.Sprintf("var node%d =", i)) {
			t.Fatalf("island %d Go declaration did not format", i)
		}
		if !strings.Contains(output, fmt.Sprintf("var outside%d = 1 + 2\n", i)) {
			t.Fatalf("independent declaration %d was not formatted", i)
		}
	}
	if again := fmtSource(t, output); again != output {
		t.Fatal("not idempotent")
	}
}

func TestSourceGoRawLanguageOperandsKeepSeparateContract(t *testing.T) {
	// A markup operand containing a separately formatted script is conservatively
	// literal-bearing: its fallback raw body can still contain fixed text.
	// Classifying it must not invoke a formatter speculatively.
	src := "var n=<script>\nfoo();\n</script>"
	parts, ok := splitGoParts(src)
	if !ok {
		t.Fatal("split")
	}
	calls := 0
	p := &printer{width: 80, tabWidth: 2, jsFmt: func(src []byte) ([]byte, error) { calls++; return src, nil }}
	for _, part := range parts {
		p.anchoredGoMarkup(part)
	}
	if calls != 0 {
		t.Fatal("source classification invoked a formatter")
	}
}
