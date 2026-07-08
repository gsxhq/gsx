// parser/file_test.go
package parser

import (
	"go/token"
	"reflect"
	"testing"

	"github.com/gsxhq/gsx/ast"
)

func TestParseFile(t *testing.T) {
	src := `package views

import "github.com/gsxhq/gsx"

type Item struct{ Name string }

component Card(title string) {
	<section>{title}</section>
}

func helper() string { return "x" }

component Spinner() {
	<svg></svg>
}
`
	fset := token.NewFileSet()
	f, err := ParseFile(fset, "test.gsx", src, 0)
	if err != nil {
		t.Fatal(err)
	}
	if f.Package != "views" {
		t.Fatalf("package = %q", f.Package)
	}
	var comps []string
	var chunks int
	for _, d := range f.Decls {
		switch v := d.(type) {
		case *ast.Component:
			comps = append(comps, v.Name)
		case *ast.GoChunk:
			chunks++
		}
	}
	if len(comps) != 2 || comps[0] != "Card" || comps[1] != "Spinner" {
		t.Fatalf("components = %v", comps)
	}
	if chunks == 0 {
		t.Fatalf("expected Go chunks (import/type/func) to be captured")
	}
}

func TestMultiComponentWithApostrophe(t *testing.T) {
	// B3: an apostrophe on the SAME line as the body-closing brace must not cause
	// the SECOND component to be dropped. (The apostrophe opens a rune literal that
	// the old whole-file scan ran into; when the `}` is on that same line it gets
	// swallowed, the depth never returns to 0, and B is missed. With the `}` on a
	// separate line the rune terminates at the newline first — which is why the
	// braces must be on the apostrophe's line to exercise the regression.)
	src := "package p\n" +
		"component A() { <p>Jack's profile</p> }\n" +
		"component B() { <span>ok</span> }\n"
	file, err := ParseFile(token.NewFileSet(), "t.gsx", src, 0)
	if err != nil {
		t.Fatalf("parse error: %v", err)
	}
	var names []string
	for _, d := range file.Decls {
		if c, ok := d.(*ast.Component); ok {
			names = append(names, c.Name)
		}
	}
	if len(names) != 2 || names[0] != "A" || names[1] != "B" {
		t.Fatalf("component names = %v, want [A B]", names)
	}
}

func TestGoDeclsBetweenComponents(t *testing.T) {
	// Interleaved Go funcs/types between components still split correctly.
	src := "package p\n" +
		"type T struct{ X int }\n" +
		"component A() {\n\t<a/>\n}\n" +
		"func helper() string { return \"x\" }\n" +
		"component B() {\n\t<b/>\n}\n"
	file, err := ParseFile(token.NewFileSet(), "t.gsx", src, 0)
	if err != nil {
		t.Fatalf("parse error: %v", err)
	}
	// Expect: GoChunk(type T), Component A, GoChunk(func helper), Component B
	var kinds []string
	for _, d := range file.Decls {
		switch d.(type) {
		case *ast.GoChunk:
			kinds = append(kinds, "go")
		case *ast.Component:
			kinds = append(kinds, "comp")
		}
	}
	want := []string{"go", "comp", "go", "comp"}
	if !reflect.DeepEqual(kinds, want) {
		t.Fatalf("decl kinds = %v, want %v", kinds, want)
	}
}

func TestGoKeywordWordInElementProse(t *testing.T) {
	// The English word "component" inside an element literal's prose is not Go.
	// nextTopLevelComponent must skip element spans and resume Go tokenization
	// past them; otherwise the word lexes as an IDENT at depth 0, is mistaken
	// for a component declaration, and splits the file mid-element.
	cases := map[string]string{
		"var, multi-line": "package p\n" +
			"var X = <div>\n\t<h1>Test</h1>\n\t<p>This is a test component.</p>\n</div>\n" +
			"component C() { <b>hi</b> }\n",
		"func body": "package p\n" +
			"func f() {\n\tx := <p>a component here</p>\n\t_ = x\n}\n" +
			"component C() { <b>hi</b> }\n",
		"interp sibling": "package p\n" +
			"var n = 1\n" +
			"var X = <p>{ n } component</p>\n" +
			"component C() { <b>hi</b> }\n",
		"attr hole": "package p\n" +
			"var c = \"x\"\n" +
			"var X = <p class={ c }>component</p>\n" +
			"component C() { <b>hi</b> }\n",
		"fragment": "package p\n" +
			"var X = <><p>component</p></>\n" +
			"component C() { <b>hi</b> }\n",
		// A bare `<` that is not a tag must not be treated as an element mark.
		"less-than": "package p\n" +
			"var Y = 1 < 2\n" +
			"component C() { <b>hi</b> }\n",
	}
	for name, src := range cases {
		t.Run(name, func(t *testing.T) {
			file, err := ParseFile(token.NewFileSet(), "t.gsx", src, 0)
			if err != nil {
				t.Fatalf("parse error: %v", err)
			}
			var names []string
			for _, d := range file.Decls {
				if c, ok := d.(*ast.Component); ok {
					names = append(names, c.Name)
				}
			}
			if len(names) != 1 || names[0] != "C" {
				t.Fatalf("component names = %v, want [C]", names)
			}
		})
	}
}
