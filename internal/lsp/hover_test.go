package lsp

import (
	"go/types"
	"path/filepath"
	"strings"
	"testing"

	gsxast "github.com/gsxhq/gsx/ast"
)

func TestFactoryComponentAttributeHoverUsesStaticSignature(t *testing.T) {
	const sameFactory = `package page

import "github.com/gsxhq/gsx"

type BadgeFactory func(name string) gsx.Node

func factory() BadgeFactory {
	return func(closureName string) gsx.Node { return nil }
}

var Badge = factory()
`
	const importedFactory = `package widgets

import "github.com/gsxhq/gsx"

type BadgeFactory = func(name string) gsx.Node

func factory() BadgeFactory {
	return func(closureName string) gsx.Node { return nil }
}

var Badge = factory()
`
	tests := []struct {
		name          string
		pageSource    string
		factorySource string
		factoryFile   string
		prefix        string
	}{
		{
			name:          "same package named type",
			pageSource:    "package page\ncomponent Page() { <Badge name=\"ok\"/> }\n",
			factorySource: sameFactory,
			factoryFile:   "page/factory.go",
			prefix:        "type BadgeFactory func(",
		},
		{
			name:          "imported alias type",
			pageSource:    "package page\nimport \"example.com/app/widgets\"\ncomponent Page() { <widgets.Badge name=\"ok\"/> }\n",
			factorySource: importedFactory,
			factoryFile:   "widgets/factory.go",
			prefix:        "type BadgeFactory = func(",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			pkg, path := analyzedLSPModule(t, map[string]string{
				"page/page.gsx":  test.pageSource,
				test.factoryFile: test.factorySource,
			}, "page/page.gsx")
			cursor, ok := componentAttrAtOffset(pkg, path, strings.Index(test.pageSource, `name="`))
			if !ok {
				t.Fatal("factory component attribute has no hover fact")
			}
			param := cursor.param.Var
			if param == nil {
				param = cursor.param.Origin
			}
			if param == nil {
				t.Fatal("factory component hover fact has no static parameter")
			}
			if got := cursor.param.Name + " " + types.TypeString(param.Type(), qualifierFor(pkg)); got != "name string" {
				t.Fatalf("hover declaration = %q, want %q", got, "name string")
			}
			position := pkg.Fset.Position(cursor.param.Origin.Pos())
			wantOffset := strings.Index(test.factorySource, test.prefix) + len(test.prefix)
			if filepath.Base(position.Filename) != "factory.go" || position.Offset != wantOffset {
				t.Fatalf("hover origin = %+v, want factory.go offset %d", position, wantOffset)
			}
		})
	}
}

func TestRenderComponentSig(t *testing.T) {
	fn := &gsxast.Component{Name: "Card", Params: "title string"}
	if got, want := renderComponentSig(fn), "component Card(title string)"; got != want {
		t.Errorf("func component: got %q, want %q", got, want)
	}
	method := &gsxast.Component{Recv: "(p UsersPage)", Name: "Row", Params: "u User"}
	if got, want := renderComponentSig(method), "component (p UsersPage) Row(u User)"; got != want {
		t.Errorf("method component: got %q, want %q", got, want)
	}
	nullary := &gsxast.Component{Name: "Page"}
	if got, want := renderComponentSig(nullary), "component Page()"; got != want {
		t.Errorf("nullary component: got %q, want %q", got, want)
	}
}
