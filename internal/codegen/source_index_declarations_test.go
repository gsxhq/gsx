package codegen

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/gsxhq/gsx/internal/sourceintel"
)

func TestSourceIndexIncludesGoWithElementsDeclarations(t *testing.T) {
	const source = `package page

func Before() {}

func Nested() any {
	node := <div/>
	return node
}

var Inline = <span/>

func After() {}
`
	dir, module := openTestModule(t, map[string]string{"page.gsx": source})
	path := filepath.Join(dir, "page.gsx")
	result, err := module.Package(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Diags) != 0 {
		t.Fatalf("unexpected diagnostics: %+v", result.Diags)
	}

	declarations := result.SourceIndex.Declarations(path)
	want := map[string]sourceintel.Span{
		"Before": sourceSpan(path, source, "func Before() {}"),
		"Nested": sourceSpan(path, source, "func Nested() any {\n\tnode := <div/>\n\treturn node\n}"),
		"Inline": sourceSpan(path, source, "var Inline = <span/>"),
		"After":  sourceSpan(path, source, "func After() {}"),
	}
	for _, declaration := range declarations {
		wantSpan, ok := want[declaration.Name]
		if !ok {
			continue
		}
		if declaration.DeclSpan != wantSpan {
			t.Errorf("%s declaration span = %+v, want %+v", declaration.Name, declaration.DeclSpan, wantSpan)
		}
		delete(want, declaration.Name)
	}
	if len(want) != 0 {
		t.Fatalf("SourceIndex missing GoWithElements declarations: %v; got %+v", want, declarations)
	}
}

func sourceSpan(path, source, declaration string) sourceintel.Span {
	start := strings.Index(source, declaration)
	return sourceintel.Span{Path: path, Start: start, End: start + len(declaration)}
}
