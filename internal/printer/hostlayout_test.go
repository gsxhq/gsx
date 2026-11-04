package printer

import (
	"go/token"
	"reflect"
	"strings"
	"testing"

	"github.com/gsxhq/gsx/ast"
	"github.com/gsxhq/gsx/parser"
)

func TestSourceGSXHosts(t *testing.T) {
	literal := "Wrap(<b>\n\t\tx\n\t</b>)"
	hosts := map[string]string{
		"conditional":           "<Box { if " + literal + " != nil { disabled } }/>",
		"composed":              "<Box class={ " + literal + ", \"other\" }/>",
		"guard":                 "<Box class={ \"active\": " + literal + " != nil }/>",
		"literal guard":         "<Box class={ f`active`: " + literal + " != nil }/>",
		"value if":              "<Box class={ if " + literal + " != nil { \"x\" } else { \"y\" } }/>",
		"value switch":          "<Box class={ switch " + literal + " { case nil: \"x\" default: \"y\" } }/>",
		"attribute switch":      "<Box { switch " + literal + " { case nil: disabled default: hidden } }/>",
		"expr":                  "<Box node={" + literal + "}/>",
		"spread":                "<Box { " + literal + "... }/>",
		"if":                    "{if " + literal + " != nil { <i/> } }",
		"for":                   "{for " + literal + " != nil { <i/> } }",
		"switch":                "{switch " + literal + " {case nil: <i/>} }",
		"interp":                "{" + literal + "}",
		"block":                 "{{ n:=" + literal + " }}",
		"preceding Go":          "<Box class={ someFunc( 1+2 ), " + literal + " }/>",
		"preceding conditional": "<Box { if ok==true { class={" + literal + "} } }/>",
		"ordered":               "<Box attrs={{ \"node\": " + literal + ", \"x\":1+2 }}/>",
		"stage multiple":        "{ x |> foo( 1+2,Wrap( <b>\nx\n</b> ), <i>\ny\n</i>, `raw\ntext` ) }",
		"stage interp":          "{ x |> foo(<b>\nx\n</b>) }",
		"stage expr":            "<Box node={ x |> foo(<b>\nx\n</b>) }/>",
		"stage composed":        "<Box class={ x |> foo(<b>\nx\n</b>) }/>",
		"stage arm":             "<Box class={ if ok { x |> foo(<b>\nx\n</b>) } else { \"x\" } }/>",
		"stage spread":          "<Box { (x |> foo(<b>\nx\n</b>))... }/>",
		"stage embedded attr":   "<Box node={f`x` |> foo(<b>\nx\n</b>)}/>",
		"stage embedded body":   "{f`x` |> foo(<b>\nx\n</b>)}",
		"braced comment":        "<Box { /* comment */ } node={" + literal + "}/>",
		"comment group":         "<Box { /*a*/ /* b */ } node={" + literal + "}/>",
		"empty comment group":   "<Box { ; } node={" + literal + "}/>",
		"dense Go comments":     "<Box class={ /*a*//*b*/" + literal + ":ok }/>",
		"ordinary Go comments":  "<Box class={ \"base\", /*before*/ " + literal + " /*after*/:ok }/>",
		"generic tag":           "<Box[ A,B ] node={" + literal + "}/>",
		"escaped ordered key":   "<Box attrs={{ \"\\u0061\": " + literal + " }}/>",
		"raw ordered key":       "<Box attrs={{ `a`: " + literal + " }}/>",
		"body block comment":    "<div>{/*c*/}<b>\n x\n</b></div>",
		"body line comment":     "<div>{// c\n}<b>\n x\n</b></div>",
		"block nested attr":     "{{ n:=Wrap(<Other node={" + literal + "}/>) }}",
		"body comment group":    "<div>{/*a*/ /* b */}<b>\nx\n</b></div>",
		"nested":                "<Box node={Wrap(<Other class={ \"active\": " + literal + " != nil }/> )}/>",
	}
	for name, host := range hosts {
		t.Run(name, func(t *testing.T) {
			src := "package p\ncomponent C(){ " + host + " }\n"
			for _, width := range []int{20, 40, 80, 120} {
				got := fmtSourceWidth(t, src, width)
				if !reflect.DeepEqual(contentAST(t, src), contentAST(t, got)) {
					t.Fatal("literal AST changed")
				}
				if again := fmtSourceWidth(t, got, width); again != got {
					t.Fatalf("not idempotent: %q", again)
				}
			}
		})
	}
}

