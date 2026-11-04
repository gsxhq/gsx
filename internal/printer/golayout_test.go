package printer

import (
	"fmt"
	"go/token"
	"reflect"
	"strings"
	"testing"

	"github.com/gsxhq/gsx/ast"
	"github.com/gsxhq/gsx/internal/pretty"
	"github.com/gsxhq/gsx/parser"
)

func TestSourceGoIslands(t *testing.T) {
	tests := []struct{ name, src, want string }{
		{"declarations", "package p\nvar before=1\nvar n=wrap(<span>\n\tx\n</span>)\nvar after=2\n", "package p\n\nvar before = 1\nvar n=wrap(<span>\n\tx\n</span>)\nvar after = 2\n"},
		{"composite", "package p\nvar rows = []item{\n\t{label: \"a\", icon: <Icon>\n\t\t<title>x</title>\n\t</Icon>, page: P{}},\n}\n", "package p\n\nvar rows = []item{\n\t{label: \"a\", icon: <Icon>\n\t\t<title>x</title>\n\t</Icon>, page: P{}},\n}\n"},
		{"same line", "package p\nvar nodes=pair(<b>\n\tx\n</b>, <i>\n\ty\n</i>)\nvar outside=1+2\n", "package p\n\nvar nodes=pair(<b>\n\tx\n</b>, <i>\n\ty\n</i>)\nvar outside = 1 + 2\n"},
		{"authored parens", "package p\nvar n = (\n\t<span>\n\t\tx\n\t</span>\n)\nvar outside=1+2\n", "package p\n\nvar n = (\n\t<span>\n\t\tx\n\t</span>\n)\nvar outside = 1 + 2\n"},
		{"comments and semicolons", "package p\nfunc f(){\n\t// Keep the authored return line.\n\treturn wrap( /* inner */ <b>\n\t\tx\n\t</b>);\n}\nvar outside=3\n", "package p\n\nfunc f() {\n\t// Keep the authored return line.\n\treturn wrap( /* inner */ <b>\n\t\tx\n\t</b>)\n}\n\nvar outside = 3\n"},
		{"crlf", "package p\r\nvar n=wrap(<span>\r\n\tx\r\n</span>)\r\nvar outside=1\r\n", "package p\n\nvar n=wrap(<span>\r\n\tx\r\n</span>)\nvar outside = 1\n"},
		{"literal on own line", "package p\nvar n = wrap(\n\t<b>\n\t\tx\n\t</b>,\n)\n", "package p\n\nvar n = wrap(\n\t<b>\n\t\tx\n\t</b>,\n)\n"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			checkFormat(t, tt.src, tt.want)
			if !reflect.DeepEqual(contentAST(t, tt.src), contentAST(t, tt.want)) {
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
	want := "package p\n\nvar n=otherWrap(<span>\n\tedited\n</span>)\nvar other = 1\n"
	if b.String() != want {
		t.Fatalf("got:\n%s\nwant:\n%s", b.String(), want)
	}
	// The independent exact-content oracle must still notice a semantic edit.
	if strings.Contains(b.String(), "old") || !strings.Contains(b.String(), "edited") {
		t.Fatal("source snapshot replaced semantic Text")
	}
}

func TestSourceGoIslandNoLegacyFallbackOnBrokenConstraint(t *testing.T) {
	p := &printer{width: 80, tabWidth: pretty.DefaultTabWidth}
	src := "var n=<span>\nx\n</span>"
	parts, ok := splitGoParts(src)
	if !ok {
		t.Fatal("split")
	}
	_, _, protected, err := p.fmtSourceGoParts(parts, "short", goExprWrapper, "", StripSyntheticPackage)
	if err == nil || protected {
		t.Fatalf("invalid source geometry must fail explicitly: protected=%v err=%v", protected, err)
	}
}

func TestSourceGoManyIndependentIslands(t *testing.T) {
	var source strings.Builder
	source.WriteString("package p\nvar 一=1\nvar 丁=2\n")
	for i := 0; i < 64; i++ {
		fmt.Fprintf(&source, "var node%d=Wrap(<b>\n\tvalue %d\n</b>)\nvar outside%d=1+2\n", i, i, i)
	}
	output := fmtSource(t, source.String())
	for i := 0; i < 64; i++ {
		if !strings.Contains(output, fmt.Sprintf("var node%d=Wrap(<b>\n\tvalue %d\n</b>)", i, i)) {
			t.Fatalf("island %d lost its source layout", i)
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
	for _, src := range []string{"var n=js`foo();\nbar();`", "var n=css`color:red;\nwidth:2px;`"} {
		parts, ok := splitGoParts(src)
		if !ok {
			t.Fatal("split")
		}
		p := &printer{width: 80, tabWidth: 2}
		_, _, protected, err := p.fmtSourceGoParts(parts, src, goExprWrapper, "", StripSyntheticPackage)
		if err != nil || protected {
			t.Fatalf("raw-language operand acquired an HTML source constraint: %v %v", protected, err)
		}
	}
	// A markup operand containing a separately formatted script is conservatively
	// source-aligned: its fallback raw body can still contain fixed literal data.
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
