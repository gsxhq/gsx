package parser

import (
	"go/token"
	"slices"
	"strings"
	"testing"

	"github.com/gsxhq/gsx/ast"
)

func TestParseUnicodeComponentAndPropIdentifiers(t *testing.T) {
	src := `package p

component Δέλτα(μήνυμα string, label数据٣ string, _κρυφό bool) {
	<section data-标签={label数据٣}>{μήνυμα}</section>
}

component Page(μήνυμα string) {
	<Δέλτα μήνυμα={μήνυμα} label数据٣="值" _κρυφό />
	<组件.卡片 标题={μήνυμα} />
}
`
	fset := token.NewFileSet()
	f, err := ParseFile(fset, "unicode.gsx", []byte(src), 0)
	if err != nil {
		t.Fatalf("ParseFile: %v", err)
	}
	if len(f.Decls) != 2 {
		t.Fatalf("declarations = %d, want 2", len(f.Decls))
	}
	decl := f.Decls[0].(*ast.Component)
	if decl.Name != "Δέλτα" || decl.Params != "μήνυμα string, label数据٣ string, _κρυφό bool" {
		t.Fatalf("Unicode declaration = name %q params %q", decl.Name, decl.Params)
	}
	section := decl.Body[0].(*ast.Element)
	if section.Tag != "section" || len(section.Attrs) != 1 || section.Attrs[0].(*ast.ExprAttr).Name != "data-标签" {
		t.Fatalf("Unicode extended HTML attribute was not retained: %#v", section)
	}

	page := f.Decls[1].(*ast.Component)
	local := page.Body[0].(*ast.Element)
	if local.Tag != "Δέλτα" {
		t.Fatalf("local call tag = %q, want Δέλτα", local.Tag)
	}
	if got := attrNames(local.Attrs); !slices.Equal(got, []string{"μήνυμα", "label数据٣", "_κρυφό"}) {
		t.Fatalf("local call attrs = %q", got)
	}
	qualified := page.Body[1].(*ast.Element)
	if qualified.Tag != "组件.卡片" || !slices.Equal(attrNames(qualified.Attrs), []string{"标题"}) {
		t.Fatalf("qualified Unicode call = tag %q attrs %q", qualified.Tag, attrNames(qualified.Attrs))
	}
}

func TestParseUnicodeComponentCloseTag(t *testing.T) {
	f, err := ParseFile(token.NewFileSet(), "unicode.gsx", `package p
component Δ() { <Δ><span /></Δ> }
`, 0)
	if err != nil {
		t.Fatalf("ParseFile: %v", err)
	}
	component := f.Decls[0].(*ast.Component)
	el := component.Body[0].(*ast.Element)
	if el.Tag != "Δ" || el.CloseNamePos == token.NoPos {
		t.Fatalf("element = %#v, want matched Unicode open/close tag", el)
	}
}

func TestUnicodePipelineStageNamesUseGoLexicalIdentifierRules(t *testing.T) {
	_, stages, err := parsePipe("value |> if |> φίλτρο |> 包.default", token.Pos(100))
	if err != nil {
		t.Fatalf("parsePipe Unicode stages: %v", err)
	}
	if len(stages) != 3 || stages[0].Name != "if" || stages[1].Name != "φίλτρο" || stages[2].Name != "包.default" {
		t.Fatalf("stages = %#v", stages)
	}

	for _, src := range []string{"value |> ٣filter", "value |> Cafe\u0301"} {
		t.Run(src, func(t *testing.T) {
			if _, _, err := parsePipe(src, token.NoPos); err == nil {
				t.Fatalf("parsePipe(%q) succeeded; stage is not a Go identifier", src)
			}
		})
	}
}

func TestUnicodeIdentifierKeywordBoundaries(t *testing.T) {
	for _, src := range []string{"ifπ", "switch数据"} {
		if got := leadingKeyword(src); got != "" {
			t.Errorf("leadingKeyword(%q) = %q, want no keyword prefix", src, got)
		}
	}
	for src, want := range map[string]string{"if π": "if", "switch 数据": "switch"} {
		if got := leadingKeyword(src); got != want {
			t.Errorf("leadingKeyword(%q) = %q, want %q", src, got, want)
		}
	}

	p := testParser("elseπ else ")
	if p.atWord("else") {
		t.Fatal("else prefix before Unicode identifier continuation matched as keyword")
	}
	p.i = len("elseπ ")
	if !p.atWord("else") {
		t.Fatal("standalone else did not match")
	}
}

func TestComponentNameMustBeGoIdentifier(t *testing.T) {
	for _, name := range []string{"if", "٣Card", "Cafe\u0301"} {
		t.Run(name, func(t *testing.T) {
			src := "package p\ncomponent " + name + "() { <p /> }\n"
			if _, err := ParseFile(token.NewFileSet(), "invalid.gsx", src, 0); err == nil {
				t.Fatalf("component name %q parsed; want Go-identifier rejection", name)
			}
		})
	}
}

func TestParseFileRejectsInvalidUTF8(t *testing.T) {
	prefix := []byte("package p\ncomponent C() { <C ")
	src := append(append([]byte(nil), prefix...), 0xff)
	src = append(src, []byte("=\"x\" /> }\n")...)
	fset := token.NewFileSet()
	_, errs := ParseFileWithClassifier(fset, "invalid.gsx", src, 0, nil)
	if len(errs) != 1 || !strings.Contains(errs[0].Msg, "UTF-8") {
		t.Fatalf("errors = %#v, want one invalid UTF-8 error", errs)
	}
	if got := fset.Position(errs[0].Pos).Offset; got != len(prefix) {
		t.Fatalf("invalid UTF-8 offset = %d, want %d", got, len(prefix))
	}
}

func attrNames(attrs []ast.Attr) []string {
	names := make([]string, 0, len(attrs))
	for _, attr := range attrs {
		switch attr := attr.(type) {
		case *ast.StaticAttr:
			names = append(names, attr.Name)
		case *ast.ExprAttr:
			names = append(names, attr.Name)
		case *ast.BoolAttr:
			names = append(names, attr.Name)
		default:
			names = append(names, "<unexpected>")
		}
	}
	return names
}