func TestSourceGSXHostUsesCurrentSemanticFields(t *testing.T) {
	src := "package p\ncomponent C(){ <Box[ A,B ] class={ Wrap(<b>\n  old\n</b>), \"other\" }/> }\n"
	f, err := parser.ParseFile(token.NewFileSet(), "test.gsx", src, 0)
	if err != nil {
		t.Fatal(err)
	}
	comp := f.Decls[0].(*ast.Component)
	comp.Name = "RenamedComponent"
	element := comp.Body[0].(*ast.Element)
	element.Tag = "LongerBox"
	element.TypeArgs = "LongerA,OtherB"
	attr := element.Attrs[0].(*ast.ComposedAttr)
	attr.Name = "style"
	attr.Parts[0].Expr = strings.ReplaceAll(attr.Parts[0].Expr, "Wrap(", "OtherWrapper(")
	attr.Parts[0].Expr = strings.ReplaceAll(attr.Parts[0].Expr, "old", "edited")
	var out strings.Builder
	if err := Fprint(&out, f, 80, 2); err != nil {
		t.Fatal(err)
	}
	for _, fragment := range []string{"component RenamedComponent() {", "LongerBox[LongerA, OtherB]", "style={", "OtherWrapper(", "edited"} {
		if !strings.Contains(out.String(), fragment) {
			t.Fatalf("current AST field missing %q in %s", fragment, out.String())
		}
	}
}

func TestSourceComponentIndependentHeader(t *testing.T) {
	src := "package p\ncomponent C( x string ) {\n    <div>\n      x\n    </div>\n}\n"
	want := "package p\n\ncomponent C(x string) {\n    <div>\n      x\n    </div>\n}\n"
	checkFormat(t, src, want)
}

func TestSourceControlIndependentHeaders(t *testing.T) {
	src := "package p\ncomponent C() {\n\t{if ok==true {\n\t\t<b>\n\t\t\tx\n\t\t</b>\n\t} }\n\t{for i:=0;i<2;i++ {\n\t\t<b>\n\t\t\tx\n\t\t</b>\n\t} }\n\t{switch n+1 {\n\tcase 1+2:\n\t\t<b>\n\t\t\tx\n\t\t</b>\n\t} }\n}\n"
	want := "package p\n\ncomponent C() {\n\t{ if ok == true {\n\t\t<b>\n\t\t\tx\n\t\t</b>\n\t} }\n\t{ for i := 0; i < 2; i++ {\n\t\t<b>\n\t\t\tx\n\t\t</b>\n\t} }\n\t{ switch n + 1 {\n\tcase 1 + 2:\n\t\t<b>\n\t\t\tx\n\t\t</b>\n\t} }\n}\n"
	checkFormat(t, src, want)
}

func TestSourceBodyCommentUsesCurrentText(t *testing.T) {
	src := "package p\ncomponent C(){ <div>{/*c*/}<b>\n x\n</b></div> }\n"
	f, err := parser.ParseFile(token.NewFileSet(), "test.gsx", src, 0)
	if err != nil {
		t.Fatal(err)
	}
	ast.Inspect(f, func(n ast.Node) bool {
		if comment, ok := n.(*ast.Comment); ok {
			comment.Text = "edited"
		}
		return true
	})
	var out strings.Builder
	if err := Fprint(&out, f, 80, 2); err != nil {
		t.Fatal(err)
	}
	want := "package p\n\ncomponent C() { <div>{/*edited*/}<b>\n x\n</b></div> }\n"
	if out.String() != want {
		t.Fatalf("got %q, want %q", out.String(), want)
	}
}

func TestCanonicalGoWithLiteralNewlines(t *testing.T) {
	src := "package p\nvar n=Wrap(<b>\n\tx\n</b>)\nfunc f(){return Wrap(<b>\n\ty\n</b>)}\n"
	want := "package p\n\nvar n = Wrap(<b>\n\tx\n</b>)\n\nfunc f() { return Wrap(<b>\n\ty\n</b>) }\n"
	checkFormat(t, src, want)
}

func TestPipelineArgumentsFormatAroundMarkup(t *testing.T) {
	src := "package p\ncomponent C() {\n\t{x |> foo( 1+2,Wrap( <b>\nx\n</b> ) )}\n}\n"
	want := "package p\n\ncomponent C() {\n\t{ x |> foo(1+2, Wrap(<b>\nx\n</b>)) }\n}\n"
	checkFormat(t, src, want)
}

// Literal delimiters use original field boundaries while edited hole and
// pipeline spellings print from the semantic AST.
func TestLiteralCursorUsesEditedHoleAndStages(t *testing.T) {
	src := "package p\ncomponent C(){\n{f`start\n@{value |> inner(1+2)}\nend` |> outer(3+4)}\n}\n"
	parse := func(source string) *ast.File {
		f, err := parser.ParseFile(token.NewFileSet(), "literal.gsx", source, 0)
		if err != nil {
			t.Fatal(err)
		}
		return f
	}
	literal := func(f *ast.File) *ast.EmbeddedInterp {
		var found *ast.EmbeddedInterp
		ast.Inspect(f, func(n ast.Node) bool {
			if v, ok := n.(*ast.EmbeddedInterp); ok && v.Lang == ast.EmbeddedText {
				found = v
				return false
			}
			return true
		})
		if found == nil {
			t.Fatal("missing text literal")
		}
		return found
	}
	text := func(l *ast.EmbeddedInterp) []string {
		var values []string
		for _, segment := range l.Segments {
			if v, ok := segment.(*ast.Text); ok {
				values = append(values, v.Value)
			}
		}
		return values
	}
	f := parse(src)
	l := literal(f)
	wantText := text(l)
	for _, segment := range l.Segments {
		if hole, ok := segment.(*ast.Interp); ok {
			hole.Expr = "longerValue"
			hole.Stages[0].Name = "innerEdited"
			hole.Stages[0].Args = "10+20"
		}
	}
	l.Stages[0].Name = "outerEdited"
	l.Stages[0].Args = "30+40"
	var out strings.Builder
	if err := Fprint(&out, f, 80, 2); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"longerValue", "innerEdited(10 + 20)", "outerEdited(30 + 40)"} {
		if !strings.Contains(out.String(), want) {
			t.Fatalf("edited field %q missing in %s", want, out.String())
		}
	}
	if !reflect.DeepEqual(text(literal(parse(out.String()))), wantText) {
		t.Fatal("literal Text changed")
	}
	if again := fmtSource(t, out.String()); again != out.String() {
		t.Fatal("not idempotent")
	}
}

func TestTagSyntaxAlignsToAuthoredRoot(t *testing.T) {
	for _, prefix := range []string{"", "\t", "   "} {
		column := len(strings.ReplaceAll(prefix, "\t", "  "))
		indent := func(n int) string { return strings.Repeat("\t", n/2) + strings.Repeat(" ", n%2) }
		src := "package p\ncomponent C() {\n" + prefix + "<div\nclass=\"x\"\n>\n" + prefix + "\tcontent\n" + prefix + "</div>\n}\n"
		want := "package p\n\ncomponent C() {\n" + prefix + "<div\n" + indent(column+2) + "class=\"x\"\n" + indent(column) + ">\n" + prefix + "\tcontent\n" + prefix + "</div>\n}\n"
		checkFormat(t, src, want)
	}
}

func TestNestedControlSyntaxUsesPhysicalIndent(t *testing.T) {
	src := "package p\ncomponent C(ok bool) {\n<section>\n\t{if ok {\n\t\t\t<p>yes</p>\n\t} }\n</section>\n}\n"
	want := "package p\n\ncomponent C(ok bool) {\n<section>\n\t{ if ok {\n\t\t<p>yes</p>\n\t} }\n</section>\n}\n"
	checkFormat(t, src, want)
}
